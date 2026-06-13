// Package webui implements the channels.Adapter interface for the built-in
// web dashboard chat. Outgoing messages are delivered to browser clients
// via SSE (Server-Sent Events). Incoming messages bypass the adapter and
// go directly through kyvik.SendMessage().
package webui

import (
	"context"
	"sync"
	"time"

	"github.com/kkjorsvik/kyvik/internal/channels"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// Compile-time interface checks.
var (
	_ channels.Adapter          = (*Adapter)(nil)
	_ channels.StreamingAdapter = (*Adapter)(nil)
)

// Adapter implements channels.Adapter for the web dashboard chat.
// It maintains SSE subscriber channels per agent, and fans out outgoing
// messages (and streaming events) to all active subscribers.
type Adapter struct {
	mu          sync.RWMutex
	subscribers map[string][]chan channels.StreamEvent // agentID -> SSE channels
	incoming    chan channels.IncomingMessage          // idle; never receives
	closed      bool
}

const subscriberBuffer = 128

// New creates a new WebUI adapter.
func New() *Adapter {
	return &Adapter{
		subscribers: make(map[string][]chan channels.StreamEvent),
		incoming:    make(chan channels.IncomingMessage),
	}
}

// Name returns the channel identifier.
func (a *Adapter) Name() string { return "webui" }

// ProvisionAgent is a no-op — every agent is web-chattable by default.
func (a *Adapter) ProvisionAgent(_ context.Context, _ types.AgentConfig) error {
	return nil
}

// DeprovisionAgent is a no-op. Returns ErrNotProvisioned to match the
// convention used by the outbox consumer (silently skipped).
func (a *Adapter) DeprovisionAgent(_ context.Context, _ string) error {
	return types.ErrNotProvisioned
}

// Receive returns a channel that never produces messages. The message router
// spawns a goroutine for this, but it just blocks on ctx.Done since web chat
// incoming messages go directly through kyvik.SendMessage().
func (a *Adapter) Receive(_ context.Context) (<-chan channels.IncomingMessage, error) {
	return a.incoming, nil
}

// Send delivers a complete message to all SSE subscribers as a "message" event.
// Returns ErrNotProvisioned if there are no active subscribers (silently
// skipped by the outbox consumer). Only delivers messages that originated from
// the webui channel — inter-agent and other channel responses are skipped.
func (a *Adapter) Send(_ context.Context, agentID string, msg types.Message) error {
	if msg.Channel != "" && msg.Channel != "webui" {
		return types.ErrNotProvisioned
	}

	event := channels.StreamEvent{
		Type:           "message",
		Content:        msg.Content,
		ConversationID: msg.ConversationID,
		Timestamp:      msg.Timestamp,
	}
	return a.SendStreamEvent(agentID, event)
}

// SendStreamEvent fans out a streaming event to all SSE subscribers for the
// given agent. Implements channels.StreamingAdapter.
func (a *Adapter) SendStreamEvent(agentID string, event channels.StreamEvent) error {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.closed {
		return types.ErrAdapterClosed
	}

	subs, ok := a.subscribers[agentID]
	if !ok || len(subs) == 0 {
		return types.ErrNotProvisioned
	}

	for _, ch := range subs {
		if isCriticalEvent(event.Type) {
			select {
			case ch <- event:
			case <-time.After(1500 * time.Millisecond):
				// Avoid unbounded blocking on stuck subscribers.
			}
			continue
		}

		// For high-frequency chunk events, do not block.
		select {
		case ch <- event:
		default:
			// Slow subscriber — drop chunk events rather than block.
		}
	}

	return nil
}

// Close shuts down the adapter by closing all subscriber channels.
func (a *Adapter) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.closed = true
	for agentID, subs := range a.subscribers {
		for _, ch := range subs {
			close(ch)
		}
		delete(a.subscribers, agentID)
	}
	return nil
}

// Subscribe creates a new SSE subscriber channel for the given agent.
// The returned channel receives StreamEvents (chunks, done, errors, messages).
// The caller must call Unsubscribe when done (typically via defer).
func (a *Adapter) Subscribe(agentID string) chan channels.StreamEvent {
	a.mu.Lock()
	defer a.mu.Unlock()

	ch := make(chan channels.StreamEvent, subscriberBuffer)
	a.subscribers[agentID] = append(a.subscribers[agentID], ch)
	return ch
}

// Unsubscribe removes a subscriber channel for the given agent.
func (a *Adapter) Unsubscribe(agentID string, ch chan channels.StreamEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()

	subs := a.subscribers[agentID]
	for i, sub := range subs {
		if sub == ch {
			a.subscribers[agentID] = append(subs[:i], subs[i+1:]...)
			break
		}
	}
}

func isCriticalEvent(eventType string) bool {
	switch eventType {
	case "done", "error", "message":
		return true
	default:
		return false
	}
}
