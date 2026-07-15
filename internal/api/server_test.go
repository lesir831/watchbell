package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/watchbell/watchbell/internal/checker"
	"github.com/watchbell/watchbell/internal/notifier"
	"github.com/watchbell/watchbell/internal/scheduler"
	"github.com/watchbell/watchbell/internal/store"
)

func newTestServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir()+"/watchbell.db")
	if err != nil {
		t.Fatal(err)
	}
	sched := scheduler.New(db, checker.NewRegistry(
		checker.NewRSSChecker(), checker.NewTestFlightChecker(), checker.NewWebpageChecker(), checker.NewGitHubReleaseChecker(),
	), notifier.NewRegistry(notifier.NewBarkNotifier(), notifier.NewEmailNotifier()), scheduler.Options{})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := httptest.NewServer(NewServer(db, sched, "", logger, nil).Routes())
	t.Cleanup(func() { server.Close(); db.Close() })
	return server, db
}

func TestValidationReturnsFieldErrorsAndRequestID(t *testing.T) {
	server, _ := newTestServer(t)
	body := []byte(`{"name":"Broken","type":"github_release","enabled":true,"intervalSeconds":30,"config":{"repository":"broken","apiUrl":"https://api.github.com","timeoutSeconds":15,"maxReleases":20}}`)
	response, err := http.Post(server.URL+"/api/monitors", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 422 {
		t.Fatalf("status = %d", response.StatusCode)
	}
	var payload struct {
		Code      string            `json:"code"`
		RequestID string            `json:"requestId"`
		Fields    map[string]string `json:"fields"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Code != "validation_failed" || payload.RequestID == "" || payload.Fields["config.repository"] == "" {
		t.Fatalf("unexpected problem response: %#v", payload)
	}
	if response.Header.Get("X-Request-ID") != payload.RequestID {
		t.Fatal("request id must be available in both header and body")
	}
}

func TestChannelSecretsAreNotReturnedByAPI(t *testing.T) {
	server, db := newTestServer(t)
	body := []byte(`{"name":"Phone","type":"bark","enabled":true,"config":{"serverUrl":"https://api.day.app","deviceKey":"sensitive-device-key","group":"WatchBell"}}`)
	response, err := http.Post(server.URL+"/api/channels", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	createdBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d body = %s", response.StatusCode, createdBody)
	}
	if bytes.Contains(createdBody, []byte("sensitive-device-key")) || !bytes.Contains(createdBody, []byte(`"configuredSecrets":["deviceKey"]`)) {
		t.Fatalf("secret response was not redacted: %s", createdBody)
	}
	listResponse, err := http.Get(server.URL + "/api/channels")
	if err != nil {
		t.Fatal(err)
	}
	listBody, _ := io.ReadAll(listResponse.Body)
	listResponse.Body.Close()
	if bytes.Contains(listBody, []byte("sensitive-device-key")) {
		t.Fatalf("list endpoint leaked channel secret: %s", listBody)
	}
	auditResponse, err := http.Get(server.URL + "/api/audit-logs")
	if err != nil {
		t.Fatal(err)
	}
	auditBody, _ := io.ReadAll(auditResponse.Body)
	auditResponse.Body.Close()
	if bytes.Contains(auditBody, []byte("sensitive-device-key")) {
		t.Fatalf("audit endpoint leaked channel secret: %s", auditBody)
	}

	updateBody := []byte(`{"name":"Phone","type":"bark","enabled":true,"config":{"serverUrl":"https://api.day.app","deviceKey":"","group":"Updated"}}`)
	request, _ := http.NewRequest(http.MethodPut, server.URL+"/api/channels/1", bytes.NewReader(updateBody))
	request.Header.Set("Content-Type", "application/json")
	updateResponse, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, updateResponse.Body)
	updateResponse.Body.Close()
	if updateResponse.StatusCode != http.StatusOK {
		t.Fatalf("empty secret update status = %d", updateResponse.StatusCode)
	}
	stored, err := db.GetNotifyChannel(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stored.Config, []byte("sensitive-device-key")) {
		t.Fatalf("empty secret update did not retain the configured value: %s", stored.Config)
	}
}

func TestEmptyCollectionsUseArrays(t *testing.T) {
	server, _ := newTestServer(t)
	for _, path := range []string{"/api/monitors", "/api/check-runs", "/api/events", "/api/rule-evaluations", "/api/notification-attempts", "/api/audit-logs"} {
		response, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if string(body) != "[]\n" {
			t.Fatalf("%s returned %q", path, body)
		}
	}
}
