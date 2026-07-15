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
	Username  string `json:"u"`
	ExpiresAt int64  `json:"exp"`
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
	if passwordHash == "" {
		if cfg.Password == "" {
			return nil, fmt.Errorf("auth is enabled; set WATCHBELL_ADMIN_PASSWORD or WATCHBELL_ADMIN_PASSWORD_HASH, or set WATCHBELL_AUTH_DISABLED=true for local-only development")
		}
		hash, err := HashPassword(cfg.Password)
		if err != nil {
			return nil, err
		}
		passwordHash = hash
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
	if username != m.username || !VerifyPassword(m.passwordHash, password) {
		m.loginFailures.record(client)
		return ErrInvalidCredentials
	}
	m.loginFailures.reset(client)
	value, err := m.signSession(sessionPayload{
		Username:  username,
		ExpiresAt: time.Now().Add(m.sessionTTL).Unix(),
	})
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     m.cookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   m.secureCookie(r),
		MaxAge:   int(m.sessionTTL.Seconds()),
	})
	return nil
}

func (m *Manager) Logout(w http.ResponseWriter, r *http.Request) {
	if m == nil {
		return
	}
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
	if !ok || payload.Username != m.username || time.Now().Unix() > payload.ExpiresAt {
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

func VerifyPassword(encoded string, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations < 100_000 {
		return false
	}
	salt, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	expected, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	actual := pbkdf2SHA256([]byte(password), salt, iterations, len(expected))
	return subtle.ConstantTimeCompare(actual, expected) == 1
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
