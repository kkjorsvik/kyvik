package history

import (
	"context"
	"time"
)

// WebConversation represents a chat conversation in the web UI.
type WebConversation struct {
	ID           string
	AgentID      string
	Title        string
	MessageCount int
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// ConversationGroup holds conversations grouped by time period for sidebar display.
type ConversationGroup struct {
	Label         string
	Conversations []WebConversation
}

// ConversationStore manages web conversation metadata.
type ConversationStore interface {
	CreateConversation(ctx context.Context, agentID, title string) (*WebConversation, error)
	ListConversations(ctx context.Context, agentID string) ([]WebConversation, error)
	GetConversation(ctx context.Context, id string) (*WebConversation, error)
	RenameConversation(ctx context.Context, id, title string) error
	DeleteConversation(ctx context.Context, id string) error
	DeleteByAgent(ctx context.Context, agentID string) error
	IncrementMessageCount(ctx context.Context, id string, delta int) error
	MostRecentConversation(ctx context.Context, agentID string) (*WebConversation, error)
}
