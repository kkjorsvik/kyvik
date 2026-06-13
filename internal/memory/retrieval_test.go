package memory

import (
	"context"
	"math"
	"testing"
	"time"
)

// mockMemoryStore implements MemoryStore for testing.
type mockMemoryStore struct {
	memories []Memory
	touched  []int64
}

func (m *mockMemoryStore) Create(_ context.Context, mem Memory) (int64, error) { return 0, nil }
func (m *mockMemoryStore) Get(_ context.Context, id int64) (Memory, error)     { return Memory{}, nil }
func (m *mockMemoryStore) Update(_ context.Context, _ Memory) error            { return nil }
func (m *mockMemoryStore) Delete(_ context.Context, _ int64) error             { return nil }
func (m *mockMemoryStore) List(_ context.Context, _ string, _ ListOptions) ([]Memory, error) {
	return m.memories, nil
}
func (m *mockMemoryStore) CountFiltered(_ context.Context, _ string, _ ListOptions) (int, error) {
	return len(m.memories), nil
}
func (m *mockMemoryStore) ListPinned(_ context.Context, _ string) ([]Memory, error) {
	var pinned []Memory
	for _, mem := range m.memories {
		if mem.Pinned {
			pinned = append(pinned, mem)
		}
	}
	return pinned, nil
}
func (m *mockMemoryStore) Touch(_ context.Context, id int64) error {
	m.touched = append(m.touched, id)
	return nil
}
func (m *mockMemoryStore) SetEmbedding(_ context.Context, _ int64, _ []float32, _ string) error {
	return nil
}
func (m *mockMemoryStore) GetUnembedded(_ context.Context, _ string) ([]Memory, error) {
	return nil, nil
}
func (m *mockMemoryStore) GetAllUnembedded(_ context.Context, _ int) ([]Memory, error) {
	return nil, nil
}
func (m *mockMemoryStore) Count(_ context.Context, _ string) (int, error)  { return 0, nil }
func (m *mockMemoryStore) DeleteByAgent(_ context.Context, _ string) error { return nil }
func (m *mockMemoryStore) Import(_ context.Context, _ string, _ []Memory) (int, error) {
	return 0, nil
}
func (m *mockMemoryStore) CreateFromAgent(_ context.Context, _, _, _ string) (int64, error) {
	return 0, nil
}
func (m *mockMemoryStore) ListRecent(_ context.Context, agentID string, limit int) ([]Memory, error) {
	var result []Memory
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
func (m *mockMemoryStore) ListWithEmbeddings(_ context.Context, _ string) ([]Memory, error) {
	var result []Memory
	for _, mem := range m.memories {
		if len(mem.Embedding) > 0 {
			result = append(result, mem)
		}
	}
	return result, nil
}
func (m *mockMemoryStore) Archive(_ context.Context, _ int64) error   { return nil }
func (m *mockMemoryStore) Unarchive(_ context.Context, _ int64) error { return nil }
func (m *mockMemoryStore) ArchiveStale(_ context.Context, _ string, _ time.Duration) (int, error) {
	return 0, nil
}
func (m *mockMemoryStore) ListCandidates(_ context.Context, _ string) ([]Memory, error) {
	return nil, nil
}
func (m *mockMemoryStore) CountCandidates(_ context.Context, _ string) (int, error) { return 0, nil }
func (m *mockMemoryStore) PromoteCandidate(_ context.Context, _ int64) error        { return nil }
func (m *mockMemoryStore) RejectCandidate(_ context.Context, _ int64) error          { return nil }
func (m *mockMemoryStore) EnforceCapAndStore(_ context.Context, _ Memory, _ int) (int64, error) {
	return 0, nil
}

func TestRetriever_RanksRelevantHigher(t *testing.T) {
	now := time.Now()
	queryEmb := []float32{1, 0, 0}

	store := &mockMemoryStore{
		memories: []Memory{
			{
				ID: 1, AgentID: "a1", Category: CategoryFact,
				Content: "relevant", Embedding: []float32{0.9, 0.1, 0},
				CreatedAt: now.Add(-time.Hour), AccessedAt: now,
			},
			{
				ID: 2, AgentID: "a1", Category: CategoryFact,
				Content: "irrelevant", Embedding: []float32{0, 0, 1},
				CreatedAt: now.Add(-time.Hour), AccessedAt: now,
			},
		},
	}

	r := NewRetriever(store)
	scored, err := r.Retrieve(context.Background(), "a1", queryEmb, RetrieveOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(scored) != 2 {
		t.Fatalf("expected 2 results, got %d", len(scored))
	}
	if scored[0].ID != 1 {
		t.Errorf("expected memory 1 ranked first, got %d", scored[0].ID)
	}
	if scored[0].Score <= scored[1].Score {
		t.Errorf("expected first score > second: %f <= %f", scored[0].Score, scored[1].Score)
	}
}

func TestRetriever_PinnedBonus(t *testing.T) {
	now := time.Now()
	queryEmb := []float32{1, 0, 0}

	store := &mockMemoryStore{
		memories: []Memory{
			{
				ID: 1, AgentID: "a1", Category: CategoryFact,
				Content: "not pinned", Embedding: []float32{0.5, 0.5, 0},
				CreatedAt: now.Add(-time.Hour), AccessedAt: now,
			},
			{
				ID: 2, AgentID: "a1", Category: CategoryFact, Pinned: true,
				Content: "pinned", Embedding: []float32{0.5, 0.5, 0},
				CreatedAt: now.Add(-time.Hour), AccessedAt: now,
			},
		},
	}

	r := NewRetriever(store)
	scored, err := r.Retrieve(context.Background(), "a1", queryEmb, RetrieveOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(scored) != 2 {
		t.Fatalf("expected 2 results, got %d", len(scored))
	}
	// Pinned memory should rank higher with identical embeddings
	if scored[0].ID != 2 {
		t.Errorf("expected pinned memory (ID=2) ranked first, got %d", scored[0].ID)
	}
}

func TestRetriever_RespectsLimit(t *testing.T) {
	now := time.Now()
	queryEmb := []float32{1, 0, 0}

	store := &mockMemoryStore{
		memories: []Memory{
			{ID: 1, AgentID: "a1", Category: CategoryFact, Embedding: []float32{1, 0, 0}, CreatedAt: now, AccessedAt: now},
			{ID: 2, AgentID: "a1", Category: CategoryFact, Embedding: []float32{0.9, 0.1, 0}, CreatedAt: now, AccessedAt: now},
			{ID: 3, AgentID: "a1", Category: CategoryFact, Embedding: []float32{0.8, 0.2, 0}, CreatedAt: now, AccessedAt: now},
		},
	}

	r := NewRetriever(store)
	scored, err := r.Retrieve(context.Background(), "a1", queryEmb, RetrieveOptions{Limit: 2})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(scored) != 2 {
		t.Fatalf("expected 2 results with limit=2, got %d", len(scored))
	}
}

func TestRetriever_DefaultLimit(t *testing.T) {
	now := time.Now()
	queryEmb := []float32{1, 0, 0}

	store := &mockMemoryStore{
		memories: []Memory{
			{ID: 1, AgentID: "a1", Category: CategoryFact, Embedding: []float32{1, 0, 0}, CreatedAt: now, AccessedAt: now},
		},
	}

	r := NewRetriever(store)
	scored, err := r.Retrieve(context.Background(), "a1", queryEmb, RetrieveOptions{}) // Limit=0 → default 10
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(scored) != 1 {
		t.Fatalf("expected 1 result, got %d", len(scored))
	}
}

func TestRetriever_TouchesCalled(t *testing.T) {
	now := time.Now()
	queryEmb := []float32{1, 0, 0}

	store := &mockMemoryStore{
		memories: []Memory{
			{ID: 1, AgentID: "a1", Category: CategoryFact, Embedding: []float32{1, 0, 0}, CreatedAt: now, AccessedAt: now},
			{ID: 2, AgentID: "a1", Category: CategoryFact, Embedding: []float32{0.5, 0.5, 0}, CreatedAt: now, AccessedAt: now},
		},
	}

	r := NewRetriever(store)
	_, err := r.Retrieve(context.Background(), "a1", queryEmb, RetrieveOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(store.touched) != 2 {
		t.Errorf("expected 2 Touch calls, got %d", len(store.touched))
	}
}

func TestRetriever_EmptyStore(t *testing.T) {
	store := &mockMemoryStore{memories: nil}

	r := NewRetriever(store)
	scored, err := r.Retrieve(context.Background(), "a1", []float32{1, 0, 0}, RetrieveOptions{})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(scored) != 0 {
		t.Errorf("expected 0 results, got %d", len(scored))
	}
}

func TestComputeScore_CategoryWeights(t *testing.T) {
	now := time.Now()
	emb := []float32{1, 0, 0}

	for _, tc := range []struct {
		category string
		weight   float32
	}{
		{CategoryInstruction, 1.0},
		{CategoryFact, 0.8},
		{CategoryDecision, 0.6},
		{CategoryContext, 0.4},
		{"unknown", 0.5},
	} {
		mem := Memory{
			Category:  tc.category,
			Embedding: emb,
			CreatedAt: now,
		}
		score := computeScore(mem, emb, now)
		// With identical embedding (sim=1 → semantic=(1+1)/2=1.0), recent (recency≈1.0),
		// 0 access count, not pinned:
		// 0.50*1.0 + 0.20*1.0 + 0.10*0 + 0.15*0 + 0.05*catWeight
		expected := 0.50 + 0.20 + 0.05*tc.weight
		if math.Abs(float64(score-float32(expected))) > 0.01 {
			t.Errorf("category %q: score=%f, want ~%f", tc.category, score, expected)
		}
	}
}
