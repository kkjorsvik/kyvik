package notifications

import (
	"context"
	"log/slog"
)

// MultiNotifier fans out events to multiple Notifier backends.
// Errors from individual backends are logged but never returned to callers.
type MultiNotifier struct {
	notifiers []Notifier
}

// NewMultiNotifier wraps one or more notifiers into a fan-out dispatcher.
func NewMultiNotifier(notifiers ...Notifier) *MultiNotifier {
	return &MultiNotifier{notifiers: notifiers}
}

// Send dispatches the event to every backend. Errors are logged, never propagated.
func (m *MultiNotifier) Send(ctx context.Context, event Event) error {
	for _, n := range m.notifiers {
		if err := n.Send(ctx, event); err != nil {
			slog.Warn("multi-notifier: backend send failed",
				"type", event.Type,
				"error", err,
			)
		}
	}
	return nil
}

// Start starts all backends in order.
func (m *MultiNotifier) Start() error {
	for _, n := range m.notifiers {
		if err := n.Start(); err != nil {
			return err
		}
	}
	return nil
}

// Stop stops all backends in reverse order so the dispatcher shuts down first.
func (m *MultiNotifier) Stop() {
	for i := len(m.notifiers) - 1; i >= 0; i-- {
		m.notifiers[i].Stop()
	}
}
