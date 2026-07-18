package store

import (
	"context"
	"database/sql"

	"github.com/watchbell/watchbell/internal/model"
)

// ConfigSnapshot is read from one SQLite transaction so exported references
// cannot straddle concurrent configuration updates.
type ConfigSnapshot struct {
	Proxies   []model.ProxyProfile
	Monitors  []model.Monitor
	Rules     []model.Rule
	Channels  []model.NotifyChannel
	Templates []model.NotificationTemplate
}

func (s *Store) ReadConfigSnapshot(ctx context.Context) (ConfigSnapshot, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ConfigSnapshot{}, err
	}
	defer tx.Rollback()

	var snapshot ConfigSnapshot
	proxyRows, err := tx.QueryContext(ctx, `SELECT id, name, type, host, port, username, password, created_at, updated_at FROM proxy_profiles WHERE deleted_at IS NULL ORDER BY id DESC`)
	if err != nil {
		return ConfigSnapshot{}, err
	}
	snapshot.Proxies, err = scanSnapshotRows(proxyRows, scanProxyProfile)
	if err != nil {
		return ConfigSnapshot{}, err
	}

	monitorRows, err := tx.QueryContext(ctx, `SELECT id, name, type, proxy_id, enabled, interval_seconds, config_json, state_json, last_checked_at, last_status, last_message, last_error, consecutive_failures, failure_alert_after, failure_notify_channel_ids_json, failure_alert_active, created_at, updated_at FROM monitors WHERE deleted_at IS NULL ORDER BY id DESC`)
	if err != nil {
		return ConfigSnapshot{}, err
	}
	snapshot.Monitors, err = scanSnapshotRows(monitorRows, scanMonitor)
	if err != nil {
		return ConfigSnapshot{}, err
	}

	ruleRows, err := tx.QueryContext(ctx, `SELECT id, monitor_id, name, enabled, condition_json, notify_channel_ids_json, template_id, cooldown_seconds, quiet_hours_json, last_fired_at, created_at, updated_at FROM rules WHERE deleted_at IS NULL ORDER BY id DESC`)
	if err != nil {
		return ConfigSnapshot{}, err
	}
	snapshot.Rules, err = scanSnapshotRows(ruleRows, scanRule)
	if err != nil {
		return ConfigSnapshot{}, err
	}

	channelRows, err := tx.QueryContext(ctx, `SELECT id, name, type, enabled, config_json, created_at, updated_at FROM notify_channels WHERE deleted_at IS NULL ORDER BY id DESC`)
	if err != nil {
		return ConfigSnapshot{}, err
	}
	snapshot.Channels, err = scanSnapshotRows(channelRows, scanNotifyChannel)
	if err != nil {
		return ConfigSnapshot{}, err
	}

	templateRows, err := tx.QueryContext(ctx, `SELECT id, name, subject_template, body_template, is_default, created_at, updated_at FROM notification_templates WHERE deleted_at IS NULL ORDER BY is_default DESC, id DESC`)
	if err != nil {
		return ConfigSnapshot{}, err
	}
	snapshot.Templates, err = scanSnapshotRows(templateRows, scanNotificationTemplate)
	if err != nil {
		return ConfigSnapshot{}, err
	}
	snapshot = normalizeConfigSnapshot(snapshot)
	if err := tx.Commit(); err != nil {
		return ConfigSnapshot{}, err
	}
	return snapshot, nil
}

// normalizeConfigSnapshot makes exports self-contained even for databases
// created by older releases that allowed soft-deleted entities to remain in
// active JSON references. Runtime history stays untouched; only the portable
// configuration view is repaired.
func normalizeConfigSnapshot(snapshot ConfigSnapshot) ConfigSnapshot {
	proxyIDs := make(map[int64]struct{}, len(snapshot.Proxies))
	for _, item := range snapshot.Proxies {
		proxyIDs[item.ID] = struct{}{}
	}
	monitorIDs := make(map[int64]struct{}, len(snapshot.Monitors))
	for _, item := range snapshot.Monitors {
		monitorIDs[item.ID] = struct{}{}
	}
	channelIDs := make(map[int64]struct{}, len(snapshot.Channels))
	for _, item := range snapshot.Channels {
		channelIDs[item.ID] = struct{}{}
	}
	templateIDs := make(map[int64]struct{}, len(snapshot.Templates))
	for _, item := range snapshot.Templates {
		templateIDs[item.ID] = struct{}{}
	}

	for index := range snapshot.Monitors {
		item := &snapshot.Monitors[index]
		if item.ProxyID != nil {
			if _, exists := proxyIDs[*item.ProxyID]; !exists {
				item.ProxyID = nil
				item.Enabled = false
			}
		}
		item.FailureNotifyChannelIDs = existingSnapshotIDs(item.FailureNotifyChannelIDs, channelIDs)
		if len(item.FailureNotifyChannelIDs) == 0 {
			item.FailureAlertAfter = 0
			item.FailureAlertActive = false
		}
	}

	rules := make([]model.Rule, 0, len(snapshot.Rules))
	for _, item := range snapshot.Rules {
		if _, exists := monitorIDs[item.MonitorID]; !exists {
			continue
		}
		item.NotifyChannelIDs = existingSnapshotIDs(item.NotifyChannelIDs, channelIDs)
		if len(item.NotifyChannelIDs) == 0 {
			continue
		}
		if item.TemplateID != nil {
			if _, exists := templateIDs[*item.TemplateID]; !exists {
				item.TemplateID = nil
			}
		}
		rules = append(rules, item)
	}
	snapshot.Rules = rules
	return snapshot
}

func existingSnapshotIDs(ids []int64, available map[int64]struct{}) []int64 {
	result := make([]int64, 0, len(ids))
	for _, id := range ids {
		if _, exists := available[id]; exists {
			result = append(result, id)
		}
	}
	return result
}

func scanSnapshotRows[T any](rows *sql.Rows, scan func(scanner) (T, error)) ([]T, error) {
	defer rows.Close()
	items := make([]T, 0)
	for rows.Next() {
		item, err := scan(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
