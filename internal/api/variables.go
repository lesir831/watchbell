package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/watchbell/watchbell/internal/eventvars"
	"github.com/watchbell/watchbell/internal/model"
	"github.com/watchbell/watchbell/internal/scheduler"
	"github.com/watchbell/watchbell/internal/store"
)

type variableSnapshotResponse struct {
	MonitorID       int64             `json:"monitorId"`
	MonitorName     string            `json:"monitorName"`
	MonitorType     string            `json:"monitorType"`
	EventID         *int64            `json:"eventId,omitempty"`
	EventType       string            `json:"eventType,omitempty"`
	EventCreatedAt  *time.Time        `json:"eventCreatedAt,omitempty"`
	Source          string            `json:"source"`
	ObservationType string            `json:"observationType,omitempty"`
	SampleAvailable bool              `json:"sampleAvailable"`
	Message         string            `json:"message,omitempty"`
	GeneratedAt     time.Time         `json:"generatedAt"`
	Values          map[string]any    `json:"values"`
	ValueLinks      map[string]string `json:"valueLinks"`
}

func (s *Server) variableCatalog(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, eventvars.VariableCatalog())
}

func (s *Server) latestMonitorVariables(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	monitorID, ok := pathID(w, r)
	if !ok {
		return
	}
	monitor, err := s.store.GetMonitor(r.Context(), monitorID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	key := strings.TrimSpace(chi.URLParam(r, "key"))
	if key != "" && !snapshotVariableKey(monitor.Type, key) {
		writeError(w, r, validationProblem("实时检查不提供这个变量。", map[string]string{"key": "这个变量不适用于所选监控的实时检查。"}))
		return
	}
	observation, err := s.scheduler.InspectMonitor(r.Context(), monitor)
	if err != nil {
		if errors.Is(err, store.ErrProxyUnavailable) {
			writeError(w, r, err)
		} else {
			status := http.StatusBadGateway
			code := "monitor_inspection_failed"
			if errors.Is(err, scheduler.ErrInspectionUnsupported) {
				status = http.StatusUnprocessableEntity
				code = "monitor_inspection_unsupported"
			}
			writeError(w, r, &problemError{Status: status, Code: code, Message: err.Error()})
		}
		return
	}
	s.writeLiveVariableSnapshot(w, r, monitor, observation, key, fmt.Sprintf("/api/monitors/%d/variables", monitor.ID))
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
	currentMonitor, err := s.store.GetMonitorIncludingArchived(r.Context(), event.MonitorID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	monitor, err := s.eventVariableMonitor(r.Context(), currentMonitor, event)
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.writeVariableSnapshot(w, r, monitor, &event, chi.URLParam(r, "key"), fmt.Sprintf("/api/events/%d/variables", event.ID))
}

func (s *Server) eventVariableMonitor(ctx context.Context, current model.Monitor, event model.Event) (model.Monitor, error) {
	if event.CheckRunID != nil {
		run, err := s.store.GetCheckRun(ctx, *event.CheckRunID)
		if err == nil && run.MonitorID == event.MonitorID {
			monitor := model.Monitor{
				ID: event.MonitorID, Name: strings.TrimSpace(run.MonitorName), Type: strings.TrimSpace(run.MonitorType),
				Config: validConfigSnapshot(run.ConfigSnapshot),
			}
			if monitor.Name == "" {
				monitor.Name = current.Name
			}
			if monitor.Type == "" {
				monitor.Type = inferredMonitorType(event, current.Type)
			}
			return monitor, nil
		}
		if err != nil && !store.IsNotFound(err) {
			return model.Monitor{}, err
		}
	}

	// Legacy events may not be linked to a check run. Preserve the current name
	// as the only available label, but never reuse mutable monitor configuration.
	// The event type and namespaced payload are sufficient to recover all built-in
	// module variables without making a historical URL drift to today's config.
	return model.Monitor{
		ID: event.MonitorID, Name: current.Name, Type: inferredMonitorType(event, current.Type), Config: json.RawMessage(`{}`),
	}, nil
}

func validConfigSnapshot(config json.RawMessage) json.RawMessage {
	if len(config) == 0 || !json.Valid(config) {
		return json.RawMessage(`{}`)
	}
	return append(json.RawMessage(nil), config...)
}

func inferredMonitorType(event model.Event, fallback string) string {
	eventType := strings.ToLower(strings.TrimSpace(event.Type))
	switch {
	case strings.HasPrefix(eventType, "rss."):
		return model.MonitorTypeRSS
	case strings.HasPrefix(eventType, "testflight."):
		return model.MonitorTypeTestFlight
	case strings.HasPrefix(eventType, "webpage."):
		return model.MonitorTypeWebpage
	case strings.HasPrefix(eventType, "github."):
		return model.MonitorTypeGitHubRelease
	}
	var payload map[string]any
	if json.Unmarshal(event.Payload, &payload) == nil {
		for _, candidate := range []struct{ root, monitorType string }{
			{"rss", model.MonitorTypeRSS}, {"testflight", model.MonitorTypeTestFlight},
			{"webpage", model.MonitorTypeWebpage}, {"github", model.MonitorTypeGitHubRelease},
		} {
			if _, ok := payload[candidate.root].(map[string]any); ok {
				return candidate.monitorType
			}
		}
	}
	return fallback
}

func (s *Server) writeVariableSnapshot(w http.ResponseWriter, r *http.Request, monitor model.Monitor, event *model.Event, key, basePath string) {
	w.Header().Set("Cache-Control", "no-store")
	values := map[string]any{
		"monitor.id": monitor.ID, "monitor.name": monitor.Name, "monitor.type": monitor.Type,
	}
	response := variableSnapshotResponse{
		MonitorID: monitor.ID, MonitorName: monitor.Name, MonitorType: monitor.Type,
		Source: "event", SampleAvailable: event != nil,
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

	s.writeVariableSnapshotResponse(w, r, response, key, "最近事件没有这个变量的实时取值。")
}

func (s *Server) writeLiveVariableSnapshot(w http.ResponseWriter, r *http.Request, monitor model.Monitor, observation model.Observation, key, basePath string) {
	w.Header().Set("Cache-Control", "no-store")
	flattened := eventvars.Flatten(eventvars.ObservationData(monitor, observation))
	values := map[string]any{}
	for _, variableKey := range eventvars.SnapshotKeys(monitor.Type) {
		if value, exists := flattened[variableKey]; exists {
			values[variableKey] = value
		}
	}
	response := variableSnapshotResponse{
		MonitorID: monitor.ID, MonitorName: monitor.Name, MonitorType: monitor.Type,
		Source: "live", ObservationType: observation.Type, SampleAvailable: observation.Available,
		Message: observation.Message, GeneratedAt: time.Now().UTC(), Values: values,
		ValueLinks: variableValueLinks(basePath, monitor.Type),
	}
	s.writeVariableSnapshotResponse(w, r, response, key, "本次实时抓取没有这个变量的取值。")
}

func (s *Server) writeVariableSnapshotResponse(w http.ResponseWriter, r *http.Request, response variableSnapshotResponse, key, unavailableMessage string) {
	key = strings.TrimSpace(key)
	if key == "" {
		writeJSON(w, http.StatusOK, response)
		return
	}
	if !eventvars.DocumentedKey(response.MonitorType, key) {
		writeError(w, r, validationProblem("变量不存在。", map[string]string{"key": "这个变量不适用于所选监控。"}))
		return
	}
	value, exists := response.Values[key]
	if !exists {
		writeError(w, r, &problemError{
			Status: http.StatusNotFound, Code: "variable_value_unavailable",
			Message: unavailableMessage, Fields: map[string]string{"key": key},
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"monitorId": response.MonitorID, "eventId": response.EventID, "source": response.Source,
		"observationType": response.ObservationType, "key": key, "value": value,
		"generatedAt": response.GeneratedAt,
	})
}

func variableValueLinks(basePath, monitorType string) map[string]string {
	result := make(map[string]string)
	for _, key := range eventvars.SnapshotKeys(monitorType) {
		result[key] = basePath + "/" + url.PathEscape(key)
	}
	return result
}

func snapshotVariableKey(monitorType, key string) bool {
	for _, candidate := range eventvars.SnapshotKeys(monitorType) {
		if candidate == key {
			return true
		}
	}
	return false
}
