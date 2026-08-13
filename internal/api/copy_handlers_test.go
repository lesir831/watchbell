package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/watchbell/watchbell/internal/model"
)

func TestCopyEndpointsCloneConfigurationAndProtectSecrets(t *testing.T) {
	server, db := newTestServer(t)
	ctx := context.Background()

	monitor, err := db.CreateMonitor(ctx, model.MonitorInput{
		Name: "Release feed", Type: model.MonitorTypeGitHubRelease, Enabled: true, IntervalSeconds: 600,
		Config: json.RawMessage(`{"repository":"watchbell/watchbell","apiUrl":"https://api.github.com","token":"monitor-secret","timeoutSeconds":15,"maxReleases":20}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateMonitorCheckResult(ctx, monitor.ID, model.CheckResult{Status: "ok", Message: "checked", State: map[string]any{"initialized": true}}, nil); err != nil {
		t.Fatal(err)
	}
	secondMonitor, err := db.CreateMonitor(ctx, model.MonitorInput{
		Name: "Second release feed", Type: model.MonitorTypeGitHubRelease, Enabled: true, IntervalSeconds: 600,
		Config: json.RawMessage(`{"repository":"watchbell/second","apiUrl":"https://api.github.com","timeoutSeconds":15,"maxReleases":20}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	channel, err := db.CreateNotifyChannel(ctx, model.NotifyChannelInput{
		Name: "Phone", Type: model.ChannelTypeBark, Enabled: true,
		Config: json.RawMessage(`{"serverUrl":"https://api.day.app","deviceKey":"channel-secret","group":"WatchBell"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	template, err := db.GetDefaultNotificationTemplate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ruleItem, err := db.CreateRule(ctx, model.RuleInput{
		MonitorID: monitor.ID, MonitorIDs: []int64{monitor.ID, secondMonitor.ID}, Name: "Notify releases", Enabled: true,
		Condition: json.RawMessage(`{}`), NotifyChannelIDs: []int64{channel.ID}, TemplateID: &template.ID,
		CooldownSeconds: 120, QuietHours: model.QuietHours{Enabled: true, Start: "22:00", End: "08:00", Timezone: "Asia/Shanghai"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateRuleFiredAt(ctx, ruleItem.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	var copiedMonitor model.Monitor
	monitorBody := postCopy(t, server.URL+fmt.Sprintf("/api/monitors/%d/copy", monitor.ID), &copiedMonitor)
	if copiedMonitor.Name != "Release feed 副本" || copiedMonitor.Enabled {
		t.Fatalf("copied monitor = %#v", copiedMonitor)
	}
	if bytes.Contains(monitorBody, []byte("monitor-secret")) || !slices.Contains(copiedMonitor.ConfiguredSecrets, "token") {
		t.Fatalf("copied monitor response leaked or omitted secret metadata: %s", monitorBody)
	}
	storedMonitor, err := db.GetMonitor(ctx, copiedMonitor.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(storedMonitor.Config, []byte("monitor-secret")) || storedMonitor.LastCheckedAt != nil || storedMonitor.LastStatus != "" || string(storedMonitor.State) != "{}" {
		t.Fatalf("copied monitor did not preserve config with fresh runtime state: %#v", storedMonitor)
	}
	var secondMonitorCopy model.Monitor
	postCopy(t, server.URL+fmt.Sprintf("/api/monitors/%d/copy", monitor.ID), &secondMonitorCopy)
	if secondMonitorCopy.Name != "Release feed 副本 2" {
		t.Fatalf("second copied monitor name = %q", secondMonitorCopy.Name)
	}

	var copiedRule model.Rule
	postCopy(t, server.URL+fmt.Sprintf("/api/rules/%d/copy", ruleItem.ID), &copiedRule)
	if copiedRule.Name != "Notify releases 副本" || copiedRule.Enabled || copiedRule.LastFiredAt != nil {
		t.Fatalf("copied rule = %#v", copiedRule)
	}
	if !slices.Equal(copiedRule.MonitorIDs, ruleItem.MonitorIDs) || !slices.Equal(copiedRule.NotifyChannelIDs, ruleItem.NotifyChannelIDs) || copiedRule.TemplateID == nil || *copiedRule.TemplateID != template.ID || copiedRule.QuietHours != ruleItem.QuietHours {
		t.Fatalf("copied rule lost configuration: source=%#v copy=%#v", ruleItem, copiedRule)
	}

	var copiedChannel model.NotifyChannel
	channelBody := postCopy(t, server.URL+fmt.Sprintf("/api/channels/%d/copy", channel.ID), &copiedChannel)
	if copiedChannel.Name != "Phone 副本" || copiedChannel.Enabled {
		t.Fatalf("copied channel = %#v", copiedChannel)
	}
	if bytes.Contains(channelBody, []byte("channel-secret")) || !slices.Contains(copiedChannel.ConfiguredSecrets, "deviceKey") {
		t.Fatalf("copied channel response leaked or omitted secret metadata: %s", channelBody)
	}
	storedChannel, err := db.GetNotifyChannel(ctx, copiedChannel.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(storedChannel.Config, []byte("channel-secret")) {
		t.Fatalf("copied channel did not preserve secret: %s", storedChannel.Config)
	}

	var copiedTemplate model.NotificationTemplate
	postCopy(t, server.URL+fmt.Sprintf("/api/templates/%d/copy", template.ID), &copiedTemplate)
	if copiedTemplate.Name != template.Name+" 副本" || copiedTemplate.IsDefault || copiedTemplate.SubjectTemplate != template.SubjectTemplate || copiedTemplate.BodyTemplate != template.BodyTemplate {
		t.Fatalf("copied template = %#v", copiedTemplate)
	}

	audits, err := db.ListAuditLogs(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	wantSources := map[string]int64{
		"monitor":  monitor.ID,
		"rule":     ruleItem.ID,
		"channel":  channel.ID,
		"template": template.ID,
	}
	for _, audit := range audits {
		if audit.Action != "copy" {
			continue
		}
		sourceID, wanted := wantSources[audit.EntityType]
		if !wanted {
			continue
		}
		if !bytes.Contains(audit.Changes, []byte(fmt.Sprintf(`"sourceId":%d`, sourceID))) {
			t.Fatalf("%s copy audit missing sourceId: %s", audit.EntityType, audit.Changes)
		}
		delete(wantSources, audit.EntityType)
	}
	if len(wantSources) != 0 {
		t.Fatalf("missing copy audits for %v", wantSources)
	}
}

func TestCopyEndpointsRejectMissingSources(t *testing.T) {
	server, _ := newTestServer(t)
	for _, collection := range []string{"monitors", "rules", "channels", "templates"} {
		request, err := http.NewRequest(http.MethodPost, server.URL+"/api/"+collection+"/99999/copy", nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("%s missing source status = %d", collection, response.StatusCode)
		}
	}
}

func postCopy(t *testing.T, url string, destination any) []byte {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("copy status = %d body = %s", response.StatusCode, body)
	}
	if err := json.Unmarshal(body, destination); err != nil {
		t.Fatalf("decode copy response: %v body=%s", err, body)
	}
	return body
}
