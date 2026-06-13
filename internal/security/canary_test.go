package security

import (
	"strings"
	"testing"
)

func TestGenerateCanary_Format(t *testing.T) {
	token, err := GenerateCanary("agent-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(token.Value, "KYVIK_CANARY_") {
		t.Errorf("token should start with KYVIK_CANARY_, got %q", token.Value)
	}
	// KYVIK_CANARY_ (13 chars) + 16 hex chars = 29 chars
	if len(token.Value) != 29 {
		t.Errorf("expected token length 29, got %d: %q", len(token.Value), token.Value)
	}
	if token.AgentID != "agent-1" {
		t.Errorf("expected agent ID agent-1, got %q", token.AgentID)
	}
}

func TestGenerateCanary_Unique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		token, err := GenerateCanary("agent-1")
		if err != nil {
			t.Fatalf("unexpected error on iteration %d: %v", i, err)
		}
		if seen[token.Value] {
			t.Fatalf("duplicate canary token generated: %q", token.Value)
		}
		seen[token.Value] = true
	}
}

func TestInjectCanary(t *testing.T) {
	token := CanaryToken{Value: "KYVIK_CANARY_abcdef0123456789", AgentID: "agent-1"}
	prompt := "You are a helpful assistant."
	result := InjectCanary(prompt, token)

	if !strings.Contains(result, prompt) {
		t.Error("injected prompt should contain original prompt")
	}
	if !strings.Contains(result, "<!-- KYVIK_CANARY_abcdef0123456789 -->") {
		t.Error("injected prompt should contain canary comment")
	}
	if !strings.HasSuffix(result, "<!-- KYVIK_CANARY_abcdef0123456789 -->") {
		t.Error("canary should be appended at the end")
	}
}

func TestCheckCanaryLeak(t *testing.T) {
	token := CanaryToken{Value: "KYVIK_CANARY_abcdef0123456789", AgentID: "agent-1"}

	if !CheckCanaryLeak("The model leaked KYVIK_CANARY_abcdef0123456789 in output", token) {
		t.Error("should detect canary leak")
	}

	if CheckCanaryLeak("This is a normal response", token) {
		t.Error("should not detect canary in normal text")
	}
}

func TestCheckCanaryLeak_NoFalsePositiveOnPartial(t *testing.T) {
	token := CanaryToken{Value: "KYVIK_CANARY_abcdef0123456789", AgentID: "agent-1"}

	// Partial matches should NOT trigger.
	if CheckCanaryLeak("KYVIK_CANARY_different_value", token) {
		t.Error("partial canary match should not trigger leak detection")
	}
	if CheckCanaryLeak("KYVIK_CANARY_", token) {
		t.Error("prefix-only should not trigger leak detection")
	}
}
