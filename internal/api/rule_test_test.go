package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/watchbell/watchbell/internal/model"
)

func TestRuleDryRunMatchesRecentMonitorEvents(t *testing.T) {
	server, db := newTestServer(t)
	ctx := context.Background()
	monitor, err := db.CreateMonitor(ctx, model.MonitorInput{
		Name: "Feed", Type: model.MonitorTypeRSS, Enabled: false, IntervalSeconds: 300,
		Config: json.RawMessage(`{"url":"https://example.com/feed.xml"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.CreateEvent(ctx, monitor.ID, model.EventData{
		Type: "rss.item", Fingerprint: "dry-run-1",
		Payload: map[string]any{"rss": map[string]any{"title": "Important release"}},
	}); err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateMonitor(ctx, model.MonitorInput{
		Name: "Busy feed", Type: model.MonitorTypeRSS, Enabled: false, IntervalSeconds: 300,
		Config: json.RawMessage(`{"url":"https://example.com/busy.xml"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Regression: a global "latest 500" query used to let another busy
	// monitor hide every event belonging to the monitor under test.
	for index := 0; index < 501; index++ {
		if _, _, err := db.CreateEvent(ctx, other.ID, model.EventData{Type: "rss.item", Fingerprint: fmt.Sprintf("busy-%d", index), Payload: map[string]any{"rss": map[string]any{"title": "noise"}}}); err != nil {
			t.Fatal(err)
		}
	}
	body := []byte(`{"monitorId":1,"condition":{"match":"all","conditions":[{"field":"rss.title","operator":"contains","value":"release"}]},"limit":20}`)
	response, err := http.Post(server.URL+"/api/rules/test", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	var result struct {
		Tested  int `json:"tested"`
		Matched int `json:"matched"`
		Results []struct {
			EventID int64 `json:"eventId"`
		} `json:"results"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Tested != 1 || result.Matched != 1 || len(result.Results) != 1 {
		t.Fatalf("unexpected dry-run result: %#v", result)
	}
}

func TestNestedRuleValidationWalksEveryGroupAndChecksTimeFields(t *testing.T) {
	server, db := newTestServer(t)
	ctx := context.Background()
	monitor, err := db.CreateMonitor(ctx, model.MonitorInput{
		Name: "Nested feed", Type: model.MonitorTypeRSS, Enabled: false, IntervalSeconds: 300,
		Config: json.RawMessage(`{"url":"https://example.com/feed.xml"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	channel, err := db.CreateNotifyChannel(ctx, model.NotifyChannelInput{
		Name: "Nested test channel", Type: model.ChannelTypeBark, Enabled: true,
		Config: json.RawMessage(`{"serverUrl":"https://api.day.app","deviceKey":"test"}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	postRule := func(name, condition string) (int, map[string]any) {
		t.Helper()
		body, err := json.Marshal(model.RuleInput{
			MonitorID: monitor.ID, Name: name, Enabled: true, Condition: json.RawMessage(condition),
			NotifyChannelIDs: []int64{channel.ID},
		})
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.Post(server.URL+"/api/rules", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var payload map[string]any
		if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, payload
	}

	valid := `{"match":"all","conditions":[{"match":"any","conditions":[{"field":"rss.title","operator":"contains","value":"送码"},{"field":"rss.content","operator":"contains","value":"兑换码"}]},{"field":"rss.publishedAt","operator":"within_last","value":"2m"}]}`
	status, payload := postRule("Nested valid", valid)
	if status != http.StatusCreated {
		t.Fatalf("valid nested rule: status=%d payload=%#v", status, payload)
	}

	invalidField := `{"match":"all","conditions":[{"match":"any","conditions":[{"field":"rss.title","operator":"contains","value":"x"},{"field":"webpage.url","operator":"exists"}]}]}`
	status, payload = postRule("Nested invalid field", invalidField)
	fields, _ := payload["fields"].(map[string]any)
	if status != http.StatusUnprocessableEntity || fields["condition.conditions.0.conditions.1.field"] == nil {
		t.Fatalf("nested field path: status=%d payload=%#v", status, payload)
	}

	invalidTimeField := `{"match":"all","conditions":[{"field":"rss.title","operator":"within_last","value":"2m"}]}`
	status, payload = postRule("Nested invalid time", invalidTimeField)
	fields, _ = payload["fields"].(map[string]any)
	if status != http.StatusUnprocessableEntity || fields["condition.conditions.0.operator"] == nil {
		t.Fatalf("time field validation: status=%d payload=%#v", status, payload)
	}
}

func TestRuleDryRunCombinesMultipleMonitors(t *testing.T) {
	server, db := newTestServer(t)
	ctx := context.Background()
	monitorIDs := make([]int64, 0, 2)
	for index, name := range []string{"First multi feed", "Second multi feed"} {
		monitor, err := db.CreateMonitor(ctx, model.MonitorInput{
			Name: name, Type: model.MonitorTypeRSS, Enabled: false, IntervalSeconds: 300,
			Config: json.RawMessage(fmt.Sprintf(`{"url":"https://example.com/%d.xml"}`, index)),
		})
		if err != nil {
			t.Fatal(err)
		}
		monitorIDs = append(monitorIDs, monitor.ID)
		if _, _, err := db.CreateEvent(ctx, monitor.ID, model.EventData{
			Type: "rss.item", Fingerprint: fmt.Sprintf("multi-%d", index),
			Payload: map[string]any{"rss": map[string]any{"title": "release"}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	body, err := json.Marshal(map[string]any{
		"monitorIds": monitorIDs,
		"condition":  map[string]any{"match": "all", "conditions": []map[string]any{{"field": "rss.title", "operator": "contains", "value": "release"}}},
		"limit":      20,
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Post(server.URL+"/api/rules/test", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result struct {
		Tested  int `json:"tested"`
		Matched int `json:"matched"`
		Results []struct {
			MonitorID   int64  `json:"monitorId"`
			MonitorName string `json:"monitorName"`
		} `json:"results"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || result.Tested != 2 || result.Matched != 2 || len(result.Results) != 2 {
		t.Fatalf("multi-monitor dry run status=%d result=%#v", response.StatusCode, result)
	}
	seen := map[int64]bool{}
	for _, item := range result.Results {
		if item.MonitorName == "" {
			t.Fatalf("missing monitor name in result %#v", item)
		}
		seen[item.MonitorID] = true
	}
	for _, id := range monitorIDs {
		if !seen[id] {
			t.Fatalf("monitor %d missing from result %#v", id, result)
		}
	}
}
