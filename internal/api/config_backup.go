package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/watchbell/watchbell/internal/model"
	"github.com/watchbell/watchbell/internal/rule"
	"github.com/watchbell/watchbell/internal/store"
)

func (s *Server) exportConfig(w http.ResponseWriter, r *http.Request) {
	includeSecrets, err := configExportIncludesSecrets(r)
	if err != nil {
		writeError(w, r, &problemError{
			Status:  http.StatusBadRequest,
			Code:    "invalid_query",
			Message: err.Error(),
			Fields:  map[string]string{"includeSecrets": "只能使用 true 或 false。"},
		})
		return
	}
	backup, err := s.buildConfigBackup(r, includeSecrets)
	if err != nil {
		writeError(w, r, err)
		return
	}
	// Record the export before any response bytes (and, potentially, secrets)
	// leave the process. Detach from a disconnected client so the audit entry
	// is not silently cancelled halfway through.
	auditCtx, cancelAudit := context.WithTimeout(context.WithoutCancel(r.Context()), 3*time.Second)
	defer cancelAudit()
	if err := s.store.CreateAuditLog(auditCtx, s.actor(r), "export", "config", nil, "导出配置备份", map[string]any{"includeSecrets": includeSecrets}); err != nil {
		writeError(w, r, fmt.Errorf("record config export audit: %w", err))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="watchbell-config-%s.json"`, backup.ExportedAt.Format("20060102T150405Z")))
	writeJSON(w, http.StatusOK, backup)
}

func (s *Server) importConfig(w http.ResponseWriter, r *http.Request) {
	var request model.ConfigImportRequest
	if !decode(w, r, &request) {
		return
	}
	if request.Mode == "" {
		request.Mode = "merge"
	}
	if err := s.validateConfigImport(r.Context(), request); err != nil {
		writeError(w, r, err)
		return
	}
	report, err := s.store.ImportConfigMergeAudited(r.Context(), request.Backup, s.actor(r))
	if err != nil {
		var importError *store.ConfigImportError
		if errors.As(err, &importError) {
			err = validationProblem("配置导入未执行，数据库未发生任何变化。", map[string]string{importError.Field: importError.Message})
		}
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) buildConfigBackup(r *http.Request, includeSecrets bool) (model.ConfigBackup, error) {
	ctx := r.Context()
	snapshot, err := s.store.ReadConfigSnapshot(ctx)
	if err != nil {
		return model.ConfigBackup{}, err
	}
	if err := validateSnapshotNaturalKeys(snapshot); err != nil {
		return model.ConfigBackup{}, err
	}
	proxies, monitors, rules, channels, templates := snapshot.Proxies, snapshot.Monitors, snapshot.Rules, snapshot.Channels, snapshot.Templates

	backup := model.ConfigBackup{
		Version:         model.ConfigBackupVersion,
		ExportedAt:      time.Now().UTC(),
		IncludesSecrets: includeSecrets,
		Proxies:         make([]model.ConfigBackupProxy, 0, len(proxies)),
		Monitors:        make([]model.ConfigBackupMonitor, 0, len(monitors)),
		Rules:           make([]model.ConfigBackupRule, 0, len(rules)),
		Channels:        make([]model.ConfigBackupChannel, 0, len(channels)),
		Templates:       make([]model.ConfigBackupTemplate, 0, len(templates)),
	}
	for _, item := range proxies {
		password := item.Password
		var redacted []string
		if !includeSecrets && password != "" {
			password = ""
			redacted = []string{"password"}
		}
		backup.Proxies = append(backup.Proxies, model.ConfigBackupProxy{
			ID: item.ID, Name: item.Name, Type: item.Type, Host: item.Host, Port: item.Port,
			Username: item.Username, Password: password, RedactedSecrets: redacted,
		})
	}
	for _, item := range monitors {
		config := item.Config
		var redacted []string
		if !includeSecrets {
			config, redacted = redactConfig(config, monitorSecretKeys(item.Type, s.scheduler.Plugins()))
		}
		backup.Monitors = append(backup.Monitors, model.ConfigBackupMonitor{
			ID: item.ID, Name: item.Name, Type: item.Type, ProxyID: item.ProxyID, Enabled: item.Enabled,
			IntervalSeconds: item.IntervalSeconds, Config: config,
			FailureAlertAfter:       item.FailureAlertAfter,
			FailureNotifyChannelIDs: append([]int64{}, item.FailureNotifyChannelIDs...),
			RedactedSecrets:         redacted,
		})
	}
	for _, item := range rules {
		channelIDs := append([]int64(nil), item.NotifyChannelIDs...)
		if channelIDs == nil {
			channelIDs = []int64{}
		}
		backup.Rules = append(backup.Rules, model.ConfigBackupRule{
			ID: item.ID, MonitorID: item.MonitorID, Name: item.Name, Enabled: item.Enabled,
			Condition: item.Condition, NotifyChannelIDs: channelIDs, TemplateID: item.TemplateID,
			CooldownSeconds: item.CooldownSeconds, QuietHours: item.QuietHours,
		})
	}
	for _, item := range channels {
		config := item.Config
		var redacted []string
		if !includeSecrets {
			config, redacted = redactConfig(config, channelSecretKeys(item.Type))
		}
		backup.Channels = append(backup.Channels, model.ConfigBackupChannel{
			ID: item.ID, Name: item.Name, Type: item.Type, Enabled: item.Enabled,
			Config: config, RedactedSecrets: redacted,
		})
	}
	for _, item := range templates {
		backup.Templates = append(backup.Templates, model.ConfigBackupTemplate{
			ID: item.ID, Name: item.Name, SubjectTemplate: item.SubjectTemplate, BodyTemplate: item.BodyTemplate, IsDefault: item.IsDefault,
		})
	}
	return backup, nil
}

func validateSnapshotNaturalKeys(snapshot store.ConfigSnapshot) error {
	fields := map[string]string{}
	check := func(collection string, id int64, key, description string, seen map[string]int64) {
		if previousID, exists := seen[key]; exists {
			fields[fmt.Sprintf("%s.%d.name", collection, id)] = fmt.Sprintf("%s；与 ID %d 冲突，请先重命名。", description, previousID)
			return
		}
		seen[key] = id
	}
	monitorKeys := map[string]int64{}
	proxyKeys := map[string]int64{}
	for _, item := range snapshot.Proxies {
		check("proxies", item.ID, strings.TrimSpace(item.Name), "同名代理无法可靠合并", proxyKeys)
	}
	for _, item := range snapshot.Monitors {
		check("monitors", item.ID, item.Type+"\x00"+strings.TrimSpace(item.Name), "同名、同类型监控无法可靠合并", monitorKeys)
	}
	channelKeys := map[string]int64{}
	for _, item := range snapshot.Channels {
		check("channels", item.ID, item.Type+"\x00"+strings.TrimSpace(item.Name), "同名、同类型渠道无法可靠合并", channelKeys)
	}
	templateKeys := map[string]int64{}
	for _, item := range snapshot.Templates {
		check("templates", item.ID, strings.TrimSpace(item.Name), "同名模板无法可靠合并", templateKeys)
	}
	ruleKeys := map[string]int64{}
	for _, item := range snapshot.Rules {
		check("rules", item.ID, strconv.FormatInt(item.MonitorID, 10)+"\x00"+strings.TrimSpace(item.Name), "同一监控下的同名规则无法可靠合并", ruleKeys)
	}
	if len(fields) > 0 {
		return &problemError{Status: http.StatusConflict, Code: "ambiguous_config", Message: "当前配置存在重复自然键，无法生成可安全回放的备份。", Fields: fields}
	}
	return nil
}

func configExportIncludesSecrets(r *http.Request) (bool, error) {
	values, exists := r.URL.Query()["includeSecrets"]
	if !exists {
		return false, nil
	}
	if len(values) != 1 || (values[0] != "true" && values[0] != "false") {
		return false, errors.New("includeSecrets 查询参数无效")
	}
	return strconv.ParseBool(values[0])
}

func (s *Server) validateConfigImport(ctx context.Context, request model.ConfigImportRequest) error {
	fields := map[string]string{}
	if request.Mode != "merge" {
		fields["mode"] = "当前仅支持 merge；导入不会删除现有配置或历史。"
	}
	backup := request.Backup
	if backup.Version != model.ConfigBackupVersion {
		fields["backup.version"] = fmt.Sprintf("不支持备份版本 %d；当前仅支持版本 %d。", backup.Version, model.ConfigBackupVersion)
	}
	if backup.ExportedAt.IsZero() {
		fields["backup.exportedAt"] = "缺少有效的导出时间。"
	}
	validateBackupCollectionSize("backup.monitors", len(backup.Monitors), fields)
	validateBackupCollectionSize("backup.proxies", len(backup.Proxies), fields)
	validateBackupCollectionSize("backup.rules", len(backup.Rules), fields)
	validateBackupCollectionSize("backup.channels", len(backup.Channels), fields)
	validateBackupCollectionSize("backup.templates", len(backup.Templates), fields)

	proxyIDs := map[int64]struct{}{}
	proxyKeys := map[string]struct{}{}
	for index, item := range backup.Proxies {
		prefix := fmt.Sprintf("backup.proxies.%d", index)
		validateSourceID(prefix+".id", item.ID, proxyIDs, fields)
		validateNaturalKey(prefix+".name", strings.TrimSpace(item.Name), proxyKeys, "备份中存在同名代理。", fields)
		password := validateBackupProxySecret(prefix, item, backup.IncludesSecrets, fields)
		input := model.ProxyProfileInput{
			Name: item.Name, Type: item.Type, Host: item.Host, Port: item.Port,
			Username: item.Username, Password: password,
		}
		normalizeProxyProfileInput(&input)
		if err := validateProxyProfileInput(input); err != nil {
			mergeBackupProblem(prefix, err, fields)
		}
	}

	monitorTypes := map[int64]string{}
	monitorIDs := map[int64]struct{}{}
	monitorKeys := map[string]struct{}{}
	for index, item := range backup.Monitors {
		prefix := fmt.Sprintf("backup.monitors.%d", index)
		validateSourceID(prefix+".id", item.ID, monitorIDs, fields)
		key := item.Type + "\x00" + strings.TrimSpace(item.Name)
		validateNaturalKey(prefix+".name", key, monitorKeys, "备份中存在相同名称和类型的监控。", fields)
		monitorTypes[item.ID] = item.Type
		secretKeys := monitorSecretKeys(item.Type, s.scheduler.Plugins())
		config := validateBackupSecrets(prefix, item.Config, item.RedactedSecrets, secretKeys, nil, backup.IncludesSecrets, fields)
		if err := s.validateMonitorInputWithChannelLookup(ctx, model.MonitorInput{
			Name: item.Name, Type: item.Type, ProxyID: item.ProxyID, Enabled: item.Enabled,
			IntervalSeconds: item.IntervalSeconds, Config: config,
			FailureAlertAfter: item.FailureAlertAfter, FailureNotifyChannelIDs: item.FailureNotifyChannelIDs,
		}, false); err != nil {
			mergeBackupProblem(prefix, err, fields)
		}
		if item.ProxyID != nil {
			if _, exists := proxyIDs[*item.ProxyID]; !exists {
				fields[prefix+".proxyId"] = fmt.Sprintf("备份中不存在代理 ID %d。", *item.ProxyID)
			}
		}
	}

	channelIDs := map[int64]struct{}{}
	channelKeys := map[string]struct{}{}
	for index, item := range backup.Channels {
		prefix := fmt.Sprintf("backup.channels.%d", index)
		validateSourceID(prefix+".id", item.ID, channelIDs, fields)
		key := item.Type + "\x00" + strings.TrimSpace(item.Name)
		validateNaturalKey(prefix+".name", key, channelKeys, "备份中存在相同名称和类型的通知渠道。", fields)
		config := validateBackupSecrets(prefix, item.Config, item.RedactedSecrets, channelSecretKeys(item.Type), backupSecretValidationValues(item.Type), backup.IncludesSecrets, fields)
		if err := validateChannelInput(model.NotifyChannelInput{Name: item.Name, Type: item.Type, Enabled: item.Enabled, Config: config}); err != nil {
			mergeBackupProblem(prefix, err, fields)
		}
	}
	for monitorIndex, item := range backup.Monitors {
		for channelIndex, channelID := range item.FailureNotifyChannelIDs {
			if _, exists := channelIDs[channelID]; !exists {
				fields[fmt.Sprintf("backup.monitors.%d.failureNotifyChannelIds.%d", monitorIndex, channelIndex)] = fmt.Sprintf("备份中不存在通知渠道 ID %d。", channelID)
			}
		}
	}

	templateIDs := map[int64]struct{}{}
	templateKeys := map[string]struct{}{}
	defaultTemplates := 0
	for index, item := range backup.Templates {
		prefix := fmt.Sprintf("backup.templates.%d", index)
		validateSourceID(prefix+".id", item.ID, templateIDs, fields)
		validateNaturalKey(prefix+".name", strings.TrimSpace(item.Name), templateKeys, "备份中存在同名通知模板。", fields)
		if item.IsDefault {
			defaultTemplates++
			if defaultTemplates > 1 {
				fields[prefix+".isDefault"] = "备份中只能有一个默认通知模板。"
			}
		}
		if err := validateTemplateInput(model.NotificationTemplateInput{Name: item.Name, SubjectTemplate: item.SubjectTemplate, BodyTemplate: item.BodyTemplate}); err != nil {
			mergeBackupProblem(prefix, err, fields)
		}
	}

	ruleIDs := map[int64]struct{}{}
	ruleKeys := map[string]struct{}{}
	for index, item := range backup.Rules {
		prefix := fmt.Sprintf("backup.rules.%d", index)
		validateSourceID(prefix+".id", item.ID, ruleIDs, fields)
		if strings.TrimSpace(item.Name) == "" {
			fields[prefix+".name"] = "请输入规则名称。"
		}
		validateNaturalKey(prefix+".name", fmt.Sprintf("%d\x00%s", item.MonitorID, strings.TrimSpace(item.Name)), ruleKeys, "备份中同一监控存在同名规则。", fields)
		monitorType, monitorExists := monitorTypes[item.MonitorID]
		if !monitorExists {
			fields[prefix+".monitorId"] = fmt.Sprintf("备份中不存在监控 ID %d。", item.MonitorID)
		}
		if item.CooldownSeconds < 0 || item.CooldownSeconds > 31_536_000 {
			fields[prefix+".cooldownSeconds"] = "冷却时间必须在 0 到 365 天之间。"
		}
		if field, err := rule.ValidateQuietHours(item.QuietHours); err != nil {
			fields[prefix+"."+field] = err.Error()
		}
		if len(item.NotifyChannelIDs) == 0 {
			fields[prefix+".notifyChannelIds"] = "请至少选择一个通知渠道。"
		}
		seenChannels := map[int64]struct{}{}
		for channelIndex, channelID := range item.NotifyChannelIDs {
			field := fmt.Sprintf("%s.notifyChannelIds.%d", prefix, channelIndex)
			if _, exists := channelIDs[channelID]; !exists {
				fields[field] = fmt.Sprintf("备份中不存在通知渠道 ID %d。", channelID)
			}
			if _, duplicate := seenChannels[channelID]; duplicate {
				fields[field] = "通知渠道 ID 不能重复。"
			}
			seenChannels[channelID] = struct{}{}
		}
		if item.TemplateID != nil {
			if _, exists := templateIDs[*item.TemplateID]; !exists {
				fields[prefix+".templateId"] = fmt.Sprintf("备份中不存在通知模板 ID %d。", *item.TemplateID)
			}
		}
		if err := rule.Validate(item.Condition); err != nil {
			fields[prefix+".condition"] = err.Error()
		} else if monitorExists {
			conditionFields := map[string]string{}
			validateConditionFields(item.Condition, monitorType, s.scheduler.Plugins(), conditionFields)
			for field, message := range conditionFields {
				fields[prefix+"."+field] = message
			}
		}
	}

	if len(fields) > 0 {
		return validationProblem("配置备份校验失败，未执行导入。", fields)
	}
	return nil
}

func validateBackupCollectionSize(field string, count int, fields map[string]string) {
	if count > 5000 {
		fields[field] = "单类配置最多允许 5000 项。"
	}
}

func validateSourceID(field string, id int64, seen map[int64]struct{}, fields map[string]string) {
	if id <= 0 {
		fields[field] = "源 ID 必须是正整数。"
		return
	}
	if _, exists := seen[id]; exists {
		fields[field] = "源 ID 不能重复。"
	}
	seen[id] = struct{}{}
}

func validateNaturalKey(field, key string, seen map[string]struct{}, message string, fields map[string]string) {
	if _, exists := seen[key]; exists {
		fields[field] = message
	}
	seen[key] = struct{}{}
}

func validateBackupSecrets(prefix string, raw json.RawMessage, redacted, allowedSecrets []string, validationValues map[string]any, includesSecrets bool, fields map[string]string) json.RawMessage {
	var config map[string]any
	if err := json.Unmarshal(raw, &config); err != nil || config == nil {
		// Entity validation will produce the canonical config error.
		return raw
	}
	allowed := make(map[string]struct{}, len(allowedSecrets))
	for _, key := range allowedSecrets {
		allowed[key] = struct{}{}
	}
	seen := map[string]struct{}{}
	for index, key := range redacted {
		field := fmt.Sprintf("%s.redactedSecrets.%d", prefix, index)
		if includesSecrets {
			fields[field] = "包含密钥的备份不能同时声明已脱敏字段。"
		}
		if _, ok := allowed[key]; !ok {
			fields[field] = fmt.Sprintf("%q 不是该配置类型的密钥字段。", key)
			continue
		}
		if _, duplicate := seen[key]; duplicate {
			fields[field] = "已脱敏字段不能重复。"
		}
		seen[key] = struct{}{}
		if _, present := config[key]; present {
			fields[field] = "已脱敏字段不能同时出现在 config 中。"
		}
		value := any("__WATCHBELL_REDACTED_FOR_VALIDATION__")
		if configured, ok := validationValues[key]; ok {
			value = configured
		}
		config[key] = value
	}
	if !includesSecrets {
		for _, key := range allowedSecrets {
			if _, declared := seen[key]; declared {
				continue
			}
			if value, present := config[key]; present && hasConfiguredSecretValue(value) {
				fields[prefix+".config."+key] = "includesSecrets=false 时不能包含密钥值。"
			}
		}
	}
	result, err := json.Marshal(config)
	if err != nil {
		return raw
	}
	return result
}

func backupSecretValidationValues(channelType string) map[string]any {
	if channelType != model.ChannelTypeWebhook {
		return nil
	}
	// Webhook secrets are not both scalar strings: headers is an object and
	// URL validation requires a syntactically valid HTTP(S) endpoint. These
	// values exist only in the temporary validation copy and are never stored.
	return map[string]any{
		"url":     "https://example.com/watchbell-validation",
		"headers": map[string]any{"X-WatchBell-Validation": "redacted"},
	}
}

func validateBackupProxySecret(prefix string, item model.ConfigBackupProxy, includesSecrets bool, fields map[string]string) string {
	password := item.Password
	seen := false
	for index, key := range item.RedactedSecrets {
		field := fmt.Sprintf("%s.redactedSecrets.%d", prefix, index)
		if key != "password" {
			fields[field] = fmt.Sprintf("%q 不是代理的密钥字段。", key)
			continue
		}
		if seen {
			fields[field] = "已脱敏字段不能重复。"
		}
		seen = true
		if includesSecrets {
			fields[field] = "包含密钥的备份不能同时声明已脱敏字段。"
		}
		if item.Password != "" {
			fields[field] = "已脱敏密码不能同时出现在 password 字段中。"
		}
		password = "__WATCHBELL_REDACTED_FOR_VALIDATION__"
	}
	if !includesSecrets && item.Password != "" {
		fields[prefix+".password"] = "includesSecrets=false 时不能包含代理密码。"
	}
	return password
}

func mergeBackupProblem(prefix string, err error, fields map[string]string) {
	var problem *problemError
	if !errors.As(err, &problem) {
		fields[prefix] = err.Error()
		return
	}
	for field, message := range problem.Fields {
		fields[prefix+"."+field] = message
	}
}
