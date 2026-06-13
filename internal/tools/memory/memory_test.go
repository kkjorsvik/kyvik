package memory

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/ktp"
	"github.com/kkjorsvik/kyvik/internal/memory"
)

// mockMemoryStore implements memory.MemoryStore for testing.
type mockMemoryStore struct {
	mu      sync.Mutex
	entries map[int64]memory.Memory
	nextID  int64
}

func newMockStore() *mockMemoryStore {
	return &mockMemoryStore{entries: make(map[int64]memory.Memory), nextID: 1}
}

func (m *mockMemoryStore) Create(_ context.Context, mem memory.Memory) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mem.ID = m.nextID
	m.nextID++
	m.entries[mem.ID] = mem
	return mem.ID, nil
}

func (m *mockMemoryStore) Get(_ context.Context, id int64) (memory.Memory, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mem, ok := m.entries[id]
	if !ok {
		return memory.Memory{}, fmt.Errorf("not found")
	}
	return mem, nil
}

func (m *mockMemoryStore) Update(_ context.Context, mem memory.Memory) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.entries[mem.ID]; !ok {
		return fmt.Errorf("not found")
	}
	m.entries[mem.ID] = mem
	return nil
}

func (m *mockMemoryStore) Delete(_ context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.entries[id]; !ok {
		return fmt.Errorf("not found")
	}
	delete(m.entries, id)
	return nil
}

func (m *mockMemoryStore) List(_ context.Context, agentID string, opts memory.ListOptions) ([]memory.Memory, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []memory.Memory
	for _, mem := range m.entries {
		if mem.AgentID != agentID {
			continue
		}
		if opts.Category != "" && mem.Category != opts.Category {
			continue
		}
		result = append(result, mem)
	}

	// Apply offset.
	if opts.Offset > 0 && opts.Offset < len(result) {
		result = result[opts.Offset:]
	} else if opts.Offset >= len(result) {
		return nil, nil
	}

	// Apply limit.
	if opts.Limit > 0 && len(result) > opts.Limit {
		result = result[:opts.Limit]
	}
	return result, nil
}

func (m *mockMemoryStore) CountFiltered(_ context.Context, agentID string, opts memory.ListOptions) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, mem := range m.entries {
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

func (m *mockMemoryStore) CreateFromAgent(_ context.Context, agentID, category, content string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mem := memory.Memory{
		ID:        m.nextID,
		AgentID:   agentID,
		Category:  category,
		Content:   content,
		Source:    memory.SourceAgent,
		CreatedAt: time.Now(),
	}
	m.nextID++
	m.entries[mem.ID] = mem
	return mem.ID, nil
}

// Unused interface methods — minimal stubs.
func (m *mockMemoryStore) ListPinned(context.Context, string) ([]memory.Memory, error) {
	return nil, nil
}
func (m *mockMemoryStore) Touch(context.Context, int64) error                           { return nil }
func (m *mockMemoryStore) SetEmbedding(context.Context, int64, []float32, string) error { return nil }
func (m *mockMemoryStore) ListWithEmbeddings(context.Context, string) ([]memory.Memory, error) {
	return nil, nil
}
func (m *mockMemoryStore) GetUnembedded(context.Context, string) ([]memory.Memory, error) {
	return nil, nil
}
func (m *mockMemoryStore) GetAllUnembedded(context.Context, int) ([]memory.Memory, error) {
	return nil, nil
}
func (m *mockMemoryStore) ListRecent(context.Context, string, int) ([]memory.Memory, error) {
	return nil, nil
}
func (m *mockMemoryStore) Count(context.Context, string) (int, error)  { return 0, nil }
func (m *mockMemoryStore) DeleteByAgent(context.Context, string) error { return nil }
func (m *mockMemoryStore) Import(context.Context, string, []memory.Memory) (int, error) {
	return 0, nil
}
func (m *mockMemoryStore) Archive(context.Context, int64) error   { return nil }
func (m *mockMemoryStore) Unarchive(context.Context, int64) error { return nil }
func (m *mockMemoryStore) ArchiveStale(context.Context, string, time.Duration) (int, error) {
	return 0, nil
}

func (m *mockMemoryStore) ListCandidates(_ context.Context, agentID string) ([]memory.Memory, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []memory.Memory
	for _, mem := range m.entries {
		if mem.AgentID == agentID && mem.Status == memory.StatusCandidate {
			result = append(result, mem)
		}
	}
	return result, nil
}

func (m *mockMemoryStore) CountCandidates(_ context.Context, agentID string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, mem := range m.entries {
		if mem.AgentID == agentID && mem.Status == memory.StatusCandidate {
			count++
		}
	}
	return count, nil
}

func (m *mockMemoryStore) PromoteCandidate(_ context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	mem, ok := m.entries[id]
	if !ok {
		return fmt.Errorf("not found")
	}
	mem.Status = memory.StatusActive
	m.entries[id] = mem
	return nil
}

func (m *mockMemoryStore) RejectCandidate(_ context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.entries[id]; !ok {
		return fmt.Errorf("not found")
	}
	delete(m.entries, id)
	return nil
}

func (m *mockMemoryStore) EnforceCapAndStore(_ context.Context, mem memory.Memory, _ int) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mem.ID = m.nextID
	m.nextID++
	m.entries[mem.ID] = mem
	return mem.ID, nil
}

func makeReq(action string, params map[string]any) ktp.ToolRequest {
	return ktp.NewToolRequest("test-agent", "memory", action, params)
}

func TestMemoryTool_Remember(t *testing.T) {
	store := newMockStore()
	tool := New(store)

	resp, err := tool.Execute(context.Background(), makeReq("remember", map[string]any{
		"content":  "the sky is blue",
		"category": "fact",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	result := resp.Result.(map[string]any)
	id := result["id"].(int64)
	if id <= 0 {
		t.Error("expected positive ID")
	}
}

func TestMemoryTool_RememberPinned(t *testing.T) {
	store := newMockStore()
	tool := New(store)

	resp, err := tool.Execute(context.Background(), makeReq("remember", map[string]any{
		"content": "important fact",
		"pinned":  true,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	result := resp.Result.(map[string]any)
	id := result["id"].(int64)

	// Verify pinned flag was set.
	mem, err := store.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !mem.Pinned {
		t.Error("expected memory to be pinned")
	}
}

func TestMemoryTool_Recall(t *testing.T) {
	store := newMockStore()
	tool := New(store)

	// Create some memories.
	_, _ = store.CreateFromAgent(context.Background(), "test-agent", "fact", "fact one")
	_, _ = store.CreateFromAgent(context.Background(), "test-agent", "decision", "decision one")

	resp, err := tool.Execute(context.Background(), makeReq("recall", map[string]any{
		"category": "fact",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	result := resp.Result.(map[string]any)
	memories := result["memories"].([]map[string]any)
	if len(memories) != 1 {
		t.Errorf("expected 1 fact memory, got %d", len(memories))
	}
}

func TestMemoryTool_Forget(t *testing.T) {
	store := newMockStore()
	tool := New(store)

	id, _ := store.CreateFromAgent(context.Background(), "test-agent", "fact", "forget me")

	resp, err := tool.Execute(context.Background(), makeReq("forget", map[string]any{
		"id": id,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	// Verify deleted.
	if _, err := store.Get(context.Background(), id); err == nil {
		t.Error("expected memory to be deleted")
	}
}

func TestMemoryTool_ForgetWrongAgent(t *testing.T) {
	store := newMockStore()
	tool := New(store)

	// Create memory for a different agent.
	id, _ := store.CreateFromAgent(context.Background(), "other-agent", "fact", "not yours")

	req := ktp.NewToolRequest("test-agent", "memory", "forget", map[string]any{"id": id})
	resp, err := tool.Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Error("expected failure when deleting another agent's memory")
	}

	// Verify not deleted.
	if _, err := store.Get(context.Background(), id); err != nil {
		t.Error("memory should not have been deleted")
	}
}

func TestMemoryTool_List(t *testing.T) {
	store := newMockStore()
	tool := New(store)

	// Create several memories.
	for i := 0; i < 5; i++ {
		_, _ = store.CreateFromAgent(context.Background(), "test-agent", "fact", fmt.Sprintf("memory %d", i))
	}

	resp, err := tool.Execute(context.Background(), makeReq("list", map[string]any{
		"limit": 3,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	result := resp.Result.(map[string]any)
	memories := result["memories"].([]map[string]any)
	if len(memories) != 3 {
		t.Errorf("expected 3 memories with limit, got %d", len(memories))
	}
}

func TestMemoryTool_Declaration(t *testing.T) {
	tool := New(newMockStore())
	decl := tool.Declaration()

	if decl.Name != "memory" {
		t.Errorf("expected name memory, got %s", decl.Name)
	}
	if decl.Version != "1.0.0" {
		t.Errorf("expected version 1.0.0, got %s", decl.Version)
	}
	if decl.MinTier != ktp.TierReader {
		t.Errorf("expected min tier reader, got %s", decl.MinTier)
	}
	if len(decl.Actions) != 7 {
		t.Errorf("expected 7 actions, got %d", len(decl.Actions))
	}
	if err := decl.Validate(); err != nil {
		t.Errorf("declaration validation failed: %v", err)
	}

	// Verify destructive flags.
	for _, a := range decl.Actions {
		if a.Name == "forget" && !a.Destructive {
			t.Error("forget action should be destructive")
		}
		if a.Name == "remember" && a.Destructive {
			t.Error("remember action should not be destructive")
		}
	}
}
