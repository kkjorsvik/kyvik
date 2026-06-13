package scheduler

import (
	"fmt"
	"strings"
)

// CronPreview returns a human-readable summary of a cron expression.
// Used by dashboard templates to display schedule timing.
func CronPreview(expr string) string {
	expr = strings.TrimSpace(expr)

	// Handle @every shorthand.
	if strings.HasPrefix(expr, "@every ") {
		interval := strings.TrimPrefix(expr, "@every ")
		return "Every " + interval
	}

	// Handle other shortcuts.
	switch expr {
	case "@daily":
		return "Once a day (midnight)"
	case "@hourly":
		return "Every hour"
	case "@weekly":
		return "Once a week"
	case "@monthly":
		return "Once a month"
	case "@yearly", "@annually":
		return "Once a year"
	}

	// Parse 5-field cron: min hour dom month dow
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return expr
	}

	min, hour, dom, month, dow := fields[0], fields[1], fields[2], fields[3], fields[4]

	// "30 7 * * *" → "Daily at 7:30"
	if dom == "*" && month == "*" && dow == "*" && !strings.Contains(hour, "/") && !strings.Contains(min, "/") {
		if strings.Contains(hour, ",") || strings.Contains(hour, "-") {
			return expr // complex hour pattern
		}
		return fmt.Sprintf("Daily at %s:%s", hour, padMinute(min))
	}

	// "30 7 * * 1-5" → "Weekdays at 7:30"
	if dom == "*" && month == "*" && (dow == "1-5" || dow == "MON-FRI") {
		return fmt.Sprintf("Weekdays at %s:%s", hour, padMinute(min))
	}

	// "30 7 * * 0,6" → "Weekends at 7:30"
	if dom == "*" && month == "*" && (dow == "0,6" || dow == "SAT,SUN" || dow == "6,0") {
		return fmt.Sprintf("Weekends at %s:%s", hour, padMinute(min))
	}

	// "0 */2 * * *" → "Every 2 hours"
	if min == "0" && strings.HasPrefix(hour, "*/") && dom == "*" && month == "*" && dow == "*" {
		n := strings.TrimPrefix(hour, "*/")
		return fmt.Sprintf("Every %s hours", n)
	}

	// "*/15 * * * *" → "Every 15 minutes"
	if strings.HasPrefix(min, "*/") && hour == "*" && dom == "*" && month == "*" && dow == "*" {
		n := strings.TrimPrefix(min, "*/")
		return fmt.Sprintf("Every %s minutes", n)
	}

	return expr
}

// padMinute ensures minute is zero-padded to 2 digits.
func padMinute(m string) string {
	if len(m) == 1 {
		return "0" + m
	}
	return m
}
