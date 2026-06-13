package ktp

import (
	"encoding/json"
	"strings"
)

// ToolNameSeparator is the delimiter between tool and action in model-facing names.
const ToolNameSeparator = "__"

// ModelToolCall represents a tool call from a model response, using the
// "tool__action" naming convention for model-facing tool names.
type ModelToolCall struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`      // e.g. "file__read"
	Arguments map[string]any `json:"arguments"`
}

// ModelToolResult represents a tool execution result to be sent back to the model.
type ModelToolResult struct {
	ToolCallID string `json:"tool_call_id"`
	Content    string `json:"content"`  // JSON result or error message
	IsError    bool   `json:"is_error"`
}

// JoinToolAction combines a tool name and action into a model-facing name.
// Example: "file" + "read" → "file__read"
func JoinToolAction(tool, action string) string {
	if action == "" {
		return tool
	}
	return tool + ToolNameSeparator + action
}

// SplitToolAction splits a model-facing name into tool and action parts.
// Only splits on the first separator occurrence.
// Examples:
//
//	"file__read"  → ("file", "read")
//	"search"      → ("search", "")
//	"a__b__c"     → ("a", "b__c")
func SplitToolAction(name string) (tool, action string) {
	tool, action, _ = strings.Cut(name, ToolNameSeparator)
	return tool, action
}

// ConvertToKTPRequest converts a ModelToolCall into a KTP ToolRequest.
// The call's Name is split via SplitToolAction to extract tool and action.
func ConvertToKTPRequest(agentID string, call ModelToolCall) ToolRequest {
	tool, action := SplitToolAction(call.Name)
	return NewToolRequest(agentID, tool, action, call.Arguments)
}

// ConvertToModelResult converts a KTP ToolResponse into a ModelToolResult
// suitable for sending back to the model.
func ConvertToModelResult(callID string, resp *ToolResponse) ModelToolResult {
	if resp == nil {
		return ModelToolResult{
			ToolCallID: callID,
			Content:    "Error: nil ToolResponse",
			IsError:    true,
		}
	}

	if !resp.Success {
		return ModelToolResult{
			ToolCallID: callID,
			Content:    "Error: " + resp.Error,
			IsError:    true,
		}
	}

	data, err := json.Marshal(resp.Result)
	if err != nil {
		return ModelToolResult{
			ToolCallID: callID,
			Content:    "Error: failed to marshal result: " + err.Error(),
			IsError:    true,
		}
	}

	return ModelToolResult{
		ToolCallID: callID,
		Content:    string(data),
		IsError:    false,
	}
}
