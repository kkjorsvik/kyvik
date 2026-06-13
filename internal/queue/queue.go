// Package queue defines a persistent message queue backed by PostgreSQL.
// Messages are durably stored in the database and delivered to agents
// via in-memory Go channels for fast reads.
package queue

import (
	"context"
	"time"
)

// Message status constants.
const (
	StatusPending    = "pending"
	StatusProcessing = "processing"
	StatusCompleted  = "completed"
	StatusFailed     = "failed"
)

// FullBehavior determines what happens when an agent's queue is at capacity.
type FullBehavior string

const (
	// BehaviorAcknowledge persists the message but does not push it to the
	// in-memory channel. It will be delivered when capacity frees up (e.g.
	// via Replay at next startup).
	BehaviorAcknowledge FullBehavior = "acknowledge"
	// BehaviorReject persists the message but logs a warning.
	BehaviorReject FullBehavior = "reject"
	// BehaviorDrop persists the message but logs a warning.
	BehaviorDrop FullBehavior = "drop"
)

// QueueMessage represents a message stored in the persistent queue.
type QueueMessage struct {
	ID             int64
	AgentID        string
	Channel        string
	ConversationID string
	Sender         string
	Content        string
	Attachments    string
	MessageType    string // "message", "task", "status", etc.
	Priority       int    // 0=normal, 1=priority_user, 2=operator
	Status         string
	Attempts       int
	MaxAttempts    int
	TargetNodeID   string
	CreatedAt      time.Time
	StartedAt      *time.Time
	CompletedAt    *time.Time
}

// Config holds queue configuration values.
type Config struct {
	Depth               int
	FullBehavior        FullBehavior
	PriorityUsers       []string
	StaleTimeoutSeconds int
	RetentionHours      int
}

// DefaultConfig returns sensible defaults for the queue.
func DefaultConfig() Config {
	return Config{
		Depth:               50,
		FullBehavior:        BehaviorAcknowledge,
		PriorityUsers:       nil,
		StaleTimeoutSeconds: 300,
		RetentionHours:      24,
	}
}

// Queue defines the persistent message queue interface.
type Queue interface {
	// Enqueue inserts a message into the queue and, if the agent's channel
	// exists and is not full, pushes it for immediate delivery.
	Enqueue(ctx context.Context, msg QueueMessage) (int64, error)

	// Dequeue returns (or creates) the in-memory delivery channel for an agent.
	Dequeue(ctx context.Context, agentID string) <-chan QueueMessage

	// MarkProcessing sets a message's status to processing with a started_at timestamp.
	MarkProcessing(ctx context.Context, id int64) error

	// Complete marks a message as successfully processed.
	Complete(ctx context.Context, id int64) error

	// Fail increments the attempt counter. If under max_attempts, the message
	// is reset to pending for retry; otherwise it is marked failed.
	Fail(ctx context.Context, id int64) error

	// ResetAgentProcessing resets all processing messages for an agent back to
	// pending with incremented attempts. Called during kill to release in-flight work.
	ResetAgentProcessing(ctx context.Context, agentID string) (int64, error)

	// Replay recovers all pending and stale-processing messages at startup,
	// pushing them to the appropriate agent channels.
	Replay(ctx context.Context) error

	// ReplayAgent recovers pending messages for a single agent. Called when
	// an agent starts to deliver any messages queued while it was stopped.
	ReplayAgent(ctx context.Context, agentID string) error

	// Cleanup deletes completed messages older than retentionHours.
	Cleanup(ctx context.Context, retentionHours int) (int64, error)

	// Depth returns the number of pending+processing messages for an agent.
	Depth(ctx context.Context, agentID string) (int, error)

	// PriorityUsers returns the configured list of priority user IDs.
	PriorityUsers() []string

	// ListMessages returns messages for an agent, optionally filtered by status.
	ListMessages(ctx context.Context, agentID string, statusFilter string, limit int) ([]QueueMessage, error)

	// Stats returns per-status message counts for an agent.
	Stats(ctx context.Context, agentID string) (map[string]int, error)

	// RetryMessage resets a failed message to pending for re-processing.
	RetryMessage(ctx context.Context, id int64) error

	// DeleteMessage removes a message from the queue.
	DeleteMessage(ctx context.Context, id int64) error

	// DeleteMessages removes all messages for an agent, optionally filtered by status.
	// If statusFilter is empty, all messages are deleted.
	DeleteMessages(ctx context.Context, agentID, statusFilter string) (int64, error)

	// Stop shuts down background goroutines and closes in-memory channels.
	Stop()
}
