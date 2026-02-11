package sitemanagement

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestGetBeijingCheckinDayAt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		inputTime    time.Time
		expectedDate string
	}{
		{
			name:         "Before 05:00 Beijing time - should use previous day",
			inputTime:    time.Date(2024, 1, 15, 4, 59, 0, 0, beijingLocation),
			expectedDate: "2024-01-14",
		},
		{
			name:         "Exactly 05:00 Beijing time - should use current day",
			inputTime:    time.Date(2024, 1, 15, 5, 0, 0, 0, beijingLocation),
			expectedDate: "2024-01-15",
		},
		{
			name:         "After 05:00 Beijing time - should use current day",
			inputTime:    time.Date(2024, 1, 15, 5, 1, 0, 0, beijingLocation),
			expectedDate: "2024-01-15",
		},
		{
			name:         "Midnight Beijing time - should use previous day",
			inputTime:    time.Date(2024, 1, 15, 0, 0, 0, 0, beijingLocation),
			expectedDate: "2024-01-14",
		},
		{
			name:         "Noon Beijing time - should use current day",
			inputTime:    time.Date(2024, 1, 15, 12, 0, 0, 0, beijingLocation),
			expectedDate: "2024-01-15",
		},
		{
			name:         "23:59 Beijing time - should use current day",
			inputTime:    time.Date(2024, 1, 15, 23, 59, 0, 0, beijingLocation),
			expectedDate: "2024-01-15",
		},
		{
			name:         "UTC time exactly 05:00 Beijing (21:00 UTC previous day)",
			inputTime:    time.Date(2024, 1, 14, 21, 0, 0, 0, time.UTC), // 05:00 Beijing next day
			expectedDate: "2024-01-15",
		},
		{
			name:         "UTC time after 05:00 Beijing (22:00 UTC previous day)",
			inputTime:    time.Date(2024, 1, 14, 22, 0, 0, 0, time.UTC), // 06:00 Beijing next day
			expectedDate: "2024-01-15",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := GetBeijingCheckinDayAt(tt.inputTime)
			assert.Equal(t, tt.expectedDate, result)
		})
	}
}

func TestGetBeijingCheckinDay_CurrentTime(t *testing.T) {
	// Non-parallel test since it uses current time
	result := GetBeijingCheckinDay()

	// Verify format is correct (YYYY-MM-DD)
	assert.Len(t, result, 10)
	assert.Equal(t, "-", string(result[4]))
	assert.Equal(t, "-", string(result[7]))

	// Verify it's a valid date
	_, err := time.Parse("2006-01-02", result)
	assert.NoError(t, err)
}

func TestParseTimeToMinutes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		input         string
		expectedMins  int
		expectedError bool
	}{
		{
			name:          "Valid time 00:00",
			input:         "00:00",
			expectedMins:  0,
			expectedError: false,
		},
		{
			name:          "Valid time 05:30",
			input:         "05:30",
			expectedMins:  330, // 5*60 + 30
			expectedError: false,
		},
		{
			name:          "Valid time 12:45",
			input:         "12:45",
			expectedMins:  765, // 12*60 + 45
			expectedError: false,
		},
		{
			name:          "Valid time 23:59",
			input:         "23:59",
			expectedMins:  1439, // 23*60 + 59
			expectedError: false,
		},
		{
			name:          "Valid time with spaces",
			input:         " 10:30 ",
			expectedMins:  630,
			expectedError: false,
		},
		{
			name:          "Invalid format - no colon",
			input:         "1030",
			expectedMins:  0,
			expectedError: true,
		},
		{
			name:          "Invalid format - too many parts",
			input:         "10:30:00",
			expectedMins:  0,
			expectedError: true,
		},
		{
			name:          "Invalid hour - negative",
			input:         "-1:30",
			expectedMins:  0,
			expectedError: true,
		},
		{
			name:          "Invalid hour - too large",
			input:         "24:00",
			expectedMins:  0,
			expectedError: true,
		},
		{
			name:          "Invalid minute - negative",
			input:         "10:-1",
			expectedMins:  0,
			expectedError: true,
		},
		{
			name:          "Invalid minute - too large",
			input:         "10:60",
			expectedMins:  0,
			expectedError: true,
		},
		{
			name:          "Invalid hour - not a number",
			input:         "abc:30",
			expectedMins:  0,
			expectedError: true,
		},
		{
			name:          "Invalid minute - not a number",
			input:         "10:xyz",
			expectedMins:  0,
			expectedError: true,
		},
		{
			name:          "Empty string",
			input:         "",
			expectedMins:  0,
			expectedError: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := parseTimeToMinutes(tt.input)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedMins, result)
			}
		})
	}
}

func TestIsMinutesWithinWindow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		minutes     int
		windowStart int
		windowEnd   int
		expected    bool
	}{
		{
			name:        "Within normal window",
			minutes:     600, // 10:00
			windowStart: 540, // 09:00
			windowEnd:   720, // 12:00
			expected:    true,
		},
		{
			name:        "At window start",
			minutes:     540, // 09:00
			windowStart: 540, // 09:00
			windowEnd:   720, // 12:00
			expected:    true,
		},
		{
			name:        "At window end",
			minutes:     720, // 12:00
			windowStart: 540, // 09:00
			windowEnd:   720, // 12:00
			expected:    true,
		},
		{
			name:        "Before window start",
			minutes:     480, // 08:00
			windowStart: 540, // 09:00
			windowEnd:   720, // 12:00
			expected:    false,
		},
		{
			name:        "After window end",
			minutes:     780, // 13:00
			windowStart: 540, // 09:00
			windowEnd:   720, // 12:00
			expected:    false,
		},
		{
			name:        "Overnight window - within (before midnight)",
			minutes:     1380, // 23:00
			windowStart: 1320, // 22:00
			windowEnd:   60,   // 01:00
			expected:    true,
		},
		{
			name:        "Overnight window - within (after midnight)",
			minutes:     30,   // 00:30
			windowStart: 1320, // 22:00
			windowEnd:   60,   // 01:00
			expected:    true,
		},
		{
			name:        "Overnight window - outside",
			minutes:     120,  // 02:00
			windowStart: 1320, // 22:00
			windowEnd:   60,   // 01:00
			expected:    false,
		},
		{
			name:        "Same start and end - always false",
			minutes:     600,
			windowStart: 600,
			windowEnd:   600,
			expected:    false,
		},
		{
			name:        "Midnight crossing - at start",
			minutes:     1320, // 22:00
			windowStart: 1320, // 22:00
			windowEnd:   60,   // 01:00
			expected:    true,
		},
		{
			name:        "Midnight crossing - at end",
			minutes:     60,   // 01:00
			windowStart: 1320, // 22:00
			windowEnd:   60,   // 01:00
			expected:    true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := isMinutesWithinWindow(tt.minutes, tt.windowStart, tt.windowEnd)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// Benchmark tests
func BenchmarkGetBeijingCheckinDayAt(b *testing.B) {
	b.ReportAllocs()

	testTime := time.Date(2024, 1, 15, 12, 0, 0, 0, beijingLocation)

	for i := 0; i < b.N; i++ {
		_ = GetBeijingCheckinDayAt(testTime)
	}
}

func BenchmarkParseTimeToMinutes(b *testing.B) {
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, _ = parseTimeToMinutes("12:45")
	}
}

func BenchmarkIsMinutesWithinWindow(b *testing.B) {
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = isMinutesWithinWindow(600, 540, 720)
	}
}
