package compression

import (
	"fmt"
	"strings"

	"github.com/kkjorsvik/kyvik/internal/history"
	"github.com/kkjorsvik/kyvik/internal/memory"
)

const systemPrompt = `You are a conversation compressor for an AI agent. Your job is to produce a concise summary that lets the agent continue the conversation without losing important context.

Rules:
- Clearly distinguish RESOLVED items from OPEN/PENDING items
- Capture decisions made and why, not the deliberation process
- Record the current state of things, not the full history of how we got here
- Preserve specific details needed to continue work (names, IDs, file paths, numbers, dates)
- Drop: pleasantries, repeated attempts, verbose tool output, debugging back-and-forth that led to a resolution
- Do NOT include facts that appear in the EXISTING MEMORIES section — the agent already has access to those

Format the summary with these sections:
## Resolved
Items that were discussed and completed/closed.

## Open
Items still in progress or pending action.

## Decisions
Key decisions made during the conversation and their reasoning.

## Context
Important background details needed to continue the conversation.`

// BuildPrompt constructs the system and user prompts for conversation compression.
func BuildPrompt(messages []history.HistoryEntry, memories []memory.Memory, previousSummary *history.HistoryEntry) (string, string) {
	var b strings.Builder

	b.WriteString("EXISTING MEMORIES:\n")
	if len(memories) == 0 {
		b.WriteString("(none)\n")
	} else {
		for _, m := range memories {
			fmt.Fprintf(&b, "- %s\n", m.Content)
		}
	}
	b.WriteString("\n")

	if previousSummary != nil {
		b.WriteString("PREVIOUS SUMMARY:\n")
		b.WriteString(previousSummary.Content)
		b.WriteString("\n\n")
	}

	b.WriteString("CONVERSATION TO COMPRESS:\n")
	for _, m := range messages {
		fmt.Fprintf(&b, "%s: %s\n", m.Role, m.Content)
	}
	b.WriteString("\nProduce a compressed summary that captures everything the agent needs to continue this conversation effectively.")

	return systemPrompt, b.String()
}
