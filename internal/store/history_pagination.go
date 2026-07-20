package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/watchbell/watchbell/internal/model"
)

const maxHistoryPageSize = 500

// PageRequest identifies a one-based result page. Invalid values are normalized
// to page 1 and 100 items per page; PageSize is capped at 500.
type PageRequest struct {
	Page     int `json:"page"`
	PageSize int `json:"pageSize"`
}

// Page is the stable response shape used by all paginated history queries.
type Page[T any] struct {
	Items      []T   `json:"items"`
	Page       int   `json:"page"`
	PageSize   int   `json:"pageSize"`
	Total      int64 `json:"total"`
	TotalPages int   `json:"totalPages"`
}

type HistoryTimeRange struct {
	From *time.Time
	To   *time.Time
}

type CheckRunFilter struct {
	PageRequest
	HistoryTimeRange
	MonitorID   int64
	Status      string
	Trigger     string
	MonitorType string
}

type EventFilter struct {
	PageRequest
	HistoryTimeRange
	EventID    int64
	MonitorID  int64
	CheckRunID int64
	Type       string
}

type RuleEvaluationFilter struct {
	PageRequest
	HistoryTimeRange
	EventID int64
	RuleID  int64
	Status  string
}

type NotificationAttemptFilter struct {
	PageRequest
	HistoryTimeRange
	MonitorID   int64
	EventID     int64
	ChannelID   int64
	Status      string
	Kind        string
	ChannelType string
}

type AuditLogFilter struct {
	PageRequest
	HistoryTimeRange
	Actor      string
	Action     string
	EntityType string
	EntityID   int64
}

func (s *Store) ListCheckRunsPage(ctx context.Context, filter CheckRunFilter) (Page[model.CheckRun], error) {
	request := normalizePageRequest(filter.PageRequest)
	clauses, args, err := historyConditions(filter.HistoryTimeRange, "created_at")
	if err != nil {
		return Page[model.CheckRun]{}, err
	}
	addInt64Condition(&clauses, &args, "monitor_id", filter.MonitorID)
	addStringCondition(&clauses, &args, "status", filter.Status)
	addStringCondition(&clauses, &args, "trigger", filter.Trigger)
	addStringCondition(&clauses, &args, "monitor_type", filter.MonitorType)
	where := historyWhere(clauses)

	total, err := s.historyCount(ctx, "check_runs", where, args)
	if err != nil {
		return Page[model.CheckRun]{}, err
	}
	queryArgs := appendPageArgs(args, request)
	rows, err := s.db.QueryContext(ctx, `SELECT id, monitor_id, monitor_name, monitor_type, trigger, config_json, status, message, error, event_count, duration_ms, started_at, finished_at, created_at FROM check_runs`+where+` ORDER BY id DESC LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return Page[model.CheckRun]{}, err
	}
	defer rows.Close()
	items := make([]model.CheckRun, 0)
	for rows.Next() {
		item, err := scanCheckRun(rows)
		if err != nil {
			return Page[model.CheckRun]{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return Page[model.CheckRun]{}, err
	}
	return historyPage(items, request, total), nil
}

func (s *Store) ListEventsPage(ctx context.Context, filter EventFilter) (Page[model.Event], error) {
	request := normalizePageRequest(filter.PageRequest)
	clauses, args, err := historyConditions(filter.HistoryTimeRange, "e.created_at")
	if err != nil {
		return Page[model.Event]{}, err
	}
	addInt64Condition(&clauses, &args, "e.id", filter.EventID)
	addInt64Condition(&clauses, &args, "e.monitor_id", filter.MonitorID)
	addInt64Condition(&clauses, &args, "l.check_run_id", filter.CheckRunID)
	addStringCondition(&clauses, &args, "e.type", filter.Type)
	where := historyWhere(clauses)

	total, err := s.historyCount(ctx, "events e LEFT JOIN event_check_runs l ON l.event_id = e.id", where, args)
	if err != nil {
		return Page[model.Event]{}, err
	}
	queryArgs := appendPageArgs(args, request)
	rows, err := s.db.QueryContext(ctx, `SELECT e.id, e.monitor_id, l.check_run_id, e.type, e.fingerprint, e.payload_json, e.created_at FROM events e LEFT JOIN event_check_runs l ON l.event_id = e.id`+where+` ORDER BY e.id DESC LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return Page[model.Event]{}, err
	}
	defer rows.Close()
	items := make([]model.Event, 0)
	for rows.Next() {
		item, err := scanEvent(rows)
		if err != nil {
			return Page[model.Event]{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return Page[model.Event]{}, err
	}
	return historyPage(items, request, total), nil
}

func (s *Store) ListRuleEvaluationsPage(ctx context.Context, filter RuleEvaluationFilter) (Page[model.RuleEvaluation], error) {
	request := normalizePageRequest(filter.PageRequest)
	clauses, args, err := historyConditions(filter.HistoryTimeRange, "created_at")
	if err != nil {
		return Page[model.RuleEvaluation]{}, err
	}
	addInt64Condition(&clauses, &args, "event_id", filter.EventID)
	addInt64Condition(&clauses, &args, "rule_id", filter.RuleID)
	addStringCondition(&clauses, &args, "status", filter.Status)
	where := historyWhere(clauses)

	total, err := s.historyCount(ctx, "rule_evaluations", where, args)
	if err != nil {
		return Page[model.RuleEvaluation]{}, err
	}
	queryArgs := appendPageArgs(args, request)
	rows, err := s.db.QueryContext(ctx, `SELECT id, event_id, rule_id, rule_name, status, reason, matched_json, created_at FROM rule_evaluations`+where+` ORDER BY id DESC LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return Page[model.RuleEvaluation]{}, err
	}
	defer rows.Close()
	items := make([]model.RuleEvaluation, 0)
	for rows.Next() {
		item, err := scanRuleEvaluation(rows)
		if err != nil {
			return Page[model.RuleEvaluation]{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return Page[model.RuleEvaluation]{}, err
	}
	return historyPage(items, request, total), nil
}

func (s *Store) ListNotificationAttemptsPage(ctx context.Context, filter NotificationAttemptFilter) (Page[model.NotificationAttempt], error) {
	request := normalizePageRequest(filter.PageRequest)
	clauses, args, err := historyConditions(filter.HistoryTimeRange, "created_at")
	if err != nil {
		return Page[model.NotificationAttempt]{}, err
	}
	if filter.MonitorID > 0 {
		// Older notification-attempt rows may predate the denormalized
		// monitor_id column. Keep those rows discoverable through their event.
		clauses = append(clauses, `(monitor_id = ? OR (monitor_id IS NULL AND event_id IN (SELECT id FROM events WHERE monitor_id = ?)))`)
		args = append(args, filter.MonitorID, filter.MonitorID)
	}
	addInt64Condition(&clauses, &args, "event_id", filter.EventID)
	addInt64Condition(&clauses, &args, "channel_id", filter.ChannelID)
	addStringCondition(&clauses, &args, "status", filter.Status)
	addStringCondition(&clauses, &args, "kind", filter.Kind)
	addStringCondition(&clauses, &args, "channel_type", filter.ChannelType)
	where := historyWhere(clauses)

	total, err := s.historyCount(ctx, "notification_attempts", where, args)
	if err != nil {
		return Page[model.NotificationAttempt]{}, err
	}
	queryArgs := appendPageArgs(args, request)
	rows, err := s.db.QueryContext(ctx, notificationAttemptSelect+where+` ORDER BY id DESC LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return Page[model.NotificationAttempt]{}, err
	}
	defer rows.Close()
	items := make([]model.NotificationAttempt, 0)
	for rows.Next() {
		item, err := scanNotificationAttempt(rows)
		if err != nil {
			return Page[model.NotificationAttempt]{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return Page[model.NotificationAttempt]{}, err
	}
	return historyPage(items, request, total), nil
}

func (s *Store) ListAuditLogsPage(ctx context.Context, filter AuditLogFilter) (Page[model.AuditLog], error) {
	request := normalizePageRequest(filter.PageRequest)
	clauses, args, err := historyConditions(filter.HistoryTimeRange, "created_at")
	if err != nil {
		return Page[model.AuditLog]{}, err
	}
	addStringCondition(&clauses, &args, "actor", filter.Actor)
	addStringCondition(&clauses, &args, "action", filter.Action)
	addStringCondition(&clauses, &args, "entity_type", filter.EntityType)
	addInt64Condition(&clauses, &args, "entity_id", filter.EntityID)
	where := historyWhere(clauses)

	total, err := s.historyCount(ctx, "audit_logs", where, args)
	if err != nil {
		return Page[model.AuditLog]{}, err
	}
	queryArgs := appendPageArgs(args, request)
	rows, err := s.db.QueryContext(ctx, `SELECT id, actor, action, entity_type, entity_id, summary, changes_json, created_at FROM audit_logs`+where+` ORDER BY id DESC LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return Page[model.AuditLog]{}, err
	}
	defer rows.Close()
	items := make([]model.AuditLog, 0)
	for rows.Next() {
		item, err := scanAuditLog(rows)
		if err != nil {
			return Page[model.AuditLog]{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return Page[model.AuditLog]{}, err
	}
	return historyPage(items, request, total), nil
}

func normalizePageRequest(request PageRequest) PageRequest {
	if request.Page <= 0 {
		request.Page = 1
	}
	if request.PageSize <= 0 {
		request.PageSize = 100
	}
	if request.PageSize > maxHistoryPageSize {
		request.PageSize = maxHistoryPageSize
	}
	return request
}

func historyConditions(timeRange HistoryTimeRange, column string) ([]string, []any, error) {
	if timeRange.From != nil && timeRange.To != nil && timeRange.From.After(*timeRange.To) {
		return nil, nil, fmt.Errorf("history time range: from must not be after to")
	}
	clauses := make([]string, 0, 2)
	args := make([]any, 0, 2)
	if timeRange.From != nil {
		clauses = append(clauses, "julianday("+column+") >= julianday(?)")
		args = append(args, formatTime(timeRange.From.UTC()))
	}
	if timeRange.To != nil {
		clauses = append(clauses, "julianday("+column+") <= julianday(?)")
		args = append(args, formatTime(timeRange.To.UTC()))
	}
	return clauses, args, nil
}

func addInt64Condition(clauses *[]string, args *[]any, column string, value int64) {
	if value <= 0 {
		return
	}
	*clauses = append(*clauses, column+" = ?")
	*args = append(*args, value)
}

func addStringCondition(clauses *[]string, args *[]any, column, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	*clauses = append(*clauses, column+" = ?")
	*args = append(*args, value)
}

func historyWhere(clauses []string) string {
	if len(clauses) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(clauses, " AND ")
}

func (s *Store) historyCount(ctx context.Context, from, where string, args []any) (int64, error) {
	var total int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+from+where, args...).Scan(&total)
	if err != nil && err != sql.ErrNoRows {
		return 0, err
	}
	return total, nil
}

func appendPageArgs(args []any, request PageRequest) []any {
	result := make([]any, 0, len(args)+2)
	result = append(result, args...)
	result = append(result, request.PageSize, (request.Page-1)*request.PageSize)
	return result
}

func historyPage[T any](items []T, request PageRequest, total int64) Page[T] {
	totalPages := 0
	if total > 0 {
		totalPages = int((total + int64(request.PageSize) - 1) / int64(request.PageSize))
	}
	return Page[T]{Items: items, Page: request.Page, PageSize: request.PageSize, Total: total, TotalPages: totalPages}
}
