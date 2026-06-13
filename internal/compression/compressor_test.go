package compression

import (
	"context"
	"fmt"
	"testing"

	"github.com/kkjorsvik/kyvik/internal/history"
	"github.com/kkjorsvik/kyvik/internal/models"
	"github.com/kkjorsvik/kyvik/internal/testutil"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// --- mock provider ---

type mockProvider struct {
	response string
}

func (m *mockProvider) Complete(_ context.Context, _ models.CompletionRequest) (*models.CompletionResponse, error) {
	return &models.CompletionResponse{Content: m.response, Model: "test-model"}, nil
}

func (m *mockProvider) Stream(_ context.Context, _ models.CompletionRequest) (<-chan models.StreamChunk, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockProvider) ListModels(_ context.Context) ([]models.ModelInfo, error) { return nil, nil }
func (m *mockProvider) Name() string                                             { return "mock" }

// --- test history helper ---

func newTestHistory(t *testing.T) history.HistoryStore {
	t.Helper()
	tdb := testutil.RequirePostgres(t)
	return history.New(tdb.DB)
}

// --- ShouldCompress tests ---

func TestShouldCompress_Disabled(t *testing.T) {
	c := New(nil, nil, nil, nil, nil)
	cfg := types.CompressionConfig{Enabled: false, MessageThreshold: 10, TokenThresholdPct: 70}
	if c.ShouldCompress(100, 9000, 10000, cfg) {
		t.Error("should not compress when disabled")
	}
}

func TestShouldCompress_BelowMessageThreshold(t *testing.T) {
	c := New(nil, nil, nil, nil, nil)
	cfg := types.CompressionConfig{Enabled: true, MessageThreshold: 20, TokenThresholdPct: 70}
	if c.ShouldCompress(15, 100, 10000, cfg) {
		t.Error("should not compress below message threshold")
	}
}

func TestShouldCompress_AboveMessageThreshold(t *testing.T) {
	c := New(nil, nil, nil, nil, nil)
	cfg := types.CompressionConfig{Enabled: true, MessageThreshold: 20, TokenThresholdPct: 70}
	if !c.ShouldCompress(20, 100, 10000, cfg) {
		t.Error("should compress at message threshold")
	}
	if !c.ShouldCompress(25, 100, 10000, cfg) {
		t.Error("should compress above message threshold")
	}
}

func TestShouldCompress_TokenThreshold(t *testing.T) {
	c := New(nil, nil, nil, nil, nil)
	cfg := types.CompressionConfig{Enabled: true, MessageThreshold: 100, TokenThresholdPct: 70}

	// 70% of 1000 = 700 tokens needed
	if c.ShouldCompress(5, 699, 1000, cfg) {
		t.Error("should not compress below token threshold")
	}
	if !c.ShouldCompress(5, 700, 1000, cfg) {
		t.Error("should compress at token threshold")
	}
	if !c.ShouldCompress(5, 900, 1000, cfg) {
		t.Error("should compress above token threshold")
	}
}

func TestShouldCompress_ZeroBudget(t *testing.T) {
	c := New(nil, nil, nil, nil, nil)
	cfg := types.CompressionConfig{Enabled: true, MessageThreshold: 100, TokenThresholdPct: 70}
	// With zero budget, token threshold should not trigger.
	if c.ShouldCompress(5, 900, 0, cfg) {
		t.Error("should not compress with zero budget")
	}
}

// --- TryCompress integration test ---

func TestTryCompress_Basic(t *testing.T) {
	hs := newTestHistory(t)
	ctx := context.Background()

	// Append 25 messages.
	for i := 0; i < 25; i++ {
		err := hs.Append(ctx, history.HistoryEntry{
			AgentID:   "agent1",
			Channel:   "web",
			ChannelID: "conv1",
			Role:      "user",
			Content:   fmt.Sprintf("Message number %d with some content to give it length", i+1),
			Tokens:    15,
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	// Verify 25 messages exist.
	before, err := hs.Recent(ctx, "agent1", "web", "conv1", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 25 {
		t.Fatalf("expected 25 messages, got %d", len(before))
	}

	mock := &mockProvider{response: "## Resolved\nHandled 15 messages.\n## Open\nNothing pending."}
	provFn := func(compressionModel string, agentCfg types.AgentConfig) models.Provider {
		return mock
	}

	comp := New(hs, nil, nil, nil, provFn)

	cfg := types.CompressionConfig{
		Enabled:            true,
		MessageThreshold:   20,
		TokenThresholdPct:  70,
		KeepRecentMessages: 10,
	}
	agentCfg := types.AgentConfig{
		ID: "agent1",
		ModelConfig: types.ModelConfig{
			Provider: "openrouter",
			Model:    "test/model",
		},
	}

	err = comp.TryCompress(ctx, "agent1", "web", "conv1", cfg, agentCfg)
	if err != nil {
		t.Fatalf("TryCompress failed: %v", err)
	}

	// After compression: should have ~10 recent messages + 1 summary.
	after, err := hs.Recent(ctx, "agent1", "web", "conv1", 1000)
	if err != nil {
		t.Fatal(err)
	}

	// Count summaries and regular messages.
	summaryCount := 0
	regularCount := 0
	for _, e := range after {
		if e.Role == "summary" {
			summaryCount++
		} else {
			regularCount++
		}
	}

	if summaryCount != 1 {
		t.Errorf("expected 1 summary, got %d", summaryCount)
	}
	if regularCount != 10 {
		t.Errorf("expected 10 regular messages kept, got %d", regularCount)
	}

	// Verify summary content.
	summary, err := hs.ActiveSummary(ctx, "agent1", "web", "conv1")
	if err != nil {
		t.Fatal(err)
	}
	if summary == nil {
		t.Fatal("expected active summary")
	}
	if summary.Content != mock.response {
		t.Errorf("summary content mismatch: got %q", summary.Content)
	}
}

func TestTryCompress_DisabledConfig(t *testing.T) {
	comp := New(nil, nil, nil, nil, nil)
	cfg := types.CompressionConfig{Enabled: false}
	err := comp.TryCompress(context.Background(), "a", "b", "c", cfg, types.AgentConfig{})
	if err != nil {
		t.Errorf("expected nil error for disabled config, got: %v", err)
	}
}

func TestTryCompress_ConcurrentSameConversation(t *testing.T) {
	hs := newTestHistory(t)
	ctx := context.Background()

	// Append enough messages to trigger compression.
	for i := 0; i < 25; i++ {
		_ = hs.Append(ctx, history.HistoryEntry{
			AgentID: "a1", Channel: "web", ChannelID: "c1",
			Role: "user", Content: fmt.Sprintf("msg %d", i), Tokens: 10,
		})
	}

	callCount := 0
	slowProvider := &mockProvider{response: "summary"}
	provFn := func(compressionModel string, agentCfg types.AgentConfig) models.Provider {
		callCount++
		return slowProvider
	}

	comp := New(hs, nil, nil, nil, provFn)
	cfg := types.CompressionConfig{
		Enabled: true, MessageThreshold: 20,
		TokenThresholdPct: 70, KeepRecentMessages: 10,
	}
	agentCfg := types.AgentConfig{ID: "a1", ModelConfig: types.ModelConfig{Model: "m"}}

	// Simulate lock: manually set active.
	comp.mu.Lock()
	comp.active["a1:web:c1"] = true
	comp.mu.Unlock()

	// This should return immediately (skip).
	err := comp.TryCompress(ctx, "a1", "web", "c1", cfg, agentCfg)
	if err != nil {
		t.Errorf("expected nil for concurrent skip, got: %v", err)
	}
	if callCount != 0 {
		t.Errorf("expected 0 provider calls for skipped compression, got %d", callCount)
	}
}
