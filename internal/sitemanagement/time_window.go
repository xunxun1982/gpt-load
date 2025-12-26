package sitemanagement

import (
	"fmt"
	"strconv"
	"strings"
)

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
