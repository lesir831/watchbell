package checker

import (
	"encoding/json"
	"testing"

	"github.com/watchbell/watchbell/internal/model"
)

func TestCinemaSchedulePluginStartsWithoutStaleBusinessSelections(t *testing.T) {
	plugin := NewCinemaScheduleChecker().Plugin()
	for _, key := range []string{"cityId", "cinemaId", "movieId"} {
		if plugin.DefaultConfig[key] != nil {
			t.Fatalf("default %s = %#v, want nil", key, plugin.DefaultConfig[key])
		}
	}
	for _, key := range []string{"cityName", "cinemaName", "movieName", "targetDate"} {
		if plugin.DefaultConfig[key] != "" {
			t.Fatalf("default %s = %#v, want empty string", key, plugin.DefaultConfig[key])
		}
	}
	fieldKeys := map[string]bool{}
	for _, field := range plugin.ConfigFields {
		fieldKeys[field.Key] = true
	}
	for _, key := range []string{"cityId", "cityName", "cinemaId", "cinemaName", "movieId", "movieName"} {
		if !fieldKeys[key] {
			t.Fatalf("plugin is missing config field %s", key)
		}
	}
}

func TestCinemaScheduleConfigKeepsLegacyIDsAndAcceptsDiscoveryMetadata(t *testing.T) {
	config := map[string]any{
		"provider": "maoyan", "cityId": 1, "cityName": " 北京 ",
		"cinemaId": 16655, "cinemaName": " 万达影城 ",
		"movieId": 1490607, "movieName": "蜘蛛侠：崭新之日", "targetDate": "2026-08-14",
		"hallPatterns": []string{"IMAX"}, "timeoutSeconds": 15,
	}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	decoded, _, err := decodeCinemaScheduleConfig(model.Monitor{Type: model.MonitorTypeCinemaSchedule, Config: raw})
	if err != nil {
		t.Fatal(err)
	}
	if decoded.CityID != 1 || decoded.CityName != "北京" || decoded.CinemaName != "万达影城" {
		t.Fatalf("unexpected discovery metadata: %#v", decoded)
	}

	legacy := model.Monitor{Type: model.MonitorTypeCinemaSchedule, Config: []byte(`{
		"provider":"maoyan","cinemaId":16655,"movieId":1490607,
		"movieName":"蜘蛛侠：崭新之日","targetDate":"2026-08-14","hallPatterns":["IMAX"]
	}`)}
	decoded, _, err = decodeCinemaScheduleConfig(legacy)
	if err != nil {
		t.Fatalf("legacy config without city metadata was rejected: %v", err)
	}
	if decoded.CityID != 0 || decoded.CityName != "" || decoded.CinemaID != 16655 {
		t.Fatalf("legacy config changed unexpectedly: %#v", decoded)
	}
}
