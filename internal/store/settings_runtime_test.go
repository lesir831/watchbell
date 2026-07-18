package store

import (
	"context"
	"testing"
	"time"
)

func TestRuntimeSettingsUseDefaultsAndPersistAuditedOverrides(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, t.TempDir()+"/watchbell.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	defaults := RuntimeSettings{
		SessionTTL:       7 * 24 * time.Hour,
		HistoryRetention: UniformHistoryRetention(90*24*time.Hour, 321),
	}
	loaded, err := db.GetRuntimeSettings(ctx, defaults)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SessionTTL != defaults.SessionTTL || loaded.HistoryRetention.EventAge != defaults.HistoryRetention.EventAge || loaded.HistoryRetention.BatchSize != 321 {
		t.Fatalf("defaults not preserved: %#v", loaded)
	}

	override := RuntimeSettings{
		SessionTTL:       8 * time.Hour,
		HistoryRetention: UniformHistoryRetention(30*24*time.Hour, 321),
	}
	if err := db.SetRuntimeSettingsAudited(ctx, override, "admin"); err != nil {
		t.Fatal(err)
	}
	loaded, err = db.GetRuntimeSettings(ctx, defaults)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SessionTTL != override.SessionTTL || loaded.HistoryRetention.EventAge != override.HistoryRetention.EventAge || loaded.HistoryRetention.AuditLogAge != override.HistoryRetention.AuditLogAge {
		t.Fatalf("override not loaded: %#v", loaded)
	}
	if loaded.HistoryRetention.BatchSize != defaults.HistoryRetention.BatchSize {
		t.Fatalf("deployment batch size changed: %d", loaded.HistoryRetention.BatchSize)
	}
	audit, err := db.ListAuditLogs(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(audit) != 1 || audit[0].EntityType != "settings" || audit[0].Actor != "admin" {
		t.Fatalf("unexpected audit rows: %#v", audit)
	}
}
