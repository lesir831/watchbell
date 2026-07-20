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
		Timezone:         "UTC",
		DateTimeFormat:   "yyyy-MM-dd HH:mm:ss",
	}
	loaded, err := db.GetRuntimeSettings(ctx, defaults)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SessionTTL != defaults.SessionTTL || loaded.HistoryRetention.EventAge != defaults.HistoryRetention.EventAge || loaded.HistoryRetention.BatchSize != 321 || loaded.Timezone != defaults.Timezone || loaded.DateTimeFormat != defaults.DateTimeFormat {
		t.Fatalf("defaults not preserved: %#v", loaded)
	}

	override := RuntimeSettings{
		SessionTTL:       8 * time.Hour,
		HistoryRetention: UniformHistoryRetention(30*24*time.Hour, 321),
		Timezone:         "Asia/Shanghai",
		DateTimeFormat:   "yyyy-MM-dd HH:mm",
	}
	if err := db.SetRuntimeSettingsAudited(ctx, override, "admin"); err != nil {
		t.Fatal(err)
	}
	loaded, err = db.GetRuntimeSettings(ctx, defaults)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SessionTTL != override.SessionTTL || loaded.HistoryRetention.EventAge != override.HistoryRetention.EventAge || loaded.HistoryRetention.AuditLogAge != override.HistoryRetention.AuditLogAge || loaded.Timezone != override.Timezone || loaded.DateTimeFormat != override.DateTimeFormat {
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

	invalid := override
	invalid.Timezone = "Local"
	if err := db.SetRuntimeSettingsAudited(ctx, invalid, "admin"); err == nil {
		t.Fatal("accepted non-portable Local timezone")
	}
	invalid = override
	invalid.DateTimeFormat = time.RFC3339
	if err := db.SetRuntimeSettingsAudited(ctx, invalid, "admin"); err == nil {
		t.Fatal("accepted unsupported date/time format")
	}
}

func TestRuntimeSettingsLegacyRowsUseDisplayDefaults(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, t.TempDir()+"/watchbell.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.db.ExecContext(ctx, `INSERT INTO app_settings (key, value, updated_at) VALUES (?, ?, ?)`, sessionTTLSetting, "86400", nowString()); err != nil {
		t.Fatal(err)
	}
	defaults := RuntimeSettings{
		SessionTTL:       8 * time.Hour,
		HistoryRetention: UniformHistoryRetention(90*24*time.Hour, 500),
		Timezone:         "Asia/Shanghai",
		DateTimeFormat:   "MM-dd-yyyy HH:mm:ss",
	}
	loaded, err := db.GetRuntimeSettings(ctx, defaults)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SessionTTL != 24*time.Hour {
		t.Fatalf("legacy session TTL = %s, want 24h", loaded.SessionTTL)
	}
	if loaded.Timezone != defaults.Timezone || loaded.DateTimeFormat != defaults.DateTimeFormat {
		t.Fatalf("legacy display defaults not preserved: %#v", loaded)
	}
}

func TestRuntimeSettingsFillPortableDisplayDefaults(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, t.TempDir()+"/watchbell.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	t.Setenv("TZ", "Asia/Shanghai")
	loaded, err := db.GetRuntimeSettings(ctx, RuntimeSettings{
		SessionTTL:       8 * time.Hour,
		HistoryRetention: UniformHistoryRetention(90*24*time.Hour, 500),
	})
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Timezone != "Asia/Shanghai" || loaded.DateTimeFormat != "yyyy-MM-dd HH:mm:ss" {
		t.Fatalf("display defaults = %#v", loaded)
	}
}
