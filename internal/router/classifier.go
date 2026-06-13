package router

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/kkjorsvik/kyvik/internal/history"
	"github.com/kkjorsvik/kyvik/internal/models"
)

// ClassifyResult holds the outcome of an automatic slot classification.
type ClassifyResult struct {
	SlotName   string
	Confidence string // "high", "medium", "low"
	Reason     string
	TokensIn   int64
	TokensOut  int64
	Cost       float64
}

// cacheEntry stores a cached classification result per agent.
type cacheEntry struct {
	slotName  string
	timestamp time.Time
}

// Classifier examines user messages and decides which model slot should handle
// the request. It is safe for concurrent use across multiple agents.
type Classifier struct {
	cache    sync.Map // map[agentID]cacheEntry
	cacheTTL time.Duration
}

// NewClassifier creates a Classifier with a 60-second cache TTL.
// The provider and model are passed per-call so one instance works for all agents.
func NewClassifier() *Classifier {
	return &Classifier{
		cacheTTL: 60 * time.Second,
	}
}

// Classify determines the best model slot for a user message.
func (c *Classifier) Classify(
	ctx context.Context,
	agentID string,
	message string,
	recentHistory []history.HistoryEntry,
	slots []ModelSlot,
	provider models.Provider,
	model string,
) (ClassifyResult, error) {
	// Cache check
	if entry, ok := c.cache.Load(agentID); ok {
		ce := entry.(cacheEntry)
		if time.Since(ce.timestamp) < c.cacheTTL {
			return ClassifyResult{
				SlotName:   ce.slotName,
				Confidence: "high",
				Reason:     "cached",
			}, nil
		}
	}

	// Build prompt
	systemPrompt := buildClassificationPrompt(slots, recentHistory)

	messages := []models.ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: message},
	}

	resp, err := provider.Complete(ctx, models.CompletionRequest{
		Model:    model,
		Messages: messages,
	})
	if err != nil {
		return ClassifyResult{}, fmt.Errorf("classifier model call failed: %w", err)
	}

	slotName, confidence, reason, err := parseClassificationResponse(resp.Content, slots)
	if err != nil {
		return ClassifyResult{}, err
	}

	// Update cache
	c.cache.Store(agentID, cacheEntry{
		slotName:  slotName,
		timestamp: time.Now(),
	})

	return ClassifyResult{
		SlotName:   slotName,
		Confidence: confidence,
		Reason:     reason,
		TokensIn:   resp.TokensIn,
		TokensOut:  resp.TokensOut,
		Cost:       resp.Cost,
	}, nil
}

// buildClassificationPrompt creates the system prompt for the classifier model.
func buildClassificationPrompt(slots []ModelSlot, recentHistory []history.HistoryEntry) string {
	var b strings.Builder

	b.WriteString("You are a message router. Decide which model slot should handle the user's message.\n\n")
	b.WriteString("Available slots:\n")
	for _, s := range slots {
		fmt.Fprintf(&b, "- %s (%s/%s)\n", s.Name, s.Provider, s.Model)
	}

	// Include up to the last 3 history entries for context
	if len(recentHistory) > 0 {
		b.WriteString("\nRecent conversation:\n")
		start := 0
		if len(recentHistory) > 3 {
			start = len(recentHistory) - 3
		}
		for _, entry := range recentHistory[start:] {
			fmt.Fprintf(&b, "[%s]: %s\n", entry.Role, entry.Content)
		}
	}

	b.WriteString("\nRespond with JSON only: {\"slot\": \"name\", \"confidence\": \"high|medium|low\", \"reason\": \"brief explanation\"}")

	return b.String()
}

// parseClassificationResponse extracts slot, confidence, and reason from the
// classifier's response. It handles raw JSON, markdown-fenced JSON, and
// brace-delimited fallback.
func parseClassificationResponse(content string, slots []ModelSlot) (slot, confidence, reason string, err error) {
	jsonStr := extractJSON(content)
	if jsonStr == "" {
		return "", "", "", fmt.Errorf("no JSON found in classifier response: %s", content)
	}

	var result struct {
		Slot       string `json:"slot"`
		Confidence string `json:"confidence"`
		Reason     string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return "", "", "", fmt.Errorf("failed to parse classifier JSON: %w", err)
	}

	// Validate slot name
	found := false
	for _, s := range slots {
		if s.Name == result.Slot {
			found = true
			break
		}
	}
	if !found {
		return "", "", "", fmt.Errorf("classifier returned unknown slot %q", result.Slot)
	}

	return result.Slot, result.Confidence, result.Reason, nil
}

// extractJSON tries to find a JSON object in the content string.
// It handles: raw JSON, markdown-fenced JSON (```json ... ```), and
// falls back to finding the first '{' to last '}'.
func extractJSON(content string) string {
	trimmed := strings.TrimSpace(content)

	// Try raw JSON first
	if strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}") {
		return trimmed
	}

	// Try markdown-fenced JSON
	if idx := strings.Index(trimmed, "```json"); idx >= 0 {
		start := idx + len("```json")
		if end := strings.Index(trimmed[start:], "```"); end >= 0 {
			return strings.TrimSpace(trimmed[start : start+end])
		}
	}
	if idx := strings.Index(trimmed, "```"); idx >= 0 {
		start := idx + len("```")
		if end := strings.Index(trimmed[start:], "```"); end >= 0 {
			candidate := strings.TrimSpace(trimmed[start : start+end])
			if strings.HasPrefix(candidate, "{") {
				return candidate
			}
		}
	}

	// Fallback: first '{' to last '}'
	first := strings.IndexByte(trimmed, '{')
	last := strings.LastIndexByte(trimmed, '}')
	if first >= 0 && last > first {
		return trimmed[first : last+1]
	}

	return ""
}
