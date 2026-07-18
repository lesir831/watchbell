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
	"github.com/watchbell/watchbell/internal/eventvars"
	"github.com/watchbell/watchbell/internal/model"
	"github.com/watchbell/watchbell/internal/notifier"
	"github.com/watchbell/watchbell/internal/rule"
	"github.com/watchbell/watchbell/internal/store"
	"github.com/watchbell/watchbell/internal/templatex"
)

const maxDeliveryAttempts = 3

var ErrAlreadyRunning = errors.New("monitor is already running")
var (
	// ErrRetryNotFailed is returned when the requested row is not a failed
	// attempt. API callers can expose it as an unprocessable retry request.
	ErrRetryNotFailed = errors.New("only failed notification attempts can be retried")
	// ErrRetryConflict means another worker owns the retry or the source already
	// has a successor. Retrying it again would duplicate delivery.
	ErrRetryConflict          = errors.New("notification attempt is already being retried or has been superseded")
	ErrRetryTargetUnavailable = errors.New("notification retry target is unavailable")
)

type Options struct {
	Tick        time.Duration
	WorkerCount int
	Logger      *slog.Logger
	Now         func() time.Time
}

type Scheduler struct {
	store       *store.Store
	checkers    checker.Registry
	notifiers   notifier.Registry
	tick        time.Duration
	workerCount int
	logger      *slog.Logger
	now         func() time.Time
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
	if options.Now == nil {
		options.Now = time.Now
	}
	return &Scheduler{
		store:       store,
		checkers:    checkers,
		notifiers:   notifiers,
		tick:        options.Tick,
		workerCount: options.WorkerCount,
		logger:      options.Logger,
		now:         options.Now,
		startedAt:   options.Now().UTC(),
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
	now := s.nowUTC()
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
			"event": map[string]any{"type": "test", "time": s.nowUTC().Format(time.RFC3339Nano)},
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
		return model.NotificationAttempt{}, ErrRetryNotFailed
	}
	claimed, err := s.store.ClaimNotificationAttemptNow(ctx, attempt.ID, s.nowUTC())
	if err != nil {
		return model.NotificationAttempt{}, err
	}
	if !claimed {
		return model.NotificationAttempt{}, ErrRetryConflict
	}
	return s.runClaimedNotificationRetry(ctx, attempt)
}

func (s *Scheduler) enqueueDue(ctx context.Context, sem chan struct{}) {
	monitors, err := s.store.ListEnabledMonitors(ctx)
	if err != nil {
		s.logger.Error("list enabled monitors", "error", err)
		return
	}
	now := s.nowUTC()
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
	started := s.nowUTC()
	run, err := s.store.CreateCheckRun(ctx, monitor, trigger, s.redactedMonitorConfig(monitor))
	if err != nil {
		return err
	}
	checkerImpl, ok := s.checkers[monitor.Type]
	if !ok {
		runErr := fmt.Errorf("unsupported monitor type %q", monitor.Type)
		_ = s.store.UpdateMonitorCheckResult(ctx, monitor.ID, model.CheckResult{Status: "error"}, runErr)
		if alertErr := s.handleMonitorHealthTransition(ctx, monitor, model.CheckResult{Status: "error"}, runErr); alertErr != nil {
			s.logger.Error("monitor failure alert", "monitor_id", monitor.ID, "error", alertErr)
		}
		_ = s.store.FinishCheckRun(ctx, run.ID, "error", "", runErr, 0, started)
		return runErr
	}

	var result model.CheckResult
	var checkErr error
	if monitor.ProxyID != nil {
		proxyProfile, proxyErr := s.store.GetProxyProfile(ctx, *monitor.ProxyID)
		if proxyErr != nil {
			checkErr = fmt.Errorf("configured proxy %d is unavailable: %w", *monitor.ProxyID, proxyErr)
		} else {
			monitor.Proxy = &proxyProfile
		}
	}
	if checkErr == nil {
		result, checkErr = checkerImpl.Check(ctx, monitor)
	}
	if updateErr := s.store.UpdateMonitorCheckResult(ctx, monitor.ID, result, checkErr); updateErr != nil {
		_ = s.store.FinishCheckRun(ctx, run.ID, "error", result.Message, updateErr, 0, started)
		return updateErr
	}
	if alertErr := s.handleMonitorHealthTransition(ctx, monitor, result, checkErr); alertErr != nil {
		s.logger.Error("monitor health alert", "monitor_id", monitor.ID, "error", alertErr)
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
		if err := s.processOutboxEvent(ctx, event.ID, 0, false); err != nil {
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

func (s *Scheduler) handleMonitorHealthTransition(ctx context.Context, previous model.Monitor, result model.CheckResult, checkErr error) error {
	if checkErr != nil {
		return s.sendMonitorFailureAlert(ctx, previous.ID)
	}
	return s.sendMonitorRecoveryAlert(ctx, previous, result)
}

func (s *Scheduler) sendMonitorFailureAlert(ctx context.Context, monitorID int64) error {
	monitor, err := s.store.GetMonitor(ctx, monitorID)
	if err != nil {
		return err
	}
	if monitor.FailureAlertActive || monitor.FailureAlertAfter <= 0 || monitor.ConsecutiveFailures < monitor.FailureAlertAfter {
		return nil
	}
	channels, err := s.store.ListNotifyChannelsByIDs(ctx, monitor.FailureNotifyChannelIDs)
	if err != nil {
		return err
	}
	// A temporarily disabled channel must not consume the one-shot incident
	// transition. Leave the flag clear so a later failed check can try again.
	if len(channels) == 0 {
		return nil
	}
	activated, err := s.store.TryActivateMonitorFailureAlert(ctx, monitorID)
	if err != nil || !activated {
		return err
	}
	cancelErr := s.store.StopMonitorNotificationRetries(ctx, monitorID, "monitor_recovery", "retry stopped: a new failure incident is active")
	message := monitorHealthMessage(monitor, "error", monitor.LastError, monitor.ConsecutiveFailures)
	message.Subject = "监控故障 · " + monitor.Name
	message.Body = fmt.Sprintf("监控 %s 已连续 %d 次检查失败。\n%s", monitor.Name, monitor.ConsecutiveFailures, monitor.LastError)
	return errors.Join(cancelErr, s.sendMonitorHealthAlert(ctx, monitor.ID, channels, message, "monitor_failure"))
}

func (s *Scheduler) sendMonitorRecoveryAlert(ctx context.Context, previous model.Monitor, result model.CheckResult) error {
	current, err := s.store.GetMonitor(ctx, previous.ID)
	if err != nil {
		return err
	}
	if !current.FailureAlertActive {
		return nil
	}
	channels, err := s.store.ListNotifyChannelsByIDs(ctx, current.FailureNotifyChannelIDs)
	if err != nil {
		return err
	}
	cleared, err := s.store.TryClearMonitorFailureAlert(ctx, current.ID)
	if err != nil || !cleared {
		return err
	}
	cancelErr := s.store.StopMonitorNotificationRetries(ctx, current.ID, "monitor_failure", "retry stopped: monitor recovered")
	status := result.Status
	if status == "" {
		status = "ok"
	}
	message := monitorHealthMessage(current, status, previous.LastError, previous.ConsecutiveFailures)
	message.Subject = "监控恢复 · " + current.Name
	message.Body = fmt.Sprintf("监控 %s 已恢复正常（此前连续 %d 次检查失败）。", current.Name, previous.ConsecutiveFailures)
	return errors.Join(cancelErr, s.sendMonitorHealthAlert(ctx, current.ID, channels, message, "monitor_recovery"))
}

func monitorHealthMessage(monitor model.Monitor, status, lastError string, failures int) notifier.Message {
	monitorData := map[string]any{
		"id": monitor.ID, "name": monitor.Name, "type": monitor.Type,
		"status": status, "error": lastError, "failures": failures,
	}
	return notifier.Message{Data: map[string]any{
		"monitor":  monitorData,
		"status":   status,
		"error":    lastError,
		"failures": failures,
	}}
}

func (s *Scheduler) sendMonitorHealthAlert(ctx context.Context, monitorID int64, channels []model.NotifyChannel, message notifier.Message, kind string) error {
	var recordErr error
	for _, channel := range channels {
		attempt, err := s.sendAndRecord(ctx, channel, message, model.NotificationAttemptInput{
			MonitorID: int64Ptr(monitorID), ChannelID: int64Ptr(channel.ID), ChannelName: channel.Name, ChannelType: channel.Type,
			Kind: kind, AttemptNo: 1,
		})
		// Provider failures have already been recorded and scheduled for retry.
		// Only surface failures that prevented creating the trace record itself.
		if err != nil && attempt.ID == 0 {
			recordErr = errors.Join(recordErr, err)
		}
	}
	return recordErr
}

func (s *Scheduler) processOutboxEvent(ctx context.Context, eventID int64, attempts int, requireDue bool) error {
	claimed, err := s.store.ClaimOutbox(ctx, eventID, s.nowUTC(), requireDue)
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
	payload = eventvars.EnrichPayload(monitor, payload)
	if len(rules) == 0 {
		_, err := s.store.CreateRuleEvaluation(ctx, event.ID, nil, "没有启用的规则", "skipped", "这个监控没有关联已启用的规则。", nil)
		return err
	}
	var dispatchErr error
	for _, item := range rules {
		ruleID := item.ID
		if item.CooldownSeconds > 0 && item.LastFiredAt != nil && s.nowUTC().Sub(*item.LastFiredAt) < time.Duration(item.CooldownSeconds)*time.Second {
			reason := fmt.Sprintf("规则处于冷却期，结束时间：%s。", item.LastFiredAt.Add(time.Duration(item.CooldownSeconds)*time.Second).Format(time.RFC3339))
			if _, err := s.store.CreateRuleEvaluation(ctx, event.ID, &ruleID, item.Name, "skipped", reason, nil); err != nil {
				return err
			}
			continue
		}
		matchedOK, matched, matchErr := rule.MatchAt(item.Condition, payload, s.nowUTC())
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
		quiet, quietErr := rule.QuietHoursActiveAt(item.QuietHours, s.nowUTC())
		if quietErr != nil {
			if _, err := s.store.CreateRuleEvaluation(ctx, event.ID, &ruleID, item.Name, "error", "免打扰时段配置无效："+quietErr.Error(), matched); err != nil {
				return err
			}
			continue
		}
		if quiet {
			reason := fmt.Sprintf("规则已匹配，但当前处于免打扰时段（%s–%s，%s），未发送通知。", item.QuietHours.Start, item.QuietHours.End, item.QuietHours.Timezone)
			if _, err := s.store.CreateRuleEvaluation(ctx, event.ID, &ruleID, item.Name, "skipped", reason, matched); err != nil {
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
				MonitorID: int64Ptr(monitor.ID), EventID: int64Ptr(event.ID), RuleEvaluationID: int64Ptr(evaluation.ID), ChannelID: int64Ptr(channel.ID),
				ChannelName: channel.Name, ChannelType: channel.Type, Kind: "delivery", AttemptNo: 1,
			})
			if attempt.ID == 0 && sendErr != nil {
				dispatchErr = errors.Join(dispatchErr, fmt.Errorf("record notification for channel %d: %w", channel.ID, sendErr))
				continue
			}
			if sendErr == nil && attempt.Status == "sent" {
				successCount++
			}
		}
		if successCount > 0 {
			if err := s.store.UpdateRuleFiredAt(ctx, item.ID, s.nowUTC()); err != nil {
				s.logger.Error("update rule fired at", "rule_id", item.ID, "error", err)
			}
		}
	}
	return dispatchErr
}

func (s *Scheduler) sendAndRecord(ctx context.Context, channel model.NotifyChannel, message notifier.Message, input model.NotificationAttemptInput) (model.NotificationAttempt, error) {
	started := s.nowUTC()
	notifierImpl, ok := s.notifiers[channel.Type]
	var sendErr error
	if !ok {
		sendErr = fmt.Errorf("unsupported channel type %q", channel.Type)
	} else {
		sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		sendErr = notifierImpl.Send(sendCtx, channel, message)
		cancel()
	}
	finished := s.nowUTC()
	input.Subject = message.Subject
	input.Body = message.Body
	input.Data = notificationAttemptData(message.Data)
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
	if source.Kind == "monitor_failure" || source.Kind == "monitor_recovery" {
		if source.MonitorID == nil {
			return model.NotificationAttempt{}, fmt.Errorf("%w: health notification has no monitor", ErrRetryTargetUnavailable)
		}
		monitor, err := s.store.GetMonitor(ctx, *source.MonitorID)
		if err != nil {
			if store.IsNotFound(err) {
				return model.NotificationAttempt{}, fmt.Errorf("%w: monitor was archived", ErrRetryTargetUnavailable)
			}
			return model.NotificationAttempt{}, err
		}
		if source.Kind == "monitor_failure" && !monitor.FailureAlertActive {
			return model.NotificationAttempt{}, fmt.Errorf("%w: failure incident is no longer active", ErrRetryTargetUnavailable)
		}
		if source.Kind == "monitor_recovery" && monitor.FailureAlertActive {
			return model.NotificationAttempt{}, fmt.Errorf("%w: monitor has entered a new failure incident", ErrRetryTargetUnavailable)
		}
	}
	if source.ChannelID == nil {
		return model.NotificationAttempt{}, fmt.Errorf("%w: attempt has no notification channel", ErrRetryTargetUnavailable)
	}
	channel, err := s.store.GetNotifyChannel(ctx, *source.ChannelID)
	if err != nil {
		if store.IsNotFound(err) {
			return model.NotificationAttempt{}, fmt.Errorf("%w: notification channel was archived", ErrRetryTargetUnavailable)
		}
		return model.NotificationAttempt{}, err
	}
	if !channel.Enabled {
		return model.NotificationAttempt{}, fmt.Errorf("%w: notification channel is disabled", ErrRetryTargetUnavailable)
	}
	data := map[string]any{}
	if len(source.Data) > 0 {
		_ = json.Unmarshal(source.Data, &data)
	}
	if data == nil {
		data = map[string]any{}
	}
	s.backfillRetryGlobalVariables(ctx, source, data)
	attempt, sendErr := s.sendAndRecord(ctx, channel, notifier.Message{Subject: source.Subject, Body: source.Body, Data: data}, model.NotificationAttemptInput{
		MonitorID: source.MonitorID, EventID: source.EventID, RuleEvaluationID: source.RuleEvaluationID, ChannelID: source.ChannelID,
		RetryOfID: int64Ptr(source.ID), ChannelName: channel.Name, ChannelType: channel.Type,
		Kind: source.Kind, AttemptNo: source.AttemptNo + 1,
	})
	if sendErr == nil && source.RuleEvaluationID != nil {
		if evaluation, err := s.store.GetRuleEvaluation(ctx, *source.RuleEvaluationID); err == nil && evaluation.RuleID != nil {
			_ = s.store.UpdateRuleFiredAt(ctx, *evaluation.RuleID, s.nowUTC())
		}
	}
	return attempt, sendErr
}

// backfillRetryGlobalVariables upgrades attempts created before the common
// event variables existed. The failed attempt's Data is the delivery snapshot:
// values already stored there must win over both the historical event row and
// the monitor's current configuration. Only missing global aliases are added.
func (s *Scheduler) backfillRetryGlobalVariables(ctx context.Context, source model.NotificationAttempt, data map[string]any) {
	if source.EventID == nil {
		return
	}
	globals := eventvars.VariableCatalog().Globals
	missingGlobal := false
	for _, definition := range globals {
		if _, exists := data[definition.Key]; !exists {
			missingGlobal = true
			break
		}
	}
	if !missingGlobal {
		return
	}
	event, err := s.store.GetEvent(ctx, *source.EventID)
	if err != nil {
		return
	}
	var payload map[string]any
	if json.Unmarshal(event.Payload, &payload) != nil {
		return
	}
	if payload == nil {
		payload = map[string]any{}
	}
	// The persisted attempt may contain an older copy of the module payload.
	// Overlay it at the top level so those snapshot values drive the aliases.
	for key, value := range data {
		payload[key] = value
	}

	monitor := model.Monitor{}
	if monitorData, ok := data["monitor"].(map[string]any); ok {
		monitor.Name, _ = monitorData["name"].(string)
		monitor.Type, _ = monitorData["type"].(string)
	}
	if event.CheckRunID != nil {
		if run, runErr := s.store.GetCheckRun(ctx, *event.CheckRunID); runErr == nil && run.MonitorID == event.MonitorID {
			if monitor.Name == "" {
				monitor.Name = run.MonitorName
			}
			if monitor.Type == "" {
				monitor.Type = run.MonitorType
			}
			if json.Valid(run.ConfigSnapshot) {
				monitor.Config = append(json.RawMessage(nil), run.ConfigSnapshot...)
			}
		}
	}
	if monitor.Type == "" {
		// The monitor type selects the module mapping, but deliberately do not
		// copy its current name or config into the historical snapshot.
		if current, monitorErr := s.store.GetMonitorIncludingArchived(ctx, event.MonitorID); monitorErr == nil {
			monitor.Type = current.Type
		}
	}
	if monitor.Type == "" {
		return
	}
	derived := eventvars.EnrichPayload(monitor, payload)
	for _, definition := range globals {
		if _, exists := data[definition.Key]; exists {
			continue
		}
		if value, exists := derived[definition.Key]; exists {
			data[definition.Key] = value
		}
	}
}

func (s *Scheduler) runClaimedNotificationRetry(ctx context.Context, source model.NotificationAttempt) (model.NotificationAttempt, error) {
	attempt, retryErr := s.retryAttempt(ctx, source)
	if attempt.ID > 0 {
		// CreateNotificationAttempt atomically committed the successor and source
		// finalization, so retryErr now describes only the provider outcome.
		return attempt, retryErr
	}
	if errors.Is(retryErr, store.ErrNotificationRetryConflict) {
		return attempt, errors.Join(ErrRetryConflict, s.store.CancelNotificationRetry(ctx, source.ID))
	}
	if errors.Is(retryErr, ErrRetryTargetUnavailable) {
		stopErr := s.store.StopNotificationRetry(ctx, source.ID, "retry stopped: "+retryErr.Error())
		return attempt, errors.Join(retryErr, stopErr)
	}
	// Keep the source attempt recoverable when the retry failed before a new
	// trace row could be created (for example a transient DB lookup failure).
	releaseErr := s.store.ReleaseNotificationRetry(ctx, source.ID, s.nowUTC().Add(time.Minute))
	return attempt, errors.Join(retryErr, releaseErr)
}

func notificationAttemptData(data map[string]any) json.RawMessage {
	if data == nil {
		return json.RawMessage("{}")
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return json.RawMessage("{}")
	}
	return encoded
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
		now := s.nowUTC()
		outbox, err := s.store.ListDueOutbox(ctx, 20, now)
		if err != nil {
			s.logger.Error("list event outbox", "error", err)
		} else {
			for _, item := range outbox {
				if err := s.processOutboxEvent(ctx, item.EventID, item.Attempts, true); err != nil {
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
			claimed, err := s.store.ClaimNotificationAttempt(ctx, attempt.ID, now)
			if err != nil || !claimed {
				continue
			}
			if _, err := s.runClaimedNotificationRetry(ctx, attempt); err != nil {
				s.logger.Warn("retry notification", "attempt_id", attempt.ID, "error", err)
			}
		}
	}()
}

func (s *Scheduler) templateForRule(ctx context.Context, item model.Rule) (model.NotificationTemplate, error) {
	if item.TemplateID != nil {
		return s.store.GetNotificationTemplate(ctx, *item.TemplateID)
	}
	return s.store.GetDefaultNotificationTemplate(ctx)
}

func notificationData(monitor model.Monitor, ruleItem model.Rule, event model.Event, payload map[string]any, matched []string) map[string]any {
	data := eventvars.EventData(monitor, event, payload)
	data["rule"] = map[string]any{"id": ruleItem.ID, "name": ruleItem.Name, "matched": matched}
	return data
}

func (s *Scheduler) NextCheckAt(monitor model.Monitor) *time.Time {
	if !monitor.Enabled {
		return nil
	}
	if monitor.LastCheckedAt == nil {
		now := s.nowUTC()
		return &now
	}
	next := monitor.LastCheckedAt.Add(s.dueInterval(monitor))
	return &next
}

func (s *Scheduler) nowUTC() time.Time {
	if s.now == nil {
		return time.Now().UTC()
	}
	return s.now().UTC()
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
