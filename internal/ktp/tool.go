package ktp

import "context"

// Tool is the interface that all KTP-compliant tools must implement.
type Tool interface {
	// Declaration returns the tool's capabilities and action specifications.
	Declaration() ToolDeclaration

	// Execute performs the requested action within a sandbox.
	Execute(ctx context.Context, req ToolRequest) (*ToolResponse, error)
}

// InlineTool is an optional interface for tools that run in-process rather than
// in a sandbox. Tools that implement this and return true from Inline() bypass
// sandbox execution (e.g. the memory tool, which only accesses local state).
type InlineTool interface {
	Tool
	Inline() bool
}
