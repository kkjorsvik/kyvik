// Package channels defines the communication adapter interface.
// Each channel (Slack, Discord, Signal, web UI, etc.) implements
// this interface. Agent identity is auto-provisioned by default
// with manual override support.
package channels

import (
	"context"
	"time"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

// IncomingMessage represents a message received from a channel.
type IncomingMessage struct {
	ChannelType string             `json:"channel_type"`
	ChannelID   string             `json:"channel_id"`
	SenderID    string             `json:"sender_id"`
	Content     string             `json:"content"`
	AgentID     string             `json:"agent_id"`      // Target agent, resolved by router
	MessageType string             `json:"message_type"`  // Bus message type (task, message, result, status); empty for external channels
	Attachments []types.Attachment `json:"attachments,omitempty"`
}

// StreamEvent represents a streaming event sent from the backend to a
// real-time subscriber (e.g. the web UI via SSE). Event types:
//
//   - "chunk":   partial content delta (incremental text from the model)
//   - "done":    response complete — carries final token/cost metadata
//   - "error":   an error occurred during streaming
//   - "message": a full, non-streamed message (fallback / non-streaming paths)
type StreamEvent struct {
	Type           string    `json:"type"`
	Content        string    `json:"content,omitempty"`
	ConversationID string    `json:"conversation_id,omitempty"`
	Timestamp      time.Time `json:"timestamp,omitempty"`
	TokensIn       int64     `json:"tokens_in,omitempty"`
	TokensOut      int64     `json:"tokens_out,omitempty"`
	Cost           float64   `json:"cost,omitempty"`
	Error          string    `json:"error,omitempty"`
}

// StreamingAdapter is an optional interface that channel adapters may implement
// to support real-time streaming of model responses (e.g. token-by-token SSE).
type StreamingAdapter interface {
	SendStreamEvent(agentID string, event StreamEvent) error
}

// Adapter defines the contract for communication channel integrations.
type Adapter interface {
	// Send delivers a message from an agent to the channel.
	Send(ctx context.Context, agentID string, msg types.Message) error

	// Receive returns a channel of incoming messages.
	Receive(ctx context.Context) (<-chan IncomingMessage, error)

	// ProvisionAgent sets up agent identity in the channel
	// (e.g., creates a Slack bot user, assigns a channel).
	ProvisionAgent(ctx context.Context, config types.AgentConfig) error

	// DeprovisionAgent removes agent identity from the channel.
	DeprovisionAgent(ctx context.Context, agentID string) error

	// Name returns the channel identifier (e.g., "slack", "webui").
	Name() string

	// Close shuts down the adapter cleanly.
	Close() error
}

// ClusterAwareAdapter is an optional interface that adapters can implement
// to declare their clustering behavior. Adapters that don't implement this
// are treated as per-node (run on every node).
type ClusterAwareAdapter interface {
	Adapter
	// Singleton returns true if this adapter should only run on the leader node.
	// Examples: Slack (Socket Mode), Discord (Gateway) — single persistent connection.
	Singleton() bool
}

// IsSingleton checks if an adapter should run only on the leader node.
func IsSingleton(a Adapter) bool {
	if ca, ok := a.(ClusterAwareAdapter); ok {
		return ca.Singleton()
	}
	return false
}
