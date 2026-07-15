package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/watchbell/watchbell/internal/auth"
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
	r.Use(middleware.RealIP)
	r.Use(s.accessLog)
	r.Use(middleware.Recoverer)

	r.Route("/api", func(r chi.Router) {
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
	r.Get("/plugins", s.listPlugins)
	r.Get("/dashboard", s.dashboard)
	r.Get("/system/status", s.systemStatus)
	r.Get("/diagnostics", s.diagnostics)

	r.Get("/monitors", s.listMonitors)
	r.Post("/monitors", s.createMonitor)
	r.Put("/monitors/{id}", s.updateMonitor)
	r.Delete("/monitors/{id}", s.deleteMonitor)
	r.Post("/monitors/{id}/check", s.checkMonitor)

	r.Get("/rules", s.listRules)
	r.Post("/rules", s.createRule)
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
	r.Get("/check-runs", s.listCheckRuns)
	r.Get("/rule-evaluations", s.listRuleEvaluations)
	r.Get("/notification-attempts", s.listNotificationAttempts)
	r.Post("/notification-attempts/{id}/retry", s.retryNotificationAttempt)
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
	if err := s.validateMonitorInput(input); err != nil {
		writeError(w, r, err)
		return
	}
	item, err := s.store.CreateMonitor(r.Context(), input)
	if err == nil {
		id := item.ID
		_ = s.store.CreateAuditLog(r.Context(), s.actor(r), "create", "monitor", &id, "创建监控 · "+item.Name, s.sanitizeMonitor(item))
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
	if existing.Type == input.Type {
		input.Config = mergeSecretConfig(existing.Config, input.Config, monitorSecretKeys(input.Type, s.scheduler.Plugins()))
	}
	if err := s.validateMonitorInput(input); err != nil {
		writeError(w, r, err)
		return
	}
	item, err := s.store.UpdateMonitor(r.Context(), id, input)
	if err == nil {
		_ = s.store.CreateAuditLog(r.Context(), s.actor(r), "update", "monitor", &id, "修改监控 · "+item.Name, map[string]any{"before": s.sanitizeMonitor(existing), "after": s.sanitizeMonitor(item)})
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
		_ = s.store.CreateAuditLog(r.Context(), s.actor(r), "delete", "monitor", &id, "归档监控 · "+item.Name, map[string]any{"retainedHistory": true})
	}
	respondNoContent(w, r, err)
}

func (s *Server) checkMonitor(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	err := s.scheduler.RunOnce(r.Context(), id)
	if err == nil {
		_ = s.store.CreateAuditLog(r.Context(), s.actor(r), "check", "monitor", &id, "手动运行监控", map[string]any{})
	}
	respond(w, r, map[string]any{"status": "checked"}, err)
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
	item, err := s.store.CreateRule(r.Context(), input)
	if err == nil {
		id := item.ID
		_ = s.store.CreateAuditLog(r.Context(), s.actor(r), "create", "rule", &id, "创建规则 · "+item.Name, item)
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
	item, err := s.store.UpdateRule(r.Context(), id, input)
	if err == nil {
		_ = s.store.CreateAuditLog(r.Context(), s.actor(r), "update", "rule", &id, "修改规则 · "+item.Name, item)
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
		_ = s.store.CreateAuditLog(r.Context(), s.actor(r), "delete", "rule", &id, "归档规则 · "+item.Name, map[string]any{"retainedHistory": true})
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
	item, err := s.store.CreateNotifyChannel(r.Context(), input)
	if err == nil {
		id := item.ID
		_ = s.store.CreateAuditLog(r.Context(), s.actor(r), "create", "channel", &id, "创建渠道 · "+item.Name, sanitizeChannel(item))
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
	item, err := s.store.UpdateNotifyChannel(r.Context(), id, input)
	if err == nil {
		_ = s.store.CreateAuditLog(r.Context(), s.actor(r), "update", "channel", &id, "修改渠道 · "+item.Name, map[string]any{"before": sanitizeChannel(existing), "after": sanitizeChannel(item)})
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
		_ = s.store.CreateAuditLog(r.Context(), s.actor(r), "delete", "channel", &id, "归档渠道 · "+item.Name, map[string]any{"retainedHistory": true})
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
		_ = s.store.CreateAuditLog(r.Context(), s.actor(r), "test", "channel", &id, "测试通知渠道", map[string]any{"attemptId": attempt.ID, "status": attempt.Status})
	}
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errorPayload(r, err, "channel_test_failed", map[string]string{}, map[string]any{"attempt": attempt}))
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
	item, err := s.store.CreateNotificationTemplate(r.Context(), input)
	if err == nil {
		id := item.ID
		_ = s.store.CreateAuditLog(r.Context(), s.actor(r), "create", "template", &id, "创建模板 · "+item.Name, item)
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
	item, err := s.store.UpdateNotificationTemplate(r.Context(), id, input)
	if err == nil {
		_ = s.store.CreateAuditLog(r.Context(), s.actor(r), "update", "template", &id, "修改模板 · "+item.Name, item)
	}
	respond(w, r, item, err)
}

func (s *Server) deleteNotificationTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	item, _ := s.store.GetNotificationTemplate(r.Context(), id)
	err := s.store.DeleteNotificationTemplate(r.Context(), id)
	if err == nil {
		_ = s.store.CreateAuditLog(r.Context(), s.actor(r), "delete", "template", &id, "归档模板 · "+item.Name, map[string]any{"retainedHistory": true})
	}
	respondNoContent(w, r, err)
}

func (s *Server) previewTemplate(w http.ResponseWriter, r *http.Request) {
	var input struct {
		SubjectTemplate string         `json:"subjectTemplate"`
		BodyTemplate    string         `json:"bodyTemplate"`
		Data            map[string]any `json:"data"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.Data == nil {
		input.Data = sampleTemplateData()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"subject": templatex.Render(input.SubjectTemplate, input.Data),
		"body":    templatex.Render(input.BodyTemplate, input.Data),
	})
}

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) {
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
	items, err := s.store.ListCheckRuns(r.Context(), queryLimit(r, 100))
	respond(w, r, items, err)
}

func (s *Server) listRuleEvaluations(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListRuleEvaluations(r.Context(), queryLimit(r, 100))
	respond(w, r, items, err)
}

func (s *Server) listNotificationAttempts(w http.ResponseWriter, r *http.Request) {
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
		_ = s.store.CreateAuditLog(r.Context(), s.actor(r), "retry", "notification_attempt", &id, "重试失败通知", map[string]any{"newAttemptId": attempt.ID, "status": attempt.Status})
	}
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errorPayload(r, err, "notification_retry_failed", map[string]string{}, map[string]any{"attempt": attempt}))
		return
	}
	writeJSON(w, http.StatusOK, attempt)
}

func (s *Server) listAuditLogs(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListAuditLogs(r.Context(), queryLimit(r, 100))
	respond(w, r, items, err)
}

func (s *Server) systemStatus(w http.ResponseWriter, r *http.Request) {
	database := "ok"
	if err := s.store.Ping(r.Context()); err != nil {
		database = "error"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"database":  database,
		"scheduler": s.scheduler.Health(),
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
	writeJSON(w, http.StatusOK, map[string]any{
		"generatedAt":                time.Now().UTC(),
		"scheduler":                  s.scheduler.Health(),
		"counts":                     counts,
		"recentCheckRuns":            runs,
		"recentNotificationAttempts": attempts,
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
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
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
		"monitor": map[string]any{"name": "Example Monitor", "type": "rss"},
		"rule":    map[string]any{"name": "Keyword Rule", "matched": []string{"keyword"}},
		"event":   map[string]any{"type": "rss.item", "time": "2026-07-02T00:00:00Z"},
		"rss":     map[string]any{"title": "Example RSS Item", "link": "https://example.com/item", "summary": "Example summary"},
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
