package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/kkjorsvik/kyvik/internal/history"
	"github.com/kkjorsvik/kyvik/internal/models"
)

// Extraction tuning constants.
const (
	MinExchanges                 = 3   // minimum user+assistant pairs required
	maxExtractionEntries         = 10  // max history entries fed to extraction prompt
)

// ExtractionConfig holds per-agent extraction settings.
type ExtractionConfig struct {
	Interval           int     // messages between extractions (default 15)
	MaxPerRun          int     // max candidates per extraction (default 2)
	DuplicateThreshold float32 // cosine sim to skip (default 0.85)
	SimilarThreshold   float32 // cosine sim to warn (default 0.75)
}

// DefaultExtractionConfig returns the conservative default settings.
func DefaultExtractionConfig() ExtractionConfig {
	return ExtractionConfig{
		Interval:           15,
		MaxPerRun:          2,
		DuplicateThreshold: 0.85,
		SimilarThreshold:   0.75,
	}
}

// Extractor uses an LLM to extract noteworthy facts from recent conversation.
type Extractor struct {
	store    MemoryStore
	provider models.Provider
	embedder models.EmbeddingProvider // may be nil (dedup skipped)
	model    string                   // LLM model name for extraction calls
	config   ExtractionConfig
}

// NewExtractor creates a new Extractor.
func NewExtractor(store MemoryStore, provider models.Provider, embedder models.EmbeddingProvider, model string, cfg ExtractionConfig) *Extractor {
	return &Extractor{
		store:    store,
		provider: provider,
		embedder: embedder,
		model:    model,
		config:   cfg,
	}
}

// extractedMemory is a single memory extracted by the LLM.
type extractedMemory struct {
	Category   string `json:"category"`
	Content    string `json:"content"`
	Confidence string `json:"confidence"` // high, medium, low
}

// Extract analyzes recent conversation history and stores novel memories.
func (e *Extractor) Extract(ctx context.Context, agentID string, recentHistory []history.HistoryEntry) {
	log := slog.With("agent_id", agentID, "component", "memory_extraction")

	// Guard: skip if insufficient history
	if len(recentHistory) < MinExchanges*2 {
		log.Debug("skipping extraction, insufficient history", "entries", len(recentHistory))
		return
	}

	// Build the extraction prompt from recent entries
	prompt := buildExtractionPrompt(recentHistory)

	sourceChannel := ""
	sourceChannelID := ""
	if len(recentHistory) > 0 {
		last := recentHistory[len(recentHistory)-1]
		sourceChannel = last.Channel
		sourceChannelID = last.ChannelID
	}

	// Load existing memories for context (helps LLM avoid duplicates)
	existingMemories, _ := e.store.ListRecent(ctx, agentID, 20)

	// Build system prompt with existing memories context
	systemPrompt := buildExtractionSystemPrompt(e.config.MaxPerRun, existingMemories)

	// Call LLM
	req := models.CompletionRequest{
		Model: e.model,
		Messages: []models.ChatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: prompt},
		},
		Temperature: 0.1,
		MaxTokens:   1024,
	}

	resp, err := e.provider.Complete(ctx, req)
	if err != nil {
		log.Error("extraction LLM call failed", "error", err)
		return
	}

	// Parse the response
	memories, err := parseExtractionResponse(resp.Content)
	if err != nil {
		log.Warn("failed to parse extraction response", "error", err, "content", truncate(resp.Content, 200))
		return
	}

	if len(memories) == 0 {
		log.Debug("extraction found no noteworthy memories")
		return
	}

	// Filter to high confidence only
	var filtered []extractedMemory
	for _, m := range memories {
		if m.Confidence == "high" {
			filtered = append(filtered, m)
		}
	}
	if len(filtered) > e.config.MaxPerRun {
		filtered = filtered[:e.config.MaxPerRun]
	}

	if len(filtered) == 0 {
		log.Debug("no high-confidence memories extracted")
		return
	}

	// Store each extracted memory as candidate
	stored := 0
	for _, m := range filtered {
		if m.Content == "" {
			continue
		}

		category := m.Category
		if !isValidCategory(category) {
			category = CategoryFact
		}

		// Deduplication check
		if e.embedder != nil {
			dup, err := e.isDuplicate(ctx, agentID, m.Content)
			if err != nil {
				log.Warn("dedup check failed, storing anyway", "error", err)
			} else if dup {
				log.Debug("skipping duplicate memory", "content", truncate(m.Content, 80))
				continue
			}
		}

		memID, err := e.store.Create(ctx, Memory{
			AgentID:         agentID,
			Category:        category,
			Content:         m.Content,
			Source:          SourceAuto,
			Status:          StatusCandidate,
			RelevanceScore:  0.5,
			Reviewed:        false,
			SourceChannel:   sourceChannel,
			SourceChannelID: sourceChannelID,
		})
		if err != nil {
			log.Error("failed to store extracted memory", "error", err)
			continue
		}

		// Best-effort embed immediately
		if e.embedder != nil {
			if vec, err := e.embedder.Embed(ctx, m.Content); err == nil {
				_ = e.store.SetEmbedding(ctx, memID, vec, e.embedder.Model())
			}
		}

		stored++
	}

	log.Info("memory extraction completed", "extracted", len(memories), "high_confidence", len(filtered), "stored", stored)
}

// isDuplicate checks if a candidate memory is too similar to existing memories.
func (e *Extractor) isDuplicate(ctx context.Context, agentID, content string) (bool, error) {
	candidateEmb, err := e.embedder.Embed(ctx, content)
	if err != nil {
		return false, fmt.Errorf("embed candidate: %w", err)
	}

	existing, err := e.store.ListWithEmbeddings(ctx, agentID)
	if err != nil {
		return false, fmt.Errorf("list existing embeddings: %w", err)
	}

	var maxSim float32
	for _, mem := range existing {
		sim := CosineSimilarity(candidateEmb, mem.Embedding)
		if sim > maxSim {
			maxSim = sim
		}
	}

	if maxSim > e.config.DuplicateThreshold {
		return true, nil
	}
	if maxSim > e.config.SimilarThreshold {
		slog.Debug("similar memory exists but storing", "similarity", maxSim, "content", truncate(content, 80))
	}

	return false, nil
}

// buildExtractionPrompt formats recent history entries into a prompt.
func buildExtractionPrompt(entries []history.HistoryEntry) string {
	// Cap at maxExtractionEntries
	if len(entries) > maxExtractionEntries {
		entries = entries[len(entries)-maxExtractionEntries:]
	}

	var b strings.Builder
	b.WriteString("Recent conversation:\n\n")
	for _, e := range entries {
		fmt.Fprintf(&b, "[%s]: %s\n\n", e.Role, truncate(e.Content, 500))
	}
	b.WriteString("Extract noteworthy memories from the conversation above.")
	return b.String()
}

// parseExtractionResponse parses the LLM's JSON array response.
func parseExtractionResponse(content string) ([]extractedMemory, error) {
	// Strip markdown code fences if present
	cleaned := strings.TrimSpace(content)
	if strings.HasPrefix(cleaned, "```") {
		// Remove opening fence (```json or ```)
		if idx := strings.Index(cleaned, "\n"); idx != -1 {
			cleaned = cleaned[idx+1:]
		}
		// Remove closing fence
		if idx := strings.LastIndex(cleaned, "```"); idx != -1 {
			cleaned = cleaned[:idx]
		}
		cleaned = strings.TrimSpace(cleaned)
	}

	if cleaned == "[]" {
		return nil, nil
	}

	var memories []extractedMemory
	if err := json.Unmarshal([]byte(cleaned), &memories); err != nil {
		return nil, fmt.Errorf("parse extraction JSON: %w", err)
	}

	return memories, nil
}

// isValidCategory returns true if the category is one of the known categories.
func isValidCategory(category string) bool {
	switch category {
	case CategoryFact, CategoryDecision, CategoryInstruction, CategoryContext:
		return true
	default:
		return false
	}
}

// truncate limits a string to maxLen characters, appending "..." if truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// buildExtractionSystemPrompt creates the system prompt for memory extraction,
// incorporating the max extraction count and existing memories for dedup context.
func buildExtractionSystemPrompt(maxPerRun int, existingMemories []Memory) string {
	prompt := fmt.Sprintf(`You are a memory extraction system. Analyze the conversation and extract noteworthy facts, decisions, instructions, or context that would be useful to remember for future conversations.

Return a JSON array of objects with "category", "content", and "confidence" fields. Categories:
- "fact": Factual information about the user, project, or domain
- "decision": A decision that was made during the conversation
- "instruction": An explicit instruction or preference from the user
- "context": Background context that helps understand the situation

Rules:
- Extract at most %d memories per segment
- Only extract NOVEL information not already covered by existing memories
- Only extract ACTIONABLE information: preferences, decisions, facts that affect future behavior
- Skip greetings, small talk, transient task details
- Each memory must include a confidence rating: "high", "medium", or "low"
- Only "high" confidence memories will be stored
- Each memory should be a concise, self-contained statement
- If nothing noteworthy, return an empty array: []

Example output:
[
  {"category": "fact", "content": "User's project uses Go with SQLite for data storage", "confidence": "high"},
  {"category": "instruction", "content": "User prefers concise responses without code comments", "confidence": "high"}
]`, maxPerRun)

	if len(existingMemories) > 0 {
		prompt += "\n\nExisting memories (do NOT extract duplicates):\n"
		for _, m := range existingMemories {
			prompt += fmt.Sprintf("- [%s] %s\n", m.Category, truncate(m.Content, 100))
		}
	}
	return prompt
}
