package security

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

// CanaryToken is a unique marker injected into system prompts to detect leaks.
type CanaryToken struct {
	Value   string
	AgentID string
}

// GenerateCanary creates a fresh canary token with 8 random hex bytes.
func GenerateCanary(agentID string) (CanaryToken, error) {
	b := make([]byte, 8)
	n, err := rand.Read(b)
	if err != nil {
		return CanaryToken{}, fmt.Errorf("canary random generation failed: %w", err)
	}
	if n != len(b) {
		return CanaryToken{}, fmt.Errorf("canary random generation: short read (%d/%d)", n, len(b))
	}
	return CanaryToken{
		Value:   "KYVIK_CANARY_" + hex.EncodeToString(b),
		AgentID: agentID,
	}, nil
}

// InjectCanary appends the canary token as an HTML comment to the system prompt.
func InjectCanary(systemPrompt string, token CanaryToken) string {
	return fmt.Sprintf("%s\n<!-- %s -->", systemPrompt, token.Value)
}

// CheckCanaryLeak returns true if the canary token value appears in the content.
func CheckCanaryLeak(content string, token CanaryToken) bool {
	return strings.Contains(content, token.Value)
}
