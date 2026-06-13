// Package models defines the pluggable LLM provider interface.
// Each provider (OpenRouter, Anthropic, Ollama, vLLM, etc.)
// implements this interface. New providers can be added without
// touching core code.
package models

import (
	"context"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

// CompletionRequest represents a request to an LLM.
type CompletionRequest struct {
	Model          string           `json:"model"`
	Messages       []ChatMessage    `json:"messages"`
	MaxTokens      int              `json:"max_tokens,omitempty"`
	Temperature    float64          `json:"temperature,omitempty"`
	Tools          []ToolDefinition `json:"tools,omitempty"`
	ProviderIgnore []string         `json:"provider_ignore,omitempty"` // OpenRouter: upstream providers to skip
}

// ChatMessage represents a single message in a conversation.
type ChatMessage struct {
	Role        string             `json:"role"`
	Content     string             `json:"content"`
	Attachments []types.Attachment `json:"attachments,omitempty"` // File attachments for multimodal
	ToolCalls   []ToolUse          `json:"tool_calls,omitempty"`  // For assistant messages with tool calls
	ToolCallID  string             `json:"tool_call_id,omitempty"`// For tool-result messages
}

// CompletionResponse represents the response from an LLM.
type CompletionResponse struct {
	Content                string    `json:"content"`
	ToolCalls              []ToolUse `json:"tool_calls,omitempty"`
	StopReason             string    `json:"stop_reason,omitempty"` // Normalized: "end", "tool_use", "max_tokens"
	TokensIn               int64     `json:"tokens_in"`
	TokensOut              int64     `json:"tokens_out"`
	Cost                   float64   `json:"cost"`
	Model                  string    `json:"model"`
	UnprocessedToolResults bool      `json:"unprocessed_tool_results,omitempty"` // Set when budget exceeded before tool results could be incorporated.
}

// StreamChunk represents a single chunk in a streaming response.
type StreamChunk struct {
	Content string `json:"content"`
	Done    bool   `json:"done"`
	Error   string `json:"error,omitempty"`
}

// ToolDefinition describes a tool available to the model.
type ToolDefinition struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  interface{} `json:"parameters"`
}

// ToolUse represents a tool call requested by the model.
type ToolUse struct {
	ID         string      `json:"id"`
	Name       string      `json:"name"`
	Parameters interface{} `json:"parameters"`
}

// ModelInfo describes an available model.
type ModelInfo struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Provider      string  `json:"provider"`
	ContextSize   int     `json:"context_size"`
	CostPerMInput float64 `json:"cost_per_m_input"` // Cost per million input tokens
	CostPerMOut   float64 `json:"cost_per_m_out"`   // Cost per million output tokens
}

// Provider defines the contract for LLM provider adapters.
type Provider interface {
	// Complete sends a request and returns the full response.
	Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error)

	// Stream sends a request and returns a channel of response chunks.
	Stream(ctx context.Context, req CompletionRequest) (<-chan StreamChunk, error)

	// ListModels returns available models from this provider.
	ListModels(ctx context.Context) ([]ModelInfo, error)

	// Name returns the provider identifier (e.g., "openrouter", "ollama").
	Name() string
}
