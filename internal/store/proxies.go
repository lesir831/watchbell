package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/watchbell/watchbell/internal/model"
)

var ErrProxyInUse = errors.New("proxy profile is still assigned to a monitor")
var ErrProxyUnavailable = errors.New("proxy profile is unavailable")

func (s *Store) ListProxyProfiles(ctx context.Context) ([]model.ProxyProfile, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, type, host, port, username, password, created_at, updated_at FROM proxy_profiles WHERE deleted_at IS NULL ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]model.ProxyProfile, 0)
	for rows.Next() {
		item, err := scanProxyProfile(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) GetProxyProfile(ctx context.Context, id int64) (model.ProxyProfile, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, name, type, host, port, username, password, created_at, updated_at FROM proxy_profiles WHERE id = ? AND deleted_at IS NULL`, id)
	return scanProxyProfile(row)
}

func (s *Store) CreateProxyProfile(ctx context.Context, input model.ProxyProfileInput) (model.ProxyProfile, error) {
	input = normalizeProxyStorageInput(input)
	now := nowString()
	name := input.Name
	res, err := s.db.ExecContext(ctx, `INSERT INTO proxy_profiles (name, type, host, port, username, password, created_at, updated_at)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?
		WHERE NOT EXISTS (SELECT 1 FROM proxy_profiles WHERE name = ? AND deleted_at IS NULL)`,
		name, input.Type, input.Host, input.Port, input.Username, input.Password, now, now, name)
	if err != nil {
		return model.ProxyProfile{}, err
	}
	if duplicate, err := insertWasSkipped(res); err != nil {
		return model.ProxyProfile{}, err
	} else if duplicate {
		return model.ProxyProfile{}, fmt.Errorf("%w: proxy profile (%s)", ErrDuplicateNaturalKey, name)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return model.ProxyProfile{}, err
	}
	return s.GetProxyProfile(ctx, id)
}

func (s *Store) UpdateProxyProfile(ctx context.Context, id int64, input model.ProxyProfileInput) (model.ProxyProfile, error) {
	input = normalizeProxyStorageInput(input)
	name := input.Name
	res, err := s.db.ExecContext(ctx, `UPDATE proxy_profiles SET name = ?, type = ?, host = ?, port = ?, username = ?, password = ?, updated_at = ?
		WHERE id = ? AND deleted_at IS NULL
		AND NOT EXISTS (SELECT 1 FROM proxy_profiles duplicate WHERE duplicate.name = ? AND duplicate.deleted_at IS NULL AND duplicate.id <> ?)`,
		name, input.Type, input.Host, input.Port, input.Username, input.Password, nowString(), id, name, id)
	if err != nil {
		return model.ProxyProfile{}, err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		if exists, lookupErr := s.activeRecordExists(ctx, "proxy_profiles", id); lookupErr != nil {
			return model.ProxyProfile{}, lookupErr
		} else if !exists {
			return model.ProxyProfile{}, sql.ErrNoRows
		}
		return model.ProxyProfile{}, fmt.Errorf("%w: proxy profile (%s)", ErrDuplicateNaturalKey, name)
	}
	return s.GetProxyProfile(ctx, id)
}

func normalizeProxyStorageInput(input model.ProxyProfileInput) model.ProxyProfileInput {
	input.Name = strings.TrimSpace(input.Name)
	input.Type = strings.ToLower(strings.TrimSpace(input.Type))
	input.Host = strings.TrimSpace(input.Host)
	input.Username = strings.TrimSpace(input.Username)
	if strings.HasPrefix(input.Host, "[") && strings.HasSuffix(input.Host, "]") {
		input.Host = strings.TrimSuffix(strings.TrimPrefix(input.Host, "["), "]")
	}
	return input
}

func (s *Store) requireActiveProxy(ctx context.Context, id *int64) error {
	if id == nil {
		return nil
	}
	if _, err := s.GetProxyProfile(ctx, *id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: %d", ErrProxyUnavailable, *id)
		}
		return err
	}
	return nil
}

func (s *Store) DeleteProxyProfile(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var references int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM monitors WHERE proxy_id = ? AND deleted_at IS NULL`, id).Scan(&references); err != nil {
		return err
	}
	if references > 0 {
		return fmt.Errorf("%w: %d monitor(s)", ErrProxyInUse, references)
	}
	res, err := tx.ExecContext(ctx, `UPDATE proxy_profiles SET deleted_at = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`, nowString(), nowString(), id)
	if err != nil {
		return err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

func scanProxyProfile(row scanner) (model.ProxyProfile, error) {
	var item model.ProxyProfile
	var createdAt, updatedAt string
	err := row.Scan(&item.ID, &item.Name, &item.Type, &item.Host, &item.Port, &item.Username, &item.Password, &createdAt, &updatedAt)
	if err != nil {
		return model.ProxyProfile{}, err
	}
	item.CreatedAt = parseTime(createdAt)
	item.UpdatedAt = parseTime(updatedAt)
	return item, nil
}
