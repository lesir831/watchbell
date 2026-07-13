package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
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
