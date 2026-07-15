package notifier

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
		BodyTemplate: `{"title":${json:message.subject},"body":${json:message.body},"link":${json:rss.link},"monitor":${json:monitor.name}}`,
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

func TestWebhookNotifierEscapesJSONTemplateValuesAndRejectsPlainJSONVariables(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode webhook body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if payload["body"] != "line one\n\"quoted\" \\ path" {
			t.Errorf("body = %#v", payload["body"])
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	message := Message{Body: "line one\n\"quoted\" \\ path"}
	safe := WebhookConfig{
		URL: server.URL, AllowPrivate: true,
		Headers:      map[string]string{"Content-Type": "application/problem+json"},
		BodyTemplate: `{"body":${json:message.body}}`,
	}
	if err := NewWebhookNotifier().Send(context.Background(), model.NotifyChannel{Config: webhookConfigJSON(t, safe)}, message); err != nil {
		t.Fatalf("safe Send() error = %v", err)
	}

	unsafe := safe
	unsafe.Headers = map[string]string{"Content-Type": "application/json"}
	unsafe.BodyTemplate = `{"body":"${message.body}"}`
	err := NewWebhookNotifier().Send(context.Background(), model.NotifyChannel{Config: webhookConfigJSON(t, unsafe)}, message)
	if err == nil || !strings.Contains(err.Error(), "must use ${json:path}") {
		t.Fatalf("unsafe Send() error = %v", err)
	}

	// Syntax validation alone is insufficient: this value would add fields and
	// still leave a valid JSON document after raw string interpolation.
	message.Body = `","admin":true,"tail":"`
	err = NewWebhookNotifier().Send(context.Background(), model.NotifyChannel{Config: webhookConfigJSON(t, unsafe)}, message)
	if err == nil || !strings.Contains(err.Error(), "must use ${json:path}") {
		t.Fatalf("semantic injection Send() error = %v", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("unsafe rendered JSON reached provider; requests = %d", got)
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

func TestWebhookNotifierBlocksSpecialPurposeAddressRanges(t *testing.T) {
	blocked := []string{
		"192.0.0.1", "198.18.0.1", "224.0.0.1", "240.0.0.1",
		"::192.0.2.1", "64:ff9b::c000:201", "64:ff9b:1::1", "100:0:0:1::1", "2001:db8::1", "3fff::1", "fec0::1",
	}
	for _, raw := range blocked {
		t.Run(raw, func(t *testing.T) {
			if !blockedWebhookIP(net.ParseIP(raw)) {
				t.Fatalf("blockedWebhookIP(%s) = false", raw)
			}
			config := webhookConfigJSON(t, WebhookConfig{URL: "http://[" + raw + "]/hook"})
			if net.ParseIP(raw).To4() != nil {
				config = webhookConfigJSON(t, WebhookConfig{URL: "http://" + raw + "/hook"})
			}
			if err := ValidateWebhookConfig(config); err == nil || !strings.Contains(err.Error(), "special-purpose") {
				t.Fatalf("ValidateWebhookConfig(%s) error = %v", raw, err)
			}
		})
	}
	for _, raw := range []string{"8.8.8.8", "2606:4700:4700::1111"} {
		if blockedWebhookIP(net.ParseIP(raw)) {
			t.Fatalf("blockedWebhookIP(%s) = true for public address", raw)
		}
	}
}

func TestWebhookNotifierBlocksSpecialPurposeDNSResolution(t *testing.T) {
	for _, raw := range []string{"198.18.0.25", "::192.0.2.25", "100:0:0:1::25", "fec0::25"} {
		t.Run(raw, func(t *testing.T) {
			transport, err := webhookTransport(nil, false, func(context.Context, string) ([]net.IPAddr, error) {
				return []net.IPAddr{{IP: net.ParseIP(raw)}}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer transport.CloseIdleConnections()
			connection, err := transport.DialContext(context.Background(), "tcp", "webhook.example.test:80")
			if connection != nil {
				connection.Close()
			}
			if err == nil || !strings.Contains(err.Error(), "private or special-purpose") {
				t.Fatalf("DialContext() error = %v, want DNS address rejection", err)
			}
		})
	}
}

func TestWebhookNotifierClosesOneShotTransportIdleConnections(t *testing.T) {
	closed := make(chan struct{}, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateClosed {
			select {
			case closed <- struct{}{}:
			default:
			}
		}
	}
	server.Start()
	defer server.Close()

	err := NewWebhookNotifier().Send(context.Background(), model.NotifyChannel{
		Config: webhookConfigJSON(t, WebhookConfig{URL: server.URL, AllowPrivate: true}),
	}, Message{})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("one-shot webhook transport left its idle connection open")
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
