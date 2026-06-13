package scheduler

import (
	"strings"
	"time"
)

// IsInQuietHours checks whether the given time falls within a quiet hours range.
// Format: "HH:MM-HH:MM" (e.g., "22:00-07:00"). Handles overnight ranges
// (where start > end, meaning crosses midnight). Returns false for empty or
// malformed input.
func IsInQuietHours(quietHours, timezone string, t time.Time) bool {
	if quietHours == "" {
		return false
	}

	parts := strings.SplitN(quietHours, "-", 2)
	if len(parts) != 2 {
		return false
	}

	startStr := strings.TrimSpace(parts[0])
	endStr := strings.TrimSpace(parts[1])

	startH, startM, ok1 := parseHHMM(startStr)
	endH, endM, ok2 := parseHHMM(endStr)
	if !ok1 || !ok2 {
		return false
	}

	// Convert the check time to the agent's timezone.
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		loc = time.UTC
	}
	local := t.In(loc)
	nowMinutes := local.Hour()*60 + local.Minute()
	startMinutes := startH*60 + startM
	endMinutes := endH*60 + endM

	if startMinutes <= endMinutes {
		// Normal range: e.g., "09:00-17:00"
		return nowMinutes >= startMinutes && nowMinutes < endMinutes
	}
	// Overnight range: e.g., "22:00-07:00"
	return nowMinutes >= startMinutes || nowMinutes < endMinutes
}

// parseHHMM parses "HH:MM" into hours and minutes.
func parseHHMM(s string) (h, m int, ok bool) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	h = atoi(parts[0])
	m = atoi(parts[1])
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, false
	}
	return h, m, true
}

// atoi converts a string to int, returning -1 on failure.
func atoi(s string) int {
	n := 0
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return -1
		}
		n = n*10 + int(ch-'0')
	}
	if len(s) == 0 {
		return -1
	}
	return n
}
