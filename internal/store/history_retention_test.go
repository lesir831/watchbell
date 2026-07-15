package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/watchbell/watchbell/internal/model"
)

func TestCleanupHistoryRespectsRetentionAndActiveWork(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "watchbell.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Now().UTC()
	old := formatTime(now.Add(-48 * time.Hour))
	monitor, err := db.CreateMonitor(ctx, model.MonitorInput{Name: "Feed", Type: "rss", Enabled: true, IntervalSeconds: 60, Config: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	channel, err := db.CreateNotifyChannel(ctx, model.NotifyChannelInput{Name: "Bark", Type: "bark", Enabled: true, Config: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}

	oldRun, _ := db.CreateCheckRun(ctx, monitor, "scheduled", json.RawMessage(`{}`))
	if err := db.FinishCheckRun(ctx, oldRun.ID, "ok", "", nil, 1, oldRun.StartedAt); err != nil {
		t.Fatal(err)
	}
	oldEvent, _, err := db.CreateEventForRun(ctx, monitor.ID, oldRun.ID, model.EventData{Type: "rss.item", Fingerprint: "old", Payload: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MarkOutboxProcessed(ctx, oldEvent.ID); err != nil {
		t.Fatal(err)
	}
	oldEvaluation, err := db.CreateRuleEvaluation(ctx, oldEvent.ID, nil, "Rule", "matched", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	eventID, evaluationID, channelID := oldEvent.ID, oldEvaluation.ID, channel.ID
	firstAttempt, err := db.CreateNotificationAttempt(ctx, model.NotificationAttemptInput{
		EventID: &eventID, RuleEvaluationID: &evaluationID, ChannelID: &channelID,
		ChannelName: channel.Name, ChannelType: channel.Type, Kind: "delivery", Status: "failed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateNotificationAttempt(ctx, model.NotificationAttemptInput{
		EventID: &eventID, RuleEvaluationID: &evaluationID, ChannelID: &channelID, RetryOfID: &firstAttempt.ID,
		ChannelName: channel.Name, ChannelType: channel.Type, Kind: "delivery", Status: "sent", AttemptNo: 2,
	}); err != nil {
		t.Fatal(err)
	}

	pendingEvent, _, err := db.CreateEventForRun(ctx, monitor.ID, 0, model.EventData{Type: "rss.item", Fingerprint: "pending", Payload: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	freshEvent, _, err := db.CreateEventForRun(ctx, monitor.ID, 0, model.EventData{Type: "rss.item", Fingerprint: "fresh", Payload: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MarkOutboxProcessed(ctx, freshEvent.ID); err != nil {
		t.Fatal(err)
	}

	standalone, err := db.CreateNotificationAttempt(ctx, model.NotificationAttemptInput{ChannelID: &channelID, ChannelName: channel.Name, ChannelType: channel.Type, Kind: "test", Status: "sent"})
	if err != nil {
		t.Fatal(err)
	}
	nextRetry := now.Add(time.Hour)
	scheduled, err := db.CreateNotificationAttempt(ctx, model.NotificationAttemptInput{ChannelID: &channelID, ChannelName: channel.Name, ChannelType: channel.Type, Kind: "test", Status: "failed", NextRetryAt: &nextRetry})
	if err != nil {
		t.Fatal(err)
	}
	running, err := db.CreateCheckRun(ctx, monitor, "scheduled", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	oldAuditID := int64(99)
	if err := db.CreateAuditLog(ctx, "admin", "update", "monitor", &oldAuditID, "old", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateAuditLog(ctx, "admin", "update", "monitor", &oldAuditID, "fresh", map[string]any{}); err != nil {
		t.Fatal(err)
	}

	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`UPDATE check_runs SET created_at = ? WHERE id IN (?, ?)`, []any{old, oldRun.ID, running.ID}},
		{`UPDATE events SET created_at = ? WHERE id IN (?, ?)`, []any{old, oldEvent.ID, pendingEvent.ID}},
		{`UPDATE rule_evaluations SET created_at = ? WHERE id = ?`, []any{old, oldEvaluation.ID}},
		{`UPDATE notification_attempts SET created_at = ? WHERE id <> ?`, []any{old, scheduled.ID}},
		{`UPDATE notification_attempts SET created_at = ? WHERE id = ?`, []any{old, scheduled.ID}},
		{`UPDATE audit_logs SET created_at = ? WHERE summary = 'old'`, []any{old}},
	} {
		if _, err := db.db.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}

	result, err := db.CleanupHistory(ctx, UniformHistoryRetention(24*time.Hour, 100), now)
	if err != nil {
		t.Fatal(err)
	}
	if result.EventsDeleted != 1 || result.CheckRunsDeleted != 1 || result.RuleEvaluationsDeleted != 1 || result.NotificationAttemptsDeleted != 3 || result.AuditLogsDeleted != 1 {
		t.Fatalf("unexpected cleanup result: %+v", result)
	}
	if _, err := db.GetEvent(ctx, oldEvent.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("old event still exists: %v", err)
	}
	if _, err := db.GetRuleEvaluation(ctx, oldEvaluation.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("old rule evaluation still exists: %v", err)
	}
	if _, err := db.GetNotificationAttempt(ctx, standalone.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("old standalone attempt still exists: %v", err)
	}
	for name, id := range map[string]int64{"pending event": pendingEvent.ID, "fresh event": freshEvent.ID} {
		if _, err := db.GetEvent(ctx, id); err != nil {
			t.Fatalf("%s must be retained: %v", name, err)
		}
	}
	if _, err := db.GetNotificationAttempt(ctx, scheduled.ID); err != nil {
		t.Fatalf("scheduled retry must be retained: %v", err)
	}
	if _, err := db.GetCheckRun(ctx, running.ID); err != nil {
		t.Fatalf("running check must be retained: %v", err)
	}
}

func TestCleanupHistoryHonorsBatchSizeAndDisabledCategories(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "watchbell.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for index := 0; index < 3; index++ {
		if err := db.CreateAuditLog(ctx, "admin", "test", "monitor", nil, "old", map[string]any{}); err != nil {
			t.Fatal(err)
		}
	}
	old := formatTime(time.Now().UTC().Add(-48 * time.Hour))
	if _, err := db.db.ExecContext(ctx, `UPDATE audit_logs SET created_at = ?`, old); err != nil {
		t.Fatal(err)
	}
	result, err := db.CleanupHistory(ctx, HistoryRetentionPolicy{AuditLogAge: 24 * time.Hour, BatchSize: 1}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if result.AuditLogsDeleted != 1 || result.EventsDeleted != 0 {
		t.Fatalf("unexpected cleanup result: %+v", result)
	}
	page, err := db.ListAuditLogsPage(ctx, AuditLogFilter{})
	if err != nil || page.Total != 2 {
		t.Fatalf("expected two audit logs after one-item batch: page=%+v err=%v", page, err)
	}
}

func TestEventCleanupBackfillsLegacyAttemptMonitorBeforeDetach(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "watchbell.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Now().UTC()
	monitor, err := db.CreateMonitor(ctx, model.MonitorInput{Name: "Legacy trace", Type: "rss", Enabled: true, IntervalSeconds: 60, Config: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	event, _, err := db.CreateEvent(ctx, monitor.ID, model.EventData{Type: "rss.item", Fingerprint: "legacy-attempt", Payload: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MarkOutboxProcessed(ctx, event.ID); err != nil {
		t.Fatal(err)
	}
	eventID := event.ID
	attempt, err := db.CreateNotificationAttempt(ctx, model.NotificationAttemptInput{
		EventID: &eventID, ChannelName: "Legacy channel", ChannelType: "webhook", Kind: "delivery", Status: "sent",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `UPDATE events SET created_at = ? WHERE id = ?`, formatTime(now.Add(-48*time.Hour)), event.ID); err != nil {
		t.Fatal(err)
	}

	result, err := db.CleanupHistory(ctx, HistoryRetentionPolicy{EventAge: 24 * time.Hour}, now)
	if err != nil {
		t.Fatal(err)
	}
	if result.EventsDeleted != 1 || result.NotificationAttemptsDeleted != 0 {
		t.Fatalf("cleanup result = %+v", result)
	}
	stored, err := db.GetNotificationAttempt(ctx, attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.MonitorID == nil || *stored.MonitorID != monitor.ID || stored.EventID != nil {
		t.Fatalf("detached attempt lost monitor trace: %#v", stored)
	}
	page, err := db.ListNotificationAttemptsPage(ctx, NotificationAttemptFilter{MonitorID: monitor.ID})
	if err != nil || page.Total != 1 || page.Items[0].ID != attempt.ID {
		t.Fatalf("monitor filter lost detached attempt: %#v err=%v", page, err)
	}
}

func TestCleanupHistoryRetainsActivelyClaimedManualRetry(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "watchbell.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Now().UTC()
	old := formatTime(now.Add(-48 * time.Hour))
	monitor, err := db.CreateMonitor(ctx, model.MonitorInput{Name: "Claimed retry trace", Type: "rss", Enabled: true, IntervalSeconds: 60, Config: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	event, _, err := db.CreateEvent(ctx, monitor.ID, model.EventData{Type: "rss.item", Fingerprint: "claimed-retry", Payload: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MarkOutboxProcessed(ctx, event.ID); err != nil {
		t.Fatal(err)
	}
	eventID := event.ID
	attempt, err := db.CreateNotificationAttempt(ctx, model.NotificationAttemptInput{
		EventID: &eventID, ChannelName: "Slow provider", ChannelType: "webhook", Kind: "delivery", Status: "failed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `UPDATE events SET created_at = ? WHERE id = ?`, old, event.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `UPDATE notification_attempts SET created_at = ? WHERE id = ?`, old, attempt.ID); err != nil {
		t.Fatal(err)
	}
	claimed, err := db.ClaimNotificationAttemptNow(ctx, attempt.ID, now)
	if err != nil || !claimed {
		t.Fatalf("manual retry claim = %v err=%v", claimed, err)
	}

	result, err := db.CleanupHistory(ctx, UniformHistoryRetention(24*time.Hour, 100), now)
	if err != nil {
		t.Fatal(err)
	}
	if result.EventsDeleted != 0 || result.NotificationAttemptsDeleted != 0 {
		t.Fatalf("active retry was cleaned up: %+v", result)
	}
	if _, err := db.GetEvent(ctx, event.ID); err != nil {
		t.Fatalf("active retry event was removed: %v", err)
	}
	if _, err := db.GetNotificationAttempt(ctx, attempt.ID); err != nil {
		t.Fatalf("active retry source was removed: %v", err)
	}

	if err := db.StopNotificationRetry(ctx, attempt.ID, "test complete"); err != nil {
		t.Fatal(err)
	}
	result, err = db.CleanupHistory(ctx, UniformHistoryRetention(24*time.Hour, 100), now)
	if err != nil {
		t.Fatal(err)
	}
	if result.EventsDeleted != 1 || result.NotificationAttemptsDeleted != 1 {
		t.Fatalf("finished retry history was not cleaned up: %+v", result)
	}
}

func TestCleanupHistoryHandlesMixedRFC3339Precision(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "watchbell.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.CreateAuditLog(ctx, "admin", "test", "monitor", nil, "whole-second", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `UPDATE audit_logs SET created_at = '2026-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 1, 500_000_000, time.UTC)
	result, err := db.CleanupHistory(ctx, HistoryRetentionPolicy{AuditLogAge: time.Second}, now)
	if err != nil {
		t.Fatal(err)
	}
	if result.AuditLogsDeleted != 1 {
		t.Fatalf("whole-second record was not deleted at fractional cutoff: %+v", result)
	}
}

func TestOutboxMovesToDeadLetterAndNoLongerBlocksRetention(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "watchbell.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	monitor, err := db.CreateMonitor(ctx, model.MonitorInput{Name: "Feed", Type: "rss", Enabled: true, IntervalSeconds: 60, Config: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	event, _, err := db.CreateEvent(ctx, monitor.ID, model.EventData{Type: "rss.item", Fingerprint: "poison", Payload: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MarkOutboxFailed(ctx, event.ID, 10, errors.New("permanent dispatch failure")); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := db.db.QueryRowContext(ctx, `SELECT status FROM event_outbox WHERE event_id = ?`, event.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "dead_letter" {
		t.Fatalf("outbox status = %q", status)
	}
	if _, err := db.db.ExecContext(ctx, `UPDATE events SET created_at = ? WHERE id = ?`, formatTime(time.Now().UTC().Add(-48*time.Hour)), event.ID); err != nil {
		t.Fatal(err)
	}
	result, err := db.CleanupHistory(ctx, HistoryRetentionPolicy{EventAge: 24 * time.Hour}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if result.EventsDeleted != 1 {
		t.Fatalf("dead-letter event still blocked retention: %+v", result)
	}
}
