package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/watchbell/watchbell/internal/checker"
	"github.com/watchbell/watchbell/internal/model"
	"github.com/watchbell/watchbell/internal/notifier"
	"github.com/watchbell/watchbell/internal/scheduler"
	"github.com/watchbell/watchbell/internal/store"
)

func TestConfigBackupRoundTripRedactionAndMerge(t *testing.T) {
	ctx := context.Background()
	sourceServer, sourceStore := newTestServer(t)
	proxyProfile, err := sourceStore.CreateProxyProfile(ctx, model.ProxyProfileInput{
		Name: "Release proxy", Type: model.ProxyTypeHTTP, Host: "proxy.example.com", Port: 8080,
		Username: "proxy-user", Password: "proxy-secret-password",
	})
	if err != nil {
		t.Fatal(err)
	}
	monitor, err := sourceStore.CreateMonitor(ctx, model.MonitorInput{
		Name: "Releases", Type: model.MonitorTypeGitHubRelease, ProxyID: &proxyProfile.ID, Enabled: true, IntervalSeconds: 300,
		Config: json.RawMessage(`{"repository":"watchbell/project","apiUrl":"https://api.github.com","timeoutSeconds":15,"maxReleases":20,"token":"github-secret-token"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	channel, err := sourceStore.CreateNotifyChannel(ctx, model.NotifyChannelInput{
		Name: "Phone", Type: model.ChannelTypeBark, Enabled: true,
		Config: json.RawMessage(`{"serverUrl":"https://api.day.app","deviceKey":"bark-secret-key","group":"WatchBell"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	monitor, err = sourceStore.UpdateMonitor(ctx, monitor.ID, model.MonitorInput{
		Name: monitor.Name, Type: monitor.Type, ProxyID: monitor.ProxyID, Enabled: monitor.Enabled, IntervalSeconds: monitor.IntervalSeconds, Config: monitor.Config,
		FailureAlertAfter: 2, FailureNotifyChannelIDs: []int64{channel.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	template, err := sourceStore.CreateNotificationTemplate(ctx, model.NotificationTemplateInput{
		Name: "Release details", SubjectTemplate: "${github.release.tagName}", BodyTemplate: "${github.release.url}",
	})
	if err != nil {
		t.Fatal(err)
	}
	templateID := template.ID
	ruleItem, err := sourceStore.CreateRule(ctx, model.RuleInput{
		MonitorID: monitor.ID, Name: "Stable releases", Enabled: true,
		Condition:        json.RawMessage(`{"match":"all","conditions":[{"field":"github.release.tagName","operator":"contains","value":"v"}]}`),
		NotifyChannelIDs: []int64{channel.ID}, TemplateID: &templateID, CooldownSeconds: 60,
		QuietHours: model.QuietHours{Enabled: true, Start: "23:00", End: "07:00", Timezone: "Asia/Shanghai"},
	})
	if err != nil {
		t.Fatal(err)
	}

	redactedResponse, err := http.Get(sourceServer.URL + "/api/config/export")
	if err != nil {
		t.Fatal(err)
	}
	redactedBody, _ := io.ReadAll(redactedResponse.Body)
	redactedResponse.Body.Close()
	if redactedResponse.StatusCode != http.StatusOK {
		t.Fatalf("redacted export status = %d body = %s", redactedResponse.StatusCode, redactedBody)
	}
	if !stringsContain(redactedResponse.Header.Get("Content-Disposition"), "watchbell-config-") || redactedResponse.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("missing safe download headers: %#v", redactedResponse.Header)
	}
	if bytes.Contains(redactedBody, []byte("github-secret-token")) || bytes.Contains(redactedBody, []byte("bark-secret-key")) || bytes.Contains(redactedBody, []byte("proxy-secret-password")) {
		t.Fatalf("default export leaked a secret: %s", redactedBody)
	}
	var redactedBackup model.ConfigBackup
	if err := json.Unmarshal(redactedBody, &redactedBackup); err != nil {
		t.Fatal(err)
	}
	if redactedBackup.IncludesSecrets {
		t.Fatal("default export must declare includesSecrets=false")
	}
	if len(redactedBackup.Monitors) != 1 || !equalStrings(redactedBackup.Monitors[0].RedactedSecrets, []string{"token"}) {
		t.Fatalf("monitor redaction metadata = %#v", redactedBackup.Monitors)
	}
	if len(redactedBackup.Channels) != 1 || !equalStrings(redactedBackup.Channels[0].RedactedSecrets, []string{"deviceKey"}) {
		t.Fatalf("channel redaction metadata = %#v", redactedBackup.Channels)
	}
	if len(redactedBackup.Proxies) != 1 || !equalStrings(redactedBackup.Proxies[0].RedactedSecrets, []string{"password"}) {
		t.Fatalf("proxy redaction metadata = %#v", redactedBackup.Proxies)
	}

	fullResponse, err := http.Get(sourceServer.URL + "/api/config/export?includeSecrets=true")
	if err != nil {
		t.Fatal(err)
	}
	fullBody, _ := io.ReadAll(fullResponse.Body)
	fullResponse.Body.Close()
	if fullResponse.StatusCode != http.StatusOK {
		t.Fatalf("full export status = %d body = %s", fullResponse.StatusCode, fullBody)
	}
	if !bytes.Contains(fullBody, []byte("github-secret-token")) || !bytes.Contains(fullBody, []byte("bark-secret-key")) || !bytes.Contains(fullBody, []byte("proxy-secret-password")) {
		t.Fatalf("explicit full export omitted secrets: %s", fullBody)
	}
	var fullBackup model.ConfigBackup
	if err := json.Unmarshal(fullBody, &fullBackup); err != nil {
		t.Fatal(err)
	}
	if !fullBackup.IncludesSecrets {
		t.Fatal("full export must declare includesSecrets=true")
	}
	exportAudits, err := sourceStore.ListAuditLogs(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	exportCount := 0
	for _, item := range exportAudits {
		if item.Action == "export" && item.EntityType == "config" {
			exportCount++
		}
	}
	if exportCount != 2 {
		t.Fatalf("export audit count = %d, want 2", exportCount)
	}

	targetServer, targetStore := newTestServer(t)
	report := importBackup(t, targetServer.URL, fullBackup, http.StatusOK)
	if report.Created.Proxies != 1 || report.Created.Monitors != 1 || report.Created.Channels != 1 || report.Created.Rules != 1 || report.Created.Templates != 1 || report.Updated.Templates != 1 {
		t.Fatalf("unexpected initial import report: %#v", report)
	}
	importAudits, err := targetStore.ListAuditLogs(ctx, 10)
	if err != nil || len(importAudits) != 1 || importAudits[0].Action != "import" || importAudits[0].EntityType != "config" {
		t.Fatalf("transactional import audit = %#v err=%v", importAudits, err)
	}
	targetMonitorID := report.IDMap.Monitors[fmt.Sprint(monitor.ID)]
	targetProxyID := report.IDMap.Proxies[fmt.Sprint(proxyProfile.ID)]
	targetChannelID := report.IDMap.Channels[fmt.Sprint(channel.ID)]
	targetTemplateID := report.IDMap.Templates[fmt.Sprint(template.ID)]
	targetRuleID := report.IDMap.Rules[fmt.Sprint(ruleItem.ID)]
	if targetProxyID == 0 || targetMonitorID == 0 || targetChannelID == 0 || targetTemplateID == 0 || targetRuleID == 0 {
		t.Fatalf("incomplete ID remapping: %#v", report.IDMap)
	}
	storedMonitor, err := targetStore.GetMonitor(ctx, targetMonitorID)
	if err != nil || !bytes.Contains(storedMonitor.Config, []byte("github-secret-token")) {
		t.Fatalf("monitor secret was not restored: item=%#v err=%v", storedMonitor, err)
	}
	if storedMonitor.ProxyID == nil || *storedMonitor.ProxyID != targetProxyID {
		t.Fatalf("monitor proxy reference was not remapped: %#v", storedMonitor.ProxyID)
	}
	storedProxy, err := targetStore.GetProxyProfile(ctx, targetProxyID)
	if err != nil || storedProxy.Password != "proxy-secret-password" {
		t.Fatalf("proxy password was not restored: item=%#v err=%v", storedProxy, err)
	}
	if storedMonitor.FailureAlertAfter != 2 || len(storedMonitor.FailureNotifyChannelIDs) != 1 || storedMonitor.FailureNotifyChannelIDs[0] != targetChannelID {
		t.Fatalf("monitor failure alert references were not remapped: %#v", storedMonitor)
	}
	storedChannel, err := targetStore.GetNotifyChannel(ctx, targetChannelID)
	if err != nil || !bytes.Contains(storedChannel.Config, []byte("bark-secret-key")) {
		t.Fatalf("channel secret was not restored: item=%#v err=%v", storedChannel, err)
	}
	storedRule, err := targetStore.GetRule(ctx, targetRuleID)
	if err != nil {
		t.Fatal(err)
	}
	if storedRule.MonitorID != targetMonitorID || len(storedRule.NotifyChannelIDs) != 1 || storedRule.NotifyChannelIDs[0] != targetChannelID || storedRule.TemplateID == nil || *storedRule.TemplateID != targetTemplateID {
		t.Fatalf("rule references were not remapped: %#v", storedRule)
	}
	if !storedRule.QuietHours.Enabled || storedRule.QuietHours.Start != "23:00" || storedRule.QuietHours.End != "07:00" || storedRule.QuietHours.Timezone != "Asia/Shanghai" {
		t.Fatalf("quiet hours were not restored: %#v", storedRule.QuietHours)
	}

	event, created, err := targetStore.CreateEvent(ctx, targetMonitorID, model.EventData{Type: "github.release", Fingerprint: "history-survives", Payload: map[string]any{"tag": "v1"}})
	if err != nil || !created {
		t.Fatalf("create history event: created=%v err=%v", created, err)
	}
	redactedReport := importBackup(t, targetServer.URL, redactedBackup, http.StatusOK)
	if redactedReport.Updated.Proxies != 1 || redactedReport.Updated.Monitors != 1 || redactedReport.Updated.Channels != 1 || redactedReport.Updated.Rules != 1 || len(redactedReport.Warnings) != 3 {
		t.Fatalf("unexpected redacted merge report: %#v", redactedReport)
	}
	if redactedReport.IDMap.Monitors[fmt.Sprint(monitor.ID)] != targetMonitorID || redactedReport.IDMap.Channels[fmt.Sprint(channel.ID)] != targetChannelID {
		t.Fatalf("merge changed entity IDs: %#v", redactedReport.IDMap)
	}
	if _, err := targetStore.GetEvent(ctx, event.ID); err != nil {
		t.Fatalf("merge broke runtime history: %v", err)
	}
	storedMonitor, _ = targetStore.GetMonitor(ctx, targetMonitorID)
	storedChannel, _ = targetStore.GetNotifyChannel(ctx, targetChannelID)
	storedProxy, _ = targetStore.GetProxyProfile(ctx, targetProxyID)
	if !bytes.Contains(storedMonitor.Config, []byte("github-secret-token")) || !bytes.Contains(storedChannel.Config, []byte("bark-secret-key")) || storedProxy.Password != "proxy-secret-password" {
		t.Fatal("redacted merge did not preserve target secrets")
	}
	monitors, _ := targetStore.ListMonitors(ctx)
	channels, _ := targetStore.ListNotifyChannels(ctx)
	rules, _ := targetStore.ListRules(ctx)
	if len(monitors) != 1 || len(channels) != 1 || len(rules) != 1 {
		t.Fatalf("merge created duplicates: monitors=%d channels=%d rules=%d", len(monitors), len(channels), len(rules))
	}

	freshServer, freshStore := newTestServer(t)
	importBackup(t, freshServer.URL, redactedBackup, http.StatusUnprocessableEntity)
	monitors, _ = freshStore.ListMonitors(ctx)
	channels, _ = freshStore.ListNotifyChannels(ctx)
	proxies, _ := freshStore.ListProxyProfiles(ctx)
	if len(monitors) != 0 || len(channels) != 0 || len(proxies) != 0 {
		t.Fatalf("failed redacted restore changed fresh database: monitors=%d channels=%d proxies=%d", len(monitors), len(channels), len(proxies))
	}
}

func TestConfigImportIsTransactionalOnLateMergeFailure(t *testing.T) {
	ctx := context.Background()
	_, db := newTestServer(t)
	channel, err := db.CreateNotifyChannel(ctx, model.NotifyChannelInput{
		Name: "Phone", Type: model.ChannelTypeBark, Enabled: true,
		Config: json.RawMessage(`{"serverUrl":"https://api.day.app","deviceKey":"original","group":"Before"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	backup := model.ConfigBackup{
		Version: model.ConfigBackupVersion, ExportedAt: time.Now().UTC(), IncludesSecrets: true,
		Channels: []model.ConfigBackupChannel{{
			ID: channel.ID, Name: channel.Name, Type: channel.Type, Enabled: true,
			Config: json.RawMessage(`{"serverUrl":"https://api.day.app","deviceKey":"replacement","group":"Would Roll Back"}`),
		}},
		Templates: []model.ConfigBackupTemplate{},
		Monitors: []model.ConfigBackupMonitor{{
			ID: 44, Name: "Would Roll Back", Type: model.MonitorTypeRSS, Enabled: true, IntervalSeconds: 300,
			Config: json.RawMessage(`{"url":"https://example.com/feed.xml"}`),
		}},
		Rules: []model.ConfigBackupRule{{
			ID: 55, MonitorID: 999, Name: "Late failure", Enabled: true,
			Condition: json.RawMessage(`{}`), NotifyChannelIDs: []int64{channel.ID},
		}},
	}
	if _, err := db.ImportConfigMerge(ctx, backup); err == nil {
		t.Fatal("expected a late reference failure")
	}
	stored, err := db.GetNotifyChannel(ctx, channel.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stored.Config, []byte(`"group":"Before"`)) || !bytes.Contains(stored.Config, []byte(`"deviceKey":"original"`)) {
		t.Fatalf("channel update was not rolled back: %s", stored.Config)
	}
	monitors, err := db.ListMonitors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(monitors) != 0 {
		t.Fatalf("monitor insert was not rolled back: %#v", monitors)
	}
}

func TestConfigExportRejectsLegacyDuplicateNaturalKeys(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/watchbell.db"
	db, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	input := model.MonitorInput{Name: "Duplicate", Type: model.MonitorTypeRSS, Enabled: true, IntervalSeconds: 300, Config: json.RawMessage(`{"url":"https://example.com/feed.xml"}`)}
	if _, err := db.CreateMonitor(ctx, input); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// Bypass current guarded writes to model a database produced by a release
	// that allowed duplicate natural keys.
	raw, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO monitors (name, type, enabled, interval_seconds, config_json, state_json, created_at, updated_at) VALUES ('Duplicate', 'rss', 1, 300, '{"url":"https://example.com/feed.xml"}', '{}', ?, ?)`, time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(ctx, path)
	if err != nil {
		t.Fatalf("legacy duplicate database failed to open: %v", err)
	}
	sched := scheduler.New(db, checker.NewRegistry(checker.NewRSSChecker()), notifier.NewRegistry(), scheduler.Options{})
	server := httptest.NewServer(NewServer(db, sched, "", slog.New(slog.NewTextHandler(io.Discard, nil)), nil).Routes())
	t.Cleanup(func() { server.Close(); _ = db.Close() })
	response, err := http.Get(server.URL + "/api/config/export")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var payload struct {
		Code   string            `json:"code"`
		Fields map[string]string `json:"fields"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusConflict || payload.Code != "ambiguous_config" || len(payload.Fields) != 1 {
		t.Fatalf("duplicate export response: status=%d payload=%#v", response.StatusCode, payload)
	}
}

func TestConfigImportStrictValidationReportsPreciseFields(t *testing.T) {
	server, _ := newTestServer(t)
	request := map[string]any{
		"mode": "replace",
		"backup": map[string]any{
			"version":         999,
			"exportedAt":      time.Now().UTC(),
			"includesSecrets": false,
			"monitors":        []any{},
			"channels":        []any{},
			"templates":       []any{},
			"rules": []any{map[string]any{
				"id": 1, "monitorId": 42, "name": "Broken references", "enabled": true,
				"condition": map[string]any{}, "notifyChannelIds": []int64{77},
				"cooldownSeconds": 0, "quietHours": map[string]any{"enabled": false},
			}},
		},
	}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Post(server.URL+"/api/config/import", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	responseBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d body = %s", response.StatusCode, responseBody)
	}
	var payload struct {
		Code   string            `json:"code"`
		Fields map[string]string `json:"fields"`
	}
	if err := json.Unmarshal(responseBody, &payload); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"mode", "backup.version", "backup.rules.0.monitorId", "backup.rules.0.notifyChannelIds.0"} {
		if payload.Fields[field] == "" {
			t.Fatalf("missing field %q in response: %s", field, responseBody)
		}
	}
	if payload.Code != "validation_failed" {
		t.Fatalf("code = %q body = %s", payload.Code, responseBody)
	}

	invalidQuery, err := http.Get(server.URL + "/api/config/export?includeSecrets=yes")
	if err != nil {
		t.Fatal(err)
	}
	invalidQueryBody, _ := io.ReadAll(invalidQuery.Body)
	invalidQuery.Body.Close()
	if invalidQuery.StatusCode != http.StatusBadRequest || !bytes.Contains(invalidQueryBody, []byte(`"code":"invalid_query"`)) {
		t.Fatalf("invalid export query response: status=%d body=%s", invalidQuery.StatusCode, invalidQueryBody)
	}
}

func importBackup(t *testing.T, baseURL string, backup model.ConfigBackup, expectedStatus int) model.ConfigImportReport {
	t.Helper()
	body, err := json.Marshal(model.ConfigImportRequest{Mode: "merge", Backup: backup})
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Post(baseURL+"/api/config/import", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	responseBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != expectedStatus {
		t.Fatalf("import status = %d want %d body = %s", response.StatusCode, expectedStatus, responseBody)
	}
	if expectedStatus != http.StatusOK {
		if !bytes.Contains(responseBody, []byte(`"code":"validation_failed"`)) {
			t.Fatalf("expected clear validation response: %s", responseBody)
		}
		return model.ConfigImportReport{}
	}
	var report model.ConfigImportReport
	if err := json.Unmarshal(responseBody, &report); err != nil {
		t.Fatalf("decode report: %v body=%s", err, responseBody)
	}
	return report
}

func stringsContain(value, fragment string) bool {
	return bytes.Contains([]byte(value), []byte(fragment))
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
