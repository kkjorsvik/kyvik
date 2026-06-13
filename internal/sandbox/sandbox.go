// Package sandbox implements execution isolation for agent tool calls.
// Each agent gets a sandboxed environment with restricted resources,
// file system isolation, and network controls.
package sandbox

import (
	"time"

	"github.com/oklog/ulid/v2"
)

// Sandbox represents an active sandboxed execution environment for a single agent.
type Sandbox struct {
	ID                   string            // ULID
	AgentID              string
	Config               SandboxConfig
	Workspace            string            // Absolute path: WorkspaceRoot/{agent_id}
	HTTPAllowedHosts     []string          // Allowed HTTP hosts for this agent
	ShellAllowedCommands []string          // Allowed shell commands for this agent
	Secrets              map[string]string // Resolved secrets for env injection (key → plaintext)
	CreatedAt            time.Time
}

// NewSandbox creates a Sandbox with a fresh ULID and current timestamp.
func NewSandbox(agentID string, cfg SandboxConfig, workspace string) *Sandbox {
	return &Sandbox{
		ID:        ulid.Make().String(),
		AgentID:   agentID,
		Config:    cfg,
		Workspace: workspace,
		CreatedAt: time.Now(),
	}
}
