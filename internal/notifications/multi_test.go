package notifications_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/kkjorsvik/kyvik/internal/notifications"
)

type mockNotifier struct {
	mu     sync.Mutex
	events []notifications.Event
	err    error
}

func (m *mockNotifier) Send(_ context.Context, event notifications.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, event)
	return m.err
}
func (m *mockNotifier) Start() error { return nil }
func (m *mockNotifier) Stop()        {}

func TestMultiNotifier_FanOut(t *testing.T) {
	n1 := &mockNotifier{}
	n2 := &mockNotifier{}
	multi := notifications.NewMultiNotifier(n1, n2)

	event := notifications.Event{
		Type:     "test",
		Severity: "info",
		Title:    "hello",
	}

	if err := multi.Send(context.Background(), event); err != nil {
		t.Fatalf("Send returned error: %v", err)
	}

	if len(n1.events) != 1 {
		t.Errorf("n1 got %d events, want 1", len(n1.events))
	}
	if len(n2.events) != 1 {
		t.Errorf("n2 got %d events, want 1", len(n2.events))
	}
	if n1.events[0].Title != "hello" {
		t.Errorf("n1 title = %q, want %q", n1.events[0].Title, "hello")
	}
}

func TestMultiNotifier_ErrorDoesNotBlock(t *testing.T) {
	n1 := &mockNotifier{err: errors.New("boom")}
	n2 := &mockNotifier{}
	multi := notifications.NewMultiNotifier(n1, n2)

	event := notifications.Event{Type: "test", Severity: "info", Title: "still works"}

	// Send should not return error even though n1 fails.
	if err := multi.Send(context.Background(), event); err != nil {
		t.Fatalf("Send returned error: %v", err)
	}

	// n1 was called (despite returning error).
	if len(n1.events) != 1 {
		t.Errorf("n1 got %d events, want 1", len(n1.events))
	}
	// n2 still received the event.
	if len(n2.events) != 1 {
		t.Errorf("n2 got %d events, want 1", len(n2.events))
	}
}

func TestMultiNotifier_StartStop(t *testing.T) {
	n1 := &mockNotifier{}
	n2 := &mockNotifier{}
	multi := notifications.NewMultiNotifier(n1, n2)

	if err := multi.Start(); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	multi.Stop()
}
