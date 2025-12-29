package sitemanagement

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// beijingLocation is the timezone for Beijing (UTC+8).
// Used for calculating check-in day with 05:00 reset.
//
// Design Decision: Using time.FixedZone instead of time.LoadLocation because:
// 1. China does not observe Daylight Saving Time (DST), so UTC+8 is always correct
// 2. time.LoadLocation requires tzdata which may not be available in minimal Docker images
// 3. time.FixedZone is faster and more reliable for fixed-offset timezones
// 4. AI review suggested LoadLocation, but FixedZone is the better choice for non-DST regions
var beijingLocation = time.FixedZone("Asia/Shanghai", 8*60*60)

// GetBeijingCheckinDay returns the current "check-in day" in Beijing time (UTC+8).
// The day resets at 05:00 Beijing time, not midnight.
// Returns date in "YYYY-MM-DD" format.
func GetBeijingCheckinDay() string {
	return GetBeijingCheckinDayAt(time.Now())
}

// GetBeijingCheckinDayAt returns the "check-in day" for a given time in Beijing time (UTC+8).
// The day resets at 05:00 Beijing time, not midnight.
// Returns date in "YYYY-MM-DD" format.
func GetBeijingCheckinDayAt(t time.Time) string {
	const checkinResetHour = 5 // Check-in day resets at 05:00 Beijing time
	beijingTime := t.In(beijingLocation)
	// If before 05:00 Beijing time, consider it as previous day
	if beijingTime.Hour() < checkinResetHour {
		beijingTime = beijingTime.AddDate(0, 0, -1)
	}
	return beijingTime.Format("2006-01-02")
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
