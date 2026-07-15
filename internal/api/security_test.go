package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/watchbell/watchbell/internal/auth"
	"github.com/watchbell/watchbell/internal/checker"
	"github.com/watchbell/watchbell/internal/model"
	"github.com/watchbell/watchbell/internal/notifier"
	"github.com/watchbell/watchbell/internal/scheduler"
	"github.com/watchbell/watchbell/internal/store"
)

func TestDecodeRequiresJSONAndExactlyOneValue(t *testing.T) {
	server, db := newTestServer(t)
	valid := `{"name":"Feed","type":"rss","enabled":true,"intervalSeconds":300,"config":{"url":"https://example.com/feed.xml"}}`
	tests := []struct {
		name        string
		contentType string
		body        string
		wantStatus  int
		wantCode    string
	}{
		{name: "plain text form payload", contentType: "text/plain", body: valid + "=", wantStatus: http.StatusUnsupportedMediaType, wantCode: "unsupported_media_type"},
		{name: "trailing content", contentType: "application/json", body: valid + "=", wantStatus: http.StatusBadRequest, wantCode: "invalid_json"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, err := http.NewRequest(http.MethodPost, server.URL+"/api/monitors", strings.NewReader(test.body))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Content-Type", test.contentType)
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			var payload struct {
				Code string `json:"code"`
			}
			if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != test.wantStatus || payload.Code != test.wantCode {
				t.Fatalf("status/code = %d/%q, want %d/%q", response.StatusCode, payload.Code, test.wantStatus, test.wantCode)
			}
		})
	}
	items, err := db.ListMonitors(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("rejected requests created %d monitors", len(items))
	}
}

func TestCookieAPIRejectsCrossOriginEmptyBodyMutation(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir()+"/watchbell.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	monitor, err := db.CreateMonitor(ctx, model.MonitorInput{
		Name: "Feed", Type: model.MonitorTypeRSS, Enabled: true, IntervalSeconds: 300,
		Config: json.RawMessage(`{"url":"https://example.com/feed.xml"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager, err := auth.NewManager(auth.Config{
		Enabled:       true,
		Username:      "admin",
		PasswordHash:  "pbkdf2-sha256$210000$wJ7uwPXRx3I5W-CYFTWCqw$ugxmCBayTf_gzUkDj1St3hd8dC5iUedtf98HzjUcbKE",
		SessionSecret: "01234567890123456789012345678901",
	}, logger)
	if err != nil {
		t.Fatal(err)
	}
	loginResponse := httptest.NewRecorder()
	loginRequest := httptest.NewRequest(http.MethodPost, "http://watchbell.test/api/auth/login", nil)
	if err := manager.Login(loginResponse, loginRequest, "admin", "correct-password"); err != nil {
		t.Fatal(err)
	}
	cookies := loginResponse.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("login cookies = %#v", cookies)
	}
	sched := scheduler.New(db, checker.NewRegistry(checker.NewRSSChecker()), notifier.NewRegistry(), scheduler.Options{})
	server := httptest.NewServer(NewServer(db, sched, "", logger, manager).Routes())
	t.Cleanup(server.Close)

	crossOrigin, err := http.NewRequest(http.MethodPost, server.URL+"/api/monitors/"+strconv.FormatInt(monitor.ID, 10)+"/check", nil)
	if err != nil {
		t.Fatal(err)
	}
	crossOrigin.AddCookie(cookies[0])
	crossOrigin.Header.Set("Content-Type", "application/json")
	crossOrigin.Header.Set("Origin", "http://attacker.example")
	crossOrigin.Header.Set("Sec-Fetch-Site", "same-site")
	response, err := http.DefaultClient.Do(crossOrigin)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var payload struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusForbidden || payload.Code != "csrf_rejected" {
		t.Fatalf("cross-origin status/code = %d/%q", response.StatusCode, payload.Code)
	}

	sameOrigin, err := http.NewRequest(http.MethodDelete, server.URL+"/api/monitors/"+strconv.FormatInt(monitor.ID, 10), nil)
	if err != nil {
		t.Fatal(err)
	}
	sameOrigin.AddCookie(cookies[0])
	sameOrigin.Header.Set("Content-Type", "application/json")
	sameOrigin.Header.Set("Origin", server.URL)
	sameOrigin.Header.Set("Sec-Fetch-Site", "same-origin")
	response, err = http.DefaultClient.Do(sameOrigin)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("same-origin mutation status = %d", response.StatusCode)
	}
}
