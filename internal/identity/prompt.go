package identity

import (
	"strings"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

// DefaultSystemPrompt is used when no soul, identity, or system prompt is configured.
const DefaultSystemPrompt = "You are a helpful AI assistant managed by Kyvik."

// BuildSystemPrompt assembles the system prompt from an agent's configuration.
// Priority:
//  1. If SoulContent and/or IdentityContent are set, concatenate them.
//  2. Else if SystemPrompt is set, use it (backward compatibility).
//  3. Else use the default prompt.
func BuildSystemPrompt(config types.AgentConfig) string {
	var parts []string

	if config.SoulContent != "" {
		parts = append(parts, strings.TrimSpace(config.SoulContent))
	}
	if config.IdentityContent != "" {
		parts = append(parts, strings.TrimSpace(config.IdentityContent))
	}

	if len(parts) > 0 {
		return strings.Join(parts, "\n\n")
	}

	if config.SystemPrompt != "" {
		return config.SystemPrompt
	}

	return DefaultSystemPrompt
}
