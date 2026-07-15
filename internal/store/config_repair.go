package store

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
)

// repairActiveConfigReferences upgrades databases created by soft-delete
// releases that left active JSON references pointing at archived entities.
// History rows are never removed; only currently runnable configuration is
// made internally consistent.
func (s *Store) repairActiveConfigReferences(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := nowString()

	if _, err := tx.ExecContext(ctx, `UPDATE rules SET enabled = 0, deleted_at = ?, updated_at = ?
		WHERE deleted_at IS NULL AND NOT EXISTS (SELECT 1 FROM monitors m WHERE m.id = rules.monitor_id AND m.deleted_at IS NULL)`, now, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE rules SET template_id = NULL, updated_at = ?
		WHERE deleted_at IS NULL AND template_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM notification_templates t WHERE t.id = rules.template_id AND t.deleted_at IS NULL)`, now); err != nil {
		return err
	}

	channelRows, err := tx.QueryContext(ctx, `SELECT id FROM notify_channels WHERE deleted_at IS NULL`)
	if err != nil {
		return err
	}
	activeChannels := map[int64]struct{}{}
	for channelRows.Next() {
		var id int64
		if err := channelRows.Scan(&id); err != nil {
			channelRows.Close()
			return err
		}
		activeChannels[id] = struct{}{}
	}
	if err := channelRows.Err(); err != nil {
		channelRows.Close()
		return err
	}
	channelRows.Close()

	type reference struct {
		id  int64
		raw string
	}
	readReferences := func(query string) ([]reference, error) {
		rows, err := tx.QueryContext(ctx, query)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		items := make([]reference, 0)
		for rows.Next() {
			var item reference
			if err := rows.Scan(&item.id, &item.raw); err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		return items, rows.Err()
	}

	ruleReferences, err := readReferences(`SELECT id, notify_channel_ids_json FROM rules WHERE deleted_at IS NULL`)
	if err != nil {
		return err
	}
	for _, reference := range ruleReferences {
		ids, err := decodeReferenceIDs(reference.raw)
		if err != nil {
			return fmt.Errorf("repair rule %d channels: %w", reference.id, err)
		}
		filtered := existingSnapshotIDs(ids, activeChannels)
		if slices.Equal(ids, filtered) {
			continue
		}
		encoded, _ := json.Marshal(filtered)
		if len(filtered) == 0 {
			if _, err := tx.ExecContext(ctx, `UPDATE rules SET enabled = 0, deleted_at = ?, notify_channel_ids_json = '[]', updated_at = ? WHERE id = ? AND deleted_at IS NULL`, now, now, reference.id); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE rules SET notify_channel_ids_json = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`, string(encoded), now, reference.id); err != nil {
			return err
		}
	}

	monitorReferences, err := readReferences(`SELECT id, failure_notify_channel_ids_json FROM monitors WHERE deleted_at IS NULL`)
	if err != nil {
		return err
	}
	for _, reference := range monitorReferences {
		ids, err := decodeReferenceIDs(reference.raw)
		if err != nil {
			return fmt.Errorf("repair monitor %d failure channels: %w", reference.id, err)
		}
		filtered := existingSnapshotIDs(ids, activeChannels)
		if slices.Equal(ids, filtered) {
			continue
		}
		encoded, _ := json.Marshal(filtered)
		if len(filtered) == 0 {
			if _, err := tx.ExecContext(ctx, `UPDATE monitors SET failure_alert_after = 0, failure_notify_channel_ids_json = ?, failure_alert_active = 0, updated_at = ? WHERE id = ? AND deleted_at IS NULL`, string(encoded), now, reference.id); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE monitors SET failure_notify_channel_ids_json = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`, string(encoded), now, reference.id); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, `UPDATE event_outbox SET status = 'processed', last_error = 'monitor archived before dispatch', updated_at = ?
		WHERE status IN ('pending', 'processing') AND EXISTS (SELECT 1 FROM events e JOIN monitors m ON m.id = e.monitor_id WHERE e.id = event_outbox.event_id AND m.deleted_at IS NOT NULL)`, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE notification_attempts SET next_retry_at = NULL, retry_claimed_at = NULL,
		error = CASE WHEN error = '' THEN 'retry stopped: referenced configuration archived' ELSE error || '; retry stopped: referenced configuration archived' END
		WHERE status = 'failed' AND next_retry_at IS NOT NULL AND (
			(channel_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM notify_channels c WHERE c.id = notification_attempts.channel_id AND c.deleted_at IS NULL)) OR
			(monitor_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM monitors m WHERE m.id = notification_attempts.monitor_id AND m.deleted_at IS NULL))
		)`); err != nil {
		return err
	}

	return tx.Commit()
}

func decodeReferenceIDs(raw string) ([]int64, error) {
	var ids []int64
	if err := json.Unmarshal([]byte(defaultJSON(raw, "[]")), &ids); err != nil {
		return nil, err
	}
	return normalizedIDs(ids), nil
}
