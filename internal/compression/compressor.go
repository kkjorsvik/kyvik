package compression

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/kkjorsvik/kyvik/internal/audit"
	"github.com/kkjorsvik/kyvik/internal/history"
	"github.com/kkjorsvik/kyvik/internal/memory"
	"github.com/kkjorsvik/kyvik/internal/models"
	"github.com/kkjorsvik/kyvik/internal/spending"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// ProviderFunc returns a provider for the given compression/agent config.
type ProviderFunc func(compressionModel string, agentCfg types.AgentConfig) models.Provider

// Compressor handles conversation compression for agents.
type Compressor struct {
	history    history.HistoryStore
	memory     memory.MemoryStore
	spending   spending.Tracker
	audit      audit.Logger
	providerFn ProviderFunc
	mu         sync.Mutex
	active     map[string]bool
}

// New creates a new Compressor.
func New(h history.HistoryStore, m memory.MemoryStore, s spending.Tracker, a audit.Logger, pf ProviderFunc) *Compressor {
	return &Compressor{
		history:    h,
		memory:     m,
		spending:   s,
		audit:      a,
		providerFn: pf,
		active:     make(map[string]bool),
	}
}

// ShouldCompress returns true if compression should be triggered.
func (c *Compressor) ShouldCompress(messageCount, historyTokens, historyBudget int, cfg types.CompressionConfig) bool {
	if !cfg.Enabled {
		return false
	}
	if messageCount >= cfg.MessageThreshold {
		return true
	}
	if historyBudget > 0 && historyTokens*100/historyBudget >= cfg.TokenThresholdPct {
		return true
	}
	return false
}

func conversationKey(agentID, channel, channelID string) string {
	return agentID + ":" + channel + ":" + channelID
}

// TryCompress attempts to compress older messages in a conversation.
// Safe to call concurrently — only one compression runs per conversation.
func (c *Compressor) TryCompress(ctx context.Context, agentID, channel, channelID string,
	cfg types.CompressionConfig, agentCfg types.AgentConfig) error {

	if !cfg.Enabled {
		return nil
	}

	key := conversationKey(agentID, channel, channelID)

	// Check-and-set: hold mutex briefly.
	c.mu.Lock()
	if c.active[key] {
		c.mu.Unlock()
		return nil
	}
	c.active[key] = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.active, key)
		c.mu.Unlock()
	}()

	start := time.Now()

	compCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Load uncompressed messages.
	entries, err := c.history.Recent(compCtx, agentID, channel, channelID, 1000)
	if err != nil {
		return fmt.Errorf("compression load history: %w", err)
	}

	// Check thresholds.
	totalTokens := 0
	for _, e := range entries {
		totalTokens += history.EstimateTokens(e.Content)
	}
	ctxBudget := types.NormalizeContextBudget(agentCfg.ContextBudget)
	historyBudget := ctxBudget.MaxTotalTokens * ctxBudget.HistoryPct / 100
	if !c.ShouldCompress(len(entries), totalTokens, historyBudget, cfg) {
		return nil
	}

	// Split: keep last N, compress the rest.
	keep := cfg.KeepRecentMessages
	if keep >= len(entries) {
		return nil
	}
	splitIdx := len(entries) - keep

	// Adjust split to avoid breaking tool-call chains.
	for splitIdx > 0 && entries[splitIdx].Role == "tool" {
		splitIdx--
	}
	if splitIdx <= 0 {
		return nil
	}

	toCompress := entries[:splitIdx]

	// Load existing summary for rolling merge.
	prevSummary, err := c.history.ActiveSummary(compCtx, agentID, channel, channelID)
	if err != nil {
		return fmt.Errorf("compression load summary: %w", err)
	}

	// Load memories for dedup.
	var memories []memory.Memory
	if c.memory != nil {
		memories, _ = c.memory.ListRecent(compCtx, agentID, 20)
	}

	// Build prompt and call LLM.
	sysProm, userProm := BuildPrompt(toCompress, memories, prevSummary)

	provider := c.providerFn(cfg.Model, agentCfg)
	if provider == nil {
		return fmt.Errorf("compression: no provider available")
	}

	model := agentCfg.ModelConfig.Model
	if cfg.Model != "" {
		model = cfg.Model
	}

	inputTokens := history.EstimateTokens(sysProm) + history.EstimateTokens(userProm)
	maxTokens := inputTokens / 4
	if maxTokens < 500 {
		maxTokens = 500
	}

	resp, err := provider.Complete(compCtx, models.CompletionRequest{
		Model: model,
		Messages: []models.ChatMessage{
			{Role: "system", Content: sysProm},
			{Role: "user", Content: userProm},
		},
		MaxTokens: maxTokens,
	})
	if err != nil {
		return fmt.Errorf("compression llm call: %w", err)
	}

	// Store summary and mark compressed atomically.
	summaryEntry := history.HistoryEntry{
		AgentID:   agentID,
		Channel:   channel,
		ChannelID: channelID,
		Role:      "summary",
		Content:   resp.Content,
		Tokens:    history.EstimateTokens(resp.Content),
	}

	compressIDs := make([]int64, len(toCompress))
	for i, e := range toCompress {
		compressIDs[i] = e.ID
	}
	if prevSummary != nil {
		compressIDs = append(compressIDs, prevSummary.ID)
	}

	if err := c.history.AppendAndCompress(compCtx, summaryEntry, compressIDs); err != nil {
		return fmt.Errorf("compression store and mark: %w", err)
	}

	// Track spending.
	if c.spending != nil {
		_ = c.spending.Record(compCtx, agentID,
			resp.TokensIn, resp.TokensOut, resp.Cost,
			spending.RecordOptions{
				Model:    resp.Model,
				Category: "compression",
			})
	}

	// Audit log.
	if c.audit != nil {
		_ = c.audit.Log(compCtx, types.AuditEntry{
			AgentID:   agentID,
			EventType: types.EventAgentLifecycle,
			Action:    "compression",
			Details: fmt.Sprintf("compressed %d messages (%d tokens) into summary (%d tokens) in %dms",
				len(toCompress), totalTokens, history.EstimateTokens(resp.Content),
				time.Since(start).Milliseconds()),
			Timestamp: time.Now(),
		})
	}

	return nil
}
