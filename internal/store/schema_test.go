package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/watchbell/watchbell/internal/model"
)

func TestDefaultTemplateFlagMigratesLegacyDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	raw, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`CREATE TABLE notification_templates (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		subject_template TEXT NOT NULL,
		body_template TEXT NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);
	INSERT INTO notification_templates (id, name, subject_template, body_template, created_at, updated_at)
	VALUES (1, 'Legacy default', 'subject', 'body', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');`)
	if err != nil {
		raw.Close()
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
	template, err := db.GetDefaultNotificationTemplate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if template.ID != 1 || !template.IsDefault {
		t.Fatalf("legacy default was not migrated: %#v", template)
	}
}

func TestDefaultTemplateIncludesGitHubReleaseAndMigratesOldDefault(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "watchbell.db")

	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	template, err := store.GetNotificationTemplate(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(template.BodyTemplate, "${github.release.tagName}") {
		t.Fatalf("default template does not include GitHub release variables: %q", template.BodyTemplate)
	}
	if !template.IsDefault {
		t.Fatal("seeded template is not marked as the default")
	}

	oldDefault := `Monitor: ${monitor.name}
Type: ${event.type}
Time: ${event.time}

${rss.title}${testflight.message}${webpage.summary}

${rss.link}${testflight.url}${webpage.url}`
	if _, err := store.db.ExecContext(ctx, `UPDATE notification_templates SET body_template = ? WHERE id = 1`, oldDefault); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	template, err = store.GetNotificationTemplate(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(template.BodyTemplate, "${github.release.url}") {
		t.Fatalf("old default template was not migrated: %q", template.BodyTemplate)
	}
}

func TestMonitorUpdateOnlyResetsStateWhenConfigMeaningChanges(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "watchbell.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	monitor, err := store.CreateMonitor(ctx, model.MonitorInput{Name: "Example", Type: "rss", Enabled: true, IntervalSeconds: 60, Config: json.RawMessage(`{"a":1,"b":2}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateMonitorCheckResult(ctx, monitor.ID, model.CheckResult{Status: "ok", State: map[string]any{"seen": true}}, nil); err != nil {
		t.Fatal(err)
	}
	_, err = store.UpdateMonitor(ctx, monitor.ID, model.MonitorInput{Name: "Example", Type: "rss", Enabled: true, IntervalSeconds: 60, Config: json.RawMessage(`{"b":2,"a":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	unchanged, _ := store.GetMonitor(ctx, monitor.ID)
	if unchanged.LastCheckedAt == nil {
		t.Fatal("reordered JSON keys must not reset monitor history")
	}
	_, err = store.UpdateMonitor(ctx, monitor.ID, model.MonitorInput{Name: "Example", Type: "rss", Enabled: true, IntervalSeconds: 60, Config: json.RawMessage(`{"a":2,"b":2}`)})
	if err != nil {
		t.Fatal(err)
	}
	changed, _ := store.GetMonitor(ctx, monitor.ID)
	if changed.LastCheckedAt != nil || changed.LastStatus != "" {
		t.Fatal("meaningful config changes must reset checker state")
	}
}

func TestSQLiteConnectionPragmasApplyAcrossPool(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "watchbell.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	connections := make([]*sql.Conn, 0, 4)
	for range 4 {
		connection, err := store.db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, connection)
	}
	defer func() {
		for _, connection := range connections {
			_ = connection.Close()
		}
	}()

	for index, connection := range connections {
		var foreignKeys, busyTimeout int
		var journalMode string
		if err := connection.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
			t.Fatal(err)
		}
		if err := connection.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
			t.Fatal(err)
		}
		if err := connection.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journalMode); err != nil {
			t.Fatal(err)
		}
		if foreignKeys != 1 || busyTimeout != 5000 || !strings.EqualFold(journalMode, "wal") {
			t.Fatalf("connection %d pragmas = foreign_keys:%d busy_timeout:%d journal_mode:%q", index, foreignKeys, busyTimeout, journalMode)
		}
	}
}

func TestNotificationAttemptMonitorBackfillsOnMigration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "watchbell.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	monitor, err := db.CreateMonitor(ctx, model.MonitorInput{Name: "Migration trace", Type: "rss", Enabled: true, IntervalSeconds: 60, Config: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	event, _, err := db.CreateEvent(ctx, monitor.ID, model.EventData{Type: "rss.item", Fingerprint: "migration-trace", Payload: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	eventID := event.ID
	attempt, err := db.CreateNotificationAttempt(ctx, model.NotificationAttemptInput{EventID: &eventID, ChannelName: "Legacy", ChannelType: "webhook", Status: "sent"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `UPDATE notification_attempts SET monitor_id = NULL WHERE id = ?`, attempt.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	attempt, err = db.GetNotificationAttempt(ctx, attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.MonitorID == nil || *attempt.MonitorID != monitor.ID {
		t.Fatalf("migration did not backfill monitor: %#v", attempt)
	}
}
