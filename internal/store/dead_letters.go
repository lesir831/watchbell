package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/watchbell/watchbell/internal/model"
)

type DeadLetterFilter struct {
	PageRequest
	MonitorID int64
}

func (s *Store) ListDeadLettersPage(ctx context.Context, filter DeadLetterFilter) (Page[model.DeadLetter], error) {
	request := normalizePageRequest(filter.PageRequest)
	clauses := []string{"o.status = 'dead_letter'"}
	args := make([]any, 0, 1)
	addInt64Condition(&clauses, &args, "e.monitor_id", filter.MonitorID)
	where := historyWhere(clauses)
	total, err := s.historyCount(ctx, "event_outbox o JOIN events e ON e.id = o.event_id", where, args)
	if err != nil {
		return Page[model.DeadLetter]{}, err
	}
	queryArgs := appendPageArgs(args, request)
	rows, err := s.db.QueryContext(ctx, `SELECT o.event_id, e.monitor_id, m.name, e.type, e.fingerprint, o.attempts, o.last_error, e.created_at, o.updated_at
		FROM event_outbox o JOIN events e ON e.id = o.event_id JOIN monitors m ON m.id = e.monitor_id`+where+` ORDER BY o.updated_at DESC, o.event_id DESC LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return Page[model.DeadLetter]{}, err
	}
	defer rows.Close()
	items := make([]model.DeadLetter, 0)
	for rows.Next() {
		var item model.DeadLetter
		var eventCreated, updatedAt string
		if err := rows.Scan(&item.EventID, &item.MonitorID, &item.MonitorName, &item.EventType, &item.Fingerprint, &item.Attempts, &item.LastError, &eventCreated, &updatedAt); err != nil {
			return Page[model.DeadLetter]{}, err
		}
		item.EventCreated = parseTime(eventCreated)
		item.UpdatedAt = parseTime(updatedAt)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return Page[model.DeadLetter]{}, err
	}
	return historyPage(items, request, total), nil
}

func (s *Store) RequeueDeadLetter(ctx context.Context, eventID int64, now time.Time) error {
	now = now.UTC()
	res, err := s.db.ExecContext(ctx, `UPDATE event_outbox SET status = 'pending', attempts = 0, next_attempt_at = ?, last_error = '', updated_at = ?
		WHERE event_id = ? AND status = 'dead_letter'
		AND EXISTS (
			SELECT 1 FROM events e JOIN monitors m ON m.id = e.monitor_id
			WHERE e.id = event_outbox.event_id AND m.deleted_at IS NULL
		)`, formatTime(now), formatTime(now), eventID)
	if err != nil {
		return err
	}
	affected, _ := res.RowsAffected()
	if affected != 1 {
		return sql.ErrNoRows
	}
	return nil
}
