package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/watchbell/watchbell/internal/model"
)

// ConfigImportError describes an expected merge conflict that should be
// presented to an API client as a validation error, not an internal failure.
type ConfigImportError struct {
	Field   string
	Message string
}

func (e *ConfigImportError) Error() string { return e.Message }

// ImportConfigMerge applies a complete backup in one transaction. Existing
// active entities are matched by their documented natural key and updated in
// place, preserving IDs and all runtime/history rows. Missing entities are
// created. Nothing is deleted.
func (s *Store) ImportConfigMerge(ctx context.Context, backup model.ConfigBackup) (model.ConfigImportReport, error) {
	return s.importConfigMerge(ctx, backup, nil)
}

// ImportConfigMergeAudited commits the import and its audit record atomically.
// A successful API import therefore cannot become an untraceable mutation.
func (s *Store) ImportConfigMergeAudited(ctx context.Context, backup model.ConfigBackup, actor string) (model.ConfigImportReport, error) {
	return s.importConfigMerge(ctx, backup, &actor)
}

func (s *Store) importConfigMerge(ctx context.Context, backup model.ConfigBackup, auditActor *string) (model.ConfigImportReport, error) {
	report := newConfigImportReport()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return report, err
	}
	defer tx.Rollback()

	for index, item := range backup.Channels {
		id, currentConfig, found, err := findMergeChannel(ctx, tx, item.Name, item.Type)
		if err != nil {
			return report, importLookupError(fmt.Sprintf("backup.channels.%d", index), "通知渠道", err)
		}
		config := item.Config
		if len(item.RedactedSecrets) > 0 {
			if !found {
				return report, &ConfigImportError{
					Field:   fmt.Sprintf("backup.channels.%d.redactedSecrets", index),
					Message: fmt.Sprintf("通知渠道 %q 缺少已脱敏的密钥；全新恢复请使用 includeSecrets=true 重新导出。", item.Name),
				}
			}
			config, err = preserveRedactedSecrets(currentConfig, config, item.RedactedSecrets)
			if err != nil {
				return report, &ConfigImportError{Field: fmt.Sprintf("backup.channels.%d.redactedSecrets", index), Message: fmt.Sprintf("通知渠道 %q：%v", item.Name, err)}
			}
			report.Warnings = append(report.Warnings, fmt.Sprintf("通知渠道 %q 保留了目标实例中已有的密钥。", item.Name))
		}

		if found {
			_, err = tx.ExecContext(ctx, `UPDATE notify_channels SET name = ?, type = ?, enabled = ?, config_json = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
				strings.TrimSpace(item.Name), item.Type, boolInt(item.Enabled), string(config), nowString(), id)
			report.Updated.Channels++
		} else {
			var result sql.Result
			result, err = tx.ExecContext(ctx, `INSERT INTO notify_channels (name, type, enabled, config_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
				strings.TrimSpace(item.Name), item.Type, boolInt(item.Enabled), string(config), nowString(), nowString())
			if err == nil {
				id, err = result.LastInsertId()
			}
			report.Created.Channels++
		}
		if err != nil {
			return report, fmt.Errorf("import channel %q: %w", item.Name, err)
		}
		report.IDMap.Channels[sourceIDKey(item.ID)] = id
	}

	backupHasDefaultTemplate := false
	for _, item := range backup.Templates {
		backupHasDefaultTemplate = backupHasDefaultTemplate || item.IsDefault
	}
	for index, item := range backup.Templates {
		id, found, err := findMergeTemplate(ctx, tx, item.Name, item.IsDefault, !backupHasDefaultTemplate)
		if err != nil {
			return report, importLookupError(fmt.Sprintf("backup.templates.%d", index), "通知模板", err)
		}
		// A default template is matched by its role rather than its name. Before
		// renaming that target, reject a collision with an existing custom
		// template; otherwise import would succeed but the next export would be
		// ambiguous and unusable.
		if item.IsDefault && found {
			conflict, err := templateNameOwnedByAnotherActiveRow(ctx, tx, item.Name, id)
			if err != nil {
				return report, fmt.Errorf("check default template name %q: %w", item.Name, err)
			}
			if conflict {
				return report, &ConfigImportError{
					Field:   fmt.Sprintf("backup.templates.%d.name", index),
					Message: fmt.Sprintf("默认通知模板 %q 与目标实例中的自定义模板同名；请先重命名其中一个模板。", item.Name),
				}
			}
		}
		if found {
			_, err = tx.ExecContext(ctx, `UPDATE notification_templates SET name = ?, subject_template = ?, body_template = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
				strings.TrimSpace(item.Name), item.SubjectTemplate, item.BodyTemplate, nowString(), id)
			report.Updated.Templates++
		} else {
			var result sql.Result
			result, err = tx.ExecContext(ctx, `INSERT INTO notification_templates (name, subject_template, body_template, is_default, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
				strings.TrimSpace(item.Name), item.SubjectTemplate, item.BodyTemplate, boolInt(item.IsDefault), nowString(), nowString())
			if err == nil {
				id, err = result.LastInsertId()
			}
			report.Created.Templates++
		}
		if err != nil {
			return report, fmt.Errorf("import template %q: %w", item.Name, err)
		}
		report.IDMap.Templates[sourceIDKey(item.ID)] = id
	}

	for index, item := range backup.Monitors {
		id, currentConfig, found, err := findMergeMonitor(ctx, tx, item.Name, item.Type)
		if err != nil {
			return report, importLookupError(fmt.Sprintf("backup.monitors.%d", index), "监控", err)
		}
		config := item.Config
		if len(item.RedactedSecrets) > 0 {
			if !found {
				return report, &ConfigImportError{
					Field:   fmt.Sprintf("backup.monitors.%d.redactedSecrets", index),
					Message: fmt.Sprintf("监控 %q 缺少已脱敏的密钥；全新恢复请使用 includeSecrets=true 重新导出。", item.Name),
				}
			}
			config, err = preserveRedactedSecrets(currentConfig, config, item.RedactedSecrets)
			if err != nil {
				return report, &ConfigImportError{Field: fmt.Sprintf("backup.monitors.%d.redactedSecrets", index), Message: fmt.Sprintf("监控 %q：%v", item.Name, err)}
			}
			report.Warnings = append(report.Warnings, fmt.Sprintf("监控 %q 保留了目标实例中已有的密钥。", item.Name))
		}
		failureChannelIDs := make([]int64, 0, len(item.FailureNotifyChannelIDs))
		for channelIndex, sourceChannelID := range item.FailureNotifyChannelIDs {
			channelID, exists := report.IDMap.Channels[sourceIDKey(sourceChannelID)]
			if !exists {
				return report, &ConfigImportError{
					Field:   fmt.Sprintf("backup.monitors.%d.failureNotifyChannelIds.%d", index, channelIndex),
					Message: fmt.Sprintf("监控 %q 引用了未导入的通知渠道 ID %d。", item.Name, sourceChannelID),
				}
			}
			failureChannelIDs = append(failureChannelIDs, channelID)
		}
		failureChannelJSON, err := json.Marshal(failureChannelIDs)
		if err != nil {
			return report, err
		}

		if found {
			if jsonEquivalent(currentConfig, config) {
				_, err = tx.ExecContext(ctx, `UPDATE monitors SET name = ?, type = ?, enabled = ?, interval_seconds = ?, config_json = ?, failure_alert_after = ?, failure_notify_channel_ids_json = ?, failure_alert_active = CASE WHEN ? = 0 THEN 0 ELSE failure_alert_active END, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
					strings.TrimSpace(item.Name), item.Type, boolInt(item.Enabled), item.IntervalSeconds, string(config), item.FailureAlertAfter, string(failureChannelJSON), item.FailureAlertAfter, nowString(), id)
			} else {
				// A different checker configuration invalidates only ephemeral
				// state. The monitor ID and all history rows stay intact.
				_, err = tx.ExecContext(ctx, `UPDATE monitors SET name = ?, type = ?, enabled = ?, interval_seconds = ?, config_json = ?, state_json = '{}', last_checked_at = NULL, last_status = '', last_message = '', last_error = '', consecutive_failures = 0, failure_alert_after = ?, failure_notify_channel_ids_json = ?, failure_alert_active = 0, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
					strings.TrimSpace(item.Name), item.Type, boolInt(item.Enabled), item.IntervalSeconds, string(config), item.FailureAlertAfter, string(failureChannelJSON), nowString(), id)
				report.Warnings = append(report.Warnings, fmt.Sprintf("监控 %q 的配置发生变化，已重置检查状态；历史记录保持不变。", item.Name))
			}
			report.Updated.Monitors++
		} else {
			var result sql.Result
			result, err = tx.ExecContext(ctx, `INSERT INTO monitors (name, type, enabled, interval_seconds, config_json, state_json, failure_alert_after, failure_notify_channel_ids_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, '{}', ?, ?, ?, ?)`,
				strings.TrimSpace(item.Name), item.Type, boolInt(item.Enabled), item.IntervalSeconds, string(config), item.FailureAlertAfter, string(failureChannelJSON), nowString(), nowString())
			if err == nil {
				id, err = result.LastInsertId()
			}
			report.Created.Monitors++
		}
		if err != nil {
			return report, fmt.Errorf("import monitor %q: %w", item.Name, err)
		}
		report.IDMap.Monitors[sourceIDKey(item.ID)] = id
	}

	for index, item := range backup.Rules {
		monitorID, ok := report.IDMap.Monitors[sourceIDKey(item.MonitorID)]
		if !ok {
			return report, &ConfigImportError{Field: fmt.Sprintf("backup.rules.%d.monitorId", index), Message: fmt.Sprintf("规则 %q 引用了未导入的监控 ID %d。", item.Name, item.MonitorID)}
		}
		channelIDs := make([]int64, 0, len(item.NotifyChannelIDs))
		for channelIndex, sourceChannelID := range item.NotifyChannelIDs {
			channelID, exists := report.IDMap.Channels[sourceIDKey(sourceChannelID)]
			if !exists {
				return report, &ConfigImportError{Field: fmt.Sprintf("backup.rules.%d.notifyChannelIds.%d", index, channelIndex), Message: fmt.Sprintf("规则 %q 引用了未导入的通知渠道 ID %d。", item.Name, sourceChannelID)}
			}
			channelIDs = append(channelIDs, channelID)
		}
		var templateID *int64
		if item.TemplateID != nil {
			mapped, exists := report.IDMap.Templates[sourceIDKey(*item.TemplateID)]
			if !exists {
				return report, &ConfigImportError{Field: fmt.Sprintf("backup.rules.%d.templateId", index), Message: fmt.Sprintf("规则 %q 引用了未导入的通知模板 ID %d。", item.Name, *item.TemplateID)}
			}
			templateID = &mapped
		}
		channelJSON, err := json.Marshal(channelIDs)
		if err != nil {
			return report, err
		}
		quietHoursJSON, err := json.Marshal(item.QuietHours)
		if err != nil {
			return report, err
		}
		id, found, err := findMergeRule(ctx, tx, monitorID, item.Name)
		if err != nil {
			return report, importLookupError(fmt.Sprintf("backup.rules.%d", index), "规则", err)
		}
		if found {
			// last_fired_at remains intact so cooldown semantics survive a merge.
			_, err = tx.ExecContext(ctx, `UPDATE rules SET monitor_id = ?, name = ?, enabled = ?, condition_json = ?, notify_channel_ids_json = ?, template_id = ?, cooldown_seconds = ?, quiet_hours_json = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
				monitorID, strings.TrimSpace(item.Name), boolInt(item.Enabled), string(item.Condition), string(channelJSON), templateID, item.CooldownSeconds, string(quietHoursJSON), nowString(), id)
			report.Updated.Rules++
		} else {
			var result sql.Result
			result, err = tx.ExecContext(ctx, `INSERT INTO rules (monitor_id, name, enabled, condition_json, notify_channel_ids_json, template_id, cooldown_seconds, quiet_hours_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				monitorID, strings.TrimSpace(item.Name), boolInt(item.Enabled), string(item.Condition), string(channelJSON), templateID, item.CooldownSeconds, string(quietHoursJSON), nowString(), nowString())
			if err == nil {
				id, err = result.LastInsertId()
			}
			report.Created.Rules++
		}
		if err != nil {
			return report, fmt.Errorf("import rule %q: %w", item.Name, err)
		}
		report.IDMap.Rules[sourceIDKey(item.ID)] = id
	}

	var activeDefaults int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM notification_templates WHERE is_default = 1 AND deleted_at IS NULL`).Scan(&activeDefaults); err != nil {
		return report, err
	}
	if activeDefaults != 1 {
		return report, &ConfigImportError{Field: "backup.templates", Message: fmt.Sprintf("导入后必须恰好有一个默认通知模板，当前为 %d 个。", activeDefaults)}
	}
	if auditActor != nil {
		changes, err := json.Marshal(report)
		if err != nil {
			return report, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO audit_logs (actor, action, entity_type, entity_id, summary, changes_json, created_at) VALUES (?, 'import', 'config', NULL, '合并配置备份', ?, ?)`, strings.TrimSpace(*auditActor), string(changes), nowString()); err != nil {
			return report, fmt.Errorf("record config import audit: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return report, err
	}
	return report, nil
}

func newConfigImportReport() model.ConfigImportReport {
	return model.ConfigImportReport{
		Version: model.ConfigBackupVersion,
		Mode:    "merge",
		IDMap: model.ConfigImportIDMap{
			Monitors:  map[string]int64{},
			Rules:     map[string]int64{},
			Channels:  map[string]int64{},
			Templates: map[string]int64{},
		},
		Warnings: []string{},
	}
}

var errAmbiguousMergeTarget = errors.New("存在多个匹配项，无法确定 merge 目标")

func importLookupError(field, entity string, err error) error {
	if errors.Is(err, errAmbiguousMergeTarget) {
		return &ConfigImportError{Field: field, Message: entity + err.Error() + "。请先为重复项设置不同名称。"}
	}
	return fmt.Errorf("lookup %s: %w", entity, err)
}

func findMergeMonitor(ctx context.Context, tx *sql.Tx, name, monitorType string) (int64, json.RawMessage, bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, config_json FROM monitors WHERE name = ? AND type = ? AND deleted_at IS NULL ORDER BY id LIMIT 2`, strings.TrimSpace(name), monitorType)
	if err != nil {
		return 0, nil, false, err
	}
	defer rows.Close()
	var id int64
	var config string
	count := 0
	for rows.Next() {
		if err := rows.Scan(&id, &config); err != nil {
			return 0, nil, false, err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, nil, false, err
	}
	if count > 1 {
		return 0, nil, false, errAmbiguousMergeTarget
	}
	return id, json.RawMessage(config), count == 1, nil
}

func findMergeChannel(ctx context.Context, tx *sql.Tx, name, channelType string) (int64, json.RawMessage, bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, config_json FROM notify_channels WHERE name = ? AND type = ? AND deleted_at IS NULL ORDER BY id LIMIT 2`, strings.TrimSpace(name), channelType)
	if err != nil {
		return 0, nil, false, err
	}
	defer rows.Close()
	var id int64
	var config string
	count := 0
	for rows.Next() {
		if err := rows.Scan(&id, &config); err != nil {
			return 0, nil, false, err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, nil, false, err
	}
	if count > 1 {
		return 0, nil, false, errAmbiguousMergeTarget
	}
	return id, json.RawMessage(config), count == 1, nil
}

func findMergeTemplate(ctx context.Context, tx *sql.Tx, name string, isDefault, legacyBackup bool) (int64, bool, error) {
	query := `SELECT id FROM notification_templates WHERE name = ? AND deleted_at IS NULL ORDER BY id LIMIT 2`
	args := []any{strings.TrimSpace(name)}
	if isDefault {
		query = `SELECT id FROM notification_templates WHERE is_default = 1 AND deleted_at IS NULL ORDER BY id LIMIT 2`
		args = nil
	} else if !legacyBackup {
		query = `SELECT id FROM notification_templates WHERE name = ? AND is_default = 0 AND deleted_at IS NULL ORDER BY id LIMIT 2`
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, false, err
	}
	defer rows.Close()
	var id int64
	count := 0
	for rows.Next() {
		if err := rows.Scan(&id); err != nil {
			return 0, false, err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, false, err
	}
	if count > 1 {
		return 0, false, errAmbiguousMergeTarget
	}
	return id, count == 1, nil
}

func findMergeRule(ctx context.Context, tx *sql.Tx, monitorID int64, name string) (int64, bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM rules WHERE monitor_id = ? AND name = ? AND deleted_at IS NULL ORDER BY id LIMIT 2`, monitorID, strings.TrimSpace(name))
	if err != nil {
		return 0, false, err
	}
	defer rows.Close()
	var id int64
	count := 0
	for rows.Next() {
		if err := rows.Scan(&id); err != nil {
			return 0, false, err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, false, err
	}
	if count > 1 {
		return 0, false, errAmbiguousMergeTarget
	}
	return id, count == 1, nil
}

func templateNameOwnedByAnotherActiveRow(ctx context.Context, tx *sql.Tx, name string, excludeID int64) (bool, error) {
	var exists bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM notification_templates
		WHERE name = ? AND id <> ? AND deleted_at IS NULL
	)`, strings.TrimSpace(name), excludeID).Scan(&exists)
	return exists, err
}

func preserveRedactedSecrets(currentRaw, incomingRaw json.RawMessage, keys []string) (json.RawMessage, error) {
	var current, incoming map[string]any
	if err := json.Unmarshal(currentRaw, &current); err != nil || current == nil {
		return nil, errors.New("目标配置不是有效的 JSON 对象")
	}
	if err := json.Unmarshal(incomingRaw, &incoming); err != nil || incoming == nil {
		return nil, errors.New("导入配置不是有效的 JSON 对象")
	}
	for _, key := range keys {
		value, ok := current[key]
		if !ok || !hasImportSecretValue(value) {
			return nil, fmt.Errorf("目标配置中没有可保留的密钥 %q；请导入包含密钥的备份", key)
		}
		incoming[key] = value
	}
	result, err := json.Marshal(incoming)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func hasImportSecretValue(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(typed) != ""
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}

func sourceIDKey(id int64) string { return strconv.FormatInt(id, 10) }
