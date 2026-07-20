package datetime

import (
	"reflect"
	"testing"
	"time"
)

func TestSupportedFormatsAndGoLayouts(t *testing.T) {
	want := []string{
		"yyyy-MM-dd HH:mm:ss",
		"yyyy-MM-dd HH:mm",
		"MM-dd-yyyy HH:mm:ss",
	}
	if got := SupportedFormats(); !reflect.DeepEqual(got, want) {
		t.Fatalf("SupportedFormats() = %#v, want %#v", got, want)
	}

	tests := map[string]string{
		FormatYearMonthDay:   "2006-01-02 15:04:05",
		FormatYearMonthDayHM: "2006-01-02 15:04",
		FormatMonthDayYear:   "01-02-2006 15:04:05",
	}
	for format, wantLayout := range tests {
		layout, ok := GoLayout(format)
		if !ok || layout != wantLayout {
			t.Errorf("GoLayout(%q) = %q, %v; want %q, true", format, layout, ok, wantLayout)
		}
	}
	if _, ok := GoLayout("RFC3339"); ok {
		t.Fatal("GoLayout accepted unsupported format")
	}
}

func TestFormatUsesConfiguredIANAZoneAndLayout(t *testing.T) {
	instant := time.Date(2026, time.July, 20, 6, 52, 58, 610003331, time.UTC)
	tests := map[string]string{
		FormatYearMonthDay:   "2026-07-20 14:52:58",
		FormatYearMonthDayHM: "2026-07-20 14:52",
		FormatMonthDayYear:   "07-20-2026 14:52:58",
	}
	for format, want := range tests {
		got, err := Format(instant, "Asia/Shanghai", format)
		if err != nil {
			t.Fatalf("Format(%q): %v", format, err)
		}
		if got != want {
			t.Errorf("Format(%q) = %q, want %q", format, got, want)
		}
	}
}

func TestFormatRejectsInvalidSettings(t *testing.T) {
	if _, err := Format(time.Now(), "Local", DefaultFormat); err == nil {
		t.Fatal("Format accepted the non-IANA Local location")
	}
	if _, err := Format(time.Now(), "UTC+8", DefaultFormat); err == nil {
		t.Fatal("Format accepted an invalid timezone")
	}
	if _, err := Format(time.Now(), "UTC", "2006-01-02"); err == nil {
		t.Fatal("Format accepted an unsupported persisted format")
	}
}

func TestDeploymentTimeZoneUsesTZOrPortableFallback(t *testing.T) {
	t.Setenv("TZ", "Asia/Shanghai")
	if got := DeploymentTimeZone(); got != "Asia/Shanghai" {
		t.Fatalf("DeploymentTimeZone() = %q, want Asia/Shanghai", got)
	}

	t.Setenv("TZ", "UTC+8")
	got := DeploymentTimeZone()
	if err := ValidateTimeZone(got); err != nil || got == "Local" {
		t.Fatalf("DeploymentTimeZone() returned non-portable %q: %v", got, err)
	}
}
