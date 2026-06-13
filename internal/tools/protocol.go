// Package tools defines Kyvik's native tool protocol.
// Unlike MCP, permissions and audit are built into the protocol itself.
// Every tool declares its capabilities, and the framework enforces them.
package tools

import (
	"context"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

// Declaration describes what a tool does and what it needs.
// This is registered at startup and used by the Permission Gate
// to evaluate access requests.
type Declaration struct {
	Name         string             `json:"name"`
	Description  string             `json:"description"`
	Version      string             `json:"version"`
	Capabilities []types.Capability `json:"capabilities"` // What this tool needs to function
	Actions      []Action           `json:"actions"`      // What operations this tool exposes
}

// Action defines a single operation the tool can perform.
type Action struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  interface{} `json:"parameters"` // JSON Schema for validation
	Returns     interface{} `json:"returns"`    // JSON Schema for output
}

// Tool is the interface that all Kyvik tools must implement.
type Tool interface {
	// Declaration returns the tool's capability and action declarations.
	Declaration() Declaration

	// Execute performs the requested action within the sandbox.
	Execute(ctx context.Context, call types.ToolCall) (*types.ToolResult, error)
}

// Registry manages available tools and their declarations.
type Registry interface {
	// Register adds a tool to the registry.
	Register(tool Tool) error

	// Get retrieves a tool by name.
	Get(name string) (Tool, error)

	// List returns all registered tool declarations.
	List() []Declaration

	// GetDeclaration returns the declaration for a specific tool.
	GetDeclaration(name string) (*Declaration, error)
}
