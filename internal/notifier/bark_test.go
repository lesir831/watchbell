package notifier

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

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
