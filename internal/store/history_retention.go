package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const (
	defaultRetentionBatchSize = 500
	maxRetentionBatchSize     = 5_000
)

// HistoryRetentionPolicy controls independent retention windows. A zero or
// negative duration disables cleanup for that category.
type HistoryRetentionPolicy struct {
	EventAge               time.Duration
	CheckRunAge            time.Duration
	NotificationAttemptAge time.Duration
	AuditLogAge            time.Duration
	BatchSize              int
}

// UniformHistoryRetention creates a policy that keeps all trace categories for
// the same period.
func UniformHistoryRetention(maxAge time.Duration, batchSize int) HistoryRetentionPolicy {
	return HistoryRetentionPolicy{
		EventAge:               maxAge,
		CheckRunAge:            maxAge,
		NotificationAttemptAge: maxAge,
		AuditLogAge:            maxAge,
		BatchSize:              batchSize,
	}
}

type HistoryCleanupResult struct {
	EventsDeleted               int64 `json:"eventsDeleted"`
	CheckRunsDeleted            int64 `json:"checkRunsDeleted"`
	RuleEvaluationsDeleted      int64 `json:"ruleEvaluationsDeleted"`
	NotificationAttemptsDeleted int64 `json:"notificationAttemptsDeleted"`
	AuditLogsDeleted            int64 `json:"auditLogsDeleted"`
}

func (result HistoryCleanupResult) TotalDeleted() int64 {
	return result.EventsDeleted + result.CheckRunsDeleted + result.RuleEvaluationsDeleted + result.NotificationAttemptsDeleted + result.AuditLogsDeleted
}

// CleanupHistory removes at most one batch from each enabled history category.
// It is safe to call repeatedly. Active check runs, pending outbox entries and
// scheduled notification retries are retained even when older than the cutoff.
func (s *Store) CleanupHistory(ctx context.Context, policy HistoryRetentionPolicy, now time.Time) (HistoryCleanupResult, error) {
	batchSize := policy.BatchSize
	if batchSize <= 0 {
		batchSize = defaultRetentionBatchSize
	}
	if batchSize > maxRetentionBatchSize {
		batchSize = maxRetentionBatchSize
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return HistoryCleanupResult{}, err
	}
	defer tx.Rollback()

	var result HistoryCleanupResult
	if policy.EventAge > 0 {
		if err := cleanupExpiredEvents(ctx, tx, formatTime(now.Add(-policy.EventAge)), batchSize, &result); err != nil {
			return HistoryCleanupResult{}, fmt.Errorf("cleanup events: %w", err)
		}
	}
	if policy.CheckRunAge > 0 {
		count, err := deleteLimited(ctx, tx, "check_runs", "julianday(created_at) < julianday(?) AND status <> 'running'", formatTime(now.Add(-policy.CheckRunAge)), batchSize)
		if err != nil {
			return HistoryCleanupResult{}, fmt.Errorf("cleanup check runs: %w", err)
		}
		result.CheckRunsDeleted = count
	}
	if policy.NotificationAttemptAge > 0 {
		count, err := cleanupExpiredNotificationAttempts(ctx, tx, formatTime(now.Add(-policy.NotificationAttemptAge)), batchSize)
		if err != nil {
			return HistoryCleanupResult{}, fmt.Errorf("cleanup notification attempts: %w", err)
		}
		result.NotificationAttemptsDeleted += count
	}
	if policy.AuditLogAge > 0 {
		count, err := deleteLimited(ctx, tx, "audit_logs", "julianday(created_at) < julianday(?)", formatTime(now.Add(-policy.AuditLogAge)), batchSize)
		if err != nil {
			return HistoryCleanupResult{}, fmt.Errorf("cleanup audit logs: %w", err)
		}
		result.AuditLogsDeleted = count
	}
	if err := tx.Commit(); err != nil {
		return HistoryCleanupResult{}, err
	}
	return result, nil
}

const expiredEventIDs = `SELECT e.id
	FROM events e
	WHERE julianday(e.created_at) < julianday(?)
	  AND NOT EXISTS (
	    SELECT 1 FROM event_outbox o
	    WHERE o.event_id = e.id AND o.status IN ('pending', 'processing')
	  )
	  AND NOT EXISTS (
	    SELECT 1
	    FROM notification_attempts pending
	    LEFT JOIN rule_evaluations pending_evaluation ON pending_evaluation.id = pending.rule_evaluation_id
	    WHERE (pending.event_id = e.id OR pending_evaluation.event_id = e.id)
	      AND pending.next_retry_at IS NOT NULL
	  )
	ORDER BY e.id
	LIMIT ?`

func cleanupExpiredEvents(ctx context.Context, tx *sql.Tx, cutoff string, batchSize int, result *HistoryCleanupResult) error {
	var evaluationCount int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM rule_evaluations WHERE event_id IN (`+expiredEventIDs+`)`, cutoff, batchSize).Scan(&evaluationCount); err != nil {
		return err
	}

	// Attempts have their own retention window and already contain immutable
	// channel/message/data snapshots. Detach them from an expiring event instead
	// of silently shortening their configured retention period.
	_, err := tx.ExecContext(ctx, `UPDATE notification_attempts
		SET event_id = NULL, rule_evaluation_id = NULL
		WHERE event_id IN (`+expiredEventIDs+`)
		   OR rule_evaluation_id IN (SELECT id FROM rule_evaluations WHERE event_id IN (`+expiredEventIDs+`))`,
		cutoff, batchSize, cutoff, batchSize)
	if err != nil {
		return err
	}

	eventResult, err := tx.ExecContext(ctx, `DELETE FROM events WHERE id IN (`+expiredEventIDs+`)`, cutoff, batchSize)
	if err != nil {
		return err
	}
	eventCount, _ := eventResult.RowsAffected()
	result.EventsDeleted = eventCount
	result.RuleEvaluationsDeleted = evaluationCount
	return nil
}

func cleanupExpiredNotificationAttempts(ctx context.Context, tx *sql.Tx, cutoff string, batchSize int) (int64, error) {
	candidates := `SELECT id FROM notification_attempts
		WHERE julianday(created_at) < julianday(?) AND next_retry_at IS NULL
		ORDER BY id LIMIT ?`
	if _, err := tx.ExecContext(ctx, `UPDATE notification_attempts SET retry_of_id = NULL WHERE retry_of_id IN (`+candidates+`)`, cutoff, batchSize); err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM notification_attempts WHERE id IN (`+candidates+`)`, cutoff, batchSize)
	if err != nil {
		return 0, err
	}
	count, _ := result.RowsAffected()
	return count, nil
}

func deleteLimited(ctx context.Context, tx *sql.Tx, table, condition string, cutoff string, batchSize int) (int64, error) {
	// table and condition are internal constants, never user-controlled.
	query := fmt.Sprintf(`DELETE FROM %s WHERE id IN (SELECT id FROM %s WHERE %s ORDER BY id LIMIT ?)`, table, table, condition)
	result, err := tx.ExecContext(ctx, query, cutoff, batchSize)
	if err != nil {
		return 0, err
	}
	count, _ := result.RowsAffected()
	return count, nil
}
