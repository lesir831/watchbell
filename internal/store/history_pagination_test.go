package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/watchbell/watchbell/internal/model"
)

func TestHistoryPaginationAndFilters(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "watchbell.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	monitor, err := db.CreateMonitor(ctx, model.MonitorInput{
		Name: "Feed", Type: model.MonitorTypeRSS, Enabled: true,
		IntervalSeconds: 60, Config: json.RawMessage(`{"url":"https://example.com/feed"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	channel, err := db.CreateNotifyChannel(ctx, model.NotifyChannelInput{
		Name: "Bark", Type: model.ChannelTypeBark, Enabled: true, Config: json.RawMessage(`{"key":"secret"}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	runs := make([]model.CheckRun, 0, 5)
	events := make([]model.Event, 0, 5)
	evaluations := make([]model.RuleEvaluation, 0, 5)
	for index := 0; index < 5; index++ {
		trigger := "scheduled"
		status := "ok"
		if index%2 == 1 {
			trigger = "manual"
			status = "error"
		}
		run, err := db.CreateCheckRun(ctx, monitor, trigger, json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		if err := db.FinishCheckRun(ctx, run.ID, status, "done", nil, 1, run.StartedAt); err != nil {
			t.Fatal(err)
		}
		run, _ = db.GetCheckRun(ctx, run.ID)
		runs = append(runs, run)

		event, created, err := db.CreateEventForRun(ctx, monitor.ID, run.ID, model.EventData{
			Type: "rss.item", Fingerprint: "event-" + string(rune('a'+index)), Payload: map[string]any{"index": index},
		})
		if err != nil || !created {
			t.Fatalf("create event %d: created=%v err=%v", index, created, err)
		}
		events = append(events, event)
		evaluation, err := db.CreateRuleEvaluation(ctx, event.ID, nil, "Rule", status, "test", []string{"rss.title"})
		if err != nil {
			t.Fatal(err)
		}
		evaluations = append(evaluations, evaluation)
		eventID, channelID, evaluationID := event.ID, channel.ID, evaluation.ID
		if _, err := db.CreateNotificationAttempt(ctx, model.NotificationAttemptInput{
			EventID: &eventID, RuleEvaluationID: &evaluationID, ChannelID: &channelID,
			ChannelName: channel.Name, ChannelType: channel.Type, Kind: "delivery", Status: status,
		}); err != nil {
			t.Fatal(err)
		}
		entityID := int64(index + 1)
		if err := db.CreateAuditLog(ctx, "admin", trigger, "monitor", &entityID, "changed", map[string]any{"index": index}); err != nil {
			t.Fatal(err)
		}
	}

	runPage, err := db.ListCheckRunsPage(ctx, CheckRunFilter{PageRequest: PageRequest{Page: 2, PageSize: 2}, MonitorID: monitor.ID})
	if err != nil {
		t.Fatal(err)
	}
	if runPage.Total != 5 || runPage.TotalPages != 3 || len(runPage.Items) != 2 || runPage.Items[0].ID != runs[2].ID {
		t.Fatalf("unexpected check run page: %+v", runPage)
	}
	errorRuns, err := db.ListCheckRunsPage(ctx, CheckRunFilter{Status: "error", Trigger: "manual"})
	if err != nil || errorRuns.Total != 2 {
		t.Fatalf("unexpected filtered check runs: page=%+v err=%v", errorRuns, err)
	}

	eventPage, err := db.ListEventsPage(ctx, EventFilter{PageRequest: PageRequest{Page: 1, PageSize: 2}, CheckRunID: runs[3].ID, Type: "rss.item"})
	if err != nil || eventPage.Total != 1 || eventPage.Items[0].ID != events[3].ID {
		t.Fatalf("unexpected event page: page=%+v err=%v", eventPage, err)
	}
	evaluationPage, err := db.ListRuleEvaluationsPage(ctx, RuleEvaluationFilter{EventID: events[1].ID, Status: "error"})
	if err != nil || evaluationPage.Total != 1 || evaluationPage.Items[0].ID != evaluations[1].ID {
		t.Fatalf("unexpected evaluation page: page=%+v err=%v", evaluationPage, err)
	}
	attemptPage, err := db.ListNotificationAttemptsPage(ctx, NotificationAttemptFilter{ChannelID: channel.ID, Status: "ok", Kind: "delivery"})
	if err != nil || attemptPage.Total != 3 {
		t.Fatalf("unexpected attempt page: page=%+v err=%v", attemptPage, err)
	}
	if _, err := db.db.ExecContext(ctx, `UPDATE notification_attempts SET monitor_id = NULL WHERE event_id = ?`, events[0].ID); err != nil {
		t.Fatal(err)
	}
	monitorAttempts, err := db.ListNotificationAttemptsPage(ctx, NotificationAttemptFilter{MonitorID: monitor.ID})
	if err != nil || monitorAttempts.Total != 5 {
		t.Fatalf("monitor filter lost a legacy attempt: page=%+v err=%v", monitorAttempts, err)
	}
	auditPage, err := db.ListAuditLogsPage(ctx, AuditLogFilter{Actor: "admin", Action: "manual", EntityType: "monitor"})
	if err != nil || auditPage.Total != 2 {
		t.Fatalf("unexpected audit page: page=%+v err=%v", auditPage, err)
	}

	from, to := time.Now().UTC(), time.Now().UTC().Add(-time.Hour)
	if _, err := db.ListEventsPage(ctx, EventFilter{HistoryTimeRange: HistoryTimeRange{From: &from, To: &to}}); err == nil {
		t.Fatal("expected reversed time range to fail")
	}
	legacy, err := db.ListEvents(ctx, 2)
	if err != nil || len(legacy) != 2 {
		t.Fatalf("legacy limit API must remain compatible: items=%d err=%v", len(legacy), err)
	}
}

func TestHistoryPageNormalizesBoundsAndReturnsEmptyArray(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "watchbell.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	page, err := db.ListAuditLogsPage(ctx, AuditLogFilter{PageRequest: PageRequest{Page: -1, PageSize: 10_000}})
	if err != nil {
		t.Fatal(err)
	}
	if page.Page != 1 || page.PageSize != 500 || page.Total != 0 || page.TotalPages != 0 || page.Items == nil {
		t.Fatalf("unexpected normalized empty page: %+v", page)
	}
}

func TestHistoryTimeFilterHandlesMixedRFC3339Precision(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "watchbell.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.CreateAuditLog(ctx, "admin", "test", "monitor", nil, "fractional", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `UPDATE audit_logs SET created_at = '2026-01-01T00:00:00.500Z'`); err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	page, err := db.ListAuditLogsPage(ctx, AuditLogFilter{HistoryTimeRange: HistoryTimeRange{From: &from}})
	if err != nil || page.Total != 1 {
		t.Fatalf("fractional timestamp missing at whole-second boundary: page=%+v err=%v", page, err)
	}
}
