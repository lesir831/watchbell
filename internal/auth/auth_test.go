package auth

import (
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestExplicitShortSessionSecretIsRejected(t *testing.T) {
	_, err := NewManager(Config{
		Enabled:       true,
		PasswordHash:  testPasswordHash,
		SessionSecret: "too-short",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil || !strings.Contains(err.Error(), "at least 32 bytes") {
		t.Fatalf("NewManager() error = %v, want minimum session-secret length error", err)
	}
}

func TestPlaintextPasswordSessionSurvivesManagerRestart(t *testing.T) {
	cfg := Config{
		Enabled: true, Username: "admin", Password: "correct-password",
		SessionSecret: "01234567890123456789012345678901", SessionTTL: time.Hour,
	}
	managerOne, err := NewManager(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	loginResponse := httptest.NewRecorder()
	if err := managerOne.Login(loginResponse, loginRequest("192.0.2.40:4123"), "admin", "correct-password"); err != nil {
		t.Fatal(err)
	}
	cookies := loginResponse.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("login cookies = %#v", cookies)
	}

	managerTwo, err := NewManager(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://watchbell.test/api/auth/me", nil)
	request.AddCookie(cookies[0])
	response := httptest.NewRecorder()
	managerTwo.Require(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })).ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("session after restart status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestSyncPasswordHashRevokesExistingSessions(t *testing.T) {
	manager := newAuthManager(t, nil)
	loginResponse := httptest.NewRecorder()
	if err := manager.Login(loginResponse, loginRequest("192.0.2.41:4123"), "admin", "correct-password"); err != nil {
		t.Fatal(err)
	}
	newHash, err := HashPassword("replacement-password")
	if err != nil {
		t.Fatal(err)
	}
	changed, err := manager.SyncPasswordHash(func() (string, bool, error) { return newHash, true, nil })
	if err != nil || !changed {
		t.Fatalf("SyncPasswordHash() = changed %v, err %v", changed, err)
	}

	request := httptest.NewRequest(http.MethodGet, "http://watchbell.test/api/auth/me", nil)
	request.AddCookie(loginResponse.Result().Cookies()[0])
	response := httptest.NewRecorder()
	manager.Require(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })).ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("old session after reload status = %d", response.Code)
	}
	if err := manager.Login(httptest.NewRecorder(), loginRequest("192.0.2.42:4123"), "admin", "replacement-password"); err != nil {
		t.Fatalf("new password login = %v", err)
	}
}

func TestSessionTTLChangeRevokesOldPolicyAndRefreshesIdleSession(t *testing.T) {
	manager := newAuthManager(t, nil)
	loginResponse := httptest.NewRecorder()
	if err := manager.Login(loginResponse, loginRequest("192.0.2.43:4123"), "admin", "correct-password"); err != nil {
		t.Fatal(err)
	}
	oldCookie := loginResponse.Result().Cookies()[0]
	if oldCookie.MaxAge != int(time.Hour/time.Second) {
		t.Fatalf("initial MaxAge = %d", oldCookie.MaxAge)
	}
	if err := manager.SetSessionTTL(8 * time.Hour); err != nil {
		t.Fatal(err)
	}
	oldRequest := httptest.NewRequest(http.MethodGet, "http://watchbell.test/api/auth/me", nil)
	oldRequest.AddCookie(oldCookie)
	oldResponse := httptest.NewRecorder()
	manager.Require(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })).ServeHTTP(oldResponse, oldRequest)
	if oldResponse.Code != http.StatusUnauthorized {
		t.Fatalf("old-policy session status = %d", oldResponse.Code)
	}

	newLogin := httptest.NewRecorder()
	if err := manager.Login(newLogin, loginRequest("192.0.2.44:4123"), "admin", "correct-password"); err != nil {
		t.Fatal(err)
	}
	newCookie := newLogin.Result().Cookies()[0]
	request := httptest.NewRequest(http.MethodGet, "http://watchbell.test/api/auth/me", nil)
	request.AddCookie(newCookie)
	response := httptest.NewRecorder()
	manager.Require(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })).ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("new-policy session status = %d", response.Code)
	}
	if cookies := response.Result().Cookies(); len(cookies) != 0 {
		t.Fatalf("background authenticated request unexpectedly refreshed session: %#v", cookies)
	}
	refreshResponse := httptest.NewRecorder()
	if err := manager.RefreshCurrentSession(refreshResponse, request); err != nil {
		t.Fatal(err)
	}
	cookies := refreshResponse.Result().Cookies()
	if len(cookies) != 1 || cookies[0].MaxAge != int((8*time.Hour)/time.Second) {
		t.Fatalf("refreshed idle cookie = %#v", cookies)
	}
}

func TestPasswordHashSyncCannotRestoreStaleCredentialAfterWebChange(t *testing.T) {
	manager := newAuthManager(t, nil)
	durableHash := testPasswordHash
	loaderStarted := make(chan struct{})
	releaseLoader := make(chan struct{})
	syncDone := make(chan error, 1)
	go func() {
		_, err := manager.SyncPasswordHash(func() (string, bool, error) {
			close(loaderStarted)
			<-releaseLoader
			return durableHash, true, nil
		})
		syncDone <- err
	}()
	<-loaderStarted

	changeDone := make(chan error, 1)
	go func() {
		request := loginRequest("192.0.2.50:4123")
		_, err := manager.ChangePassword(request, "correct-password", "replacement-password", func(hash string) error {
			durableHash = hash
			return nil
		})
		changeDone <- err
	}()
	close(releaseLoader)
	if err := <-syncDone; err != nil {
		t.Fatal(err)
	}
	if err := <-changeDone; err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(durableHash, "replacement-password") {
		t.Fatal("durable credential was not updated")
	}
	if err := manager.Login(httptest.NewRecorder(), loginRequest("192.0.2.51:4123"), "admin", "replacement-password"); err != nil {
		t.Fatalf("replacement password login = %v", err)
	}
	if err := manager.Login(httptest.NewRecorder(), loginRequest("192.0.2.52:4123"), "admin", "correct-password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("stale password login = %v, want ErrInvalidCredentials", err)
	}
}

func TestRefreshSessionRejectsVersionReplacedByRecovery(t *testing.T) {
	manager := newAuthManager(t, nil)
	request := loginRequest("192.0.2.60:4123")
	webVersion, err := manager.ChangePassword(request, "correct-password", "web-password-123", nil)
	if err != nil {
		t.Fatal(err)
	}
	recoveryHash, err := HashPassword("recovery-password-123")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SyncPasswordHash(func() (string, bool, error) { return recoveryHash, true, nil }); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	if err := manager.RefreshSession(response, request, webVersion); !errors.Is(err, ErrCredentialChanged) {
		t.Fatalf("RefreshSession() = %v, want ErrCredentialChanged", err)
	}
	if len(response.Result().Cookies()) != 0 {
		t.Fatalf("stale password change received cookies: %#v", response.Result().Cookies())
	}
}
