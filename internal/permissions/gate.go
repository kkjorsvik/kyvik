// Package permissions implements the deny-by-default capability system.
// Every tool invocation passes through the Permission Gate.
// Denied calls are logged and blocked.
package permissions

import (
	"context"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

// Decision represents the outcome of a permission check.
type Decision struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason"` // Why it was allowed or denied
	Rule    string `json:"rule"`   // Which rule matched (template name, override ID, etc.)
}

// Template defines a named set of capabilities.
// Built-in templates: reader, worker, operator, admin, guide.
type Template struct {
	Name         string             `json:"name"`
	Description  string             `json:"description"`
	Capabilities []types.Capability `json:"capabilities"`
}

// Override represents a granular permission modification for a specific agent.
type Override struct {
	AgentID    string           `json:"agent_id"`
	Capability types.Capability `json:"capability"`
	Grant      bool             `json:"grant"` // true = allow, false = deny (overrides template)
}

// Gate is the central permission enforcement point.
// Every tool call flows through the Gate before execution.
type Gate interface {
	// Check evaluates whether an agent is allowed to perform a tool call.
	// Returns a Decision and logs the check to the audit trail.
	Check(ctx context.Context, agentID string, call types.ToolCall) (*Decision, error)

	// GetAgentCapabilities returns the effective permissions for an agent
	// (template + overrides merged).
	GetAgentCapabilities(ctx context.Context, agentID string) ([]types.Capability, error)

	// LoadTemplate retrieves a permission template by name.
	LoadTemplate(ctx context.Context, name string) (*Template, error)

	// ListTemplates returns all available permission templates.
	ListTemplates(ctx context.Context) ([]Template, error)

	// AddOverride adds a granular permission override for an agent.
	AddOverride(ctx context.Context, override Override) error

	// RemoveOverride removes a specific override.
	RemoveOverride(ctx context.Context, agentID string, capability types.Capability) error

	// ListOverrides returns all overrides for an agent.
	ListOverrides(ctx context.Context, agentID string) ([]Override, error)

	// RemoveAllOverrides removes all overrides for an agent.
	RemoveAllOverrides(ctx context.Context, agentID string) error
}

// Built-in templates.
var (
	ReaderTemplate = Template{
		Name:        "reader",
		Description: "Read-only access. Can query data but cannot modify anything.",
		Capabilities: []types.Capability{
			{Tool: "filesystem", Action: "read", Resource: "*"},
			{Tool: "http", Action: "get", Resource: "*"},
			{Tool: "database", Action: "select", Resource: "*"},
		},
	}

	WorkerTemplate = Template{
		Name:        "worker",
		Description: "Read and write within defined boundaries. Can create and modify resources.",
		Capabilities: []types.Capability{
			{Tool: "filesystem", Action: "read", Resource: "*"},
			{Tool: "filesystem", Action: "write", Resource: "*"},
			{Tool: "http", Action: "get", Resource: "*"},
			{Tool: "http", Action: "post", Resource: "*"},
			{Tool: "database", Action: "select", Resource: "*"},
			{Tool: "database", Action: "insert", Resource: "*"},
			{Tool: "database", Action: "update", Resource: "*"},
		},
	}

	OperatorTemplate = Template{
		Name:        "operator",
		Description: "Automate and manage. Full read-write plus delete and execute — all within the sandbox.",
		Capabilities: []types.Capability{
			{Tool: "filesystem", Action: "read", Resource: "*"},
			{Tool: "filesystem", Action: "write", Resource: "*"},
			{Tool: "filesystem", Action: "delete", Resource: "*"},
			{Tool: "http", Action: "get", Resource: "*"},
			{Tool: "http", Action: "post", Resource: "*"},
			{Tool: "http", Action: "delete", Resource: "*"},
			{Tool: "database", Action: "select", Resource: "*"},
			{Tool: "database", Action: "insert", Resource: "*"},
			{Tool: "database", Action: "update", Resource: "*"},
			{Tool: "database", Action: "delete", Resource: "*"},
			{Tool: "shell", Action: "execute", Resource: "*"},
			{Tool: "code_exec", Action: "execute", Resource: "*"},
		},
	}

	AdminTemplate = Template{
		Name:        "admin",
		Description: "Full access. All capabilities granted. Still audited and sandboxed.",
		Capabilities: []types.Capability{
			{Tool: "*", Action: "*", Resource: "*"},
		},
	}

	GuideBasicTemplate = Template{
		Name:        "guide",
		Description: "Internal guide agent. Read-only access to Kyvik system docs and logs.",
		Capabilities: []types.Capability{
			{Tool: "kyvik", Action: "read", Resource: "docs"},
			{Tool: "kyvik", Action: "read", Resource: "logs"},
		},
	}

	GuideFullTemplate = Template{
		Name:        "guide",
		Description: "Internal guide agent. Read-only access to Kyvik system docs, logs, agent configs, and status.",
		Capabilities: []types.Capability{
			{Tool: "kyvik", Action: "read", Resource: "docs"},
			{Tool: "kyvik", Action: "read", Resource: "logs"},
			{Tool: "kyvik", Action: "read", Resource: "agents"},
			{Tool: "kyvik", Action: "read", Resource: "status"},
		},
	}
)
