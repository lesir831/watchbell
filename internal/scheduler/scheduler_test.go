package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/watchbell/watchbell/internal/checker"
	"github.com/watchbell/watchbell/internal/model"
	"github.com/watchbell/watchbell/internal/notifier"
	"github.com/watchbell/watchbell/internal/store"
)

type traceChecker struct{}

func (traceChecker) Type() string { return "trace_test" }
func (traceChecker) Plugin() model.MonitorPlugin {
	return model.MonitorPlugin{
		ID: "trace_test", Name: "Trace test", DefaultIntervalSeconds: 60,
		ConfigFields: []model.PluginConfigField{{Key: "token", Label: "Token", Type: "secret", Secret: true}},
	}
}

func TestNeverCheckedMonitorIsImmediatelyDue(t *testing.T) {
	scheduler := &Scheduler{}
	monitor := model.Monitor{ID: 1, Enabled: true, IntervalSeconds: 300}
	if !scheduler.isDue(monitor, time.Now().UTC()) {
		t.Fatal("an enabled monitor without a previous check must be due immediately")
	}
}

func (traceChecker) Check(context.Context, model.Monitor) (model.CheckResult, error) {
	return model.CheckResult{
		Status: "ok", Message: "one event", State: map[string]any{"seen": true},
		Events: []model.EventData{{Type: "trace.event", Fingerprint: "trace:1", Payload: map[string]any{"trace": map[string]any{"value": "ready"}}}},
	}, nil
}

type traceNotifier struct {
	mu   sync.Mutex
	fail bool
}

func (n *traceNotifier) Type() string { return "trace_channel" }
func (n *traceNotifier) Send(context.Context, model.NotifyChannel, notifier.Message) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.fail {
		return errors.New("provider unavailable")
	}
	return nil
}
func (n *traceNotifier) setFail(value bool) {
	n.mu.Lock()
	n.fail = value
	n.mu.Unlock()
}

func TestTraceChainAndRetry(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir()+"/watchbell.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	monitor, err := db.CreateMonitor(ctx, model.MonitorInput{Name: "Trace monitor", Type: "trace_test", Enabled: false, IntervalSeconds: 60, Config: json.RawMessage(`{"token":"must-not-leak"}`)})
	if err != nil {
		t.Fatal(err)
	}
	channel, err := db.CreateNotifyChannel(ctx, model.NotifyChannelInput{Name: "Trace channel", Type: "trace_channel", Enabled: true, Config: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	templateID := int64(1)
	ruleItem, err := db.CreateRule(ctx, model.RuleInput{MonitorID: monitor.ID, Name: "All events", Enabled: true, Condition: json.RawMessage(`{}`), NotifyChannelIDs: []int64{channel.ID}, TemplateID: &templateID})
	if err != nil {
		t.Fatal(err)
	}

	sender := &traceNotifier{fail: true}
	scheduler := New(db, checker.NewRegistry(traceChecker{}), notifier.NewRegistry(sender), Options{})
	if err := scheduler.RunOnce(ctx, monitor.ID); err != nil {
		t.Fatalf("monitor check should succeed even when delivery is recorded as failed: %v", err)
	}

	runs, _ := db.ListCheckRuns(ctx, 10)
	events, _ := db.ListEvents(ctx, 10)
	evaluations, _ := db.ListRuleEvaluations(ctx, 10)
	attempts, _ := db.ListNotificationAttempts(ctx, 10)
	if len(runs) != 1 || runs[0].EventCount != 1 || runs[0].Status != "ok" {
		t.Fatalf("unexpected runs: %#v", runs)
	}
	if strings.Contains(string(runs[0].ConfigSnapshot), "must-not-leak") || !strings.Contains(string(runs[0].ConfigSnapshot), "redacted") {
		t.Fatalf("check run leaked its secret config: %s", runs[0].ConfigSnapshot)
	}
	if len(events) != 1 || events[0].CheckRunID == nil || *events[0].CheckRunID != runs[0].ID {
		t.Fatalf("event is not linked to its run: %#v", events)
	}
	if len(evaluations) != 1 || evaluations[0].Status != "matched" {
		t.Fatalf("unexpected evaluations: %#v", evaluations)
	}
	if len(attempts) != 1 || attempts[0].Status != "failed" || attempts[0].NextRetryAt == nil {
		t.Fatalf("failed delivery was not made retryable: %#v", attempts)
	}
	storedRule, _ := db.GetRule(ctx, ruleItem.ID)
	if storedRule.LastFiredAt != nil {
		t.Fatal("rule must not enter cooldown before a delivery succeeds")
	}

	sender.setFail(false)
	retried, err := scheduler.RetryAttempt(ctx, attempts[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if retried.Status != "sent" || retried.RetryOfID == nil || *retried.RetryOfID != attempts[0].ID || retried.AttemptNo != 2 {
		t.Fatalf("unexpected retry: %#v", retried)
	}
	original, _ := db.GetNotificationAttempt(ctx, attempts[0].ID)
	if original.NextRetryAt != nil {
		t.Fatal("manual retry must cancel the original scheduled retry")
	}
	storedRule, _ = db.GetRule(ctx, ruleItem.ID)
	if storedRule.LastFiredAt == nil {
		t.Fatal("successful retry should update the rule fire time")
	}
}
