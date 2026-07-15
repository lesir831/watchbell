package rule

import (
	"testing"
	"time"

	"github.com/watchbell/watchbell/internal/model"
)

func TestQuietHoursActiveAt(t *testing.T) {
	tests := []struct {
		name    string
		quiet   model.QuietHours
		instant string
		want    bool
	}{
		{
			name: "overnight before midnight", quiet: model.QuietHours{Enabled: true, Start: "22:00", End: "08:00", Timezone: "Asia/Shanghai"},
			instant: "2026-07-15T15:30:00Z", want: true,
		},
		{
			name: "overnight after midnight", quiet: model.QuietHours{Enabled: true, Start: "22:00", End: "08:00", Timezone: "Asia/Shanghai"},
			instant: "2026-07-14T23:30:00Z", want: true,
		},
		{
			name: "end is exclusive", quiet: model.QuietHours{Enabled: true, Start: "22:00", End: "08:00", Timezone: "Asia/Shanghai"},
			instant: "2026-07-15T00:00:00Z", want: false,
		},
		{
			name: "spring forward before jump", quiet: model.QuietHours{Enabled: true, Start: "01:30", End: "03:30", Timezone: "America/New_York"},
			instant: "2026-03-08T06:45:00Z", want: true,
		},
		{
			name: "spring forward after jump", quiet: model.QuietHours{Enabled: true, Start: "01:30", End: "03:30", Timezone: "America/New_York"},
			instant: "2026-03-08T07:15:00Z", want: true,
		},
		{
			name: "fall back first occurrence", quiet: model.QuietHours{Enabled: true, Start: "01:15", End: "01:45", Timezone: "America/New_York"},
			instant: "2026-11-01T05:30:00Z", want: true,
		},
		{
			name: "fall back repeated occurrence", quiet: model.QuietHours{Enabled: true, Start: "01:15", End: "01:45", Timezone: "America/New_York"},
			instant: "2026-11-01T06:30:00Z", want: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			instant, err := time.Parse(time.RFC3339, test.instant)
			if err != nil {
				t.Fatal(err)
			}
			got, err := QuietHoursActiveAt(test.quiet, instant)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("active = %v, want %v", got, test.want)
			}
		})
	}
}

func TestValidateQuietHours(t *testing.T) {
	for _, test := range []struct {
		name  string
		value model.QuietHours
		field string
	}{
		{name: "disabled can be empty", value: model.QuietHours{}},
		{name: "bad start", value: model.QuietHours{Enabled: true, Start: "9:00", End: "10:00", Timezone: "UTC"}, field: "start"},
		{name: "same time", value: model.QuietHours{Enabled: true, Start: "09:00", End: "09:00", Timezone: "UTC"}, field: "end"},
		{name: "bad timezone", value: model.QuietHours{Enabled: true, Start: "09:00", End: "10:00", Timezone: "UTC+8"}, field: "timezone"},
	} {
		t.Run(test.name, func(t *testing.T) {
			field, err := ValidateQuietHours(test.value)
			if field != test.field {
				t.Fatalf("field = %q, want %q (err=%v)", field, test.field, err)
			}
			if test.field == "" && err != nil {
				t.Fatal(err)
			}
			if test.field != "" && err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
