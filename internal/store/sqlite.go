package store

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/watchbell/watchbell/internal/model"
)

//go:embed schema.sql
var schemaSQL string

type Store struct {
	db *sql.DB
}

func Open(ctx context.Context, path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite3", sqliteConnectionDSN(path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON;"); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.ExecContext(ctx, schemaSQL); err != nil {
		_ = db.Close()
		return nil, err
	}
	store := &Store{db: db}
	if err := store.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func sqliteConnectionDSN(path string) string {
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	// foreign_keys and busy_timeout are connection-local SQLite settings.
	// Supplying them through the driver DSN applies them to every pooled
	// connection, rather than only whichever connection executes PRAGMA first.
	return path + separator + "_busy_timeout=5000&_foreign_keys=on&_journal_mode=WAL"
}

func (s *Store) migrate(ctx context.Context) error {
	columns := []struct {
		table      string
		name       string
		definition string
	}{
		{"monitors", "consecutive_failures", "INTEGER NOT NULL DEFAULT 0"},
		{"monitors", "failure_alert_after", "INTEGER NOT NULL DEFAULT 0"},
		{"monitors", "failure_notify_channel_ids_json", "TEXT NOT NULL DEFAULT '[]'"},
		{"monitors", "failure_alert_active", "INTEGER NOT NULL DEFAULT 0"},
		{"monitors", "deleted_at", "TEXT"},
		{"rules", "quiet_hours_json", "TEXT NOT NULL DEFAULT '{}'"},
		{"rules", "deleted_at", "TEXT"},
		{"notify_channels", "deleted_at", "TEXT"},
		{"notification_templates", "is_default", "INTEGER NOT NULL DEFAULT 0"},
		{"notification_templates", "deleted_at", "TEXT"},
		{"notification_attempts", "monitor_id", "INTEGER REFERENCES monitors(id)"},
		{"notification_attempts", "data_json", "TEXT NOT NULL DEFAULT '{}'"},
		{"notification_attempts", "retry_claimed_at", "TEXT"},
	}
	for _, column := range columns {
		exists, err := s.columnExists(ctx, column.table, column.name)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		query := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", column.table, column.name, column.definition)
		if _, err := s.db.ExecContext(ctx, query); err != nil {
			return fmt.Errorf("migrate %s.%s: %w", column.table, column.name, err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE notification_templates SET is_default = 1 WHERE id = 1 AND NOT EXISTS (SELECT 1 FROM notification_templates WHERE is_default = 1)`); err != nil {
		return fmt.Errorf("mark default notification template: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS idx_notification_templates_default ON notification_templates(is_default) WHERE is_default = 1 AND deleted_at IS NULL`); err != nil {
		return fmt.Errorf("index default notification template: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_notification_attempts_monitor_id ON notification_attempts(monitor_id, created_at DESC)`); err != nil {
		return fmt.Errorf("index notification attempt monitor: %w", err)
	}
	if err := s.repairActiveConfigReferences(ctx); err != nil {
		return fmt.Errorf("repair active configuration references: %w", err)
	}
	return nil
}

func (s *Store) columnExists(ctx context.Context, table, column string) (bool, error) {
	rows, err := s.db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, dataType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) ListMonitors(ctx context.Context) ([]model.Monitor, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, type, enabled, interval_seconds, config_json, state_json, last_checked_at, last_status, last_message, last_error, consecutive_failures, failure_alert_after, failure_notify_channel_ids_json, failure_alert_active, created_at, updated_at FROM monitors WHERE deleted_at IS NULL ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]model.Monitor, 0)
	for rows.Next() {
		item, err := scanMonitor(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ListEnabledMonitors(ctx context.Context) ([]model.Monitor, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, type, enabled, interval_seconds, config_json, state_json, last_checked_at, last_status, last_message, last_error, consecutive_failures, failure_alert_after, failure_notify_channel_ids_json, failure_alert_active, created_at, updated_at FROM monitors WHERE enabled = 1 AND deleted_at IS NULL ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]model.Monitor, 0)
	for rows.Next() {
		item, err := scanMonitor(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) GetMonitor(ctx context.Context, id int64) (model.Monitor, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, name, type, enabled, interval_seconds, config_json, state_json, last_checked_at, last_status, last_message, last_error, consecutive_failures, failure_alert_after, failure_notify_channel_ids_json, failure_alert_active, created_at, updated_at FROM monitors WHERE id = ? AND deleted_at IS NULL`, id)
	return scanMonitor(row)
}

// GetMonitorIncludingArchived is used when rendering immutable history whose
// originating monitor may have been archived after the event was created.
func (s *Store) GetMonitorIncludingArchived(ctx context.Context, id int64) (model.Monitor, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, name, type, enabled, interval_seconds, config_json, state_json, last_checked_at, last_status, last_message, last_error, consecutive_failures, failure_alert_after, failure_notify_channel_ids_json, failure_alert_active, created_at, updated_at FROM monitors WHERE id = ?`, id)
	return scanMonitor(row)
}

func (s *Store) CreateMonitor(ctx context.Context, input model.MonitorInput) (model.Monitor, error) {
	now := nowString()
	if input.IntervalSeconds <= 0 {
		input.IntervalSeconds = 300
	}
	config := normalizedJSON(input.Config, "{}")
	channelIDs, err := json.Marshal(normalizedIDs(input.FailureNotifyChannelIDs))
	if err != nil {
		return model.Monitor{}, err
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO monitors (name, type, enabled, interval_seconds, config_json, state_json, failure_alert_after, failure_notify_channel_ids_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, '{}', ?, ?, ?, ?)`,
		strings.TrimSpace(input.Name), input.Type, boolInt(input.Enabled), input.IntervalSeconds, string(config), input.FailureAlertAfter, string(channelIDs), now, now)
	if err != nil {
		return model.Monitor{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return model.Monitor{}, err
	}
	return s.GetMonitor(ctx, id)
}

func (s *Store) UpdateMonitor(ctx context.Context, id int64, input model.MonitorInput) (model.Monitor, error) {
	if input.IntervalSeconds <= 0 {
		input.IntervalSeconds = 300
	}
	config := normalizedJSON(input.Config, "{}")
	channelIDs, err := json.Marshal(normalizedIDs(input.FailureNotifyChannelIDs))
	if err != nil {
		return model.Monitor{}, err
	}
	existing, err := s.GetMonitor(ctx, id)
	if err != nil {
		return model.Monitor{}, err
	}
	resetState := existing.Type != input.Type || !jsonEquivalent(existing.Config, config)
	query := `UPDATE monitors SET name = ?, type = ?, enabled = ?, interval_seconds = ?, config_json = ?, failure_alert_after = ?, failure_notify_channel_ids_json = ?, failure_alert_active = CASE WHEN ? = 0 THEN 0 ELSE failure_alert_active END, updated_at = ? WHERE id = ? AND deleted_at IS NULL`
	args := []any{strings.TrimSpace(input.Name), input.Type, boolInt(input.Enabled), input.IntervalSeconds, string(config), input.FailureAlertAfter, string(channelIDs), input.FailureAlertAfter, nowString(), id}
	if resetState {
		query = `UPDATE monitors SET name = ?, type = ?, enabled = ?, interval_seconds = ?, config_json = ?, state_json = '{}', last_checked_at = NULL, last_status = '', last_message = '', last_error = '', consecutive_failures = 0, failure_alert_after = ?, failure_notify_channel_ids_json = ?, failure_alert_active = 0, updated_at = ? WHERE id = ? AND deleted_at IS NULL`
		args = []any{strings.TrimSpace(input.Name), input.Type, boolInt(input.Enabled), input.IntervalSeconds, string(config), input.FailureAlertAfter, string(channelIDs), nowString(), id}
	}
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return model.Monitor{}, err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return model.Monitor{}, sql.ErrNoRows
	}
	return s.GetMonitor(ctx, id)
}

func (s *Store) UpdateMonitorCheckResult(ctx context.Context, id int64, result model.CheckResult, checkErr error) error {
	state := normalizedMap(result.State)
	status := strings.TrimSpace(result.Status)
	message := strings.TrimSpace(result.Message)
	lastErr := ""
	if checkErr != nil {
		status = "error"
		lastErr = checkErr.Error()
	}
	failureDelta := 0
	if checkErr != nil {
		failureDelta = 1
	}
	_, err := s.db.ExecContext(ctx, `UPDATE monitors SET state_json = ?, last_checked_at = ?, last_status = ?, last_message = ?, last_error = ?, consecutive_failures = CASE WHEN ? = 1 THEN consecutive_failures + 1 ELSE 0 END, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
		string(state), nowString(), status, message, lastErr, failureDelta, nowString(), id)
	return err
}

func (s *Store) UpdateMonitorDispatchWarning(ctx context.Context, id int64, warning error) error {
	if warning == nil {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE monitors SET last_status = 'warning', last_error = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`, warning.Error(), nowString(), id)
	return err
}

func (s *Store) DeleteMonitor(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := nowString()
	res, err := tx.ExecContext(ctx, `UPDATE monitors SET enabled = 0, deleted_at = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`, now, now, id)
	if err != nil {
		return err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}
	// Rules cannot be meaningfully restored without their monitor. Archive the
	// active configuration rows while preserving all rule evaluation history.
	if _, err := tx.ExecContext(ctx, `UPDATE rules SET enabled = 0, deleted_at = ?, updated_at = ? WHERE monitor_id = ? AND deleted_at IS NULL`, now, now, id); err != nil {
		return err
	}
	// Events already discovered remain part of history, but no notification
	// should be dispatched after the owning monitor and its rules are archived.
	if _, err := tx.ExecContext(ctx, `UPDATE event_outbox SET status = 'processed', last_error = 'monitor archived before dispatch', updated_at = ? WHERE event_id IN (SELECT id FROM events WHERE monitor_id = ?) AND status IN ('pending', 'processing')`, now, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE notification_attempts SET next_retry_at = NULL, retry_claimed_at = NULL,
		error = CASE WHEN error = '' THEN 'retry stopped: monitor archived' ELSE error || '; retry stopped: monitor archived' END
		WHERE monitor_id = ? AND status = 'failed' AND next_retry_at IS NOT NULL`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListRules(ctx context.Context) ([]model.Rule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, monitor_id, name, enabled, condition_json, notify_channel_ids_json, template_id, cooldown_seconds, quiet_hours_json, last_fired_at, created_at, updated_at FROM rules WHERE deleted_at IS NULL ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRules(rows)
}

func (s *Store) ListRulesForMonitor(ctx context.Context, monitorID int64) ([]model.Rule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, monitor_id, name, enabled, condition_json, notify_channel_ids_json, template_id, cooldown_seconds, quiet_hours_json, last_fired_at, created_at, updated_at FROM rules WHERE monitor_id = ? AND enabled = 1 AND deleted_at IS NULL ORDER BY id ASC`, monitorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRules(rows)
}

func (s *Store) GetRule(ctx context.Context, id int64) (model.Rule, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, monitor_id, name, enabled, condition_json, notify_channel_ids_json, template_id, cooldown_seconds, quiet_hours_json, last_fired_at, created_at, updated_at FROM rules WHERE id = ? AND deleted_at IS NULL`, id)
	return scanRule(row)
}

func (s *Store) CreateRule(ctx context.Context, input model.RuleInput) (model.Rule, error) {
	now := nowString()
	condition := normalizedJSON(input.Condition, "{}")
	channelIDs, err := json.Marshal(input.NotifyChannelIDs)
	if err != nil {
		return model.Rule{}, err
	}
	quietHours, err := json.Marshal(input.QuietHours)
	if err != nil {
		return model.Rule{}, err
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO rules (monitor_id, name, enabled, condition_json, notify_channel_ids_json, template_id, cooldown_seconds, quiet_hours_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		input.MonitorID, strings.TrimSpace(input.Name), boolInt(input.Enabled), string(condition), string(channelIDs), input.TemplateID, input.CooldownSeconds, string(quietHours), now, now)
	if err != nil {
		return model.Rule{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return model.Rule{}, err
	}
	return s.GetRule(ctx, id)
}

func (s *Store) UpdateRule(ctx context.Context, id int64, input model.RuleInput) (model.Rule, error) {
	condition := normalizedJSON(input.Condition, "{}")
	channelIDs, err := json.Marshal(input.NotifyChannelIDs)
	if err != nil {
		return model.Rule{}, err
	}
	quietHours, err := json.Marshal(input.QuietHours)
	if err != nil {
		return model.Rule{}, err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE rules SET monitor_id = ?, name = ?, enabled = ?, condition_json = ?, notify_channel_ids_json = ?, template_id = ?, cooldown_seconds = ?, quiet_hours_json = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
		input.MonitorID, strings.TrimSpace(input.Name), boolInt(input.Enabled), string(condition), string(channelIDs), input.TemplateID, input.CooldownSeconds, string(quietHours), nowString(), id)
	if err != nil {
		return model.Rule{}, err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return model.Rule{}, sql.ErrNoRows
	}
	return s.GetRule(ctx, id)
}

func (s *Store) UpdateRuleFiredAt(ctx context.Context, id int64, firedAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE rules SET last_fired_at = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`, formatTime(firedAt), nowString(), id)
	return err
}

func (s *Store) DeleteRule(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE rules SET enabled = 0, deleted_at = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`, nowString(), nowString(), id)
	if err != nil {
		return err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) ListNotifyChannels(ctx context.Context) ([]model.NotifyChannel, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, type, enabled, config_json, created_at, updated_at FROM notify_channels WHERE deleted_at IS NULL ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]model.NotifyChannel, 0)
	for rows.Next() {
		item, err := scanNotifyChannel(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ListNotifyChannelsByIDs(ctx context.Context, ids []int64) ([]model.NotifyChannel, error) {
	if len(ids) == 0 {
		return []model.NotifyChannel{}, nil
	}
	placeholders := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	query := fmt.Sprintf(`SELECT id, name, type, enabled, config_json, created_at, updated_at FROM notify_channels WHERE enabled = 1 AND deleted_at IS NULL AND id IN (%s) ORDER BY id ASC`, strings.Join(placeholders, ","))
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]model.NotifyChannel, 0)
	for rows.Next() {
		item, err := scanNotifyChannel(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) GetNotifyChannel(ctx context.Context, id int64) (model.NotifyChannel, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, name, type, enabled, config_json, created_at, updated_at FROM notify_channels WHERE id = ? AND deleted_at IS NULL`, id)
	return scanNotifyChannel(row)
}

func (s *Store) CreateNotifyChannel(ctx context.Context, input model.NotifyChannelInput) (model.NotifyChannel, error) {
	now := nowString()
	config := normalizedJSON(input.Config, "{}")
	res, err := s.db.ExecContext(ctx, `INSERT INTO notify_channels (name, type, enabled, config_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		strings.TrimSpace(input.Name), input.Type, boolInt(input.Enabled), string(config), now, now)
	if err != nil {
		return model.NotifyChannel{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return model.NotifyChannel{}, err
	}
	return s.GetNotifyChannel(ctx, id)
}

func (s *Store) UpdateNotifyChannel(ctx context.Context, id int64, input model.NotifyChannelInput) (model.NotifyChannel, error) {
	config := normalizedJSON(input.Config, "{}")
	res, err := s.db.ExecContext(ctx, `UPDATE notify_channels SET name = ?, type = ?, enabled = ?, config_json = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
		strings.TrimSpace(input.Name), input.Type, boolInt(input.Enabled), string(config), nowString(), id)
	if err != nil {
		return model.NotifyChannel{}, err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return model.NotifyChannel{}, sql.ErrNoRows
	}
	return s.GetNotifyChannel(ctx, id)
}

func (s *Store) DeleteNotifyChannel(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := nowString()
	res, err := tx.ExecContext(ctx, `UPDATE notify_channels SET enabled = 0, deleted_at = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`, now, now, id)
	if err != nil {
		return err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}

	type jsonReference struct {
		id  int64
		raw string
	}
	ruleRows, err := tx.QueryContext(ctx, `SELECT id, notify_channel_ids_json FROM rules WHERE deleted_at IS NULL`)
	if err != nil {
		return err
	}
	ruleReferences := make([]jsonReference, 0)
	for ruleRows.Next() {
		var item jsonReference
		if err := ruleRows.Scan(&item.id, &item.raw); err != nil {
			ruleRows.Close()
			return err
		}
		ruleReferences = append(ruleReferences, item)
	}
	if err := ruleRows.Err(); err != nil {
		ruleRows.Close()
		return err
	}
	ruleRows.Close()
	for _, reference := range ruleReferences {
		ids, changed, err := removeJSONID(reference.raw, id)
		if err != nil {
			return fmt.Errorf("remove channel from rule %d: %w", reference.id, err)
		}
		if !changed {
			continue
		}
		if len(ids) == 0 {
			if _, err := tx.ExecContext(ctx, `UPDATE rules SET enabled = 0, deleted_at = ?, notify_channel_ids_json = '[]', updated_at = ? WHERE id = ? AND deleted_at IS NULL`, now, now, reference.id); err != nil {
				return err
			}
			continue
		}
		encoded, _ := json.Marshal(ids)
		if _, err := tx.ExecContext(ctx, `UPDATE rules SET notify_channel_ids_json = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`, string(encoded), now, reference.id); err != nil {
			return err
		}
	}

	monitorRows, err := tx.QueryContext(ctx, `SELECT id, failure_notify_channel_ids_json FROM monitors WHERE deleted_at IS NULL`)
	if err != nil {
		return err
	}
	monitorReferences := make([]jsonReference, 0)
	for monitorRows.Next() {
		var item jsonReference
		if err := monitorRows.Scan(&item.id, &item.raw); err != nil {
			monitorRows.Close()
			return err
		}
		monitorReferences = append(monitorReferences, item)
	}
	if err := monitorRows.Err(); err != nil {
		monitorRows.Close()
		return err
	}
	monitorRows.Close()
	for _, reference := range monitorReferences {
		ids, changed, err := removeJSONID(reference.raw, id)
		if err != nil {
			return fmt.Errorf("remove failure channel from monitor %d: %w", reference.id, err)
		}
		if !changed {
			continue
		}
		encoded, _ := json.Marshal(ids)
		if len(ids) == 0 {
			if _, err := tx.ExecContext(ctx, `UPDATE monitors SET failure_alert_after = 0, failure_notify_channel_ids_json = ?, failure_alert_active = 0, updated_at = ? WHERE id = ? AND deleted_at IS NULL`, string(encoded), now, reference.id); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE monitors SET failure_notify_channel_ids_json = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`, string(encoded), now, reference.id); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE notification_attempts SET next_retry_at = NULL, retry_claimed_at = NULL,
		error = CASE WHEN error = '' THEN 'retry stopped: channel archived' ELSE error || '; retry stopped: channel archived' END
		WHERE channel_id = ? AND status = 'failed' AND next_retry_at IS NOT NULL`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListNotificationTemplates(ctx context.Context) ([]model.NotificationTemplate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, subject_template, body_template, is_default, created_at, updated_at FROM notification_templates WHERE deleted_at IS NULL ORDER BY is_default DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]model.NotificationTemplate, 0)
	for rows.Next() {
		item, err := scanNotificationTemplate(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) GetNotificationTemplate(ctx context.Context, id int64) (model.NotificationTemplate, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, name, subject_template, body_template, is_default, created_at, updated_at FROM notification_templates WHERE id = ? AND deleted_at IS NULL`, id)
	return scanNotificationTemplate(row)
}

func (s *Store) GetDefaultNotificationTemplate(ctx context.Context) (model.NotificationTemplate, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, name, subject_template, body_template, is_default, created_at, updated_at FROM notification_templates WHERE is_default = 1 AND deleted_at IS NULL LIMIT 1`)
	return scanNotificationTemplate(row)
}

func (s *Store) CreateNotificationTemplate(ctx context.Context, input model.NotificationTemplateInput) (model.NotificationTemplate, error) {
	now := nowString()
	res, err := s.db.ExecContext(ctx, `INSERT INTO notification_templates (name, subject_template, body_template, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		strings.TrimSpace(input.Name), input.SubjectTemplate, input.BodyTemplate, now, now)
	if err != nil {
		return model.NotificationTemplate{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return model.NotificationTemplate{}, err
	}
	return s.GetNotificationTemplate(ctx, id)
}

func (s *Store) UpdateNotificationTemplate(ctx context.Context, id int64, input model.NotificationTemplateInput) (model.NotificationTemplate, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE notification_templates SET name = ?, subject_template = ?, body_template = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
		strings.TrimSpace(input.Name), input.SubjectTemplate, input.BodyTemplate, nowString(), id)
	if err != nil {
		return model.NotificationTemplate{}, err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return model.NotificationTemplate{}, sql.ErrNoRows
	}
	return s.GetNotificationTemplate(ctx, id)
}

func (s *Store) DeleteNotificationTemplate(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := nowString()
	res, err := tx.ExecContext(ctx, `UPDATE notification_templates SET deleted_at = ?, updated_at = ? WHERE id = ? AND is_default = 0 AND deleted_at IS NULL`, now, now, id)
	if err != nil {
		return err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}
	// A nil template means "use the default template" and keeps every active
	// rule valid after a custom template is archived.
	if _, err := tx.ExecContext(ctx, `UPDATE rules SET template_id = NULL, updated_at = ? WHERE template_id = ? AND deleted_at IS NULL`, now, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateEvent(ctx context.Context, monitorID int64, event model.EventData) (model.Event, bool, error) {
	return s.CreateEventForRun(ctx, monitorID, 0, event)
}

func (s *Store) CreateEventForRun(ctx context.Context, monitorID, checkRunID int64, event model.EventData) (model.Event, bool, error) {
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		return model.Event{}, false, err
	}
	now := nowString()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Event{}, false, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO events (monitor_id, type, fingerprint, payload_json, created_at) VALUES (?, ?, ?, ?, ?)`,
		monitorID, event.Type, event.Fingerprint, string(payload), now)
	if err != nil {
		return model.Event{}, false, err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		if err := tx.Commit(); err != nil {
			return model.Event{}, false, err
		}
		existing, err := s.getEventByFingerprint(ctx, monitorID, event.Fingerprint)
		return existing, false, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return model.Event{}, false, err
	}
	if checkRunID > 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO event_check_runs (event_id, check_run_id) VALUES (?, ?)`, id, checkRunID); err != nil {
			return model.Event{}, false, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO event_outbox (event_id, status, attempts, next_attempt_at, created_at, updated_at) VALUES (?, 'pending', 0, ?, ?, ?)`, id, now, now, now); err != nil {
		return model.Event{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return model.Event{}, false, err
	}
	created, err := s.GetEvent(ctx, id)
	return created, true, err
}

func (s *Store) GetEvent(ctx context.Context, id int64) (model.Event, error) {
	row := s.db.QueryRowContext(ctx, `SELECT e.id, e.monitor_id, l.check_run_id, e.type, e.fingerprint, e.payload_json, e.created_at FROM events e LEFT JOIN event_check_runs l ON l.event_id = e.id WHERE e.id = ?`, id)
	return scanEvent(row)
}

func (s *Store) getEventByFingerprint(ctx context.Context, monitorID int64, fingerprint string) (model.Event, error) {
	row := s.db.QueryRowContext(ctx, `SELECT e.id, e.monitor_id, l.check_run_id, e.type, e.fingerprint, e.payload_json, e.created_at FROM events e LEFT JOIN event_check_runs l ON l.event_id = e.id WHERE e.monitor_id = ? AND e.fingerprint = ?`, monitorID, fingerprint)
	return scanEvent(row)
}

func (s *Store) ListEvents(ctx context.Context, limit int) ([]model.Event, error) {
	page, err := s.ListEventsPage(ctx, EventFilter{PageRequest: PageRequest{Page: 1, PageSize: normalizeLimit(limit)}})
	return page.Items, err
}

func (s *Store) CreateNotificationLog(ctx context.Context, eventID, channelID int64, status string, sendErr error) error {
	now := time.Now().UTC()
	errText := ""
	var sentAt *time.Time
	if sendErr != nil {
		errText = sendErr.Error()
	} else {
		sentAt = &now
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO notification_logs (event_id, channel_id, status, error, sent_at, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		eventID, channelID, status, errText, formatTimePtr(sentAt), formatTime(now))
	return err
}

func (s *Store) ListNotificationLogs(ctx context.Context, limit int) ([]model.NotificationLog, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, event_id, channel_id, status, error, sent_at, created_at FROM notification_logs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []model.NotificationLog
	for rows.Next() {
		item, err := scanNotificationLog(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanMonitor(row scanner) (model.Monitor, error) {
	var item model.Monitor
	var enabled, failureAlertActive int
	var config, state, failureChannelIDs string
	var lastChecked sql.NullString
	var createdAt, updatedAt string
	err := row.Scan(&item.ID, &item.Name, &item.Type, &enabled, &item.IntervalSeconds, &config, &state, &lastChecked, &item.LastStatus, &item.LastMessage, &item.LastError, &item.ConsecutiveFailures, &item.FailureAlertAfter, &failureChannelIDs, &failureAlertActive, &createdAt, &updatedAt)
	if err != nil {
		return model.Monitor{}, err
	}
	item.Enabled = enabled == 1
	item.FailureAlertActive = failureAlertActive == 1
	item.Config = json.RawMessage(defaultJSON(config, "{}"))
	item.State = json.RawMessage(defaultJSON(state, "{}"))
	if err := json.Unmarshal([]byte(defaultJSON(failureChannelIDs, "[]")), &item.FailureNotifyChannelIDs); err != nil {
		return model.Monitor{}, err
	}
	if item.FailureNotifyChannelIDs == nil {
		item.FailureNotifyChannelIDs = []int64{}
	}
	item.LastCheckedAt = parseTimePtr(lastChecked)
	item.CreatedAt = parseTime(createdAt)
	item.UpdatedAt = parseTime(updatedAt)
	return item, nil
}

func normalizedIDs(ids []int64) []int64 {
	if ids == nil {
		return []int64{}
	}
	return ids
}

func removeJSONID(raw string, remove int64) ([]int64, bool, error) {
	var ids []int64
	if err := json.Unmarshal([]byte(defaultJSON(raw, "[]")), &ids); err != nil {
		return nil, false, err
	}
	filtered := make([]int64, 0, len(ids))
	changed := false
	for _, id := range ids {
		if id == remove {
			changed = true
			continue
		}
		filtered = append(filtered, id)
	}
	return filtered, changed, nil
}

func scanRules(rows *sql.Rows) ([]model.Rule, error) {
	items := make([]model.Rule, 0)
	for rows.Next() {
		item, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func scanRule(row scanner) (model.Rule, error) {
	var item model.Rule
	var enabled int
	var condition, channelIDs, quietHours string
	var templateID sql.NullInt64
	var lastFired sql.NullString
	var createdAt, updatedAt string
	err := row.Scan(&item.ID, &item.MonitorID, &item.Name, &enabled, &condition, &channelIDs, &templateID, &item.CooldownSeconds, &quietHours, &lastFired, &createdAt, &updatedAt)
	if err != nil {
		return model.Rule{}, err
	}
	item.Enabled = enabled == 1
	item.Condition = json.RawMessage(defaultJSON(condition, "{}"))
	if templateID.Valid {
		item.TemplateID = &templateID.Int64
	}
	if err := json.Unmarshal([]byte(defaultJSON(channelIDs, "[]")), &item.NotifyChannelIDs); err != nil {
		return model.Rule{}, err
	}
	if err := json.Unmarshal([]byte(defaultJSON(quietHours, "{}")), &item.QuietHours); err != nil {
		return model.Rule{}, err
	}
	item.LastFiredAt = parseTimePtr(lastFired)
	item.CreatedAt = parseTime(createdAt)
	item.UpdatedAt = parseTime(updatedAt)
	return item, nil
}

func scanNotifyChannel(row scanner) (model.NotifyChannel, error) {
	var item model.NotifyChannel
	var enabled int
	var config string
	var createdAt, updatedAt string
	err := row.Scan(&item.ID, &item.Name, &item.Type, &enabled, &config, &createdAt, &updatedAt)
	if err != nil {
		return model.NotifyChannel{}, err
	}
	item.Enabled = enabled == 1
	item.Config = json.RawMessage(defaultJSON(config, "{}"))
	item.CreatedAt = parseTime(createdAt)
	item.UpdatedAt = parseTime(updatedAt)
	return item, nil
}

func scanNotificationTemplate(row scanner) (model.NotificationTemplate, error) {
	var item model.NotificationTemplate
	var isDefault int
	var createdAt, updatedAt string
	err := row.Scan(&item.ID, &item.Name, &item.SubjectTemplate, &item.BodyTemplate, &isDefault, &createdAt, &updatedAt)
	if err != nil {
		return model.NotificationTemplate{}, err
	}
	item.IsDefault = isDefault == 1
	item.CreatedAt = parseTime(createdAt)
	item.UpdatedAt = parseTime(updatedAt)
	return item, nil
}

func scanEvent(row scanner) (model.Event, error) {
	var item model.Event
	var checkRunID sql.NullInt64
	var payload, createdAt string
	err := row.Scan(&item.ID, &item.MonitorID, &checkRunID, &item.Type, &item.Fingerprint, &payload, &createdAt)
	if err != nil {
		return model.Event{}, err
	}
	item.Payload = json.RawMessage(defaultJSON(payload, "{}"))
	if checkRunID.Valid {
		item.CheckRunID = &checkRunID.Int64
	}
	item.CreatedAt = parseTime(createdAt)
	return item, nil
}

func scanNotificationLog(row scanner) (model.NotificationLog, error) {
	var item model.NotificationLog
	var sentAt sql.NullString
	var createdAt string
	err := row.Scan(&item.ID, &item.EventID, &item.ChannelID, &item.Status, &item.Error, &sentAt, &createdAt)
	if err != nil {
		return model.NotificationLog{}, err
	}
	item.SentAt = parseTimePtr(sentAt)
	item.CreatedAt = parseTime(createdAt)
	return item, nil
}

func normalizedJSON(raw json.RawMessage, fallback string) json.RawMessage {
	if len(raw) == 0 || !json.Valid(raw) {
		return json.RawMessage(fallback)
	}
	return raw
}

func normalizedMap(value map[string]any) json.RawMessage {
	if value == nil {
		return json.RawMessage("{}")
	}
	data, err := json.Marshal(value)
	if err != nil || !json.Valid(data) {
		return json.RawMessage("{}")
	}
	return data
}

func defaultJSON(value, fallback string) string {
	if strings.TrimSpace(value) == "" || !json.Valid([]byte(value)) {
		return fallback
	}
	return value
}

func jsonEquivalent(left, right json.RawMessage) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func nowString() string {
	return formatTime(time.Now().UTC())
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func formatTimePtr(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatTime(*value)
}

func parseTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func parseTimePtr(value sql.NullString) *time.Time {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil
	}
	parsed := parseTime(value.String)
	if parsed.IsZero() {
		return nil
	}
	return &parsed
}

func IsNotFound(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}
