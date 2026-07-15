package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/watchbell/watchbell/internal/store"
)

func wantsHistoryPage(r *http.Request) bool {
	query := r.URL.Query()
	for _, key := range []string{
		"page", "pageSize", "from", "to", "monitorId", "checkRunId", "eventId", "ruleId", "channelId", "entityId",
		"status", "trigger", "monitorType", "type", "kind", "channelType", "actor", "action", "entityType",
	} {
		if query.Has(key) {
			return true
		}
	}
	return false
}

func historyPageRequest(r *http.Request) (store.PageRequest, error) {
	page, err := positiveQueryInt(r, "page", 1, 0)
	if err != nil {
		return store.PageRequest{}, err
	}
	pageSize, err := positiveQueryInt(r, "pageSize", 20, 500)
	if err != nil {
		return store.PageRequest{}, err
	}
	return store.PageRequest{Page: page, PageSize: pageSize}, nil
}

func historyTimeRange(r *http.Request) (store.HistoryTimeRange, error) {
	from, err := optionalQueryTime(r, "from")
	if err != nil {
		return store.HistoryTimeRange{}, err
	}
	to, err := optionalQueryTime(r, "to")
	if err != nil {
		return store.HistoryTimeRange{}, err
	}
	if from != nil && to != nil && from.After(*to) {
		return store.HistoryTimeRange{}, historyQueryProblem("from", "开始时间不能晚于结束时间。")
	}
	return store.HistoryTimeRange{From: from, To: to}, nil
}

func optionalQueryID(r *http.Request, key string) (int64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, historyQueryProblem(key, "必须是正整数。")
	}
	return value, nil
}

func positiveQueryInt(r *http.Request, key string, fallback, maximum int) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, historyQueryProblem(key, "必须是正整数。")
	}
	if maximum > 0 && value > maximum {
		return 0, historyQueryProblem(key, fmt.Sprintf("不能大于 %d。", maximum))
	}
	return value, nil
}

func optionalQueryTime(r *http.Request, key string) (*time.Time, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return nil, nil
	}
	value, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, historyQueryProblem(key, "必须是 RFC3339 时间，例如 2026-07-15T12:00:00Z。")
	}
	value = value.UTC()
	return &value, nil
}

func historyQueryProblem(field, message string) error {
	return validationProblem("历史记录查询参数无效。", map[string]string{field: message})
}

func checkRunFilter(r *http.Request) (store.CheckRunFilter, error) {
	page, timeRange, err := historyPageAndRange(r)
	if err != nil {
		return store.CheckRunFilter{}, err
	}
	monitorID, err := optionalQueryID(r, "monitorId")
	if err != nil {
		return store.CheckRunFilter{}, err
	}
	return store.CheckRunFilter{PageRequest: page, HistoryTimeRange: timeRange, MonitorID: monitorID, Status: r.URL.Query().Get("status"), Trigger: r.URL.Query().Get("trigger"), MonitorType: r.URL.Query().Get("monitorType")}, nil
}

func eventFilter(r *http.Request) (store.EventFilter, error) {
	page, timeRange, err := historyPageAndRange(r)
	if err != nil {
		return store.EventFilter{}, err
	}
	monitorID, err := optionalQueryID(r, "monitorId")
	if err != nil {
		return store.EventFilter{}, err
	}
	checkRunID, err := optionalQueryID(r, "checkRunId")
	if err != nil {
		return store.EventFilter{}, err
	}
	return store.EventFilter{PageRequest: page, HistoryTimeRange: timeRange, MonitorID: monitorID, CheckRunID: checkRunID, Type: r.URL.Query().Get("type")}, nil
}

func ruleEvaluationFilter(r *http.Request) (store.RuleEvaluationFilter, error) {
	page, timeRange, err := historyPageAndRange(r)
	if err != nil {
		return store.RuleEvaluationFilter{}, err
	}
	eventID, err := optionalQueryID(r, "eventId")
	if err != nil {
		return store.RuleEvaluationFilter{}, err
	}
	ruleID, err := optionalQueryID(r, "ruleId")
	if err != nil {
		return store.RuleEvaluationFilter{}, err
	}
	return store.RuleEvaluationFilter{PageRequest: page, HistoryTimeRange: timeRange, EventID: eventID, RuleID: ruleID, Status: r.URL.Query().Get("status")}, nil
}

func notificationAttemptFilter(r *http.Request) (store.NotificationAttemptFilter, error) {
	page, timeRange, err := historyPageAndRange(r)
	if err != nil {
		return store.NotificationAttemptFilter{}, err
	}
	monitorID, err := optionalQueryID(r, "monitorId")
	if err != nil {
		return store.NotificationAttemptFilter{}, err
	}
	eventID, err := optionalQueryID(r, "eventId")
	if err != nil {
		return store.NotificationAttemptFilter{}, err
	}
	channelID, err := optionalQueryID(r, "channelId")
	if err != nil {
		return store.NotificationAttemptFilter{}, err
	}
	return store.NotificationAttemptFilter{PageRequest: page, HistoryTimeRange: timeRange, MonitorID: monitorID, EventID: eventID, ChannelID: channelID, Status: r.URL.Query().Get("status"), Kind: r.URL.Query().Get("kind"), ChannelType: r.URL.Query().Get("channelType")}, nil
}

func auditLogFilter(r *http.Request) (store.AuditLogFilter, error) {
	page, timeRange, err := historyPageAndRange(r)
	if err != nil {
		return store.AuditLogFilter{}, err
	}
	entityID, err := optionalQueryID(r, "entityId")
	if err != nil {
		return store.AuditLogFilter{}, err
	}
	return store.AuditLogFilter{PageRequest: page, HistoryTimeRange: timeRange, Actor: r.URL.Query().Get("actor"), Action: r.URL.Query().Get("action"), EntityType: r.URL.Query().Get("entityType"), EntityID: entityID}, nil
}

func historyPageAndRange(r *http.Request) (store.PageRequest, store.HistoryTimeRange, error) {
	page, err := historyPageRequest(r)
	if err != nil {
		return store.PageRequest{}, store.HistoryTimeRange{}, err
	}
	timeRange, err := historyTimeRange(r)
	return page, timeRange, err
}
