package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// ClaimWeComCallback provides durable at-most-once command admission. No
// message text or credentials are stored. Old receipts exceed the replay window.
func (s *Store) ClaimWeComCallback(ctx context.Context, channelID int64, key string) (bool, error) {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM wecom_callback_receipts WHERE created_at < ?`, time.Now().Add(-24*time.Hour).Unix()); err != nil {
		return false, err
	}
	result, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO wecom_callback_receipts(channel_id, message_key, created_at) VALUES (?, ?, ?)`, channelID, key, time.Now().Unix())
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

// Update only enabled, rather than saving a stale full monitor snapshot over a
// simultaneous editor or scheduler update. Audit and mutation commit together.
func (s *Store) SetMonitorEnabledAudited(ctx context.Context, id int64, enabled bool, actor string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE monitors SET enabled = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`, boolInt(enabled), nowString(), id)
	if err != nil {
		return err
	}
	count, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return sql.ErrNoRows
	}
	changes, _ := json.Marshal(map[string]any{"enabled": enabled})
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_logs(actor, action, entity_type, entity_id, summary, changes_json, created_at) VALUES (?, 'wecom_command', 'monitor', ?, ?, ?, ?)`, actor, id, "企业微信指令修改监控状态", string(changes), nowString())
	if err != nil {
		return err
	}
	return tx.Commit()
}
