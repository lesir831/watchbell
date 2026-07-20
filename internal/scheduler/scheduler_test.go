package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

type proxyCaptureChecker struct {
	seen chan *model.ProxyProfile
}

func (c *proxyCaptureChecker) Type() string { return "proxy_capture" }
func (c *proxyCaptureChecker) Check(_ context.Context, monitor model.Monitor) (model.CheckResult, error) {
	c.seen <- monitor.Proxy
	return model.CheckResult{Status: "ok", Message: "proxy captured"}, nil
}
func (c *proxyCaptureChecker) Inspect(_ context.Context, monitor model.Monitor) (model.Observation, error) {
	c.seen <- monitor.Proxy
	return model.Observation{
		Type: "proxy.current", Fingerprint: "proxy:current", Available: true,
		Message: "proxy inspected", Payload: map[string]any{"proxy": map[string]any{"current": true}},
	}, nil
}

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

func TestRunOnceResolvesAssignedProxyProfile(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir()+"/watchbell.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	profile, err := db.CreateProxyProfile(ctx, model.ProxyProfileInput{
		Name: "Capture proxy", Type: model.ProxyTypeHTTP, Host: "127.0.0.1", Port: 8080, Username: "user", Password: "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	monitor, err := db.CreateMonitor(ctx, model.MonitorInput{
		Name: "Proxy monitor", Type: "proxy_capture", ProxyID: &profile.ID, Enabled: false, IntervalSeconds: 60, Config: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	capture := &proxyCaptureChecker{seen: make(chan *model.ProxyProfile, 1)}
	scheduler := New(db, checker.NewRegistry(capture), notifier.NewRegistry(), Options{})
	if err := scheduler.RunOnce(ctx, monitor.ID); err != nil {
		t.Fatal(err)
	}
	seen := <-capture.seen
	if seen == nil || seen.ID != profile.ID || seen.Password != "secret" {
		t.Fatalf("resolved proxy = %#v", seen)
	}
}

func TestInspectMonitorResolvesProxyWithoutPersistingCheckState(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir()+"/watchbell.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	profile, err := db.CreateProxyProfile(ctx, model.ProxyProfileInput{
		Name: "Inspect proxy", Type: model.ProxyTypeSOCKS5, Host: "127.0.0.1", Port: 1080, Username: "user", Password: "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	monitor, err := db.CreateMonitor(ctx, model.MonitorInput{
		Name: "Inspect monitor", Type: "proxy_capture", ProxyID: &profile.ID, Enabled: false, IntervalSeconds: 60, Config: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateMonitorCheckResult(ctx, monitor.ID, model.CheckResult{
		Status: "ok", Message: "existing state", State: map[string]any{"seen": true},
	}, nil); err != nil {
		t.Fatal(err)
	}
	before, err := db.GetMonitor(ctx, monitor.ID)
	if err != nil {
		t.Fatal(err)
	}

	capture := &proxyCaptureChecker{seen: make(chan *model.ProxyProfile, 1)}
	scheduler := New(db, checker.NewRegistry(capture), notifier.NewRegistry(), Options{})
	observation, err := scheduler.InspectMonitor(ctx, before)
	if err != nil {
		t.Fatal(err)
	}
	seen := <-capture.seen
	if seen == nil || seen.ID != profile.ID || seen.Password != "secret" {
		t.Fatalf("resolved proxy = %#v", seen)
	}
	if observation.Type != "proxy.current" || !observation.Available {
		t.Fatalf("unexpected inspection result: observation=%#v", observation)
	}
	after, err := db.GetMonitor(ctx, monitor.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(after.State) != string(before.State) || !timePtrEqual(after.LastCheckedAt, before.LastCheckedAt) || after.LastStatus != before.LastStatus || after.UpdatedAt != before.UpdatedAt {
		t.Fatalf("inspection persisted monitor state: before=%#v after=%#v", before, after)
	}
	runs, _ := db.ListCheckRuns(ctx, 10)
	events, _ := db.ListEvents(ctx, 10)
	if len(runs) != 0 || len(events) != 0 {
		t.Fatalf("inspection created tracking rows: runs=%#v events=%#v", runs, events)
	}
}

func TestInspectMonitorDoesNotFallBackWhenConfiguredProxyIsMissing(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir()+"/watchbell.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	missingProxyID := int64(999)
	capture := &proxyCaptureChecker{seen: make(chan *model.ProxyProfile, 1)}
	scheduler := New(db, checker.NewRegistry(capture), notifier.NewRegistry(), Options{})
	_, err = scheduler.InspectMonitor(ctx, model.Monitor{
		ID: 1, Name: "Missing proxy", Type: capture.Type(), ProxyID: &missingProxyID,
		// An attached profile must not bypass persisted ProxyID resolution.
		Proxy: &model.ProxyProfile{ID: missingProxyID, Type: model.ProxyTypeHTTP, Host: "127.0.0.1", Port: 8080},
	})
	if !errors.Is(err, store.ErrProxyUnavailable) {
		t.Fatalf("inspection error = %v, want ErrProxyUnavailable", err)
	}
	select {
	case seen := <-capture.seen:
		t.Fatalf("inspector ran with missing proxy: %#v", seen)
	default:
	}
}

func timePtrEqual(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func (traceChecker) Check(context.Context, model.Monitor) (model.CheckResult, error) {
	return model.CheckResult{
		Status: "ok", Message: "one event", State: map[string]any{"seen": true},
		Events: []model.EventData{{Type: "trace.event", Fingerprint: "trace:1", Payload: map[string]any{"trace": map[string]any{"value": "ready"}}}},
	}, nil
}

type traceNotifier struct {
	mu       sync.Mutex
	fail     bool
	sent     int
	messages []notifier.Message
}

func (n *traceNotifier) Type() string { return "trace_channel" }
func (n *traceNotifier) Send(_ context.Context, _ model.NotifyChannel, message notifier.Message) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.sent++
	n.messages = append(n.messages, message)
	if n.fail {
		return errors.New("provider unavailable")
	}
	return nil
}
func (n *traceNotifier) sentCount() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.sent
}
func (n *traceNotifier) setFail(value bool) {
	n.mu.Lock()
	n.fail = value
	n.mu.Unlock()
}

func (n *traceNotifier) sentMessages() []notifier.Message {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]notifier.Message(nil), n.messages...)
}

func TestChannelProvidesRepresentativeTemplateData(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir()+"/watchbell.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	channel, err := db.CreateNotifyChannel(ctx, model.NotifyChannelInput{
		Name: "Template test", Type: "trace_channel", Enabled: true, Config: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	fixed := time.Date(2026, time.July, 20, 6, 52, 58, 0, time.UTC)
	sender := &traceNotifier{}
	scheduler := New(db, checker.NewRegistry(), notifier.NewRegistry(sender), Options{Now: func() time.Time { return fixed }})
	attempt, err := scheduler.TestChannel(ctx, channel.ID)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.Status != "sent" || attempt.Kind != "test" {
		t.Fatalf("attempt = %#v", attempt)
	}
	messages := sender.sentMessages()
	if len(messages) != 1 {
		t.Fatalf("messages = %d", len(messages))
	}
	data := messages[0].Data
	for _, key := range []string{"url", "title", "summary", "content", "author", "publishedAt", "status"} {
		if value, ok := data[key].(string); !ok || strings.TrimSpace(value) == "" {
			t.Errorf("test data %s = %#v", key, data[key])
		}
	}
	for _, key := range []string{"monitor", "rule", "event", "rss", "testflight", "webpage", "github"} {
		if value, ok := data[key].(map[string]any); !ok || len(value) == 0 {
			t.Errorf("test data %s = %#v", key, data[key])
		}
	}
	rss := data["rss"].(map[string]any)
	github := data["github"].(map[string]any)
	release := github["release"].(map[string]any)
	if rss["link"] == "" || release["url"] == "" {
		t.Fatalf("card template URLs are empty: rss=%#v github=%#v", rss, release)
	}
}

type healthSequenceChecker struct {
	mu                sync.Mutex
	failuresRemaining int
}

func (c *healthSequenceChecker) Type() string { return "health_sequence" }
func (c *healthSequenceChecker) Plugin() model.MonitorPlugin {
	return model.MonitorPlugin{ID: c.Type(), Name: "Health sequence", DefaultIntervalSeconds: 60}
}
func (c *healthSequenceChecker) Check(context.Context, model.Monitor) (model.CheckResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failuresRemaining > 0 {
		failureNumber := c.failuresRemaining
		c.failuresRemaining--
		return model.CheckResult{Status: "error", Message: "source unavailable"}, fmt.Errorf("source failure %d", failureNumber)
	}
	return model.CheckResult{Status: "ok", Message: "source recovered"}, nil
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
	messages := sender.sentMessages()
	if len(messages) != 2 {
		t.Fatalf("retry sends = %d, want 2", len(messages))
	}
	traceData, ok := messages[1].Data["trace"].(map[string]any)
	if !ok || traceData["value"] != "ready" {
		t.Fatalf("retry lost event template data: %#v", messages[1].Data)
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

func TestUnmatchedRuleEvaluationIdentifiesFailedCondition(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir()+"/watchbell.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	monitor, err := db.CreateMonitor(ctx, model.MonitorInput{
		Name: "Diagnostic monitor", Type: "trace_test", Enabled: false, IntervalSeconds: 60, Config: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.CreateRule(ctx, model.RuleInput{
		MonitorID: monitor.ID, Name: "Blocked value", Enabled: true,
		Condition: json.RawMessage(`{"match":"all","conditions":[{"field":"trace.value","operator":"equals","value":"blocked"}]}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	scheduler := New(db, checker.NewRegistry(traceChecker{}), notifier.NewRegistry(), Options{})
	if err := scheduler.RunOnce(ctx, monitor.ID); err != nil {
		t.Fatal(err)
	}
	evaluations, err := db.ListRuleEvaluations(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(evaluations) != 1 || evaluations[0].Status != "not_matched" {
		t.Fatalf("unexpected evaluations: %#v", evaluations)
	}
	for _, want := range []string{"第 1 个条件", `"trace.value"`, `"blocked"`, `实际值为 "ready"`} {
		if !strings.Contains(evaluations[0].Reason, want) {
			t.Fatalf("reason %q does not contain %q", evaluations[0].Reason, want)
		}
	}
	if evaluations[0].Reason == "事件内容不符合规则条件。" {
		t.Fatalf("evaluation kept generic reason: %#v", evaluations[0])
	}
}

func TestNotificationDataIncludesCrossModuleVariables(t *testing.T) {
	monitor := model.Monitor{ID: 1, Name: "Feed", Type: model.MonitorTypeRSS, Config: json.RawMessage(`{"url":"https://example.com/feed.xml"}`)}
	ruleItem := model.Rule{ID: 2, Name: "Keywords"}
	event := model.Event{ID: 3, Type: "rss.item", Fingerprint: "item-3", CreatedAt: time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC)}
	data := notificationData(monitor, ruleItem, event, map[string]any{"rss": map[string]any{
		"title": "Version 3", "link": "https://example.com/v3", "publishedAt": "2026-07-17T11:59:00Z",
	}}, []string{"Version"})
	if data["url"] != "https://example.com/v3" || data["title"] != "Version 3" || data["publishedAt"] != "2026-07-17T11:59:00Z" {
		t.Fatalf("missing cross-module variables: %#v", data)
	}
	if data["rule"].(map[string]any)["name"] != "Keywords" || data["event"].(map[string]any)["id"] != int64(3) {
		t.Fatalf("system context changed: %#v", data)
	}
}

func TestRetryAttemptPreservesPersistedNotificationSnapshot(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir()+"/watchbell.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	monitor, err := db.CreateMonitor(ctx, model.MonitorInput{
		Name: "Original feed", Type: model.MonitorTypeRSS, Enabled: false, IntervalSeconds: 60,
		Config: json.RawMessage(`{"url":"https://old.example/feed.xml"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	event, _, err := db.CreateEvent(ctx, monitor.ID, model.EventData{
		Type: "rss.item", Fingerprint: "database-fingerprint",
		Payload: map[string]any{"rss": map[string]any{"title": "Database event title"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	channel, err := db.CreateNotifyChannel(ctx, model.NotifyChannelInput{Name: "Retry channel", Type: "trace_channel", Enabled: true, Config: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := json.Marshal(map[string]any{
		"url":     "https://old.example/item",
		"title":   "Snapshot global title",
		"summary": "Snapshot summary",
		"monitor": map[string]any{"id": monitor.ID, "name": "Original feed", "type": model.MonitorTypeRSS},
		"event":   map[string]any{"id": event.ID, "type": event.Type, "fingerprint": "snapshot-fingerprint"},
		"rule":    map[string]any{"id": 42, "name": "Original rule"},
		"rss":     map[string]any{"title": "Snapshot module title"},
		"custom":  "keep-me",
	})
	if err != nil {
		t.Fatal(err)
	}
	nextRetry := time.Now().UTC().Add(time.Minute)
	monitorID, eventID, channelID := monitor.ID, event.ID, channel.ID
	source, err := db.CreateNotificationAttempt(ctx, model.NotificationAttemptInput{
		MonitorID: &monitorID, EventID: &eventID, ChannelID: &channelID,
		ChannelName: channel.Name, ChannelType: channel.Type, Kind: "delivery", Status: "failed",
		Subject: "Original subject", Body: "Original body", Data: snapshot, Error: "provider unavailable",
		AttemptNo: 1, NextRetryAt: &nextRetry,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpdateMonitor(ctx, monitor.ID, model.MonitorInput{
		Name: "Renamed feed", Type: model.MonitorTypeRSS, Enabled: false, IntervalSeconds: 60,
		Config: json.RawMessage(`{"url":"https://new.example/feed.xml"}`),
	}); err != nil {
		t.Fatal(err)
	}

	sender := &traceNotifier{}
	scheduler := New(db, checker.NewRegistry(), notifier.NewRegistry(sender), Options{})
	if _, err := scheduler.RetryAttempt(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	messages := sender.sentMessages()
	if len(messages) != 1 {
		t.Fatalf("retry sends = %d, want 1", len(messages))
	}
	data := messages[0].Data
	if data["url"] != "https://old.example/item" || data["title"] != "Snapshot global title" || data["summary"] != "Snapshot summary" || data["custom"] != "keep-me" {
		t.Fatalf("retry replaced persisted top-level values: %#v", data)
	}
	monitorData, _ := data["monitor"].(map[string]any)
	eventData, _ := data["event"].(map[string]any)
	ruleData, _ := data["rule"].(map[string]any)
	rssData, _ := data["rss"].(map[string]any)
	if monitorData["name"] != "Original feed" || eventData["fingerprint"] != "snapshot-fingerprint" || ruleData["name"] != "Original rule" || rssData["title"] != "Snapshot module title" {
		t.Fatalf("retry replaced persisted structured values: %#v", data)
	}
}

func TestRetryAttemptBackfillsMissingGlobalVariablesFromLegacySnapshot(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir()+"/watchbell.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	monitor, err := db.CreateMonitor(ctx, model.MonitorInput{
		Name: "Legacy feed", Type: model.MonitorTypeRSS, Enabled: false, IntervalSeconds: 60,
		Config: json.RawMessage(`{"url":"https://database.example/feed.xml"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := db.CreateCheckRun(ctx, monitor, "manual", monitor.Config)
	if err != nil {
		t.Fatal(err)
	}
	event, _, err := db.CreateEventForRun(ctx, monitor.ID, run.ID, model.EventData{
		Type: "rss.item", Fingerprint: "legacy-event",
		Payload: map[string]any{"rss": map[string]any{
			"title": "Database title",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	channel, err := db.CreateNotifyChannel(ctx, model.NotifyChannelInput{Name: "Legacy retry channel", Type: "trace_channel", Enabled: true, Config: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	legacyData, err := json.Marshal(map[string]any{
		"monitor": map[string]any{"id": monitor.ID, "name": "Legacy feed", "type": model.MonitorTypeRSS},
		"event":   map[string]any{"id": event.ID, "type": event.Type, "fingerprint": event.Fingerprint},
		"rule":    map[string]any{"id": 7, "name": "Legacy rule"},
		"rss": map[string]any{
			"title":   "Snapshot title",
			"summary": "Snapshot summary", "content": "Snapshot content", "author": "Snapshot author",
			"publishedAt": "2026-07-17T12:00:00Z",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	nextRetry := time.Now().UTC().Add(time.Minute)
	monitorID, eventID, channelID := monitor.ID, event.ID, channel.ID
	source, err := db.CreateNotificationAttempt(ctx, model.NotificationAttemptInput{
		MonitorID: &monitorID, EventID: &eventID, ChannelID: &channelID,
		ChannelName: channel.Name, ChannelType: channel.Type, Kind: "delivery", Status: "failed",
		Subject: "Legacy subject", Body: "Legacy body", Data: legacyData, Error: "provider unavailable",
		AttemptNo: 1, NextRetryAt: &nextRetry,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpdateMonitor(ctx, monitor.ID, model.MonitorInput{
		Name: "Changed legacy feed", Type: model.MonitorTypeRSS, Enabled: false, IntervalSeconds: 60,
		Config: json.RawMessage(`{"url":"https://current.example/feed.xml"}`),
	}); err != nil {
		t.Fatal(err)
	}

	sender := &traceNotifier{}
	scheduler := New(db, checker.NewRegistry(), notifier.NewRegistry(sender), Options{})
	if _, err := scheduler.RetryAttempt(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	messages := sender.sentMessages()
	if len(messages) != 1 {
		t.Fatalf("retry sends = %d, want 1", len(messages))
	}
	data := messages[0].Data
	want := map[string]any{
		"url": "https://database.example/feed.xml", "title": "Snapshot title", "summary": "Snapshot summary",
		"content": "Snapshot content", "author": "Snapshot author", "publishedAt": "2026-07-17T12:00:00Z", "status": "published",
	}
	for key, value := range want {
		if data[key] != value {
			t.Fatalf("backfilled %s = %#v, want %#v; data=%#v", key, data[key], value, data)
		}
	}
}

func TestRetryAttemptIsSingleWinnerAndRejectsSupersededSource(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir()+"/watchbell.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	channel, err := db.CreateNotifyChannel(ctx, model.NotifyChannelInput{Name: "Retry channel", Type: "trace_channel", Enabled: true, Config: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	channelID := channel.ID
	next := time.Now().UTC().Add(time.Minute)
	source, err := db.CreateNotificationAttempt(ctx, model.NotificationAttemptInput{
		ChannelID: &channelID, ChannelName: channel.Name, ChannelType: channel.Type, Kind: "test", Status: "failed",
		Subject: "retry once", Body: "body", AttemptNo: 1, NextRetryAt: &next,
	})
	if err != nil {
		t.Fatal(err)
	}

	sender := &traceNotifier{}
	scheduler := New(db, checker.NewRegistry(), notifier.NewRegistry(sender), Options{})
	const workers = 8
	start := make(chan struct{})
	results := make(chan error, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			_, err := scheduler.RetryAttempt(ctx, source.ID)
			results <- err
		}()
	}
	close(start)
	group.Wait()
	close(results)

	successes, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrRetryConflict):
			conflicts++
		default:
			t.Fatalf("RetryAttempt() unexpected error: %v", err)
		}
	}
	if successes != 1 || conflicts != workers-1 || sender.sentCount() != 1 {
		t.Fatalf("retry results: successes=%d conflicts=%d sends=%d", successes, conflicts, sender.sentCount())
	}

	source, err = db.GetNotificationAttempt(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if source.Retriable || !source.Resolved {
		t.Fatalf("source contract after retry: %#v", source)
	}
	if _, err := scheduler.RetryAttempt(ctx, source.ID); !errors.Is(err, ErrRetryConflict) {
		t.Fatalf("superseded retry error = %v, want ErrRetryConflict", err)
	}
	attempts, err := db.ListNotificationAttempts(ctx, 10)
	if err != nil || len(attempts) != 2 {
		t.Fatalf("attempt chain = %#v err=%v", attempts, err)
	}
	var successor model.NotificationAttempt
	for _, attempt := range attempts {
		if attempt.RetryOfID != nil && *attempt.RetryOfID == source.ID {
			successor = attempt
		}
	}
	if successor.ID == 0 {
		t.Fatalf("successor missing from chain: %#v", attempts)
	}
	if _, err := scheduler.RetryAttempt(ctx, successor.ID); !errors.Is(err, ErrRetryNotFailed) {
		t.Fatalf("sent retry error = %v, want ErrRetryNotFailed", err)
	}
}

func TestMatchedRuleIsTraceablySkippedDuringQuietHours(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir()+"/watchbell.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	monitor, err := db.CreateMonitor(ctx, model.MonitorInput{
		Name: "Quiet monitor", Type: "trace_test", Enabled: false, IntervalSeconds: 60, Config: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	channel, err := db.CreateNotifyChannel(ctx, model.NotifyChannelInput{
		Name: "Quiet channel", Type: "trace_channel", Enabled: true, Config: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	templateID := int64(1)
	ruleItem, err := db.CreateRule(ctx, model.RuleInput{
		MonitorID: monitor.ID, Name: "Quiet rule", Enabled: true, Condition: json.RawMessage(`{}`),
		NotifyChannelIDs: []int64{channel.ID}, TemplateID: &templateID,
		QuietHours: model.QuietHours{Enabled: true, Start: "22:00", End: "08:00", Timezone: "America/Los_Angeles"},
	})
	if err != nil {
		t.Fatal(err)
	}

	fixedNow := time.Date(2026, time.July, 15, 6, 30, 0, 0, time.UTC) // 23:30 PDT
	sender := &traceNotifier{}
	scheduler := New(db, checker.NewRegistry(traceChecker{}), notifier.NewRegistry(sender), Options{Now: func() time.Time { return fixedNow }})
	if err := scheduler.RunOnce(ctx, monitor.ID); err != nil {
		t.Fatal(err)
	}

	evaluations, err := db.ListRuleEvaluations(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(evaluations) != 1 || evaluations[0].Status != "skipped" || !strings.Contains(evaluations[0].Reason, "免打扰时段") || !strings.Contains(evaluations[0].Reason, "America/Los_Angeles") {
		t.Fatalf("unexpected quiet-hours evaluation: %#v", evaluations)
	}
	attempts, err := db.ListNotificationAttempts(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 0 || sender.sentCount() != 0 {
		t.Fatalf("quiet hours sent notifications: attempts=%#v sends=%d", attempts, sender.sentCount())
	}
	storedRule, err := db.GetRule(ctx, ruleItem.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedRule.LastFiredAt != nil {
		t.Fatal("quiet-hours skip must not start the cooldown")
	}
}

func TestMonitorFailureAndRecoveryAlertsAreOneShotTraceableAndRetryable(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir()+"/watchbell.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	channel, err := db.CreateNotifyChannel(ctx, model.NotifyChannelInput{
		Name: "On call", Type: "trace_channel", Enabled: true, Config: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	monitor, err := db.CreateMonitor(ctx, model.MonitorInput{
		Name: "Critical feed", Type: "health_sequence", Enabled: false, IntervalSeconds: 60, Config: json.RawMessage(`{}`),
		FailureAlertAfter: 2, FailureNotifyChannelIDs: []int64{channel.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	checkerImpl := &healthSequenceChecker{failuresRemaining: 3}
	sender := &traceNotifier{}
	scheduler := New(db, checker.NewRegistry(checkerImpl), notifier.NewRegistry(sender), Options{})

	if err := scheduler.RunOnce(ctx, monitor.ID); err == nil {
		t.Fatal("first failed check should return its checker error")
	}
	first, _ := db.GetMonitor(ctx, monitor.ID)
	if first.ConsecutiveFailures != 1 || first.FailureAlertActive {
		t.Fatalf("first failure activated alert too early: %#v", first)
	}
	if attempts, _ := db.ListNotificationAttempts(ctx, 10); len(attempts) != 0 {
		t.Fatalf("first failure sent an alert: %#v", attempts)
	}

	if err := scheduler.RunOnce(ctx, monitor.ID); err == nil {
		t.Fatal("second failed check should return its checker error")
	}
	second, _ := db.GetMonitor(ctx, monitor.ID)
	if second.ConsecutiveFailures != 2 || !second.FailureAlertActive {
		t.Fatalf("threshold failure did not activate incident: %#v", second)
	}
	attempts, _ := db.ListNotificationAttempts(ctx, 10)
	if len(attempts) != 1 || attempts[0].Kind != "monitor_failure" || attempts[0].MonitorID == nil || *attempts[0].MonitorID != monitor.ID || attempts[0].EventID != nil || attempts[0].Status != "sent" {
		t.Fatalf("failure attempt is not traceable: %#v", attempts)
	}
	messages := sender.sentMessages()
	if len(messages) != 1 || messages[0].Data["status"] != "error" || messages[0].Data["failures"] != 2 || messages[0].Data["error"] != "source failure 2" {
		t.Fatalf("failure template data is incomplete: %#v", messages)
	}
	monitorData, ok := messages[0].Data["monitor"].(map[string]any)
	if !ok || monitorData["id"] != monitor.ID || monitorData["name"] != monitor.Name {
		t.Fatalf("failure monitor data is incomplete: %#v", messages[0].Data)
	}

	if err := scheduler.RunOnce(ctx, monitor.ID); err == nil {
		t.Fatal("third failed check should return its checker error")
	}
	attempts, _ = db.ListNotificationAttempts(ctx, 10)
	if len(attempts) != 1 || sender.sentCount() != 1 {
		t.Fatalf("active incident emitted a duplicate failure alert: attempts=%#v sends=%d", attempts, sender.sentCount())
	}

	// Recovery delivery fails, but the incident must still close. The ordinary
	// attempt retry worker owns subsequent delivery instead of every healthy
	// check emitting another recovery notification.
	sender.setFail(true)
	if err := scheduler.RunOnce(ctx, monitor.ID); err != nil {
		t.Fatalf("recovered check failed because notification delivery failed: %v", err)
	}
	recovered, _ := db.GetMonitor(ctx, monitor.ID)
	if recovered.FailureAlertActive || recovered.ConsecutiveFailures != 0 {
		t.Fatalf("recovery did not close incident: %#v", recovered)
	}
	attempts, _ = db.ListNotificationAttempts(ctx, 10)
	if len(attempts) != 2 || attempts[0].Kind != "monitor_recovery" || attempts[0].Status != "failed" || attempts[0].NextRetryAt == nil || attempts[0].MonitorID == nil || *attempts[0].MonitorID != monitor.ID {
		t.Fatalf("failed recovery was not recorded for retry: %#v", attempts)
	}
	messages = sender.sentMessages()
	if len(messages) != 2 || messages[1].Data["status"] != "ok" || messages[1].Data["failures"] != 3 || messages[1].Data["error"] != "source failure 1" {
		t.Fatalf("recovery template data is incomplete: %#v", messages)
	}

	sender.setFail(false)
	retried, err := scheduler.RetryAttempt(ctx, attempts[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if retried.Status != "sent" || retried.Kind != "monitor_recovery" || retried.MonitorID == nil || *retried.MonitorID != monitor.ID {
		t.Fatalf("recovery retry lost its monitor link: %#v", retried)
	}
	messages = sender.sentMessages()
	if len(messages) != 3 || messages[2].Data["status"] != "ok" || messages[2].Data["error"] != "source failure 1" {
		t.Fatalf("recovery retry lost dynamic template data: %#v", messages)
	}
	if err := scheduler.RunOnce(ctx, monitor.ID); err != nil {
		t.Fatal(err)
	}
	afterAnotherSuccess, _ := db.ListNotificationAttempts(ctx, 10)
	if len(afterAnotherSuccess) != 3 {
		t.Fatalf("ordinary healthy check emitted another recovery: %#v", afterAnotherSuccess)
	}
}

func TestDisabledAlertChannelDoesNotConsumeFailureIncident(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir()+"/watchbell.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	channelInput := model.NotifyChannelInput{Name: "Temporarily disabled", Type: "trace_channel", Enabled: false, Config: json.RawMessage(`{}`)}
	channel, err := db.CreateNotifyChannel(ctx, channelInput)
	if err != nil {
		t.Fatal(err)
	}
	monitor, err := db.CreateMonitor(ctx, model.MonitorInput{
		Name: "Retry alert", Type: "health_sequence", Enabled: false, IntervalSeconds: 60, Config: json.RawMessage(`{}`),
		FailureAlertAfter: 1, FailureNotifyChannelIDs: []int64{channel.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	sender := &traceNotifier{}
	scheduler := New(db, checker.NewRegistry(&healthSequenceChecker{failuresRemaining: 2}), notifier.NewRegistry(sender), Options{})
	_ = scheduler.RunOnce(ctx, monitor.ID)
	stored, _ := db.GetMonitor(ctx, monitor.ID)
	if stored.FailureAlertActive || sender.sentCount() != 0 {
		t.Fatalf("disabled channel consumed incident: monitor=%#v sends=%d", stored, sender.sentCount())
	}
	channelInput.Enabled = true
	if _, err := db.UpdateNotifyChannel(ctx, channel.ID, channelInput); err != nil {
		t.Fatal(err)
	}
	_ = scheduler.RunOnce(ctx, monitor.ID)
	stored, _ = db.GetMonitor(ctx, monitor.ID)
	if !stored.FailureAlertActive || sender.sentCount() != 1 {
		t.Fatalf("later failed check did not retry alert after channel enabled: monitor=%#v sends=%d", stored, sender.sentCount())
	}
}

func TestRecoveredMonitorCancelsStaleFailureRetry(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir()+"/watchbell.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	channel, err := db.CreateNotifyChannel(ctx, model.NotifyChannelInput{Name: "Health", Type: "trace_channel", Enabled: true, Config: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	monitor, err := db.CreateMonitor(ctx, model.MonitorInput{
		Name: "Sequence", Type: "health_sequence", Enabled: false, IntervalSeconds: 60, Config: json.RawMessage(`{}`),
		FailureAlertAfter: 1, FailureNotifyChannelIDs: []int64{channel.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	sender := &traceNotifier{fail: true}
	scheduler := New(db, checker.NewRegistry(&healthSequenceChecker{failuresRemaining: 1}), notifier.NewRegistry(sender), Options{})
	if err := scheduler.RunOnce(ctx, monitor.ID); err == nil {
		t.Fatal("failure check unexpectedly succeeded")
	}
	attempts, _ := db.ListNotificationAttempts(ctx, 10)
	if len(attempts) != 1 || attempts[0].Kind != "monitor_failure" || attempts[0].NextRetryAt == nil {
		t.Fatalf("failure retry was not scheduled: %#v", attempts)
	}
	failureAttemptID := attempts[0].ID

	sender.setFail(false)
	if err := scheduler.RunOnce(ctx, monitor.ID); err != nil {
		t.Fatal(err)
	}
	staleFailure, err := db.GetNotificationAttempt(ctx, failureAttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if staleFailure.NextRetryAt != nil || !strings.Contains(staleFailure.Error, "monitor recovered") {
		t.Fatalf("stale failure retry was not stopped: %#v", staleFailure)
	}
	if sender.sentCount() != 2 {
		t.Fatalf("failure + recovery sends = %d, want 2", sender.sentCount())
	}
	if _, err := scheduler.RetryAttempt(ctx, failureAttemptID); !errors.Is(err, ErrRetryTargetUnavailable) {
		t.Fatalf("manual stale retry error = %v", err)
	}
	if sender.sentCount() != 2 {
		t.Fatalf("stale failure retry sent after recovery: %d", sender.sentCount())
	}
}

func TestArchivedChannelStopsNotificationRetry(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir()+"/watchbell.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	channel, err := db.CreateNotifyChannel(ctx, model.NotifyChannelInput{Name: "Temporary", Type: "trace_channel", Enabled: true, Config: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	next := time.Now().UTC().Add(time.Minute)
	attempt, err := db.CreateNotificationAttempt(ctx, model.NotificationAttemptInput{
		ChannelID: &channel.ID, ChannelName: channel.Name, ChannelType: channel.Type, Kind: "delivery", Status: "failed",
		Subject: "subject", Body: "body", Error: "provider failed", AttemptNo: 1, NextRetryAt: &next,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteNotifyChannel(ctx, channel.ID); err != nil {
		t.Fatal(err)
	}
	scheduler := New(db, checker.NewRegistry(), notifier.NewRegistry(&traceNotifier{}), Options{})
	if _, err := scheduler.RetryAttempt(ctx, attempt.ID); !errors.Is(err, ErrRetryTargetUnavailable) {
		t.Fatalf("retry error = %v", err)
	}
	stopped, err := db.GetNotificationAttempt(ctx, attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.NextRetryAt != nil || !strings.Contains(stopped.Error, "channel was archived") {
		t.Fatalf("archived-channel retry remained scheduled: %#v", stopped)
	}
}
