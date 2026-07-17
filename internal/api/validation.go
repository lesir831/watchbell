package api

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/mail"
	"net/url"
	"strings"

	"github.com/watchbell/watchbell/internal/eventvars"
	"github.com/watchbell/watchbell/internal/model"
	"github.com/watchbell/watchbell/internal/notifier"
	"github.com/watchbell/watchbell/internal/rule"
)

type problemError struct {
	Status  int
	Code    string
	Message string
	Fields  map[string]string
}

func (e *problemError) Error() string {
	return e.Message
}

func validationProblem(message string, fields map[string]string) error {
	return &problemError{Status: 422, Code: "validation_failed", Message: message, Fields: fields}
}

func (s *Server) validateMonitorInput(ctx context.Context, input model.MonitorInput) error {
	return s.validateMonitorInputWithChannelLookup(ctx, input, true)
}

func (s *Server) validateMonitorNaturalKey(ctx context.Context, input model.MonitorInput, excludeID int64) error {
	items, err := s.store.ListMonitors(ctx)
	if err != nil {
		return err
	}
	name := strings.TrimSpace(input.Name)
	for _, item := range items {
		if item.ID != excludeID && item.Type == input.Type && strings.TrimSpace(item.Name) == name {
			return validationProblem("监控名称与类型不能重复。", map[string]string{"name": "已存在同名、同类型的监控。"})
		}
	}
	return nil
}

func (s *Server) validateMonitorInputWithChannelLookup(ctx context.Context, input model.MonitorInput, lookupChannels bool) error {
	fields := map[string]string{}
	if strings.TrimSpace(input.Name) == "" {
		fields["name"] = "请输入名称。"
	}
	if !s.scheduler.HasPlugin(input.Type) {
		fields["type"] = fmt.Sprintf("不支持监控类型 %q。", input.Type)
	}
	if input.IntervalSeconds < 30 || input.IntervalSeconds > 2_592_000 {
		fields["intervalSeconds"] = "检查间隔必须在 30 秒到 30 天之间。"
	}
	if input.FailureAlertAfter < 0 || input.FailureAlertAfter > 100 {
		fields["failureAlertAfter"] = "故障告警阈值必须在 1 到 100 次之间，或设为 0 关闭。"
	}
	if input.FailureAlertAfter > 0 && len(input.FailureNotifyChannelIDs) == 0 {
		fields["failureNotifyChannelIds"] = "启用故障告警时，请至少选择一个通知渠道。"
	}
	seenChannels := map[int64]struct{}{}
	for index, id := range input.FailureNotifyChannelIDs {
		field := fmt.Sprintf("failureNotifyChannelIds.%d", index)
		if id <= 0 {
			fields[field] = "通知渠道 ID 必须为正整数。"
			continue
		}
		if _, duplicate := seenChannels[id]; duplicate {
			fields[field] = "通知渠道不能重复。"
			continue
		}
		seenChannels[id] = struct{}{}
		if lookupChannels {
			if _, err := s.store.GetNotifyChannel(ctx, id); err != nil {
				fields[field] = fmt.Sprintf("通知渠道 %d 不存在。", id)
			}
		}
	}
	config, err := decodeJSONObject(input.Config)
	if err != nil {
		fields["config"] = err.Error()
	} else {
		for _, plugin := range s.scheduler.Plugins() {
			if plugin.ID != input.Type {
				continue
			}
			for _, field := range plugin.ConfigFields {
				if field.Required && isEmptyConfigValue(config[field.Key]) {
					fields["config."+field.Key] = "请填写" + field.Label + "。"
					continue
				}
				validatePluginConfigFieldType(config, field, fields)
			}
		}
		validateMonitorConfig(input.Type, config, fields)
	}
	if len(fields) > 0 {
		return validationProblem("请修正监控配置中的问题。", fields)
	}
	return nil
}

func validatePluginConfigFieldType(config map[string]any, field model.PluginConfigField, fields map[string]string) {
	value, present := config[field.Key]
	if !present || value == nil || isEmptyConfigValue(value) {
		return
	}
	problemField := "config." + field.Key
	switch field.Type {
	case "string", "secret", "url", "textarea":
		if _, ok := value.(string); !ok {
			fields[problemField] = field.Label + "必须是字符串。"
		}
	case "number":
		number, ok := value.(float64)
		if !ok || math.Trunc(number) != number {
			fields[problemField] = field.Label + "必须是整数。"
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			fields[problemField] = field.Label + "必须是布尔值。"
		}
	case "string-list":
		items, ok := value.([]any)
		if !ok {
			fields[problemField] = field.Label + "必须是字符串数组。"
			return
		}
		for _, item := range items {
			if _, ok := item.(string); !ok {
				fields[problemField] = field.Label + "必须是字符串数组。"
				return
			}
		}
	}
}

func validateMonitorConfig(monitorType string, config map[string]any, fields map[string]string) {
	if monitorType == model.MonitorTypeGitHubRelease {
		repository := strings.Trim(strings.TrimSpace(stringValue(config["repository"])), "/")
		parts := strings.Split(repository, "/")
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(strings.TrimSuffix(parts[1], ".git")) == "" {
			fields["config.repository"] = "仓库必须使用 owner/repository 格式。"
		}
		if raw := stringValue(config["apiUrl"]); raw != "" && !validHTTPURL(raw) {
			fields["config.apiUrl"] = "API URL 必须是有效的 HTTP 或 HTTPS 地址。"
		}
		if value, ok := numberValue(config["maxReleases"]); ok && (value < 1 || value > 100) {
			fields["config.maxReleases"] = "每次检查的 Release 数量必须在 1 到 100 之间。"
		}
	} else {
		if raw := stringValue(config["url"]); raw == "" || !validHTTPURL(raw) {
			fields["config.url"] = "地址必须是有效的 HTTP 或 HTTPS URL。"
		}
	}
	if value, ok := numberValue(config["timeoutSeconds"]); ok && (value < 1 || value > 120) {
		fields["config.timeoutSeconds"] = "超时时间必须在 1 到 120 秒之间。"
	}
}

func (s *Server) validateRuleInput(ctx context.Context, input model.RuleInput) error {
	fields := map[string]string{}
	if strings.TrimSpace(input.Name) == "" {
		fields["name"] = "请输入规则名称。"
	}
	monitor, err := s.store.GetMonitor(ctx, input.MonitorID)
	if err != nil {
		fields["monitorId"] = "请选择一个现有监控。"
	}
	if input.CooldownSeconds < 0 || input.CooldownSeconds > 31_536_000 {
		fields["cooldownSeconds"] = "冷却时间必须在 0 到 365 天之间。"
	}
	if field, err := rule.ValidateQuietHours(input.QuietHours); err != nil {
		fields["quietHours."+field] = err.Error()
	}
	if len(input.NotifyChannelIDs) == 0 {
		fields["notifyChannelIds"] = "请至少选择一个通知渠道。"
	}
	for _, id := range input.NotifyChannelIDs {
		if _, err := s.store.GetNotifyChannel(ctx, id); err != nil {
			fields["notifyChannelIds"] = fmt.Sprintf("通知渠道 %d 不存在。", id)
			break
		}
	}
	if input.TemplateID != nil {
		if _, err := s.store.GetNotificationTemplate(ctx, *input.TemplateID); err != nil {
			fields["templateId"] = "请选择一个现有通知模板。"
		}
	}
	if err := rule.Validate(input.Condition); err != nil {
		fields["condition"] = err.Error()
	} else if monitor.ID > 0 {
		validateConditionFields(input.Condition, monitor.Type, s.scheduler.Plugins(), fields)
	}
	if len(fields) > 0 {
		return validationProblem("请修正规则配置中的问题。", fields)
	}
	return nil
}

func (s *Server) validateRuleNaturalKey(ctx context.Context, input model.RuleInput, excludeID int64) error {
	items, err := s.store.ListRules(ctx)
	if err != nil {
		return err
	}
	name := strings.TrimSpace(input.Name)
	for _, item := range items {
		if item.ID != excludeID && item.MonitorID == input.MonitorID && strings.TrimSpace(item.Name) == name {
			return validationProblem("同一监控下的规则名称不能重复。", map[string]string{"name": "这个监控已有同名规则。"})
		}
	}
	return nil
}

func validateConditionFields(raw json.RawMessage, monitorType string, plugins []model.MonitorPlugin, fields map[string]string) {
	if len(raw) == 0 || string(raw) == "{}" {
		return
	}
	var set rule.ConditionSet
	if json.Unmarshal(raw, &set) != nil {
		return
	}
	allowed := map[string]struct{}{}
	for _, plugin := range plugins {
		if plugin.ID == monitorType {
			for _, variable := range plugin.TemplateVariables {
				allowed[variable] = struct{}{}
			}
		}
	}
	validateConditionNodeFields(set.Conditions, "condition.conditions", monitorType, allowed, fields)
}

func validateConditionNodeFields(conditions []rule.Condition, path, monitorType string, allowed map[string]struct{}, fields map[string]string) {
	for index, condition := range conditions {
		nodePath := fmt.Sprintf("%s.%d", path, index)
		if condition.Conditions != nil || strings.TrimSpace(condition.Match) != "" {
			validateConditionNodeFields(condition.Conditions, nodePath+".conditions", monitorType, allowed, fields)
			continue
		}
		if _, ok := allowed[condition.Field]; !ok {
			fields[nodePath+".field"] = "所选监控不会产生这个事件字段。"
			continue
		}
		if strings.EqualFold(strings.TrimSpace(condition.Operator), "within_last") {
			definition, exists := eventvars.EventDefinition(monitorType, condition.Field)
			if !exists || definition.ValueType != "datetime" {
				fields[nodePath+".operator"] = "“在最近时间内”只能用于时间字段。"
			}
		}
	}
}

func validateChannelInput(input model.NotifyChannelInput) error {
	fields := map[string]string{}
	if strings.TrimSpace(input.Name) == "" {
		fields["name"] = "请输入渠道名称。"
	}
	if input.Type != model.ChannelTypeBark && input.Type != model.ChannelTypeEmail && input.Type != model.ChannelTypeWebhook {
		fields["type"] = "请选择支持的渠道类型。"
	}
	_, err := decodeJSONObject(input.Config)
	if err != nil {
		fields["config"] = err.Error()
	} else if input.Type == model.ChannelTypeBark {
		var cfg notifier.BarkConfig
		if err := json.Unmarshal(input.Config, &cfg); err != nil {
			fields["config"] = "Bark 配置字段类型无效：" + err.Error()
		} else {
			if strings.TrimSpace(cfg.DeviceKey) == "" {
				fields["config.deviceKey"] = "请填写设备密钥。"
			}
			if raw := strings.TrimSpace(cfg.ServerURL); raw != "" && !validHTTPURL(raw) {
				fields["config.serverUrl"] = "服务地址必须是有效的 HTTP 或 HTTPS URL。"
			}
			if raw := strings.TrimSpace(cfg.Icon); raw != "" && !validHTTPURL(raw) {
				fields["config.icon"] = "图标必须是有效的 HTTP 或 HTTPS URL。"
			}
		}
	} else if input.Type == model.ChannelTypeEmail {
		var cfg notifier.EmailConfig
		if err := json.Unmarshal(input.Config, &cfg); err != nil {
			fields["config"] = "邮件配置字段类型无效：" + err.Error()
		} else {
			if strings.TrimSpace(cfg.Host) == "" {
				fields["config.host"] = "请填写 SMTP 主机。"
			}
			if cfg.Port < 1 || cfg.Port > 65535 {
				fields["config.port"] = "端口必须在 1 到 65535 之间。"
			}
			from := strings.TrimSpace(cfg.From)
			if from == "" {
				from = strings.TrimSpace(cfg.Username)
			}
			if _, err := mail.ParseAddress(from); err != nil {
				fields["config.from"] = "发件人必须是有效的邮件地址。"
			}
			if len(cfg.To) == 0 {
				fields["config.to"] = "请至少添加一个收件人。"
			} else {
				for _, value := range cfg.To {
					if _, err := mail.ParseAddress(value); err != nil {
						fields["config.to"] = "每个收件人都必须是有效的邮件地址。"
						break
					}
				}
			}
			if cfg.StartTLS && cfg.ImplicitTLS {
				fields["config.implicitTls"] = "STARTTLS 与隐式 TLS 只能启用一种。"
			}
		}
	} else if input.Type == model.ChannelTypeWebhook {
		if err := notifier.ValidateWebhookConfig(input.Config); err != nil {
			fields["config"] = "Webhook 配置无效：" + err.Error()
		}
	}
	if len(fields) > 0 {
		return validationProblem("请修正通知渠道配置中的问题。", fields)
	}
	return nil
}

func (s *Server) validateChannelNaturalKey(ctx context.Context, input model.NotifyChannelInput, excludeID int64) error {
	items, err := s.store.ListNotifyChannels(ctx)
	if err != nil {
		return err
	}
	name := strings.TrimSpace(input.Name)
	for _, item := range items {
		if item.ID != excludeID && item.Type == input.Type && strings.TrimSpace(item.Name) == name {
			return validationProblem("通知渠道名称与类型不能重复。", map[string]string{"name": "已存在同名、同类型的通知渠道。"})
		}
	}
	return nil
}

func validateTemplateInput(input model.NotificationTemplateInput) error {
	fields := map[string]string{}
	if strings.TrimSpace(input.Name) == "" {
		fields["name"] = "请输入模板名称。"
	}
	if strings.TrimSpace(input.SubjectTemplate) == "" {
		fields["subjectTemplate"] = "请输入通知标题。"
	}
	if strings.TrimSpace(input.BodyTemplate) == "" {
		fields["bodyTemplate"] = "请输入通知正文。"
	}
	if len(fields) > 0 {
		return validationProblem("请修正通知模板中的问题。", fields)
	}
	return nil
}

func (s *Server) validateTemplateNaturalKey(ctx context.Context, input model.NotificationTemplateInput, excludeID int64) error {
	items, err := s.store.ListNotificationTemplates(ctx)
	if err != nil {
		return err
	}
	name := strings.TrimSpace(input.Name)
	for _, item := range items {
		if item.ID != excludeID && strings.TrimSpace(item.Name) == name {
			return validationProblem("通知模板名称不能重复。", map[string]string{"name": "已存在同名通知模板。"})
		}
	}
	return nil
}

func decodeJSONObject(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("请填写配置")
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil || result == nil {
		return nil, fmt.Errorf("配置必须是 JSON 对象")
	}
	return result, nil
}

func validHTTPURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

func isEmptyConfigValue(value any) bool {
	if value == nil {
		return true
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed) == ""
	case []any:
		return len(typed) == 0
	default:
		return false
	}
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func numberValue(value any) (float64, bool) {
	number, ok := value.(float64)
	return number, ok
}
