package scheduler

import (
	"testing"
	"time"
)

func TestIsInQuietHours(t *testing.T) {
	utc := "UTC"

	tests := []struct {
		name       string
		quietHours string
		timezone   string
		time       time.Time
		want       bool
	}{
		// Normal range: 09:00-17:00
		{
			name:       "normal range - inside (12:00)",
			quietHours: "09:00-17:00",
			timezone:   utc,
			time:       time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC),
			want:       true,
		},
		{
			name:       "normal range - outside (20:00)",
			quietHours: "09:00-17:00",
			timezone:   utc,
			time:       time.Date(2026, 1, 15, 20, 0, 0, 0, time.UTC),
			want:       false,
		},
		{
			name:       "normal range - at start boundary",
			quietHours: "09:00-17:00",
			timezone:   utc,
			time:       time.Date(2026, 1, 15, 9, 0, 0, 0, time.UTC),
			want:       true,
		},
		{
			name:       "normal range - at end boundary (exclusive)",
			quietHours: "09:00-17:00",
			timezone:   utc,
			time:       time.Date(2026, 1, 15, 17, 0, 0, 0, time.UTC),
			want:       false,
		},

		// Overnight range: 22:00-07:00
		{
			name:       "overnight range - inside (23:00)",
			quietHours: "22:00-07:00",
			timezone:   utc,
			time:       time.Date(2026, 1, 15, 23, 0, 0, 0, time.UTC),
			want:       true,
		},
		{
			name:       "overnight range - inside (05:00)",
			quietHours: "22:00-07:00",
			timezone:   utc,
			time:       time.Date(2026, 1, 16, 5, 0, 0, 0, time.UTC),
			want:       true,
		},
		{
			name:       "overnight range - outside (12:00)",
			quietHours: "22:00-07:00",
			timezone:   utc,
			time:       time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC),
			want:       false,
		},
		{
			name:       "overnight range - at start boundary",
			quietHours: "22:00-07:00",
			timezone:   utc,
			time:       time.Date(2026, 1, 15, 22, 0, 0, 0, time.UTC),
			want:       true,
		},
		{
			name:       "overnight range - at end boundary (exclusive)",
			quietHours: "22:00-07:00",
			timezone:   utc,
			time:       time.Date(2026, 1, 16, 7, 0, 0, 0, time.UTC),
			want:       false,
		},

		// Midnight edge cases
		{
			name:       "midnight - inside overnight range",
			quietHours: "22:00-07:00",
			timezone:   utc,
			time:       time.Date(2026, 1, 16, 0, 0, 0, 0, time.UTC),
			want:       true,
		},
		{
			name:       "midnight - outside normal range",
			quietHours: "09:00-17:00",
			timezone:   utc,
			time:       time.Date(2026, 1, 16, 0, 0, 0, 0, time.UTC),
			want:       false,
		},

		// Empty and malformed
		{
			name:       "empty string",
			quietHours: "",
			timezone:   utc,
			time:       time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC),
			want:       false,
		},
		{
			name:       "malformed - no dash",
			quietHours: "22:00",
			timezone:   utc,
			time:       time.Date(2026, 1, 15, 22, 0, 0, 0, time.UTC),
			want:       false,
		},
		{
			name:       "malformed - bad time format",
			quietHours: "abc-def",
			timezone:   utc,
			time:       time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC),
			want:       false,
		},
		{
			name:       "malformed - hours out of range",
			quietHours: "25:00-07:00",
			timezone:   utc,
			time:       time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC),
			want:       false,
		},

		// Timezone handling
		{
			name:       "timezone conversion - Chicago time inside",
			quietHours: "22:00-07:00",
			timezone:   "America/Chicago",
			// 04:00 UTC = 22:00 CST (previous day) — inside quiet hours
			time: time.Date(2026, 1, 16, 4, 0, 0, 0, time.UTC),
			want: true,
		},
		{
			name:       "invalid timezone falls back to UTC",
			quietHours: "22:00-07:00",
			timezone:   "Invalid/Zone",
			time:       time.Date(2026, 1, 15, 23, 0, 0, 0, time.UTC),
			want:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsInQuietHours(tt.quietHours, tt.timezone, tt.time)
			if got != tt.want {
				t.Errorf("IsInQuietHours(%q, %q, %v) = %v, want %v",
					tt.quietHours, tt.timezone, tt.time, got, tt.want)
			}
		})
	}
}

func TestParseHHMM(t *testing.T) {
	tests := []struct {
		input string
		h, m  int
		ok    bool
	}{
		{"07:30", 7, 30, true},
		{"22:00", 22, 0, true},
		{"00:00", 0, 0, true},
		{"23:59", 23, 59, true},
		{"24:00", 0, 0, false},
		{"12:60", 0, 0, false},
		{"abc", 0, 0, false},
		{"", 0, 0, false},
		{"12", 0, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			h, m, ok := parseHHMM(tt.input)
			if ok != tt.ok {
				t.Errorf("parseHHMM(%q) ok = %v, want %v", tt.input, ok, tt.ok)
			}
			if ok && (h != tt.h || m != tt.m) {
				t.Errorf("parseHHMM(%q) = (%d, %d), want (%d, %d)", tt.input, h, m, tt.h, tt.m)
			}
		})
	}
}
