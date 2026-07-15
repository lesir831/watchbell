package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/watchbell/watchbell/internal/model"
)

func TestRuleQuietHoursMigrationAndRoundTrip(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "watchbell.db")
	legacy, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`
CREATE TABLE rules (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  monitor_id INTEGER NOT NULL,
  name TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  condition_json TEXT NOT NULL DEFAULT '{}',
  notify_channel_ids_json TEXT NOT NULL DEFAULT '[]',
  template_id INTEGER,
  cooldown_seconds INTEGER NOT NULL DEFAULT 0,
  last_fired_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
INSERT INTO rules (monitor_id, name, enabled, created_at, updated_at)
VALUES (42, 'Legacy rule', 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');`)
	if err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if exists, err := db.columnExists(ctx, "rules", "quiet_hours_json"); err != nil || !exists {
		t.Fatalf("quiet_hours_json migration failed: exists=%v err=%v", exists, err)
	}
	// Startup reference repair archives this deliberately orphaned legacy rule,
	// but the migration must still add a valid disabled quiet-hours value.
	var quietHoursJSON string
	if err := db.db.QueryRowContext(ctx, `SELECT quiet_hours_json FROM rules WHERE id = 1`).Scan(&quietHoursJSON); err != nil {
		t.Fatal(err)
	}
	var quietHours model.QuietHours
	if err := json.Unmarshal([]byte(quietHoursJSON), &quietHours); err != nil {
		t.Fatal(err)
	}
	if quietHours.Enabled || quietHours.Start != "" {
		t.Fatalf("legacy rule did not receive a disabled default: %#v", quietHours)
	}

	monitor, err := db.CreateMonitor(ctx, model.MonitorInput{
		Name: "Feed", Type: model.MonitorTypeRSS, Enabled: true, IntervalSeconds: 300,
		Config: json.RawMessage(`{"url":"https://example.com/feed.xml"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := db.CreateRule(ctx, model.RuleInput{
		MonitorID: monitor.ID, Name: "Night silence", Enabled: true, Condition: json.RawMessage(`{}`),
		NotifyChannelIDs: []int64{}, QuietHours: model.QuietHours{Enabled: true, Start: "22:00", End: "08:00", Timezone: "Asia/Shanghai"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.QuietHours.Start != "22:00" || created.QuietHours.Timezone != "Asia/Shanghai" {
		t.Fatalf("quiet hours were not persisted: %#v", created.QuietHours)
	}

	updated, err := db.UpdateRule(ctx, created.ID, model.RuleInput{
		MonitorID: monitor.ID, Name: created.Name, Enabled: true, Condition: created.Condition,
		NotifyChannelIDs: []int64{}, QuietHours: model.QuietHours{Enabled: true, Start: "23:30", End: "07:15", Timezone: "America/Los_Angeles"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.QuietHours.End != "07:15" || updated.QuietHours.Timezone != "America/Los_Angeles" {
		t.Fatalf("updated quiet hours were not persisted: %#v", updated.QuietHours)
	}
}
