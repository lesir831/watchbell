package config

import (
	"testing"
	"time"
)

func TestRetentionFromEnv(t *testing.T) {
	t.Setenv("WATCHBELL_HISTORY_RETENTION", "30d")
	t.Setenv("WATCHBELL_EVENT_RETENTION", "off")
	t.Setenv("WATCHBELL_CHECK_RUN_RETENTION", "48h")
	t.Setenv("WATCHBELL_NOTIFICATION_ATTEMPT_RETENTION", "disabled")
	t.Setenv("WATCHBELL_AUDIT_LOG_RETENTION", "7d")
	t.Setenv("WATCHBELL_HISTORY_CLEANUP_INTERVAL", "30m")
	t.Setenv("WATCHBELL_HISTORY_CLEANUP_BATCH", "123")

	config := RetentionFromEnv()
	if config.EventAge != 0 || config.CheckRunAge != 48*time.Hour || config.NotificationAttemptAge != 0 || config.AuditLogAge != 7*24*time.Hour {
		t.Fatalf("unexpected retention windows: %+v", config)
	}
	if config.CleanupInterval != 30*time.Minute || config.BatchSize != 123 {
		t.Fatalf("unexpected cleanup settings: %+v", config)
	}
}

func TestRetentionFromEnvUsesCommonFallback(t *testing.T) {
	t.Setenv("WATCHBELL_HISTORY_RETENTION", "14d")
	t.Setenv("WATCHBELL_EVENT_RETENTION", "")
	t.Setenv("WATCHBELL_CHECK_RUN_RETENTION", "")
	t.Setenv("WATCHBELL_NOTIFICATION_ATTEMPT_RETENTION", "")
	t.Setenv("WATCHBELL_AUDIT_LOG_RETENTION", "")

	config := RetentionFromEnv()
	want := 14 * 24 * time.Hour
	if config.EventAge != want || config.CheckRunAge != want || config.NotificationAttemptAge != want || config.AuditLogAge != want {
		t.Fatalf("category settings did not inherit common retention: %+v", config)
	}
}
