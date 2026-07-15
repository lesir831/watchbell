package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/watchbell/watchbell/internal/model"
)

func TestConfigImportRejectsDefaultTemplateRenameCollision(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, t.TempDir()+"/watchbell.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.CreateNotificationTemplate(ctx, model.NotificationTemplateInput{
		Name: "Imported default", SubjectTemplate: "custom subject", BodyTemplate: "custom body",
	}); err != nil {
		t.Fatal(err)
	}

	backup := model.ConfigBackup{
		Version: model.ConfigBackupVersion, ExportedAt: time.Now().UTC(), IncludesSecrets: true,
		Channels: []model.ConfigBackupChannel{}, Monitors: []model.ConfigBackupMonitor{}, Rules: []model.ConfigBackupRule{},
		Templates: []model.ConfigBackupTemplate{{
			ID: 99, Name: "Imported default", SubjectTemplate: "new default subject", BodyTemplate: "new default body", IsDefault: true,
		}},
	}
	_, err = db.ImportConfigMerge(ctx, backup)
	var importErr *ConfigImportError
	if !errors.As(err, &importErr) || importErr.Field != "backup.templates.0.name" {
		t.Fatalf("ImportConfigMerge() error = %#v, want template name conflict", err)
	}

	defaultTemplate, err := db.GetDefaultNotificationTemplate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if defaultTemplate.Name != "Default" || defaultTemplate.SubjectTemplate == "new default subject" {
		t.Fatalf("failed import changed default template: %#v", defaultTemplate)
	}
	templates, err := db.ListNotificationTemplates(ctx)
	if err != nil || len(templates) != 2 {
		t.Fatalf("failed import changed template count: %#v err=%v", templates, err)
	}
}
