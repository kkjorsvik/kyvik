// Package ctxbudget assembles agent prompts within configurable token budgets.
// The package name avoids shadowing the standard library "context" package.
package ctxbudget

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/kkjorsvik/kyvik/internal/history"
	"github.com/kkjorsvik/kyvik/internal/identity"
	"github.com/kkjorsvik/kyvik/internal/memory"
	"github.com/kkjorsvik/kyvik/internal/models"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// SkillsProvider returns concatenated prompt content for an agent's granted skills.
type SkillsProvider interface {
	PromptContentForAgent(ctx context.Context, agentID string) (string, error)
}

// TeamContextProvider returns team-level shared context for an agent.
type TeamContextProvider interface {
	SharedContextForAgent(ctx context.Context, agentID string) (string, error)
}

// IntegrationPromptProvider returns prompt guidance for installed integrations.
type IntegrationPromptProvider interface {
	PromptContentForAgent(ctx context.Context, agentID string) (string, error)
}

// compressionService is a local interface to avoid importing the compression package.
type compressionService interface {
	ShouldCompress(messageCount, historyTokens, historyBudget int, cfg types.CompressionConfig) bool
	TryCompress(ctx context.Context, agentID, channel, channelID string,
		cfg types.CompressionConfig, agentCfg types.AgentConfig) error
}

// Assembler builds prompt components within a token budget.
type Assembler struct {
	memoryStore               memory.MemoryStore
	historyStore              history.HistoryStore
	skillsProvider            SkillsProvider            // nil = no skills content
	teamContextProvider       TeamContextProvider       // nil = no team context
	integrationPromptProvider IntegrationPromptProvider // nil = no integration content
	compressor                compressionService        // nil = no compression
}

// SetSkillsProvider wires a skills provider for prompt injection.
func (a *Assembler) SetSkillsProvider(sp SkillsProvider) {
	a.skillsProvider = sp
}

// SetTeamContextProvider wires a team context provider for prompt injection.
func (a *Assembler) SetTeamContextProvider(tp TeamContextProvider) {
	a.teamContextProvider = tp
}

// SetIntegrationPromptProvider wires an integration prompt provider for prompt injection.
func (a *Assembler) SetIntegrationPromptProvider(ip IntegrationPromptProvider) {
	a.integrationPromptProvider = ip
}

// SetCompressor wires a compression service for summary loading and fallback compression.
func (a *Assembler) SetCompressor(c compressionService) { a.compressor = c }

// AssembleOptions provides per-call configuration.
type AssembleOptions struct {
	EmbeddingProvider  models.EmbeddingProvider // nil = skip semantic retrieval
	Channel            string                   // e.g. "webui", "slack"
	ChannelID          string
	MessageMetadata    map[string]string // metadata from the incoming message (e.g. user_role)
	MessageCount       int               // conversation message count (for periodic nudges)
	ToolTokenEstimate  int               // estimated tokens consumed by tool definitions (deducted from history budget)
	CurrentMsgEstimate int               // estimated tokens in the current user message (deducted from history budget)
}

// AssembledContext is the result of assembling all prompt components.
type AssembledContext struct {
	SystemPrompt  string
	Messages      []models.ChatMessage
	TokenEstimate int
}

// memEntry represents a memory candidate during budget enforcement.
type memEntry struct {
	category string
	content  string
	pinned   bool
	score    float32
}

// New creates a new Assembler. Either store may be nil.
func New(ms memory.MemoryStore, hs history.HistoryStore) *Assembler {
	return &Assembler{
		memoryStore:  ms,
		historyStore: hs,
	}
}

// Assemble builds the system prompt and message history within the agent's token budget.
// The current message is NOT included in the returned Messages — the caller appends it.
func (a *Assembler) Assemble(ctx context.Context, config types.AgentConfig,
	currentMessage string, opts AssembleOptions) (AssembledContext, error) {

	budget := types.NormalizeContextBudget(config.ContextBudget)
	soulBudget := budget.MaxTotalTokens * budget.SoulIdentityPct / 100
	memoryBudget := budget.MaxTotalTokens * budget.MemoriesPct / 100
	historyBudget := budget.MaxTotalTokens * budget.HistoryPct / 100

	// Track surplus from sections that use less than their budget.
	// Surplus is redistributed to history (the last and most flexible section).
	surplus := 0

	// 1. Soul + Identity
	soulIdentityText := identity.BuildSystemPrompt(config)
	soulTokens := EstimateTokens(soulIdentityText)

	if soulTokens > soulBudget {
		soulIdentityText = truncateToTokenBudget(soulIdentityText, config, soulBudget)
		soulTokens = EstimateTokens(soulIdentityText)
	} else {
		surplus += soulBudget - soulTokens
	}

	// 2. Team Context + Skills
	skillsBudget := budget.MaxTotalTokens * budget.SkillsPct / 100
	remainingSkillsBudget := skillsBudget

	var teamBlock string
	var teamTokens int
	if a.teamContextProvider != nil {
		teamContent, err := a.teamContextProvider.SharedContextForAgent(ctx, config.ID)
		if err != nil {
			slog.Warn("team context retrieval failed", "agent_id", config.ID, "error", err)
		} else if strings.TrimSpace(teamContent) != "" {
			teamBlock = "\n\n## Team Context\n" + strings.TrimSpace(teamContent)
			teamTokens = EstimateTokens(teamBlock)
			if teamTokens > remainingSkillsBudget {
				teamBlock = truncateText(teamBlock, remainingSkillsBudget)
				teamTokens = EstimateTokens(teamBlock)
			}
			remainingSkillsBudget -= teamTokens
			if remainingSkillsBudget < 0 {
				remainingSkillsBudget = 0
			}
		}
	}

	var skillsBlock string
	var skillsTokens int
	var integrationsBlock string
	var integrationsTokens int
	if a.integrationPromptProvider != nil {
		integrationContent, err := a.integrationPromptProvider.PromptContentForAgent(ctx, config.ID)
		if err != nil {
			slog.Warn("integration content retrieval failed", "agent_id", config.ID, "error", err)
		} else if strings.TrimSpace(integrationContent) != "" {
			integrationsBlock = "\n\n## Active Integrations\n" + strings.TrimSpace(integrationContent)
			integrationsTokens = EstimateTokens(integrationsBlock)
			if integrationsTokens > remainingSkillsBudget {
				integrationsBlock = truncateText(integrationsBlock, remainingSkillsBudget)
				integrationsTokens = EstimateTokens(integrationsBlock)
			}
			remainingSkillsBudget -= integrationsTokens
			if remainingSkillsBudget < 0 {
				remainingSkillsBudget = 0
			}
		}
	}
	if a.skillsProvider != nil {
		skillsContent, err := a.skillsProvider.PromptContentForAgent(ctx, config.ID)
		if err != nil {
			slog.Warn("skills content retrieval failed", "agent_id", config.ID, "error", err)
		} else if skillsContent != "" {
			skillsBlock = "\n\n## Active Skills\n" + skillsContent
			skillsTokens = EstimateTokens(skillsBlock)
			if skillsTokens > remainingSkillsBudget {
				skillsBlock = truncateText(skillsBlock, remainingSkillsBudget)
				skillsTokens = EstimateTokens(skillsBlock)
			}
		}
	}

	// Track skills surplus (budget minus actual usage).
	actualSkillsTokens := teamTokens + integrationsTokens + skillsTokens
	surplus += skillsBudget - actualSkillsTokens

	// 3. Memories
	var memoryBlock string
	var memoryTokens int
	if a.memoryStore != nil {
		var err error
		memoryBlock, memoryTokens, err = a.buildMemoryBlock(ctx, config, currentMessage, opts, memoryBudget)
		if err != nil {
			return AssembledContext{}, fmt.Errorf("build memory block: %w", err)
		}
	}
	surplus += memoryBudget - memoryTokens

	// Redistribute surplus to history — the most flexible section.
	historyBudget += surplus

	// Reserve headroom for tool definitions and the current user message,
	// which are added by the caller after assembly but count against the
	// model's context window.
	if opts.ToolTokenEstimate > 0 {
		historyBudget -= opts.ToolTokenEstimate
	}
	if opts.CurrentMsgEstimate > 0 {
		historyBudget -= opts.CurrentMsgEstimate
	}
	// Also reserve a fixed buffer for model output tokens (the model needs
	// room to generate a response within the context window).
	const outputReserve = 4096
	historyBudget -= outputReserve
	if historyBudget < 0 {
		historyBudget = 0
	}

	// Compose system prompt
	systemPrompt := soulIdentityText
	if teamBlock != "" {
		systemPrompt += teamBlock
	}
	if integrationsBlock != "" {
		systemPrompt += integrationsBlock
	}
	if skillsBlock != "" {
		systemPrompt += skillsBlock
	}
	if memoryBlock != "" {
		systemPrompt += memoryBlock
	}

	// Inject user role context from message metadata.
	if role := opts.MessageMetadata["user_role"]; role != "" {
		roleCtx := fmt.Sprintf("\n\n## Current User Context\nThe user you are speaking with has the dashboard role: %s. Only offer to show or do things their role permits.", role)
		systemPrompt += roleCtx
	}

	totalEstimate := soulTokens + teamTokens + integrationsTokens + skillsTokens + memoryTokens

	// 4. History
	var historyMessages []models.ChatMessage
	if a.historyStore != nil {
		var err error
		var histTokens int
		historyMessages, histTokens, err = a.buildHistory(ctx, config, opts, historyBudget)
		if err != nil {
			return AssembledContext{}, fmt.Errorf("build history: %w", err)
		}
		totalEstimate += histTokens
	}

	return AssembledContext{
		SystemPrompt:  systemPrompt,
		Messages:      historyMessages,
		TokenEstimate: totalEstimate,
	}, nil
}

// truncateToTokenBudget truncates the soul+identity text to fit within budget.
// It truncates identity first, then soul if needed.
func truncateToTokenBudget(text string, config types.AgentConfig, budget int) string {
	// If both soul and identity are set, try truncating identity first
	if config.SoulContent != "" && config.IdentityContent != "" {
		soulText := strings.TrimSpace(config.SoulContent)
		soulTokens := EstimateTokens(soulText)

		if soulTokens <= budget {
			// Truncate identity to fill remaining budget
			remaining := budget - soulTokens
			if remaining > 0 {
				truncatedIdentity := truncateText(strings.TrimSpace(config.IdentityContent), remaining)
				if truncatedIdentity != "" {
					return soulText + "\n\n" + truncatedIdentity
				}
			}
			return soulText
		}

		// Soul alone exceeds budget — truncate soul
		return truncateText(soulText, budget)
	}

	// Single component or system prompt — just truncate
	return truncateText(text, budget)
}

// truncateText truncates text to approximately fit within a token budget,
// preserving word boundaries where possible.
func truncateText(text string, tokenBudget int) string {
	charBudget := tokenBudget * 3 // matches EstimateTokens ratio (len/3)
	if len(text) <= charBudget {
		return text
	}

	truncated := text[:charBudget]

	// Try to break at last space to preserve word boundaries
	if lastSpace := strings.LastIndex(truncated, " "); lastSpace > charBudget/2 {
		truncated = truncated[:lastSpace]
	}

	return truncated
}

// buildMemoryBlock retrieves and formats memories within the token budget.
func (a *Assembler) buildMemoryBlock(ctx context.Context, config types.AgentConfig,
	currentMessage string, opts AssembleOptions, budget int) (string, int, error) {

	agentID := config.ID

	// Check for pending memory candidates and build a nudge if any exist.
	var candidateNudge string
	candidateCount, _ := a.memoryStore.CountCandidates(ctx, agentID)
	if candidateCount > 0 {
		candidateNudge = fmt.Sprintf("You have %d pending memory candidates. Use the memory tool's 'review' action to accept or reject them when convenient.", candidateCount)
		if opts.MessageCount > 0 && opts.MessageCount%15 == 0 && candidateCount > 5 {
			candidateNudge = fmt.Sprintf("You have %d unreviewed memory candidates piling up. Please review them soon.", candidateCount)
		}
	}

	// Retrieve pinned memories
	pinned, err := a.memoryStore.ListPinned(ctx, agentID)
	if err != nil {
		pinned = nil // non-fatal
	}

	// Retrieve relevant memories (semantic if embeddings available, recency fallback otherwise)
	memLimit := config.MemoryLimit
	if memLimit <= 0 {
		memLimit = 10
	}

	var scored []memory.ScoredMemory
	if opts.EmbeddingProvider != nil {
		queryEmb, err := opts.EmbeddingProvider.Embed(ctx, currentMessage)
		if err == nil {
			retriever := memory.NewRetriever(a.memoryStore)
			scored, err = retriever.Retrieve(ctx, agentID, queryEmb, memory.RetrieveOptions{Limit: memLimit})
			if err != nil {
				scored = nil // non-fatal
			}
		}
	} else {
		// Fallback: load recent memories when no embedding provider is available
		recent, err := a.memoryStore.ListRecent(ctx, agentID, memLimit)
		if err == nil {
			for i, m := range recent {
				// Assign a decaying score so more recent memories rank higher
				score := float32(1.0) - float32(i)*float32(1.0)/float32(len(recent)+1)
				scored = append(scored, memory.ScoredMemory{
					Memory: m,
					Score:  score,
				})
			}
		}
	}

	// Deduplicate: pinned take priority
	pinnedIDs := make(map[int64]bool, len(pinned))
	for _, p := range pinned {
		pinnedIDs[p.ID] = true
	}

	// Build combined list: pinned first, then scored (deduped)
	var entries []memEntry
	for _, p := range pinned {
		entries = append(entries, memEntry{
			category: p.Category,
			content:  p.Content,
			pinned:   true,
		})
	}
	for _, s := range scored {
		if !pinnedIDs[s.ID] {
			entries = append(entries, memEntry{
				category: s.Category,
				content:  s.Content,
				pinned:   false,
				score:    s.Score,
			})
		}
	}

	if len(entries) == 0 {
		// Even with no memories, return the nudge if present.
		if candidateNudge != "" {
			block := "\n\n## Memories\n" + candidateNudge + "\n"
			return block, EstimateTokens(block), nil
		}
		return "", 0, nil
	}

	// prependNudge adds the candidate nudge to a formatted memory block.
	prependNudge := func(block string) string {
		if candidateNudge == "" {
			return block
		}
		// Insert nudge after the "## Memories\n" header.
		const header = "\n\n## Memories\n"
		if strings.HasPrefix(block, header) {
			return header + candidateNudge + "\n" + block[len(header):]
		}
		return block
	}

	// Build text and enforce budget by dropping lowest-scored non-pinned entries
	for {
		block := prependNudge(formatMemoryBlock(entries))
		tokens := EstimateTokens(block)
		if tokens <= budget {
			return block, tokens, nil
		}

		// Try dropping lowest-scored non-pinned entry
		dropIdx := -1
		var lowestScore float32 = 2.0 // higher than any real score
		for i := len(entries) - 1; i >= 0; i-- {
			if !entries[i].pinned && entries[i].score < lowestScore {
				lowestScore = entries[i].score
				dropIdx = i
			}
		}

		if dropIdx < 0 {
			// Only pinned memories remain — include them even if over budget (safety valve)
			block = prependNudge(formatMemoryBlock(entries))
			return block, EstimateTokens(block), nil
		}

		entries = append(entries[:dropIdx], entries[dropIdx+1:]...)
	}
}

// formatMemoryBlock formats memory entries into a markdown block.
func formatMemoryBlock(entries []memEntry) string {
	var b strings.Builder
	b.WriteString("\n\n## Memories\n")
	for _, e := range entries {
		b.WriteString(fmt.Sprintf("- [%s] %s\n", e.category, e.content))
	}
	return b.String()
}

// buildHistory loads and trims history to fit within the token budget.
func (a *Assembler) buildHistory(ctx context.Context, config types.AgentConfig,
	opts AssembleOptions, budget int) ([]models.ChatMessage, int, error) {

	limit := config.HistoryLimit
	if limit <= 0 {
		limit = history.DefaultLimit
	}

	channel := opts.Channel
	if channel == "" {
		channel = "internal"
	}

	recent, err := a.historyStore.Recent(ctx, config.ID, channel, opts.ChannelID, limit)
	if err != nil {
		return nil, 0, nil // non-fatal
	}

	// Load active summary for this conversation.
	var summaryMsg *models.ChatMessage
	if summary, err := a.historyStore.ActiveSummary(ctx, config.ID, channel, opts.ChannelID); err == nil && summary != nil {
		summaryMsg = &models.ChatMessage{
			Role:    "system",
			Content: "Conversation summary (older messages have been compressed):\n\n" + summary.Content,
		}
	}

	// Convert to chat messages, handling both old-format and new-format entries.
	//
	// Old-format (backward compat — filter out):
	//   - tool entries with empty ToolCallID
	//   - assistant entries starting with "[tool_calls]"
	//   - empty assistant entries with no ToolCallsJSON
	//
	// New-format:
	//   - tool entries with ToolCallID → reconstruct with ToolCallID
	//   - assistant entries with ToolCallsJSON → reconstruct with ToolCalls
	//
	// Merge consecutive same-role user/assistant messages (without ToolCalls)
	// to avoid API rejections from duplicate roles.
	var messages []models.ChatMessage
	for _, entry := range recent {
		switch entry.Role {
		case "tool":
			if entry.ToolCallID == "" {
				// Old format: no tool_call_id, skip to avoid API errors
				continue
			}
			messages = append(messages, models.ChatMessage{
				Role:       "tool",
				Content:    entry.Content,
				ToolCallID: entry.ToolCallID,
			})

		case "assistant":
			if entry.ToolCallsJSON != "" {
				// New format: reconstruct assistant message with tool calls
				var toolCalls []models.ToolUse
				if json.Unmarshal([]byte(entry.ToolCallsJSON), &toolCalls) == nil && len(toolCalls) > 0 {
					messages = append(messages, models.ChatMessage{
						Role:      "assistant",
						Content:   entry.Content,
						ToolCalls: toolCalls,
					})
					continue
				}
			}
			// Old format: skip mangled "[tool_calls]" entries
			if strings.HasPrefix(entry.Content, "[tool_calls]") {
				continue
			}
			// Skip empty assistant entries (old tool_call-only messages)
			if entry.Content == "" {
				continue
			}
			msg := models.ChatMessage{
				Role:    "assistant",
				Content: entry.Content,
			}
			if entry.Attachments != "" {
				var atts []types.Attachment
				if json.Unmarshal([]byte(entry.Attachments), &atts) == nil {
					msg.Attachments = atts
				}
			}
			// Merge consecutive same-role messages
			if len(messages) > 0 && messages[len(messages)-1].Role == "assistant" && len(messages[len(messages)-1].ToolCalls) == 0 {
				prev := &messages[len(messages)-1]
				prev.Content += "\n" + msg.Content
				prev.Attachments = append(prev.Attachments, msg.Attachments...)
			} else {
				messages = append(messages, msg)
			}

		default: // "user", "summary", and others
			role := entry.Role
			// Compression summaries use "summary" role internally; map to
			// "system" so providers accept them.
			if role == "summary" {
				role = "system"
			}
			msg := models.ChatMessage{
				Role:    role,
				Content: entry.Content,
			}
			if entry.Attachments != "" {
				var atts []types.Attachment
				if json.Unmarshal([]byte(entry.Attachments), &atts) == nil {
					msg.Attachments = atts
				}
			}
			// Merge consecutive same-role messages
			if len(messages) > 0 && messages[len(messages)-1].Role == msg.Role {
				prev := &messages[len(messages)-1]
				prev.Content += "\n" + msg.Content
				prev.Attachments = append(prev.Attachments, msg.Attachments...)
			} else {
				messages = append(messages, msg)
			}
		}
	}

	// Sanitize tool message integrity before budget trimming.
	messages = sanitizeToolMessages(messages)

	// Estimate total tokens
	totalTokens := 0
	for _, m := range messages {
		totalTokens += EstimateTokens(m.Content)
	}

	// Safety net: if no summary and compression should trigger, compress synchronously.
	if summaryMsg == nil && a.compressor != nil {
		var compCfg types.CompressionConfig
		if config.CompressionJSON != "" {
			json.Unmarshal([]byte(config.CompressionJSON), &compCfg)
		}
		compCfg = types.NormalizeCompressionConfig(compCfg)
		if compCfg.Enabled && a.compressor.ShouldCompress(len(messages), totalTokens, budget, compCfg) {
			if err := a.compressor.TryCompress(ctx, config.ID, channel, opts.ChannelID, compCfg, config); err == nil {
				// Reload — compression succeeded. Recursive call with same budget.
				return a.buildHistory(ctx, config, opts, budget)
			}
			// On failure, fall through to drop-oldest behavior.
		}
	}

	// Drop oldest messages if over budget
	for totalTokens > budget && len(messages) > 0 {
		totalTokens -= EstimateTokens(messages[0].Content)
		messages = messages[1:]
	}

	// Orphan cleanup: drop leading tool-role messages whose parent assistant
	// message was trimmed away during budget enforcement.
	for len(messages) > 0 && messages[0].Role == "tool" {
		totalTokens -= EstimateTokens(messages[0].Content)
		messages = messages[1:]
	}

	// Inject conversation summary at the start of history if available.
	if summaryMsg != nil {
		messages = append([]models.ChatMessage{*summaryMsg}, messages...)
		totalTokens += EstimateTokens(summaryMsg.Content)
	}

	return messages, totalTokens, nil
}

// sanitizeToolMessages validates tool message integrity in the conversation history.
// It ensures:
//   - Every assistant message with ToolCalls is followed by matching tool results
//   - Every tool-role message has a preceding assistant with a matching tool_call
//
// If an assistant message has ToolCalls but incomplete/missing tool results,
// the ToolCalls are stripped (keeping text content). Orphaned tool messages
// without a matching assistant are removed.
func sanitizeToolMessages(messages []models.ChatMessage) []models.ChatMessage {
	var result []models.ChatMessage

	for i := 0; i < len(messages); i++ {
		msg := messages[i]

		switch msg.Role {
		case "assistant":
			if len(msg.ToolCalls) == 0 {
				result = append(result, msg)
				continue
			}

			// Collect expected tool_call IDs.
			expectedIDs := make(map[string]bool, len(msg.ToolCalls))
			for _, tc := range msg.ToolCalls {
				expectedIDs[tc.ID] = true
			}

			// Look ahead for matching tool results immediately following.
			foundIDs := make(map[string]bool)
			j := i + 1
			for j < len(messages) && messages[j].Role == "tool" {
				if expectedIDs[messages[j].ToolCallID] {
					foundIDs[messages[j].ToolCallID] = true
				}
				j++
			}

			// Check if all expected tool results are present.
			if len(foundIDs) == len(expectedIDs) {
				// Complete pair — keep assistant with ToolCalls and all tool results.
				result = append(result, msg)
				for k := i + 1; k < j; k++ {
					result = append(result, messages[k])
				}
			} else {
				// Incomplete pair — strip ToolCalls from assistant, drop tool results.
				slog.Warn("sanitizeToolMessages: incomplete tool pair, stripping tool calls",
					"expected", len(expectedIDs), "found", len(foundIDs))
				if msg.Content != "" {
					result = append(result, models.ChatMessage{
						Role:    "assistant",
						Content: msg.Content,
					})
				}
				// Skip the orphaned tool results.
			}
			i = j - 1 // Advance past the tool results.

		case "tool":
			// Orphaned tool message — no preceding assistant with matching ToolCalls.
			// Check if the previous result message has a matching assistant.
			hasParent := false
			for k := len(result) - 1; k >= 0; k-- {
				if result[k].Role == "assistant" && len(result[k].ToolCalls) > 0 {
					for _, tc := range result[k].ToolCalls {
						if tc.ID == msg.ToolCallID {
							hasParent = true
							break
						}
					}
					break
				}
				if result[k].Role != "tool" {
					break
				}
			}
			if hasParent {
				result = append(result, msg)
			} else {
				slog.Debug("sanitizeToolMessages: dropping orphaned tool message",
					"tool_call_id", msg.ToolCallID)
			}

		default:
			result = append(result, msg)
		}
	}

	return result
}
