package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

const defaultHistoryRetention = 90 * 24 * time.Hour

// RetentionConfig is intentionally separate from Config so applications can
// opt into the maintenance worker without changing existing startup wiring.
type RetentionConfig struct {
	EventAge               time.Duration
	CheckRunAge            time.Duration
	NotificationAttemptAge time.Duration
	AuditLogAge            time.Duration
	CleanupInterval        time.Duration
	BatchSize              int
}

// RetentionFromEnv reads history lifecycle settings. "0", "off" and
// "disabled" disable a category. Durations accept Go syntax (for example
// "2160h") plus a convenient whole-day suffix such as "90d".
//
// WATCHBELL_HISTORY_RETENTION sets the common default. Category-specific
// variables override it:
//   - WATCHBELL_EVENT_RETENTION
//   - WATCHBELL_CHECK_RUN_RETENTION
//   - WATCHBELL_NOTIFICATION_ATTEMPT_RETENTION
//   - WATCHBELL_AUDIT_LOG_RETENTION
//
// The worker cadence and batch size use WATCHBELL_HISTORY_CLEANUP_INTERVAL and
// WATCHBELL_HISTORY_CLEANUP_BATCH respectively.
func RetentionFromEnv() RetentionConfig {
	common := retentionDuration("WATCHBELL_HISTORY_RETENTION", defaultHistoryRetention)
	return RetentionConfig{
		EventAge:               retentionDuration("WATCHBELL_EVENT_RETENTION", common),
		CheckRunAge:            retentionDuration("WATCHBELL_CHECK_RUN_RETENTION", common),
		NotificationAttemptAge: retentionDuration("WATCHBELL_NOTIFICATION_ATTEMPT_RETENTION", common),
		AuditLogAge:            retentionDuration("WATCHBELL_AUDIT_LOG_RETENTION", common),
		CleanupInterval:        retentionDuration("WATCHBELL_HISTORY_CLEANUP_INTERVAL", 6*time.Hour),
		BatchSize:              getInt("WATCHBELL_HISTORY_CLEANUP_BATCH", 500),
	}
}

func retentionDuration(key string, fallback time.Duration) time.Duration {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if value == "" {
		return fallback
	}
	if value == "0" || value == "off" || value == "disabled" || value == "none" {
		return 0
	}
	if strings.HasSuffix(value, "d") {
		days, err := strconv.ParseInt(strings.TrimSuffix(value, "d"), 10, 32)
		if err == nil && days > 0 {
			return time.Duration(days) * 24 * time.Hour
		}
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}
