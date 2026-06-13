package memory

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/history"
)

// mockEmbeddingProvider implements models.EmbeddingProvider for testing.
type mockEmbeddingProvider struct {
	embedding []float32
}

func (m *mockEmbeddingProvider) Embed(_ context.Context, _ string) ([]float32, error) {
	return m.embedding, nil
}
func (m *mockEmbeddingProvider) EmbedBatch(_ context.Context, inputs []string) ([][]float32, error) {
	out := make([][]float32, len(inputs))
	for i := range inputs {
		out[i] = m.embedding
	}
	return out, nil
}
func (m *mockEmbeddingProvider) Model() string  { return "test-embed" }
func (m *mockEmbeddingProvider) Dimensions() int { return len(m.embedding) }

func TestParseExtractionResponse_ValidJSON(t *testing.T) {
	input := `[{"category":"fact","content":"User prefers Go"},{"category":"decision","content":"Using SQLite"}]`
	memories, err := parseExtractionResponse(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(memories) != 2 {
		t.Fatalf("expected 2 memories, got %d", len(memories))
	}
	if memories[0].Category != "fact" || memories[0].Content != "User prefers Go" {
		t.Errorf("memory[0] = %+v, want fact/User prefers Go", memories[0])
	}
	if memories[1].Category != "decision" || memories[1].Content != "Using SQLite" {
		t.Errorf("memory[1] = %+v, want decision/Using SQLite", memories[1])
	}
}

func TestParseExtractionResponse_MarkdownFences(t *testing.T) {
	input := "```json\n[{\"category\":\"fact\",\"content\":\"test\"}]\n```"
	memories, err := parseExtractionResponse(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(memories) != 1 {
		t.Fatalf("expected 1 memory, got %d", len(memories))
	}
	if memories[0].Content != "test" {
		t.Errorf("content = %q, want %q", memories[0].Content, "test")
	}
}

func TestParseExtractionResponse_EmptyArray(t *testing.T) {
	memories, err := parseExtractionResponse("[]")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if memories != nil {
		t.Errorf("expected nil for empty array, got %v", memories)
	}
}

func TestParseExtractionResponse_InvalidJSON(t *testing.T) {
	_, err := parseExtractionResponse("not json at all")
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestIsValidCategory(t *testing.T) {
	for _, tc := range []struct {
		cat  string
		want bool
	}{
		{CategoryFact, true},
		{CategoryDecision, true},
		{CategoryInstruction, true},
		{CategoryContext, true},
		{"unknown", false},
		{"", false},
	} {
		got := isValidCategory(tc.cat)
		if got != tc.want {
			t.Errorf("isValidCategory(%q) = %v, want %v", tc.cat, got, tc.want)
		}
	}
}

func TestBuildExtractionPrompt_LimitsTo10Entries(t *testing.T) {
	entries := make([]history.HistoryEntry, 15)
	for i := range entries {
		entries[i] = history.HistoryEntry{
			Role:    "user",
			Content: "message",
		}
	}

	prompt := buildExtractionPrompt(entries)

	// Count occurrences of "[user]:" — should be 10, not 15
	count := strings.Count(prompt, "[user]:")
	if count != 10 {
		t.Errorf("expected 10 entries in prompt, got %d", count)
	}
}

func TestBuildExtractionPrompt_ShortHistory(t *testing.T) {
	entries := []history.HistoryEntry{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi there"},
	}

	prompt := buildExtractionPrompt(entries)
	if !strings.Contains(prompt, "[user]: hello") {
		t.Error("expected user message in prompt")
	}
	if !strings.Contains(prompt, "[assistant]: hi there") {
		t.Error("expected assistant message in prompt")
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("short", 10); got != "short" {
		t.Errorf("truncate short string: got %q, want %q", got, "short")
	}
	if got := truncate("this is a long string", 10); got != "this is a ..." {
		t.Errorf("truncate long string: got %q, want %q", got, "this is a ...")
	}
}

func TestExtractor_SkipsShortHistory(t *testing.T) {
	store := &mockMemoryStore{}
	extractor := NewExtractor(store, nil, nil, "test-model", DefaultExtractionConfig())

	// 4 entries < MinExchanges*2 (6) → should return without calling provider
	entries := []history.HistoryEntry{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello"},
		{Role: "user", Content: "how are you"},
		{Role: "assistant", Content: "good"},
	}

	// This should not panic (provider is nil) because it returns early
	extractor.Extract(context.Background(), "agent-1", entries)
}

func TestIsDuplicate_HighSimilarity(t *testing.T) {
	store := &mockMemoryStore{
		memories: []Memory{
			{
				ID:        1,
				AgentID:   "a1",
				Content:   "existing memory",
				Embedding: []float32{1, 0, 0},
				CreatedAt: time.Now(),
			},
		},
	}

	embedder := &mockEmbeddingProvider{embedding: []float32{0.99, 0.01, 0}}

	extractor := NewExtractor(store, nil, embedder, "test-model", DefaultExtractionConfig())
	dup, err := extractor.isDuplicate(context.Background(), "a1", "very similar memory")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !dup {
		t.Error("expected duplicate for high-similarity vectors")
	}
}

func TestIsDuplicate_LowSimilarity(t *testing.T) {
	store := &mockMemoryStore{
		memories: []Memory{
			{
				ID:        1,
				AgentID:   "a1",
				Content:   "existing memory",
				Embedding: []float32{1, 0, 0},
				CreatedAt: time.Now(),
			},
		},
	}

	// Orthogonal vector → cosine sim ≈ 0
	embedder := &mockEmbeddingProvider{embedding: []float32{0, 1, 0}}

	extractor := NewExtractor(store, nil, embedder, "test-model", DefaultExtractionConfig())
	dup, err := extractor.isDuplicate(context.Background(), "a1", "completely different topic")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dup {
		t.Error("expected no duplicate for orthogonal vectors")
	}
}
