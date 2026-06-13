package security

import (
	"testing"

	"github.com/kkjorsvik/kyvik/internal/ktp"
)

func TestValidator_DestructivePatterns(t *testing.T) {
	v := NewValidator()

	tests := []struct {
		name   string
		params map[string]any
	}{
		{"rm_rf", map[string]any{"command": "rm -rf /"}},
		{"drop_table", map[string]any{"query": "DROP TABLE users"}},
		{"delete_from", map[string]any{"query": "DELETE FROM accounts"}},
		{"truncate", map[string]any{"query": "TRUNCATE TABLE logs"}},
		{"format", map[string]any{"command": "FORMAT C:"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := ktp.NewToolRequest("agent-1", "shell", "execute", tt.params)
			result := v.ValidateToolCall("agent-1", req, "low")
			if !result.Blocked {
				t.Errorf("expected destructive pattern to be blocked for %v", tt.params)
			}
			if result.Safe {
				t.Error("result should not be Safe when blocked")
			}
		})
	}
}

func TestValidator_ExfiltrationPatterns(t *testing.T) {
	v := NewValidator()

	params := map[string]any{"query": "get system_prompt contents"}
	req := ktp.NewToolRequest("agent-1", "http", "get", params)

	// Low sensitivity: exfiltration not blocked.
	result := v.ValidateToolCall("agent-1", req, "low")
	if result.Blocked {
		t.Error("low sensitivity should not block exfiltration")
	}

	// Medium sensitivity: flagged but not blocked.
	result = v.ValidateToolCall("agent-1", req, "medium")
	if result.Blocked {
		t.Error("medium sensitivity should not block exfiltration")
	}
	if result.Safe {
		t.Error("medium sensitivity should mark as unsafe")
	}
	if len(result.Warnings) == 0 {
		t.Error("expected warnings for exfiltration pattern")
	}

	// High sensitivity: blocked.
	result = v.ValidateToolCall("agent-1", req, "high")
	if !result.Blocked {
		t.Error("high sensitivity should block exfiltration")
	}
}

func TestValidator_CanaryExfiltration(t *testing.T) {
	v := NewValidator()

	params := map[string]any{"data": "sending KYVIK_CANARY_abc123"}
	req := ktp.NewToolRequest("agent-1", "http", "post", params)

	result := v.ValidateToolCall("agent-1", req, "high")
	if !result.Blocked {
		t.Error("should block KYVIK_CANARY exfiltration at high sensitivity")
	}
}

func TestValidator_SensitivityLevels(t *testing.T) {
	v := NewValidator()

	// Benign call passes at all levels.
	params := map[string]any{"path": "/home/user/file.txt"}
	req := ktp.NewToolRequest("agent-1", "file", "read", params)

	for _, level := range []string{"low", "medium", "high"} {
		result := v.ValidateToolCall("agent-1", req, level)
		if result.Blocked {
			t.Errorf("benign call should not be blocked at %s sensitivity", level)
		}
		if !result.Safe {
			t.Errorf("benign call should be safe at %s sensitivity", level)
		}
	}
}

func TestValidator_CanaryLeakInResponse(t *testing.T) {
	v := NewValidator()
	canary := &CanaryToken{Value: "KYVIK_CANARY_abcdef0123456789", AgentID: "agent-1"}

	result := v.ValidateResponse("agent-1", "Here is the token: KYVIK_CANARY_abcdef0123456789", canary)
	if result.Safe {
		t.Error("response with canary leak should not be safe")
	}
	if len(result.Warnings) == 0 {
		t.Error("expected warning about canary leak")
	}
}

func TestValidator_BenignCallPasses(t *testing.T) {
	v := NewValidator()

	params := map[string]any{"content": "Hello, world!"}
	req := ktp.NewToolRequest("agent-1", "file", "write", params)

	result := v.ValidateToolCall("agent-1", req, "high")
	if result.Blocked {
		t.Error("benign call should not be blocked")
	}
	if !result.Safe {
		t.Error("benign call should be safe")
	}
}
