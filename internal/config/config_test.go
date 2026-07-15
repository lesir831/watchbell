package config

import (
	"testing"
	"time"
)

func TestAuthSecurityConfigurationFromEnv(t *testing.T) {
	t.Setenv("WATCHBELL_COOKIE_SECURE", "true")
	t.Setenv("WATCHBELL_TRUST_PROXY_HEADERS", "true")
	t.Setenv("WATCHBELL_TRUSTED_PROXY_HOPS", "2")
	t.Setenv("WATCHBELL_LOGIN_MAX_FAILURES", "7")
	t.Setenv("WATCHBELL_LOGIN_FAILURE_WINDOW", "9m")

	cfg := FromEnv()
	if cfg.Auth.CookieSecure == nil || !*cfg.Auth.CookieSecure {
		t.Fatalf("CookieSecure = %#v, want true", cfg.Auth.CookieSecure)
	}
	if !cfg.Auth.TrustProxyHeaders {
		t.Fatal("TrustProxyHeaders = false, want true")
	}
	if cfg.Auth.TrustedProxyHops != 2 {
		t.Fatalf("TrustedProxyHops = %d, want 2", cfg.Auth.TrustedProxyHops)
	}
	if cfg.Auth.LoginMaxFailures != 7 {
		t.Fatalf("LoginMaxFailures = %d, want 7", cfg.Auth.LoginMaxFailures)
	}
	if cfg.Auth.LoginFailureWindow != 9*time.Minute {
		t.Fatalf("LoginFailureWindow = %v, want 9m", cfg.Auth.LoginFailureWindow)
	}
}

func TestCookieSecureSupportsExplicitFalseAndAutomaticMode(t *testing.T) {
	t.Setenv("WATCHBELL_COOKIE_SECURE", "false")
	cfg := FromEnv()
	if cfg.Auth.CookieSecure == nil || *cfg.Auth.CookieSecure {
		t.Fatalf("CookieSecure = %#v, want explicit false", cfg.Auth.CookieSecure)
	}

	t.Setenv("WATCHBELL_COOKIE_SECURE", "")
	cfg = FromEnv()
	if cfg.Auth.CookieSecure != nil {
		t.Fatalf("CookieSecure = %#v, want automatic mode", cfg.Auth.CookieSecure)
	}
	if cfg.Auth.TrustProxyHeaders {
		t.Fatal("TrustProxyHeaders defaults to true")
	}
	if cfg.Auth.TrustedProxyHops != 1 {
		t.Fatalf("TrustedProxyHops = %d, want 1", cfg.Auth.TrustedProxyHops)
	}
}

func TestAuthSecurityConfigurationDefaults(t *testing.T) {
	t.Setenv("WATCHBELL_COOKIE_SECURE", "invalid")
	t.Setenv("WATCHBELL_LOGIN_MAX_FAILURES", "invalid")
	t.Setenv("WATCHBELL_LOGIN_FAILURE_WINDOW", "invalid")

	cfg := FromEnv()
	if cfg.Auth.CookieSecure != nil {
		t.Fatalf("CookieSecure = %#v, want automatic mode", cfg.Auth.CookieSecure)
	}
	if cfg.Auth.LoginMaxFailures != 5 {
		t.Fatalf("LoginMaxFailures = %d, want 5", cfg.Auth.LoginMaxFailures)
	}
	if cfg.Auth.LoginFailureWindow != 15*time.Minute {
		t.Fatalf("LoginFailureWindow = %v, want 15m", cfg.Auth.LoginFailureWindow)
	}
}
