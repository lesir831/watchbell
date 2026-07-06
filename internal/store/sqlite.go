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
	db, err := sql.Open("sqlite3", path)
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
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) ListMonitors(ctx context.Context) ([]model.Monitor, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, type, enabled, interval_seconds, config_json, state_json, last_checked_at, last_status, last_message, last_error, created_at, updated_at FROM monitors ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []model.Monitor
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
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, type, enabled, interval_seconds, config_json, state_json, last_checked_at, last_status, last_message, last_error, created_at, updated_at FROM monitors WHERE enabled = 1 ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []model.Monitor
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
	row := s.db.QueryRowContext(ctx, `SELECT id, name, type, enabled, interval_seconds, config_json, state_json, last_checked_at, last_status, last_message, last_error, created_at, updated_at FROM monitors WHERE id = ?`, id)
	return scanMonitor(row)
}

func (s *Store) CreateMonitor(ctx context.Context, input model.MonitorInput) (model.Monitor, error) {
	now := nowString()
	if input.IntervalSeconds <= 0 {
		input.IntervalSeconds = 300
	}
	config := normalizedJSON(input.Config, "{}")
	res, err := s.db.ExecContext(ctx, `INSERT INTO monitors (name, type, enabled, interval_seconds, config_json, state_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, '{}', ?, ?)`,
		strings.TrimSpace(input.Name), input.Type, boolInt(input.Enabled), input.IntervalSeconds, string(config), now, now)
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
	res, err := s.db.ExecContext(ctx, `UPDATE monitors SET name = ?, type = ?, enabled = ?, interval_seconds = ?, config_json = ?, updated_at = ? WHERE id = ?`,
		strings.TrimSpace(input.Name), input.Type, boolInt(input.Enabled), input.IntervalSeconds, string(config), nowString(), id)
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
	_, err := s.db.ExecContext(ctx, `UPDATE monitors SET state_json = ?, last_checked_at = ?, last_status = ?, last_message = ?, last_error = ?, updated_at = ? WHERE id = ?`,
		string(state), nowString(), status, message, lastErr, nowString(), id)
	return err
}

func (s *Store) DeleteMonitor(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM monitors WHERE id = ?`, id)
	if err != nil {
		return err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) ListRules(ctx context.Context) ([]model.Rule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, monitor_id, name, enabled, condition_json, notify_channel_ids_json, template_id, cooldown_seconds, last_fired_at, created_at, updated_at FROM rules ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRules(rows)
}

func (s *Store) ListRulesForMonitor(ctx context.Context, monitorID int64) ([]model.Rule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, monitor_id, name, enabled, condition_json, notify_channel_ids_json, template_id, cooldown_seconds, last_fired_at, created_at, updated_at FROM rules WHERE monitor_id = ? AND enabled = 1 ORDER BY id ASC`, monitorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRules(rows)
}

func (s *Store) GetRule(ctx context.Context, id int64) (model.Rule, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, monitor_id, name, enabled, condition_json, notify_channel_ids_json, template_id, cooldown_seconds, last_fired_at, created_at, updated_at FROM rules WHERE id = ?`, id)
	return scanRule(row)
}

func (s *Store) CreateRule(ctx context.Context, input model.RuleInput) (model.Rule, error) {
	now := nowString()
	condition := normalizedJSON(input.Condition, "{}")
	channelIDs, err := json.Marshal(input.NotifyChannelIDs)
	if err != nil {
		return model.Rule{}, err
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO rules (monitor_id, name, enabled, condition_json, notify_channel_ids_json, template_id, cooldown_seconds, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		input.MonitorID, strings.TrimSpace(input.Name), boolInt(input.Enabled), string(condition), string(channelIDs), input.TemplateID, input.CooldownSeconds, now, now)
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
	res, err := s.db.ExecContext(ctx, `UPDATE rules SET monitor_id = ?, name = ?, enabled = ?, condition_json = ?, notify_channel_ids_json = ?, template_id = ?, cooldown_seconds = ?, updated_at = ? WHERE id = ?`,
		input.MonitorID, strings.TrimSpace(input.Name), boolInt(input.Enabled), string(condition), string(channelIDs), input.TemplateID, input.CooldownSeconds, nowString(), id)
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
	_, err := s.db.ExecContext(ctx, `UPDATE rules SET last_fired_at = ?, updated_at = ? WHERE id = ?`, formatTime(firedAt), nowString(), id)
	return err
}

func (s *Store) DeleteRule(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM rules WHERE id = ?`, id)
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
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, type, enabled, config_json, created_at, updated_at FROM notify_channels ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []model.NotifyChannel
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
		return nil, nil
	}
	placeholders := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	query := fmt.Sprintf(`SELECT id, name, type, enabled, config_json, created_at, updated_at FROM notify_channels WHERE enabled = 1 AND id IN (%s) ORDER BY id ASC`, strings.Join(placeholders, ","))
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []model.NotifyChannel
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
	row := s.db.QueryRowContext(ctx, `SELECT id, name, type, enabled, config_json, created_at, updated_at FROM notify_channels WHERE id = ?`, id)
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
	res, err := s.db.ExecContext(ctx, `UPDATE notify_channels SET name = ?, type = ?, enabled = ?, config_json = ?, updated_at = ? WHERE id = ?`,
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
	res, err := s.db.ExecContext(ctx, `DELETE FROM notify_channels WHERE id = ?`, id)
	if err != nil {
		return err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) ListNotificationTemplates(ctx context.Context) ([]model.NotificationTemplate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, subject_template, body_template, created_at, updated_at FROM notification_templates ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []model.NotificationTemplate
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
	row := s.db.QueryRowContext(ctx, `SELECT id, name, subject_template, body_template, created_at, updated_at FROM notification_templates WHERE id = ?`, id)
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
	res, err := s.db.ExecContext(ctx, `UPDATE notification_templates SET name = ?, subject_template = ?, body_template = ?, updated_at = ? WHERE id = ?`,
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
	res, err := s.db.ExecContext(ctx, `DELETE FROM notification_templates WHERE id = ? AND id <> 1`, id)
	if err != nil {
		return err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) CreateEvent(ctx context.Context, monitorID int64, event model.EventData) (model.Event, bool, error) {
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		return model.Event{}, false, err
	}
	now := nowString()
	res, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO events (monitor_id, type, fingerprint, payload_json, created_at) VALUES (?, ?, ?, ?, ?)`,
		monitorID, event.Type, event.Fingerprint, string(payload), now)
	if err != nil {
		return model.Event{}, false, err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		existing, err := s.getEventByFingerprint(ctx, monitorID, event.Fingerprint)
		return existing, false, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return model.Event{}, false, err
	}
	created, err := s.GetEvent(ctx, id)
	return created, true, err
}

func (s *Store) GetEvent(ctx context.Context, id int64) (model.Event, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, monitor_id, type, fingerprint, payload_json, created_at FROM events WHERE id = ?`, id)
	return scanEvent(row)
}

func (s *Store) getEventByFingerprint(ctx context.Context, monitorID int64, fingerprint string) (model.Event, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, monitor_id, type, fingerprint, payload_json, created_at FROM events WHERE monitor_id = ? AND fingerprint = ?`, monitorID, fingerprint)
	return scanEvent(row)
}

func (s *Store) ListEvents(ctx context.Context, limit int) ([]model.Event, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, monitor_id, type, fingerprint, payload_json, created_at FROM events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []model.Event
	for rows.Next() {
		item, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
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
	var enabled int
	var config, state string
	var lastChecked sql.NullString
	var createdAt, updatedAt string
	err := row.Scan(&item.ID, &item.Name, &item.Type, &enabled, &item.IntervalSeconds, &config, &state, &lastChecked, &item.LastStatus, &item.LastMessage, &item.LastError, &createdAt, &updatedAt)
	if err != nil {
		return model.Monitor{}, err
	}
	item.Enabled = enabled == 1
	item.Config = json.RawMessage(defaultJSON(config, "{}"))
	item.State = json.RawMessage(defaultJSON(state, "{}"))
	item.LastCheckedAt = parseTimePtr(lastChecked)
	item.CreatedAt = parseTime(createdAt)
	item.UpdatedAt = parseTime(updatedAt)
	return item, nil
}

func scanRules(rows *sql.Rows) ([]model.Rule, error) {
	var items []model.Rule
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
	var condition, channelIDs string
	var templateID sql.NullInt64
	var lastFired sql.NullString
	var createdAt, updatedAt string
	err := row.Scan(&item.ID, &item.MonitorID, &item.Name, &enabled, &condition, &channelIDs, &templateID, &item.CooldownSeconds, &lastFired, &createdAt, &updatedAt)
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
	var createdAt, updatedAt string
	err := row.Scan(&item.ID, &item.Name, &item.SubjectTemplate, &item.BodyTemplate, &createdAt, &updatedAt)
	if err != nil {
		return model.NotificationTemplate{}, err
	}
	item.CreatedAt = parseTime(createdAt)
	item.UpdatedAt = parseTime(updatedAt)
	return item, nil
}

func scanEvent(row scanner) (model.Event, error) {
	var item model.Event
	var payload, createdAt string
	err := row.Scan(&item.ID, &item.MonitorID, &item.Type, &item.Fingerprint, &payload, &createdAt)
	if err != nil {
		return model.Event{}, err
	}
	item.Payload = json.RawMessage(defaultJSON(payload, "{}"))
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
