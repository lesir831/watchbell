package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/watchbell/watchbell/internal/eventvars"
	"github.com/watchbell/watchbell/internal/model"
	"github.com/watchbell/watchbell/internal/store"
)

type variableSnapshotResponse struct {
	MonitorID      int64             `json:"monitorId"`
	MonitorName    string            `json:"monitorName"`
	MonitorType    string            `json:"monitorType"`
	EventID        *int64            `json:"eventId,omitempty"`
	EventType      string            `json:"eventType,omitempty"`
	EventCreatedAt *time.Time        `json:"eventCreatedAt,omitempty"`
	GeneratedAt    time.Time         `json:"generatedAt"`
	Values         map[string]any    `json:"values"`
	ValueLinks     map[string]string `json:"valueLinks"`
}

func (s *Server) variableCatalog(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, eventvars.VariableCatalog())
}

func (s *Server) latestMonitorVariables(w http.ResponseWriter, r *http.Request) {
	monitorID, ok := pathID(w, r)
	if !ok {
		return
	}
	monitor, err := s.store.GetMonitor(r.Context(), monitorID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	page, err := s.store.ListEventsPage(r.Context(), store.EventFilter{
		PageRequest: store.PageRequest{Page: 1, PageSize: 1}, MonitorID: monitor.ID,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	var event *model.Event
	if len(page.Items) > 0 {
		event = &page.Items[0]
	}
	s.writeVariableSnapshot(w, r, monitor, event, chi.URLParam(r, "key"), fmt.Sprintf("/api/monitors/%d/variables", monitor.ID))
}

func (s *Server) eventVariables(w http.ResponseWriter, r *http.Request) {
	eventID, ok := pathID(w, r)
	if !ok {
		return
	}
	event, err := s.store.GetEvent(r.Context(), eventID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	monitor, err := s.store.GetMonitorIncludingArchived(r.Context(), event.MonitorID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.writeVariableSnapshot(w, r, monitor, &event, chi.URLParam(r, "key"), fmt.Sprintf("/api/events/%d/variables", event.ID))
}

func (s *Server) writeVariableSnapshot(w http.ResponseWriter, r *http.Request, monitor model.Monitor, event *model.Event, key, basePath string) {
	w.Header().Set("Cache-Control", "no-store")
	values := map[string]any{
		"monitor.id": monitor.ID, "monitor.name": monitor.Name, "monitor.type": monitor.Type,
	}
	response := variableSnapshotResponse{
		MonitorID: monitor.ID, MonitorName: monitor.Name, MonitorType: monitor.Type,
		GeneratedAt: time.Now().UTC(), Values: values, ValueLinks: variableValueLinks(basePath, monitor.Type),
	}
	if event != nil {
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			writeError(w, r, err)
			return
		}
		response.EventID = &event.ID
		response.EventType = event.Type
		createdAt := event.CreatedAt.UTC()
		response.EventCreatedAt = &createdAt
		flattened := eventvars.Flatten(eventvars.EventData(monitor, *event, payload))
		response.Values = map[string]any{}
		for _, variableKey := range eventvars.SnapshotKeys(monitor.Type) {
			if value, exists := flattened[variableKey]; exists {
				response.Values[variableKey] = value
			}
		}
	}

	key = strings.TrimSpace(key)
	if key == "" {
		writeJSON(w, http.StatusOK, response)
		return
	}
	if !eventvars.DocumentedKey(monitor.Type, key) {
		writeError(w, r, validationProblem("变量不存在。", map[string]string{"key": "这个变量不适用于所选监控。"}))
		return
	}
	value, exists := response.Values[key]
	if !exists {
		writeError(w, r, &problemError{
			Status: http.StatusNotFound, Code: "variable_value_unavailable",
			Message: "最近事件没有这个变量的实时取值。", Fields: map[string]string{"key": key},
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"monitorId": monitor.ID, "eventId": response.EventID, "key": key,
		"value": value, "generatedAt": response.GeneratedAt,
	})
}

func variableValueLinks(basePath, monitorType string) map[string]string {
	result := make(map[string]string)
	for _, key := range eventvars.SnapshotKeys(monitorType) {
		result[key] = basePath + "/" + url.PathEscape(key)
	}
	return result
}
