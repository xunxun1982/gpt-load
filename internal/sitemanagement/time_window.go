package sitemanagement

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const fallbackTimezoneName = "Asia/Shanghai"

// beijingLocation is the fallback timezone for site-management dates.
var beijingLocation = time.FixedZone(fallbackTimezoneName, 8*60*60)

var (
	systemLocalTimezoneNameOnce  sync.Once
	systemLocalTimezoneNameValue string
)

func checkinLocation() *time.Location {
	loc, _ := checkinLocationWithName()
	return loc
}

func checkinLocationWithName() (*time.Location, string) {
	tz := strings.TrimSpace(os.Getenv("TZ"))
	if tz == "" {
		return localCheckinLocationWithName()
	}
	// Only IANA location names are accepted here. Go's time.LoadLocation does
	// not parse POSIX TZ strings or absolute TZ paths; unsupported values keep
	// the existing site-management fallback to Beijing time.
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return beijingLocation, fallbackTimezoneName
	}
	return loc, tz
}

func localCheckinLocationWithName() (*time.Location, string) {
	loc := time.Local
	if loc == nil {
		return beijingLocation, fallbackTimezoneName
	}
	name := strings.TrimSpace(loc.String())
	if name == "" || name == "Local" {
		if systemName := systemLocalTimezoneName(); systemName != "" {
			if systemLoc, err := time.LoadLocation(systemName); err == nil {
				return systemLoc, systemName
			}
		}
	}
	if name == "" {
		return beijingLocation, fallbackTimezoneName
	}
	// time.Local is initialized by Go from the host-local timezone when TZ is
	// unset, so site-management follows the server's resolved local clock here.
	return loc, name
}

func systemLocalTimezoneName() string {
	systemLocalTimezoneNameOnce.Do(func() {
		systemLocalTimezoneNameValue = detectSystemLocalTimezoneName()
	})
	return systemLocalTimezoneNameValue
}

func detectSystemLocalTimezoneName() string {
	for _, path := range []string{"/etc/timezone"} {
		if data, err := os.ReadFile(path); err == nil {
			if name := verifiedLocationName(string(data)); name != "" {
				return name
			}
		}
	}
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		target = strings.ReplaceAll(target, "\\", "/")
		if index := strings.Index(target, "/zoneinfo/"); index >= 0 {
			if name := verifiedLocationName(target[index+len("/zoneinfo/"):]); name != "" {
				return name
			}
		}
	}
	return ""
}

func verifiedLocationName(candidate string) string {
	name := strings.TrimSpace(candidate)
	if name == "" {
		return ""
	}
	if _, err := time.LoadLocation(name); err != nil {
		return ""
	}
	return name
}

// GetBeijingCheckinDay returns the current "check-in day" in TZ or server-local time.
// The day resets at local midnight.
// Returns date in "YYYY-MM-DD" format.
func GetBeijingCheckinDay() string {
	return GetBeijingCheckinDayAt(time.Now())
}

// GetBeijingCheckinDayAt returns the "check-in day" for a given time in TZ or server-local time.
// The day resets at local midnight.
// Returns date in "YYYY-MM-DD" format.
func GetBeijingCheckinDayAt(t time.Time) string {
	return t.In(checkinLocation()).Format("2006-01-02")
}

func nextCheckinResetAt(base time.Time) time.Time {
	loc := checkinLocation()
	now := base.In(loc)
	reset := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	if !reset.After(now) {
		reset = reset.AddDate(0, 0, 1)
	}
	return reset
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
