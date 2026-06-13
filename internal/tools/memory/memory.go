// Package memory implements a KTP memory tool for agent long-term memory.
package memory

import (
	"context"
	"fmt"
	"time"

	"github.com/kkjorsvik/kyvik/internal/ktp"
	"github.com/kkjorsvik/kyvik/internal/memory"
)

// maxMemories is the default cap on active memories per agent.
// TODO: get from agent config.
const maxMemories = 100

// MemoryTool implements ktp.Tool for agent memory operations.
type MemoryTool struct {
	store memory.MemoryStore
}

// New creates a MemoryTool backed by the given store.
func New(store memory.MemoryStore) *MemoryTool {
	return &MemoryTool{store: store}
}

// Declaration returns the memory tool's KTP declaration.
func (t *MemoryTool) Declaration() ktp.ToolDeclaration {
	return ktp.ToolDeclaration{
		Name:        "memory",
		Version:     "1.0.0",
		Description: "Store and retrieve long-term memories for the agent",
		MinTier:      ktp.TierReader,
		DefaultTiers: []string{ktp.TierReader, ktp.TierWriter, ktp.TierOperator, ktp.TierAdmin},
		Actions: []ktp.ActionSpec{
			{
				Name:        "remember",
				Description: "Store a new memory",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"content":  {Type: "string", Description: "Memory content to store"},
						"category": {Type: "string", Description: "Memory category", Enum: []string{"fact", "decision", "context", "instruction"}, Default: "fact"},
						"pinned":   {Type: "boolean", Description: "Pin this memory so it is never archived", Default: false},
					},
					Required: []string{"content"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"id": {Type: "integer"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "memory", Access: "write", Resource: "*"}},
			},
			{
				Name:        "recall",
				Description: "Recall memories, optionally filtered by category",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"query":    {Type: "string", Description: "Search query (future: semantic search)"},
						"category": {Type: "string", Description: "Filter by category"},
						"limit":    {Type: "integer", Description: "Max memories to return", Default: 10},
					},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"memories": {Type: "array", Items: &ktp.JSONSchema{Type: "object"}},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "memory", Access: "read", Resource: "*"}},
			},
			{
				Name:        "forget",
				Description: "Delete a memory by ID",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"id": {Type: "integer", Description: "Memory ID to delete"},
					},
					Required: []string{"id"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"deleted": {Type: "boolean"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "memory", Access: "write", Resource: "*"}},
				Destructive:          true,
			},
			{
				Name:        "list",
				Description: "List memories with pagination",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"category": {Type: "string", Description: "Filter by category"},
						"status":   {Type: "string", Description: "Filter by status (active, candidate, archived, all)", Default: "active"},
						"limit":    {Type: "integer", Description: "Max memories to return", Default: 50},
						"offset":   {Type: "integer", Description: "Pagination offset", Default: 0},
					},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"memories": {Type: "array", Items: &ktp.JSONSchema{Type: "object"}},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "memory", Access: "read", Resource: "*"}},
			},
			{
				Name:        "review",
				Description: "Review a candidate memory: accept, reject, or edit",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"id":      {Type: "integer", Description: "Memory ID to review"},
						"action":  {Type: "string", Description: "Review action", Enum: []string{"accept", "reject", "edit"}},
						"content": {Type: "string", Description: "New content (required for edit action)"},
					},
					Required: []string{"id", "action"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"reviewed": {Type: "boolean"},
						"status":   {Type: "string"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "memory", Access: "write", Resource: "*"}},
			},
			{
				Name:        "review_all",
				Description: "Accept or reject all candidate memories at once",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"action": {Type: "string", Description: "Review action for all candidates", Enum: []string{"accept", "reject"}},
					},
					Required: []string{"action"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"count":  {Type: "integer"},
						"action": {Type: "string"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "memory", Access: "write", Resource: "*"}},
			},
			{
				Name:        "stats",
				Description: "Get memory usage statistics for the agent",
				Parameters: ktp.JSONSchema{
					Type: "object",
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"active_count":    {Type: "integer"},
						"candidate_count": {Type: "integer"},
						"pinned_count":    {Type: "integer"},
						"archived_count":  {Type: "integer"},
						"max_memories":    {Type: "integer"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "memory", Access: "read", Resource: "*"}},
			},
		},
	}
}

// Inline returns true because the memory tool accesses local state only
// and does not need sandbox isolation.
func (t *MemoryTool) Inline() bool { return true }

// Execute dispatches to the requested action.
func (t *MemoryTool) Execute(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	start := time.Now()

	switch req.Action {
	case "remember":
		return t.remember(ctx, req, start)
	case "recall":
		return t.recall(ctx, req, start)
	case "forget":
		return t.forget(ctx, req, start)
	case "list":
		return t.list(ctx, req, start)
	case "review":
		return t.review(ctx, req, start)
	case "review_all":
		return t.reviewAll(ctx, req, start)
	case "stats":
		return t.stats(ctx, req, start)
	default:
		return errResp(req.ID, fmt.Sprintf("unknown action: %s", req.Action)), nil
	}
}

func (t *MemoryTool) remember(ctx context.Context, req ktp.ToolRequest, start time.Time) (*ktp.ToolResponse, error) {
	content, err := strParam(req.Parameters, "content")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	category := strDefault(req.Parameters, "category", "fact")
	pinned := boolDefault(req.Parameters, "pinned", false)

	mem := memory.Memory{
		AgentID:  req.AgentID,
		Category: category,
		Content:  content,
		Source:   memory.SourceAgent,
		Status:   memory.StatusActive,
		Pinned:   pinned,
	}

	id, err := t.store.EnforceCapAndStore(ctx, mem, maxMemories)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("failed to create memory: %s", err)), nil
	}

	resp := ktp.NewToolResponse(req.ID, true, map[string]any{"id": id}, "", ms(start))
	return &resp, nil
}

func (t *MemoryTool) recall(ctx context.Context, req ktp.ToolRequest, start time.Time) (*ktp.ToolResponse, error) {
	category := strDefault(req.Parameters, "category", "")
	limit := intDefault(req.Parameters, "limit", 10)

	opts := memory.ListOptions{
		Category: category,
		Limit:    limit,
	}

	memories, err := t.store.List(ctx, req.AgentID, opts)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("failed to recall memories: %s", err)), nil
	}

	result := memoriesToResult(memories)
	resp := ktp.NewToolResponse(req.ID, true, map[string]any{"memories": result}, "", ms(start))
	return &resp, nil
}

func (t *MemoryTool) forget(ctx context.Context, req ktp.ToolRequest, start time.Time) (*ktp.ToolResponse, error) {
	id, err := intParam(req.Parameters, "id")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	// Verify ownership — agent can only delete its own memories.
	mem, err := t.store.Get(ctx, id)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("memory not found: %s", err)), nil
	}
	if mem.AgentID != req.AgentID {
		return errResp(req.ID, "cannot delete another agent's memory"), nil
	}

	if err := t.store.Delete(ctx, id); err != nil {
		return errResp(req.ID, fmt.Sprintf("failed to delete memory: %s", err)), nil
	}

	resp := ktp.NewToolResponse(req.ID, true, map[string]any{"deleted": true}, "", ms(start))
	return &resp, nil
}

func (t *MemoryTool) list(ctx context.Context, req ktp.ToolRequest, start time.Time) (*ktp.ToolResponse, error) {
	category := strDefault(req.Parameters, "category", "")
	status := strDefault(req.Parameters, "status", "active")
	limit := intDefault(req.Parameters, "limit", 50)
	offset := intDefault(req.Parameters, "offset", 0)

	opts := memory.ListOptions{
		Category: category,
		Status:   status,
		Limit:    limit,
		Offset:   offset,
	}

	memories, err := t.store.List(ctx, req.AgentID, opts)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("failed to list memories: %s", err)), nil
	}

	result := memoriesToResult(memories)
	resp := ktp.NewToolResponse(req.ID, true, map[string]any{"memories": result}, "", ms(start))
	return &resp, nil
}

func (t *MemoryTool) review(ctx context.Context, req ktp.ToolRequest, start time.Time) (*ktp.ToolResponse, error) {
	id, err := intParam(req.Parameters, "id")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}
	action, err := strParam(req.Parameters, "action")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	// Verify ownership and candidate status.
	mem, err := t.store.Get(ctx, id)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("memory not found: %s", err)), nil
	}
	if mem.AgentID != req.AgentID {
		return errResp(req.ID, "cannot review another agent's memory"), nil
	}
	if mem.Status != memory.StatusCandidate {
		return errResp(req.ID, fmt.Sprintf("memory %d is not a candidate (status: %s)", id, mem.Status)), nil
	}

	switch action {
	case "accept":
		if err := t.store.PromoteCandidate(ctx, id); err != nil {
			return errResp(req.ID, fmt.Sprintf("failed to accept: %s", err)), nil
		}
		if err := t.enforceCap(ctx, req.AgentID); err != nil {
			return errResp(req.ID, fmt.Sprintf("cap enforcement failed: %s", err)), nil
		}
		resp := ktp.NewToolResponse(req.ID, true, map[string]any{"reviewed": true, "status": "accepted"}, "", ms(start))
		return &resp, nil

	case "reject":
		if err := t.store.RejectCandidate(ctx, id); err != nil {
			return errResp(req.ID, fmt.Sprintf("failed to reject: %s", err)), nil
		}
		resp := ktp.NewToolResponse(req.ID, true, map[string]any{"reviewed": true, "status": "rejected"}, "", ms(start))
		return &resp, nil

	case "edit":
		content, err := strParam(req.Parameters, "content")
		if err != nil {
			return errResp(req.ID, "content parameter is required for edit action"), nil
		}
		mem.Content = content
		if err := t.store.Update(ctx, mem); err != nil {
			return errResp(req.ID, fmt.Sprintf("failed to update: %s", err)), nil
		}
		if err := t.store.PromoteCandidate(ctx, id); err != nil {
			return errResp(req.ID, fmt.Sprintf("failed to promote after edit: %s", err)), nil
		}
		if err := t.enforceCap(ctx, req.AgentID); err != nil {
			return errResp(req.ID, fmt.Sprintf("cap enforcement failed: %s", err)), nil
		}
		resp := ktp.NewToolResponse(req.ID, true, map[string]any{"reviewed": true, "status": "edited_and_accepted"}, "", ms(start))
		return &resp, nil

	default:
		return errResp(req.ID, fmt.Sprintf("invalid review action: %s (use accept, reject, or edit)", action)), nil
	}
}

func (t *MemoryTool) reviewAll(ctx context.Context, req ktp.ToolRequest, start time.Time) (*ktp.ToolResponse, error) {
	action, err := strParam(req.Parameters, "action")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	candidates, err := t.store.ListCandidates(ctx, req.AgentID)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("failed to list candidates: %s", err)), nil
	}

	count := 0
	switch action {
	case "accept":
		for _, c := range candidates {
			if err := t.store.PromoteCandidate(ctx, c.ID); err == nil {
				count++
			}
		}
		// Enforce cap once after all promotions.
		if err := t.enforceCap(ctx, req.AgentID); err != nil {
			return errResp(req.ID, fmt.Sprintf("cap enforcement failed after accepting %d: %s", count, err)), nil
		}
	case "reject":
		for _, c := range candidates {
			if err := t.store.RejectCandidate(ctx, c.ID); err == nil {
				count++
			}
		}
	default:
		return errResp(req.ID, fmt.Sprintf("invalid review_all action: %s (use accept or reject)", action)), nil
	}

	resp := ktp.NewToolResponse(req.ID, true, map[string]any{"count": count, "action": action}, "", ms(start))
	return &resp, nil
}

func (t *MemoryTool) stats(ctx context.Context, req ktp.ToolRequest, start time.Time) (*ktp.ToolResponse, error) {
	activeCount, err := t.store.CountFiltered(ctx, req.AgentID, memory.ListOptions{Status: memory.StatusActive})
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("failed to count active: %s", err)), nil
	}

	candidateCount, err := t.store.CountCandidates(ctx, req.AgentID)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("failed to count candidates: %s", err)), nil
	}

	pinned, err := t.store.ListPinned(ctx, req.AgentID)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("failed to count pinned: %s", err)), nil
	}

	archivedCount, err := t.store.CountFiltered(ctx, req.AgentID, memory.ListOptions{Status: memory.StatusArchived})
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("failed to count archived: %s", err)), nil
	}

	resp := ktp.NewToolResponse(req.ID, true, map[string]any{
		"active_count":    activeCount,
		"candidate_count": candidateCount,
		"pinned_count":    len(pinned),
		"archived_count":  archivedCount,
		"max_memories":    maxMemories,
	}, "", ms(start))
	return &resp, nil
}

// enforceCap checks if the agent has exceeded the memory cap and archives
// the lowest-scored active memories until within the limit.
func (t *MemoryTool) enforceCap(ctx context.Context, agentID string) error {
	for {
		count, err := t.store.CountFiltered(ctx, agentID, memory.ListOptions{Status: memory.StatusActive})
		if err != nil {
			return fmt.Errorf("count active memories: %w", err)
		}
		if count <= maxMemories {
			return nil
		}

		// List all active non-pinned memories to find eviction target.
		// We get them all and pick the one with lowest access_count and oldest access time.
		active, err := t.store.List(ctx, agentID, memory.ListOptions{
			Status: memory.StatusActive,
			Limit:  count,
		})
		if err != nil {
			return fmt.Errorf("list active memories: %w", err)
		}

		// Find the best candidate for archival: lowest access count, oldest access.
		var targetID int64 = -1
		var lowestScore float64 = -1
		first := true
		for _, m := range active {
			if m.Pinned {
				continue
			}
			// Simple eviction score: lower is more evictable.
			score := float64(m.AccessCount) + float64(time.Since(m.AccessedAt).Hours())*-0.01
			if first || score < lowestScore {
				lowestScore = score
				targetID = m.ID
				first = false
			}
		}
		if targetID == -1 {
			// All memories are pinned, cannot evict further.
			return nil
		}

		if err := t.store.Archive(ctx, targetID); err != nil {
			return fmt.Errorf("archive memory %d: %w", targetID, err)
		}
	}
}

// memoriesToResult converts memories to a serializable format.
func memoriesToResult(memories []memory.Memory) []map[string]any {
	result := make([]map[string]any, 0, len(memories))
	for _, m := range memories {
		result = append(result, map[string]any{
			"id":           m.ID,
			"content":      m.Content,
			"category":     m.Category,
			"pinned":       m.Pinned,
			"created_at":   m.CreatedAt.UTC().Format(time.RFC3339),
			"access_count": m.AccessCount,
		})
	}
	return result
}

// --- parameter helpers ---

func strParam(params map[string]any, key string) (string, error) {
	raw, ok := params[key]
	if !ok {
		return "", fmt.Errorf("missing required parameter: %s", key)
	}
	s, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("parameter %s must be a string", key)
	}
	return s, nil
}

func strDefault(params map[string]any, key, def string) string {
	raw, ok := params[key]
	if !ok {
		return def
	}
	s, ok := raw.(string)
	if !ok {
		return def
	}
	return s
}

func intParam(params map[string]any, key string) (int64, error) {
	raw, ok := params[key]
	if !ok {
		return 0, fmt.Errorf("missing required parameter: %s", key)
	}
	switch v := raw.(type) {
	case float64:
		return int64(v), nil
	case int:
		return int64(v), nil
	case int64:
		return v, nil
	default:
		return 0, fmt.Errorf("parameter %s must be a number", key)
	}
}

func intDefault(params map[string]any, key string, def int) int {
	raw, ok := params[key]
	if !ok {
		return def
	}
	switch v := raw.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	default:
		return def
	}
}

func boolDefault(params map[string]any, key string, def bool) bool {
	raw, ok := params[key]
	if !ok {
		return def
	}
	b, ok := raw.(bool)
	if !ok {
		return def
	}
	return b
}

func errResp(reqID, msg string) *ktp.ToolResponse {
	resp := ktp.NewToolResponse(reqID, false, nil, msg, 0)
	return &resp
}

func ms(start time.Time) int64 {
	return time.Since(start).Milliseconds()
}
