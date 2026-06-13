package compression

import (
	"strings"
	"testing"

	"github.com/kkjorsvik/kyvik/internal/history"
	"github.com/kkjorsvik/kyvik/internal/memory"
)

func TestBuildPrompt_BasicMessagesAndMemories(t *testing.T) {
	messages := []history.HistoryEntry{
		{Role: "user", Content: "Hello, can you help me?"},
		{Role: "assistant", Content: "Of course! What do you need?"},
		{Role: "user", Content: "Please update the config file."},
	}
	memories := []memory.Memory{
		{Content: "User prefers YAML config format"},
		{Content: "Config file is at /etc/app/config.yaml"},
	}

	sys, user := BuildPrompt(messages, memories, nil)

	// System prompt should be the constant.
	if sys != systemPrompt {
		t.Error("system prompt does not match expected constant")
	}

	// User prompt should contain memories.
	if !strings.Contains(user, "EXISTING MEMORIES:") {
		t.Error("user prompt missing EXISTING MEMORIES section")
	}
	if !strings.Contains(user, "- User prefers YAML config format") {
		t.Error("user prompt missing first memory")
	}
	if !strings.Contains(user, "- Config file is at /etc/app/config.yaml") {
		t.Error("user prompt missing second memory")
	}

	// Should NOT contain previous summary section.
	if strings.Contains(user, "PREVIOUS SUMMARY:") {
		t.Error("user prompt should not contain PREVIOUS SUMMARY when nil")
	}

	// Should contain conversation.
	if !strings.Contains(user, "CONVERSATION TO COMPRESS:") {
		t.Error("user prompt missing CONVERSATION TO COMPRESS section")
	}
	if !strings.Contains(user, "user: Hello, can you help me?") {
		t.Error("user prompt missing first message")
	}
	if !strings.Contains(user, "assistant: Of course! What do you need?") {
		t.Error("user prompt missing second message")
	}
}

func TestBuildPrompt_WithPreviousSummary(t *testing.T) {
	messages := []history.HistoryEntry{
		{Role: "user", Content: "Continue the work."},
	}
	prev := &history.HistoryEntry{
		Content: "## Resolved\nSetup completed.\n## Open\nDeployment pending.",
	}

	_, user := BuildPrompt(messages, nil, prev)

	if !strings.Contains(user, "PREVIOUS SUMMARY:") {
		t.Error("user prompt missing PREVIOUS SUMMARY section")
	}
	if !strings.Contains(user, "## Resolved\nSetup completed.") {
		t.Error("user prompt missing previous summary content")
	}
}

func TestBuildPrompt_EmptyMemories(t *testing.T) {
	messages := []history.HistoryEntry{
		{Role: "user", Content: "Test message"},
	}

	_, user := BuildPrompt(messages, nil, nil)

	if !strings.Contains(user, "(none)") {
		t.Error("user prompt should show (none) for empty memories")
	}

	_, user2 := BuildPrompt(messages, []memory.Memory{}, nil)
	if !strings.Contains(user2, "(none)") {
		t.Error("user prompt should show (none) for zero-length memories slice")
	}
}
