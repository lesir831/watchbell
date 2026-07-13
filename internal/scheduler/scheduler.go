package scheduler

import (
	"context"
	"encoding/json"
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
	inFlight    map[int64]struct{}
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
		inFlight:    map[int64]struct{}{},
	}
}

func (s *Scheduler) Plugins() []model.MonitorPlugin {
	return s.checkers.Plugins()
}

func (s *Scheduler) HasPlugin(pluginID string) bool {
	return s.checkers.Has(pluginID)
}

func (s *Scheduler) Start(ctx context.Context) {
	ticker := time.NewTicker(s.tick)
	defer ticker.Stop()
	sem := make(chan struct{}, s.workerCount)
	s.enqueueDue(ctx, sem)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.enqueueDue(ctx, sem)
		}
	}
}

func (s *Scheduler) RunOnce(ctx context.Context, monitorID int64) error {
	monitor, err := s.store.GetMonitor(ctx, monitorID)
	if err != nil {
		return err
	}
	return s.processMonitor(ctx, monitor)
}

func (s *Scheduler) TestChannel(ctx context.Context, channelID int64) error {
	channel, err := s.store.GetNotifyChannel(ctx, channelID)
	if err != nil {
		return err
	}
	notifierImpl, ok := s.notifiers[channel.Type]
	if !ok {
		return fmt.Errorf("unsupported channel type %q", channel.Type)
	}
	return notifierImpl.Send(ctx, channel, notifier.Message{
		Subject: "WatchBell test notification",
		Body:    "This is a test notification from WatchBell.",
		Data: map[string]any{
			"event": map[string]any{"type": "test", "time": time.Now().UTC().Format(time.RFC3339Nano)},
		},
	})
}

func (s *Scheduler) enqueueDue(ctx context.Context, sem chan struct{}) {
	monitors, err := s.store.ListEnabledMonitors(ctx)
	if err != nil {
		s.logger.Error("list enabled monitors", "error", err)
		return
	}
	now := time.Now().UTC()
	for _, monitor := range monitors {
		if !isDue(monitor, now) || !s.markInFlight(monitor.ID) {
			continue
		}
		sem <- struct{}{}
		go func(item model.Monitor) {
			defer func() {
				<-sem
				s.clearInFlight(item.ID)
			}()
			if err := s.processMonitor(ctx, item); err != nil {
				s.logger.Error("process monitor", "monitor_id", item.ID, "error", err)
			}
		}(monitor)
	}
}

func (s *Scheduler) processMonitor(ctx context.Context, monitor model.Monitor) error {
	checkerImpl, ok := s.checkers[monitor.Type]
	if !ok {
		err := fmt.Errorf("unsupported monitor type %q", monitor.Type)
		_ = s.store.UpdateMonitorCheckResult(ctx, monitor.ID, model.CheckResult{Status: "error"}, err)
		return err
	}

	result, err := checkerImpl.Check(ctx, monitor)
	if updateErr := s.store.UpdateMonitorCheckResult(ctx, monitor.ID, result, err); updateErr != nil {
		return updateErr
	}
	if err != nil {
		return err
	}
	for _, eventData := range result.Events {
		event, created, err := s.store.CreateEvent(ctx, monitor.ID, eventData)
		if err != nil {
			return err
		}
		if !created {
			continue
		}
		if err := s.dispatchEvent(ctx, monitor, event); err != nil {
			s.logger.Error("dispatch event", "event_id", event.ID, "error", err)
		}
	}
	return nil
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
	for _, item := range rules {
		if item.CooldownSeconds > 0 && item.LastFiredAt != nil && time.Since(*item.LastFiredAt) < time.Duration(item.CooldownSeconds)*time.Second {
			continue
		}
		ok, matched, err := rule.Match(item.Condition, payload)
		if err != nil {
			s.logger.Error("match rule", "rule_id", item.ID, "error", err)
			continue
		}
		if !ok {
			continue
		}
		channels, err := s.store.ListNotifyChannelsByIDs(ctx, item.NotifyChannelIDs)
		if err != nil {
			return err
		}
		if len(channels) == 0 {
			continue
		}
		template, err := s.templateForRule(ctx, item)
		if err != nil {
			return err
		}
		data := notificationData(monitor, item, event, payload, matched)
		message := notifier.Message{
			Subject: templatex.Render(template.SubjectTemplate, data),
			Body:    templatex.Render(template.BodyTemplate, data),
			Data:    data,
		}
		for _, channel := range channels {
			notifierImpl, ok := s.notifiers[channel.Type]
			if !ok {
				_ = s.store.CreateNotificationLog(ctx, event.ID, channel.ID, "failed", fmt.Errorf("unsupported channel type %q", channel.Type))
				continue
			}
			sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			sendErr := notifierImpl.Send(sendCtx, channel, message)
			cancel()
			status := "sent"
			if sendErr != nil {
				status = "failed"
			}
			if err := s.store.CreateNotificationLog(ctx, event.ID, channel.ID, status, sendErr); err != nil {
				s.logger.Error("create notification log", "error", err)
			}
		}
		if err := s.store.UpdateRuleFiredAt(ctx, item.ID, time.Now().UTC()); err != nil {
			s.logger.Error("update rule fired at", "rule_id", item.ID, "error", err)
		}
	}
	return nil
}

func (s *Scheduler) templateForRule(ctx context.Context, item model.Rule) (model.NotificationTemplate, error) {
	if item.TemplateID != nil {
		return s.store.GetNotificationTemplate(ctx, *item.TemplateID)
	}
	return s.store.GetNotificationTemplate(ctx, 1)
}

func notificationData(monitor model.Monitor, rule model.Rule, event model.Event, payload map[string]any, matched []string) map[string]any {
	data := map[string]any{
		"monitor": map[string]any{
			"id":   monitor.ID,
			"name": monitor.Name,
			"type": monitor.Type,
		},
		"rule": map[string]any{
			"id":      rule.ID,
			"name":    rule.Name,
			"matched": matched,
		},
		"event": map[string]any{
			"id":          event.ID,
			"type":        event.Type,
			"fingerprint": event.Fingerprint,
			"time":        event.CreatedAt.Format(time.RFC3339Nano),
		},
	}
	for key, value := range payload {
		data[key] = value
	}
	return data
}

func isDue(monitor model.Monitor, now time.Time) bool {
	if monitor.LastCheckedAt == nil {
		return true
	}
	interval := monitor.IntervalSeconds
	if interval <= 0 {
		interval = 300
	}
	return now.Sub(*monitor.LastCheckedAt) >= time.Duration(interval)*time.Second
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
