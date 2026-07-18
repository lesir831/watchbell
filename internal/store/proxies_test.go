package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/watchbell/watchbell/internal/model"
)

func TestProxyProfileLifecycleAndMonitorReference(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, t.TempDir()+"/watchbell.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	profile, err := db.CreateProxyProfile(ctx, model.ProxyProfileInput{
		Name: "Outbound", Type: model.ProxyTypeSOCKS5, Host: "127.0.0.1", Port: 1080,
		Username: "watchbell", Password: "proxy-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if profile.Password != "proxy-secret" || profile.Type != model.ProxyTypeSOCKS5 {
		t.Fatalf("created proxy = %#v", profile)
	}
	monitor, err := db.CreateMonitor(ctx, model.MonitorInput{
		Name: "Feed", Type: model.MonitorTypeRSS, ProxyID: &profile.ID, Enabled: true, IntervalSeconds: 300,
		Config: json.RawMessage(`{"url":"https://example.com/feed.xml"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if monitor.ProxyID == nil || *monitor.ProxyID != profile.ID {
		t.Fatalf("monitor proxy = %#v", monitor.ProxyID)
	}
	if err := db.DeleteProxyProfile(ctx, profile.ID); !errors.Is(err, ErrProxyInUse) {
		t.Fatalf("delete referenced proxy = %v, want ErrProxyInUse", err)
	}

	monitor.ProxyID = nil
	if _, err := db.UpdateMonitor(ctx, monitor.ID, model.MonitorInput{
		Name: monitor.Name, Type: monitor.Type, Enabled: monitor.Enabled, IntervalSeconds: monitor.IntervalSeconds,
		Config: monitor.Config,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteProxyProfile(ctx, profile.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetProxyProfile(ctx, profile.ID); !IsNotFound(err) {
		t.Fatalf("archived proxy lookup = %v, want not found", err)
	}
}

func TestProxyProfilesNormalizeNamesAndRejectArchivedReferences(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, t.TempDir()+"/watchbell.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	profile, err := db.CreateProxyProfile(ctx, model.ProxyProfileInput{
		Name: "Outbound", Type: "HTTP", Host: "[::1]", Port: 8080,
	})
	if err != nil {
		t.Fatal(err)
	}
	if profile.Type != model.ProxyTypeHTTP || profile.Host != "::1" {
		t.Fatalf("normalized proxy = %#v", profile)
	}
	if _, err := db.CreateProxyProfile(ctx, model.ProxyProfileInput{
		Name: "Outbound", Type: model.ProxyTypeHTTP, Host: "proxy.example.com", Port: 8080,
	}); !errors.Is(err, ErrDuplicateNaturalKey) {
		t.Fatalf("duplicate proxy = %v, want ErrDuplicateNaturalKey", err)
	}
	other, err := db.CreateProxyProfile(ctx, model.ProxyProfileInput{
		Name: "Other", Type: model.ProxyTypeHTTP, Host: "proxy.example.com", Port: 8080,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpdateProxyProfile(ctx, other.ID, model.ProxyProfileInput{
		Name: "Outbound", Type: model.ProxyTypeHTTP, Host: "proxy.example.com", Port: 8080,
	}); !errors.Is(err, ErrDuplicateNaturalKey) {
		t.Fatalf("duplicate proxy update = %v, want ErrDuplicateNaturalKey", err)
	}
	if _, err := db.UpdateProxyProfile(ctx, 999999, model.ProxyProfileInput{
		Name: "Missing", Type: model.ProxyTypeHTTP, Host: "proxy.example.com", Port: 8080,
	}); !IsNotFound(err) {
		t.Fatalf("missing proxy update = %v, want not found", err)
	}
	if err := db.DeleteProxyProfile(ctx, profile.ID); err != nil {
		t.Fatal(err)
	}
	input := model.MonitorInput{
		Name: "Feed", Type: model.MonitorTypeRSS, ProxyID: &profile.ID, Enabled: true, IntervalSeconds: 300,
		Config: json.RawMessage(`{"url":"https://example.com/feed.xml"}`),
	}
	if _, err := db.CreateMonitor(ctx, input); !errors.Is(err, ErrProxyUnavailable) {
		t.Fatalf("monitor with archived proxy = %v, want ErrProxyUnavailable", err)
	}

	input.ProxyID = nil
	monitor, err := db.CreateMonitor(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	input.ProxyID = &profile.ID
	if _, err := db.UpdateMonitor(ctx, monitor.ID, input); !errors.Is(err, ErrProxyUnavailable) {
		t.Fatalf("update to archived proxy = %v, want ErrProxyUnavailable", err)
	}
}

func TestChangingProxyPreservesMonitorDeduplicationState(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, t.TempDir()+"/watchbell.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	first, err := db.CreateProxyProfile(ctx, model.ProxyProfileInput{Name: "First", Type: model.ProxyTypeHTTP, Host: "first.example.com", Port: 8080})
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.CreateProxyProfile(ctx, model.ProxyProfileInput{Name: "Second", Type: model.ProxyTypeHTTP, Host: "second.example.com", Port: 8080})
	if err != nil {
		t.Fatal(err)
	}
	input := model.MonitorInput{
		Name: "Feed", Type: model.MonitorTypeRSS, ProxyID: &first.ID, Enabled: true, IntervalSeconds: 300,
		Config: json.RawMessage(`{"url":"https://example.com/feed.xml"}`),
	}
	monitor, err := db.CreateMonitor(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	state := map[string]any{"seen": []string{"entry-1"}}
	if err := db.UpdateMonitorCheckResult(ctx, monitor.ID, model.CheckResult{Status: "ok", State: state}, nil); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateMonitorCheckResult(ctx, monitor.ID, model.CheckResult{}, errors.New("proxy unavailable")); err != nil {
		t.Fatal(err)
	}
	input.ProxyID = &second.ID
	updated, err := db.UpdateMonitor(ctx, monitor.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if updated.LastCheckedAt == nil || !strings.Contains(string(updated.State), "entry-1") {
		t.Fatalf("proxy failure or proxy change reset monitor state: %#v", updated)
	}
}

func TestPersistedAuthPasswordHashIsAudited(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, t.TempDir()+"/watchbell.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, exists, err := db.GetAuthPasswordHash(ctx); err != nil || exists {
		t.Fatalf("initial password setting = exists %v err %v", exists, err)
	}
	const passwordHash = "pbkdf2-sha256$210000$salt$hash"
	if err := db.SetAuthPasswordHashAudited(ctx, passwordHash, "admin"); err != nil {
		t.Fatal(err)
	}
	stored, exists, err := db.GetAuthPasswordHash(ctx)
	if err != nil || !exists || stored != passwordHash {
		t.Fatalf("stored password setting = %q exists=%v err=%v", stored, exists, err)
	}
	logs, err := db.ListAuditLogs(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 || logs[0].EntityType != "account" || logs[0].Summary != "修改管理员密码" || strings.Contains(string(logs[0].Changes), passwordHash) {
		t.Fatalf("password audit = %#v", logs)
	}
}
