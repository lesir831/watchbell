// Package datetime owns WatchBell's user-selectable date/time display policy.
//
// Persisted formats intentionally use stable, language-neutral tokens instead
// of Go's reference-time layouts. Keeping the conversion here gives API,
// template, and other rendering paths one source of truth.
package datetime

import (
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	DefaultFormat        = "yyyy-MM-dd HH:mm:ss"
	FormatYearMonthDay   = DefaultFormat
	FormatYearMonthDayHM = "yyyy-MM-dd HH:mm"
	FormatMonthDayYear   = "MM-dd-yyyy HH:mm:ss"
	DefaultTimeZone      = "UTC"
)

var formatLayouts = map[string]string{
	FormatYearMonthDay:   "2006-01-02 15:04:05",
	FormatYearMonthDayHM: "2006-01-02 15:04",
	FormatMonthDayYear:   "01-02-2006 15:04:05",
}

// SupportedFormats returns the stable values accepted by the runtime settings
// API, in the order they should normally be presented to users.
func SupportedFormats() []string {
	return []string{FormatYearMonthDay, FormatYearMonthDayHM, FormatMonthDayYear}
}

// GoLayout converts a supported persisted format to Go's reference-time
// layout. The boolean is false for every value outside the documented list.
func GoLayout(format string) (string, bool) {
	layout, ok := formatLayouts[format]
	return layout, ok
}

func ValidateFormat(format string) error {
	if _, ok := GoLayout(format); !ok {
		return fmt.Errorf("unsupported date/time format %q", format)
	}
	return nil
}

// LoadLocation accepts IANA location names (including UTC) and deliberately
// rejects Go's process-local pseudo-location, which is not portable across
// deployments.
func LoadLocation(timezone string) (*time.Location, error) {
	if timezone == "" {
		return nil, fmt.Errorf("timezone is required")
	}
	if timezone == "Local" {
		return nil, fmt.Errorf("timezone must be an IANA name")
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, fmt.Errorf("invalid IANA timezone %q: %w", timezone, err)
	}
	return location, nil
}

func ValidateTimeZone(timezone string) error {
	_, err := LoadLocation(timezone)
	return err
}

// DeploymentTimeZone returns a portable IANA default from the standard TZ
// environment variable or the process location. Environments that expose only
// Go's opaque "Local" location fall back to UTC.
func DeploymentTimeZone() string {
	for _, candidate := range []string{strings.TrimSpace(os.Getenv("TZ")), time.Local.String()} {
		if candidate == "" || candidate == "Local" {
			continue
		}
		if _, err := LoadLocation(candidate); err == nil {
			return candidate
		}
	}
	return DefaultTimeZone
}

// Format renders an instant using one of the persisted formats in the
// requested IANA time zone.
func Format(value time.Time, timezone, format string) (string, error) {
	location, err := LoadLocation(timezone)
	if err != nil {
		return "", err
	}
	layout, ok := GoLayout(format)
	if !ok {
		return "", fmt.Errorf("unsupported date/time format %q", format)
	}
	return value.In(location).Format(layout), nil
}
