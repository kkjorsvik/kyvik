package ktp_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/ktp"
)

func TestJoinToolAction(t *testing.T) {
	tests := []struct {
		tool, action, want string
	}{
		{"file", "read", "file__read"},
		{"search", "", "search"},
		{"a", "b__c", "a__b__c"},
	}
	for _, tt := range tests {
		got := ktp.JoinToolAction(tt.tool, tt.action)
		if got != tt.want {
			t.Errorf("JoinToolAction(%q, %q) = %q, want %q", tt.tool, tt.action, got, tt.want)
		}
	}
}

func TestSplitToolAction_WithSeparator(t *testing.T) {
	tool, action := ktp.SplitToolAction("file__read")
	if tool != "file" {
		t.Errorf("tool = %q, want %q", tool, "file")
	}
	if action != "read" {
		t.Errorf("action = %q, want %q", action, "read")
	}
}

func TestSplitToolAction_WithoutSeparator(t *testing.T) {
	tool, action := ktp.SplitToolAction("search")
	if tool != "search" {
		t.Errorf("tool = %q, want %q", tool, "search")
	}
	if action != "" {
		t.Errorf("action = %q, want empty string", action)
	}
}

func TestSplitToolAction_MultipleSeparators(t *testing.T) {
	tool, action := ktp.SplitToolAction("a__b__c")
	if tool != "a" {
		t.Errorf("tool = %q, want %q", tool, "a")
	}
	if action != "b__c" {
		t.Errorf("action = %q, want %q", action, "b__c")
	}
}

func TestConvertToKTPRequest(t *testing.T) {
	call := ktp.ModelToolCall{
		ID:        "call_123",
		Name:      "echo__echo",
		Arguments: map[string]any{"message": "hello"},
	}
	req := ktp.ConvertToKTPRequest("agent-1", call)

	if req.AgentID != "agent-1" {
		t.Errorf("AgentID = %q, want %q", req.AgentID, "agent-1")
	}
	if req.Tool != "echo" {
		t.Errorf("Tool = %q, want %q", req.Tool, "echo")
	}
	if req.Action != "echo" {
		t.Errorf("Action = %q, want %q", req.Action, "echo")
	}
	if req.Parameters["message"] != "hello" {
		t.Errorf("Parameters[message] = %v, want %q", req.Parameters["message"], "hello")
	}
	// ULID should be valid (26 chars)
	if len(req.ID) != 26 {
		t.Errorf("ID length = %d, want 26 (ULID)", len(req.ID))
	}
	// Timestamp should be recent
	if time.Since(req.Timestamp) > 5*time.Second {
		t.Errorf("Timestamp %v is not recent", req.Timestamp)
	}
}

func TestConvertToKTPRequest_NoSeparator(t *testing.T) {
	call := ktp.ModelToolCall{
		ID:        "call_456",
		Name:      "search",
		Arguments: map[string]any{"query": "test"},
	}
	req := ktp.ConvertToKTPRequest("agent-2", call)

	if req.Tool != "search" {
		t.Errorf("Tool = %q, want %q", req.Tool, "search")
	}
	if req.Action != "" {
		t.Errorf("Action = %q, want empty string", req.Action)
	}
}

func TestConvertToModelResult_Success(t *testing.T) {
	resp := &ktp.ToolResponse{
		RequestID: "req-1",
		Success:   true,
		Result:    map[string]any{"echo": "hello"},
	}

	result := ktp.ConvertToModelResult("call_123", resp)

	if result.ToolCallID != "call_123" {
		t.Errorf("ToolCallID = %q, want %q", result.ToolCallID, "call_123")
	}
	if result.IsError {
		t.Error("IsError = true, want false")
	}

	// Content should be valid JSON
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result.Content), &parsed); err != nil {
		t.Fatalf("Content is not valid JSON: %v", err)
	}
	if parsed["echo"] != "hello" {
		t.Errorf("parsed echo = %v, want %q", parsed["echo"], "hello")
	}
}

func TestConvertToModelResult_Error(t *testing.T) {
	resp := &ktp.ToolResponse{
		RequestID: "req-2",
		Success:   false,
		Error:     "permission denied",
	}

	result := ktp.ConvertToModelResult("call_456", resp)

	if result.ToolCallID != "call_456" {
		t.Errorf("ToolCallID = %q, want %q", result.ToolCallID, "call_456")
	}
	if !result.IsError {
		t.Error("IsError = false, want true")
	}
	if !strings.Contains(result.Content, "permission denied") {
		t.Errorf("Content = %q, want it to contain %q", result.Content, "permission denied")
	}
}

func TestConvertToModelResult_NilResult(t *testing.T) {
	resp := &ktp.ToolResponse{
		RequestID: "req-3",
		Success:   true,
		Result:    nil,
	}

	result := ktp.ConvertToModelResult("call_789", resp)

	if result.IsError {
		t.Error("IsError = true, want false")
	}
	if result.Content != "null" {
		t.Errorf("Content = %q, want %q", result.Content, "null")
	}
}
