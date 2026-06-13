// Package notifications provides operator alerting for important Kyvik events.
// Events include agent errors, spending threshold breaches, and key failures.
package notifications

import (
	"context"
	"log/slog"
	"time"
)

// Notifier sends operator notifications about important system events.
type Notifier interface {
	Send(ctx context.Context, event Event) error
	Start() error
	Stop()
}

// Event represents a notification-worthy occurrence in the system.
type Event struct {
	Type      string         // "circuit_breaker", "agent_error", "spending_alert", "key_failure", etc.
	Severity  string         // "info", "warning", "critical"
	Agent     string         // agent ID, empty for system events
	Title     string
	Detail    string
	Timestamp time.Time
	Metadata  map[string]any // optional extra data for webhook payloads
}

// LogNotifier is a fallback Notifier that writes events to slog.
// Used when Slack is not configured.
type LogNotifier struct{}

// NewLogNotifier creates a LogNotifier.
func NewLogNotifier() *LogNotifier {
	return &LogNotifier{}
}

func (l *LogNotifier) Send(_ context.Context, event Event) error {
	slog.Warn("notification",
		"type", event.Type,
		"severity", event.Severity,
		"agent", event.Agent,
		"title", event.Title,
		"detail", event.Detail,
	)
	return nil
}

func (l *LogNotifier) Start() error { return nil }
func (l *LogNotifier) Stop()        {}
