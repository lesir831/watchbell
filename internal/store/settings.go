package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

const authPasswordHashSetting = "auth.password_hash"

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
