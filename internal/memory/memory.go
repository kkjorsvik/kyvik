// Package memory provides per-agent long-term memory storage.
package memory

import (
	"context"
	"time"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

// Status values for the memory lifecycle.
const (
	StatusCandidate = "candidate"
	StatusActive    = "active"
	StatusArchived  = "archived"
)

// Categories for memory entries.
const (
	CategoryFact        = "fact"
	CategoryDecision    = "decision"
	CategoryContext     = "context"
	CategoryInstruction = "instruction"
)

// Sources for memory entries.
const (
	SourceAgent = "agent"
	SourceUser  = "user"
	SourceAuto  = "auto"
)

// MemoryStore persists and retrieves agent memories.
type MemoryStore interface {
	// Create inserts a new memory and returns its ID.
	Create(ctx context.Context, mem Memory) (int64, error)

	// Get returns a single memory by ID.
	Get(ctx context.Context, id int64) (Memory, error)

	// Update modifies an existing memory.
	Update(ctx context.Context, mem Memory) error

	// Delete removes a single memory by ID.
	Delete(ctx context.Context, id int64) error

	// List returns memories for an agent, filtered by ListOptions.
	List(ctx context.Context, agentID string, opts ListOptions) ([]Memory, error)

	// CountFiltered returns the count for an agent, filtered by ListOptions.
	CountFiltered(ctx context.Context, agentID string, opts ListOptions) (int, error)

	// ListPinned returns all pinned memories for an agent.
	ListPinned(ctx context.Context, agentID string) ([]Memory, error)

	// Touch updates accessed_at and increments access_count.
	Touch(ctx context.Context, id int64) error

	// SetEmbedding stores the embedding vector and model name for a memory.
	SetEmbedding(ctx context.Context, id int64, embedding []float32, model string) error

	// ListWithEmbeddings returns memories with their embedding vectors loaded.
	// Only returns memories that have a non-NULL embedding.
	ListWithEmbeddings(ctx context.Context, agentID string) ([]Memory, error)

	// GetUnembedded returns memories that have no embedding yet.
	GetUnembedded(ctx context.Context, agentID string) ([]Memory, error)

	// GetAllUnembedded returns unembedded memories across all agents.
	GetAllUnembedded(ctx context.Context, limit int) ([]Memory, error)

	// ListRecent returns non-archived memories ordered by most recently accessed,
	// falling back to creation time. Used when no embedding provider is available.
	ListRecent(ctx context.Context, agentID string, limit int) ([]Memory, error)

	// Count returns the total number of memories for an agent.
	Count(ctx context.Context, agentID string) (int, error)

	// DeleteByAgent removes all memories for an agent.
	DeleteByAgent(ctx context.Context, agentID string) error

	// Import bulk-inserts memories for an agent, returning the count inserted.
	Import(ctx context.Context, agentID string, memories []Memory) (int, error)

	// CreateFromAgent is a convenience for agent-originated memories.
	CreateFromAgent(ctx context.Context, agentID, category, content string) (int64, error)

	// Archive marks a memory as archived.
	Archive(ctx context.Context, id int64) error

	// Unarchive restores an archived memory.
	Unarchive(ctx context.Context, id int64) error

	// ArchiveStale archives non-pinned memories not accessed within the given duration.
	// Returns the count of newly archived memories.
	ArchiveStale(ctx context.Context, agentID string, staleAfter time.Duration) (int, error)

	// Candidate pipeline
	ListCandidates(ctx context.Context, agentID string) ([]Memory, error)
	CountCandidates(ctx context.Context, agentID string) (int, error)
	PromoteCandidate(ctx context.Context, id int64) error
	RejectCandidate(ctx context.Context, id int64) error

	// EnforceCapAndStore checks the memory cap, evicts if needed, then stores
	// the memory as active. Returns the new memory ID.
	EnforceCapAndStore(ctx context.Context, mem Memory, maxMemories int) (int64, error)
}

// Memory represents a single long-term memory entry.
type Memory struct {
	ID              int64
	AgentID         string
	Category        string
	Content         string
	Source          string
	RelevanceScore  float64
	Pinned          bool
	Archived        bool
	Status          string
	Reviewed        bool
	SourceChannel   string
	SourceChannelID string
	Embedding       []float32
	EmbeddingModel  string
	CreatedAt       time.Time
	AccessedAt      time.Time
	AccessCount     int
}

// ListOptions controls filtering and pagination for List queries.
type ListOptions struct {
	Category        string
	Source          string
	Status          string // "active", "candidate", "all" — empty defaults to "active"
	Pinned          *bool
	Reviewed        *bool
	IncludeArchived *bool // nil/false = active only, true = include all
	ArchivedOnly    bool  // true = only show archived
	Limit           int
	Offset          int
}

// AgentLister is satisfied by store.Store (avoids importing core).
type AgentLister interface {
	ListAgents(ctx context.Context) ([]types.AgentConfig, error)
}
