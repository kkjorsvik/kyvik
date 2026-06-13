package busadapter

import (
	"context"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/audit"
	"github.com/kkjorsvik/kyvik/internal/teams"
	"github.com/kkjorsvik/kyvik/internal/testutil"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// mockAuditLogger is a no-op audit logger for tests.
type mockAuditLogger struct{}

func (mockAuditLogger) Log(_ context.Context, _ types.AuditEntry) error                     { return nil }
func (mockAuditLogger) Query(_ context.Context, _ audit.Filter) ([]types.AuditEntry, error)  { return nil, nil }
func (mockAuditLogger) Stream(_ context.Context, _ string) (<-chan types.AuditEntry, error)   { return nil, nil }
func (mockAuditLogger) Subscribe(_ context.Context, _ audit.SubscriptionFilter) (<-chan types.AuditEntry, error) {
	ch := make(chan types.AuditEntry)
	close(ch)
	return ch, nil
}
func (mockAuditLogger) Close() error { return nil }

func newTestBus(t *testing.T) *teams.Bus {
	t.Helper()
	tdb := testutil.RequirePostgres(t)
	return teams.NewBus(tdb.DB, mockAuditLogger{})
}

func TestName(t *testing.T) {
	bus := newTestBus(t)
	a := New(bus)
	defer a.Close()

	if a.Name() != "internal" {
		t.Errorf("Name() = %q, want %q", a.Name(), "internal")
	}
}

func TestProvisionAndReceive(t *testing.T) {
	bus := newTestBus(t)
	a := New(bus)
	defer a.Close()
	ctx := context.Background()

	// Provision agent-b so it receives bus messages.
	err := a.ProvisionAgent(ctx, types.AgentConfig{ID: "agent-b"})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}

	// Get the incoming channel.
	incoming, err := a.Receive(ctx)
	if err != nil {
		t.Fatalf("receive: %v", err)
	}

	// Send a message via the bus from agent-a to agent-b.
	err = bus.Send(ctx, types.InternalMessage{
		From:    "agent-a",
		To:      "agent-b",
		Content: "hello from a",
		Type:    types.MessageTypeMessage,
	})
	if err != nil {
		t.Fatalf("bus send: %v", err)
	}

	select {
	case msg := <-incoming:
		if msg.ChannelType != "internal" {
			t.Errorf("ChannelType = %q, want internal", msg.ChannelType)
		}
		if msg.ChannelID != "agent-a" {
			t.Errorf("ChannelID = %q, want agent-a", msg.ChannelID)
		}
		if msg.SenderID != "agent-a" {
			t.Errorf("SenderID = %q, want agent-a", msg.SenderID)
		}
		if msg.AgentID != "agent-b" {
			t.Errorf("AgentID = %q, want agent-b", msg.AgentID)
		}
		if msg.Content != "hello from a" {
			t.Errorf("Content = %q, want 'hello from a'", msg.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for incoming message")
	}
}

func TestSendRoutesResult(t *testing.T) {
	bus := newTestBus(t)
	a := New(bus)
	defer a.Close()
	ctx := context.Background()

	// Provision both agents.
	err := a.ProvisionAgent(ctx, types.AgentConfig{ID: "agent-a"})
	if err != nil {
		t.Fatalf("provision agent-a: %v", err)
	}
	err = a.ProvisionAgent(ctx, types.AgentConfig{ID: "agent-b"})
	if err != nil {
		t.Fatalf("provision agent-b: %v", err)
	}

	// Also subscribe to agent-a on the bus directly to verify the result arrives.
	busChA := bus.Subscribe(ctx, "agent-a")

	// Simulate agent-b sending a response back to agent-a.
	err = a.Send(ctx, "agent-b", types.Message{
		Channel: "internal",
		Sender:  "agent-a",
		Content: "result from b",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case msg := <-busChA:
		if msg.From != "agent-b" {
			t.Errorf("From = %q, want agent-b", msg.From)
		}
		if msg.To != "agent-a" {
			t.Errorf("To = %q, want agent-a", msg.To)
		}
		if msg.Content != "result from b" {
			t.Errorf("Content = %q, want 'result from b'", msg.Content)
		}
		if msg.Type != types.MessageTypeResult {
			t.Errorf("Type = %q, want result", msg.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for bus result message")
	}
}

func TestSendSkipsNonInternal(t *testing.T) {
	bus := newTestBus(t)
	a := New(bus)
	defer a.Close()
	ctx := context.Background()

	err := a.Send(ctx, "agent-b", types.Message{
		Channel: "webui",
		Sender:  "agent-a",
		Content: "should skip",
	})
	if err != types.ErrNotProvisioned {
		t.Errorf("Send with webui channel = %v, want ErrNotProvisioned", err)
	}
}

func TestSendSkipsEmptySender(t *testing.T) {
	bus := newTestBus(t)
	a := New(bus)
	defer a.Close()
	ctx := context.Background()

	err := a.Send(ctx, "agent-b", types.Message{
		Channel: "internal",
		Sender:  "",
		Content: "should skip",
	})
	if err != types.ErrNotProvisioned {
		t.Errorf("Send with empty sender = %v, want ErrNotProvisioned", err)
	}
}

func TestDeprovision(t *testing.T) {
	bus := newTestBus(t)
	a := New(bus)
	defer a.Close()
	ctx := context.Background()

	err := a.ProvisionAgent(ctx, types.AgentConfig{ID: "agent-x"})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}

	incoming, _ := a.Receive(ctx)

	// Deprovision the agent.
	err = a.DeprovisionAgent(ctx, "agent-x")
	if err != nil {
		t.Fatalf("deprovision: %v", err)
	}

	// Send a message — it should NOT arrive on incoming.
	_ = bus.Send(ctx, types.InternalMessage{
		From:    "sender",
		To:      "agent-x",
		Content: "after deprovision",
	})

	select {
	case msg := <-incoming:
		t.Errorf("received message after deprovision: %+v", msg)
	case <-time.After(200 * time.Millisecond):
		// Expected: no message.
	}
}

func TestDeprovisionNotProvisioned(t *testing.T) {
	bus := newTestBus(t)
	a := New(bus)
	defer a.Close()
	ctx := context.Background()

	err := a.DeprovisionAgent(ctx, "nonexistent")
	if err != types.ErrNotProvisioned {
		t.Errorf("DeprovisionAgent = %v, want ErrNotProvisioned", err)
	}
}

func TestResultPassthrough(t *testing.T) {
	bus := newTestBus(t)
	a := New(bus)
	defer a.Close()
	ctx := context.Background()

	err := a.ProvisionAgent(ctx, types.AgentConfig{ID: "agent-b"})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}

	incoming, _ := a.Receive(ctx)

	// Send a result-type message — should pass through with type set.
	err = bus.Send(ctx, types.InternalMessage{
		From:    "agent-a",
		To:      "agent-b",
		Content: "result content",
		Type:    types.MessageTypeResult,
	})
	if err != nil {
		t.Fatalf("bus send: %v", err)
	}

	// Send a regular message.
	err = bus.Send(ctx, types.InternalMessage{
		From:    "agent-a",
		To:      "agent-b",
		Content: "regular message",
		Type:    types.MessageTypeMessage,
	})
	if err != nil {
		t.Fatalf("bus send: %v", err)
	}

	// Both messages should arrive; result first.
	select {
	case msg := <-incoming:
		if msg.Content != "result content" {
			t.Errorf("first message Content = %q, want 'result content'", msg.Content)
		}
		if msg.MessageType != "result" {
			t.Errorf("first message MessageType = %q, want 'result'", msg.MessageType)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for result message")
	}

	select {
	case msg := <-incoming:
		if msg.Content != "regular message" {
			t.Errorf("second message Content = %q, want 'regular message'", msg.Content)
		}
		if msg.MessageType != "message" {
			t.Errorf("second message MessageType = %q, want 'message'", msg.MessageType)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for regular message")
	}
}

func TestClose(t *testing.T) {
	bus := newTestBus(t)
	a := New(bus)
	ctx := context.Background()

	err := a.ProvisionAgent(ctx, types.AgentConfig{ID: "agent-a"})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}

	incoming, _ := a.Receive(ctx)

	err = a.Close()
	if err != nil {
		t.Fatalf("close: %v", err)
	}

	// Incoming channel should be closed.
	select {
	case _, ok := <-incoming:
		if ok {
			t.Error("expected incoming channel to be closed")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for incoming channel to close")
	}

	// Subscriptions should be empty.
	a.mu.RLock()
	subsLen := len(a.subs)
	a.mu.RUnlock()
	if subsLen != 0 {
		t.Errorf("subs length = %d, want 0 after close", subsLen)
	}
}

func TestPermissionCheckAllowed(t *testing.T) {
	bus := newTestBus(t)
	a := New(bus)
	defer a.Close()
	ctx := context.Background()

	configs := map[string]*types.AgentConfig{
		"agent-a": {ID: "agent-a", CanMessage: []string{"agent-b"}},
		"agent-b": {ID: "agent-b"},
	}
	a.SetConfigLookup(func(_ context.Context, id string) (*types.AgentConfig, error) {
		if c, ok := configs[id]; ok {
			return c, nil
		}
		return nil, types.ErrNotFound
	})

	err := a.ProvisionAgent(ctx, *configs["agent-b"])
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	incoming, _ := a.Receive(ctx)

	err = bus.Send(ctx, types.InternalMessage{
		From:    "agent-a",
		To:      "agent-b",
		Content: "permitted msg",
		Type:    types.MessageTypeMessage,
	})
	if err != nil {
		t.Fatalf("bus send: %v", err)
	}

	select {
	case msg := <-incoming:
		if msg.Content != "permitted msg" {
			t.Errorf("Content = %q, want 'permitted msg'", msg.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for permitted message")
	}
}

func TestPermissionCheckDenied(t *testing.T) {
	bus := newTestBus(t)
	a := New(bus)
	defer a.Close()
	ctx := context.Background()

	configs := map[string]*types.AgentConfig{
		"agent-a": {ID: "agent-a"}, // no can_message entries
		"agent-b": {ID: "agent-b"},
	}
	a.SetConfigLookup(func(_ context.Context, id string) (*types.AgentConfig, error) {
		if c, ok := configs[id]; ok {
			return c, nil
		}
		return nil, types.ErrNotFound
	})

	err := a.ProvisionAgent(ctx, *configs["agent-b"])
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	incoming, _ := a.Receive(ctx)

	err = bus.Send(ctx, types.InternalMessage{
		From:    "agent-a",
		To:      "agent-b",
		Content: "denied msg",
		Type:    types.MessageTypeMessage,
	})
	if err != nil {
		t.Fatalf("bus send: %v", err)
	}

	select {
	case msg := <-incoming:
		t.Errorf("received message that should have been denied: %+v", msg)
	case <-time.After(500 * time.Millisecond):
		// Expected: message dropped.
	}
}

func TestPermissionCheckSameTeamAllowed(t *testing.T) {
	bus := newTestBus(t)
	a := New(bus)
	defer a.Close()
	ctx := context.Background()

	configs := map[string]*types.AgentConfig{
		"agent-a": {ID: "agent-a", TeamID: "ops"},
		"agent-b": {ID: "agent-b", TeamID: "ops"},
	}
	a.SetConfigLookup(func(_ context.Context, id string) (*types.AgentConfig, error) {
		if c, ok := configs[id]; ok {
			return c, nil
		}
		return nil, types.ErrNotFound
	})

	err := a.ProvisionAgent(ctx, *configs["agent-b"])
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	incoming, _ := a.Receive(ctx)

	err = bus.Send(ctx, types.InternalMessage{
		From:    "agent-a",
		To:      "agent-b",
		Content: "team msg",
		Type:    types.MessageTypeMessage,
	})
	if err != nil {
		t.Fatalf("bus send: %v", err)
	}

	select {
	case msg := <-incoming:
		if msg.Content != "team msg" {
			t.Errorf("Content = %q, want 'team msg'", msg.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for team message")
	}
}

func TestDoubleProvision(t *testing.T) {
	bus := newTestBus(t)
	a := New(bus)
	defer a.Close()
	ctx := context.Background()

	cfg := types.AgentConfig{ID: "agent-x"}

	err := a.ProvisionAgent(ctx, cfg)
	if err != nil {
		t.Fatalf("first provision: %v", err)
	}

	// Second provision should be idempotent.
	err = a.ProvisionAgent(ctx, cfg)
	if err != nil {
		t.Fatalf("second provision: %v", err)
	}

	// Should still receive exactly one message (not duplicated).
	incoming, _ := a.Receive(ctx)

	err = bus.Send(ctx, types.InternalMessage{
		From:    "sender",
		To:      "agent-x",
		Content: "single message",
	})
	if err != nil {
		t.Fatalf("bus send: %v", err)
	}

	select {
	case msg := <-incoming:
		if msg.Content != "single message" {
			t.Errorf("Content = %q, want 'single message'", msg.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for message")
	}

	// Verify no duplicate.
	select {
	case msg := <-incoming:
		t.Errorf("received duplicate message: %+v", msg)
	case <-time.After(200 * time.Millisecond):
		// Expected: no duplicate.
	}
}
