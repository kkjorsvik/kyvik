package timeutil

import (
	"strings"
	"time"
)

const dbTimestampLayout = "2006-01-02 15:04:05"

var parseLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05Z07:00",
	"2006-01-02 15:04:05 -0700 MST",
}

// NowUTC returns the current instant in UTC.
func NowUTC() time.Time {
	return time.Now().UTC()
}

// ParseTimestampUTC parses known timestamp formats and normalizes to UTC.
// Naive values are interpreted as UTC.
func ParseTimestampUTC(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range parseLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	t, err := time.ParseInLocation(dbTimestampLayout, s, time.UTC)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}
