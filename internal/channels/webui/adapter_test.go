package webui

import (
	"context"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/channels"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

func TestName(t *testing.T) {
	a := New()
	if a.Name() != "webui" {
		t.Errorf("expected name 'webui', got %q", a.Name())
	}
}

func TestSubscribeAndSend(t *testing.T) {
	a := New()
	ch := a.Subscribe("agent-1")

	msg := types.Message{AgentID: "agent-1", Role: "assistant", Content: "hello"}
	if err := a.Send(context.Background(), "agent-1", msg); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case got := <-ch:
		if got.Type != "message" {
			t.Errorf("expected type 'message', got %q", got.Type)
		}
		if got.Content != "hello" {
			t.Errorf("expected content 'hello', got %q", got.Content)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for message")
	}
}

func TestSendNoSubscribers(t *testing.T) {
	a := New()
	msg := types.Message{Content: "hi"}
	err := a.Send(context.Background(), "agent-1", msg)
	if err != types.ErrNotProvisioned {
		t.Errorf("expected ErrNotProvisioned, got %v", err)
	}
}

func TestUnsubscribe(t *testing.T) {
	a := New()
	ch := a.Subscribe("agent-1")
	a.Unsubscribe("agent-1", ch)

	err := a.Send(context.Background(), "agent-1", types.Message{Content: "hi"})
	if err != types.ErrNotProvisioned {
		t.Errorf("expected ErrNotProvisioned after unsubscribe, got %v", err)
	}
}

func TestMultipleSubscribers(t *testing.T) {
	a := New()
	ch1 := a.Subscribe("agent-1")
	ch2 := a.Subscribe("agent-1")

	msg := types.Message{AgentID: "agent-1", Role: "assistant", Content: "broadcast"}
	if err := a.Send(context.Background(), "agent-1", msg); err != nil {
		t.Fatalf("Send: %v", err)
	}

	for i, ch := range []chan channels.StreamEvent{ch1, ch2} {
		select {
		case got := <-ch:
			if got.Content != "broadcast" {
				t.Errorf("subscriber %d: expected 'broadcast', got %q", i, got.Content)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d: timed out waiting for message", i)
		}
	}
}

func TestProvisionDeprovision(t *testing.T) {
	a := New()

	// ProvisionAgent is a no-op, should return nil
	if err := a.ProvisionAgent(context.Background(), types.AgentConfig{ID: "x"}); err != nil {
		t.Errorf("ProvisionAgent: %v", err)
	}

	// DeprovisionAgent returns ErrNotProvisioned (convention)
	err := a.DeprovisionAgent(context.Background(), "x")
	if err != types.ErrNotProvisioned {
		t.Errorf("expected ErrNotProvisioned, got %v", err)
	}
}

func TestReceive(t *testing.T) {
	a := New()
	ch, err := a.Receive(context.Background())
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if ch == nil {
		t.Fatal("expected non-nil channel")
	}
}

func TestClose(t *testing.T) {
	a := New()
	ch := a.Subscribe("agent-1")

	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Channel should be closed
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("expected channel to be closed")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out; channel not closed")
	}

	// Send after close should return ErrAdapterClosed
	err := a.Send(context.Background(), "agent-1", types.Message{Content: "hi"})
	if err != types.ErrAdapterClosed {
		t.Errorf("expected ErrAdapterClosed after close, got %v", err)
	}
}

func TestSendStreamEvent(t *testing.T) {
	a := New()
	ch := a.Subscribe("agent-1")

	event := channels.StreamEvent{
		Type:           "chunk",
		Content:        "Hello ",
		ConversationID: "conv-1",
	}
	if err := a.SendStreamEvent("agent-1", event); err != nil {
		t.Fatalf("SendStreamEvent: %v", err)
	}

	select {
	case got := <-ch:
		if got.Type != "chunk" {
			t.Errorf("expected type 'chunk', got %q", got.Type)
		}
		if got.Content != "Hello " {
			t.Errorf("expected content 'Hello ', got %q", got.Content)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for stream event")
	}
}

func TestStreamingAdapterInterface(t *testing.T) {
	a := New()
	// Verify *Adapter satisfies channels.StreamingAdapter at runtime
	var _ channels.StreamingAdapter = a
}

func TestCriticalEventDeliveredWhenBufferIsFull(t *testing.T) {
	a := New()
	ch := a.Subscribe("agent-1")

	for i := 0; i < subscriberBuffer; i++ {
		if err := a.SendStreamEvent("agent-1", channels.StreamEvent{
			Type:    "chunk",
			Content: "x",
		}); err != nil {
			t.Fatalf("fill chunk %d: %v", i, err)
		}
	}

	doneSent := make(chan struct{})
	go func() {
		_ = a.SendStreamEvent("agent-1", channels.StreamEvent{Type: "done"})
		close(doneSent)
	}()

	select {
	case <-doneSent:
		t.Fatal("expected done send to wait for buffer space")
	case <-time.After(50 * time.Millisecond):
		// Expected: still waiting
	}

	// Free one slot so done can be queued.
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("timed out draining initial chunk")
	}

	select {
	case <-doneSent:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out sending done event")
	}

	foundDone := false
	for i := 0; i < subscriberBuffer; i++ {
		select {
		case ev := <-ch:
			if ev.Type == "done" {
				foundDone = true
				break
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for buffered events")
		}
	}

	if !foundDone {
		t.Fatal("expected done event in channel")
	}
}
