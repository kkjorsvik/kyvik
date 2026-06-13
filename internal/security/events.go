// Package security implements prompt injection defense layers for Kyvik agents.
package security

import (
	"context"

	"github.com/kkjorsvik/kyvik/internal/notifications"
	"github.com/kkjorsvik/kyvik/internal/timeutil"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// SecurityStore is the subset of store.Store needed by the security subsystem.
type SecurityStore interface {
	InsertSecurityEvent(ctx context.Context, event types.SecurityEvent) error
	QuerySecurityEvents(ctx context.Context, agentID string, limit int) ([]types.SecurityEvent, error)
}

// EventRecorder persists security events and sends critical notifications.
type EventRecorder struct {
	store    SecurityStore
	notifier notifications.Notifier
}

// NewEventRecorder creates an EventRecorder.
// notifier may be nil if notifications are not configured.
func NewEventRecorder(store SecurityStore, notifier notifications.Notifier) *EventRecorder {
	return &EventRecorder{store: store, notifier: notifier}
}

// Record persists a security event and sends a notification for critical severity.
func (r *EventRecorder) Record(ctx context.Context, event types.SecurityEvent) {
	if event.CreatedAt.IsZero() {
		event.CreatedAt = timeutil.NowUTC()
	}

	_ = r.store.InsertSecurityEvent(ctx, event)

	if event.Severity == "critical" && r.notifier != nil {
		_ = r.notifier.Send(ctx, notifications.Event{
			Type:      "security_alert",
			Severity:  "critical",
			Agent:     event.AgentID,
			Title:     "Security: " + event.EventType,
			Detail:    event.Details,
			Timestamp: event.CreatedAt,
		})
	}
}
