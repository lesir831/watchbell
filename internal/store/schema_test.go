package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/watchbell/watchbell/internal/model"
)

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
