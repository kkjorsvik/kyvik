package ctxbudget

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/history"
	"github.com/kkjorsvik/kyvik/internal/memory"
	"github.com/kkjorsvik/kyvik/internal/models"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// --- Mock memory store ---

type mockMemoryStore struct {
	memories []memory.Memory
}

func (m *mockMemoryStore) Create(_ context.Context, mem memory.Memory) (int64, error) {
	return 0, nil
}
func (m *mockMemoryStore) Get(_ context.Context, id int64) (memory.Memory, error) {
	return memory.Memory{}, nil
}
func (m *mockMemoryStore) Update(_ context.Context, _ memory.Memory) error { return nil }
func (m *mockMemoryStore) Delete(_ context.Context, _ int64) error         { return nil }
func (m *mockMemoryStore) List(_ context.Context, _ string, _ memory.ListOptions) ([]memory.Memory, error) {
	return nil, nil
}
func (m *mockMemoryStore) CountFiltered(_ context.Context, agentID string, opts memory.ListOptions) (int, error) {
	count := 0
	for _, mem := range m.memories {
		if mem.AgentID != agentID {
			continue
		}
		if opts.Category != "" && mem.Category != opts.Category {
			continue
		}
		if opts.Source != "" && mem.Source != opts.Source {
			continue
		}
		if opts.Pinned != nil && mem.Pinned != *opts.Pinned {
			continue
		}
		if opts.Reviewed != nil && mem.Reviewed != *opts.Reviewed {
			continue
		}
		if opts.ArchivedOnly && !mem.Archived {
			continue
		}
		if !opts.ArchivedOnly {
			if opts.IncludeArchived == nil || !*opts.IncludeArchived {
				if mem.Archived {
					continue
				}
			}
		}
		count++
	}
	return count, nil
}
func (m *mockMemoryStore) ListRecent(_ context.Context, agentID string, limit int) ([]memory.Memory, error) {
	var result []memory.Memory
	for _, mem := range m.memories {
		if mem.AgentID == agentID && !mem.Archived {
			result = append(result, mem)
		}
	}
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}
func (m *mockMemoryStore) ListPinned(_ context.Context, agentID string) ([]memory.Memory, error) {
	var pinned []memory.Memory
	for _, mem := range m.memories {
		if mem.AgentID == agentID && mem.Pinned {
			pinned = append(pinned, mem)
		}
	}
	return pinned, nil
}
func (m *mockMemoryStore) Touch(_ context.Context, _ int64) error { return nil }
func (m *mockMemoryStore) SetEmbedding(_ context.Context, _ int64, _ []float32, _ string) error {
	return nil
}
func (m *mockMemoryStore) ListWithEmbeddings(_ context.Context, _ string) ([]memory.Memory, error) {
	return nil, nil
}
func (m *mockMemoryStore) GetUnembedded(_ context.Context, _ string) ([]memory.Memory, error) {
	return nil, nil
}
func (m *mockMemoryStore) GetAllUnembedded(_ context.Context, _ int) ([]memory.Memory, error) {
	return nil, nil
}
func (m *mockMemoryStore) Count(_ context.Context, _ string) (int, error)  { return 0, nil }
func (m *mockMemoryStore) DeleteByAgent(_ context.Context, _ string) error { return nil }
func (m *mockMemoryStore) Import(_ context.Context, _ string, _ []memory.Memory) (int, error) {
	return 0, nil
}
func (m *mockMemoryStore) CreateFromAgent(_ context.Context, _, _, _ string) (int64, error) {
	return 0, nil
}
func (m *mockMemoryStore) Archive(_ context.Context, _ int64) error   { return nil }
func (m *mockMemoryStore) Unarchive(_ context.Context, _ int64) error { return nil }
func (m *mockMemoryStore) ArchiveStale(_ context.Context, _ string, _ time.Duration) (int, error) {
	return 0, nil
}

func (m *mockMemoryStore) ListCandidates(_ context.Context, _ string) ([]memory.Memory, error) {
	return nil, nil
}
func (m *mockMemoryStore) CountCandidates(_ context.Context, _ string) (int, error) { return 0, nil }
func (m *mockMemoryStore) PromoteCandidate(_ context.Context, _ int64) error        { return nil }
func (m *mockMemoryStore) RejectCandidate(_ context.Context, _ int64) error         { return nil }
func (m *mockMemoryStore) EnforceCapAndStore(_ context.Context, _ memory.Memory, _ int) (int64, error) {
	return 0, nil
}

// --- Mock history store ---

type mockHistoryStore struct {
	entries []history.HistoryEntry
}

func (m *mockHistoryStore) Append(_ context.Context, _ history.HistoryEntry) error { return nil }
func (m *mockHistoryStore) Recent(_ context.Context, agentID, channel, channelID string, limit int) ([]history.HistoryEntry, error) {
	var result []history.HistoryEntry
	for _, e := range m.entries {
		if e.AgentID == agentID {
			result = append(result, e)
		}
	}
	if limit > 0 && len(result) > limit {
		result = result[len(result)-limit:]
	}
	return result, nil
}
func (m *mockHistoryStore) Count(_ context.Context, _, _, _ string) (int, error) { return 0, nil }
func (m *mockHistoryStore) Trim(_ context.Context, _, _, _ string, _ int) (int64, error) {
	return 0, nil
}
func (m *mockHistoryStore) Clear(_ context.Context, _ string) error { return nil }
func (m *mockHistoryStore) Search(_ context.Context, _ string, _ string, _ int) ([]history.HistoryEntry, error) {
	return nil, nil
}
func (m *mockHistoryStore) ActiveSummary(_ context.Context, _, _, _ string) (*history.HistoryEntry, error) {
	return nil, nil
}
func (m *mockHistoryStore) MarkCompressed(_ context.Context, _ []int64, _ int64) error { return nil }
func (m *mockHistoryStore) AppendAndCompress(_ context.Context, _ history.HistoryEntry, _ []int64) error {
	return nil
}

// --- Mock skills provider ---

type mockSkillsProvider struct {
	content string
	err     error
}

func (m *mockSkillsProvider) PromptContentForAgent(_ context.Context, _ string) (string, error) {
	return m.content, m.err
}

type mockTeamContextProvider struct {
	content string
	err     error
}

func (m *mockTeamContextProvider) SharedContextForAgent(_ context.Context, _ string) (string, error) {
	return m.content, m.err
}

type mockIntegrationPromptProvider struct {
	content string
	err     error
}

func (m *mockIntegrationPromptProvider) PromptContentForAgent(_ context.Context, _ string) (string, error) {
	return m.content, m.err
}

// --- Tests ---

func TestAssemble_DefaultBudget(t *testing.T) {
	a := New(nil, nil)
	config := types.AgentConfig{
		ID:          "test-agent",
		SoulContent: "You are helpful.",
	}

	result, err := a.Assemble(context.Background(), config, "hello", AssembleOptions{})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	if result.SystemPrompt == "" {
		t.Error("SystemPrompt should not be empty")
	}
	if !strings.Contains(result.SystemPrompt, "You are helpful.") {
		t.Error("SystemPrompt should contain soul content")
	}
	if result.TokenEstimate <= 0 {
		t.Errorf("TokenEstimate = %d, want > 0", result.TokenEstimate)
	}
}

func TestAssemble_SoulIdentityTruncation(t *testing.T) {
	a := New(nil, nil)

	// Create very long soul and identity content
	longSoul := strings.Repeat("Soul text here. ", 500)    // ~8000 chars
	longIdentity := strings.Repeat("Identity text. ", 500) // ~7500 chars

	config := types.AgentConfig{
		ID:              "test-agent",
		SoulContent:     longSoul,
		IdentityContent: longIdentity,
		ContextBudget: types.ContextBudget{
			MaxTotalTokens:  1000,
			SoulIdentityPct: 10, // only 100 tokens = ~400 chars for soul+identity
			MemoriesPct:     25,
			HistoryPct:      50,
			SkillsPct:       10,
		},
	}

	result, err := a.Assemble(context.Background(), config, "hello", AssembleOptions{})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	// System prompt should be truncated to fit within soul budget
	soulTokens := EstimateTokens(result.SystemPrompt)
	soulBudget := 100 // 1000 * 10 / 100
	if soulTokens > soulBudget {
		t.Errorf("soul+identity tokens = %d, exceeds budget %d", soulTokens, soulBudget)
	}
}

func TestAssemble_MemoryBudgetEnforced(t *testing.T) {
	ms := &mockMemoryStore{
		memories: []memory.Memory{
			{ID: 1, AgentID: "test-agent", Category: "fact", Content: strings.Repeat("Memory one. ", 100), Pinned: true},
			{ID: 2, AgentID: "test-agent", Category: "fact", Content: strings.Repeat("Memory two. ", 100), Pinned: false},
			{ID: 3, AgentID: "test-agent", Category: "fact", Content: strings.Repeat("Memory three. ", 100), Pinned: false},
		},
	}

	a := New(ms, nil)
	config := types.AgentConfig{
		ID:          "test-agent",
		SoulContent: "Short soul.",
		ContextBudget: types.ContextBudget{
			MaxTotalTokens:  500,
			SoulIdentityPct: 10,
			MemoriesPct:     10, // very small memory budget = 50 tokens
			HistoryPct:      50,
			SkillsPct:       10,
		},
	}

	result, err := a.Assemble(context.Background(), config, "hello", AssembleOptions{})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	// Pinned memory should always be included
	if !strings.Contains(result.SystemPrompt, "Memory one.") {
		t.Error("pinned memory should always be included")
	}
}

func TestAssemble_IntegrationPromptsIncluded(t *testing.T) {
	a := New(nil, nil)
	a.SetIntegrationPromptProvider(&mockIntegrationPromptProvider{content: "### Google Calendar\nUse rest_api__call."})

	config := types.AgentConfig{
		ID:          "test-agent",
		SoulContent: "You are helpful.",
	}

	result, err := a.Assemble(context.Background(), config, "hello", AssembleOptions{})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if !strings.Contains(result.SystemPrompt, "## Active Integrations") {
		t.Fatalf("system prompt missing integrations section:\n%s", result.SystemPrompt)
	}
	if !strings.Contains(result.SystemPrompt, "Google Calendar") {
		t.Fatalf("system prompt missing integration content:\n%s", result.SystemPrompt)
	}
}

func TestAssemble_HistoryBudgetEnforced(t *testing.T) {
	hs := &mockHistoryStore{
		entries: []history.HistoryEntry{
			{AgentID: "test-agent", Role: "user", Content: strings.Repeat("Old message. ", 200)},
			{AgentID: "test-agent", Role: "assistant", Content: strings.Repeat("Old reply. ", 200)},
			{AgentID: "test-agent", Role: "user", Content: "Recent question?"},
			{AgentID: "test-agent", Role: "assistant", Content: "Recent answer."},
		},
	}

	a := New(nil, hs)
	config := types.AgentConfig{
		ID:           "test-agent",
		SoulContent:  "Short soul.",
		HistoryLimit: 50,
		ContextBudget: types.ContextBudget{
			MaxTotalTokens:  200,
			SoulIdentityPct: 10,
			MemoriesPct:     10,
			HistoryPct:      30, // 60 tokens for history
			SkillsPct:       10,
		},
	}

	result, err := a.Assemble(context.Background(), config, "hello", AssembleOptions{
		Channel: "webui",
	})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	// Recent messages should be kept, old messages dropped
	hasRecent := false
	for _, m := range result.Messages {
		if strings.Contains(m.Content, "Recent") {
			hasRecent = true
		}
	}
	if !hasRecent && len(result.Messages) > 0 {
		t.Error("recent messages should be preserved when trimming history")
	}
}

func TestAssemble_ToolRoleFiltered(t *testing.T) {
	// Old-format entries: tool messages without ToolCallID, assistant messages
	// with "[tool_calls]" prefix or empty content. These should all be filtered.
	hs := &mockHistoryStore{
		entries: []history.HistoryEntry{
			{AgentID: "test-agent", Role: "user", Content: "Use the file tool"},
			{AgentID: "test-agent", Role: "assistant", Content: "I'll list files"},
			{AgentID: "test-agent", Role: "tool", Content: `{"files":["a.txt"]}`},                                 // old format: no ToolCallID
			{AgentID: "test-agent", Role: "assistant", Content: `[tool_calls] [{"id":"tc1","name":"file.list"}]`}, // old format: mangled
			{AgentID: "test-agent", Role: "tool", Content: `{"content":"hello"}`},                                 // old format: no ToolCallID
			{AgentID: "test-agent", Role: "assistant", Content: ""},                                               // old format: empty
			{AgentID: "test-agent", Role: "assistant", Content: "Found the file"},
		},
	}

	a := New(nil, hs)
	config := types.AgentConfig{
		ID:           "test-agent",
		SoulContent:  "Short.",
		HistoryLimit: 50,
	}

	result, err := a.Assemble(context.Background(), config, "current", AssembleOptions{
		Channel: "webui",
	})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	// Old-format tool, empty assistant, and [tool_calls] assistant entries filtered out.
	// Remaining: user, assistant("I'll list files"), assistant("Found the file") → merged to 2
	for _, m := range result.Messages {
		if m.Role == "tool" {
			t.Error("old-format tool messages should be filtered from history")
		}
		if m.Role == "assistant" && m.Content == "" {
			t.Error("empty assistant messages should be filtered from history")
		}
		if strings.HasPrefix(m.Content, "[tool_calls]") {
			t.Error("[tool_calls] prefixed messages should be filtered from history")
		}
	}
	if len(result.Messages) != 2 {
		t.Fatalf("expected 2 messages after filtering, got %d: %+v", len(result.Messages), result.Messages)
	}
}

func TestAssemble_ToolRoleWithMetadata(t *testing.T) {
	// New-format entries: tool messages with ToolCallID and assistant messages
	// with ToolCallsJSON should be reconstructed into proper ChatMessages.
	hs := &mockHistoryStore{
		entries: []history.HistoryEntry{
			{AgentID: "test-agent", Role: "user", Content: "Use the file tool"},
			{AgentID: "test-agent", Role: "assistant", Content: "I'll list files.",
				ToolCallsJSON: `[{"id":"tc_001","name":"file.list","parameters":{"path":"/"}}]`},
			{AgentID: "test-agent", Role: "tool", Content: `{"files":["a.txt"]}`,
				ToolCallID: "tc_001"},
			{AgentID: "test-agent", Role: "assistant", Content: "Found 1 file: a.txt"},
		},
	}

	a := New(nil, hs)
	config := types.AgentConfig{
		ID:           "test-agent",
		SoulContent:  "Short.",
		HistoryLimit: 50,
	}

	result, err := a.Assemble(context.Background(), config, "current", AssembleOptions{
		Channel: "webui",
	})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	// Should have 4 messages: user, assistant+toolcalls, tool, assistant
	if len(result.Messages) != 4 {
		t.Fatalf("expected 4 messages, got %d: %+v", len(result.Messages), result.Messages)
	}

	// Verify assistant with tool calls
	assistantTC := result.Messages[1]
	if assistantTC.Role != "assistant" {
		t.Errorf("messages[1].Role = %q, want assistant", assistantTC.Role)
	}
	if len(assistantTC.ToolCalls) != 1 {
		t.Fatalf("messages[1].ToolCalls = %d, want 1", len(assistantTC.ToolCalls))
	}
	if assistantTC.ToolCalls[0].ID != "tc_001" {
		t.Errorf("ToolCalls[0].ID = %q, want tc_001", assistantTC.ToolCalls[0].ID)
	}
	if assistantTC.ToolCalls[0].Name != "file.list" {
		t.Errorf("ToolCalls[0].Name = %q, want file.list", assistantTC.ToolCalls[0].Name)
	}

	// Verify tool result
	toolMsg := result.Messages[2]
	if toolMsg.Role != "tool" {
		t.Errorf("messages[2].Role = %q, want tool", toolMsg.Role)
	}
	if toolMsg.ToolCallID != "tc_001" {
		t.Errorf("messages[2].ToolCallID = %q, want tc_001", toolMsg.ToolCallID)
	}

	// Verify final assistant (no tool calls, not merged with the tool-call assistant)
	finalAssistant := result.Messages[3]
	if finalAssistant.Role != "assistant" {
		t.Errorf("messages[3].Role = %q, want assistant", finalAssistant.Role)
	}
	if len(finalAssistant.ToolCalls) != 0 {
		t.Errorf("messages[3] should have no ToolCalls")
	}
}

func TestAssemble_ToolOrphanCleanup(t *testing.T) {
	// When budget trimming removes the assistant message that contained
	// tool calls, orphaned leading tool messages should be dropped.
	hs := &mockHistoryStore{
		entries: []history.HistoryEntry{
			{AgentID: "test-agent", Role: "assistant", Content: strings.Repeat("Big content. ", 500),
				ToolCallsJSON: `[{"id":"tc_001","name":"file.list","parameters":{}}]`},
			{AgentID: "test-agent", Role: "tool", Content: "result data",
				ToolCallID: "tc_001"},
			{AgentID: "test-agent", Role: "assistant", Content: "Final answer."},
		},
	}

	a := New(nil, hs)
	config := types.AgentConfig{
		ID:           "test-agent",
		SoulContent:  "Short.",
		HistoryLimit: 50,
		ContextBudget: types.ContextBudget{
			MaxTotalTokens:  200,
			SoulIdentityPct: 10,
			MemoriesPct:     10,
			HistoryPct:      30, // 60 tokens — not enough for the big assistant message
			SkillsPct:       10,
		},
	}

	result, err := a.Assemble(context.Background(), config, "current", AssembleOptions{
		Channel: "webui",
	})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	// The big assistant message should be trimmed, and the orphaned tool message
	// should also be removed. Only the final assistant should remain.
	for _, m := range result.Messages {
		if m.Role == "tool" {
			t.Error("orphaned tool message should be cleaned up after budget trimming")
		}
	}
}

func TestAssemble_ConsecutiveSameRoleMerged(t *testing.T) {
	hs := &mockHistoryStore{
		entries: []history.HistoryEntry{
			{AgentID: "test-agent", Role: "user", Content: "Hello"},
			{AgentID: "test-agent", Role: "assistant", Content: "Hi there"},
			{AgentID: "test-agent", Role: "user", Content: "First retry"},
			{AgentID: "test-agent", Role: "user", Content: "Second retry"},
			{AgentID: "test-agent", Role: "user", Content: "Third retry"},
		},
	}

	a := New(nil, hs)
	config := types.AgentConfig{
		ID:           "test-agent",
		SoulContent:  "Short.",
		HistoryLimit: 50,
	}

	result, err := a.Assemble(context.Background(), config, "current", AssembleOptions{
		Channel: "webui",
	})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	// 5 entries should be merged into 3 messages: user, assistant, user (merged)
	if len(result.Messages) != 3 {
		t.Fatalf("expected 3 messages after merging, got %d", len(result.Messages))
	}
	if result.Messages[2].Role != "user" {
		t.Errorf("expected last message role=user, got %s", result.Messages[2].Role)
	}
	if !strings.Contains(result.Messages[2].Content, "First retry") ||
		!strings.Contains(result.Messages[2].Content, "Third retry") {
		t.Errorf("expected merged content to contain all retry messages, got: %s", result.Messages[2].Content)
	}
}

func TestAssemble_NilStores(t *testing.T) {
	a := New(nil, nil)
	config := types.AgentConfig{
		ID:          "test-agent",
		SoulContent: "Test soul.",
	}

	result, err := a.Assemble(context.Background(), config, "hello", AssembleOptions{})
	if err != nil {
		t.Fatalf("Assemble with nil stores: %v", err)
	}

	if result.SystemPrompt == "" {
		t.Error("should produce system prompt even with nil stores")
	}
	if len(result.Messages) != 0 {
		t.Errorf("Messages = %d, want 0 with nil history store", len(result.Messages))
	}
}

func TestAssemble_TokenEstimateAccurate(t *testing.T) {
	a := New(nil, nil)
	config := types.AgentConfig{
		ID:          "test-agent",
		SoulContent: "A test soul with some content.",
	}

	result, err := a.Assemble(context.Background(), config, "hello", AssembleOptions{})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	// Token estimate should be at least the estimate of the system prompt
	sysTokens := EstimateTokens(result.SystemPrompt)
	if result.TokenEstimate < sysTokens {
		t.Errorf("TokenEstimate = %d, but system prompt alone is %d tokens", result.TokenEstimate, sysTokens)
	}
}

func TestAssemble_CurrentMessageNotTruncated(t *testing.T) {
	a := New(nil, nil)

	// Create a very long current message
	longMessage := strings.Repeat("This is a very long user message. ", 1000)

	config := types.AgentConfig{
		ID:          "test-agent",
		SoulContent: "Short.",
		ContextBudget: types.ContextBudget{
			MaxTotalTokens:  100, // tiny budget
			SoulIdentityPct: 50,
			MemoriesPct:     10,
			HistoryPct:      30,
			SkillsPct:       10,
		},
	}

	// Assemble should succeed — current message is not included in the result
	// (caller appends it), so the assembler never truncates it.
	result, err := a.Assemble(context.Background(), config, longMessage, AssembleOptions{})
	if err != nil {
		t.Fatalf("Assemble with long message: %v", err)
	}

	if result.SystemPrompt == "" {
		t.Error("should still produce a system prompt")
	}
}

func TestNormalizeContextBudget_Defaults(t *testing.T) {
	cb := types.NormalizeContextBudget(types.ContextBudget{})

	if cb.MaxTotalTokens != types.DefaultContextMaxTotalTokens {
		t.Errorf("MaxTotalTokens = %d, want %d", cb.MaxTotalTokens, types.DefaultContextMaxTotalTokens)
	}
	if cb.SoulIdentityPct != types.DefaultContextSoulIdentityPct {
		t.Errorf("SoulIdentityPct = %d, want %d", cb.SoulIdentityPct, types.DefaultContextSoulIdentityPct)
	}
	if cb.SkillsPct != types.DefaultContextSkillsPct {
		t.Errorf("SkillsPct = %d, want %d", cb.SkillsPct, types.DefaultContextSkillsPct)
	}
	if cb.MemoriesPct != types.DefaultContextMemoriesPct {
		t.Errorf("MemoriesPct = %d, want %d", cb.MemoriesPct, types.DefaultContextMemoriesPct)
	}
	if cb.HistoryPct != types.DefaultContextHistoryPct {
		t.Errorf("HistoryPct = %d, want %d", cb.HistoryPct, types.DefaultContextHistoryPct)
	}
}

func TestNormalizeContextBudget_CustomValues(t *testing.T) {
	cb := types.NormalizeContextBudget(types.ContextBudget{
		MaxTotalTokens:  4000,
		SoulIdentityPct: 20,
		SkillsPct:       5,
		MemoriesPct:     30,
		HistoryPct:      45,
	})

	if cb.MaxTotalTokens != 4000 {
		t.Errorf("MaxTotalTokens = %d, want 4000", cb.MaxTotalTokens)
	}
	if cb.SoulIdentityPct != 20 {
		t.Errorf("SoulIdentityPct = %d, want 20", cb.SoulIdentityPct)
	}
}

func TestTruncateText(t *testing.T) {
	short := "hello"
	if got := truncateText(short, 100); got != short {
		t.Errorf("truncateText(%q, 100) = %q, want %q", short, got, short)
	}

	long := strings.Repeat("word ", 200) // 1000 chars
	result := truncateText(long, 10)     // 10 tokens = 40 chars
	if len(result) > 40 {
		t.Errorf("truncateText result len = %d, want <= 40", len(result))
	}
}

func TestAssemble_MemoryFallbackWithoutEmbeddings(t *testing.T) {
	ms := &mockMemoryStore{
		memories: []memory.Memory{
			{ID: 1, AgentID: "test-agent", Category: "fact", Content: "User likes Go", Pinned: false},
			{ID: 2, AgentID: "test-agent", Category: "instruction", Content: "Be concise", Pinned: false},
		},
	}

	a := New(ms, nil)
	config := types.AgentConfig{
		ID:          "test-agent",
		SoulContent: "Short soul.",
	}

	// No EmbeddingProvider — should fall back to ListRecent
	result, err := a.Assemble(context.Background(), config, "hello", AssembleOptions{})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	// Memories should appear in the system prompt via recency fallback
	if !strings.Contains(result.SystemPrompt, "User likes Go") {
		t.Error("expected recency-fallback memory 'User likes Go' in system prompt")
	}
	if !strings.Contains(result.SystemPrompt, "Be concise") {
		t.Error("expected recency-fallback memory 'Be concise' in system prompt")
	}
}

func TestFormatMemoryBlock(t *testing.T) {
	entries := []memEntry{
		{category: "fact", content: "The sky is blue."},
		{category: "decision", content: "We use Go."},
	}

	block := formatMemoryBlock(entries)
	if !strings.Contains(block, "## Memories") {
		t.Error("block should contain ## Memories header")
	}
	if !strings.Contains(block, "[fact] The sky is blue.") {
		t.Error("block should contain fact memory")
	}
	if !strings.Contains(block, "[decision] We use Go.") {
		t.Error("block should contain decision memory")
	}
}

// --- Skills provider tests ---

func TestAssemble_SkillsInjected(t *testing.T) {
	ms := &mockMemoryStore{
		memories: []memory.Memory{
			{ID: 1, AgentID: "test-agent", Category: "fact", Content: "User likes Go", Pinned: false},
		},
	}
	sp := &mockSkillsProvider{content: "### web-search\nYou are a web search assistant."}

	a := New(ms, nil)
	a.SetSkillsProvider(sp)

	config := types.AgentConfig{
		ID:          "test-agent",
		SoulContent: "You are helpful.",
	}

	result, err := a.Assemble(context.Background(), config, "hello", AssembleOptions{})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	// Skills section should be present
	if !strings.Contains(result.SystemPrompt, "## Active Skills") {
		t.Error("system prompt should contain '## Active Skills' header")
	}
	if !strings.Contains(result.SystemPrompt, "web-search") {
		t.Error("system prompt should contain skill content")
	}

	// Verify ordering: soul before skills before memories
	soulIdx := strings.Index(result.SystemPrompt, "You are helpful.")
	skillsIdx := strings.Index(result.SystemPrompt, "## Active Skills")
	memIdx := strings.Index(result.SystemPrompt, "## Memories")

	if soulIdx < 0 || skillsIdx < 0 || memIdx < 0 {
		t.Fatalf("missing expected sections: soul=%d, skills=%d, mem=%d", soulIdx, skillsIdx, memIdx)
	}
	if soulIdx >= skillsIdx {
		t.Errorf("soul (%d) should appear before skills (%d)", soulIdx, skillsIdx)
	}
	if skillsIdx >= memIdx {
		t.Errorf("skills (%d) should appear before memories (%d)", skillsIdx, memIdx)
	}
}

func TestAssemble_TeamContextInjectedBeforeSkills(t *testing.T) {
	sp := &mockSkillsProvider{content: "### web-search\nYou are a web search assistant."}
	tp := &mockTeamContextProvider{content: "Team mission: Reduce incident response time."}

	a := New(nil, nil)
	a.SetTeamContextProvider(tp)
	a.SetSkillsProvider(sp)

	config := types.AgentConfig{
		ID:          "test-agent",
		SoulContent: "You are helpful.",
	}

	result, err := a.Assemble(context.Background(), config, "hello", AssembleOptions{})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	if !strings.Contains(result.SystemPrompt, "## Team Context") {
		t.Error("system prompt should contain '## Team Context' header")
	}
	if !strings.Contains(result.SystemPrompt, "## Active Skills") {
		t.Error("system prompt should contain '## Active Skills' header")
	}

	soulIdx := strings.Index(result.SystemPrompt, "You are helpful.")
	teamIdx := strings.Index(result.SystemPrompt, "## Team Context")
	skillsIdx := strings.Index(result.SystemPrompt, "## Active Skills")

	if soulIdx < 0 || teamIdx < 0 || skillsIdx < 0 {
		t.Fatalf("missing expected sections: soul=%d, team=%d, skills=%d", soulIdx, teamIdx, skillsIdx)
	}
	if soulIdx >= teamIdx {
		t.Errorf("soul (%d) should appear before team context (%d)", soulIdx, teamIdx)
	}
	if teamIdx >= skillsIdx {
		t.Errorf("team context (%d) should appear before skills (%d)", teamIdx, skillsIdx)
	}
}

func TestAssemble_NilSkillsProvider(t *testing.T) {
	a := New(nil, nil)
	// No SetSkillsProvider call — backward compatible

	config := types.AgentConfig{
		ID:          "test-agent",
		SoulContent: "You are helpful.",
	}

	result, err := a.Assemble(context.Background(), config, "hello", AssembleOptions{})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	if strings.Contains(result.SystemPrompt, "## Active Skills") {
		t.Error("system prompt should not contain '## Active Skills' when provider is nil")
	}
}

func TestAssemble_EmptySkillsContent(t *testing.T) {
	sp := &mockSkillsProvider{content: ""}

	a := New(nil, nil)
	a.SetSkillsProvider(sp)

	config := types.AgentConfig{
		ID:          "test-agent",
		SoulContent: "You are helpful.",
	}

	result, err := a.Assemble(context.Background(), config, "hello", AssembleOptions{})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	if strings.Contains(result.SystemPrompt, "## Active Skills") {
		t.Error("system prompt should not contain '## Active Skills' when content is empty")
	}
}

func TestAssemble_SkillsProviderError(t *testing.T) {
	sp := &mockSkillsProvider{err: errors.New("database connection lost")}

	a := New(nil, nil)
	a.SetSkillsProvider(sp)

	config := types.AgentConfig{
		ID:          "test-agent",
		SoulContent: "You are helpful.",
	}

	result, err := a.Assemble(context.Background(), config, "hello", AssembleOptions{})
	if err != nil {
		t.Fatalf("Assemble should succeed despite provider error: %v", err)
	}

	if strings.Contains(result.SystemPrompt, "## Active Skills") {
		t.Error("system prompt should not contain skills when provider errors")
	}
	if !strings.Contains(result.SystemPrompt, "You are helpful.") {
		t.Error("soul content should still be present after provider error")
	}
}

func TestAssemble_SkillsBudgetTruncation(t *testing.T) {
	// Generate long skills content that exceeds the budget
	longContent := strings.Repeat("This is a very detailed skill instruction. ", 500)
	sp := &mockSkillsProvider{content: longContent}

	a := New(nil, nil)
	a.SetSkillsProvider(sp)

	config := types.AgentConfig{
		ID:          "test-agent",
		SoulContent: "Short soul.",
		ContextBudget: types.ContextBudget{
			MaxTotalTokens:  1000,
			SoulIdentityPct: 10,
			SkillsPct:       5, // 50 tokens = ~200 chars
			MemoriesPct:     25,
			HistoryPct:      50,
		},
	}

	result, err := a.Assemble(context.Background(), config, "hello", AssembleOptions{})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	// Skills section should be present but truncated
	if !strings.Contains(result.SystemPrompt, "## Active Skills") {
		t.Error("truncated skills should still contain header")
	}

	// Extract the skills section and verify it fits the budget
	skillsStart := strings.Index(result.SystemPrompt, "\n\n## Active Skills\n")
	if skillsStart < 0 {
		t.Fatal("could not find skills section")
	}
	skillsSection := result.SystemPrompt[skillsStart:]
	// If memories follow, trim them off
	if memIdx := strings.Index(skillsSection[1:], "\n\n##"); memIdx >= 0 {
		skillsSection = skillsSection[:memIdx+1]
	}

	skillsTokens := EstimateTokens(skillsSection)
	skillsBudget := 1000 * 5 / 100 // 50 tokens
	if skillsTokens > skillsBudget {
		t.Errorf("skills tokens = %d, exceeds budget %d", skillsTokens, skillsBudget)
	}
}

// --- sanitizeToolMessages tests ---

func TestSanitizeToolMessages_ValidPair(t *testing.T) {
	messages := []models.ChatMessage{
		{Role: "user", Content: "Use the file tool"},
		{Role: "assistant", Content: "I'll list files.", ToolCalls: []models.ToolUse{
			{ID: "tc_001", Name: "file.list"},
		}},
		{Role: "tool", Content: `{"files":["a.txt"]}`, ToolCallID: "tc_001"},
		{Role: "assistant", Content: "Found 1 file."},
	}

	result := sanitizeToolMessages(messages)

	if len(result) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(result))
	}
	if len(result[1].ToolCalls) != 1 {
		t.Error("valid tool calls should be preserved")
	}
	if result[2].Role != "tool" || result[2].ToolCallID != "tc_001" {
		t.Error("valid tool result should be preserved")
	}
}

func TestSanitizeToolMessages_IncompleteToolPair(t *testing.T) {
	// Assistant has tool calls for A and B, but only A has a result.
	messages := []models.ChatMessage{
		{Role: "user", Content: "Do two things"},
		{Role: "assistant", Content: "I'll do both.", ToolCalls: []models.ToolUse{
			{ID: "tc_A", Name: "tool_a"},
			{ID: "tc_B", Name: "tool_b"},
		}},
		{Role: "tool", Content: "result A", ToolCallID: "tc_A"},
		// tc_B is missing
		{Role: "assistant", Content: "Done."},
	}

	result := sanitizeToolMessages(messages)

	// The incomplete assistant+tool pair should be stripped.
	// The assistant content "I'll do both." should be preserved as plain text.
	// The orphan tool result for tc_A should be dropped.
	// The final assistant should remain.
	for _, m := range result {
		if m.Role == "tool" {
			t.Error("orphaned tool result from incomplete pair should be removed")
		}
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			t.Error("incomplete ToolCalls should be stripped from assistant")
		}
	}

	// Should have: user, assistant("I'll do both."), assistant("Done.")
	if len(result) != 3 {
		t.Fatalf("expected 3 messages, got %d: %+v", len(result), result)
	}
}

func TestSanitizeToolMessages_OrphanedToolResult(t *testing.T) {
	// A tool result with no preceding assistant that has matching ToolCalls.
	messages := []models.ChatMessage{
		{Role: "user", Content: "Hello"},
		{Role: "tool", Content: "stale result", ToolCallID: "orphan_id"},
		{Role: "assistant", Content: "Hi there."},
	}

	result := sanitizeToolMessages(messages)

	for _, m := range result {
		if m.Role == "tool" {
			t.Error("orphaned tool message should be removed")
		}
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 messages (user + assistant), got %d", len(result))
	}
}

func TestSanitizeToolMessages_MultipleToolCalls(t *testing.T) {
	// Assistant calls 3 tools, all 3 results present.
	messages := []models.ChatMessage{
		{Role: "user", Content: "Do three things"},
		{Role: "assistant", Content: "", ToolCalls: []models.ToolUse{
			{ID: "tc_1", Name: "tool_a"},
			{ID: "tc_2", Name: "tool_b"},
			{ID: "tc_3", Name: "tool_c"},
		}},
		{Role: "tool", Content: "result 1", ToolCallID: "tc_1"},
		{Role: "tool", Content: "result 2", ToolCallID: "tc_2"},
		{Role: "tool", Content: "result 3", ToolCallID: "tc_3"},
		{Role: "assistant", Content: "All done."},
	}

	result := sanitizeToolMessages(messages)

	if len(result) != 6 {
		t.Fatalf("expected 6 messages, got %d", len(result))
	}
	if len(result[1].ToolCalls) != 3 {
		t.Error("all 3 tool calls should be preserved")
	}
}

func TestSanitizeToolMessages_EmptyAssistantStripped(t *testing.T) {
	// Assistant has tool calls but no content, and tool results are missing.
	// The empty assistant should be removed entirely.
	messages := []models.ChatMessage{
		{Role: "user", Content: "Do something"},
		{Role: "assistant", Content: "", ToolCalls: []models.ToolUse{
			{ID: "tc_missing", Name: "tool_x"},
		}},
		// No tool results
		{Role: "assistant", Content: "Fallback."},
	}

	result := sanitizeToolMessages(messages)

	// Empty assistant with stripped ToolCalls should be removed.
	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d: %+v", len(result), result)
	}
	if result[0].Role != "user" || result[1].Role != "assistant" {
		t.Error("expected user + assistant(Fallback)")
	}
}
