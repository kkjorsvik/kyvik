package ktp

import (
	"context"
	"fmt"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

// --- Shared mock types for KTP tests ---

type mockAgentStore struct {
	agents map[string]*types.AgentConfig
}

func (m *mockAgentStore) GetAgent(_ context.Context, id string) (*types.AgentConfig, error) {
	agent, ok := m.agents[id]
	if !ok {
		return nil, fmt.Errorf("agent %s: %w", id, types.ErrNotFound)
	}
	return agent, nil
}

type auditEntry struct {
	Result   PermissionResult
	Request  *ToolRequest
	Response *ToolResponse
}

type mockAuditLogger struct {
	entries []auditEntry
}

func (m *mockAuditLogger) LogToolPermission(_ context.Context, result PermissionResult) error {
	m.entries = append(m.entries, auditEntry{Result: result})
	return nil
}

func (m *mockAuditLogger) LogToolExecution(_ context.Context, req ToolRequest, resp *ToolResponse) error {
	m.entries = append(m.entries, auditEntry{Request: &req, Response: resp})
	return nil
}

// --- Shared helper builders ---

func newTestGate(agents map[string]*types.AgentConfig) (*PermissionGate, *mockAuditLogger) {
	audit := &mockAuditLogger{}
	store := &mockAgentStore{agents: agents}
	gate := NewPermissionGate(store, audit)
	return gate, audit
}

func minimalToolDecl(name, minTier string, actions ...ActionSpec) ToolDeclaration {
	if len(actions) == 0 {
		actions = []ActionSpec{{
			Name:       "do",
			Parameters: JSONSchema{Type: "object"},
		}}
	}
	return ToolDeclaration{
		Name:    name,
		Version: "1.0.0",
		MinTier: minTier,
		Actions: actions,
	}
}

func agentWithTemplate(id, template string) *types.AgentConfig {
	return &types.AgentConfig{ID: id, Template: template}
}

// --- Sandbox mock types for executor tests ---

// mockSandboxExecutor records sandbox calls and returns canned responses.
type mockSandboxExecutor struct {
	createCalls  []mockCreateCall
	executeCalls []mockExecuteCall
	secretCalls  []mockSecretCall
	response     *ToolResponse // canned response for ExecuteInSandbox
	createErr    error         // error to return from GetOrCreateSandbox
	executeErr   error         // error to return from ExecuteInSandbox
	sandboxID    string        // ID to return from GetOrCreateSandbox
}

type mockCreateCall struct {
	AgentID       string
	TierOverrides map[string]any
}

type mockExecuteCall struct {
	SandboxID string
	Req       ToolRequest
}

type mockSecretCall struct {
	SandboxID string
	Secrets   map[string]string
}

func (m *mockSandboxExecutor) GetOrCreateSandbox(agentID string, tierOverrides map[string]any) (SandboxInfo, error) {
	m.createCalls = append(m.createCalls, mockCreateCall{AgentID: agentID, TierOverrides: tierOverrides})
	if m.createErr != nil {
		return SandboxInfo{}, m.createErr
	}
	id := m.sandboxID
	if id == "" {
		id = "sb-test-001"
	}
	return SandboxInfo{ID: id, AgentID: agentID}, nil
}

func (m *mockSandboxExecutor) ExecuteInSandbox(_ context.Context, sandboxID string, req ToolRequest) (*ToolResponse, error) {
	m.executeCalls = append(m.executeCalls, mockExecuteCall{SandboxID: sandboxID, Req: req})
	if m.executeErr != nil {
		return nil, m.executeErr
	}
	if m.response != nil {
		return m.response, nil
	}
	return &ToolResponse{
		RequestID: req.ID,
		Success:   true,
		Result:    req.Parameters,
	}, nil
}

func (m *mockSandboxExecutor) SetSandboxSecrets(sandboxID string, secrets map[string]string) {
	m.secretCalls = append(m.secretCalls, mockSecretCall{SandboxID: sandboxID, Secrets: secrets})
}

// mockSecretResolver returns secrets from a static map.
type mockSecretResolver struct {
	secrets map[string]string // key → value
	err     error
}

func (m *mockSecretResolver) Resolve(_ context.Context, _, _, key string) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	v, ok := m.secrets[key]
	if !ok {
		return "", fmt.Errorf("secret %q not found", key)
	}
	return v, nil
}

type funcSecretResolver struct {
	fn func(context.Context, string, string, string) (string, error)
}

func (f *funcSecretResolver) Resolve(ctx context.Context, agentID, teamID, key string) (string, error) {
	return f.fn(ctx, agentID, teamID, key)
}

// inlineEchoTool is an echo tool that implements InlineTool.
type inlineEchoTool struct {
	echoTool
}

func (t *inlineEchoTool) Inline() bool { return true }
