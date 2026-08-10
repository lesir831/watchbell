package notifier

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"github.com/watchbell/watchbell/internal/model"
)

func TestBarkNotifierRendersClickURLFromMessageData(t *testing.T) {
	t.Helper()

	received := make(chan map[string]string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/push" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		received <- body
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	config, err := json.Marshal(BarkConfig{
		ServerURL: server.URL,
		DeviceKey: "device-key",
		URL:       "${rss.link}?source=${monitor.name}",
	})
	if err != nil {
		t.Fatal(err)
	}

	n := NewBarkNotifier()
	err = n.Send(context.Background(), model.NotifyChannel{Config: config}, Message{
		Subject: "New release",
		Body:    "Version 2.0",
		Data: map[string]any{
			"rss":     map[string]any{"link": "https://example.com/items/42"},
			"monitor": map[string]any{"name": "Releases"},
		},
	})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	body := <-received
	if got, want := body["url"], "https://example.com/items/42?source=Releases"; got != want {
		t.Fatalf("click URL = %q, want %q", got, want)
	}
	if got := body["title"]; got != "New release" {
		t.Fatalf("title = %q", got)
	}
	if got := body["body"]; got != "Version 2.0" {
		t.Fatalf("body = %q", got)
	}
}

func TestBarkNotifierUsesCrossModuleURLAlias(t *testing.T) {
	received := make(chan map[string]string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		received <- body
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	config, err := json.Marshal(BarkConfig{ServerURL: server.URL, DeviceKey: "device-key", URL: "${url}"})
	if err != nil {
		t.Fatal(err)
	}
	if err := NewBarkNotifier().Send(context.Background(), model.NotifyChannel{Config: config}, Message{
		Subject: "Release", Body: "Version 2", Data: map[string]any{"url": "https://github.com/acme/app/releases/tag/v2"},
	}); err != nil {
		t.Fatal(err)
	}
	if got := (<-received)["url"]; got != "https://github.com/acme/app/releases/tag/v2" {
		t.Fatalf("global click URL = %q", got)
	}
}

func TestBarkNotifierOmitsEmptyRenderedURL(t *testing.T) {
	received := make(chan map[string]string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		received <- body
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	config, err := json.Marshal(BarkConfig{ServerURL: server.URL, DeviceKey: "device-key", URL: "${rss.missing}"})
	if err != nil {
		t.Fatal(err)
	}
	if err := NewBarkNotifier().Send(context.Background(), model.NotifyChannel{Config: config}, Message{}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	body := <-received
	if _, exists := body["url"]; exists {
		t.Fatalf("empty rendered URL should be omitted: %#v", body)
	}
}

func TestBarkNotifierDoesNotRedirectDeviceKey(t *testing.T) {
	var targetRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetRequests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/stolen", http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	config, err := json.Marshal(BarkConfig{ServerURL: redirect.URL, DeviceKey: "must-not-leak"})
	if err != nil {
		t.Fatal(err)
	}
	err = NewBarkNotifier().Send(context.Background(), model.NotifyChannel{Config: config}, Message{Subject: "test", Body: "body"})
	if err == nil || !strings.Contains(err.Error(), "bark http 307") {
		t.Fatalf("Send() error = %v", err)
	}
	if targetRequests.Load() != 0 {
		t.Fatalf("redirect target received %d credential-bearing requests", targetRequests.Load())
	}
}

func TestBarkNotifierTruncatesOversizedPayloadAtRuneBoundary(t *testing.T) {
	received := make(chan map[string]string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		received <- payload
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	config, err := json.Marshal(BarkConfig{
		ServerURL: server.URL,
		DeviceKey: "device-key",
		Group:     "WatchBell",
		Sound:     "alarm",
		Icon:      "https://example.com/icon.png",
		URL:       "${url}",
	})
	if err != nil {
		t.Fatal(err)
	}
	originalBody := strings.Repeat("正文🙂<&\\\"\n", 1000)
	message := Message{
		Subject: "保留完整标题",
		Body:    originalBody,
		Data:    map[string]any{"url": "https://example.com/full-content"},
	}
	if err := NewBarkNotifier().Send(context.Background(), model.NotifyChannel{Config: config}, message); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	payload := <-received
	if got := payload["title"]; got != message.Subject {
		t.Fatalf("title = %q, want %q", got, message.Subject)
	}
	gotBody := payload["body"]
	if gotBody == originalBody || !strings.HasSuffix(gotBody, barkTruncationMarker) {
		t.Fatalf("body was not marked as truncated: length=%d", len(gotBody))
	}
	if !utf8.ValidString(gotBody) {
		t.Fatal("truncated body is not valid UTF-8")
	}
	prefix := strings.TrimSuffix(gotBody, barkTruncationMarker)
	if !strings.HasPrefix(originalBody, prefix) {
		t.Fatal("truncated body is not a rune-aligned prefix of the original")
	}
	if payload["group"] != "WatchBell" || payload["sound"] != "alarm" || payload["icon"] != "https://example.com/icon.png" || payload["url"] != "https://example.com/full-content" {
		t.Fatalf("optional fields changed during truncation: %#v", payload)
	}
	size, err := estimatedBarkAPNsPayloadSize(payload)
	if err != nil {
		t.Fatal(err)
	}
	if size > barkPayloadBudgetBytes {
		t.Fatalf("estimated APNs payload size = %d, budget = %d", size, barkPayloadBudgetBytes)
	}
}

func TestPreprocessBarkPayloadTruncatesOversizedTitle(t *testing.T) {
	originalTitle := strings.Repeat("标题🙂", 1000)
	payload := map[string]string{
		"device_key": "device-key",
		"title":      originalTitle,
		"body":       "",
	}
	if err := preprocessBarkPayload(payload); err != nil {
		t.Fatal(err)
	}
	got := payload["title"]
	if got == originalTitle || !strings.HasSuffix(got, barkTruncationMarker) || !utf8.ValidString(got) {
		t.Fatalf("title was not safely truncated: length=%d", len(got))
	}
	if !strings.HasPrefix(originalTitle, strings.TrimSuffix(got, barkTruncationMarker)) {
		t.Fatal("truncated title is not a rune-aligned prefix of the original")
	}
	if size, err := estimatedBarkAPNsPayloadSize(payload); err != nil || size > barkPayloadBudgetBytes {
		t.Fatalf("estimated APNs payload size = %d, err = %v", size, err)
	}
}

func TestPreprocessBarkPayloadPreservesExactBudgetAndTruncatesOverflow(t *testing.T) {
	base := map[string]string{"device_key": "device-key", "title": "T", "body": "x"}
	baseSize, err := estimatedBarkAPNsPayloadSize(base)
	if err != nil {
		t.Fatal(err)
	}
	bodyLength := barkPayloadBudgetBytes - (baseSize - 1)
	exact := strings.Repeat("x", bodyLength)

	atLimit := map[string]string{"device_key": "device-key", "title": "T", "body": exact}
	if err := preprocessBarkPayload(atLimit); err != nil {
		t.Fatal(err)
	}
	if atLimit["body"] != exact {
		t.Fatal("payload at the exact byte budget was truncated")
	}

	overLimit := map[string]string{"device_key": "device-key", "title": "T", "body": exact + "x"}
	if err := preprocessBarkPayload(overLimit); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(overLimit["body"], barkTruncationMarker) {
		t.Fatal("payload one byte over the budget was not truncated")
	}
	if size, err := estimatedBarkAPNsPayloadSize(overLimit); err != nil || size > barkPayloadBudgetBytes {
		t.Fatalf("estimated APNs payload size = %d, err = %v", size, err)
	}
}

func TestBarkNotifierRejectsOversizedMetadataBeforeSending(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	config, err := json.Marshal(BarkConfig{
		ServerURL: server.URL,
		DeviceKey: "device-key",
		Icon:      "https://example.com/" + strings.Repeat("x", barkPayloadBudgetBytes),
	})
	if err != nil {
		t.Fatal(err)
	}
	err = NewBarkNotifier().Send(context.Background(), model.NotifyChannel{Config: config}, Message{
		Subject: "title",
		Body:    strings.Repeat("body", 1000),
	})
	if err == nil || !strings.Contains(err.Error(), "payload metadata exceeds") {
		t.Fatalf("Send() error = %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("server received %d requests for an unsendable payload", requests.Load())
	}
}
