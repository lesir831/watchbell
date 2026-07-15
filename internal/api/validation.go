package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/mail"
	"net/url"
	"strings"

	"github.com/watchbell/watchbell/internal/model"
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

func (s *Server) validateMonitorInput(input model.MonitorInput) error {
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
				}
			}
		}
		validateMonitorConfig(input.Type, config, fields)
	}
	if len(fields) > 0 {
		return validationProblem("请修正监控配置中的问题。", fields)
	}
	return nil
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
	for index, condition := range set.Conditions {
		if _, ok := allowed[condition.Field]; !ok {
			fields[fmt.Sprintf("condition.conditions.%d.field", index)] = "所选监控不会产生这个事件字段。"
		}
	}
}

func validateChannelInput(input model.NotifyChannelInput) error {
	fields := map[string]string{}
	if strings.TrimSpace(input.Name) == "" {
		fields["name"] = "请输入渠道名称。"
	}
	if input.Type != model.ChannelTypeBark && input.Type != model.ChannelTypeEmail {
		fields["type"] = "请选择支持的渠道类型。"
	}
	config, err := decodeJSONObject(input.Config)
	if err != nil {
		fields["config"] = err.Error()
	} else if input.Type == model.ChannelTypeBark {
		if strings.TrimSpace(stringValue(config["deviceKey"])) == "" {
			fields["config.deviceKey"] = "请填写设备密钥。"
		}
		if raw := stringValue(config["serverUrl"]); raw != "" && !validHTTPURL(raw) {
			fields["config.serverUrl"] = "服务地址必须是有效的 HTTP 或 HTTPS URL。"
		}
	} else if input.Type == model.ChannelTypeEmail {
		if strings.TrimSpace(stringValue(config["host"])) == "" {
			fields["config.host"] = "请填写 SMTP 主机。"
		}
		if value, ok := numberValue(config["port"]); !ok || value < 1 || value > 65535 {
			fields["config.port"] = "端口必须在 1 到 65535 之间。"
		}
		from := stringValue(config["from"])
		if from == "" {
			from = stringValue(config["username"])
		}
		if _, err := mail.ParseAddress(from); err != nil {
			fields["config.from"] = "发件人必须是有效的邮件地址。"
		}
		to, ok := config["to"].([]any)
		if !ok || len(to) == 0 {
			fields["config.to"] = "请至少添加一个收件人。"
		} else {
			for _, value := range to {
				if _, err := mail.ParseAddress(fmt.Sprint(value)); err != nil {
					fields["config.to"] = "每个收件人都必须是有效的邮件地址。"
					break
				}
			}
		}
	}
	if len(fields) > 0 {
		return validationProblem("请修正通知渠道配置中的问题。", fields)
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
