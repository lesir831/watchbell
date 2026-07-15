package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/watchbell/watchbell/internal/model"
)

const maxOutboxAttempts = 10
const notificationRetryLease = 5 * time.Minute

// ErrNotificationRetryConflict means the source attempt has already been
// claimed or superseded by a successor. It is safe for callers to map this to
// HTTP 409 because retrying the same request would duplicate an in-flight or
// completed delivery.
var ErrNotificationRetryConflict = errors.New("notification retry conflicts with an existing retry")

type OutboxItem struct {
	EventID  int64
	Attempts int
}

type notificationAttemptExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func (s *Store) CreateCheckRun(ctx context.Context, monitor model.Monitor, trigger string, config json.RawMessage) (model.CheckRun, error) {
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `INSERT INTO check_runs (monitor_id, monitor_name, monitor_type, trigger, config_json, status, started_at, created_at) VALUES (?, ?, ?, ?, ?, 'running', ?, ?)`,
		monitor.ID, monitor.Name, monitor.Type, trigger, string(normalizedJSON(config, "{}")), formatTime(now), formatTime(now))
	if err != nil {
		return model.CheckRun{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return model.CheckRun{}, err
	}
	return s.GetCheckRun(ctx, id)
}

func (s *Store) FinishCheckRun(ctx context.Context, id int64, status, message string, runErr error, eventCount int, started time.Time) error {
	finished := time.Now().UTC()
	errText := ""
	if runErr != nil {
		errText = runErr.Error()
	}
	duration := finished.Sub(started).Milliseconds()
	if duration < 0 {
		duration = 0
	}
	_, err := s.db.ExecContext(ctx, `UPDATE check_runs SET status = ?, message = ?, error = ?, event_count = ?, duration_ms = ?, finished_at = ? WHERE id = ?`,
		status, strings.TrimSpace(message), errText, eventCount, duration, formatTime(finished), id)
	return err
}

func (s *Store) GetCheckRun(ctx context.Context, id int64) (model.CheckRun, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, monitor_id, monitor_name, monitor_type, trigger, config_json, status, message, error, event_count, duration_ms, started_at, finished_at, created_at FROM check_runs WHERE id = ?`, id)
	return scanCheckRun(row)
}

func (s *Store) ListCheckRuns(ctx context.Context, limit int) ([]model.CheckRun, error) {
	page, err := s.ListCheckRunsPage(ctx, CheckRunFilter{PageRequest: PageRequest{Page: 1, PageSize: normalizeLimit(limit)}})
	return page.Items, err
}

func (s *Store) CreateRuleEvaluation(ctx context.Context, eventID int64, ruleID *int64, ruleName, status, reason string, matched []string) (model.RuleEvaluation, error) {
	if matched == nil {
		matched = []string{}
	}
	data, err := json.Marshal(matched)
	if err != nil {
		return model.RuleEvaluation{}, err
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO rule_evaluations (event_id, rule_id, rule_name, status, reason, matched_json, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		eventID, nullableInt64(ruleID), ruleName, status, reason, string(data), nowString())
	if err != nil {
		return model.RuleEvaluation{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return model.RuleEvaluation{}, err
	}
	return s.GetRuleEvaluation(ctx, id)
}

func (s *Store) GetRuleEvaluation(ctx context.Context, id int64) (model.RuleEvaluation, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, event_id, rule_id, rule_name, status, reason, matched_json, created_at FROM rule_evaluations WHERE id = ?`, id)
	return scanRuleEvaluation(row)
}

func (s *Store) ListRuleEvaluations(ctx context.Context, limit int) ([]model.RuleEvaluation, error) {
	page, err := s.ListRuleEvaluationsPage(ctx, RuleEvaluationFilter{PageRequest: PageRequest{Page: 1, PageSize: normalizeLimit(limit)}})
	return page.Items, err
}

func (s *Store) CreateNotificationAttempt(ctx context.Context, input model.NotificationAttemptInput) (model.NotificationAttempt, error) {
	if input.AttemptNo <= 0 {
		input.AttemptNo = 1
	}
	if strings.TrimSpace(input.Kind) == "" {
		input.Kind = "delivery"
	}
	// A retry source may have at most one direct successor. A retry insert also
	// clears the source schedule and lease in the same transaction, so a crash
	// cannot leave a superseded source eligible for delivery again.
	var executor notificationAttemptExecer = s.db
	var tx *sql.Tx
	var err error
	if input.RetryOfID != nil {
		tx, err = s.db.BeginTx(ctx, nil)
		if err != nil {
			return model.NotificationAttempt{}, err
		}
		defer tx.Rollback()
		executor = tx
	}
	res, err := executor.ExecContext(ctx, `INSERT INTO notification_attempts (monitor_id, event_id, rule_evaluation_id, channel_id, retry_of_id, channel_name, channel_type, kind, status, subject, body, data_json, error, attempt_no, duration_ms, sent_at, next_retry_at, created_at)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		WHERE ? IS NULL OR NOT EXISTS (SELECT 1 FROM notification_attempts successor WHERE successor.retry_of_id = ?)`,
		nullableInt64(input.MonitorID), nullableInt64(input.EventID), nullableInt64(input.RuleEvaluationID), nullableInt64(input.ChannelID), nullableInt64(input.RetryOfID), input.ChannelName, input.ChannelType,
		input.Kind, input.Status, input.Subject, input.Body, string(normalizedJSON(input.Data, "{}")), input.Error, input.AttemptNo, input.DurationMS, formatTimePtr(input.SentAt), formatTimePtr(input.NextRetryAt), nowString(), nullableInt64(input.RetryOfID), nullableInt64(input.RetryOfID))
	if err != nil {
		return model.NotificationAttempt{}, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return model.NotificationAttempt{}, err
	}
	if affected == 0 {
		return model.NotificationAttempt{}, ErrNotificationRetryConflict
	}
	id, err := res.LastInsertId()
	if err != nil {
		return model.NotificationAttempt{}, err
	}
	if tx != nil {
		result, err := tx.ExecContext(ctx, `UPDATE notification_attempts SET next_retry_at = NULL, retry_claimed_at = NULL WHERE id = ?`, *input.RetryOfID)
		if err != nil {
			return model.NotificationAttempt{}, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return model.NotificationAttempt{}, err
		}
		if affected != 1 {
			return model.NotificationAttempt{}, fmt.Errorf("%w: retry source %d no longer exists", ErrNotificationRetryConflict, *input.RetryOfID)
		}
		if err := tx.Commit(); err != nil {
			return model.NotificationAttempt{}, err
		}
	}
	return s.GetNotificationAttempt(ctx, id)
}

func (s *Store) GetNotificationAttempt(ctx context.Context, id int64) (model.NotificationAttempt, error) {
	row := s.db.QueryRowContext(ctx, notificationAttemptSelect+` WHERE id = ?`, id)
	return scanNotificationAttempt(row)
}

func (s *Store) ListNotificationAttempts(ctx context.Context, limit int) ([]model.NotificationAttempt, error) {
	page, err := s.ListNotificationAttemptsPage(ctx, NotificationAttemptFilter{PageRequest: PageRequest{Page: 1, PageSize: normalizeLimit(limit)}})
	return page.Items, err
}

func (s *Store) ListDueNotificationAttempts(ctx context.Context, limit int, now time.Time) ([]model.NotificationAttempt, error) {
	limit = normalizeLimit(limit)
	stale := now.UTC().Add(-notificationRetryLease)
	rows, err := s.db.QueryContext(ctx, notificationAttemptSelect+` WHERE status = 'failed' AND next_retry_at IS NOT NULL AND next_retry_at <= ? AND (retry_claimed_at IS NULL OR retry_claimed_at <= ?) AND NOT EXISTS (SELECT 1 FROM notification_attempts successor WHERE successor.retry_of_id = notification_attempts.id) ORDER BY id ASC LIMIT ?`, formatTime(now), formatTime(stale), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]model.NotificationAttempt, 0)
	for rows.Next() {
		item, err := scanNotificationAttempt(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ClaimNotificationAttempt(ctx context.Context, id int64, now time.Time) (bool, error) {
	return s.claimNotificationAttempt(ctx, id, now, true)
}

func (s *Store) ClaimNotificationAttemptNow(ctx context.Context, id int64, now time.Time) (bool, error) {
	return s.claimNotificationAttempt(ctx, id, now, false)
}

func (s *Store) claimNotificationAttempt(ctx context.Context, id int64, now time.Time, requireDue bool) (bool, error) {
	now = now.UTC()
	stale := now.Add(-notificationRetryLease)
	query := `UPDATE notification_attempts SET retry_claimed_at = ? WHERE id = ? AND status = 'failed' AND (retry_claimed_at IS NULL OR retry_claimed_at <= ?) AND NOT EXISTS (SELECT 1 FROM notification_attempts successor WHERE successor.retry_of_id = notification_attempts.id)`
	args := []any{formatTime(now), id, formatTime(stale)}
	if requireDue {
		query += ` AND next_retry_at IS NOT NULL AND next_retry_at <= ?`
		args = append(args, formatTime(now))
	}
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return false, err
	}
	affected, _ := res.RowsAffected()
	return affected == 1, nil
}

func (s *Store) CancelNotificationRetry(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE notification_attempts SET next_retry_at = NULL, retry_claimed_at = NULL WHERE id = ?`, id)
	return err
}

func (s *Store) StopNotificationRetry(ctx context.Context, id int64, reason string) error {
	reason = strings.TrimSpace(reason)
	_, err := s.db.ExecContext(ctx, `UPDATE notification_attempts SET next_retry_at = NULL, retry_claimed_at = NULL,
		error = CASE WHEN ? = '' THEN error WHEN error = '' THEN ? ELSE error || '; ' || ? END WHERE id = ?`, reason, reason, reason, id)
	return err
}

func (s *Store) StopMonitorNotificationRetries(ctx context.Context, monitorID int64, kind, reason string) error {
	reason = strings.TrimSpace(reason)
	_, err := s.db.ExecContext(ctx, `UPDATE notification_attempts SET next_retry_at = NULL, retry_claimed_at = NULL,
		error = CASE WHEN ? = '' THEN error WHEN error = '' THEN ? ELSE error || '; ' || ? END
		WHERE monitor_id = ? AND kind = ? AND status = 'failed' AND next_retry_at IS NOT NULL`, reason, reason, reason, monitorID, kind)
	return err
}

func (s *Store) ReleaseNotificationRetry(ctx context.Context, id int64, next time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE notification_attempts SET next_retry_at = ?, retry_claimed_at = NULL WHERE id = ? AND status = 'failed'`, formatTime(next), id)
	return err
}

func (s *Store) ListDueOutbox(ctx context.Context, limit int, now time.Time) ([]OutboxItem, error) {
	limit = normalizeLimit(limit)
	stale := now.Add(-5 * time.Minute)
	rows, err := s.db.QueryContext(ctx, `SELECT event_id, attempts FROM event_outbox WHERE (status = 'pending' AND next_attempt_at <= ?) OR (status = 'processing' AND updated_at <= ?) ORDER BY created_at ASC LIMIT ?`, formatTime(now), formatTime(stale), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]OutboxItem, 0)
	for rows.Next() {
		var item OutboxItem
		if err := rows.Scan(&item.EventID, &item.Attempts); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ClaimOutbox(ctx context.Context, eventID int64, now time.Time, requireDue bool) (bool, error) {
	now = now.UTC()
	query := `UPDATE event_outbox SET status = 'processing', attempts = attempts + 1, updated_at = ? WHERE event_id = ? AND status = 'pending'`
	args := []any{formatTime(now), eventID}
	if requireDue {
		stale := now.Add(-5 * time.Minute)
		query = `UPDATE event_outbox SET status = 'processing', attempts = attempts + 1, updated_at = ?
			WHERE event_id = ? AND ((status = 'pending' AND next_attempt_at <= ?) OR (status = 'processing' AND updated_at <= ?))`
		args = append(args, formatTime(now), formatTime(stale))
	}
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return false, err
	}
	affected, _ := res.RowsAffected()
	return affected == 1, nil
}

func (s *Store) MarkOutboxProcessed(ctx context.Context, eventID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE event_outbox SET status = 'processed', last_error = '', updated_at = ? WHERE event_id = ?`, nowString(), eventID)
	return err
}

func (s *Store) MarkOutboxFailed(ctx context.Context, eventID int64, attempts int, dispatchErr error) error {
	if attempts < 1 {
		attempts = 1
	}
	shift := attempts
	if shift > 6 {
		shift = 6
	}
	delay := time.Duration(1<<shift) * time.Minute
	next := time.Now().UTC().Add(delay)
	errText := "dispatch failed"
	if dispatchErr != nil {
		errText = dispatchErr.Error()
	}
	if attempts >= maxOutboxAttempts {
		_, err := s.db.ExecContext(ctx, `UPDATE event_outbox SET status = 'dead_letter', attempts = ?, last_error = ?, updated_at = ? WHERE event_id = ?`, attempts, errText, nowString(), eventID)
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE event_outbox SET status = 'pending', next_attempt_at = ?, last_error = ?, updated_at = ? WHERE event_id = ?`, formatTime(next), errText, nowString(), eventID)
	return err
}

func (s *Store) CreateAuditLog(ctx context.Context, actor, action, entityType string, entityID *int64, summary string, changes any) error {
	data, err := json.Marshal(changes)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO audit_logs (actor, action, entity_type, entity_id, summary, changes_json, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		actor, action, entityType, nullableInt64(entityID), summary, string(data), nowString())
	return err
}

func (s *Store) ListAuditLogs(ctx context.Context, limit int) ([]model.AuditLog, error) {
	page, err := s.ListAuditLogsPage(ctx, AuditLogFilter{PageRequest: PageRequest{Page: 1, PageSize: normalizeLimit(limit)}})
	return page.Items, err
}

func (s *Store) DashboardSummary(ctx context.Context) (model.DashboardSummary, error) {
	var result model.DashboardSummary
	queries := []struct {
		destination *int
		query       string
		args        []any
	}{
		{&result.MonitorCount, `SELECT COUNT(*) FROM monitors WHERE deleted_at IS NULL`, nil},
		{&result.HealthyMonitors, `SELECT COUNT(*) FROM monitors WHERE deleted_at IS NULL AND enabled = 1 AND last_status NOT IN ('', 'error', 'warning')`, nil},
		{&result.FailingMonitors, `SELECT COUNT(*) FROM monitors WHERE deleted_at IS NULL AND enabled = 1 AND last_status IN ('error', 'warning')`, nil},
		{&result.PendingMonitors, `SELECT COUNT(*) FROM monitors WHERE deleted_at IS NULL AND enabled = 1 AND last_status = ''`, nil},
		{&result.RuleCount, `SELECT COUNT(*) FROM rules WHERE deleted_at IS NULL`, nil},
		{&result.ChannelCount, `SELECT COUNT(*) FROM notify_channels WHERE deleted_at IS NULL`, nil},
		{&result.EventsLast24Hours, `SELECT COUNT(*) FROM events WHERE julianday(created_at) >= julianday(?)`, []any{formatTime(time.Now().UTC().Add(-24 * time.Hour))}},
		{&result.FailedAttempts, `SELECT COUNT(*) FROM notification_attempts WHERE status = 'failed' AND julianday(created_at) >= julianday(?)`, []any{formatTime(time.Now().UTC().Add(-24 * time.Hour))}},
	}
	for _, item := range queries {
		if err := s.db.QueryRowContext(ctx, item.query, item.args...).Scan(item.destination); err != nil {
			return result, err
		}
	}
	return result, nil
}

func scanCheckRun(row scanner) (model.CheckRun, error) {
	var item model.CheckRun
	var config, startedAt, createdAt string
	var finishedAt sql.NullString
	err := row.Scan(&item.ID, &item.MonitorID, &item.MonitorName, &item.MonitorType, &item.Trigger, &config, &item.Status, &item.Message, &item.Error, &item.EventCount, &item.DurationMS, &startedAt, &finishedAt, &createdAt)
	if err != nil {
		return item, err
	}
	item.ConfigSnapshot = json.RawMessage(defaultJSON(config, "{}"))
	item.StartedAt = parseTime(startedAt)
	item.FinishedAt = parseTimePtr(finishedAt)
	item.CreatedAt = parseTime(createdAt)
	return item, nil
}

func scanRuleEvaluation(row scanner) (model.RuleEvaluation, error) {
	var item model.RuleEvaluation
	var ruleID sql.NullInt64
	var matched, createdAt string
	err := row.Scan(&item.ID, &item.EventID, &ruleID, &item.RuleName, &item.Status, &item.Reason, &matched, &createdAt)
	if err != nil {
		return item, err
	}
	if ruleID.Valid {
		item.RuleID = &ruleID.Int64
	}
	item.Matched = json.RawMessage(defaultJSON(matched, "[]"))
	item.CreatedAt = parseTime(createdAt)
	return item, nil
}

const notificationAttemptSelect = `SELECT id, monitor_id, event_id, rule_evaluation_id, channel_id, retry_of_id, channel_name, channel_type, kind, status, subject, body, data_json, error, attempt_no, duration_ms, sent_at, next_retry_at,
	CASE WHEN status = 'failed' AND NOT EXISTS (SELECT 1 FROM notification_attempts successor WHERE successor.retry_of_id = notification_attempts.id) THEN 1 ELSE 0 END,
	CASE WHEN EXISTS (SELECT 1 FROM notification_attempts successor WHERE successor.retry_of_id = notification_attempts.id) THEN 1 ELSE 0 END,
	created_at FROM notification_attempts`

func scanNotificationAttempt(row scanner) (model.NotificationAttempt, error) {
	var item model.NotificationAttempt
	var monitorID, eventID, evaluationID, channelID, retryOfID sql.NullInt64
	var sentAt, nextRetryAt sql.NullString
	var retriable, resolved int
	var data, createdAt string
	err := row.Scan(&item.ID, &monitorID, &eventID, &evaluationID, &channelID, &retryOfID, &item.ChannelName, &item.ChannelType, &item.Kind, &item.Status, &item.Subject, &item.Body, &data, &item.Error, &item.AttemptNo, &item.DurationMS, &sentAt, &nextRetryAt, &retriable, &resolved, &createdAt)
	if err != nil {
		return item, err
	}
	item.MonitorID = nullInt64Ptr(monitorID)
	item.EventID = nullInt64Ptr(eventID)
	item.RuleEvaluationID = nullInt64Ptr(evaluationID)
	item.ChannelID = nullInt64Ptr(channelID)
	item.RetryOfID = nullInt64Ptr(retryOfID)
	item.Data = json.RawMessage(defaultJSON(data, "{}"))
	item.SentAt = parseTimePtr(sentAt)
	item.NextRetryAt = parseTimePtr(nextRetryAt)
	item.Retriable = retriable == 1
	item.Resolved = resolved == 1
	item.CreatedAt = parseTime(createdAt)
	return item, nil
}

func scanAuditLog(row scanner) (model.AuditLog, error) {
	var item model.AuditLog
	var entityID sql.NullInt64
	var changes, createdAt string
	err := row.Scan(&item.ID, &item.Actor, &item.Action, &item.EntityType, &entityID, &item.Summary, &changes, &createdAt)
	if err != nil {
		return item, err
	}
	item.EntityID = nullInt64Ptr(entityID)
	item.Changes = json.RawMessage(defaultJSON(changes, "{}"))
	item.CreatedAt = parseTime(createdAt)
	return item, nil
}

func normalizeLimit(limit int) int {
	if limit <= 0 || limit > 500 {
		return 100
	}
	return limit
}

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullInt64Ptr(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	return &value.Int64
}

func (s *Store) DebugCounts(ctx context.Context) (map[string]int, error) {
	tables := []string{"monitors", "rules", "notify_channels", "check_runs", "events", "rule_evaluations", "notification_attempts", "event_outbox"}
	result := make(map[string]int, len(tables))
	for _, table := range tables {
		var count int
		if err := s.db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s", table)).Scan(&count); err != nil {
			return nil, err
		}
		result[table] = count
	}
	outbox, err := s.OutboxStatusCounts(ctx)
	if err != nil {
		return nil, err
	}
	for status, count := range outbox {
		result["event_outbox_"+status] = count
	}
	return result, nil
}

func (s *Store) OutboxStatusCounts(ctx context.Context) (map[string]int, error) {
	result := map[string]int{"pending": 0, "processing": 0, "processed": 0, "dead_letter": 0}
	rows, err := s.db.QueryContext(ctx, `SELECT status, COUNT(*) FROM event_outbox GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		result[status] = count
	}
	return result, rows.Err()
}
