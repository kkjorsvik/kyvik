package sandbox

import (
	"context"
	"fmt"
	"sync"

	"github.com/kkjorsvik/kyvik/internal/ktp"
)

// KTPAdapter bridges the sandbox Manager to the ktp.SandboxExecutor interface.
// It caches active sandboxes by ID for ExecuteInSandbox lookups.
type KTPAdapter struct {
	mgr       *Manager
	sandboxes map[string]*Sandbox // sandboxID → *Sandbox
	mu        sync.Mutex
}

// Compile-time check that KTPAdapter implements ktp.SandboxExecutor.
var _ ktp.SandboxExecutor = (*KTPAdapter)(nil)

// NewKTPAdapter creates a KTPAdapter backed by the given sandbox Manager.
func NewKTPAdapter(mgr *Manager) *KTPAdapter {
	return &KTPAdapter{
		mgr:       mgr,
		sandboxes: make(map[string]*Sandbox),
	}
}

// GetOrCreateSandbox returns an existing or new sandbox for the agent.
// The returned SandboxInfo contains the sandbox ID for later ExecuteInSandbox calls.
func (a *KTPAdapter) GetOrCreateSandbox(agentID string, tierOverrides map[string]any) (ktp.SandboxInfo, error) {
	sb, err := a.mgr.Create(agentID, tierOverrides)
	if err != nil {
		return ktp.SandboxInfo{}, fmt.Errorf("create sandbox for agent %s: %w", agentID, err)
	}

	a.mu.Lock()
	a.sandboxes[sb.ID] = sb
	a.mu.Unlock()

	return ktp.SandboxInfo{
		ID:      sb.ID,
		AgentID: sb.AgentID,
	}, nil
}

// ExecuteInSandbox executes a tool request in the specified sandbox.
func (a *KTPAdapter) ExecuteInSandbox(ctx context.Context, sandboxID string, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	a.mu.Lock()
	sb, ok := a.sandboxes[sandboxID]
	a.mu.Unlock()

	if !ok {
		return nil, fmt.Errorf("sandbox %s not found", sandboxID)
	}

	return a.mgr.Execute(ctx, sb, req)
}

// SetSandboxSecrets sets resolved secrets on a cached sandbox for env injection.
func (a *KTPAdapter) SetSandboxSecrets(sandboxID string, secrets map[string]string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if sb, ok := a.sandboxes[sandboxID]; ok {
		sb.Secrets = secrets
	}
}
