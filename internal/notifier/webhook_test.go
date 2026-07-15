package notifier

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/watchbell/watchbell/internal/model"
)

func TestWebhookNotifierRendersAndSendsRequest(t *testing.T) {
	type requestSnapshot struct {
		method      string
		path        string
		authorize   string
		contentType string
		body        string
	}
	received := make(chan requestSnapshot, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received <- requestSnapshot{
			method:      r.Method,
			path:        r.URL.RequestURI(),
			authorize:   r.Header.Get("Authorization"),
			contentType: r.Header.Get("Content-Type"),
			body:        string(body),
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	config := webhookConfigJSON(t, WebhookConfig{
		URL:          server.URL + "/hooks/${event.id}?source=watchbell",
		AllowPrivate: true,
		Method:       "patch",
		Headers: map[string]string{
			"Authorization": "Bearer ${event.token}",
			"Content-Type":  "application/json",
		},
		BodyTemplate: `{"title":"${message.subject}","body":"${message.body}","link":"${rss.link}","monitor":"${monitor.name}"}`,
	})
	message := Message{
		Subject: "New release",
		Body:    "Version 2.0",
		Data: map[string]any{
			"event":   map[string]any{"id": 42, "token": "secret-token"},
			"rss":     map[string]any{"link": "https://example.com/items/42"},
			"monitor": map[string]any{"name": "Releases"},
		},
	}

	if err := NewWebhookNotifier().Send(context.Background(), model.NotifyChannel{Config: config}, message); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	request := <-received
	if request.method != http.MethodPatch {
		t.Fatalf("method = %q", request.method)
	}
	if request.path != "/hooks/42?source=watchbell" {
		t.Fatalf("path = %q", request.path)
	}
	if request.authorize != "Bearer secret-token" {
		t.Fatalf("Authorization = %q", request.authorize)
	}
	if request.contentType != "application/json" {
		t.Fatalf("Content-Type = %q", request.contentType)
	}
	wantBody := `{"title":"New release","body":"Version 2.0","link":"https://example.com/items/42","monitor":"Releases"}`
	if request.body != wantBody {
		t.Fatalf("body = %q, want %q", request.body, wantBody)
	}
}

func TestWebhookNotifierUsesDefaultJSONBody(t *testing.T) {
	type webhookPayload struct {
		Subject string         `json:"subject"`
		Body    string         `json:"body"`
		Data    map[string]any `json:"data"`
	}
	received := make(chan webhookPayload, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Content-Type"); got != "application/json; charset=utf-8" {
			w.WriteHeader(http.StatusUnsupportedMediaType)
			return
		}
		var body webhookPayload
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		received <- body
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	config := webhookConfigJSON(t, WebhookConfig{URL: server.URL, AllowPrivate: true})
	message := Message{Subject: "Subject", Body: "Body", Data: map[string]any{"event": map[string]any{"id": 7}}}
	if err := NewWebhookNotifier().Send(context.Background(), model.NotifyChannel{Config: config}, message); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	payload := <-received
	if payload.Subject != message.Subject || payload.Body != message.Body {
		t.Fatalf("payload = %#v", payload)
	}
	event, ok := payload.Data["event"].(map[string]any)
	if !ok || event["id"] != float64(7) {
		t.Fatalf("payload data = %#v", payload.Data)
	}
}

func TestWebhookNotifierRejectsUnsafeConfiguration(t *testing.T) {
	tests := []struct {
		name    string
		config  WebhookConfig
		message Message
		want    string
	}{
		{name: "scheme", config: WebhookConfig{URL: "ftp://example.com/hook"}, want: "must use http or https"},
		{name: "credentials", config: WebhookConfig{URL: "https://user:pass@example.com/hook"}, want: "must not include credentials"},
		{name: "fragment", config: WebhookConfig{URL: "https://example.com/hook#token"}, want: "must not include a fragment"},
		{name: "templated host", config: WebhookConfig{URL: "https://${event.host}/hook"}, want: "host must not contain template variables"},
		{name: "method", config: WebhookConfig{URL: "https://example.com/hook", Method: http.MethodConnect}, want: "is not allowed"},
		{name: "host header", config: WebhookConfig{URL: "https://example.com/hook", Headers: map[string]string{"Host": "evil.example"}}, want: "is not allowed"},
		{name: "duplicate header", config: WebhookConfig{URL: "https://example.com/hook", Headers: map[string]string{"X-Token": "one", "x-token": "two"}}, want: "configured more than once"},
		{
			name:    "rendered newline header",
			config:  WebhookConfig{URL: "https://example.com/hook", Headers: map[string]string{"X-Name": "${event.value}"}},
			message: Message{Data: map[string]any{"event": map[string]any{"value": "ok\r\nInjected: yes"}}},
			want:    "invalid value",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := NewWebhookNotifier().Send(context.Background(), model.NotifyChannel{Config: webhookConfigJSON(t, test.config)}, test.message)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Send() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestWebhookNotifierDoesNotFollowRedirects(t *testing.T) {
	var redirectedRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectedRequests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	err := NewWebhookNotifier().Send(context.Background(), model.NotifyChannel{
		Config: webhookConfigJSON(t, WebhookConfig{URL: redirect.URL, Headers: map[string]string{"Authorization": "Bearer secret"}, AllowPrivate: true}),
	}, Message{})
	if err == nil || !strings.Contains(err.Error(), "webhook http 307") {
		t.Fatalf("Send() error = %v", err)
	}
	if got := redirectedRequests.Load(); got != 0 {
		t.Fatalf("redirect target received %d requests", got)
	}
}

func TestWebhookNotifierReportsBoundedProviderError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, strings.Repeat("provider failed ", 100))
	}))
	defer server.Close()

	err := NewWebhookNotifier().Send(context.Background(), model.NotifyChannel{
		Config: webhookConfigJSON(t, WebhookConfig{URL: server.URL, AllowPrivate: true}),
	}, Message{})
	if err == nil || !strings.Contains(err.Error(), "webhook http 502: provider failed") {
		t.Fatalf("Send() error = %v", err)
	}
	if len(err.Error()) > 560 {
		t.Fatalf("provider error was not bounded: %d bytes", len(err.Error()))
	}
}

func TestWebhookNotifierBlocksPrivateAddressesByDefault(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("blocked private webhook reached the server")
	}))
	defer server.Close()
	err := NewWebhookNotifier().Send(context.Background(), model.NotifyChannel{
		Config: webhookConfigJSON(t, WebhookConfig{URL: server.URL}),
	}, Message{})
	if err == nil || !strings.Contains(err.Error(), "private or special-purpose") {
		t.Fatalf("Send() error = %v, want private-address rejection", err)
	}
}

func TestWebhookURLValidationDoesNotLeakSecretURL(t *testing.T) {
	secretURL := "https://private-hook.example/%zz?token=top-secret"
	err := ValidateWebhookConfig(webhookConfigJSON(t, WebhookConfig{URL: secretURL}))
	if err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("ValidateWebhookConfig() error = %v", err)
	}
	if strings.Contains(err.Error(), "private-hook.example") || strings.Contains(err.Error(), "top-secret") {
		t.Fatalf("validation error leaked webhook URL: %v", err)
	}
}

func webhookConfigJSON(t *testing.T, config WebhookConfig) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
