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
