package api

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/watchbell/watchbell/internal/model"
	"github.com/watchbell/watchbell/internal/store"
)

const maxCopyNameAttempts = 10_000

func (s *Server) copyMonitor(w http.ResponseWriter, r *http.Request) {
	sourceID, ok := pathID(w, r)
	if !ok {
		return
	}
	source, err := s.store.GetMonitor(r.Context(), sourceID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	input := model.MonitorInput{
		Type:                    source.Type,
		ProxyID:                 source.ProxyID,
		Enabled:                 false,
		IntervalSeconds:         source.IntervalSeconds,
		Config:                  cloneRawMessage(source.Config),
		FailureAlertAfter:       source.FailureAlertAfter,
		FailureNotifyChannelIDs: append([]int64(nil), source.FailureNotifyChannelIDs...),
	}
	item, err := createNamedCopy(source.Name, func(name string) (model.Monitor, error) {
		input.Name = name
		return s.store.CreateMonitor(r.Context(), input)
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	id := item.ID
	s.recordAudit(r.Context(), s.actor(r), "copy", "monitor", &id, "复制监控 · "+item.Name, map[string]any{"sourceId": sourceID})
	writeJSON(w, http.StatusCreated, s.sanitizeMonitor(item))
}

func (s *Server) copyRule(w http.ResponseWriter, r *http.Request) {
	sourceID, ok := pathID(w, r)
	if !ok {
		return
	}
	source, err := s.store.GetRule(r.Context(), sourceID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	input := model.RuleInput{
		MonitorID:        source.MonitorID,
		MonitorIDs:       append([]int64(nil), source.MonitorIDs...),
		Enabled:          false,
		Condition:        cloneRawMessage(source.Condition),
		NotifyChannelIDs: append([]int64(nil), source.NotifyChannelIDs...),
		TemplateID:       cloneInt64(source.TemplateID),
		CooldownSeconds:  source.CooldownSeconds,
		QuietHours:       source.QuietHours,
	}
	item, err := createNamedCopy(source.Name, func(name string) (model.Rule, error) {
		input.Name = name
		if err := s.validateRuleNaturalKey(r.Context(), input, 0); err != nil {
			var problem *problemError
			if errors.As(err, &problem) && problem.Status == http.StatusUnprocessableEntity && problem.Fields["name"] != "" {
				return model.Rule{}, store.ErrDuplicateNaturalKey
			}
			return model.Rule{}, err
		}
		return s.store.CreateRule(r.Context(), input)
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	id := item.ID
	s.recordAudit(r.Context(), s.actor(r), "copy", "rule", &id, "复制规则 · "+item.Name, map[string]any{"sourceId": sourceID})
	writeJSON(w, http.StatusCreated, item)
}

func (s *Server) copyNotifyChannel(w http.ResponseWriter, r *http.Request) {
	sourceID, ok := pathID(w, r)
	if !ok {
		return
	}
	source, err := s.store.GetNotifyChannel(r.Context(), sourceID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	input := model.NotifyChannelInput{
		Type:    source.Type,
		Enabled: false,
		Config:  cloneRawMessage(source.Config),
	}
	item, err := createNamedCopy(source.Name, func(name string) (model.NotifyChannel, error) {
		input.Name = name
		return s.store.CreateNotifyChannel(r.Context(), input)
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	id := item.ID
	s.recordAudit(r.Context(), s.actor(r), "copy", "channel", &id, "复制渠道 · "+item.Name, map[string]any{"sourceId": sourceID})
	writeJSON(w, http.StatusCreated, sanitizeChannel(item))
}

func (s *Server) copyNotificationTemplate(w http.ResponseWriter, r *http.Request) {
	sourceID, ok := pathID(w, r)
	if !ok {
		return
	}
	source, err := s.store.GetNotificationTemplate(r.Context(), sourceID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	input := model.NotificationTemplateInput{
		SubjectTemplate: source.SubjectTemplate,
		BodyTemplate:    source.BodyTemplate,
	}
	item, err := createNamedCopy(source.Name, func(name string) (model.NotificationTemplate, error) {
		input.Name = name
		return s.store.CreateNotificationTemplate(r.Context(), input)
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	id := item.ID
	s.recordAudit(r.Context(), s.actor(r), "copy", "template", &id, "复制模板 · "+item.Name, map[string]any{"sourceId": sourceID})
	writeJSON(w, http.StatusCreated, item)
}

// createNamedCopy retries on the store's atomic natural-key guard. This keeps
// concurrent copy requests from failing merely because they selected the same
// suffix before either write became visible.
func createNamedCopy[T any](sourceName string, create func(string) (T, error)) (T, error) {
	var zero T
	for attempt := 1; attempt <= maxCopyNameAttempts; attempt++ {
		item, err := create(copyName(sourceName, attempt))
		if err == nil {
			return item, nil
		}
		if !errors.Is(err, store.ErrDuplicateNaturalKey) {
			return zero, err
		}
	}
	return zero, fmt.Errorf("generate copy name for %q: %w", sourceName, store.ErrDuplicateNaturalKey)
}

func copyName(sourceName string, attempt int) string {
	if attempt <= 1 {
		return sourceName + " 副本"
	}
	return fmt.Sprintf("%s 副本 %d", sourceName, attempt)
}

func cloneRawMessage(raw []byte) []byte {
	return append([]byte(nil), raw...)
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
