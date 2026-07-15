package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/watchbell/watchbell/internal/checker"
	"github.com/watchbell/watchbell/internal/model"
	"github.com/watchbell/watchbell/internal/notifier"
	"github.com/watchbell/watchbell/internal/rule"
	"github.com/watchbell/watchbell/internal/store"
	"github.com/watchbell/watchbell/internal/templatex"
)

const maxDeliveryAttempts = 3

var ErrAlreadyRunning = errors.New("monitor is already running")

type Options struct {
	Tick        time.Duration
	WorkerCount int
	Logger      *slog.Logger
}

type Scheduler struct {
	store       *store.Store
	checkers    checker.Registry
	notifiers   notifier.Registry
	tick        time.Duration
	workerCount int
	logger      *slog.Logger
	startedAt   time.Time
	lastTickAt  *time.Time
	inFlight    map[int64]struct{}
	maintenance bool
	mu          sync.Mutex
}

func New(store *store.Store, checkers checker.Registry, notifiers notifier.Registry, options Options) *Scheduler {
	if options.Tick <= 0 {
		options.Tick = 10 * time.Second
	}
	if options.WorkerCount <= 0 {
		options.WorkerCount = 4
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	return &Scheduler{
		store:       store,
		checkers:    checkers,
		notifiers:   notifiers,
		tick:        options.Tick,
		workerCount: options.WorkerCount,
		logger:      options.Logger,
		startedAt:   time.Now().UTC(),
		inFlight:    map[int64]struct{}{},
	}
}

func (s *Scheduler) Plugins() []model.MonitorPlugin {
	return s.checkers.Plugins()
}

func (s *Scheduler) HasPlugin(pluginID string) bool {
	return s.checkers.Has(pluginID)
}

func (s *Scheduler) Health() model.SchedulerHealth {
	s.mu.Lock()
	defer s.mu.Unlock()
	var lastTick *time.Time
	if s.lastTickAt != nil {
		value := *s.lastTickAt
		lastTick = &value
	}
	return model.SchedulerHealth{
		StartedAt: s.startedAt, LastTickAt: lastTick,
		WorkerCount: s.workerCount, InFlight: len(s.inFlight),
	}
}

func (s *Scheduler) Start(ctx context.Context) {
	ticker := time.NewTicker(s.tick)
	defer ticker.Stop()
	sem := make(chan struct{}, s.workerCount)
	s.runTick(ctx, sem)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runTick(ctx, sem)
		}
	}
}

func (s *Scheduler) runTick(ctx context.Context, sem chan struct{}) {
	now := time.Now().UTC()
	s.mu.Lock()
	s.lastTickAt = &now
	s.mu.Unlock()
	s.startMaintenance(ctx)
	s.enqueueDue(ctx, sem)
}

func (s *Scheduler) RunOnce(ctx context.Context, monitorID int64) error {
	if !s.markInFlight(monitorID) {
		return ErrAlreadyRunning
	}
	defer s.clearInFlight(monitorID)
	monitor, err := s.store.GetMonitor(ctx, monitorID)
	if err != nil {
		return err
	}
	return s.processMonitor(ctx, monitor, "manual")
}

func (s *Scheduler) TestChannel(ctx context.Context, channelID int64) (model.NotificationAttempt, error) {
	channel, err := s.store.GetNotifyChannel(ctx, channelID)
	if err != nil {
		return model.NotificationAttempt{}, err
	}
	message := notifier.Message{
		Subject: "WatchBell 测试通知",
		Body:    "这是一条来自 WatchBell 的测试通知。",
		Data: map[string]any{
			"event": map[string]any{"type": "test", "time": time.Now().UTC().Format(time.RFC3339Nano)},
		},
	}
	return s.sendAndRecord(ctx, channel, message, model.NotificationAttemptInput{
		ChannelID: int64Ptr(channel.ID), ChannelName: channel.Name, ChannelType: channel.Type,
		Kind: "test", AttemptNo: 1,
	})
}

func (s *Scheduler) RetryAttempt(ctx context.Context, attemptID int64) (model.NotificationAttempt, error) {
	attempt, err := s.store.GetNotificationAttempt(ctx, attemptID)
	if err != nil {
		return model.NotificationAttempt{}, err
	}
	if attempt.Status != "failed" {
		return model.NotificationAttempt{}, fmt.Errorf("only failed attempts can be retried")
	}
	if err := s.store.CancelNotificationRetry(ctx, attempt.ID); err != nil {
		return model.NotificationAttempt{}, err
	}
	return s.retryAttempt(ctx, attempt)
}

func (s *Scheduler) enqueueDue(ctx context.Context, sem chan struct{}) {
	monitors, err := s.store.ListEnabledMonitors(ctx)
	if err != nil {
		s.logger.Error("list enabled monitors", "error", err)
		return
	}
	now := time.Now().UTC()
	for _, monitor := range monitors {
		if !s.isDue(monitor, now) || !s.markInFlight(monitor.ID) {
			continue
		}
		sem <- struct{}{}
		go func(item model.Monitor) {
			defer func() {
				<-sem
				s.clearInFlight(item.ID)
			}()
			if err := s.processMonitor(ctx, item, "scheduled"); err != nil {
				s.logger.Error("process monitor", "monitor_id", item.ID, "error", err)
			}
		}(monitor)
	}
}

func (s *Scheduler) processMonitor(ctx context.Context, monitor model.Monitor, trigger string) error {
	started := time.Now().UTC()
	run, err := s.store.CreateCheckRun(ctx, monitor, trigger, s.redactedMonitorConfig(monitor))
	if err != nil {
		return err
	}
	checkerImpl, ok := s.checkers[monitor.Type]
	if !ok {
		runErr := fmt.Errorf("unsupported monitor type %q", monitor.Type)
		_ = s.store.UpdateMonitorCheckResult(ctx, monitor.ID, model.CheckResult{Status: "error"}, runErr)
		_ = s.store.FinishCheckRun(ctx, run.ID, "error", "", runErr, 0, started)
		return runErr
	}

	result, checkErr := checkerImpl.Check(ctx, monitor)
	if updateErr := s.store.UpdateMonitorCheckResult(ctx, monitor.ID, result, checkErr); updateErr != nil {
		_ = s.store.FinishCheckRun(ctx, run.ID, "error", result.Message, updateErr, 0, started)
		return updateErr
	}
	if checkErr != nil {
		_ = s.store.FinishCheckRun(ctx, run.ID, "error", result.Message, checkErr, 0, started)
		return checkErr
	}

	eventCount := 0
	var dispatchErr error
	for _, eventData := range result.Events {
		event, created, err := s.store.CreateEventForRun(ctx, monitor.ID, run.ID, eventData)
		if err != nil {
			dispatchErr = errors.Join(dispatchErr, err)
			continue
		}
		if !created {
			continue
		}
		eventCount++
		if err := s.processOutboxEvent(ctx, event.ID, 0); err != nil {
			dispatchErr = errors.Join(dispatchErr, err)
		}
	}
	status := result.Status
	if status == "" {
		status = "ok"
	}
	if dispatchErr != nil {
		status = "warning"
		_ = s.store.UpdateMonitorDispatchWarning(ctx, monitor.ID, dispatchErr)
	}
	if err := s.store.FinishCheckRun(ctx, run.ID, status, result.Message, dispatchErr, eventCount, started); err != nil {
		return err
	}
	return dispatchErr
}

func (s *Scheduler) processOutboxEvent(ctx context.Context, eventID int64, attempts int) error {
	claimed, err := s.store.ClaimOutbox(ctx, eventID)
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	event, err := s.store.GetEvent(ctx, eventID)
	if err == nil {
		var monitor model.Monitor
		monitor, err = s.store.GetMonitor(ctx, event.MonitorID)
		if err == nil {
			err = s.dispatchEvent(ctx, monitor, event)
		}
	}
	if err != nil {
		_ = s.store.MarkOutboxFailed(ctx, eventID, attempts+1, err)
		return err
	}
	return s.store.MarkOutboxProcessed(ctx, eventID)
}

func (s *Scheduler) dispatchEvent(ctx context.Context, monitor model.Monitor, event model.Event) error {
	rules, err := s.store.ListRulesForMonitor(ctx, monitor.ID)
	if err != nil {
		return err
	}
	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return err
	}
	if len(rules) == 0 {
		_, err := s.store.CreateRuleEvaluation(ctx, event.ID, nil, "没有启用的规则", "skipped", "这个监控没有关联已启用的规则。", nil)
		return err
	}
	for _, item := range rules {
		ruleID := item.ID
		if item.CooldownSeconds > 0 && item.LastFiredAt != nil && time.Since(*item.LastFiredAt) < time.Duration(item.CooldownSeconds)*time.Second {
			reason := fmt.Sprintf("规则处于冷却期，结束时间：%s。", item.LastFiredAt.Add(time.Duration(item.CooldownSeconds)*time.Second).Format(time.RFC3339))
			if _, err := s.store.CreateRuleEvaluation(ctx, event.ID, &ruleID, item.Name, "skipped", reason, nil); err != nil {
				return err
			}
			continue
		}
		matchedOK, matched, matchErr := rule.Match(item.Condition, payload)
		if matchErr != nil {
			if _, err := s.store.CreateRuleEvaluation(ctx, event.ID, &ruleID, item.Name, "error", matchErr.Error(), matched); err != nil {
				return err
			}
			continue
		}
		if !matchedOK {
			if _, err := s.store.CreateRuleEvaluation(ctx, event.ID, &ruleID, item.Name, "not_matched", "事件内容不符合规则条件。", matched); err != nil {
				return err
			}
			continue
		}
		channels, err := s.store.ListNotifyChannelsByIDs(ctx, item.NotifyChannelIDs)
		if err != nil {
			return err
		}
		if len(channels) == 0 {
			if _, err := s.store.CreateRuleEvaluation(ctx, event.ID, &ruleID, item.Name, "skipped", "所选通知渠道均未启用。", matched); err != nil {
				return err
			}
			continue
		}
		template, err := s.templateForRule(ctx, item)
		if err != nil {
			if _, recordErr := s.store.CreateRuleEvaluation(ctx, event.ID, &ruleID, item.Name, "error", err.Error(), matched); recordErr != nil {
				return recordErr
			}
			continue
		}
		data := notificationData(monitor, item, event, payload, matched)
		message := notifier.Message{
			Subject: templatex.Render(template.SubjectTemplate, data),
			Body:    templatex.Render(template.BodyTemplate, data),
			Data:    data,
		}
		evaluation, err := s.store.CreateRuleEvaluation(ctx, event.ID, &ruleID, item.Name, "matched", "规则已匹配，并已尝试发送通知。", matched)
		if err != nil {
			return err
		}
		successCount := 0
		for _, channel := range channels {
			attempt, sendErr := s.sendAndRecord(ctx, channel, message, model.NotificationAttemptInput{
				EventID: int64Ptr(event.ID), RuleEvaluationID: int64Ptr(evaluation.ID), ChannelID: int64Ptr(channel.ID),
				ChannelName: channel.Name, ChannelType: channel.Type, Kind: "delivery", AttemptNo: 1,
			})
			if sendErr == nil && attempt.Status == "sent" {
				successCount++
			}
		}
		if successCount > 0 {
			if err := s.store.UpdateRuleFiredAt(ctx, item.ID, time.Now().UTC()); err != nil {
				s.logger.Error("update rule fired at", "rule_id", item.ID, "error", err)
			}
		}
	}
	return nil
}

func (s *Scheduler) sendAndRecord(ctx context.Context, channel model.NotifyChannel, message notifier.Message, input model.NotificationAttemptInput) (model.NotificationAttempt, error) {
	started := time.Now().UTC()
	notifierImpl, ok := s.notifiers[channel.Type]
	var sendErr error
	if !ok {
		sendErr = fmt.Errorf("unsupported channel type %q", channel.Type)
	} else {
		sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		sendErr = notifierImpl.Send(sendCtx, channel, message)
		cancel()
	}
	finished := time.Now().UTC()
	input.Subject = message.Subject
	input.Body = message.Body
	input.DurationMS = finished.Sub(started).Milliseconds()
	input.Status = "sent"
	input.SentAt = &finished
	if sendErr != nil {
		input.Status = "failed"
		input.Error = sendErr.Error()
		input.SentAt = nil
		if input.AttemptNo < maxDeliveryAttempts {
			next := finished.Add(retryDelay(input.AttemptNo))
			input.NextRetryAt = &next
		}
	}
	attempt, recordErr := s.store.CreateNotificationAttempt(ctx, input)
	if input.Kind == "delivery" && input.EventID != nil && input.ChannelID != nil {
		if err := s.store.CreateNotificationLog(ctx, *input.EventID, *input.ChannelID, input.Status, sendErr); err != nil {
			s.logger.Error("create legacy notification log", "error", err)
		}
	}
	if recordErr != nil {
		return model.NotificationAttempt{}, recordErr
	}
	return attempt, sendErr
}

func (s *Scheduler) retryAttempt(ctx context.Context, source model.NotificationAttempt) (model.NotificationAttempt, error) {
	if source.ChannelID == nil {
		return model.NotificationAttempt{}, fmt.Errorf("attempt has no notification channel")
	}
	channel, err := s.store.GetNotifyChannel(ctx, *source.ChannelID)
	if err != nil {
		return model.NotificationAttempt{}, err
	}
	attempt, sendErr := s.sendAndRecord(ctx, channel, notifier.Message{Subject: source.Subject, Body: source.Body}, model.NotificationAttemptInput{
		EventID: source.EventID, RuleEvaluationID: source.RuleEvaluationID, ChannelID: source.ChannelID,
		RetryOfID: int64Ptr(source.ID), ChannelName: channel.Name, ChannelType: channel.Type,
		Kind: source.Kind, AttemptNo: source.AttemptNo + 1,
	})
	if sendErr == nil && source.RuleEvaluationID != nil {
		if evaluation, err := s.store.GetRuleEvaluation(ctx, *source.RuleEvaluationID); err == nil && evaluation.RuleID != nil {
			_ = s.store.UpdateRuleFiredAt(ctx, *evaluation.RuleID, time.Now().UTC())
		}
	}
	return attempt, sendErr
}

func (s *Scheduler) startMaintenance(ctx context.Context) {
	s.mu.Lock()
	if s.maintenance {
		s.mu.Unlock()
		return
	}
	s.maintenance = true
	s.mu.Unlock()
	go func() {
		defer func() {
			s.mu.Lock()
			s.maintenance = false
			s.mu.Unlock()
		}()
		now := time.Now().UTC()
		outbox, err := s.store.ListDueOutbox(ctx, 20, now)
		if err != nil {
			s.logger.Error("list event outbox", "error", err)
		} else {
			for _, item := range outbox {
				if err := s.processOutboxEvent(ctx, item.EventID, item.Attempts); err != nil {
					s.logger.Error("dispatch outbox event", "event_id", item.EventID, "error", err)
				}
			}
		}
		due, err := s.store.ListDueNotificationAttempts(ctx, 20, now)
		if err != nil {
			s.logger.Error("list retryable notification attempts", "error", err)
			return
		}
		for _, attempt := range due {
			claimed, err := s.store.ClaimNotificationAttempt(ctx, attempt.ID)
			if err != nil || !claimed {
				continue
			}
			if _, err := s.retryAttempt(ctx, attempt); err != nil {
				s.logger.Warn("retry notification", "attempt_id", attempt.ID, "error", err)
			}
		}
	}()
}

func (s *Scheduler) templateForRule(ctx context.Context, item model.Rule) (model.NotificationTemplate, error) {
	if item.TemplateID != nil {
		return s.store.GetNotificationTemplate(ctx, *item.TemplateID)
	}
	return s.store.GetNotificationTemplate(ctx, 1)
}

func notificationData(monitor model.Monitor, ruleItem model.Rule, event model.Event, payload map[string]any, matched []string) map[string]any {
	data := map[string]any{
		"monitor": map[string]any{"id": monitor.ID, "name": monitor.Name, "type": monitor.Type},
		"rule":    map[string]any{"id": ruleItem.ID, "name": ruleItem.Name, "matched": matched},
		"event": map[string]any{
			"id": event.ID, "type": event.Type, "fingerprint": event.Fingerprint,
			"time": event.CreatedAt.Format(time.RFC3339Nano),
		},
	}
	for key, value := range payload {
		data[key] = value
	}
	return data
}

func (s *Scheduler) NextCheckAt(monitor model.Monitor) *time.Time {
	if !monitor.Enabled {
		return nil
	}
	if monitor.LastCheckedAt == nil {
		now := time.Now().UTC()
		return &now
	}
	next := monitor.LastCheckedAt.Add(s.dueInterval(monitor))
	return &next
}

func (s *Scheduler) isDue(monitor model.Monitor, now time.Time) bool {
	if monitor.Enabled && monitor.LastCheckedAt == nil {
		return true
	}
	next := s.NextCheckAt(monitor)
	return next != nil && !now.Before(*next)
}

func (s *Scheduler) dueInterval(monitor model.Monitor) time.Duration {
	interval := monitor.IntervalSeconds
	if interval <= 0 {
		interval = 300
	}
	duration := time.Duration(interval) * time.Second
	failures := monitor.ConsecutiveFailures
	if failures > 6 {
		failures = 6
	}
	if failures > 0 {
		duration *= time.Duration(1 << failures)
	}
	if duration > 6*time.Hour {
		duration = 6 * time.Hour
	}
	jitterPercent := (monitor.ID*37)%21 - 10
	duration += duration * time.Duration(jitterPercent) / 100
	return duration
}

func (s *Scheduler) redactedMonitorConfig(monitor model.Monitor) json.RawMessage {
	var config map[string]any
	if err := json.Unmarshal(monitor.Config, &config); err != nil {
		return json.RawMessage("{}")
	}
	for _, plugin := range s.checkers.Plugins() {
		if plugin.ID != monitor.Type {
			continue
		}
		for _, field := range plugin.ConfigFields {
			if field.Secret {
				if value, exists := config[field.Key]; exists && fmt.Sprint(value) != "" {
					config[field.Key] = "<redacted>"
				}
			}
		}
	}
	data, err := json.Marshal(config)
	if err != nil {
		return json.RawMessage("{}")
	}
	return data
}

func retryDelay(attemptNo int) time.Duration {
	if attemptNo < 1 {
		attemptNo = 1
	}
	return time.Duration(1<<(attemptNo-1)) * time.Minute
}

func int64Ptr(value int64) *int64 {
	return &value
}

func (s *Scheduler) markInFlight(id int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.inFlight[id]; exists {
		return false
	}
	s.inFlight[id] = struct{}{}
	return true
}

func (s *Scheduler) clearInFlight(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.inFlight, id)
}
