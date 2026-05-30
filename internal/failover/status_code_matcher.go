package failover

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const DefaultStatusCodePattern = "400-403,405-999"

type statusCodeRange struct {
	start int
	end   int
}

// StatusCodeMatcher matches HTTP status codes against normalized inclusive ranges.
type StatusCodeMatcher struct {
	ranges []statusCodeRange
}

func DefaultStatusCodeMatcher() StatusCodeMatcher {
	matcher, err := ParseStatusCodeMatcher(DefaultStatusCodePattern)
	if err != nil {
		panic(err)
	}
	return matcher
}

func ParseStatusCodeMatcher(pattern string) (StatusCodeMatcher, error) {
	parts := strings.Split(pattern, ",")
	ranges := make([]statusCodeRange, 0, len(parts))

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return StatusCodeMatcher{}, fmt.Errorf("empty status code segment")
		}

		codeRange, err := parseStatusCodeRange(part)
		if err != nil {
			return StatusCodeMatcher{}, err
		}
		ranges = append(ranges, codeRange)
	}

	sort.Slice(ranges, func(i, j int) bool {
		if ranges[i].start == ranges[j].start {
			return ranges[i].end < ranges[j].end
		}
		return ranges[i].start < ranges[j].start
	})

	merged := ranges[:0]
	for _, current := range ranges {
		if len(merged) == 0 || current.start > merged[len(merged)-1].end+1 {
			merged = append(merged, current)
			continue
		}
		if current.end > merged[len(merged)-1].end {
			merged[len(merged)-1].end = current.end
		}
	}

	return StatusCodeMatcher{ranges: merged}, nil
}

func parseStatusCodeRange(part string) (statusCodeRange, error) {
	if strings.Contains(part, "-") {
		bounds := strings.Split(part, "-")
		if len(bounds) != 2 {
			return statusCodeRange{}, fmt.Errorf("invalid status code range %q", part)
		}
		start, err := parseStatusCode(strings.TrimSpace(bounds[0]))
		if err != nil {
			return statusCodeRange{}, err
		}
		end, err := parseStatusCode(strings.TrimSpace(bounds[1]))
		if err != nil {
			return statusCodeRange{}, err
		}
		if start > end {
			return statusCodeRange{}, fmt.Errorf("invalid descending status code range %q", part)
		}
		return statusCodeRange{start: start, end: end}, nil
	}

	code, err := parseStatusCode(part)
	if err != nil {
		return statusCodeRange{}, err
	}
	return statusCodeRange{start: code, end: code}, nil
}

func parseStatusCode(value string) (int, error) {
	if value == "" {
		return 0, fmt.Errorf("empty status code")
	}
	code, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("invalid status code %q: %w", value, err)
	}
	if code < 100 || code > 999 {
		return 0, fmt.Errorf("status code %d is outside supported range 100-999", code)
	}
	return code, nil
}

func (m StatusCodeMatcher) Match(statusCode int) bool {
	for _, r := range m.ranges {
		if statusCode < r.start {
			return false
		}
		if statusCode <= r.end {
			return true
		}
	}
	return false
}

func (m StatusCodeMatcher) IsZero() bool {
	return len(m.ranges) == 0
}
