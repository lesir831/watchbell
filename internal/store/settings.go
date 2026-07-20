package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/watchbell/watchbell/internal/datetime"
)

const (
	authPasswordHashSetting             = "auth.password_hash"
	sessionTTLSetting                   = "auth.session_ttl_seconds"
	eventRetentionSetting               = "history.event_retention_seconds"
	checkRunRetentionSetting            = "history.check_run_retention_seconds"
	notificationAttemptRetentionSetting = "history.notification_attempt_retention_seconds"
	auditLogRetentionSetting            = "history.audit_log_retention_seconds"
	systemTimezoneSetting               = "system.timezone"
	systemDateTimeFormatSetting         = "system.date_time_format"
)

// RuntimeSettings contains the durable settings that may be changed without
// restarting WatchBell. Durations are stored as integer seconds in
// app_settings so they remain independent from environment-variable syntax.
type RuntimeSettings struct {
	SessionTTL       time.Duration
	HistoryRetention HistoryRetentionPolicy
	Timezone         string
	DateTimeFormat   string
}

func (s *Store) GetRuntimeSettings(ctx context.Context, defaults RuntimeSettings) (RuntimeSettings, error) {
	settings, err := runtimeSettingsWithDisplayDefaults(defaults)
	if err != nil {
		return RuntimeSettings{}, err
	}
	keys := []string{
		sessionTTLSetting,
		eventRetentionSetting,
		checkRunRetentionSetting,
		notificationAttemptRetentionSetting,
		auditLogRetentionSetting,
		systemTimezoneSetting,
		systemDateTimeFormatSetting,
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(keys)), ",")
	args := make([]any, len(keys))
	for index, key := range keys {
		args[index] = key
	}
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM app_settings WHERE key IN (`+placeholders+`)`, args...)
	if err != nil {
		return RuntimeSettings{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var key, raw string
		if err := rows.Scan(&key, &raw); err != nil {
			return RuntimeSettings{}, err
		}
		switch key {
		case systemTimezoneSetting:
			timezone := strings.TrimSpace(raw)
			if err := datetime.ValidateTimeZone(timezone); err != nil {
				return RuntimeSettings{}, fmt.Errorf("invalid runtime setting %s: %w", key, err)
			}
			settings.Timezone = timezone
		case systemDateTimeFormatSetting:
			if err := datetime.ValidateFormat(raw); err != nil {
				return RuntimeSettings{}, fmt.Errorf("invalid runtime setting %s: %w", key, err)
			}
			settings.DateTimeFormat = raw
		case sessionTTLSetting:
			duration, err := storedDuration(key, raw)
			if err != nil {
				return RuntimeSettings{}, err
			}
			if duration <= 0 {
				return RuntimeSettings{}, fmt.Errorf("invalid runtime setting %s", key)
			}
			settings.SessionTTL = duration
		case eventRetentionSetting:
			duration, err := storedDuration(key, raw)
			if err != nil {
				return RuntimeSettings{}, err
			}
			settings.HistoryRetention.EventAge = duration
		case checkRunRetentionSetting:
			duration, err := storedDuration(key, raw)
			if err != nil {
				return RuntimeSettings{}, err
			}
			settings.HistoryRetention.CheckRunAge = duration
		case notificationAttemptRetentionSetting:
			duration, err := storedDuration(key, raw)
			if err != nil {
				return RuntimeSettings{}, err
			}
			settings.HistoryRetention.NotificationAttemptAge = duration
		case auditLogRetentionSetting:
			duration, err := storedDuration(key, raw)
			if err != nil {
				return RuntimeSettings{}, err
			}
			settings.HistoryRetention.AuditLogAge = duration
		}
	}
	if err := rows.Err(); err != nil {
		return RuntimeSettings{}, err
	}
	return settings, nil
}

// SetRuntimeSettingsAudited replaces the browser-editable runtime policy and
// records the change atomically. Batch size remains a deployment concern.
func (s *Store) SetRuntimeSettingsAudited(ctx context.Context, settings RuntimeSettings, actor string) error {
	if settings.SessionTTL <= 0 {
		return fmt.Errorf("session TTL must be positive")
	}
	settings.Timezone = strings.TrimSpace(settings.Timezone)
	if err := datetime.ValidateTimeZone(settings.Timezone); err != nil {
		return fmt.Errorf("invalid runtime setting %s: %w", systemTimezoneSetting, err)
	}
	if err := datetime.ValidateFormat(settings.DateTimeFormat); err != nil {
		return fmt.Errorf("invalid runtime setting %s: %w", systemDateTimeFormatSetting, err)
	}
	durations := map[string]int64{
		sessionTTLSetting:                   int64(settings.SessionTTL / time.Second),
		eventRetentionSetting:               int64(settings.HistoryRetention.EventAge / time.Second),
		checkRunRetentionSetting:            int64(settings.HistoryRetention.CheckRunAge / time.Second),
		notificationAttemptRetentionSetting: int64(settings.HistoryRetention.NotificationAttemptAge / time.Second),
		auditLogRetentionSetting:            int64(settings.HistoryRetention.AuditLogAge / time.Second),
	}
	values := map[string]string{
		systemTimezoneSetting:       settings.Timezone,
		systemDateTimeFormatSetting: settings.DateTimeFormat,
	}
	for key, value := range durations {
		if value < 0 {
			return fmt.Errorf("runtime setting %s cannot be negative", key)
		}
		values[key] = strconv.FormatInt(value, 10)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := nowString()
	for key, value := range values {
		if _, err := tx.ExecContext(ctx, `INSERT INTO app_settings (key, value, updated_at) VALUES (?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, key, value, now); err != nil {
			return fmt.Errorf("persist runtime setting %s: %w", key, err)
		}
	}
	changes, _ := json.Marshal(map[string]any{
		"sessionTimeoutSeconds": int64(settings.SessionTTL / time.Second),
		"historyRetentionSeconds": map[string]int64{
			"events":               int64(settings.HistoryRetention.EventAge / time.Second),
			"checkRuns":            int64(settings.HistoryRetention.CheckRunAge / time.Second),
			"notificationAttempts": int64(settings.HistoryRetention.NotificationAttemptAge / time.Second),
			"auditLogs":            int64(settings.HistoryRetention.AuditLogAge / time.Second),
		},
		"timezone":       settings.Timezone,
		"dateTimeFormat": settings.DateTimeFormat,
	})
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_logs (actor, action, entity_type, entity_id, summary, changes_json, created_at)
		VALUES (?, 'update', 'settings', NULL, '修改运行时设置', ?, ?)`, strings.TrimSpace(actor), string(changes), now); err != nil {
		return fmt.Errorf("record runtime settings audit: %w", err)
	}
	return tx.Commit()
}

func runtimeSettingsWithDisplayDefaults(settings RuntimeSettings) (RuntimeSettings, error) {
	settings.Timezone = strings.TrimSpace(settings.Timezone)
	if settings.Timezone == "" {
		settings.Timezone = datetime.DeploymentTimeZone()
	}
	if settings.DateTimeFormat == "" {
		settings.DateTimeFormat = datetime.DefaultFormat
	}
	if err := datetime.ValidateTimeZone(settings.Timezone); err != nil {
		return RuntimeSettings{}, fmt.Errorf("invalid runtime default %s: %w", systemTimezoneSetting, err)
	}
	if err := datetime.ValidateFormat(settings.DateTimeFormat); err != nil {
		return RuntimeSettings{}, fmt.Errorf("invalid runtime default %s: %w", systemDateTimeFormatSetting, err)
	}
	return settings, nil
}

func storedDuration(key, raw string) (time.Duration, error) {
	seconds, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || seconds < 0 {
		return 0, fmt.Errorf("invalid runtime setting %s", key)
	}
	return time.Duration(seconds) * time.Second, nil
}

func (s *Store) GetAuthPasswordHash(ctx context.Context) (string, bool, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key = ?`, authPasswordHashSetting).Scan(&value)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return strings.TrimSpace(value), true, nil
}

// SetAuthPasswordHashAudited stores the credential override and its audit row
// in one transaction. Password hashes are never included in audit changes.
func (s *Store) SetAuthPasswordHashAudited(ctx context.Context, passwordHash, actor string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := nowString()
	if _, err := tx.ExecContext(ctx, `INSERT INTO app_settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, authPasswordHashSetting, passwordHash, now); err != nil {
		return fmt.Errorf("persist password hash: %w", err)
	}
	changes, _ := json.Marshal(map[string]any{"credentialUpdated": true})
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_logs (actor, action, entity_type, entity_id, summary, changes_json, created_at)
		VALUES (?, 'update', 'account', NULL, '修改管理员密码', ?, ?)`, strings.TrimSpace(actor), string(changes), now); err != nil {
		return fmt.Errorf("record password audit: %w", err)
	}
	return tx.Commit()
}
