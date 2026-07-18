package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/watchbell/watchbell/internal/eventvars"
	"github.com/watchbell/watchbell/internal/model"
	"github.com/watchbell/watchbell/internal/store"
)

func TestVariableCatalogAndLiveValueLinks(t *testing.T) {
	var requests atomic.Int32
	var conditionalRequest atomic.Bool
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Modified-Since") != "" {
			conditionalRequest.Store(true)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		version := requests.Add(1) + 1
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Header().Set("ETag", fmt.Sprintf(`"feed-v%d"`, version))
		_, _ = fmt.Fprintf(w, `<?xml version="1.0"?><rss version="2.0"><channel>
			<title>Release feed</title><link>https://example.com/releases</link>
			<item><guid>v%d</guid><title>Version %d</title><link>https://example.com/v%d</link><description>Release notes %d</description><pubDate>Fri, 17 Jul 2026 12:0%d:00 GMT</pubDate></item>
			<item><guid>v1</guid><title>Version 1</title><link>https://example.com/v1</link><description>Old notes</description><pubDate>Thu, 16 Jul 2026 12:00:00 GMT</pubDate></item>
		</channel></rss>`, version, version, version, version, version)
	}))
	defer source.Close()

	server, db := newTestServer(t)
	ctx := context.Background()
	monitor, err := db.CreateMonitor(ctx, model.MonitorInput{
		Name: "Release feed", Type: model.MonitorTypeRSS, Enabled: false, IntervalSeconds: 300,
		Config: json.RawMessage(fmt.Sprintf(`{"url":%q,"notifyExisting":false}`, source.URL)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateMonitorCheckResult(ctx, monitor.ID, model.CheckResult{
		Status: "ok", Message: "baseline",
		State: map[string]any{
			"initialized": true, "etag": `"persisted-etag"`, "lastModified": "Fri, 17 Jul 2026 10:00:00 GMT",
			"seen": map[string]any{"rss:guid:v2": "2026-07-17T12:00:00Z"},
		},
	}, nil); err != nil {
		t.Fatal(err)
	}
	event, _, err := db.CreateEvent(ctx, monitor.ID, model.EventData{
		Type: "rss.item", Fingerprint: "variable-event",
		Payload: map[string]any{
			"rss": map[string]any{
				"title": "Persisted old event", "link": "https://example.com/old", "content": "Old event notes",
				"publishedAt": "2026-07-17T12:00:00Z",
			},
			"privateInternalValue": "must-not-leak",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := db.GetMonitor(ctx, monitor.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"not.a.real.variable", "rule.id"} {
		invalidResponse, err := http.Get(fmt.Sprintf("%s/api/monitors/%d/variables/%s", server.URL, monitor.ID, key))
		if err != nil {
			t.Fatal(err)
		}
		invalidResponse.Body.Close()
		if invalidResponse.StatusCode != http.StatusUnprocessableEntity || invalidResponse.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("invalid variable %q response: status=%d cache=%q", key, invalidResponse.StatusCode, invalidResponse.Header.Get("Cache-Control"))
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("invalid variable key accessed source %d time(s)", requests.Load())
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
	if snapshotResponse.StatusCode != http.StatusOK || snapshot.EventID != nil || snapshot.Source != "live" || snapshot.ObservationType != "rss.item" || !snapshot.SampleAvailable {
		t.Fatalf("unexpected snapshot: status=%d snapshot=%#v", snapshotResponse.StatusCode, snapshot)
	}
	if snapshot.Values["url"] != "https://example.com/v2" || snapshot.Values["title"] != "Version 2" || snapshot.Values["privateInternalValue"] != nil {
		t.Fatalf("unexpected or unsafe values: %#v", snapshot.Values)
	}
	if snapshot.Values["rss.content"] != "Release notes 2" || snapshot.Values["event.id"] != nil || snapshot.Values["event.time"] != nil {
		t.Fatalf("live inspection forged persisted event context: %#v", snapshot.Values)
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
		Source string `json:"source"`
		Key    string `json:"key"`
		Value  string `json:"value"`
	}
	if err := json.NewDecoder(valueResponse.Body).Decode(&value); err != nil {
		t.Fatal(err)
	}
	if valueResponse.StatusCode != http.StatusOK || value.Source != "live" || value.Key != "url" || value.Value != "https://example.com/v3" {
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

	after, err := db.GetMonitor(ctx, monitor.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("live inspection mutated monitor:\nbefore=%#v\nafter=%#v", before, after)
	}
	if conditionalRequest.Load() {
		t.Fatal("live inspection reused conditional request headers from monitor state")
	}
	if requests.Load() != 2 {
		t.Fatalf("source requests = %d, want 2", requests.Load())
	}
	assertVariableInspectionHasNoSideEffects(t, db, monitor.ID, 1)
}

func TestMonitorVariableSnapshotWithoutCurrentRSSItemReturnsSourceContext(t *testing.T) {
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = fmt.Fprint(w, `<?xml version="1.0"?><rss version="2.0"><channel><title>Empty feed</title><link>https://example.com/empty</link></channel></rss>`)
	}))
	defer source.Close()

	server, db := newTestServer(t)
	monitor, err := db.CreateMonitor(context.Background(), model.MonitorInput{
		Name: "Empty feed", Type: model.MonitorTypeRSS, Enabled: false, IntervalSeconds: 300,
		Config: json.RawMessage(fmt.Sprintf(`{"url":%q}`, source.URL)),
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
	if response.StatusCode != http.StatusOK || snapshot.EventID != nil || snapshot.Source != "live" || snapshot.SampleAvailable || snapshot.Message == "" {
		t.Fatalf("unexpected empty snapshot: status=%d snapshot=%#v", response.StatusCode, snapshot)
	}
	if snapshot.Values["monitor.name"] != "Empty feed" || snapshot.Values["rss.sourceTitle"] != "Empty feed" || snapshot.Values["rss.sourceLink"] != "https://example.com/empty" {
		t.Fatalf("empty source context missing: %#v", snapshot.Values)
	}
	if snapshot.Values["status"] != "" {
		t.Fatalf("empty feed was presented as a published item: %#v", snapshot.Values)
	}
	assertVariableInspectionHasNoSideEffects(t, db, monitor.ID, 0)
}

func TestMonitorVariableInspectionFailureIsNotCachedOrPersisted(t *testing.T) {
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer source.Close()

	server, db := newTestServer(t)
	ctx := context.Background()
	monitor, err := db.CreateMonitor(ctx, model.MonitorInput{
		Name: "Unavailable feed", Type: model.MonitorTypeRSS, Enabled: false, IntervalSeconds: 300,
		Config: json.RawMessage(fmt.Sprintf(`{"url":%q}`, source.URL)),
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := db.GetMonitor(ctx, monitor.ID)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Get(fmt.Sprintf("%s/api/monitors/%d/variables", server.URL, monitor.ID))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var problem struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(response.Body).Decode(&problem); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusBadGateway || problem.Code != "monitor_inspection_failed" || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("failure response: status=%d code=%q cache=%q", response.StatusCode, problem.Code, response.Header.Get("Cache-Control"))
	}
	after, err := db.GetMonitor(ctx, monitor.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("failed inspection mutated monitor: before=%#v after=%#v", before, after)
	}
	assertVariableInspectionHasNoSideEffects(t, db, monitor.ID, 0)
}

func TestEventVariableSnapshotUsesHistoricalCheckRunContext(t *testing.T) {
	server, db := newTestServer(t)
	ctx := context.Background()
	monitor, err := db.CreateMonitor(ctx, model.MonitorInput{
		Name: "Original feed", Type: model.MonitorTypeRSS, Enabled: false, IntervalSeconds: 300,
		Config: json.RawMessage(`{"url":"https://old.example/feed.xml"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := db.CreateCheckRun(ctx, monitor, "manual", monitor.Config)
	if err != nil {
		t.Fatal(err)
	}
	event, created, err := db.CreateEventForRun(ctx, monitor.ID, run.ID, model.EventData{
		Type: "rss.item", Fingerprint: "historical-variable-event",
		Payload: map[string]any{"rss": map[string]any{"title": "Historical item"}},
	})
	if err != nil || !created {
		t.Fatalf("create event: created=%v err=%v", created, err)
	}
	if _, err := db.UpdateMonitor(ctx, monitor.ID, model.MonitorInput{
		Name: "Current page", Type: model.MonitorTypeWebpage, Enabled: false, IntervalSeconds: 300,
		Config: json.RawMessage(`{"url":"https://new.example/page"}`),
	}); err != nil {
		t.Fatal(err)
	}

	response, err := http.Get(fmt.Sprintf("%s/api/events/%d/variables", server.URL, event.ID))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var snapshot variableSnapshotResponse
	if err := json.NewDecoder(response.Body).Decode(&snapshot); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, snapshot=%#v", response.StatusCode, snapshot)
	}
	if snapshot.MonitorName != "Original feed" || snapshot.MonitorType != model.MonitorTypeRSS {
		t.Fatalf("historical monitor context drifted: %#v", snapshot)
	}
	if snapshot.Values["rss.title"] != "Historical item" || snapshot.Values["url"] != "https://old.example/feed.xml" {
		t.Fatalf("historical values drifted: %#v", snapshot.Values)
	}
	if _, exists := snapshot.Values["webpage.url"]; exists {
		t.Fatalf("historical RSS event was interpreted as webpage: %#v", snapshot.Values)
	}
	valueResponse, err := http.Get(fmt.Sprintf("%s/api/events/%d/variables/rss.title", server.URL, event.ID))
	if err != nil {
		t.Fatal(err)
	}
	defer valueResponse.Body.Close()
	if valueResponse.StatusCode != http.StatusOK {
		t.Fatalf("historical variable link status = %d", valueResponse.StatusCode)
	}
}

func assertVariableInspectionHasNoSideEffects(t *testing.T, db *store.Store, monitorID int64, wantEvents int64) {
	t.Helper()
	ctx := context.Background()
	events, err := db.ListEventsPage(ctx, store.EventFilter{PageRequest: store.PageRequest{Page: 1, PageSize: 100}, MonitorID: monitorID})
	if err != nil {
		t.Fatal(err)
	}
	runs, err := db.ListCheckRunsPage(ctx, store.CheckRunFilter{PageRequest: store.PageRequest{Page: 1, PageSize: 100}, MonitorID: monitorID})
	if err != nil {
		t.Fatal(err)
	}
	evaluations, err := db.ListRuleEvaluationsPage(ctx, store.RuleEvaluationFilter{PageRequest: store.PageRequest{Page: 1, PageSize: 100}})
	if err != nil {
		t.Fatal(err)
	}
	attempts, err := db.ListNotificationAttemptsPage(ctx, store.NotificationAttemptFilter{PageRequest: store.PageRequest{Page: 1, PageSize: 100}, MonitorID: monitorID})
	if err != nil {
		t.Fatal(err)
	}
	if events.Total != wantEvents || runs.Total != 0 || evaluations.Total != 0 || attempts.Total != 0 {
		t.Fatalf("inspection side effects: events=%d runs=%d evaluations=%d attempts=%d", events.Total, runs.Total, evaluations.Total, attempts.Total)
	}
}

func TestLegacyEventVariableSnapshotInfersTypeWithoutCurrentConfig(t *testing.T) {
	server, db := newTestServer(t)
	ctx := context.Background()
	monitor, err := db.CreateMonitor(ctx, model.MonitorInput{
		Name: "Legacy feed", Type: model.MonitorTypeRSS, Enabled: false, IntervalSeconds: 300,
		Config: json.RawMessage(`{"url":"https://old.example/private-feed.xml"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	event, _, err := db.CreateEvent(ctx, monitor.ID, model.EventData{
		Type: "rss.item", Fingerprint: "legacy-variable-event",
		Payload: map[string]any{"rss": map[string]any{"title": "Legacy item", "link": "https://old.example/item"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpdateMonitor(ctx, monitor.ID, model.MonitorInput{
		Name: "Changed monitor", Type: model.MonitorTypeWebpage, Enabled: false, IntervalSeconds: 300,
		Config: json.RawMessage(`{"url":"https://new.example/page"}`),
	}); err != nil {
		t.Fatal(err)
	}

	response, err := http.Get(fmt.Sprintf("%s/api/events/%d/variables", server.URL, event.ID))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var snapshot variableSnapshotResponse
	if err := json.NewDecoder(response.Body).Decode(&snapshot); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || snapshot.MonitorType != model.MonitorTypeRSS || snapshot.Values["rss.title"] != "Legacy item" || snapshot.Values["url"] != "https://old.example/item" {
		t.Fatalf("legacy event context = status %d snapshot %#v", response.StatusCode, snapshot)
	}
}
