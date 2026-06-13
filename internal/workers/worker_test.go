package workers

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/memory"
	"github.com/kkjorsvik/kyvik/internal/models"
	"github.com/kkjorsvik/kyvik/internal/router"
	"github.com/kkjorsvik/kyvik/internal/spending"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// --- Mock provider ---

type mockProvider struct {
	name     string
	delay    time.Duration
	response *models.CompletionResponse
	err      error
}

func (m *mockProvider) Complete(ctx context.Context, _ models.CompletionRequest) (*models.CompletionResponse, error) {
	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if m.err != nil {
		return nil, m.err
	}
	if m.response != nil {
		return m.response, nil
	}
	return &models.CompletionResponse{Content: "worker result", TokensIn: 10, TokensOut: 20, Cost: 0.001}, nil
}

func (m *mockProvider) Stream(_ context.Context, _ models.CompletionRequest) (<-chan models.StreamChunk, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockProvider) ListModels(_ context.Context) ([]models.ModelInfo, error) {
	return nil, nil
}

func (m *mockProvider) Name() string { return m.name }

// --- Mock spending tracker ---

type mockTracker struct {
	mu           sync.Mutex
	withinBudget bool
	records      []recordCall
}

type recordCall struct {
	AgentID       string
	TokensIn      int64
	TokensOut     int64
	Cost          float64
	ParentAgentID string
}

func newMockTracker(withinBudget bool) *mockTracker {
	return &mockTracker{withinBudget: withinBudget}
}

func (m *mockTracker) Record(_ context.Context, agentID string, tokensIn, tokensOut int64, cost float64, opts spending.RecordOptions) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records = append(m.records, recordCall{
		AgentID:       agentID,
		TokensIn:      tokensIn,
		TokensOut:     tokensOut,
		Cost:          cost,
		ParentAgentID: opts.ParentAgentID,
	})
	return nil
}

func (m *mockTracker) CheckBudget(_ context.Context, _ string) (*spending.BudgetStatus, error) {
	return &spending.BudgetStatus{WithinBudget: m.withinBudget}, nil
}

func (m *mockTracker) GetSummary(_ context.Context, _ spending.Filter) (*spending.Summary, error) {
	return &spending.Summary{}, nil
}

func (m *mockTracker) GetSlotBreakdown(_ context.Context, _, _ string) ([]spending.SlotUsageSummary, error) {
	return nil, nil
}

func (m *mockTracker) GetProviderBreakdown(_ context.Context, _, _ string) ([]spending.ProviderUsageSummary, error) {
	return nil, nil
}

func (m *mockTracker) SetGlobalLimit(_ context.Context, _ types.SpendingLimits) error { return nil }
func (m *mockTracker) SetAgentLimit(_ context.Context, _ string, _ types.SpendingLimits) error {
	return nil
}
func (m *mockTracker) GetDailyTimeSeries(_ context.Context, _ string, _ int) ([]spending.DailyUsage, error) {
	return nil, nil
}

// --- Mock memory store ---

type mockMemoryStore struct {
	memories []memory.Memory
}

func (m *mockMemoryStore) Create(_ context.Context, _ memory.Memory) (int64, error) { return 0, nil }
func (m *mockMemoryStore) Get(_ context.Context, _ int64) (memory.Memory, error) {
	return memory.Memory{}, nil
}
func (m *mockMemoryStore) Update(_ context.Context, _ memory.Memory) error { return nil }
func (m *mockMemoryStore) Delete(_ context.Context, _ int64) error         { return nil }
func (m *mockMemoryStore) List(_ context.Context, _ string, _ memory.ListOptions) ([]memory.Memory, error) {
	return nil, nil
}
func (m *mockMemoryStore) CountFiltered(_ context.Context, _ string, _ memory.ListOptions) (int, error) {
	return 0, nil
}
func (m *mockMemoryStore) ListPinned(_ context.Context, _ string) ([]memory.Memory, error) {
	return nil, nil
}
func (m *mockMemoryStore) ListRecent(_ context.Context, _ string, limit int) ([]memory.Memory, error) {
	if len(m.memories) > limit {
		return m.memories[:limit], nil
	}
	return m.memories, nil
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

// --- Helpers ---

func testParent(enabled bool) types.AgentConfig {
	return types.AgentConfig{
		ID:              "parent-1",
		Name:            "Test Parent",
		SoulContent:     "You are helpful.",
		IdentityContent: "You are a researcher.",
		ModelConfig:     types.ModelConfig{Provider: "test", Model: "test-model"},
		Workers: types.WorkerConfig{
			Enabled:       enabled,
			MaxConcurrent: 3,
			TTLSeconds:    300,
			ModelSlot:     "fast",
		},
		ModelSlotsJSON:    `[{"name":"default","provider":"test","model":"test-model"},{"name":"fast","provider":"test","model":"fast-model"}]`,
		RoutingConfigJSON: `{"default_slot":"default"}`,
	}
}

func testManager(provider models.Provider, tracker spending.Tracker, mem memory.MemoryStore) *WorkerManager {
	providers := map[string]models.Provider{"test": provider}
	registry := router.NewProviderRegistry(func() map[string]models.Provider { return providers })
	wm := NewWorkerManager(registry, tracker, mem)
	wm.Start(context.Background())
	return wm
}

// --- Tests ---

func TestSpawn_Success(t *testing.T) {
	prov := &mockProvider{name: "test"}
	tracker := newMockTracker(true)
	wm := testManager(prov, tracker, nil)
	defer wm.Stop()

	parent := testParent(true)
	w, err := wm.Spawn(context.Background(), parent, "summarize this document")
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}
	if w.Status != "running" {
		t.Errorf("expected status 'running', got %q", w.Status)
	}
	if w.ParentID != "parent-1" {
		t.Errorf("expected parent ID 'parent-1', got %q", w.ParentID)
	}

	result, err := wm.Wait(context.Background(), w.ID)
	if err != nil {
		t.Fatalf("Wait failed: %v", err)
	}
	if result.Status != "completed" {
		t.Errorf("expected status 'completed', got %q", result.Status)
	}
	if result.Result != "worker result" {
		t.Errorf("expected result 'worker result', got %q", result.Result)
	}
}

func TestSpawn_Disabled(t *testing.T) {
	prov := &mockProvider{name: "test"}
	wm := testManager(prov, newMockTracker(true), nil)
	defer wm.Stop()

	parent := testParent(false) // disabled
	_, err := wm.Spawn(context.Background(), parent, "task")
	if err != types.ErrWorkersDisabled {
		t.Errorf("expected ErrWorkersDisabled, got %v", err)
	}
}

func TestSpawn_MaxConcurrent(t *testing.T) {
	prov := &mockProvider{name: "test", delay: 5 * time.Second}
	wm := testManager(prov, newMockTracker(true), nil)
	defer wm.Stop()

	parent := testParent(true)
	parent.Workers.MaxConcurrent = 2

	// Spawn 2 workers (they'll be slow)
	_, err := wm.Spawn(context.Background(), parent, "task 1")
	if err != nil {
		t.Fatalf("Spawn 1 failed: %v", err)
	}
	_, err = wm.Spawn(context.Background(), parent, "task 2")
	if err != nil {
		t.Fatalf("Spawn 2 failed: %v", err)
	}

	// Third should fail
	_, err = wm.Spawn(context.Background(), parent, "task 3")
	if err != types.ErrWorkerLimitReached {
		t.Errorf("expected ErrWorkerLimitReached, got %v", err)
	}
}

func TestSpawn_BudgetExceeded(t *testing.T) {
	prov := &mockProvider{name: "test"}
	tracker := newMockTracker(false) // budget exceeded
	wm := testManager(prov, tracker, nil)
	defer wm.Stop()

	parent := testParent(true)
	_, err := wm.Spawn(context.Background(), parent, "task")
	if err != types.ErrBudgetExceeded {
		t.Errorf("expected ErrBudgetExceeded, got %v", err)
	}
}

func TestSpawn_TTLTimeout(t *testing.T) {
	prov := &mockProvider{name: "test", delay: 2 * time.Second}
	wm := testManager(prov, newMockTracker(true), nil)
	defer wm.Stop()

	parent := testParent(true)
	parent.Workers.TTLSeconds = 1 // 1 second TTL

	w, err := wm.Spawn(context.Background(), parent, "slow task")
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}

	result, err := wm.Wait(context.Background(), w.ID)
	if err != nil {
		t.Fatalf("Wait failed: %v", err)
	}
	if result.Status != "timeout" {
		t.Errorf("expected status 'timeout', got %q", result.Status)
	}
}

func TestCancel(t *testing.T) {
	prov := &mockProvider{name: "test", delay: 10 * time.Second}
	wm := testManager(prov, newMockTracker(true), nil)
	defer wm.Stop()

	parent := testParent(true)
	w, err := wm.Spawn(context.Background(), parent, "cancellable task")
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}

	// Cancel it
	if err := wm.Cancel(context.Background(), w.ID); err != nil {
		t.Fatalf("Cancel failed: %v", err)
	}

	result, err := wm.Wait(context.Background(), w.ID)
	if err != nil {
		t.Fatalf("Wait failed: %v", err)
	}
	if result.Status != "timeout" {
		t.Errorf("expected status 'timeout' after cancel, got %q", result.Status)
	}
}

func TestActiveCount(t *testing.T) {
	prov := &mockProvider{name: "test", delay: 5 * time.Second}
	wm := testManager(prov, newMockTracker(true), nil)
	defer wm.Stop()

	parent := testParent(true)

	if count := wm.ActiveCount("parent-1"); count != 0 {
		t.Errorf("expected 0 active, got %d", count)
	}

	_, _ = wm.Spawn(context.Background(), parent, "task 1")
	_, _ = wm.Spawn(context.Background(), parent, "task 2")

	if count := wm.ActiveCount("parent-1"); count != 2 {
		t.Errorf("expected 2 active, got %d", count)
	}
}

func TestActiveWorkers(t *testing.T) {
	prov := &mockProvider{name: "test", delay: 5 * time.Second}
	wm := testManager(prov, newMockTracker(true), nil)
	defer wm.Stop()

	parent := testParent(true)
	_, _ = wm.Spawn(context.Background(), parent, "task 1")
	_, _ = wm.Spawn(context.Background(), parent, "task 2")

	workers := wm.ActiveWorkers("parent-1")
	if len(workers) != 2 {
		t.Errorf("expected 2 workers, got %d", len(workers))
	}

	// Check different parent returns empty
	otherWorkers := wm.ActiveWorkers("other-parent")
	if len(otherWorkers) != 0 {
		t.Errorf("expected 0 workers for other parent, got %d", len(otherWorkers))
	}
}

func TestCleanup(t *testing.T) {
	prov := &mockProvider{name: "test"} // instant completion
	wm := testManager(prov, newMockTracker(true), nil)
	defer wm.Stop()

	parent := testParent(true)
	w, err := wm.Spawn(context.Background(), parent, "task")
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}

	// Wait for completion
	_, _ = wm.Wait(context.Background(), w.ID)

	// Worker should still be in active map (within retention)
	workers := wm.ActiveWorkers("parent-1")
	if len(workers) != 1 {
		t.Fatalf("expected 1 worker in active map, got %d", len(workers))
	}

	// Manually backdate completedAt to trigger cleanup
	rw := wm.findWorker(w.ID)
	rw.mu.Lock()
	rw.worker.CompletedAt = time.Now().Add(-10 * time.Minute)
	rw.mu.Unlock()

	wm.cleanup()

	workers = wm.ActiveWorkers("parent-1")
	if len(workers) != 0 {
		t.Errorf("expected 0 workers after cleanup, got %d", len(workers))
	}
}

func TestSpawn_CostAttribution(t *testing.T) {
	prov := &mockProvider{name: "test"}
	tracker := newMockTracker(true)
	wm := testManager(prov, tracker, nil)
	defer wm.Stop()

	parent := testParent(true)
	w, err := wm.Spawn(context.Background(), parent, "attributed task")
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}

	_, _ = wm.Wait(context.Background(), w.ID)

	// Check that Record was called with ParentAgentID
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if len(tracker.records) == 0 {
		t.Fatal("expected at least one spending record")
	}
	rec := tracker.records[0]
	if rec.ParentAgentID != "parent-1" {
		t.Errorf("expected ParentAgentID 'parent-1', got %q", rec.ParentAgentID)
	}
	if rec.AgentID != "parent-1" {
		t.Errorf("expected AgentID 'parent-1', got %q", rec.AgentID)
	}
}

func TestSpawn_SlotFallback(t *testing.T) {
	prov := &mockProvider{name: "test"}
	wm := testManager(prov, newMockTracker(true), nil)
	defer wm.Stop()

	parent := testParent(true)
	parent.Workers.ModelSlot = "nonexistent" // no such slot

	w, err := wm.Spawn(context.Background(), parent, "fallback task")
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}

	result, _ := wm.Wait(context.Background(), w.ID)
	if result.Status != "completed" {
		t.Errorf("expected completed with fallback slot, got %q", result.Status)
	}
	// Should have used the default slot
	if result.Model.Name != "default" {
		t.Errorf("expected default slot fallback, got %q", result.Model.Name)
	}
}

func TestSpawn_NilMemory(t *testing.T) {
	prov := &mockProvider{name: "test"}
	wm := testManager(prov, newMockTracker(true), nil) // nil memory
	defer wm.Stop()

	parent := testParent(true)
	w, err := wm.Spawn(context.Background(), parent, "no memory task")
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}

	result, _ := wm.Wait(context.Background(), w.ID)
	if result.Status != "completed" {
		t.Errorf("expected completed with nil memory, got %q", result.Status)
	}
}
