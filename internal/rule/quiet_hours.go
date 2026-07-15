package rule

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/watchbell/watchbell/internal/model"
)

var clockPattern = regexp.MustCompile(`^(?:[01][0-9]|2[0-3]):[0-5][0-9]$`)

// ValidateQuietHours returns the invalid field name along with a user-facing
// error. Disabled quiet hours intentionally accept an empty configuration.
func ValidateQuietHours(value model.QuietHours) (string, error) {
	if !value.Enabled {
		return "", nil
	}
	if !clockPattern.MatchString(value.Start) {
		return "start", fmt.Errorf("开始时间必须使用 HH:mm 格式")
	}
	if !clockPattern.MatchString(value.End) {
		return "end", fmt.Errorf("结束时间必须使用 HH:mm 格式")
	}
	if value.Start == value.End {
		return "end", fmt.Errorf("结束时间不能与开始时间相同")
	}
	timezone := strings.TrimSpace(value.Timezone)
	if timezone == "" {
		return "timezone", fmt.Errorf("请选择 IANA 时区")
	}
	if timezone == "Local" {
		return "timezone", fmt.Errorf("时区必须使用 IANA 名称，例如 Asia/Shanghai")
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		return "timezone", fmt.Errorf("时区不是有效的 IANA 时区")
	}
	return "", nil
}

// QuietHoursActiveAt evaluates the instant in the configured location. Using
// the location's wall-clock time makes overnight windows and DST transitions
// behave consistently without constructing ambiguous local timestamps.
func QuietHoursActiveAt(value model.QuietHours, instant time.Time) (bool, error) {
	if _, err := ValidateQuietHours(value); err != nil {
		return false, err
	}
	if !value.Enabled {
		return false, nil
	}
	location, err := time.LoadLocation(strings.TrimSpace(value.Timezone))
	if err != nil {
		return false, err
	}
	local := instant.In(location)
	nowMinute := local.Hour()*60 + local.Minute()
	startMinute := clockMinute(value.Start)
	endMinute := clockMinute(value.End)
	if startMinute < endMinute {
		return nowMinute >= startMinute && nowMinute < endMinute, nil
	}
	return nowMinute >= startMinute || nowMinute < endMinute, nil
}

func clockMinute(value string) int {
	hour, _ := strconv.Atoi(value[:2])
	minute, _ := strconv.Atoi(value[3:])
	return hour*60 + minute
}
