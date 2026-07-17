package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/watchbell/watchbell/internal/eventvars"
	"github.com/watchbell/watchbell/internal/model"
)

func TestVariableCatalogAndLiveValueLinks(t *testing.T) {
	server, db := newTestServer(t)
	monitor, err := db.CreateMonitor(context.Background(), model.MonitorInput{
		Name: "Release feed", Type: model.MonitorTypeRSS, Enabled: false, IntervalSeconds: 300,
		Config: json.RawMessage(`{"url":"https://example.com/feed.xml"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	event, _, err := db.CreateEvent(context.Background(), monitor.ID, model.EventData{
		Type: "rss.item", Fingerprint: "variable-event",
		Payload: map[string]any{
			"rss": map[string]any{
				"title": "Version 2", "link": "https://example.com/v2", "content": "Release notes",
				"publishedAt": "2026-07-17T12:00:00Z",
			},
			"privateInternalValue": "must-not-leak",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	catalogResponse, err := http.Get(server.URL + "/api/help/variables")
	if err != nil {
		t.Fatal(err)
	}
	defer catalogResponse.Body.Close()
	var catalog eventvars.Catalog
	if err := json.NewDecoder(catalogResponse.Body).Decode(&catalog); err != nil {
		t.Fatal(err)
	}
	if catalogResponse.StatusCode != http.StatusOK || catalogResponse.Header.Get("Cache-Control") != "no-store" || len(catalog.Globals) != 7 || len(catalog.Modules) != 4 {
		t.Fatalf("unexpected catalog response: status=%d cache=%q catalog=%#v", catalogResponse.StatusCode, catalogResponse.Header.Get("Cache-Control"), catalog)
	}

	snapshotResponse, err := http.Get(fmt.Sprintf("%s/api/monitors/%d/variables", server.URL, monitor.ID))
	if err != nil {
		t.Fatal(err)
	}
	defer snapshotResponse.Body.Close()
	var snapshot variableSnapshotResponse
	if err := json.NewDecoder(snapshotResponse.Body).Decode(&snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshotResponse.StatusCode != http.StatusOK || snapshot.EventID == nil || *snapshot.EventID != event.ID {
		t.Fatalf("unexpected snapshot: status=%d snapshot=%#v", snapshotResponse.StatusCode, snapshot)
	}
	if snapshot.Values["url"] != "https://example.com/v2" || snapshot.Values["title"] != "Version 2" || snapshot.Values["privateInternalValue"] != nil {
		t.Fatalf("unexpected or unsafe values: %#v", snapshot.Values)
	}
	if snapshot.ValueLinks["url"] != fmt.Sprintf("/api/monitors/%d/variables/url", monitor.ID) {
		t.Fatalf("missing stable value link: %#v", snapshot.ValueLinks)
	}

	valueResponse, err := http.Get(server.URL + snapshot.ValueLinks["url"])
	if err != nil {
		t.Fatal(err)
	}
	defer valueResponse.Body.Close()
	var value struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := json.NewDecoder(valueResponse.Body).Decode(&value); err != nil {
		t.Fatal(err)
	}
	if valueResponse.StatusCode != http.StatusOK || value.Key != "url" || value.Value != "https://example.com/v2" {
		t.Fatalf("unexpected value endpoint: status=%d value=%#v", valueResponse.StatusCode, value)
	}

	eventValueResponse, err := http.Get(fmt.Sprintf("%s/api/events/%d/variables/rss.title", server.URL, event.ID))
	if err != nil {
		t.Fatal(err)
	}
	defer eventValueResponse.Body.Close()
	if eventValueResponse.StatusCode != http.StatusOK {
		t.Fatalf("event value status = %d", eventValueResponse.StatusCode)
	}
}

func TestMonitorVariableSnapshotWithoutEventsStillReturnsMonitorContext(t *testing.T) {
	server, db := newTestServer(t)
	monitor, err := db.CreateMonitor(context.Background(), model.MonitorInput{
		Name: "New page", Type: model.MonitorTypeWebpage, Enabled: false, IntervalSeconds: 300,
		Config: json.RawMessage(`{"url":"https://example.com"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Get(fmt.Sprintf("%s/api/monitors/%d/variables", server.URL, monitor.ID))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var snapshot variableSnapshotResponse
	if err := json.NewDecoder(response.Body).Decode(&snapshot); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || snapshot.EventID != nil || snapshot.Values["monitor.name"] != "New page" {
		t.Fatalf("unexpected empty snapshot: status=%d snapshot=%#v", response.StatusCode, snapshot)
	}
}
