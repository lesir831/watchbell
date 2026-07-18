package checker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/watchbell/watchbell/internal/model"
)

var (
	_ Inspector = (*RSSChecker)(nil)
	_ Inspector = (*TestFlightChecker)(nil)
	_ Inspector = (*WebpageChecker)(nil)
	_ Inspector = (*GitHubReleaseChecker)(nil)
)

func TestRSSInspectFetchesLatestItemWithoutConditionalState(t *testing.T) {
	empty := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if value := r.Header.Get("If-None-Match"); value != "" {
			t.Errorf("Inspect sent If-None-Match %q", value)
		}
		if value := r.Header.Get("If-Modified-Since"); value != "" {
			t.Errorf("Inspect sent If-Modified-Since %q", value)
		}
		w.Header().Set("Content-Type", "application/rss+xml")
		items := `<item><title>Newest</title><link>https://example.com/new</link><guid>new</guid><description>new body</description></item>
			<item><title>Older</title><link>https://example.com/old</link><guid>old</guid></item>`
		if empty {
			items = ""
		}
		_, _ = w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>Example feed</title><link>https://example.com</link><description>Feed</description>` + items + `</channel></rss>`))
	}))
	defer server.Close()

	state := `{"initialized":true,"etag":"old-etag","lastModified":"Wed, 01 Jul 2026 00:00:00 GMT","seen":{"rss:guid:new":"2026-07-01T00:00:00Z"}}`
	monitor := model.Monitor{
		Type: model.MonitorTypeRSS,
		Config: testJSON(t, RSSConfig{
			URL: server.URL, TimeoutSeconds: 5,
		}),
		State: []byte(state),
	}
	observation, err := NewRSSChecker().Inspect(context.Background(), monitor)
	if err != nil {
		t.Fatal(err)
	}
	if !observation.Available || observation.Type != "rss.item" || observation.Fingerprint != "rss:guid:new" {
		t.Fatalf("unexpected observation: %#v", observation)
	}
	rss := observation.Payload["rss"].(map[string]any)
	if rss["title"] != "Newest" || rss["content"] != "new body" || rss["sourceTitle"] != "Example feed" {
		t.Fatalf("unexpected RSS payload: %#v", rss)
	}
	if string(monitor.State) != state {
		t.Fatalf("Inspect changed monitor state: %s", monitor.State)
	}

	empty = true
	observation, err = NewRSSChecker().Inspect(context.Background(), monitor)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Available || observation.Message != "feed contains no items" {
		t.Fatalf("unexpected empty-feed observation: %#v", observation)
	}
	rss = observation.Payload["rss"].(map[string]any)
	if rss["sourceTitle"] != "Example feed" || rss["sourceLink"] != "https://example.com" {
		t.Fatalf("empty-feed source fields = %#v", rss)
	}
}

func TestTestFlightInspectReturnsEveryCurrentStatus(t *testing.T) {
	body := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	checker := NewTestFlightChecker()
	monitor := model.Monitor{
		Type: model.MonitorTypeTestFlight,
		Config: testJSON(t, TestFlightConfig{
			URL: server.URL, TimeoutSeconds: 5,
		}),
		State: []byte(`{"initialized":true,"lastStatus":"available"}`),
	}

	for _, test := range []struct {
		name   string
		body   string
		status string
	}{
		{name: "full", body: "This beta is full.", status: "full"},
		{name: "available", body: "Start testing. View in TestFlight.", status: "available"},
		{name: "unknown", body: "A page that has no known marker.", status: "unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			body = test.body
			observation, err := checker.Inspect(context.Background(), monitor)
			if err != nil {
				t.Fatal(err)
			}
			values := observation.Payload["testflight"].(map[string]any)
			if !observation.Available || observation.Type != "testflight.status" || values["status"] != test.status || values["url"] != server.URL {
				t.Fatalf("unexpected observation: %#v", observation)
			}
		})
	}
}

func TestWebpageInspectComparesCurrentHashWithoutAdvancingState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><body><main id="target">Current   value</main><p>ignored</p></body></html>`))
	}))
	defer server.Close()
	checker := NewWebpageChecker()
	config := testJSON(t, WebpageConfig{URL: server.URL, Selector: "#target", TimeoutSeconds: 5})
	oldHash := strings.Repeat("a", 64)
	state := `{"initialized":true,"lastHash":"` + oldHash + `"}`
	monitor := model.Monitor{Type: model.MonitorTypeWebpage, Config: config, State: []byte(state)}

	observation, err := checker.Inspect(context.Background(), monitor)
	if err != nil {
		t.Fatal(err)
	}
	values := observation.Payload["webpage"].(map[string]any)
	expectedSum := sha256.Sum256([]byte("Current value"))
	expectedHash := hex.EncodeToString(expectedSum[:])
	if !observation.Available || values["summary"] != "Current value" || values["oldHash"] != oldHash || values["newHash"] != expectedHash || values["status"] != "changed" {
		t.Fatalf("unexpected changed observation: %#v", observation)
	}
	if string(monitor.State) != state {
		t.Fatalf("Inspect changed monitor state: %s", monitor.State)
	}

	monitor.State = testJSON(t, webpageState{Initialized: true, LastHash: expectedHash})
	observation, err = checker.Inspect(context.Background(), monitor)
	if err != nil {
		t.Fatal(err)
	}
	values = observation.Payload["webpage"].(map[string]any)
	if values["status"] != "unchanged" {
		t.Fatalf("same hash status = %#v", values["status"])
	}

	monitor.State = nil
	observation, err = checker.Inspect(context.Background(), monitor)
	if err != nil {
		t.Fatal(err)
	}
	values = observation.Payload["webpage"].(map[string]any)
	if values["status"] != "initialized" || values["oldHash"] != "" {
		t.Fatalf("initial observation values = %#v", values)
	}
}

func TestGitHubReleaseInspectBypassesETagAndReturnsFirstFilteredRelease(t *testing.T) {
	empty := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if value := r.Header.Get("If-None-Match"); value != "" {
			t.Errorf("Inspect sent If-None-Match %q", value)
		}
		w.Header().Set("Content-Type", "application/json")
		if empty {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = w.Write([]byte(`[
			{"id":9,"tag_name":"draft","draft":true},
			{"id":8,"tag_name":"v2-rc","prerelease":true},
			{"id":7,"tag_name":"v1.1","name":"Latest stable","body":"notes","html_url":"https://github.com/acme/widget/releases/tag/v1.1","published_at":"2026-07-17T00:00:00Z","author":{"login":"alice"}},
			{"id":6,"tag_name":"v1.0","name":"Older stable"}
		]`))
	}))
	defer server.Close()

	state := `{"initialized":true,"source":"` + server.URL + `|acme/widget|prerelease=false","etag":"old-etag","seenReleaseIds":[7]}`
	monitor := model.Monitor{
		Type: model.MonitorTypeGitHubRelease,
		Config: testJSON(t, GitHubReleaseConfig{
			Repository: "acme/widget", APIURL: server.URL, TimeoutSeconds: 5,
		}),
		State: []byte(state),
	}
	observation, err := NewGitHubReleaseChecker().Inspect(context.Background(), monitor)
	if err != nil {
		t.Fatal(err)
	}
	github := observation.Payload["github"].(map[string]any)
	release := github["release"].(map[string]any)
	if !observation.Available || observation.Fingerprint != "github:release:7" || release["name"] != "Latest stable" || release["id"] != int64(7) {
		t.Fatalf("unexpected observation: %#v", observation)
	}
	if string(monitor.State) != state {
		t.Fatalf("Inspect changed monitor state: %s", monitor.State)
	}

	empty = true
	observation, err = NewGitHubReleaseChecker().Inspect(context.Background(), monitor)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Available || observation.Message != "no published releases found" {
		t.Fatalf("unexpected no-release observation: %#v", observation)
	}
	github = observation.Payload["github"].(map[string]any)
	if github["repository"] != "acme/widget" {
		t.Fatalf("no-release source fields = %#v", github)
	}
}
