package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/watchbell/watchbell/internal/auth"
	"github.com/watchbell/watchbell/internal/eventvars"
	"github.com/watchbell/watchbell/internal/model"
	"github.com/watchbell/watchbell/internal/scheduler"
	"github.com/watchbell/watchbell/internal/store"
	"github.com/watchbell/watchbell/internal/templatex"
)

type Server struct {
	store     *store.Store
	scheduler *scheduler.Scheduler
	webDir    string
	logger    *slog.Logger
	auth      *auth.Manager
}

func NewServer(store *store.Store, scheduler *scheduler.Scheduler, webDir string, logger *slog.Logger, authManager *auth.Manager) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{store: store, scheduler: scheduler, webDir: webDir, logger: logger, auth: authManager}
}

func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	if s.auth != nil && s.auth.TrustProxyHeaders() {
		// Read only the X-Forwarded-For position that a configured number of
		// trusted proxy hops controls. The deprecated RealIP middleware accepts
		// attacker-supplied True-Client-IP/X-Real-IP values and is not suitable
		// for login rate-limit identity.
		r.Use(middleware.ClientIPFromXFFTrustedProxies(s.auth.TrustedProxyHops()))
	} else {
		r.Use(middleware.ClientIPFromRemoteAddr)
	}
	r.Use(s.accessLog)
	r.Use(middleware.Recoverer)

	r.Route("/api", func(r chi.Router) {
		if s.auth != nil && s.auth.Enabled() {
			r.Use(s.protectBrowserMutation)
		}
		r.Get("/health", s.health)
		r.Get("/health/ready", s.readiness)
		r.Get("/auth/status", s.authStatus)
		r.Post("/auth/login", s.authLogin)
		r.Post("/auth/logout", s.authLogout)

		if s.auth != nil && s.auth.Enabled() {
			r.Group(func(r chi.Router) {
				r.Use(s.auth.Require)
				s.privateRoutes(r)
			})
			return
		}
		s.privateRoutes(r)
	})

	r.NotFound(s.serveSPA)
	r.Head("/*", s.serveSPA)
	r.Get("/*", s.serveSPA)
	return r
}

func (s *Server) privateRoutes(r chi.Router) {
	r.Get("/auth/me", s.authMe)
	r.Get("/settings", s.settingsOverview)
	r.Post("/settings/password", s.changePassword)
	r.Get("/settings/proxies", s.listProxyProfiles)
	r.Post("/settings/proxies", s.createProxyProfile)
	r.Put("/settings/proxies/{id}", s.updateProxyProfile)
	r.Delete("/settings/proxies/{id}", s.deleteProxyProfile)
	r.Get("/plugins", s.listPlugins)
	r.Get("/help/variables", s.variableCatalog)
	r.Get("/dashboard", s.dashboard)
	r.Get("/system/status", s.systemStatus)
	r.Get("/diagnostics", s.diagnostics)
	r.Get("/config/export", s.exportConfig)
	r.Post("/config/import", s.importConfig)

	r.Get("/monitors", s.listMonitors)
	r.Post("/monitors", s.createMonitor)
	r.Put("/monitors/{id}", s.updateMonitor)
	r.Delete("/monitors/{id}", s.deleteMonitor)
	r.Post("/monitors/{id}/check", s.checkMonitor)
	r.Get("/monitors/{id}/variables", s.latestMonitorVariables)
	r.Get("/monitors/{id}/variables/{key}", s.latestMonitorVariables)

	r.Get("/rules", s.listRules)
	r.Post("/rules", s.createRule)
	r.Post("/rules/test", s.testRule)
	r.Put("/rules/{id}", s.updateRule)
	r.Delete("/rules/{id}", s.deleteRule)

	r.Get("/channels", s.listNotifyChannels)
	r.Post("/channels", s.createNotifyChannel)
	r.Put("/channels/{id}", s.updateNotifyChannel)
	r.Delete("/channels/{id}", s.deleteNotifyChannel)
	r.Post("/channels/{id}/test", s.testNotifyChannel)

	r.Get("/templates", s.listNotificationTemplates)
	r.Post("/templates", s.createNotificationTemplate)
	r.Put("/templates/{id}", s.updateNotificationTemplate)
	r.Delete("/templates/{id}", s.deleteNotificationTemplate)
	r.Post("/templates/preview", s.previewTemplate)

	r.Get("/events", s.listEvents)
	r.Get("/events/{id}/variables", s.eventVariables)
	r.Get("/events/{id}/variables/{key}", s.eventVariables)
	r.Get("/check-runs", s.listCheckRuns)
	r.Get("/rule-evaluations", s.listRuleEvaluations)
	r.Get("/notification-attempts", s.listNotificationAttempts)
	r.Post("/notification-attempts/{id}/retry", s.retryNotificationAttempt)
	r.Get("/dead-letters", s.listDeadLetters)
	r.Post("/dead-letters/{id}/retry", s.retryDeadLetter)
	r.Get("/audit-logs", s.listAuditLogs)
	r.Get("/notification-logs", s.listNotificationLogs)
}

func (s *Server) listPlugins(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.scheduler.Plugins())
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) readiness(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Ping(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not_ready", "database": "unavailable"})
		return
	}
	health := s.scheduler.Health()
	status := http.StatusOK
	ready := "ready"
	if health.LastTickAt == nil || time.Since(*health.LastTickAt) > 2*time.Minute {
		status = http.StatusServiceUnavailable
		ready = "not_ready"
	}
	writeJSON(w, status, map[string]any{"status": ready, "database": "ok", "scheduler": health})
}

func (s *Server) authStatus(w http.ResponseWriter, r *http.Request) {
	enabled := s.auth != nil && s.auth.Enabled()
	username := ""
	if enabled {
		username = s.auth.Username()
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": enabled, "username": username})
}

func (s *Server) authLogin(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil || !s.auth.Enabled() {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "auth is disabled"})
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decode(w, r, &input) {
		return
	}
	if err := s.auth.Login(w, r, input.Username, input.Password); err != nil {
		if retryAfter, limited := auth.LoginRetryAfter(err); limited {
			seconds := max(1, int((retryAfter+time.Second-1)/time.Second))
			w.Header().Set("Retry-After", strconv.Itoa(seconds))
			writeJSON(w, http.StatusTooManyRequests, map[string]any{
				"error":             "too many failed login attempts",
				"retryAfterSeconds": seconds,
			})
			return
		}
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid username or password"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"username": s.auth.Username()})
}

func (s *Server) authLogout(w http.ResponseWriter, r *http.Request) {
	if s.auth != nil {
		s.auth.Logout(w, r)
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "logged_out"})
}

func (s *Server) authMe(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil || !s.auth.Enabled() {
		writeJSON(w, http.StatusOK, map[string]any{"username": ""})
		return
	}
	username, ok := s.auth.User(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"username": username})
}

func (s *Server) listMonitors(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListMonitors(r.Context())
	if err == nil {
		items = s.sanitizeMonitors(items)
	}
	respond(w, r, items, err)
}

func (s *Server) createMonitor(w http.ResponseWriter, r *http.Request) {
	var input model.MonitorInput
	if !decode(w, r, &input) {
		return
	}
	if err := s.validateMonitorInput(r.Context(), input); err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.validateMonitorNaturalKey(r.Context(), input, 0); err != nil {
		writeError(w, r, err)
		return
	}
	item, err := s.store.CreateMonitor(r.Context(), input)
	if err == nil {
		id := item.ID
		s.recordAudit(r.Context(), s.actor(r), "create", "monitor", &id, "创建监控 · "+item.Name, s.sanitizeMonitor(item))
		item = s.sanitizeMonitor(item)
	}
	respondCreated(w, r, item, err)
}

func (s *Server) updateMonitor(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input model.MonitorInput
	if !decode(w, r, &input) {
		return
	}
	existing, err := s.store.GetMonitor(r.Context(), id)
	if err != nil {
		respond(w, r, model.Monitor{}, err)
		return
	}
	if existing.Type != input.Type {
		writeError(w, r, validationProblem("已创建监控的类型不可修改。", map[string]string{"type": "如需使用其他类型，请新建监控。"}))
		return
	}
	input.Config = mergeSecretConfig(existing.Config, input.Config, monitorSecretKeys(input.Type, s.scheduler.Plugins()))
	if err := s.validateMonitorInput(r.Context(), input); err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.validateMonitorNaturalKey(r.Context(), input, id); err != nil {
		writeError(w, r, err)
		return
	}
	item, err := s.store.UpdateMonitor(r.Context(), id, input)
	if err == nil {
		s.recordAudit(r.Context(), s.actor(r), "update", "monitor", &id, "修改监控 · "+item.Name, map[string]any{"before": s.sanitizeMonitor(existing), "after": s.sanitizeMonitor(item)})
		item = s.sanitizeMonitor(item)
	}
	respond(w, r, item, err)
}

func (s *Server) deleteMonitor(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	item, _ := s.store.GetMonitor(r.Context(), id)
	err := s.store.DeleteMonitor(r.Context(), id)
	if err == nil {
		s.recordAudit(r.Context(), s.actor(r), "delete", "monitor", &id, "归档监控 · "+item.Name, map[string]any{"retainedHistory": true})
	}
	respondNoContent(w, r, err)
}

func (s *Server) checkMonitor(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	err := s.scheduler.RunOnce(r.Context(), id)
	page, pageErr := s.store.ListCheckRunsPage(r.Context(), store.CheckRunFilter{
		PageRequest: store.PageRequest{Page: 1, PageSize: 1}, MonitorID: id, Trigger: "manual",
	})
	var run *model.CheckRun
	if pageErr == nil && len(page.Items) > 0 {
		run = &page.Items[0]
	}
	if err == nil {
		if pageErr != nil {
			writeError(w, r, pageErr)
			return
		}
		changes := map[string]any{}
		if run != nil {
			changes = map[string]any{"checkRunId": run.ID, "status": run.Status, "eventCount": run.EventCount}
		}
		s.recordAudit(r.Context(), s.actor(r), "check", "monitor", &id, "手动运行监控", changes)
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	eventCount := 0
	if run != nil {
		eventCount = run.EventCount
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "checked", "eventCount": eventCount, "checkRun": run})
}

func (s *Server) listRules(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListRules(r.Context())
	respond(w, r, items, err)
}

func (s *Server) createRule(w http.ResponseWriter, r *http.Request) {
	var input model.RuleInput
	if !decode(w, r, &input) {
		return
	}
	if err := s.validateRuleInput(r.Context(), input); err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.validateRuleNaturalKey(r.Context(), input, 0); err != nil {
		writeError(w, r, err)
		return
	}
	item, err := s.store.CreateRule(r.Context(), input)
	if err == nil {
		id := item.ID
		s.recordAudit(r.Context(), s.actor(r), "create", "rule", &id, "创建规则 · "+item.Name, item)
	}
	respondCreated(w, r, item, err)
}

func (s *Server) updateRule(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input model.RuleInput
	if !decode(w, r, &input) {
		return
	}
	if err := s.validateRuleInput(r.Context(), input); err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.validateRuleNaturalKey(r.Context(), input, id); err != nil {
		writeError(w, r, err)
		return
	}
	item, err := s.store.UpdateRule(r.Context(), id, input)
	if err == nil {
		s.recordAudit(r.Context(), s.actor(r), "update", "rule", &id, "修改规则 · "+item.Name, item)
	}
	respond(w, r, item, err)
}

func (s *Server) deleteRule(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	item, _ := s.store.GetRule(r.Context(), id)
	err := s.store.DeleteRule(r.Context(), id)
	if err == nil {
		s.recordAudit(r.Context(), s.actor(r), "delete", "rule", &id, "归档规则 · "+item.Name, map[string]any{"retainedHistory": true})
	}
	respondNoContent(w, r, err)
}

func (s *Server) listNotifyChannels(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListNotifyChannels(r.Context())
	if err == nil {
		items = sanitizeChannels(items)
	}
	respond(w, r, items, err)
}

func (s *Server) createNotifyChannel(w http.ResponseWriter, r *http.Request) {
	var input model.NotifyChannelInput
	if !decode(w, r, &input) {
		return
	}
	if err := validateChannelInput(input); err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.validateChannelNaturalKey(r.Context(), input, 0); err != nil {
		writeError(w, r, err)
		return
	}
	item, err := s.store.CreateNotifyChannel(r.Context(), input)
	if err == nil {
		id := item.ID
		s.recordAudit(r.Context(), s.actor(r), "create", "channel", &id, "创建渠道 · "+item.Name, sanitizeChannel(item))
		item = sanitizeChannel(item)
	}
	respondCreated(w, r, item, err)
}

func (s *Server) updateNotifyChannel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input model.NotifyChannelInput
	if !decode(w, r, &input) {
		return
	}
	existing, err := s.store.GetNotifyChannel(r.Context(), id)
	if err != nil {
		respond(w, r, model.NotifyChannel{}, err)
		return
	}
	if existing.Type == input.Type {
		input.Config = mergeSecretConfig(existing.Config, input.Config, channelSecretKeys(input.Type))
	}
	if err := validateChannelInput(input); err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.validateChannelNaturalKey(r.Context(), input, id); err != nil {
		writeError(w, r, err)
		return
	}
	item, err := s.store.UpdateNotifyChannel(r.Context(), id, input)
	if err == nil {
		s.recordAudit(r.Context(), s.actor(r), "update", "channel", &id, "修改渠道 · "+item.Name, map[string]any{"before": sanitizeChannel(existing), "after": sanitizeChannel(item)})
		item = sanitizeChannel(item)
	}
	respond(w, r, item, err)
}

func (s *Server) deleteNotifyChannel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	item, _ := s.store.GetNotifyChannel(r.Context(), id)
	err := s.store.DeleteNotifyChannel(r.Context(), id)
	if err == nil {
		s.recordAudit(r.Context(), s.actor(r), "delete", "channel", &id, "归档渠道 · "+item.Name, map[string]any{"retainedHistory": true})
	}
	respondNoContent(w, r, err)
}

func (s *Server) testNotifyChannel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	attempt, err := s.scheduler.TestChannel(r.Context(), id)
	if attempt.ID > 0 {
		s.recordAudit(r.Context(), s.actor(r), "test", "channel", &id, "测试通知渠道", map[string]any{"attemptId": attempt.ID, "status": attempt.Status})
	}
	if err != nil {
		status := http.StatusInternalServerError
		code := "internal_error"
		if store.IsNotFound(err) || errors.Is(err, sql.ErrNoRows) {
			status = http.StatusNotFound
			code = "channel_not_found"
		} else if attempt.ID > 0 {
			status = http.StatusBadGateway
			code = "channel_test_failed"
		}
		writeJSON(w, status, errorPayload(r, err, code, map[string]string{}, map[string]any{"attempt": attempt}))
		return
	}
	writeJSON(w, http.StatusOK, attempt)
}

func (s *Server) listNotificationTemplates(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListNotificationTemplates(r.Context())
	respond(w, r, items, err)
}

func (s *Server) createNotificationTemplate(w http.ResponseWriter, r *http.Request) {
	var input model.NotificationTemplateInput
	if !decode(w, r, &input) {
		return
	}
	if err := validateTemplateInput(input); err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.validateTemplateNaturalKey(r.Context(), input, 0); err != nil {
		writeError(w, r, err)
		return
	}
	item, err := s.store.CreateNotificationTemplate(r.Context(), input)
	if err == nil {
		id := item.ID
		s.recordAudit(r.Context(), s.actor(r), "create", "template", &id, "创建模板 · "+item.Name, item)
	}
	respondCreated(w, r, item, err)
}

func (s *Server) updateNotificationTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input model.NotificationTemplateInput
	if !decode(w, r, &input) {
		return
	}
	if err := validateTemplateInput(input); err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.validateTemplateNaturalKey(r.Context(), input, id); err != nil {
		writeError(w, r, err)
		return
	}
	item, err := s.store.UpdateNotificationTemplate(r.Context(), id, input)
	if err == nil {
		s.recordAudit(r.Context(), s.actor(r), "update", "template", &id, "修改模板 · "+item.Name, item)
	}
	respond(w, r, item, err)
}

func (s *Server) deleteNotificationTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	item, err := s.store.GetNotificationTemplate(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if item.IsDefault {
		writeError(w, r, &problemError{Status: http.StatusConflict, Code: "default_template", Message: "默认通知模板不能归档。", Fields: map[string]string{}})
		return
	}
	err = s.store.DeleteNotificationTemplate(r.Context(), id)
	if err == nil {
		s.recordAudit(r.Context(), s.actor(r), "delete", "template", &id, "归档模板 · "+item.Name, map[string]any{"retainedHistory": true})
	}
	respondNoContent(w, r, err)
}

func (s *Server) previewTemplate(w http.ResponseWriter, r *http.Request) {
	var input struct {
		SubjectTemplate string         `json:"subjectTemplate"`
		BodyTemplate    string         `json:"bodyTemplate"`
		Data            map[string]any `json:"data"`
		EventID         *int64         `json:"eventId"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.EventID != nil {
		if *input.EventID <= 0 {
			writeError(w, r, validationProblem("预览事件无效。", map[string]string{"eventId": "必须是正整数。"}))
			return
		}
		event, err := s.store.GetEvent(r.Context(), *input.EventID)
		if err != nil {
			writeError(w, r, err)
			return
		}
		monitor, err := s.store.GetMonitorIncludingArchived(r.Context(), event.MonitorID)
		if err != nil {
			writeError(w, r, err)
			return
		}
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			writeError(w, r, err)
			return
		}
		input.Data = templatePreviewData(monitor, event, payload)
	} else if input.Data == nil {
		input.Data = sampleTemplateData()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"subject": templatex.Render(input.SubjectTemplate, input.Data),
		"body":    templatex.Render(input.BodyTemplate, input.Data),
	})
}

func templatePreviewData(monitor model.Monitor, event model.Event, payload map[string]any) map[string]any {
	data := eventvars.EventData(monitor, event, payload)
	data["rule"] = map[string]any{"id": int64(0), "name": "模板预览", "matched": []string{}}
	return data
}

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) {
	if wantsHistoryPage(r) {
		filter, err := eventFilter(r)
		if err != nil {
			writeError(w, r, err)
			return
		}
		items, err := s.store.ListEventsPage(r.Context(), filter)
		respond(w, r, items, err)
		return
	}
	items, err := s.store.ListEvents(r.Context(), queryLimit(r, 100))
	respond(w, r, items, err)
}

func (s *Server) listNotificationLogs(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListNotificationLogs(r.Context(), queryLimit(r, 100))
	respond(w, r, items, err)
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	summary, err := s.store.DashboardSummary(r.Context())
	respond(w, r, summary, err)
}

func (s *Server) listCheckRuns(w http.ResponseWriter, r *http.Request) {
	if wantsHistoryPage(r) {
		filter, err := checkRunFilter(r)
		if err != nil {
			writeError(w, r, err)
			return
		}
		items, err := s.store.ListCheckRunsPage(r.Context(), filter)
		respond(w, r, items, err)
		return
	}
	items, err := s.store.ListCheckRuns(r.Context(), queryLimit(r, 100))
	respond(w, r, items, err)
}

func (s *Server) listRuleEvaluations(w http.ResponseWriter, r *http.Request) {
	if wantsHistoryPage(r) {
		filter, err := ruleEvaluationFilter(r)
		if err != nil {
			writeError(w, r, err)
			return
		}
		items, err := s.store.ListRuleEvaluationsPage(r.Context(), filter)
		respond(w, r, items, err)
		return
	}
	items, err := s.store.ListRuleEvaluations(r.Context(), queryLimit(r, 100))
	respond(w, r, items, err)
}

func (s *Server) listNotificationAttempts(w http.ResponseWriter, r *http.Request) {
	if wantsHistoryPage(r) {
		filter, err := notificationAttemptFilter(r)
		if err != nil {
			writeError(w, r, err)
			return
		}
		items, err := s.store.ListNotificationAttemptsPage(r.Context(), filter)
		respond(w, r, items, err)
		return
	}
	items, err := s.store.ListNotificationAttempts(r.Context(), queryLimit(r, 100))
	respond(w, r, items, err)
}

func (s *Server) retryNotificationAttempt(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	attempt, err := s.scheduler.RetryAttempt(r.Context(), id)
	if attempt.ID > 0 {
		s.recordAudit(r.Context(), s.actor(r), "retry", "notification_attempt", &id, "重试失败通知", map[string]any{"newAttemptId": attempt.ID, "status": attempt.Status})
	}
	if err != nil {
		status := http.StatusInternalServerError
		code := "internal_error"
		switch {
		case store.IsNotFound(err), errors.Is(err, sql.ErrNoRows):
			status = http.StatusNotFound
			code = "notification_attempt_not_found"
		case errors.Is(err, scheduler.ErrRetryNotFailed):
			status = http.StatusUnprocessableEntity
			code = "notification_retry_not_failed"
		case errors.Is(err, scheduler.ErrRetryConflict):
			status = http.StatusConflict
			code = "notification_retry_conflict"
		case errors.Is(err, scheduler.ErrRetryTargetUnavailable):
			status = http.StatusConflict
			code = "notification_retry_target_unavailable"
		case attempt.ID > 0:
			status = http.StatusBadGateway
			code = "notification_retry_failed"
		}
		writeJSON(w, status, errorPayload(r, err, code, map[string]string{}, map[string]any{"attempt": attempt}))
		return
	}
	writeJSON(w, http.StatusOK, attempt)
}

func (s *Server) listDeadLetters(w http.ResponseWriter, r *http.Request) {
	page, err := historyPageRequest(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	monitorID, err := optionalQueryID(r, "monitorId")
	if err != nil {
		writeError(w, r, err)
		return
	}
	items, err := s.store.ListDeadLettersPage(r.Context(), store.DeadLetterFilter{PageRequest: page, MonitorID: monitorID})
	respond(w, r, items, err)
}

func (s *Server) retryDeadLetter(w http.ResponseWriter, r *http.Request) {
	eventID, ok := pathID(w, r)
	if !ok {
		return
	}
	event, err := s.store.GetEvent(r.Context(), eventID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if _, err := s.store.GetMonitor(r.Context(), event.MonitorID); err != nil {
		writeError(w, r, &problemError{
			Status:  http.StatusConflict,
			Code:    "monitor_archived",
			Message: "该事件所属监控已归档，不能重新处理。",
			Fields:  map[string]string{},
		})
		return
	}
	if err := s.store.RequeueDeadLetter(r.Context(), eventID, time.Now().UTC()); err != nil {
		writeError(w, r, err)
		return
	}
	s.recordAudit(r.Context(), s.actor(r), "retry", "dead_letter", &eventID, "重新入队死信事件", map[string]any{"eventId": eventID})
	writeJSON(w, http.StatusOK, map[string]any{"status": "queued", "eventId": eventID})
}

func (s *Server) listAuditLogs(w http.ResponseWriter, r *http.Request) {
	if wantsHistoryPage(r) {
		filter, err := auditLogFilter(r)
		if err != nil {
			writeError(w, r, err)
			return
		}
		items, err := s.store.ListAuditLogsPage(r.Context(), filter)
		respond(w, r, items, err)
		return
	}
	items, err := s.store.ListAuditLogs(r.Context(), queryLimit(r, 100))
	respond(w, r, items, err)
}

func (s *Server) systemStatus(w http.ResponseWriter, r *http.Request) {
	database := "ok"
	if err := s.store.Ping(r.Context()); err != nil {
		database = "error"
	}
	outbox, outboxErr := s.store.OutboxStatusCounts(r.Context())
	if outboxErr != nil {
		writeError(w, r, outboxErr)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"database":  database,
		"scheduler": s.scheduler.Health(),
		"outbox":    outbox,
		"time":      time.Now().UTC(),
	})
}

func (s *Server) diagnostics(w http.ResponseWriter, r *http.Request) {
	counts, err := s.store.DebugCounts(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	runs, _ := s.store.ListCheckRuns(r.Context(), 20)
	attempts, _ := s.store.ListNotificationAttempts(r.Context(), 20)
	deadLetters, _ := s.store.ListDeadLettersPage(r.Context(), store.DeadLetterFilter{PageRequest: store.PageRequest{Page: 1, PageSize: 20}})
	writeJSON(w, http.StatusOK, map[string]any{
		"generatedAt":                time.Now().UTC(),
		"scheduler":                  s.scheduler.Health(),
		"counts":                     counts,
		"recentCheckRuns":            runs,
		"recentNotificationAttempts": attempts,
		"recentDeadLetters":          deadLetters.Items,
	})
}

func (s *Server) actor(r *http.Request) string {
	if s.auth != nil && s.auth.Enabled() {
		if username, ok := s.auth.User(r); ok {
			return username
		}
	}
	return "local"
}

func (s *Server) recordAudit(ctx context.Context, actor, action, entityType string, entityID *int64, summary string, changes any) {
	if err := s.store.CreateAuditLog(ctx, actor, action, entityType, entityID, summary, changes); err != nil {
		s.logger.Error("audit log write failed", "action", action, "entity_type", entityType, "entity_id", entityID, "error", err)
	}
}

func (s *Server) serveSPA(w http.ResponseWriter, r *http.Request) {
	if s.webDir == "" {
		http.NotFound(w, r)
		return
	}
	clean := filepath.Clean("/" + r.URL.Path)
	if clean == "/" {
		clean = "/index.html"
	}
	target := filepath.Join(s.webDir, strings.TrimPrefix(clean, "/"))
	if stat, err := os.Stat(target); err == nil && !stat.IsDir() {
		http.ServeFile(w, r, target)
		return
	}
	index := filepath.Join(s.webDir, "index.html")
	if _, err := os.Stat(index); err == nil {
		http.ServeFile(w, r, index)
		return
	}
	writeJSON(w, http.StatusNotFound, map[string]any{"error": "frontend is not built"})
}

func respond(w http.ResponseWriter, r *http.Request, value any, err error) {
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func respondCreated(w http.ResponseWriter, r *http.Request, value any, err error) {
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, value)
}

func respondNoContent(w http.ResponseWriter, r *http.Request, err error) {
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func decode(w http.ResponseWriter, r *http.Request, value any) bool {
	defer r.Body.Close()
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, errorPayload(r,
			errors.New("Content-Type must be application/json"),
			"unsupported_media_type", map[string]string{}, nil,
		))
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		writeJSON(w, http.StatusBadRequest, errorPayload(r, err, "invalid_json", map[string]string{}, nil))
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("request body must contain exactly one JSON value")
		}
		writeJSON(w, http.StatusBadRequest, errorPayload(r, err, "invalid_json", map[string]string{}, nil))
		return false
	}
	return true
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, errorPayload(r, errors.New("invalid id"), "invalid_id", map[string]string{"id": "ID must be a positive integer."}, nil))
		return 0, false
	}
	return id, true
}

func queryLimit(r *http.Request, fallback int) int {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func writeError(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusInternalServerError
	code := "internal_error"
	fields := map[string]string{}
	var problem *problemError
	if errors.As(err, &problem) {
		status = problem.Status
		code = problem.Code
		fields = problem.Fields
	}
	if errors.Is(err, sql.ErrNoRows) || store.IsNotFound(err) {
		status = http.StatusNotFound
		code = "not_found"
	}
	if errors.Is(err, scheduler.ErrAlreadyRunning) {
		status = http.StatusConflict
		code = "already_running"
	}
	if errors.Is(err, store.ErrDuplicateNaturalKey) {
		status = http.StatusConflict
		code = "duplicate_natural_key"
	}
	if errors.Is(err, store.ErrProxyUnavailable) {
		status = http.StatusUnprocessableEntity
		code = "validation_failed"
		fields = map[string]string{"proxyId": "所选代理已不存在或已被归档，请重新选择。"}
	}
	writeJSON(w, status, errorPayload(r, err, code, fields, nil))
}

func errorPayload(r *http.Request, err error, code string, fields map[string]string, extra map[string]any) map[string]any {
	payload := map[string]any{
		"error":     err.Error(),
		"code":      code,
		"requestId": middleware.GetReqID(r.Context()),
	}
	if len(fields) > 0 {
		payload["fields"] = fields
	}
	for key, value := range extra {
		payload[key] = value
	}
	return payload
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func sampleTemplateData() map[string]any {
	return map[string]any{
		"url":         "https://example.com/item",
		"title":       "Example Event",
		"summary":     "Example summary",
		"content":     "Example content",
		"author":      "example",
		"publishedAt": "2026-07-02T00:00:00Z",
		"status":      "published",
		"monitor":     map[string]any{"name": "Example Monitor", "type": "rss"},
		"rule":        map[string]any{"name": "Keyword Rule", "matched": []string{"keyword"}},
		"event":       map[string]any{"type": "rss.item", "time": "2026-07-02T00:00:00Z"},
		"rss":         map[string]any{"title": "Example RSS Item", "link": "https://example.com/item", "summary": "Example summary"},
		"testflight": map[string]any{
			"url":     "https://testflight.apple.com/join/example",
			"status":  "available",
			"message": "testflight beta has available slots",
		},
		"webpage": map[string]any{"url": "https://example.com", "summary": "changed content"},
		"github": map[string]any{
			"owner": "example", "repo": "project", "repository": "example/project",
			"release": map[string]any{
				"id": 42, "tagName": "v1.2.3", "name": "Version 1.2.3",
				"body": "Release notes", "url": "https://github.com/example/project/releases/tag/v1.2.3",
				"prerelease": false, "publishedAt": "2026-07-02T00:00:00Z", "author": "example",
			},
		},
	}
}
