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
	"testing"
	"time"

	"github.com/watchbell/watchbell/internal/auth"
	"github.com/watchbell/watchbell/internal/checker"
	"github.com/watchbell/watchbell/internal/notifier"
	"github.com/watchbell/watchbell/internal/scheduler"
	"github.com/watchbell/watchbell/internal/store"
)

func TestLoginEndpointReturnsRateLimitResponse(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir()+"/watchbell.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager, err := auth.NewManager(auth.Config{
		Enabled:            true,
		Username:           "admin",
		PasswordHash:       "pbkdf2-sha256$210000$wJ7uwPXRx3I5W-CYFTWCqw$ugxmCBayTf_gzUkDj1St3hd8dC5iUedtf98HzjUcbKE",
		SessionSecret:      "01234567890123456789012345678901",
		LoginMaxFailures:   1,
		LoginFailureWindow: time.Minute,
	}, logger)
	if err != nil {
		t.Fatal(err)
	}
	sched := scheduler.New(db, checker.NewRegistry(), notifier.NewRegistry(), scheduler.Options{})
	server := httptest.NewServer(NewServer(db, sched, "", logger, manager).Routes())
	t.Cleanup(server.Close)

	postLogin := func(password, forwardedFor string) *http.Response {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"username": "admin", "password": password})
		request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/auth/login", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Forwarded-For", forwardedFor)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}

	first := postLogin("wrong", "198.51.100.10")
	io.Copy(io.Discard, first.Body)
	first.Body.Close()
	if first.StatusCode != http.StatusUnauthorized {
		t.Fatalf("first failure status = %d, want 401", first.StatusCode)
	}

	// Proxy headers are ignored unless explicitly trusted; rotating XFF must
	// not bypass the default TCP-peer rate limit.
	limited := postLogin("correct-password", "198.51.100.11")
	defer limited.Body.Close()
	if limited.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("limited status = %d, want 429", limited.StatusCode)
	}
	retryAfter, err := strconv.Atoi(limited.Header.Get("Retry-After"))
	if err != nil || retryAfter < 1 {
		t.Fatalf("Retry-After = %q", limited.Header.Get("Retry-After"))
	}
	var payload struct {
		Error             string `json:"error"`
		RetryAfterSeconds int    `json:"retryAfterSeconds"`
	}
	if err := json.NewDecoder(limited.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error == "" || payload.RetryAfterSeconds != retryAfter {
		t.Fatalf("payload = %#v, Retry-After = %d", payload, retryAfter)
	}
}

func TestTrustedProxyLoginLimitIgnoresSpoofableHeaders(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir()+"/watchbell.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager, err := auth.NewManager(auth.Config{
		Enabled:            true,
		Username:           "admin",
		PasswordHash:       "pbkdf2-sha256$210000$wJ7uwPXRx3I5W-CYFTWCqw$ugxmCBayTf_gzUkDj1St3hd8dC5iUedtf98HzjUcbKE",
		SessionSecret:      "01234567890123456789012345678901",
		TrustProxyHeaders:  true,
		TrustedProxyHops:   1,
		LoginMaxFailures:   1,
		LoginFailureWindow: time.Minute,
	}, logger)
	if err != nil {
		t.Fatal(err)
	}
	sched := scheduler.New(db, checker.NewRegistry(), notifier.NewRegistry(), scheduler.Options{})
	server := httptest.NewServer(NewServer(db, sched, "", logger, manager).Routes())
	t.Cleanup(server.Close)

	postLogin := func(password, xff, spoofed string) *http.Response {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"username": "admin", "password": password})
		request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/auth/login", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Forwarded-For", xff)
		request.Header.Set("True-Client-IP", spoofed)
		request.Header.Set("X-Real-IP", spoofed)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}

	first := postLogin("wrong", "203.0.113.10, 198.51.100.7", "192.0.2.10")
	io.Copy(io.Discard, first.Body)
	first.Body.Close()
	if first.StatusCode != http.StatusUnauthorized {
		t.Fatalf("first status = %d, want 401", first.StatusCode)
	}

	// Rotating attacker-controlled left entries and single-IP headers cannot
	// change the one-hop proxy boundary selected from the right of XFF.
	limited := postLogin("correct-password", "203.0.113.11, 198.51.100.7", "192.0.2.11")
	io.Copy(io.Discard, limited.Body)
	limited.Body.Close()
	if limited.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("spoofed identity bypass status = %d, want 429", limited.StatusCode)
	}

	other := postLogin("correct-password", "203.0.113.11, 198.51.100.8", "192.0.2.11")
	io.Copy(io.Discard, other.Body)
	other.Body.Close()
	if other.StatusCode != http.StatusOK {
		t.Fatalf("different proxy-boundary client status = %d, want 200", other.StatusCode)
	}
}
