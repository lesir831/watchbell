package checker

import (
	"testing"

	"github.com/watchbell/watchbell/internal/model"
)

func TestBuiltinPluginRegistry(t *testing.T) {
	registry := NewRegistry(
		NewRSSChecker(),
		NewTestFlightChecker(),
		NewWebpageChecker(),
		NewGitHubReleaseChecker(),
	)

	for _, pluginID := range []string{
		model.MonitorTypeRSS,
		model.MonitorTypeTestFlight,
		model.MonitorTypeWebpage,
		model.MonitorTypeGitHubRelease,
	} {
		if !registry.Has(pluginID) {
			t.Errorf("plugin %q is not registered", pluginID)
		}
	}

	plugins := registry.Plugins()
	if len(plugins) != 4 {
		t.Fatalf("got %d plugins, want 4", len(plugins))
	}
	for _, plugin := range plugins {
		if !plugin.Builtin || plugin.ID == "" || plugin.Name == "" || plugin.DefaultConfig == nil {
			t.Errorf("invalid plugin descriptor: %#v", plugin)
		}
	}
}
