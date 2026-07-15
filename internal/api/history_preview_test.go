package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/watchbell/watchbell/internal/model"
	"github.com/watchbell/watchbell/internal/store"
)

func TestHistoryEndpointsSupportPaginationAndFilters(t *testing.T) {
	server, db := newTestServer(t)
	monitor, err := db.CreateMonitor(context.Background(), model.MonitorInput{
		Name: "History feed", Type: model.MonitorTypeRSS, Enabled: false, IntervalSeconds: 300,
		Config: json.RawMessage(`{"url":"https://example.com/feed.xml"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 3; index++ {
		if _, _, err := db.CreateEvent(context.Background(), monitor.ID, model.EventData{
			Type: "rss.item", Fingerprint: fmt.Sprintf("page-%d", index), Payload: map[string]any{"rss": map[string]any{"title": fmt.Sprintf("Item %d", index)}},
		}); err != nil {
			t.Fatal(err)
		}
	}

	response, err := http.Get(fmt.Sprintf("%s/api/events?page=2&pageSize=1&monitorId=%d&type=rss.item", server.URL, monitor.ID))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	var page store.Page[model.Event]
	if err := json.NewDecoder(response.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if page.Page != 2 || page.PageSize != 1 || page.Total != 3 || page.TotalPages != 3 || len(page.Items) != 1 {
		t.Fatalf("unexpected page: %#v", page)
	}
	filteredOnly, err := http.Get(fmt.Sprintf("%s/api/events?monitorId=%d", server.URL, monitor.ID))
	if err != nil {
		t.Fatal(err)
	}
	defer filteredOnly.Body.Close()
	var filteredPage store.Page[model.Event]
	if err := json.NewDecoder(filteredOnly.Body).Decode(&filteredPage); err != nil {
		t.Fatal(err)
	}
	if filteredOnly.StatusCode != http.StatusOK || filteredPage.Total != 3 || len(filteredPage.Items) != 3 {
		t.Fatalf("filter-only query was not paginated: status=%d page=%#v", filteredOnly.StatusCode, filteredPage)
	}

	invalid, err := http.Get(server.URL + "/api/events?page=0&pageSize=20")
	if err != nil {
		t.Fatal(err)
	}
	defer invalid.Body.Close()
	if invalid.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("invalid query status = %d", invalid.StatusCode)
	}
}

func TestTemplatePreviewCanUseRealEvent(t *testing.T) {
	server, db := newTestServer(t)
	monitor, err := db.CreateMonitor(context.Background(), model.MonitorInput{
		Name: "Production feed", Type: model.MonitorTypeRSS, Enabled: false, IntervalSeconds: 300,
		Config: json.RawMessage(`{"url":"https://example.com/feed.xml"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	event, _, err := db.CreateEvent(context.Background(), monitor.ID, model.EventData{
		Type: "rss.item", Fingerprint: "preview-event", Payload: map[string]any{"rss": map[string]any{"title": "A real release", "link": "https://example.com/release"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{
		"subjectTemplate": "${monitor.name}: ${rss.title}",
		"bodyTemplate":    "${event.type} ${rss.link}",
		"eventId":         event.ID,
	})
	response, err := http.Post(server.URL+"/api/templates/preview", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	var result map[string]string
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result["subject"] != "Production feed: A real release" || result["body"] != "rss.item https://example.com/release" {
		t.Fatalf("unexpected preview: %#v", result)
	}
}

func TestManualCheckReturnsRunAndNewEventCount(t *testing.T) {
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>Releases</title><link>https://example.com</link><description>Feed</description><item><title>Version 2</title><link>https://example.com/v2</link><guid>v2</guid></item></channel></rss>`))
	}))
	defer feed.Close()
	server, db := newTestServer(t)
	config, _ := json.Marshal(map[string]any{"url": feed.URL, "notifyExisting": true})
	monitor, err := db.CreateMonitor(context.Background(), model.MonitorInput{
		Name: "Release feed", Type: model.MonitorTypeRSS, Enabled: true, IntervalSeconds: 300, Config: config,
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Post(fmt.Sprintf("%s/api/monitors/%d/check", server.URL, monitor.ID), "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	var result struct {
		Status     string          `json:"status"`
		EventCount int             `json:"eventCount"`
		CheckRun   *model.CheckRun `json:"checkRun"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "checked" || result.EventCount != 1 || result.CheckRun == nil || result.CheckRun.EventCount != 1 || result.CheckRun.Trigger != "manual" {
		t.Fatalf("unexpected manual check result: %#v", result)
	}
}

func TestDeadLetterCanBeInspectedAndRequeued(t *testing.T) {
	server, db := newTestServer(t)
	ctx := context.Background()
	monitor, err := db.CreateMonitor(ctx, model.MonitorInput{Name: "Dead feed", Type: model.MonitorTypeRSS, Enabled: false, IntervalSeconds: 300, Config: json.RawMessage(`{"url":"https://example.com/feed.xml"}`)})
	if err != nil {
		t.Fatal(err)
	}
	event, created, err := db.CreateEvent(ctx, monitor.ID, model.EventData{Type: "rss.item", Fingerprint: "dead-event", Payload: map[string]any{}})
	if err != nil || !created {
		t.Fatalf("create event: created=%v err=%v", created, err)
	}
	if err := db.MarkOutboxFailed(ctx, event.ID, 10, errors.New("provider permanently unavailable")); err != nil {
		t.Fatal(err)
	}

	response, err := http.Get(fmt.Sprintf("%s/api/dead-letters?page=1&pageSize=20&monitorId=%d", server.URL, monitor.ID))
	if err != nil {
		t.Fatal(err)
	}
	var page store.Page[model.DeadLetter]
	if err := json.NewDecoder(response.Body).Decode(&page); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || page.Total != 1 || len(page.Items) != 1 || page.Items[0].EventID != event.ID || page.Items[0].LastError != "provider permanently unavailable" {
		t.Fatalf("dead-letter page: status=%d page=%#v", response.StatusCode, page)
	}

	retryResponse, err := http.Post(fmt.Sprintf("%s/api/dead-letters/%d/retry", server.URL, event.ID), "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	retryResponse.Body.Close()
	if retryResponse.StatusCode != http.StatusOK {
		t.Fatalf("retry status = %d", retryResponse.StatusCode)
	}
	due, err := db.ListDueOutbox(ctx, 10, time.Now().UTC().Add(time.Second))
	if err != nil || len(due) != 1 || due[0].EventID != event.ID || due[0].Attempts != 0 {
		t.Fatalf("requeued outbox = %#v err=%v", due, err)
	}
}

func TestArchivedMonitorDeadLetterCannotBeRequeued(t *testing.T) {
	server, db := newTestServer(t)
	ctx := context.Background()
	monitor, err := db.CreateMonitor(ctx, model.MonitorInput{Name: "Archived feed", Type: model.MonitorTypeRSS, Enabled: false, IntervalSeconds: 300, Config: json.RawMessage(`{"url":"https://example.com/feed.xml"}`)})
	if err != nil {
		t.Fatal(err)
	}
	event, created, err := db.CreateEvent(ctx, monitor.ID, model.EventData{Type: "rss.item", Fingerprint: "archived-dead-event", Payload: map[string]any{}})
	if err != nil || !created {
		t.Fatalf("create event: created=%v err=%v", created, err)
	}
	if err := db.MarkOutboxFailed(ctx, event.ID, 10, errors.New("permanent processing failure")); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteMonitor(ctx, monitor.ID); err != nil {
		t.Fatal(err)
	}

	response, err := http.Post(fmt.Sprintf("%s/api/dead-letters/%d/retry", server.URL, event.ID), "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("retry status = %d, want %d", response.StatusCode, http.StatusConflict)
	}

	listResponse, err := http.Get(fmt.Sprintf("%s/api/dead-letters?page=1&pageSize=20&monitorId=%d", server.URL, monitor.ID))
	if err != nil {
		t.Fatal(err)
	}
	defer listResponse.Body.Close()
	var page store.Page[model.DeadLetter]
	if err := json.NewDecoder(listResponse.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].EventID != event.ID {
		t.Fatalf("archived dead letter changed after rejected retry: %#v", page)
	}
}
