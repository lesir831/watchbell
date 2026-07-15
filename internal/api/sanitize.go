package api

import (
	"encoding/json"
	"strings"

	"github.com/watchbell/watchbell/internal/model"
)

func (s *Server) sanitizeMonitor(item model.Monitor) model.Monitor {
	secretKeys := make([]string, 0)
	for _, plugin := range s.scheduler.Plugins() {
		if plugin.ID != item.Type {
			continue
		}
		for _, field := range plugin.ConfigFields {
			if field.Secret {
				secretKeys = append(secretKeys, field.Key)
			}
		}
	}
	item.Config, item.ConfiguredSecrets = redactConfig(item.Config, secretKeys)
	item.NextCheckAt = s.scheduler.NextCheckAt(item)
	item.State = nil
	return item
}

func (s *Server) sanitizeMonitors(items []model.Monitor) []model.Monitor {
	result := make([]model.Monitor, 0, len(items))
	for _, item := range items {
		result = append(result, s.sanitizeMonitor(item))
	}
	return result
}

func sanitizeChannel(item model.NotifyChannel) model.NotifyChannel {
	item.Config, item.ConfiguredSecrets = redactConfig(item.Config, channelSecretKeys(item.Type))
	return item
}

func sanitizeChannels(items []model.NotifyChannel) []model.NotifyChannel {
	result := make([]model.NotifyChannel, 0, len(items))
	for _, item := range items {
		result = append(result, sanitizeChannel(item))
	}
	return result
}

func redactConfig(raw json.RawMessage, secretKeys []string) (json.RawMessage, []string) {
	var config map[string]any
	if json.Unmarshal(raw, &config) != nil || config == nil {
		return json.RawMessage("{}"), nil
	}
	configured := make([]string, 0)
	for _, key := range secretKeys {
		if value, exists := config[key]; exists && hasConfiguredSecretValue(value) {
			configured = append(configured, key)
		}
		delete(config, key)
	}
	data, err := json.Marshal(config)
	if err != nil {
		return json.RawMessage("{}"), configured
	}
	return data, configured
}

func hasConfiguredSecretValue(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(typed) != ""
	case map[string]any:
		return len(typed) > 0
	case []any:
		return len(typed) > 0
	default:
		return true
	}
}

func mergeSecretConfig(existing, incoming json.RawMessage, secretKeys []string) json.RawMessage {
	var current, next map[string]any
	if json.Unmarshal(existing, &current) != nil {
		current = map[string]any{}
	}
	if json.Unmarshal(incoming, &next) != nil {
		return incoming
	}
	for _, key := range secretKeys {
		if isEmptyConfigValue(next[key]) {
			if value, ok := current[key]; ok {
				next[key] = value
			}
		}
	}
	data, err := json.Marshal(next)
	if err != nil {
		return incoming
	}
	return data
}

func monitorSecretKeys(monitorType string, plugins []model.MonitorPlugin) []string {
	keys := make([]string, 0)
	for _, plugin := range plugins {
		if plugin.ID != monitorType {
			continue
		}
		for _, field := range plugin.ConfigFields {
			if field.Secret {
				keys = append(keys, field.Key)
			}
		}
	}
	return keys
}

func channelSecretKeys(channelType string) []string {
	switch channelType {
	case model.ChannelTypeBark:
		return []string{"deviceKey"}
	case model.ChannelTypeEmail:
		return []string{"password"}
	case model.ChannelTypeWebhook:
		// Tokens are commonly embedded in both provider URLs and headers.
		return []string{"url", "headers"}
	default:
		return nil
	}
}
