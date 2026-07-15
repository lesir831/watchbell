package auth

import (
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

const testPasswordHash = "pbkdf2-sha256$210000$wJ7uwPXRx3I5W-CYFTWCqw$ugxmCBayTf_gzUkDj1St3hd8dC5iUedtf98HzjUcbKE"

func newAuthManager(t *testing.T, configure func(*Config)) *Manager {
	t.Helper()
	cfg := Config{
		Enabled:            true,
		Username:           "admin",
		PasswordHash:       testPasswordHash,
		SessionSecret:      "01234567890123456789012345678901",
		SessionTTL:         time.Hour,
		LoginMaxFailures:   2,
		LoginFailureWindow: time.Minute,
	}
	if configure != nil {
		configure(&cfg)
	}
	manager, err := NewManager(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func loginRequest(remoteAddr string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "http://watchbell.test/api/auth/login", nil)
	request.RemoteAddr = remoteAddr
	return request
}

func TestLoginRateLimitAndSuccessReset(t *testing.T) {
	manager := newAuthManager(t, nil)
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	manager.loginFailures.now = func() time.Time { return now }
	request := loginRequest("192.0.2.10:4123")

	if err := manager.Login(httptest.NewRecorder(), request, "admin", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("first failure = %v", err)
	}
	if err := manager.Login(httptest.NewRecorder(), request, "admin", "correct-password"); err != nil {
		t.Fatalf("valid login after one failure = %v", err)
	}
	manager.loginFailures.mu.Lock()
	remainingFailures := len(manager.loginFailures.failures["192.0.2.10"])
	manager.loginFailures.mu.Unlock()
	if remainingFailures != 0 {
		t.Fatalf("successful login left %d failures", remainingFailures)
	}

	manager.loginFailures.record("192.0.2.10")
	manager.loginFailures.record("192.0.2.10")
	err := manager.Login(httptest.NewRecorder(), request, "admin", "correct-password")
	retryAfter, limited := LoginRetryAfter(err)
	if !limited || retryAfter != time.Minute {
		t.Fatalf("rate limit = (%v, %v), want (1m, true)", retryAfter, limited)
	}

	// A different client is not locked out by this client's failures.
	if retryAfter := manager.loginFailures.retryAfter("192.0.2.11"); retryAfter != 0 {
		t.Fatalf("other client was rate limited for %v", retryAfter)
	}

	now = now.Add(time.Minute)
	if retryAfter := manager.loginFailures.retryAfter("192.0.2.10"); retryAfter != 0 {
		t.Fatalf("expired failures still rate limited for %v", retryAfter)
	}
}

func TestFailureLimiterIsConcurrentSafe(t *testing.T) {
	limiter := &failureLimiter{
		failures: make(map[string][]time.Time),
		max:      200,
		window:   time.Minute,
		now:      func() time.Time { return time.Unix(100, 0) },
	}
	var group sync.WaitGroup
	for range 100 {
		group.Add(1)
		go func() {
			defer group.Done()
			limiter.record("client")
			_ = limiter.retryAfter("client")
		}()
	}
	group.Wait()
	limiter.mu.Lock()
	failures := len(limiter.failures["client"])
	limiter.mu.Unlock()
	if failures != 100 {
		t.Fatalf("recorded failures = %d, want 100", failures)
	}
}

func TestSecureCookieDetectionAndOverride(t *testing.T) {
	trueValue := true
	falseValue := false
	tests := []struct {
		name       string
		override   *bool
		trustProxy bool
		configure  func(*http.Request)
		wantSecure bool
	}{
		{name: "plain HTTP remains compatible", wantSecure: false},
		{name: "direct TLS", configure: func(r *http.Request) { r.TLS = &tls.ConnectionState{} }, wantSecure: true},
		{name: "untrusted forwarded proto is ignored", configure: func(r *http.Request) { r.Header.Set("X-Forwarded-Proto", "https") }, wantSecure: false},
		{name: "trusted closest x forwarded proto", trustProxy: true, configure: func(r *http.Request) { r.Header.Set("X-Forwarded-Proto", "http, https") }, wantSecure: true},
		{name: "forged leftmost proto is ignored", trustProxy: true, configure: func(r *http.Request) { r.Header.Set("X-Forwarded-Proto", "https, http") }, wantSecure: false},
		{name: "trusted standard forwarded header", trustProxy: true, configure: func(r *http.Request) { r.Header.Set("Forwarded", `for=192.0.2.1; proto="HTTPS"; host=watchbell.test`) }, wantSecure: true},
		{name: "explicit true wins", override: &trueValue, wantSecure: true},
		{name: "explicit false wins over TLS", override: &falseValue, configure: func(r *http.Request) { r.TLS = &tls.ConnectionState{} }, wantSecure: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := newAuthManager(t, func(cfg *Config) { cfg.CookieSecure = test.override; cfg.TrustProxyHeaders = test.trustProxy })
			request := loginRequest("192.0.2.20:4123")
			if test.configure != nil {
				test.configure(request)
			}
			if got := manager.secureCookie(request); got != test.wantSecure {
				t.Fatalf("secureCookie() = %v, want %v", got, test.wantSecure)
			}
		})
	}
}

func TestLogoutUsesSecureCookiePolicy(t *testing.T) {
	manager := newAuthManager(t, func(cfg *Config) { cfg.TrustProxyHeaders = true })
	request := loginRequest("192.0.2.30:4123")
	request.Header.Set("X-Forwarded-Proto", "https")
	response := httptest.NewRecorder()
	manager.Logout(response, request)
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || cookies[0].MaxAge != -1 {
		t.Fatalf("logout cookie = %#v", cookies)
	}
}
