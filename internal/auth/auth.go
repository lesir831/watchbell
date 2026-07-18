package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5/middleware"
)

const (
	defaultUsername        = "admin"
	defaultCookieName      = "watchbell_session"
	defaultPBKDF2Iter      = 210_000
	defaultPasswordKeySize = 32
	defaultMaxFailures     = 5
	defaultFailureWindow   = 15 * time.Minute
)

var ErrInvalidCredentials = errors.New("invalid username or password")
var ErrPasswordUnchanged = errors.New("new password must differ from the current password")
var ErrPasswordTooShort = errors.New("password must contain at least 8 characters")
var ErrPasswordTooLong = errors.New("password is too long")
var ErrCredentialChanged = errors.New("credential changed concurrently")

type Config struct {
	Enabled            bool
	Username           string
	Password           string
	PasswordHash       string
	SessionSecret      string
	SessionTTL         time.Duration
	CookieName         string
	CookieSecure       *bool
	TrustProxyHeaders  bool
	TrustedProxyHops   int
	LoginMaxFailures   int
	LoginFailureWindow time.Duration
}

type Manager struct {
	enabled           bool
	username          string
	passwordHash      string
	credentialVersion string
	credentialsMu     sync.RWMutex
	sessionSecret     []byte
	sessionTTL        time.Duration
	cookieName        string
	cookieSecure      *bool
	trustProxyHeaders bool
	trustedProxyHops  int
	loginFailures     *failureLimiter
	logger            *slog.Logger
}

// RateLimitError indicates that this client has made too many failed login
// attempts. RetryAfter is always positive.
type RateLimitError struct {
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("too many failed login attempts; retry after %s", e.RetryAfter.Round(time.Second))
}

type failureLimiter struct {
	mu        sync.Mutex
	failures  map[string][]time.Time
	max       int
	window    time.Duration
	now       func() time.Time
	lastSweep time.Time
}

type contextKey struct{}

type sessionPayload struct {
	Username   string `json:"u"`
	ExpiresAt  int64  `json:"exp"`
	Version    string `json:"v"`
	TTLSeconds int64  `json:"ttl"`
}

func NewManager(cfg Config, logger *slog.Logger) (*Manager, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.TrustedProxyHops <= 0 {
		cfg.TrustedProxyHops = 1
	}
	if !cfg.Enabled {
		return &Manager{enabled: false, trustProxyHeaders: cfg.TrustProxyHeaders, trustedProxyHops: cfg.TrustedProxyHops, logger: logger}, nil
	}
	if strings.TrimSpace(cfg.Username) == "" {
		cfg.Username = defaultUsername
	}
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = 7 * 24 * time.Hour
	}
	if strings.TrimSpace(cfg.CookieName) == "" {
		cfg.CookieName = defaultCookieName
	}
	if cfg.LoginMaxFailures <= 0 {
		cfg.LoginMaxFailures = defaultMaxFailures
	}
	if cfg.LoginFailureWindow <= 0 {
		cfg.LoginFailureWindow = defaultFailureWindow
	}
	passwordHash := strings.TrimSpace(cfg.PasswordHash)
	credentialMaterial := "hash:" + passwordHash
	if passwordHash == "" {
		if cfg.Password == "" {
			return nil, fmt.Errorf("auth is enabled; set WATCHBELL_ADMIN_PASSWORD or WATCHBELL_ADMIN_PASSWORD_HASH, or set WATCHBELL_AUTH_DISABLED=true for local-only development")
		}
		credentialMaterial = "password:" + cfg.Password
		hash, err := HashPassword(cfg.Password)
		if err != nil {
			return nil, err
		}
		passwordHash = hash
	} else if err := validatePasswordHash(passwordHash); err != nil {
		return nil, fmt.Errorf("invalid administrator password hash: %w", err)
	}
	secret := []byte(cfg.SessionSecret)
	if len(secret) == 0 {
		secret = make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return nil, err
		}
		logger.Warn("WATCHBELL_SESSION_SECRET is not set; generated sessions will be invalid after restart")
	} else if len(secret) < 32 {
		return nil, fmt.Errorf("WATCHBELL_SESSION_SECRET must be at least 32 bytes when set")
	}
	return &Manager{
		enabled:           true,
		username:          cfg.Username,
		passwordHash:      passwordHash,
		credentialVersion: credentialVersion(secret, credentialMaterial),
		sessionSecret:     secret,
		sessionTTL:        cfg.SessionTTL,
		cookieName:        cfg.CookieName,
		cookieSecure:      cloneBool(cfg.CookieSecure),
		trustProxyHeaders: cfg.TrustProxyHeaders,
		trustedProxyHops:  cfg.TrustedProxyHops,
		loginFailures: &failureLimiter{
			failures: make(map[string][]time.Time),
			max:      cfg.LoginMaxFailures,
			window:   cfg.LoginFailureWindow,
			now:      time.Now,
		},
		logger: logger,
	}, nil
}

func (m *Manager) Enabled() bool {
	return m != nil && m.enabled
}

func (m *Manager) Username() string {
	if m == nil {
		return ""
	}
	return m.username
}

func (m *Manager) SessionTTL() time.Duration {
	if m == nil {
		return 0
	}
	m.credentialsMu.RLock()
	defer m.credentialsMu.RUnlock()
	return m.sessionTTL
}

// SetSessionTTL applies the idle-session policy immediately. Session payloads
// include the policy duration, so changing it invalidates other sessions that
// were issued under the previous policy.
func (m *Manager) SetSessionTTL(ttl time.Duration) error {
	if ttl <= 0 {
		return fmt.Errorf("session TTL must be positive")
	}
	if !m.Enabled() {
		return nil
	}
	m.credentialsMu.Lock()
	m.sessionTTL = ttl
	m.credentialsMu.Unlock()
	return nil
}

func (m *Manager) TrustProxyHeaders() bool {
	return m != nil && m.trustProxyHeaders
}

func (m *Manager) TrustedProxyHops() int {
	if m == nil || m.trustedProxyHops <= 0 {
		return 1
	}
	return m.trustedProxyHops
}

func (m *Manager) Login(w http.ResponseWriter, r *http.Request, username string, password string) error {
	if !m.Enabled() {
		return fmt.Errorf("auth is disabled")
	}
	client := loginClientKey(r)
	if retryAfter := m.loginFailures.retryAfter(client); retryAfter > 0 {
		return &RateLimitError{RetryAfter: retryAfter}
	}
	m.credentialsMu.RLock()
	valid := username == m.username && VerifyPassword(m.passwordHash, password)
	if !valid {
		m.credentialsMu.RUnlock()
		m.loginFailures.record(client)
		return ErrInvalidCredentials
	}
	ttl := m.sessionTTL
	value, err := m.signSession(sessionPayload{
		Username:   username,
		ExpiresAt:  time.Now().Add(ttl).Unix(),
		Version:    m.credentialVersion,
		TTLSeconds: int64(ttl / time.Second),
	})
	m.credentialsMu.RUnlock()
	if err != nil {
		return err
	}
	m.loginFailures.reset(client)
	m.setSessionCookie(w, r, value, ttl)
	return nil
}

// ChangePassword verifies the current credential, persists the replacement
// hash, and only then activates it in memory. The callback lets callers make
// persistence and audit logging atomic without coupling auth to a database.
func (m *Manager) ChangePassword(r *http.Request, currentPassword, newPassword string, persist func(string) error) (string, error) {
	if !m.Enabled() {
		return "", fmt.Errorf("auth is disabled")
	}
	client := loginClientKey(r)
	if retryAfter := m.loginFailures.retryAfter(client); retryAfter > 0 {
		return "", &RateLimitError{RetryAfter: retryAfter}
	}
	m.credentialsMu.Lock()
	defer m.credentialsMu.Unlock()
	if !VerifyPassword(m.passwordHash, currentPassword) {
		m.loginFailures.record(client)
		return "", ErrInvalidCredentials
	}
	if VerifyPassword(m.passwordHash, newPassword) {
		return "", ErrPasswordUnchanged
	}
	if err := ValidatePassword(newPassword); err != nil {
		return "", err
	}
	passwordHash, err := HashPassword(newPassword)
	if err != nil {
		return "", err
	}
	if persist != nil {
		if err := persist(passwordHash); err != nil {
			return "", err
		}
	}
	m.passwordHash = passwordHash
	m.credentialVersion = credentialVersion(m.sessionSecret, "hash:"+passwordHash)
	m.loginFailures.reset(client)
	return m.credentialVersion, nil
}

// SyncPasswordHash serializes loading the durable credential with in-process
// password changes. Keeping the load inside the credential lock prevents a
// delayed poll from restoring a stale hash after a concurrent web change.
func (m *Manager) SyncPasswordHash(load func() (string, bool, error)) (bool, error) {
	if !m.Enabled() || load == nil {
		return false, nil
	}
	m.credentialsMu.Lock()
	defer m.credentialsMu.Unlock()
	passwordHash, exists, err := load()
	if err != nil || !exists {
		return false, err
	}
	return m.reloadPasswordHashLocked(passwordHash)
}

func (m *Manager) reloadPasswordHashLocked(passwordHash string) (bool, error) {
	passwordHash = strings.TrimSpace(passwordHash)
	if err := validatePasswordHash(passwordHash); err != nil {
		return false, err
	}
	if passwordHash == m.passwordHash {
		return false, nil
	}
	m.passwordHash = passwordHash
	m.credentialVersion = credentialVersion(m.sessionSecret, "hash:"+passwordHash)
	return true, nil
}

// RefreshSession issues a cookie only if the expected password version is
// still current. An out-of-process recovery change must never accidentally
// grant its new session version to a browser that does not know that password.
func (m *Manager) RefreshSession(w http.ResponseWriter, r *http.Request, expectedVersion string) error {
	if !m.Enabled() {
		return fmt.Errorf("auth is disabled")
	}
	m.credentialsMu.RLock()
	if expectedVersion == "" || expectedVersion != m.credentialVersion {
		m.credentialsMu.RUnlock()
		return ErrCredentialChanged
	}
	ttl := m.sessionTTL
	value, err := m.signSession(sessionPayload{
		Username:   m.username,
		ExpiresAt:  time.Now().Add(ttl).Unix(),
		Version:    m.credentialVersion,
		TTLSeconds: int64(ttl / time.Second),
	})
	m.credentialsMu.RUnlock()
	if err != nil {
		return err
	}
	m.setSessionCookie(w, r, value, ttl)
	return nil
}

// RefreshCurrentSession reissues the authenticated request's cookie using the
// current idle timeout. It is used after a policy change so the browser that
// made the change remains signed in while sessions using the old policy stop.
func (m *Manager) RefreshCurrentSession(w http.ResponseWriter, r *http.Request) error {
	if !m.Enabled() {
		return nil
	}
	username, ok := m.User(r)
	if !ok {
		return ErrInvalidCredentials
	}
	return m.issueSession(w, r, username)
}

func (m *Manager) issueSession(w http.ResponseWriter, r *http.Request, username string) error {
	m.credentialsMu.RLock()
	if username != m.username {
		m.credentialsMu.RUnlock()
		return ErrInvalidCredentials
	}
	ttl := m.sessionTTL
	value, err := m.signSession(sessionPayload{
		Username:   username,
		ExpiresAt:  time.Now().Add(ttl).Unix(),
		Version:    m.credentialVersion,
		TTLSeconds: int64(ttl / time.Second),
	})
	m.credentialsMu.RUnlock()
	if err != nil {
		return err
	}
	m.setSessionCookie(w, r, value, ttl)
	return nil
}

func (m *Manager) setSessionCookie(w http.ResponseWriter, r *http.Request, value string, ttl time.Duration) {
	m.removePendingSessionCookie(w)
	http.SetCookie(w, &http.Cookie{
		Name:     m.cookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   m.secureCookie(r),
		MaxAge:   int(ttl.Seconds()),
	})
}

func (m *Manager) removePendingSessionCookie(w http.ResponseWriter) {
	if m == nil || w == nil {
		return
	}
	values := w.Header().Values("Set-Cookie")
	w.Header().Del("Set-Cookie")
	prefix := m.cookieName + "="
	for _, value := range values {
		if !strings.HasPrefix(value, prefix) {
			w.Header().Add("Set-Cookie", value)
		}
	}
}

func (m *Manager) Logout(w http.ResponseWriter, r *http.Request) {
	if m == nil {
		return
	}
	m.removePendingSessionCookie(w)
	http.SetCookie(w, &http.Cookie{
		Name:     m.cookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   m.secureCookie(r),
		MaxAge:   -1,
	})
}

// LoginRetryAfter extracts retry timing from a failed Login call.
func LoginRetryAfter(err error) (time.Duration, bool) {
	var rateLimitErr *RateLimitError
	if !errors.As(err, &rateLimitErr) {
		return 0, false
	}
	return rateLimitErr.RetryAfter, true
}

func (m *Manager) secureCookie(r *http.Request) bool {
	if m != nil && m.cookieSecure != nil {
		return *m.cookieSecure
	}
	if requestUsesDirectHTTPS(r) {
		return true
	}
	return m != nil && m.trustProxyHeaders && requestUsesForwardedHTTPS(r)
}

// RequestUsesHTTPS reports the effective request scheme using the same trusted
// proxy boundary as secure-cookie detection. API middleware uses this when
// comparing a browser Origin header with the request origin.
func (m *Manager) RequestUsesHTTPS(r *http.Request) bool {
	if requestUsesDirectHTTPS(r) {
		return true
	}
	return m != nil && m.trustProxyHeaders && requestUsesForwardedHTTPS(r)
}

func requestUsesDirectHTTPS(r *http.Request) bool {
	if r == nil {
		return false
	}
	return r.TLS != nil || (r.URL != nil && strings.EqualFold(r.URL.Scheme, "https"))
}

func requestUsesForwardedHTTPS(r *http.Request) bool {
	if r == nil {
		return false
	}
	if forwardedProto(strings.Join(r.Header.Values("Forwarded"), ",")) == "https" {
		return true
	}
	proto := strings.Trim(lastForwardedValue(strings.Join(r.Header.Values("X-Forwarded-Proto"), ",")), `"`)
	return strings.EqualFold(proto, "https")
}

func forwardedProto(header string) string {
	last := lastForwardedValue(header)
	for _, parameter := range strings.Split(last, ";") {
		key, value, ok := strings.Cut(strings.TrimSpace(parameter), "=")
		if ok && strings.EqualFold(key, "proto") {
			return strings.ToLower(strings.Trim(strings.TrimSpace(value), `"`))
		}
	}
	return ""
}

func lastForwardedValue(header string) string {
	values := strings.Split(header, ",")
	for index := len(values) - 1; index >= 0; index-- {
		if value := strings.TrimSpace(values[index]); value != "" {
			return value
		}
	}
	return ""
}

func loginClientKey(r *http.Request) string {
	if r == nil {
		return "unknown"
	}
	if address := middleware.GetClientIP(r.Context()); address != "" {
		return address
	}
	address := strings.TrimSpace(r.RemoteAddr)
	if host, _, err := net.SplitHostPort(address); err == nil {
		address = host
	}
	if address == "" {
		return "unknown"
	}
	return address
}

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func (l *failureLimiter) retryAfter(key string) time.Duration {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.sweepLocked(now)
	failures := l.activeLocked(key, now)
	if len(failures) < l.max {
		return 0
	}
	retryAfter := failures[0].Add(l.window).Sub(now)
	if retryAfter <= 0 {
		return time.Nanosecond
	}
	return retryAfter
}

func (l *failureLimiter) record(key string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.sweepLocked(now)
	failures := l.activeLocked(key, now)
	if len(failures) >= l.max {
		return
	}
	l.failures[key] = append(failures, now)
}

func (l *failureLimiter) reset(key string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	delete(l.failures, key)
	l.mu.Unlock()
}

func (l *failureLimiter) activeLocked(key string, now time.Time) []time.Time {
	cutoff := now.Add(-l.window)
	failures := l.failures[key]
	firstActive := 0
	for firstActive < len(failures) && !failures[firstActive].After(cutoff) {
		firstActive++
	}
	if firstActive == len(failures) {
		delete(l.failures, key)
		return nil
	}
	if firstActive > 0 {
		failures = append([]time.Time(nil), failures[firstActive:]...)
		l.failures[key] = failures
	}
	return failures
}

func (l *failureLimiter) sweepLocked(now time.Time) {
	if !l.lastSweep.IsZero() && now.Sub(l.lastSweep) < l.window {
		return
	}
	for key := range l.failures {
		l.activeLocked(key, now)
	}
	l.lastSweep = now
}

func (m *Manager) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !m.Enabled() {
			next.ServeHTTP(w, r)
			return
		}
		username, ok := m.verifyRequest(r)
		if !ok {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "unauthorized"})
			return
		}
		ctx := context.WithValue(r.Context(), contextKey{}, username)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (m *Manager) User(r *http.Request) (string, bool) {
	if !m.Enabled() {
		return "", false
	}
	if username, ok := r.Context().Value(contextKey{}).(string); ok && username != "" {
		return username, true
	}
	return m.verifyRequest(r)
}

func (m *Manager) verifyRequest(r *http.Request) (string, bool) {
	cookie, err := r.Cookie(m.cookieName)
	if err != nil || cookie.Value == "" {
		return "", false
	}
	payload, ok := m.verifySession(cookie.Value)
	if !ok || time.Now().Unix() > payload.ExpiresAt {
		return "", false
	}
	m.credentialsMu.RLock()
	valid := payload.Username == m.username && payload.Version == m.credentialVersion &&
		payload.TTLSeconds == int64(m.sessionTTL/time.Second)
	m.credentialsMu.RUnlock()
	if !valid {
		return "", false
	}
	return payload.Username, true
}

func (m *Manager) signSession(payload sessionPayload) (string, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	encodedPayload := base64.RawURLEncoding.EncodeToString(data)
	sig := m.sign(encodedPayload)
	return encodedPayload + "." + sig, nil
}

func (m *Manager) verifySession(value string) (sessionPayload, bool) {
	var payload sessionPayload
	parts := strings.Split(value, ".")
	if len(parts) != 2 {
		return payload, false
	}
	expected := m.sign(parts[0])
	if subtle.ConstantTimeCompare([]byte(expected), []byte(parts[1])) != 1 {
		return payload, false
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return payload, false
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return payload, false
	}
	return payload, true
}

func (m *Manager) sign(value string) string {
	mac := hmac.New(sha256.New, m.sessionSecret)
	_, _ = mac.Write([]byte(value))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func HashPassword(password string) (string, error) {
	if password == "" {
		return "", fmt.Errorf("password cannot be empty")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := pbkdf2SHA256([]byte(password), salt, defaultPBKDF2Iter, defaultPasswordKeySize)
	return strings.Join([]string{
		"pbkdf2-sha256",
		strconv.Itoa(defaultPBKDF2Iter),
		base64.RawURLEncoding.EncodeToString(salt),
		base64.RawURLEncoding.EncodeToString(key),
	}, "$"), nil
}

func ValidatePassword(password string) error {
	if utf8.RuneCountInString(password) < 8 {
		return ErrPasswordTooShort
	}
	if len(password) > 1024 {
		return ErrPasswordTooLong
	}
	return nil
}

func VerifyPassword(encoded string, password string) bool {
	iterations, salt, expected, err := parsePasswordHash(encoded)
	if err != nil {
		return false
	}
	actual := pbkdf2SHA256([]byte(password), salt, iterations, len(expected))
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

func validatePasswordHash(encoded string) error {
	_, _, _, err := parsePasswordHash(encoded)
	return err
}

func parsePasswordHash(encoded string) (int, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return 0, nil, nil, errors.New("unsupported password hash format")
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations < 100_000 || iterations > 10_000_000 {
		return 0, nil, nil, errors.New("invalid PBKDF2 iteration count")
	}
	salt, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(salt) < 16 || len(salt) > 1024 {
		return 0, nil, nil, errors.New("invalid password hash salt")
	}
	expected, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil || len(expected) < 32 || len(expected) > 128 {
		return 0, nil, nil, errors.New("invalid password hash key")
	}
	return iterations, salt, expected, nil
}

func credentialVersion(secret []byte, material string) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte("watchbell-credential-version\x00"))
	_, _ = mac.Write([]byte(material))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func pbkdf2SHA256(password []byte, salt []byte, iterations int, keyLen int) []byte {
	hLen := sha256.Size
	numBlocks := (keyLen + hLen - 1) / hLen
	var derived []byte
	for block := 1; block <= numBlocks; block++ {
		u := pbkdf2Block(password, salt, iterations, block)
		derived = append(derived, u...)
	}
	return derived[:keyLen]
}

func pbkdf2Block(password []byte, salt []byte, iterations int, block int) []byte {
	mac := hmac.New(sha256.New, password)
	_, _ = mac.Write(salt)
	var blockBuf [4]byte
	binary.BigEndian.PutUint32(blockBuf[:], uint32(block))
	_, _ = mac.Write(blockBuf[:])
	u := mac.Sum(nil)
	out := make([]byte, len(u))
	copy(out, u)
	for i := 1; i < iterations; i++ {
		mac = hmac.New(sha256.New, password)
		_, _ = mac.Write(u)
		u = mac.Sum(nil)
		for j := range out {
			out[j] ^= u[j]
		}
	}
	return out
}
