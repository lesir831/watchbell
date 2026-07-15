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
	"github.com/watchbell/watchbell/internal/model"
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

func TestCreateEndpointsRejectAmbiguousNaturalKeys(t *testing.T) {
	server, db := newTestServer(t)
	ctx := context.Background()
	monitorInput := model.MonitorInput{Name: "Unique feed", Type: model.MonitorTypeRSS, Enabled: true, IntervalSeconds: 300, Config: json.RawMessage(`{"url":"https://example.com/feed.xml"}`)}
	monitor, err := db.CreateMonitor(ctx, monitorInput)
	if err != nil {
		t.Fatal(err)
	}
	channelInput := model.NotifyChannelInput{Name: "Unique phone", Type: model.ChannelTypeBark, Enabled: true, Config: json.RawMessage(`{"deviceKey":"key"}`)}
	channel, err := db.CreateNotifyChannel(ctx, channelInput)
	if err != nil {
		t.Fatal(err)
	}
	templateInput := model.NotificationTemplateInput{Name: "Unique template", SubjectTemplate: "subject", BodyTemplate: "body"}
	if _, err := db.CreateNotificationTemplate(ctx, templateInput); err != nil {
		t.Fatal(err)
	}
	ruleInput := model.RuleInput{MonitorID: monitor.ID, Name: "Unique rule", Enabled: true, Condition: json.RawMessage(`{}`), NotifyChannelIDs: []int64{channel.ID}}
	if _, err := db.CreateRule(ctx, ruleInput); err != nil {
		t.Fatal(err)
	}

	postDuplicate := func(path string, input any) {
		t.Helper()
		body, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.Post(server.URL+path, "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var payload struct {
			Fields map[string]string `json:"fields"`
		}
		if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusUnprocessableEntity || payload.Fields["name"] == "" {
			t.Fatalf("%s duplicate response: status=%d fields=%#v", path, response.StatusCode, payload.Fields)
		}
	}
	postDuplicate("/api/monitors", monitorInput)
	postDuplicate("/api/channels", channelInput)
	postDuplicate("/api/templates", templateInput)
	postDuplicate("/api/rules", ruleInput)
}

func TestMonitorManifestFieldTypesAreValidatedBeforeSave(t *testing.T) {
	server, _ := newTestServer(t)
	body := []byte(`{"name":"Typed config","type":"webpage","enabled":true,"intervalSeconds":300,"config":{"url":"https://example.com","selector":7,"timeoutSeconds":"999","ignorePatterns":["safe",2]}}`)
	response, err := http.Post(server.URL+"/api/monitors", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var payload struct {
		Fields map[string]string `json:"fields"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusUnprocessableEntity || payload.Fields["config.selector"] == "" || payload.Fields["config.timeoutSeconds"] == "" || payload.Fields["config.ignorePatterns"] == "" {
		t.Fatalf("typed validation status=%d fields=%#v", response.StatusCode, payload.Fields)
	}
}

func TestRuleQuietHoursValidationReturnsFieldError(t *testing.T) {
	server, db := newTestServer(t)
	ctx := context.Background()
	monitor, err := db.CreateMonitor(ctx, model.MonitorInput{
		Name: "Feed", Type: model.MonitorTypeRSS, Enabled: true, IntervalSeconds: 300,
		Config: json.RawMessage(`{"url":"https://example.com/feed.xml"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	channel, err := db.CreateNotifyChannel(ctx, model.NotifyChannelInput{
		Name: "Phone", Type: model.ChannelTypeBark, Enabled: true,
		Config: json.RawMessage(`{"serverUrl":"https://api.day.app","deviceKey":"test"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(model.RuleInput{
		MonitorID: monitor.ID, Name: "Invalid quiet hours", Enabled: true, Condition: json.RawMessage(`{}`),
		NotifyChannelIDs: []int64{channel.ID}, QuietHours: model.QuietHours{Enabled: true, Start: "22:00", End: "08:00", Timezone: "UTC+8"},
	})
	response, err := http.Post(server.URL+"/api/rules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d", response.StatusCode)
	}
	var payload struct {
		Fields map[string]string `json:"fields"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Fields["quietHours.timezone"] == "" {
		t.Fatalf("missing quiet-hours field error: %#v", payload.Fields)
	}
}

func TestMonitorFailureAlertValidationAndPersistence(t *testing.T) {
	server, db := newTestServer(t)
	ctx := context.Background()
	base := model.MonitorInput{
		Name: "Feed", Type: model.MonitorTypeRSS, Enabled: true, IntervalSeconds: 300,
		Config: json.RawMessage(`{"url":"https://example.com/feed.xml"}`), FailureAlertAfter: 3,
	}
	post := func(input model.MonitorInput) (int, map[string]any) {
		t.Helper()
		body, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.Post(server.URL+"/api/monitors", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var payload map[string]any
		if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, payload
	}
	status, payload := post(base)
	fields, _ := payload["fields"].(map[string]any)
	if status != http.StatusUnprocessableEntity || fields["failureNotifyChannelIds"] == nil {
		t.Fatalf("missing-channel validation: status=%d payload=%#v", status, payload)
	}
	base.FailureAlertAfter = 101
	status, payload = post(base)
	fields, _ = payload["fields"].(map[string]any)
	if status != http.StatusUnprocessableEntity || fields["failureAlertAfter"] == nil {
		t.Fatalf("threshold validation: status=%d payload=%#v", status, payload)
	}
	base.FailureAlertAfter = 3
	base.FailureNotifyChannelIDs = []int64{9999}
	status, payload = post(base)
	fields, _ = payload["fields"].(map[string]any)
	if status != http.StatusUnprocessableEntity || fields["failureNotifyChannelIds.0"] == nil {
		t.Fatalf("channel reference validation: status=%d payload=%#v", status, payload)
	}
	channel, err := db.CreateNotifyChannel(ctx, model.NotifyChannelInput{
		Name: "Phone", Type: model.ChannelTypeBark, Enabled: true,
		Config: json.RawMessage(`{"serverUrl":"https://api.day.app","deviceKey":"test"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	base.FailureNotifyChannelIDs = []int64{channel.ID}
	status, payload = post(base)
	if status != http.StatusCreated || int(payload["failureAlertAfter"].(float64)) != 3 {
		t.Fatalf("valid monitor alert config: status=%d payload=%#v", status, payload)
	}
	ids, ok := payload["failureNotifyChannelIds"].([]any)
	if !ok || len(ids) != 1 || int64(ids[0].(float64)) != channel.ID {
		t.Fatalf("saved channel references missing: %#v", payload)
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

func TestChannelConfigTypesAreValidatedBeforeSave(t *testing.T) {
	tests := []model.NotifyChannelInput{
		{Name: "Bark", Type: model.ChannelTypeBark, Enabled: true, Config: json.RawMessage(`{"serverUrl":"https://api.day.app","deviceKey":123}`)},
		{Name: "Email", Type: model.ChannelTypeEmail, Enabled: true, Config: json.RawMessage(`{"host":"smtp.example.com","port":587,"from":"sender@example.com","to":["recipient@example.com"],"startTls":"yes"}`)},
	}
	for _, input := range tests {
		err := validateChannelInput(input)
		problem, ok := err.(*problemError)
		if !ok || problem.Fields["config"] == "" {
			t.Fatalf("%s type validation error = %#v", input.Type, err)
		}
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
