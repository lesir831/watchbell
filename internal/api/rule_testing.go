package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/watchbell/watchbell/internal/eventvars"
	"github.com/watchbell/watchbell/internal/rule"
	"github.com/watchbell/watchbell/internal/store"
)

type ruleTestInput struct {
	MonitorID int64           `json:"monitorId"`
	Condition json.RawMessage `json:"condition"`
	Limit     int             `json:"limit"`
}

type ruleTestResult struct {
	EventID   int64           `json:"eventId"`
	EventType string          `json:"eventType"`
	Matched   []string        `json:"matched"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"createdAt"`
}

func (s *Server) testRule(w http.ResponseWriter, r *http.Request) {
	var input ruleTestInput
	if !decode(w, r, &input) {
		return
	}
	monitor, err := s.store.GetMonitor(r.Context(), input.MonitorID)
	if err != nil {
		writeError(w, r, validationProblem("请选择一个现有监控。", map[string]string{"monitorId": "监控不存在或已归档。"}))
		return
	}
	if err := rule.Validate(input.Condition); err != nil {
		writeError(w, r, validationProblem("规则条件无效。", map[string]string{"condition": err.Error()}))
		return
	}
	fields := map[string]string{}
	validateConditionFields(input.Condition, monitor.Type, s.scheduler.Plugins(), fields)
	if len(fields) > 0 {
		writeError(w, r, validationProblem("规则包含该监控不会产生的字段。", fields))
		return
	}
	limit := input.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	eventPage, err := s.store.ListEventsPage(r.Context(), store.EventFilter{
		PageRequest: store.PageRequest{Page: 1, PageSize: limit},
		MonitorID:   input.MonitorID,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	results := make([]ruleTestResult, 0)
	tested := 0
	for _, event := range eventPage.Items {
		tested++
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			continue
		}
		payload = eventvars.EnrichPayload(monitor, payload)
		matchedOK, matched, err := rule.Match(input.Condition, payload)
		if err != nil {
			writeError(w, r, err)
			return
		}
		if matchedOK {
			if matched == nil {
				matched = []string{}
			}
			results = append(results, ruleTestResult{EventID: event.ID, EventType: event.Type, Matched: matched, Payload: event.Payload, CreatedAt: event.CreatedAt})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tested": tested, "matched": len(results), "results": results})
}
