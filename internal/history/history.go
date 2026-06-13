// Package history provides per-agent, per-channel conversation history storage.
package history

import (
	"context"
	"time"
)

// DefaultLimit is the default number of messages to retain in context.
const DefaultLimit = 50

// HistoryStore persists and retrieves conversation history.
type HistoryStore interface {
	// Append adds a message to the conversation history.
	Append(ctx context.Context, entry HistoryEntry) error

	// Recent returns the most recent messages for an agent+channel pair, oldest first.
	Recent(ctx context.Context, agentID, channel, channelID string, limit int) ([]HistoryEntry, error)

	// Count returns the total message count for an agent+channel pair.
	Count(ctx context.Context, agentID, channel, channelID string) (int, error)

	// Trim removes the oldest entries, keeping only the most recent keepLast messages.
	// Returns the number of rows deleted.
	Trim(ctx context.Context, agentID, channel, channelID string, keepLast int) (int64, error)

	// Clear removes all history for an agent (all channels).
	Clear(ctx context.Context, agentID string) error

	// Search performs a simple text search across message content for an agent.
	Search(ctx context.Context, agentID string, query string, limit int) ([]HistoryEntry, error)

	// ActiveSummary returns the current (non-compressed) summary for a conversation.
	// Returns nil, nil if no summary exists.
	ActiveSummary(ctx context.Context, agentID, channel, channelID string) (*HistoryEntry, error)

	// MarkCompressed sets CompressedBy on the given entry IDs, linking them to a summary.
	MarkCompressed(ctx context.Context, ids []int64, summaryID int64) error

	// AppendAndCompress atomically inserts a summary entry and marks the given IDs
	// as compressed by the new summary's ID. Used by the compressor for crash safety.
	AppendAndCompress(ctx context.Context, summary HistoryEntry, compressIDs []int64) error
}

// HistoryEntry represents a single message in conversation history.
type HistoryEntry struct {
	ID            int64
	AgentID       string
	Channel       string
	ChannelID     string
	Role          string // "user", "assistant", or "tool"
	Content       string
	Sender        string
	Tokens        int
	Attachments   string // JSON metadata: [{filename, content_type, size}] — no Data
	ToolCallID    string // For tool-role messages: ID linking back to the assistant's tool call
	ToolCallsJSON string // For assistant-role messages: JSON array of []models.ToolUse
	CompressedBy  int64  // 0 = not compressed; non-zero = summary entry ID
	CreatedAt     time.Time
}

// EstimateTokens returns a rough token count using len(content)/4.
func EstimateTokens(content string) int {
	n := len(content) / 4
	if n == 0 && len(content) > 0 {
		n = 1
	}
	return n
}
