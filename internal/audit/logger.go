// Package audit provides the audit logging system.
// Every action in Kyvik is logged: tool calls, permission checks,
// model requests, agent lifecycle events, spending, and configuration changes.
package audit

import (
	"context"
	"time"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

// Logger defines the audit logging contract.
type Logger interface {
	// Log records an audit entry.
	Log(ctx context.Context, entry types.AuditEntry) error

	// Query retrieves audit entries matching the given filter.
	Query(ctx context.Context, filter Filter) ([]types.AuditEntry, error)

	// Stream returns a channel of real-time audit events (for dashboard).
	Stream(ctx context.Context, agentID string) (<-chan types.AuditEntry, error)

	// Subscribe returns a channel of real-time audit events filtered by the given criteria.
	// The channel is closed when ctx is cancelled. Non-blocking: entries are dropped if the
	// subscriber's channel is full.
	Subscribe(ctx context.Context, filter SubscriptionFilter) (<-chan types.AuditEntry, error)

	// Close flushes any buffered entries and releases resources.
	Close() error
}

// SubscriptionFilter controls which audit entries a subscriber receives.
type SubscriptionFilter struct {
	AgentID   string   // if non-empty, only entries for this agent
	Actions   []string // if non-empty, only entries matching one of these actions
	Levels    []string // if non-empty, only entries matching one of these risk levels
	Decisions []string // if non-empty, only entries matching one of these decisions
}

// Filter defines query parameters for audit log retrieval.
type Filter struct {
	ID        string          `json:"id,omitempty"`
	AgentID   string          `json:"agent_id,omitempty"`
	EventType types.EventType `json:"event_type,omitempty"`
	Decision  string          `json:"decision,omitempty"`
	RiskLevel string          `json:"risk_level,omitempty"`
	StartTime *time.Time      `json:"start_time,omitempty"`
	EndTime   *time.Time      `json:"end_time,omitempty"`
	Limit     int             `json:"limit"`
	Offset    int             `json:"offset"`
}
