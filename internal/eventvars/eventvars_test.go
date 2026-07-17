package eventvars

import (
	"reflect"
	"testing"

	"github.com/watchbell/watchbell/internal/model"
)

func TestEnrichPayloadMapsGlobalVariablesForEveryModule(t *testing.T) {
	tests := []struct {
		name        string
		monitorType string
		payload     map[string]any
		want        map[string]any
	}{
		{
			name: "rss", monitorType: model.MonitorTypeRSS,
			payload: map[string]any{"rss": map[string]any{
				"title": "Item", "link": "https://example.com/item", "summary": "Summary", "content": "Body",
				"author": "Alice", "publishedAt": "2026-07-17T12:00:00Z",
			}},
			want: map[string]any{"url": "https://example.com/item", "title": "Item", "summary": "Summary", "content": "Body", "author": "Alice", "publishedAt": "2026-07-17T12:00:00Z", "status": "published"},
		},
		{
			name: "testflight", monitorType: model.MonitorTypeTestFlight,
			payload: map[string]any{"testflight": map[string]any{
				"url": "https://testflight.apple.com/join/code", "status": "available", "message": "Slots available",
			}},
			want: map[string]any{"url": "https://testflight.apple.com/join/code", "title": "Monitor", "summary": "Slots available", "content": "Slots available", "author": "", "publishedAt": "", "status": "available"},
		},
		{
			name: "webpage", monitorType: model.MonitorTypeWebpage,
			payload: map[string]any{"webpage": map[string]any{
				"url": "https://example.com/page", "summary": "Changed text",
			}},
			want: map[string]any{"url": "https://example.com/page", "title": "Monitor", "summary": "Changed text", "content": "Changed text", "author": "", "publishedAt": "", "status": "changed"},
		},
		{
			name: "github", monitorType: model.MonitorTypeGitHubRelease,
			payload: map[string]any{"github": map[string]any{"release": map[string]any{
				"name": "Version 2", "tagName": "v2", "body": "Notes", "url": "https://github.com/acme/app/releases/tag/v2",
				"author": "octocat", "publishedAt": "2026-07-17T12:00:00Z", "prerelease": false,
			}}},
			want: map[string]any{"url": "https://github.com/acme/app/releases/tag/v2", "title": "Version 2", "summary": "Notes", "content": "Notes", "author": "octocat", "publishedAt": "2026-07-17T12:00:00Z", "status": "released"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := EnrichPayload(model.Monitor{Name: "Monitor", Type: test.monitorType}, test.payload)
			for key, want := range test.want {
				if !reflect.DeepEqual(got[key], want) {
					t.Fatalf("%s = %#v, want %#v", key, got[key], want)
				}
			}
			if _, retained := got[moduleRoot(test.monitorType)]; !retained {
				t.Fatalf("namespaced payload was not retained: %#v", got)
			}
		})
	}
}

func TestCatalogAndRuleKeysStayInSync(t *testing.T) {
	catalog := VariableCatalog()
	if len(catalog.Globals) == 0 || len(catalog.Modules) != 4 {
		t.Fatalf("unexpected catalog: %#v", catalog)
	}
	for _, module := range catalog.Modules {
		keys := EventVariableKeys(module.ID)
		for _, definition := range append(catalog.Globals, module.Variables...) {
			if !contains(keys, definition.Key) {
				t.Fatalf("%s is documented but missing from %s rule keys", definition.Key, module.ID)
			}
		}
	}
}

func TestEventDataProtectsReservedAndGlobalVariables(t *testing.T) {
	monitor := model.Monitor{ID: 7, Name: "Protected feed", Type: model.MonitorTypeRSS, Config: []byte(`{"url":"https://example.com/feed.xml"}`)}
	event := model.Event{ID: 11, Type: "rss.item", Fingerprint: "safe"}
	data := EventData(monitor, event, map[string]any{
		"monitor": map[string]any{"name": "attacker"},
		"event":   map[string]any{"id": -1},
		"url":     "https://attacker.invalid",
		"rss":     map[string]any{"title": "Item"},
	})
	if data["url"] != "https://example.com/feed.xml" {
		t.Fatalf("url fallback or override protection failed: %#v", data["url"])
	}
	monitorData := data["monitor"].(map[string]any)
	eventData := data["event"].(map[string]any)
	if monitorData["name"] != "Protected feed" || eventData["id"] != int64(11) {
		t.Fatalf("reserved context was overwritten: monitor=%#v event=%#v", monitorData, eventData)
	}
}

func moduleRoot(monitorType string) string {
	if monitorType == model.MonitorTypeGitHubRelease {
		return "github"
	}
	return monitorType
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
