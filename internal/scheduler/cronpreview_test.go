package scheduler

import "testing"

func TestCronPreview(t *testing.T) {
	tests := []struct {
		expr string
		want string
	}{
		// @every shorthand
		{"@every 30m", "Every 30m"},
		{"@every 15m", "Every 15m"},
		{"@every 1h", "Every 1h"},
		{"@every 2h", "Every 2h"},

		// Cron shortcuts
		{"@daily", "Once a day (midnight)"},
		{"@hourly", "Every hour"},
		{"@weekly", "Once a week"},
		{"@monthly", "Once a month"},
		{"@yearly", "Once a year"},
		{"@annually", "Once a year"},

		// Daily at specific time
		{"30 7 * * *", "Daily at 7:30"},
		{"0 9 * * *", "Daily at 9:00"},
		{"5 14 * * *", "Daily at 14:05"},

		// Weekdays
		{"30 7 * * 1-5", "Weekdays at 7:30"},
		{"0 8 * * MON-FRI", "Weekdays at 8:00"},

		// Weekends
		{"0 10 * * 0,6", "Weekends at 10:00"},
		{"0 10 * * SAT,SUN", "Weekends at 10:00"},
		{"0 10 * * 6,0", "Weekends at 10:00"},

		// Every N hours
		{"0 */2 * * *", "Every 2 hours"},
		{"0 */4 * * *", "Every 4 hours"},

		// Every N minutes
		{"*/15 * * * *", "Every 15 minutes"},
		{"*/5 * * * *", "Every 5 minutes"},

		// Complex/unrecognized — returns raw expression
		{"0 9 1 * *", "0 9 1 * *"},                         // monthly on 1st
		{"30 7 * * 1,3,5", "30 7 * * 1,3,5"},               // specific days
		{"invalid expression here extra", "invalid expression here extra"}, // wrong field count is fine, returned as-is
	}

	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			got := CronPreview(tt.expr)
			if got != tt.want {
				t.Errorf("CronPreview(%q) = %q, want %q", tt.expr, got, tt.want)
			}
		})
	}
}

func TestPadMinute(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"0", "00"},
		{"5", "05"},
		{"30", "30"},
		{"59", "59"},
	}
	for _, tt := range tests {
		got := padMinute(tt.input)
		if got != tt.want {
			t.Errorf("padMinute(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
