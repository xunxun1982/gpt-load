package sitemanagement

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// beijingLocation is the fallback timezone for site-management dates.
var beijingLocation = time.FixedZone("Asia/Shanghai", 8*60*60)

func checkinLocation() *time.Location {
	tz := strings.TrimSpace(os.Getenv("TZ"))
	if tz == "" {
		return beijingLocation
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return beijingLocation
	}
	return loc
}

// GetBeijingCheckinDay returns the current "check-in day" in TZ, defaulting to Beijing time.
// The day resets at local midnight.
// Returns date in "YYYY-MM-DD" format.
func GetBeijingCheckinDay() string {
	return GetBeijingCheckinDayAt(time.Now())
}

// GetBeijingCheckinDayAt returns the "check-in day" for a given time in TZ, defaulting to Beijing time.
// The day resets at local midnight.
// Returns date in "YYYY-MM-DD" format.
func GetBeijingCheckinDayAt(t time.Time) string {
	return t.In(checkinLocation()).Format("2006-01-02")
}

func parseTimeToMinutes(value string) (int, error) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 2 {
		return 0, fmt.Errorf("invalid format")
	}

	hour, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, fmt.Errorf("invalid hour")
	}
	minute, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, fmt.Errorf("invalid minute")
	}
	if hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, fmt.Errorf("out of range")
	}

	return hour*60 + minute, nil
}

func isMinutesWithinWindow(minutes, windowStart, windowEnd int) bool {
	if windowStart == windowEnd {
		return false
	}
	if windowStart < windowEnd {
		return minutes >= windowStart && minutes <= windowEnd
	}
	return minutes >= windowStart || minutes <= windowEnd
}
