package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/watchbell/watchbell/internal/model"
)

func TestMonitorFailureAlertSettingsAndIncidentStateRoundTrip(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "watchbell.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	channel, err := db.CreateNotifyChannel(ctx, model.NotifyChannelInput{Name: "Phone", Type: "bark", Enabled: true, Config: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	input := model.MonitorInput{
		Name: "Feed", Type: "rss", Enabled: true, IntervalSeconds: 60, Config: json.RawMessage(`{"url":"https://example.com/feed"}`),
		FailureAlertAfter: 2, FailureNotifyChannelIDs: []int64{channel.ID},
	}
	monitor, err := db.CreateMonitor(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if monitor.FailureAlertAfter != 2 || len(monitor.FailureNotifyChannelIDs) != 1 || monitor.FailureNotifyChannelIDs[0] != channel.ID || monitor.FailureAlertActive {
		t.Fatalf("failure settings did not round trip: %#v", monitor)
	}
	checkErr := assertError("unavailable")
	if err := db.UpdateMonitorCheckResult(ctx, monitor.ID, model.CheckResult{Status: "error"}, checkErr); err != nil {
		t.Fatal(err)
	}
	activated, err := db.TryActivateMonitorFailureAlert(ctx, monitor.ID)
	if err != nil {
		t.Fatal(err)
	}
	if activated {
		t.Fatal("incident activated below threshold")
	}
	if err := db.UpdateMonitorCheckResult(ctx, monitor.ID, model.CheckResult{Status: "error"}, checkErr); err != nil {
		t.Fatal(err)
	}
	activated, err = db.TryActivateMonitorFailureAlert(ctx, monitor.ID)
	if err != nil || !activated {
		t.Fatalf("incident was not activated at threshold: activated=%v err=%v", activated, err)
	}
	activated, err = db.TryActivateMonitorFailureAlert(ctx, monitor.ID)
	if err != nil || activated {
		t.Fatalf("incident activation was not idempotent: activated=%v err=%v", activated, err)
	}
	updated, err := db.UpdateMonitor(ctx, monitor.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.FailureAlertActive {
		t.Fatal("ordinary settings update unexpectedly reopened the active incident")
	}
	input.FailureAlertAfter = 0
	input.FailureNotifyChannelIDs = nil
	updated, err = db.UpdateMonitor(ctx, monitor.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if updated.FailureAlertActive || updated.FailureNotifyChannelIDs == nil || len(updated.FailureNotifyChannelIDs) != 0 {
		t.Fatalf("disabling alerts did not clear incident/settings: %#v", updated)
	}
}

func TestFailureAlertColumnsMigrateFromLegacyMonitorTable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	raw, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`CREATE TABLE monitors (
		id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, type TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1, interval_seconds INTEGER NOT NULL DEFAULT 300,
		config_json TEXT NOT NULL DEFAULT '{}', state_json TEXT NOT NULL DEFAULT '{}',
		last_checked_at TEXT, last_status TEXT NOT NULL DEFAULT '', last_message TEXT NOT NULL DEFAULT '',
		last_error TEXT NOT NULL DEFAULT '', consecutive_failures INTEGER NOT NULL DEFAULT 0,
		deleted_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
	)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`INSERT INTO monitors (name, type, enabled, interval_seconds, config_json, state_json, created_at, updated_at) VALUES ('Legacy', 'rss', 1, 60, '{}', '{}', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	monitor, err := db.GetMonitor(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if monitor.FailureAlertAfter != 0 || monitor.FailureAlertActive || monitor.FailureNotifyChannelIDs == nil || len(monitor.FailureNotifyChannelIDs) != 0 {
		t.Fatalf("legacy monitor received invalid alert defaults: %#v", monitor)
	}
	for _, column := range []struct{ table, name string }{{"monitors", "failure_alert_after"}, {"monitors", "failure_notify_channel_ids_json"}, {"monitors", "failure_alert_active"}, {"notification_attempts", "monitor_id"}} {
		exists, err := db.columnExists(ctx, column.table, column.name)
		if err != nil || !exists {
			t.Fatalf("migration missing %s.%s: exists=%v err=%v", column.table, column.name, exists, err)
		}
	}
}

type assertError string

func (e assertError) Error() string { return string(e) }
