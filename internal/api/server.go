package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

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
	r.Use(middleware.Recoverer)

	r.Route("/api", func(r chi.Router) {
		r.Get("/health", s.health)
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
	r.Get("/notification-logs", s.listNotificationLogs)
}

func (s *Server) listPlugins(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.scheduler.Plugins())
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
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
	respond(w, items, err)
}

func (s *Server) createMonitor(w http.ResponseWriter, r *http.Request) {
	var input model.MonitorInput
	if !decode(w, r, &input) {
		return
	}
	if !s.validateMonitorInput(w, input) {
		return
	}
	item, err := s.store.CreateMonitor(r.Context(), input)
	respondCreated(w, item, err)
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
	if !s.validateMonitorInput(w, input) {
		return
	}
	item, err := s.store.UpdateMonitor(r.Context(), id, input)
	respond(w, item, err)
}

func (s *Server) validateMonitorInput(w http.ResponseWriter, input model.MonitorInput) bool {
	if strings.TrimSpace(input.Name) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "monitor name is required"})
		return false
	}
	if !s.scheduler.HasPlugin(input.Type) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": fmt.Sprintf("unsupported monitor plugin %q", input.Type)})
		return false
	}
	return true
}

func (s *Server) deleteMonitor(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	respondNoContent(w, s.store.DeleteMonitor(r.Context(), id))
}

func (s *Server) checkMonitor(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	err := s.scheduler.RunOnce(r.Context(), id)
	respond(w, map[string]any{"status": "checked"}, err)
}

func (s *Server) listRules(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListRules(r.Context())
	respond(w, items, err)
}

func (s *Server) createRule(w http.ResponseWriter, r *http.Request) {
	var input model.RuleInput
	if !decode(w, r, &input) {
		return
	}
	item, err := s.store.CreateRule(r.Context(), input)
	respondCreated(w, item, err)
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
	item, err := s.store.UpdateRule(r.Context(), id, input)
	respond(w, item, err)
}

func (s *Server) deleteRule(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	respondNoContent(w, s.store.DeleteRule(r.Context(), id))
}

func (s *Server) listNotifyChannels(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListNotifyChannels(r.Context())
	respond(w, items, err)
}

func (s *Server) createNotifyChannel(w http.ResponseWriter, r *http.Request) {
	var input model.NotifyChannelInput
	if !decode(w, r, &input) {
		return
	}
	item, err := s.store.CreateNotifyChannel(r.Context(), input)
	respondCreated(w, item, err)
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
	item, err := s.store.UpdateNotifyChannel(r.Context(), id, input)
	respond(w, item, err)
}

func (s *Server) deleteNotifyChannel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	respondNoContent(w, s.store.DeleteNotifyChannel(r.Context(), id))
}

func (s *Server) testNotifyChannel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	err := s.scheduler.TestChannel(r.Context(), id)
	respond(w, map[string]any{"status": "sent"}, err)
}

func (s *Server) listNotificationTemplates(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListNotificationTemplates(r.Context())
	respond(w, items, err)
}

func (s *Server) createNotificationTemplate(w http.ResponseWriter, r *http.Request) {
	var input model.NotificationTemplateInput
	if !decode(w, r, &input) {
		return
	}
	item, err := s.store.CreateNotificationTemplate(r.Context(), input)
	respondCreated(w, item, err)
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
	item, err := s.store.UpdateNotificationTemplate(r.Context(), id, input)
	respond(w, item, err)
}

func (s *Server) deleteNotificationTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	respondNoContent(w, s.store.DeleteNotificationTemplate(r.Context(), id))
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
	respond(w, items, err)
}

func (s *Server) listNotificationLogs(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListNotificationLogs(r.Context(), queryLimit(r, 100))
	respond(w, items, err)
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

func respond(w http.ResponseWriter, value any, err error) {
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func respondCreated(w http.ResponseWriter, value any, err error) {
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, value)
}

func respondNoContent(w http.ResponseWriter, err error) {
	if err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func decode(w http.ResponseWriter, r *http.Request, value any) bool {
	defer r.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return false
	}
	return true
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid id"})
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

func writeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	if errors.Is(err, sql.ErrNoRows) || store.IsNotFound(err) {
		status = http.StatusNotFound
	}
	writeJSON(w, status, map[string]any{"error": err.Error()})
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
