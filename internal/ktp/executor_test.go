package ktp

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

// --- Mock tools for executor tests ---

// echoTool returns its parameters as the result.
type echoTool struct {
	decl ToolDeclaration
}

func (t *echoTool) Declaration() ToolDeclaration { return t.decl }
func (t *echoTool) Execute(_ context.Context, req ToolRequest) (*ToolResponse, error) {
	return &ToolResponse{
		RequestID: req.ID,
		Success:   true,
		Result:    req.Parameters,
		Timestamp: time.Now(),
	}, nil
}

// panicTool panics on Execute.
type panicTool struct {
	decl ToolDeclaration
}

func (t *panicTool) Declaration() ToolDeclaration { return t.decl }
func (t *panicTool) Execute(_ context.Context, _ ToolRequest) (*ToolResponse, error) {
	panic("something went very wrong")
}

// slowTool blocks until the context is cancelled.
type slowTool struct {
	decl ToolDeclaration
}

func (t *slowTool) Declaration() ToolDeclaration { return t.decl }
func (t *slowTool) Execute(ctx context.Context, req ToolRequest) (*ToolResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// counterTool counts executions with an atomic counter and sleeps briefly.
type counterTool struct {
	decl    ToolDeclaration
	count   atomic.Int32
	holdFor time.Duration
}

func (t *counterTool) Declaration() ToolDeclaration { return t.decl }
func (t *counterTool) Execute(ctx context.Context, req ToolRequest) (*ToolResponse, error) {
	t.count.Add(1)
	select {
	case <-time.After(t.holdFor):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &ToolResponse{
		RequestID: req.ID,
		Success:   true,
		Timestamp: time.Now(),
	}, nil
}

// --- Helper to build a test executor ---

func newTestExecutor(agents map[string]*types.AgentConfig, tools []Tool, cfg ExecutorConfig) (*Executor, *mockAuditLogger) {
	audit := &mockAuditLogger{}
	store := &mockAgentStore{agents: agents}
	gate := NewPermissionGate(store, audit)

	registry := NewRegistry()
	for _, tool := range tools {
		if err := registry.Register(tool); err != nil {
			panic("test setup: " + err.Error())
		}
	}

	return NewExecutor(registry, gate, audit, cfg), audit
}

func testDecl(name string) ToolDeclaration {
	return ToolDeclaration{
		Name:    name,
		Version: "1.0.0",
		MinTier: TierReader,
		Actions: []ActionSpec{{
			Name:       "do",
			Parameters: JSONSchema{Type: "object"},
		}},
	}
}

func testDeclWithParams(name string, params JSONSchema) ToolDeclaration {
	return ToolDeclaration{
		Name:    name,
		Version: "1.0.0",
		MinTier: TierReader,
		Actions: []ActionSpec{{
			Name:       "do",
			Parameters: params,
		}},
	}
}

// --- Tests ---

func TestExecute_FullPipeline(t *testing.T) {
	decl := testDecl("echo")
	exec, _ := newTestExecutor(
		map[string]*types.AgentConfig{"a1": agentWithTemplate("a1", "reader")},
		[]Tool{&echoTool{decl: decl}},
		ExecutorConfig{},
	)

	req := NewToolRequest("a1", "echo", "do", map[string]any{"msg": "hello"})
	resp, err := exec.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", resp.Result)
	}
	if result["msg"] != "hello" {
		t.Fatalf("expected msg=hello, got %v", result["msg"])
	}
	if resp.ExecutionMs < 0 {
		t.Fatal("expected non-negative ExecutionMs")
	}
}

func TestExecute_UnknownTool(t *testing.T) {
	exec, _ := newTestExecutor(
		map[string]*types.AgentConfig{"a1": agentWithTemplate("a1", "reader")},
		nil,
		ExecutorConfig{},
	)

	req := NewToolRequest("a1", "nonexistent", "do", nil)
	resp, err := exec.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Success {
		t.Fatal("expected failure for unknown tool")
	}
	if !strings.Contains(resp.Error, "unknown tool") {
		t.Fatalf("expected 'unknown tool' in error, got: %s", resp.Error)
	}
}

func TestExecute_UnknownAction(t *testing.T) {
	decl := testDecl("echo")
	exec, _ := newTestExecutor(
		map[string]*types.AgentConfig{"a1": agentWithTemplate("a1", "reader")},
		[]Tool{&echoTool{decl: decl}},
		ExecutorConfig{},
	)

	req := NewToolRequest("a1", "echo", "nonexistent", nil)
	resp, err := exec.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Success {
		t.Fatal("expected failure for unknown action")
	}
	if !strings.Contains(resp.Error, "unknown action") {
		t.Fatalf("expected 'unknown action' in error, got: %s", resp.Error)
	}
}

func TestExecute_InvalidParams(t *testing.T) {
	decl := testDeclWithParams("strict", JSONSchema{
		Type:     "object",
		Required: []string{"path"},
		Properties: map[string]JSONSchema{
			"path": {Type: "string"},
		},
	})
	exec, _ := newTestExecutor(
		map[string]*types.AgentConfig{"a1": agentWithTemplate("a1", "reader")},
		[]Tool{&echoTool{decl: decl}},
		ExecutorConfig{},
	)

	// Missing required "path" parameter.
	req := NewToolRequest("a1", "strict", "do", map[string]any{})
	resp, err := exec.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Success {
		t.Fatal("expected failure for invalid params")
	}
	if !strings.Contains(resp.Error, "validation failed") {
		t.Fatalf("expected 'validation failed' in error, got: %s", resp.Error)
	}
}

func TestExecute_PermissionDenied(t *testing.T) {
	// Tool requires writer tier, agent is reader.
	decl := ToolDeclaration{
		Name:    "writer-tool",
		Version: "1.0.0",
		MinTier: TierWriter,
		Actions: []ActionSpec{{
			Name:       "do",
			Parameters: JSONSchema{Type: "object"},
		}},
	}
	exec, _ := newTestExecutor(
		map[string]*types.AgentConfig{"a1": agentWithTemplate("a1", "reader")},
		[]Tool{&echoTool{decl: decl}},
		ExecutorConfig{},
	)

	req := NewToolRequest("a1", "writer-tool", "do", nil)
	resp, err := exec.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Success {
		t.Fatal("expected denied")
	}
	if !strings.Contains(resp.Error, "tier") {
		t.Fatalf("expected tier-related denial, got: %s", resp.Error)
	}
}

func TestExecute_ToolTimeout(t *testing.T) {
	decl := testDecl("slow")
	exec, _ := newTestExecutor(
		map[string]*types.AgentConfig{"a1": agentWithTemplate("a1", "reader")},
		[]Tool{&slowTool{decl: decl}},
		ExecutorConfig{DefaultTimeout: 50 * time.Millisecond},
	)

	req := NewToolRequest("a1", "slow", "do", nil)
	resp, err := exec.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Success {
		t.Fatal("expected failure from timeout")
	}
	if !strings.Contains(resp.Error, "deadline") && !strings.Contains(resp.Error, "context") {
		t.Fatalf("expected deadline/context error, got: %s", resp.Error)
	}
}

func TestExecute_ToolPanic(t *testing.T) {
	decl := testDecl("panic")
	exec, _ := newTestExecutor(
		map[string]*types.AgentConfig{"a1": agentWithTemplate("a1", "reader")},
		[]Tool{&panicTool{decl: decl}},
		ExecutorConfig{},
	)

	req := NewToolRequest("a1", "panic", "do", nil)
	resp, err := exec.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Success {
		t.Fatal("expected failure from panic")
	}
	if !strings.Contains(resp.Error, "tool panicked") {
		t.Fatalf("expected 'tool panicked' in error, got: %s", resp.Error)
	}
}

func TestExecute_ConcurrentLimit(t *testing.T) {
	decl := testDecl("counter")
	tool := &counterTool{decl: decl, holdFor: 100 * time.Millisecond}
	exec, _ := newTestExecutor(
		map[string]*types.AgentConfig{"a1": agentWithTemplate("a1", "reader")},
		[]Tool{tool},
		ExecutorConfig{MaxConcurrent: 1},
	)

	var wg sync.WaitGroup
	results := make([]*ToolResponse, 2)
	errs := make([]error, 2)

	for i := range 2 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			req := NewToolRequest("a1", "counter", "do", nil)
			results[idx], errs[idx] = exec.Execute(context.Background(), req)
		}(i)
	}
	wg.Wait()

	for i := range 2 {
		if errs[i] != nil {
			t.Fatalf("request %d: unexpected error: %v", i, errs[i])
		}
		if !results[i].Success {
			t.Fatalf("request %d: expected success, got: %s", i, results[i].Error)
		}
	}

	if tool.count.Load() != 2 {
		t.Fatalf("expected 2 executions, got %d", tool.count.Load())
	}
}

func TestExecute_AuditLogged(t *testing.T) {
	decl := testDecl("echo")
	exec, audit := newTestExecutor(
		map[string]*types.AgentConfig{"a1": agentWithTemplate("a1", "reader")},
		[]Tool{&echoTool{decl: decl}},
		ExecutorConfig{},
	)

	req := NewToolRequest("a1", "echo", "do", map[string]any{"key": "val"})
	resp, err := exec.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	// Expect at least a permission entry and an execution entry.
	var hasPermission, hasExecution bool
	for _, e := range audit.entries {
		if e.Result.Tool != "" {
			hasPermission = true
		}
		if e.Request != nil {
			hasExecution = true
		}
	}
	if !hasPermission {
		t.Fatal("expected a permission audit entry")
	}
	if !hasExecution {
		t.Fatal("expected an execution audit entry")
	}
}

// --- Sandbox routing tests ---

func TestExecute_SandboxRouting(t *testing.T) {
	decl := testDecl("echo")
	mockSb := &mockSandboxExecutor{}

	exec, _ := newTestExecutor(
		map[string]*types.AgentConfig{"a1": agentWithTemplate("a1", "reader")},
		[]Tool{&echoTool{decl: decl}},
		ExecutorConfig{},
	)
	exec.SetSandbox(mockSb)

	req := NewToolRequest("a1", "echo", "do", map[string]any{"msg": "hello"})
	resp, err := exec.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	// Verify sandbox was called.
	if len(mockSb.createCalls) != 1 {
		t.Fatalf("expected 1 create call, got %d", len(mockSb.createCalls))
	}
	if mockSb.createCalls[0].AgentID != "a1" {
		t.Fatalf("expected agent a1, got %s", mockSb.createCalls[0].AgentID)
	}
	if len(mockSb.executeCalls) != 1 {
		t.Fatalf("expected 1 execute call, got %d", len(mockSb.executeCalls))
	}

	// Verify SandboxID is set on response.
	if resp.SandboxID == "" {
		t.Fatal("expected SandboxID to be set")
	}
}

func TestExecute_InlineToolBypassesSandbox(t *testing.T) {
	decl := testDecl("inline-echo")
	mockSb := &mockSandboxExecutor{}

	exec, _ := newTestExecutor(
		map[string]*types.AgentConfig{"a1": agentWithTemplate("a1", "reader")},
		[]Tool{&inlineEchoTool{echoTool{decl: decl}}},
		ExecutorConfig{},
	)
	exec.SetSandbox(mockSb)

	req := NewToolRequest("a1", "inline-echo", "do", map[string]any{"msg": "hi"})
	resp, err := exec.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	// Sandbox should NOT be called for inline tools.
	if len(mockSb.createCalls) != 0 {
		t.Fatalf("expected 0 sandbox create calls for inline tool, got %d", len(mockSb.createCalls))
	}
	if len(mockSb.executeCalls) != 0 {
		t.Fatalf("expected 0 sandbox execute calls for inline tool, got %d", len(mockSb.executeCalls))
	}

	// SandboxID should be empty for inline.
	if resp.SandboxID != "" {
		t.Fatalf("expected empty SandboxID for inline tool, got %s", resp.SandboxID)
	}
}

func TestExecute_SecretInjection(t *testing.T) {
	decl := ToolDeclaration{
		Name:            "secret-tool",
		Version:         "1.0.0",
		MinTier:         TierReader,
		RequiredSecrets: []string{"api_key", "db_password"},
		Actions: []ActionSpec{{
			Name:       "do",
			Parameters: JSONSchema{Type: "object"},
		}},
	}

	mockSb := &mockSandboxExecutor{}
	mockSr := &mockSecretResolver{
		secrets: map[string]string{
			"api_key":     "sk-12345",
			"db_password": "hunter2",
		},
	}

	exec, _ := newTestExecutor(
		map[string]*types.AgentConfig{"a1": agentWithTemplate("a1", "reader")},
		[]Tool{&echoTool{decl: decl}},
		ExecutorConfig{},
	)
	exec.SetSandbox(mockSb)
	exec.SetSecretResolver(mockSr)

	req := NewToolRequest("a1", "secret-tool", "do", nil)
	_, err := exec.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify secrets were injected and then cleared.
	if len(mockSb.secretCalls) < 3 {
		t.Fatalf("expected at least 3 secret calls (clear/set/clear), got %d", len(mockSb.secretCalls))
	}
	secrets := mockSb.secretCalls[1].Secrets
	if secrets["api_key"] != "sk-12345" {
		t.Fatalf("expected api_key=sk-12345, got %s", secrets["api_key"])
	}
	if secrets["db_password"] != "hunter2" {
		t.Fatalf("expected db_password=hunter2, got %s", secrets["db_password"])
	}
	if mockSb.secretCalls[len(mockSb.secretCalls)-1].Secrets != nil {
		t.Fatal("expected secrets to be cleared after sandbox execution")
	}
}

func TestExecute_SecretRedaction(t *testing.T) {
	decl := ToolDeclaration{
		Name:            "leaky-tool",
		Version:         "1.0.0",
		MinTier:         TierReader,
		RequiredSecrets: []string{"api_key"},
		Actions: []ActionSpec{{
			Name:       "do",
			Parameters: JSONSchema{Type: "object"},
		}},
	}

	mockSb := &mockSandboxExecutor{
		response: &ToolResponse{
			RequestID: "test",
			Success:   true,
			Result:    map[string]any{"output": "the key is sk-secret-value here"},
			Error:     "error contains sk-secret-value too",
		},
	}
	mockSr := &mockSecretResolver{
		secrets: map[string]string{"api_key": "sk-secret-value"},
	}

	exec, _ := newTestExecutor(
		map[string]*types.AgentConfig{"a1": agentWithTemplate("a1", "reader")},
		[]Tool{&echoTool{decl: decl}},
		ExecutorConfig{},
	)
	exec.SetSandbox(mockSb)
	exec.SetSecretResolver(mockSr)

	req := NewToolRequest("a1", "leaky-tool", "do", nil)
	resp, err := exec.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Secret value should be redacted from error.
	if strings.Contains(resp.Error, "sk-secret-value") {
		t.Fatalf("secret value leaked in error: %s", resp.Error)
	}
	if !strings.Contains(resp.Error, "[REDACTED]") {
		t.Fatalf("expected [REDACTED] in error, got: %s", resp.Error)
	}

	// Secret value should be redacted from result.
	resultMap, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", resp.Result)
	}
	output := resultMap["output"].(string)
	if strings.Contains(output, "sk-secret-value") {
		t.Fatalf("secret value leaked in result: %s", output)
	}
	if !strings.Contains(output, "[REDACTED]") {
		t.Fatalf("expected [REDACTED] in result, got: %s", output)
	}
}

func TestExecute_SecretResolutionFailure(t *testing.T) {
	decl := ToolDeclaration{
		Name:            "secret-tool",
		Version:         "1.0.0",
		MinTier:         TierReader,
		RequiredSecrets: []string{"api_key"},
		Actions: []ActionSpec{{
			Name:       "do",
			Parameters: JSONSchema{Type: "object"},
		}},
	}

	t.Run("no resolver configured", func(t *testing.T) {
		mockSb := &mockSandboxExecutor{}
		exec, _ := newTestExecutor(
			map[string]*types.AgentConfig{"a1": agentWithTemplate("a1", "reader")},
			[]Tool{&echoTool{decl: decl}},
			ExecutorConfig{},
		)
		exec.SetSandbox(mockSb)
		// Do NOT set secret resolver.

		req := NewToolRequest("a1", "secret-tool", "do", nil)
		resp, err := exec.Execute(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.Success {
			t.Fatal("expected failure response when secret resolver is nil")
		}
		if !strings.Contains(resp.Error, "secret resolver not configured") {
			t.Fatalf("expected 'secret resolver not configured' error, got: %s", resp.Error)
		}
	})

	t.Run("resolve fails", func(t *testing.T) {
		mockSb := &mockSandboxExecutor{}
		mockSr := &mockSecretResolver{
			err: fmt.Errorf("vault sealed"),
		}
		exec, _ := newTestExecutor(
			map[string]*types.AgentConfig{"a1": agentWithTemplate("a1", "reader")},
			[]Tool{&echoTool{decl: decl}},
			ExecutorConfig{},
		)
		exec.SetSandbox(mockSb)
		exec.SetSecretResolver(mockSr)

		req := NewToolRequest("a1", "secret-tool", "do", nil)
		resp, err := exec.Execute(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.Success {
			t.Fatal("expected failure response when secret resolution fails")
		}
		if !strings.Contains(resp.Error, "failed to resolve required secret") {
			t.Fatalf("expected 'failed to resolve required secret' error, got: %s", resp.Error)
		}
		if len(mockSb.secretCalls) < 2 {
			t.Fatalf("expected secrets to be cleared on failure, got %d calls", len(mockSb.secretCalls))
		}
		if mockSb.secretCalls[len(mockSb.secretCalls)-1].Secrets != nil {
			t.Fatal("expected secrets to be cleared after resolution failure")
		}
	})
}

func TestExecute_SecretResolutionUsesTeamScope(t *testing.T) {
	decl := ToolDeclaration{
		Name:            "secret-tool",
		Version:         "1.0.0",
		MinTier:         TierReader,
		RequiredSecrets: []string{"api_key"},
		Actions: []ActionSpec{{
			Name:       "do",
			Parameters: JSONSchema{Type: "object"},
		}},
	}

	type resolveCall struct {
		agentID string
		teamID  string
		key     string
	}
	var calls []resolveCall
	mockSb := &mockSandboxExecutor{}

	exec, _ := newTestExecutor(
		map[string]*types.AgentConfig{"a1": agentWithTemplate("a1", "reader")},
		[]Tool{&echoTool{decl: decl}},
		ExecutorConfig{},
	)
	exec.SetSandbox(mockSb)
	exec.SetSecretResolver(&funcSecretResolver{
		fn: func(_ context.Context, agentID, teamID, key string) (string, error) {
			calls = append(calls, resolveCall{agentID: agentID, teamID: teamID, key: key})
			return "sk-team", nil
		},
	})

	req := NewToolRequest("a1", "secret-tool", "do", nil)
	req.TeamID = "team-1"
	_, err := exec.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 resolver call, got %d", len(calls))
	}
	if calls[0].teamID != "team-1" {
		t.Fatalf("resolver teamID = %q, want %q", calls[0].teamID, "team-1")
	}
}

func TestExecute_SandboxCreateError(t *testing.T) {
	decl := testDecl("echo")
	mockSb := &mockSandboxExecutor{
		createErr: fmt.Errorf("disk full"),
	}

	exec, _ := newTestExecutor(
		map[string]*types.AgentConfig{"a1": agentWithTemplate("a1", "reader")},
		[]Tool{&echoTool{decl: decl}},
		ExecutorConfig{},
	)
	exec.SetSandbox(mockSb)

	req := NewToolRequest("a1", "echo", "do", nil)
	resp, err := exec.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected infrastructure error: %v", err)
	}
	if resp.Success {
		t.Fatal("expected failure from sandbox creation error")
	}
	if !strings.Contains(resp.Error, "sandbox creation failed") {
		t.Fatalf("expected 'sandbox creation failed' in error, got: %s", resp.Error)
	}
}

func TestExecute_SandboxExecuteTimeout(t *testing.T) {
	decl := testDecl("echo")
	mockSb := &mockSandboxExecutor{
		executeErr: context.DeadlineExceeded,
	}

	exec, _ := newTestExecutor(
		map[string]*types.AgentConfig{"a1": agentWithTemplate("a1", "reader")},
		[]Tool{&echoTool{decl: decl}},
		ExecutorConfig{},
	)
	exec.SetSandbox(mockSb)

	req := NewToolRequest("a1", "echo", "do", nil)
	resp, err := exec.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected infrastructure error: %v", err)
	}
	if resp.Success {
		t.Fatal("expected failure from sandbox timeout")
	}
	if !strings.Contains(resp.Error, "sandbox execution failed") {
		t.Fatalf("expected 'sandbox execution failed' in error, got: %s", resp.Error)
	}
}

func TestExecute_AdminTierSandboxOverrides(t *testing.T) {
	decl := testDecl("echo")
	// Give echo tool a network capability so admin tier enables networking.
	decl.Capabilities = []Capability{{Type: "network", Access: "read", Resource: "*"}}
	mockSb := &mockSandboxExecutor{}

	exec, _ := newTestExecutor(
		map[string]*types.AgentConfig{"a1": agentWithTemplate("a1", "admin")},
		[]Tool{&echoTool{decl: decl}},
		ExecutorConfig{},
	)
	exec.SetSandbox(mockSb)

	req := NewToolRequest("a1", "echo", "do", map[string]any{"msg": "hello"})
	_, err := exec.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify sandbox was called with admin tier overrides.
	if len(mockSb.createCalls) != 1 {
		t.Fatalf("expected 1 create call, got %d", len(mockSb.createCalls))
	}
	overrides := mockSb.createCalls[0].TierOverrides

	if overrides["max_memory_mb"] != 1024 {
		t.Errorf("expected max_memory_mb=1024, got %v", overrides["max_memory_mb"])
	}
	if overrides["timeout_seconds"] != 120 {
		t.Errorf("expected timeout_seconds=120, got %v", overrides["timeout_seconds"])
	}
	if overrides["allow_network"] != true {
		t.Errorf("expected allow_network=true, got %v", overrides["allow_network"])
	}
}

func TestExecute_NoSandboxFallsBackToInProcess(t *testing.T) {
	decl := testDecl("echo")

	// No sandbox set — should fall back to in-process execution.
	exec, _ := newTestExecutor(
		map[string]*types.AgentConfig{"a1": agentWithTemplate("a1", "reader")},
		[]Tool{&echoTool{decl: decl}},
		ExecutorConfig{},
	)

	req := NewToolRequest("a1", "echo", "do", map[string]any{"msg": "fallback"})
	resp, err := exec.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	// SandboxID should be empty when running in-process.
	if resp.SandboxID != "" {
		t.Fatalf("expected empty SandboxID for in-process execution, got %s", resp.SandboxID)
	}

	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", resp.Result)
	}
	if result["msg"] != "fallback" {
		t.Fatalf("expected msg=fallback, got %v", result["msg"])
	}
}
