package core_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/channels"
	"github.com/kkjorsvik/kyvik/internal/core"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// routerTestAdapter is a channel adapter for testing the router.
type routerTestAdapter struct {
	name     string
	incoming chan channels.IncomingMessage

	mu   sync.Mutex
	sent []sentMessage
}

type sentMessage struct {
	agentID string
	msg     types.Message
}

func newRouterTestAdapter(name string) *routerTestAdapter {
	return &routerTestAdapter{
		name:     name,
		incoming: make(chan channels.IncomingMessage, 16),
	}
}

func (a *routerTestAdapter) Name() string { return a.name }

func (a *routerTestAdapter) Send(_ context.Context, agentID string, msg types.Message) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sent = append(a.sent, sentMessage{agentID: agentID, msg: msg})
	return nil
}

func (a *routerTestAdapter) Receive(_ context.Context) (<-chan channels.IncomingMessage, error) {
	return a.incoming, nil
}

func (a *routerTestAdapter) ProvisionAgent(_ context.Context, cfg types.AgentConfig) error {
	return nil
}

func (a *routerTestAdapter) DeprovisionAgent(_ context.Context, _ string) error {
	return types.ErrNotProvisioned
}

func (a *routerTestAdapter) Close() error {
	close(a.incoming)
	return nil
}

func (a *routerTestAdapter) getSent() []sentMessage {
	a.mu.Lock()
	defer a.mu.Unlock()
	cp := make([]sentMessage, len(a.sent))
	copy(cp, a.sent)
	return cp
}

// TestRouterEndToEnd tests the full message flow:
// adapter incoming → router → agent inbox → handleMessage → outbox → outbox consumer → adapter.Send
func TestRouterEndToEnd(t *testing.T) {
	// Build the runtime with a test adapter
	s := newMockStore()
	al := &mockAuditLogger{}
	sp := newMockSpendingTracker(true)
	prov := newMockProvider("test-provider")

	st := core.New(s, &mockGate{}, nil, al, &mockToolsRegistry{}, sp)
	st.RegisterModel(prov)

	adapter := newRouterTestAdapter("test-channel")
	st.RegisterChannel(adapter)

	// Start the router
	ctx := context.Background()
	if err := st.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Start an agent with a channel mapping
	config := types.AgentConfig{
		ID:   "router-agent",
		Name: "Router Test Agent",
		ModelConfig: types.ModelConfig{
			Provider: "test-provider",
			Model:    "test-model",
		},
		Template:     "worker",
		SystemPrompt: "You are a test agent.",
		Channels: []types.ChannelMapping{
			{ChannelType: "test-channel", ChannelID: "ch-1"},
		},
	}
	if err := st.StartAgent(ctx, config); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	waitForRunning(t, &testHarness{kyvik: st, store: s, provider: prov, audit: al, spending: sp}, "router-agent")

	// Simulate an incoming message from the adapter
	adapter.incoming <- channels.IncomingMessage{
		ChannelType: "test-channel",
		ChannelID:   "ch-1",
		SenderID:    "user-1",
		Content:     "hello from channel",
		AgentID:     "router-agent",
	}

	// Wait for the outbox consumer to deliver the response back to the adapter
	deadline := time.After(3 * time.Second)
	for {
		sent := adapter.getSent()
		if len(sent) > 0 {
			if sent[0].agentID != "router-agent" {
				t.Errorf("expected agentID 'router-agent', got %q", sent[0].agentID)
			}
			if sent[0].msg.Content != "mock response" {
				t.Errorf("expected content 'mock response', got %q", sent[0].msg.Content)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for adapter.Send to be called")
		case <-time.After(10 * time.Millisecond):
		}
	}

	// Verify the model was called
	if prov.getCallCount() == 0 {
		t.Error("model provider was never called")
	}

	// Clean up
	if err := st.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

// TestRouterDropsUnmappedAgent tests that messages with unknown agent IDs
// are logged and dropped (not panicking).
func TestRouterDropsUnmappedAgent(t *testing.T) {
	s := newMockStore()
	al := &mockAuditLogger{}
	sp := newMockSpendingTracker(true)
	prov := newMockProvider("test-provider")

	st := core.New(s, &mockGate{}, nil, al, &mockToolsRegistry{}, sp)
	st.RegisterModel(prov)

	adapter := newRouterTestAdapter("test-channel")
	st.RegisterChannel(adapter)

	ctx := context.Background()
	if err := st.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Send a message for a non-existent agent — should not panic
	adapter.incoming <- channels.IncomingMessage{
		ChannelType: "test-channel",
		ChannelID:   "ch-unknown",
		SenderID:    "user-1",
		Content:     "hello?",
		AgentID:     "nonexistent-agent",
	}

	// Give the router time to process the message
	time.Sleep(50 * time.Millisecond)

	// Nothing should have been sent back
	if len(adapter.getSent()) != 0 {
		t.Error("expected no messages sent for unmapped agent")
	}

	if err := st.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}
