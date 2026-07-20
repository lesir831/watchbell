package checker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/watchbell/watchbell/internal/model"
)

func TestGitHubReleaseCheckerDetectsNewReleaseAndUsesETag(t *testing.T) {
	mode := 1
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/repos/acme/widget/releases" {
			t.Errorf("unexpected path %q", got)
		}
		if got := r.URL.Query().Get("per_page"); got != "20" {
			t.Errorf("unexpected per_page %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("unexpected authorization header %q", got)
		}
		if got := r.Header.Get("X-GitHub-Api-Version"); got != defaultGitHubAPIVersion {
			t.Errorf("unexpected API version %q", got)
		}

		if mode == 304 {
			if got := r.Header.Get("If-None-Match"); got != `"release-v1"` {
				t.Errorf("unexpected If-None-Match %q", got)
			}
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if mode == 1 {
			w.Header().Set("ETag", `"release-v1"`)
			_, _ = w.Write([]byte(`[{
				"id": 1, "tag_name": "v1.0.0", "name": "First", "body": "notes",
				"html_url": "https://github.com/acme/widget/releases/tag/v1.0.0",
				"published_at": "2026-07-01T00:00:00Z", "author": {"login": "alice"}
			}]`))
			return
		}
		w.Header().Set("ETag", `"release-v2"`)
		_, _ = w.Write([]byte(`[
			{"id": 2, "tag_name": "v1.1.0", "name": "Second", "body": "new notes",
			 "html_url": "https://github.com/acme/widget/releases/tag/v1.1.0",
			 "published_at": "2026-07-02T00:00:00Z", "author": {"login": "bob"},
			 "assets": [{"name": "widget.zip", "browser_download_url": "https://example.com/widget.zip", "size": 42}]},
			{"id": 1, "tag_name": "v1.0.0", "name": "First", "published_at": "2026-07-01T00:00:00Z"}
		]`))
	}))
	defer server.Close()

	checker := NewGitHubReleaseChecker()
	monitor := model.Monitor{Config: testJSON(t, GitHubReleaseConfig{
		Repository: "acme/widget", Token: "test-token", APIURL: server.URL,
		TimeoutSeconds: 5, MaxReleases: 20,
	})}

	first, err := checker.Check(context.Background(), monitor)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Events) != 0 {
		t.Fatalf("first check emitted %d event(s), want 0", len(first.Events))
	}
	if first.Message != "最新版本：v1.0.0" {
		t.Fatalf("first message = %q", first.Message)
	}
	monitor.State = testJSON(t, first.State)

	mode = 304
	unchanged, err := checker.Check(context.Background(), monitor)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Message != "最新版本：v1.0.0" || len(unchanged.Events) != 0 {
		t.Fatalf("unexpected unchanged result: %#v", unchanged)
	}

	mode = 2
	result, err := checker.Check(context.Background(), monitor)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("got %d event(s), want 1", len(result.Events))
	}
	if result.Message != "最新版本：v1.1.0" {
		t.Fatalf("new release message = %q", result.Message)
	}
	event := result.Events[0]
	if event.Type != "github.release" || event.Fingerprint != "github:release:2" {
		t.Fatalf("unexpected event: %#v", event)
	}
	github := event.Payload["github"].(map[string]any)
	release := github["release"].(map[string]any)
	if release["tagName"] != "v1.1.0" || release["assetCount"] != 1 {
		t.Fatalf("unexpected release payload: %#v", release)
	}
}

func TestGitHubReleaseCheckerHandlesNoPublishedReleaseAndIts304(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 2 {
			if got := r.Header.Get("If-None-Match"); got != `"empty"` {
				t.Errorf("If-None-Match = %q, want empty ETag", got)
			}
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `"empty"`)
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	checker := NewGitHubReleaseChecker()
	monitor := model.Monitor{Config: testJSON(t, GitHubReleaseConfig{Repository: "acme/empty", APIURL: server.URL})}
	first, err := checker.Check(context.Background(), monitor)
	if err != nil {
		t.Fatal(err)
	}
	if first.Message != "暂无已发布版本" || len(first.Events) != 0 {
		t.Fatalf("first empty result = %#v", first)
	}

	monitor.State = testJSON(t, first.State)
	unchanged, err := checker.Check(context.Background(), monitor)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Message != "暂无已发布版本" || len(unchanged.Events) != 0 {
		t.Fatalf("unchanged empty result = %#v", unchanged)
	}
}

func TestGitHubReleaseCheckerRefreshesLegacyStateToLearnLatestVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("If-None-Match"); got != "" {
			t.Errorf("legacy state sent stale conditional ETag %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `"release-v3"`)
		_, _ = w.Write([]byte(`[{
			"id": 3, "tag_name": "v3.0.0", "name": "Third",
			"published_at": "2026-07-03T00:00:00Z"
		}]`))
	}))
	defer server.Close()

	checker := NewGitHubReleaseChecker()
	monitor := model.Monitor{
		Config: testJSON(t, GitHubReleaseConfig{Repository: "acme/widget", APIURL: server.URL}),
		State: testJSON(t, githubReleaseState{
			Initialized: true, Source: server.URL + "|acme/widget|prerelease=false",
			ETag: `"release-v2"`, SeenReleaseIDs: []int64{3},
		}),
	}
	result, err := checker.Check(context.Background(), monitor)
	if err != nil {
		t.Fatal(err)
	}
	if result.Message != "最新版本：v3.0.0" || len(result.Events) != 0 {
		t.Fatalf("legacy refresh result = %#v", result)
	}
}

func TestGitHubReleaseCheckerFiltersPrereleases(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"id": 2, "tag_name": "v2.0.0-rc.1", "prerelease": true, "published_at": "2026-07-02T00:00:00Z"},
			{"id": 1, "tag_name": "v1.0.0", "published_at": "2026-07-01T00:00:00Z"}
		]`))
	}))
	defer server.Close()

	checker := NewGitHubReleaseChecker()
	result, err := checker.Check(context.Background(), model.Monitor{Config: testJSON(t, GitHubReleaseConfig{
		Repository: "acme/widget", APIURL: server.URL, NotifyExisting: true,
	})})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 1 || result.Events[0].Fingerprint != "github:release:1" {
		t.Fatalf("unexpected events: %#v", result.Events)
	}
}

func testJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
