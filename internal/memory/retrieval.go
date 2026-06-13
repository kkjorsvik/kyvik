package memory

import (
	"context"
	"math"
	"sort"
	"time"
)

// ScoredMemory pairs a Memory with a combined retrieval score.
type ScoredMemory struct {
	Memory
	Score float32 // Combined retrieval score [0, 1]
}

// RetrieveOptions controls the retrieval query.
type RetrieveOptions struct {
	Limit int // Max memories to return (default 10)
}

// Retriever orchestrates semantic search with multi-factor scoring.
type Retriever struct {
	store MemoryStore
}

// NewRetriever creates a new Retriever.
func NewRetriever(store MemoryStore) *Retriever {
	return &Retriever{store: store}
}

// Retrieve finds the most relevant memories for a query embedding.
//
// Scoring formula:
//
//	semantic similarity (50%) + recency (20%) + access frequency (10%) +
//	pinned bonus (15%) + category weight (5%)
func (r *Retriever) Retrieve(ctx context.Context, agentID string, queryEmbedding []float32, opts RetrieveOptions) ([]ScoredMemory, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}

	memories, err := r.store.ListWithEmbeddings(ctx, agentID)
	if err != nil {
		return nil, err
	}

	if len(memories) == 0 {
		return nil, nil
	}

	now := time.Now()
	scored := make([]ScoredMemory, 0, len(memories))

	for _, mem := range memories {
		score := computeScore(mem, queryEmbedding, now)
		scored = append(scored, ScoredMemory{Memory: mem, Score: score})
	}

	// Sort by score descending.
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].Score > scored[j].Score
	})

	// Trim to limit.
	if len(scored) > limit {
		scored = scored[:limit]
	}

	// Touch each returned memory (best-effort, don't fail retrieval).
	for _, sm := range scored {
		_ = r.store.Touch(ctx, sm.ID)
	}

	return scored, nil
}

// computeScore calculates the multi-factor score for a memory.
func computeScore(mem Memory, queryEmbedding []float32, now time.Time) float32 {
	// Semantic similarity (50%): cosine similarity mapped from [-1,1] to [0,1]
	rawSim := CosineSimilarity(queryEmbedding, mem.Embedding)
	semantic := (rawSim + 1) / 2

	// Recency (20%): exp(-ageHours / 720), half-life ~30 days
	ageHours := now.Sub(mem.CreatedAt).Hours()
	recency := float32(math.Exp(-ageHours / 720))

	// Access frequency (10%): min(accessCount, 20) / 20.0
	accessCount := float32(mem.AccessCount)
	if accessCount > 20 {
		accessCount = 20
	}
	frequency := accessCount / 20.0

	// Pinned bonus (15%): 1.0 if pinned, 0.0 if not
	var pinned float32
	if mem.Pinned {
		pinned = 1.0
	}

	// Category weight (5%): instruction=1.0, fact=0.8, decision=0.6, context=0.4
	catWeight := categoryWeight(mem.Category)

	return 0.50*semantic +
		0.20*recency +
		0.10*frequency +
		0.15*pinned +
		0.05*catWeight
}

// categoryWeight returns the weight for a memory category.
func categoryWeight(category string) float32 {
	switch category {
	case CategoryInstruction:
		return 1.0
	case CategoryFact:
		return 0.8
	case CategoryDecision:
		return 0.6
	case CategoryContext:
		return 0.4
	default:
		return 0.5
	}
}
