package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/watchbell/watchbell/internal/auth"
	"github.com/watchbell/watchbell/internal/checker"
	"github.com/watchbell/watchbell/internal/model"
	"github.com/watchbell/watchbell/internal/notifier"
	"github.com/watchbell/watchbell/internal/scheduler"
	"github.com/watchbell/watchbell/internal/store"
)

func TestProxySettingsCRUDAndMonitorReferenceValidation(t *testing.T) {
	server, db := newTestServer(t)
	createBody := []byte(`{"name":"Outbound","type":"http","host":"127.0.0.1","port":8080,"username":"proxy-user","password":"proxy-secret"}`)
	response, err := http.Post(server.URL+"/api/settings/proxies", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatal(err)
	}
	createdBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusCreated || bytes.Contains(createdBody, []byte("proxy-secret")) || !bytes.Contains(createdBody, []byte(`"configuredSecrets":["password"]`)) {
		t.Fatalf("create proxy status=%d body=%s", response.StatusCode, createdBody)
	}
	var created model.ProxyProfile
	if err := json.Unmarshal(createdBody, &created); err != nil {
		t.Fatal(err)
	}

	updateBody := []byte(`{"name":"Outbound","type":"http","host":"proxy.internal","port":3128,"username":"proxy-user","password":""}`)
	request, _ := http.NewRequest(http.MethodPut, server.URL+"/api/settings/proxies/"+jsonNumber(created.ID), bytes.NewReader(updateBody))
	request.Header.Set("Content-Type", "application/json")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	stored, err := db.GetProxyProfile(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || stored.Password != "proxy-secret" || stored.Host != "proxy.internal" {
		t.Fatalf("updated proxy status=%d stored=%#v", response.StatusCode, stored)
	}

	monitorBody := []byte(`{"name":"Proxied feed","type":"rss","proxyId":` + jsonNumber(created.ID) + `,"enabled":false,"intervalSeconds":300,"config":{"url":"https://example.com/feed.xml"},"failureAlertAfter":0,"failureNotifyChannelIds":[]}`)
	response, err = http.Post(server.URL+"/api/monitors", "application/json", bytes.NewReader(monitorBody))
	if err != nil {
		t.Fatal(err)
	}
	var monitor model.Monitor
	if err := json.NewDecoder(response.Body).Decode(&monitor); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusCreated || monitor.ProxyID == nil || *monitor.ProxyID != created.ID {
		t.Fatalf("proxied monitor status=%d monitor=%#v", response.StatusCode, monitor)
	}

	request, _ = http.NewRequest(http.MethodDelete, server.URL+"/api/settings/proxies/"+jsonNumber(created.ID), nil)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var conflict struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(response.Body).Decode(&conflict); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusConflict || conflict.Code != "proxy_in_use" {
		t.Fatalf("delete referenced proxy status/code=%d/%q", response.StatusCode, conflict.Code)
	}

	invalidMonitor := []byte(`{"name":"Missing proxy","type":"rss","proxyId":999999,"enabled":false,"intervalSeconds":300,"config":{"url":"https://example.com/feed.xml"},"failureAlertAfter":0,"failureNotifyChannelIds":[]}`)
	response, err = http.Post(server.URL+"/api/monitors", "application/json", bytes.NewReader(invalidMonitor))
	if err != nil {
		t.Fatal(err)
	}
	var invalid struct {
		Fields map[string]string `json:"fields"`
	}
	if err := json.NewDecoder(response.Body).Decode(&invalid); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnprocessableEntity || invalid.Fields["proxyId"] == "" {
		t.Fatalf("invalid proxy reference status=%d fields=%#v", response.StatusCode, invalid.Fields)
	}
}

func TestPasswordChangePersistsAndInvalidatesPreviousSession(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir()+"/watchbell.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	const initialHash = "pbkdf2-sha256$210000$wJ7uwPXRx3I5W-CYFTWCqw$ugxmCBayTf_gzUkDj1St3hd8dC5iUedtf98HzjUcbKE"
	manager, err := auth.NewManager(auth.Config{
		Enabled: true, Username: "admin", PasswordHash: initialHash, SessionSecret: "01234567890123456789012345678901",
	}, logger)
	if err != nil {
		t.Fatal(err)
	}
	loginRecorder := httptest.NewRecorder()
	loginRequest := httptest.NewRequest(http.MethodPost, "http://watchbell.test/api/auth/login", nil)
	if err := manager.Login(loginRecorder, loginRequest, "admin", "correct-password"); err != nil {
		t.Fatal(err)
	}
	oldCookie := loginRecorder.Result().Cookies()[0]
	sched := scheduler.New(db, checker.NewRegistry(), notifier.NewRegistry(), scheduler.Options{})
	server := httptest.NewServer(NewServer(db, sched, "", logger, manager).Routes())
	t.Cleanup(server.Close)

	body := []byte(`{"currentPassword":"correct-password","newPassword":"new-password-123","confirmPassword":"new-password-123"}`)
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/settings/password", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(oldCookie)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || len(response.Cookies()) != 1 {
		t.Fatalf("password change status=%d cookies=%#v", response.StatusCode, response.Cookies())
	}
	newCookie := response.Cookies()[0]

	request, _ = http.NewRequest(http.MethodGet, server.URL+"/api/auth/me", nil)
	request.AddCookie(oldCookie)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old session status=%d, want 401", response.StatusCode)
	}
	request, _ = http.NewRequest(http.MethodGet, server.URL+"/api/auth/me", nil)
	request.AddCookie(newCookie)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("refreshed session status=%d", response.StatusCode)
	}

	persisted, exists, err := db.GetAuthPasswordHash(ctx)
	if err != nil || !exists || !auth.VerifyPassword(persisted, "new-password-123") || auth.VerifyPassword(persisted, "correct-password") {
		t.Fatalf("persisted password exists=%v err=%v hash valid=%v", exists, err, auth.VerifyPassword(persisted, "new-password-123"))
	}
	logs, err := db.ListAuditLogs(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 || logs[0].EntityType != "account" || strings.Contains(string(logs[0].Changes), persisted) {
		t.Fatalf("password audit leaked or missing: %#v", logs)
	}
}

func TestPasswordChangeCurrentCredentialIsRateLimited(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir()+"/watchbell.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager, err := auth.NewManager(auth.Config{
		Enabled: true, Username: "admin", PasswordHash: "pbkdf2-sha256$210000$wJ7uwPXRx3I5W-CYFTWCqw$ugxmCBayTf_gzUkDj1St3hd8dC5iUedtf98HzjUcbKE",
		SessionSecret: "01234567890123456789012345678901", LoginMaxFailures: 2, LoginFailureWindow: time.Minute,
	}, logger)
	if err != nil {
		t.Fatal(err)
	}
	loginRecorder := httptest.NewRecorder()
	if err := manager.Login(loginRecorder, httptest.NewRequest(http.MethodPost, "http://watchbell.test/api/auth/login", nil), "admin", "correct-password"); err != nil {
		t.Fatal(err)
	}
	cookie := loginRecorder.Result().Cookies()[0]
	sched := scheduler.New(db, checker.NewRegistry(), notifier.NewRegistry(), scheduler.Options{})
	server := httptest.NewServer(NewServer(db, sched, "", logger, manager).Routes())
	t.Cleanup(server.Close)

	for attempt := 1; attempt <= 3; attempt++ {
		body := []byte(`{"currentPassword":"wrong-password","newPassword":"new-password-123","confirmPassword":"new-password-123"}`)
		request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/settings/password", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.AddCookie(cookie)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		want := http.StatusUnprocessableEntity
		if attempt == 3 {
			want = http.StatusTooManyRequests
			if response.Header.Get("Retry-After") == "" {
				t.Fatal("rate-limited password change omitted Retry-After")
			}
		}
		if response.StatusCode != want {
			t.Fatalf("attempt %d status = %d, want %d", attempt, response.StatusCode, want)
		}
	}
}

func jsonNumber(value int64) string {
	return strconv.FormatInt(value, 10)
}
