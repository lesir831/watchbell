package store

import "context"

// TryActivateMonitorFailureAlert marks a monitor's current failure incident as
// announced. The conditional update makes concurrent/manual checks idempotent:
// only the caller that changes the flag should send the incident notification.
func (s *Store) TryActivateMonitorFailureAlert(ctx context.Context, monitorID int64) (bool, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE monitors
		SET failure_alert_active = 1, updated_at = ?
		WHERE id = ? AND deleted_at IS NULL
		  AND failure_alert_active = 0
		  AND failure_alert_after > 0
		  AND consecutive_failures >= failure_alert_after`, nowString(), monitorID)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

// TryClearMonitorFailureAlert closes an announced incident before recovery
// delivery is attempted. A failed recovery delivery is retried through the
// normal notification-attempt worker without re-opening or permanently
// locking the incident state.
func (s *Store) TryClearMonitorFailureAlert(ctx context.Context, monitorID int64) (bool, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE monitors
		SET failure_alert_active = 0, updated_at = ?
		WHERE id = ? AND deleted_at IS NULL AND failure_alert_active = 1`, nowString(), monitorID)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}
