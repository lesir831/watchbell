package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/watchbell/watchbell/internal/eventvars"
	"github.com/watchbell/watchbell/internal/model"
	"github.com/watchbell/watchbell/internal/rule"
	"github.com/watchbell/watchbell/internal/store"
)

type ruleTestInput struct {
	MonitorID  int64           `json:"monitorId"`
	MonitorIDs []int64         `json:"monitorIds"`
	Condition  json.RawMessage `json:"condition"`
	Limit      int             `json:"limit"`
}

type ruleTestResult struct {
	MonitorID   int64           `json:"monitorId"`
	MonitorName string          `json:"monitorName"`
	EventID     int64           `json:"eventId"`
	EventType   string          `json:"eventType"`
	Matched     []string        `json:"matched"`
	Payload     json.RawMessage `json:"payload"`
	CreatedAt   time.Time       `json:"createdAt"`
}

func (s *Server) testRule(w http.ResponseWriter, r *http.Request) {
	var input ruleTestInput
	if !decode(w, r, &input) {
		return
	}
	monitorIDs := effectiveRuleMonitorIDs(input.MonitorID, input.MonitorIDs)
	monitors := make([]model.Monitor, 0, len(monitorIDs))
	fields := map[string]string{}
	if len(monitorIDs) > 100 {
		fields["monitorIds"] = "一次最多测试 100 个监控。"
		writeError(w, r, validationProblem("请选择现有监控。", fields))
		return
	}
	seen := map[int64]struct{}{}
	for index, monitorID := range monitorIDs {
		field := "monitorIds." + strconv.Itoa(index)
		if monitorID <= 0 {
			fields[field] = "监控 ID 必须是正整数。"
			continue
		}
		if _, duplicate := seen[monitorID]; duplicate {
			fields[field] = "关联监控不能重复。"
			continue
		}
		seen[monitorID] = struct{}{}
		monitor, err := s.store.GetMonitor(r.Context(), monitorID)
		if err != nil {
			fields[field] = "监控不存在或已归档。"
			continue
		}
		monitors = append(monitors, monitor)
	}
	if len(monitorIDs) == 0 {
		fields["monitorIds"] = "请至少选择一个现有监控。"
	}
	if len(fields) > 0 {
		writeError(w, r, validationProblem("请选择现有监控。", fields))
		return
	}
	if err := rule.Validate(input.Condition); err != nil {
		writeError(w, r, validationProblem("规则条件无效。", map[string]string{"condition": err.Error()}))
		return
	}
	monitorTypes := make([]string, 0, len(monitors))
	for _, monitor := range monitors {
		monitorTypes = append(monitorTypes, monitor.Type)
	}
	fields = map[string]string{}
	validateConditionFieldsForTypes(input.Condition, monitorTypes, s.scheduler.Plugins(), fields)
	if len(fields) > 0 {
		writeError(w, r, validationProblem("规则包含该监控不会产生的字段。", fields))
		return
	}
	limit := input.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	type testEvent struct {
		event   model.Event
		monitor model.Monitor
	}
	events := make([]testEvent, 0, limit*len(monitors))
	for _, monitor := range monitors {
		eventPage, err := s.store.ListEventsPage(r.Context(), store.EventFilter{
			PageRequest: store.PageRequest{Page: 1, PageSize: limit},
			MonitorID:   monitor.ID,
		})
		if err != nil {
			writeError(w, r, err)
			return
		}
		for _, event := range eventPage.Items {
			events = append(events, testEvent{event: event, monitor: monitor})
		}
	}
	sort.Slice(events, func(i, j int) bool {
		if events[i].event.CreatedAt.Equal(events[j].event.CreatedAt) {
			return events[i].event.ID > events[j].event.ID
		}
		return events[i].event.CreatedAt.After(events[j].event.CreatedAt)
	})
	if len(events) > limit {
		events = events[:limit]
	}
	results := make([]ruleTestResult, 0)
	tested := 0
	for _, candidate := range events {
		event := candidate.event
		tested++
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			continue
		}
		payload = eventvars.EnrichPayload(candidate.monitor, payload)
		matchedOK, matched, err := rule.Match(input.Condition, payload)
		if err != nil {
			writeError(w, r, err)
			return
		}
		if matchedOK {
			if matched == nil {
				matched = []string{}
			}
			results = append(results, ruleTestResult{MonitorID: candidate.monitor.ID, MonitorName: candidate.monitor.Name, EventID: event.ID, EventType: event.Type, Matched: matched, Payload: event.Payload, CreatedAt: event.CreatedAt})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tested": tested, "matched": len(results), "results": results})
}
