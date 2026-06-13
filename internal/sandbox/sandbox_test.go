package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/ktp"
)

func TestDefaultSandboxConfig(t *testing.T) {
	cfg := DefaultSandboxConfig()
	if cfg.MaxMemoryMB != 1024 {
		t.Errorf("expected MaxMemoryMB 1024, got %d", cfg.MaxMemoryMB)
	}
	if cfg.MaxCPUPercent != 50 {
		t.Errorf("expected MaxCPUPercent 50, got %d", cfg.MaxCPUPercent)
	}
	if cfg.TimeoutSeconds != 60 {
		t.Errorf("expected TimeoutSeconds 60, got %d", cfg.TimeoutSeconds)
	}
	if cfg.AllowNetwork {
		t.Error("expected AllowNetwork false")
	}
	if cfg.MaxOutputBytes != 1<<20 {
		t.Errorf("expected MaxOutputBytes 1MB, got %d", cfg.MaxOutputBytes)
	}
}

func TestSandboxConfig_Timeout(t *testing.T) {
	// Normal value
	cfg := SandboxConfig{TimeoutSeconds: 30}
	if got := cfg.Timeout(); got != 30*time.Second {
		t.Errorf("expected 30s, got %v", got)
	}

	// Zero falls back to 60s
	cfg = SandboxConfig{TimeoutSeconds: 0}
	if got := cfg.Timeout(); got != 60*time.Second {
		t.Errorf("expected 60s fallback, got %v", got)
	}

	// Negative falls back to 60s
	cfg = SandboxConfig{TimeoutSeconds: -1}
	if got := cfg.Timeout(); got != 60*time.Second {
		t.Errorf("expected 60s fallback for negative, got %v", got)
	}
}

func TestCreateSandbox_CreatesWorkspaceDirs(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, err := newTestManager(tmpDir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	sb, err := mgr.Create("agent-1", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Verify workspace subdirectories exist
	for _, sub := range []string{"data", "tmp", "output"} {
		dir := filepath.Join(sb.Workspace, sub)
		info, err := os.Stat(dir)
		if err != nil {
			t.Errorf("workspace dir %s not created: %v", sub, err)
		} else if !info.IsDir() {
			t.Errorf("workspace path %s is not a directory", sub)
		}
	}

	// Verify sandbox fields
	if sb.AgentID != "agent-1" {
		t.Errorf("expected AgentID 'agent-1', got %q", sb.AgentID)
	}
	if sb.ID == "" {
		t.Error("expected non-empty sandbox ID")
	}
	if sb.Workspace == "" {
		t.Error("expected non-empty workspace path")
	}
}

func TestCreateSandbox_IdempotentForSameAgent(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, err := newTestManager(tmpDir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	sb1, err := mgr.Create("agent-idem", nil)
	if err != nil {
		t.Fatalf("Create 1: %v", err)
	}

	sb2, err := mgr.Create("agent-idem", nil)
	if err != nil {
		t.Fatalf("Create 2: %v", err)
	}

	if sb1.ID != sb2.ID {
		t.Errorf("expected same sandbox ID on repeat call, got %q and %q", sb1.ID, sb2.ID)
	}
}

func TestCleanup_RemovesWorkspace(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, err := newTestManager(tmpDir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	sb, err := mgr.Create("agent-cleanup", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Verify workspace exists
	if _, err := os.Stat(sb.Workspace); err != nil {
		t.Fatalf("workspace should exist before cleanup: %v", err)
	}

	// Cleanup
	if err := mgr.Cleanup(sb); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	// Workspace should be removed
	if _, err := os.Stat(sb.Workspace); !os.IsNotExist(err) {
		t.Error("workspace should be removed after cleanup")
	}

	// Should not be in active map
	if _, ok := mgr.Get(sb.AgentID); ok {
		t.Error("sandbox should not be in active map after cleanup")
	}
}

func TestExecute_TimeoutKillsProcess(t *testing.T) {
	tmpDir := t.TempDir()

	// Use /bin/sleep as the runner — it will not write valid JSON to stdout.
	sleepPath := "/bin/sleep"
	if _, err := os.Stat(sleepPath); err != nil {
		t.Skip("skipping: /bin/sleep not available")
	}

	mgr := &Manager{
		config: ManagerConfig{
			WorkspaceRoot: tmpDir,
			RunnerPath:    sleepPath,
			Defaults:      DefaultSandboxConfig(),
		},
		active: make(map[string]*Sandbox),
		runner: sleepPath,
	}

	sb := NewSandbox("timeout-agent", SandboxConfig{
		TimeoutSeconds: 1,
		MaxOutputBytes: 1 << 20,
	}, tmpDir)

	req := ktp.ToolRequest{
		ID:      "req-timeout",
		AgentID: "timeout-agent",
		Tool:    "echo",
		Action:  "echo",
	}

	ctx := context.Background()
	start := time.Now()
	resp, err := mgr.Execute(ctx, sb, req)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Execute returned Go error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.Success {
		t.Error("expected Success=false for timeout")
	}

	// Should complete within ~2s (1s timeout + some slack)
	if elapsed > 3*time.Second {
		t.Errorf("expected completion within 3s, took %v", elapsed)
	}
}

func TestLimitedWriter(t *testing.T) {
	w := &limitedWriter{limit: 10}

	// Write within limit
	n, err := w.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != 5 {
		t.Errorf("expected 5 bytes written, got %d", n)
	}

	// Write that exceeds limit — should truncate silently
	n, err = w.Write([]byte("world!!!!"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	// Reports full length even though truncated
	if n != 5 {
		t.Errorf("expected 5 bytes written (truncated), got %d", n)
	}

	// Total bytes should be capped at limit
	if w.buf.Len() != 10 {
		t.Errorf("expected buffer length 10, got %d", w.buf.Len())
	}
	if w.String() != "helloworld" {
		t.Errorf("expected 'helloworld', got %q", w.String())
	}

	// Further writes should be fully discarded
	n, err = w.Write([]byte("more"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != 4 { // reports full length
		t.Errorf("expected 4, got %d", n)
	}
	if w.buf.Len() != 10 {
		t.Errorf("expected buffer still at 10, got %d", w.buf.Len())
	}
}

func TestBuildEnvironment_IncludesGOMEMLIMIT(t *testing.T) {
	sb := NewSandbox("test-agent", SandboxConfig{
		MaxMemoryMB:    512,
		MaxOutputBytes: 1 << 20,
	}, "/tmp/test-workspace")

	env := buildEnvironment(sb, nil)

	var foundGOMEMLIMIT, foundMaxMemory bool
	for _, e := range env {
		if e == "GOMEMLIMIT=512MiB" {
			foundGOMEMLIMIT = true
		}
		if e == "KYVIK_SANDBOX_MAX_MEMORY_MB=512" {
			foundMaxMemory = true
		}
	}

	if !foundGOMEMLIMIT {
		t.Error("expected GOMEMLIMIT=512MiB in sandbox environment")
	}
	if !foundMaxMemory {
		t.Error("expected KYVIK_SANDBOX_MAX_MEMORY_MB=512 in sandbox environment")
	}
}

func TestBuildEnvironment_SecretsViaSocket(t *testing.T) {
	sb := NewSandbox("test-agent", SandboxConfig{
		MaxMemoryMB:    512,
		MaxOutputBytes: 1 << 20,
	}, "/tmp/test-workspace")
	sb.Secrets = map[string]string{
		"github:token": "ghp_test",
		"api_key":      "sk-secret",
	}

	// With socket path: should set KYVIK_SECRETS_SOCKET, NOT individual KYVIK_SECRET_* vars.
	socketPath := "/tmp/test-workspace/tmp/.kyvik-secrets.sock"
	env := buildEnvironment(sb, nil, socketPath)

	var foundSocket bool
	for _, e := range env {
		if e == "KYVIK_SECRETS_SOCKET="+socketPath {
			foundSocket = true
		}
		if strings.HasPrefix(e, "KYVIK_SECRET_") {
			t.Errorf("should NOT have KYVIK_SECRET_* env var when socket is set, found: %s", e)
		}
	}
	if !foundSocket {
		t.Error("expected KYVIK_SECRETS_SOCKET in environment when socket path is set")
	}
}

func TestBuildEnvironment_SecretsViaEnvFallback(t *testing.T) {
	sb := NewSandbox("test-agent", SandboxConfig{
		MaxMemoryMB:    512,
		MaxOutputBytes: 1 << 20,
	}, "/tmp/test-workspace")
	sb.Secrets = map[string]string{
		"api_key": "sk-secret",
	}

	// Without socket path: should inject KYVIK_SECRET_* env vars.
	env := buildEnvironment(sb, nil)

	var foundSecret bool
	for _, e := range env {
		if e == "KYVIK_SECRET_API_KEY=sk-secret" {
			foundSecret = true
		}
		if strings.HasPrefix(e, "KYVIK_SECRETS_SOCKET=") {
			t.Error("should NOT have KYVIK_SECRETS_SOCKET when no socket path is set")
		}
	}
	if !foundSecret {
		t.Error("expected KYVIK_SECRET_API_KEY=sk-secret in fallback environment")
	}
}

func TestBuildEnvironment_NoSecretsNoSocketNoEnvVars(t *testing.T) {
	sb := NewSandbox("test-agent", SandboxConfig{
		MaxMemoryMB:    512,
		MaxOutputBytes: 1 << 20,
	}, "/tmp/test-workspace")
	// No secrets set.

	env := buildEnvironment(sb, nil)

	for _, e := range env {
		if strings.HasPrefix(e, "KYVIK_SECRET_") || strings.HasPrefix(e, "KYVIK_SECRETS_SOCKET=") {
			t.Errorf("should NOT have any secret-related env vars when no secrets, found: %s", e)
		}
	}
}

// newTestManager creates a Manager for testing with a dummy runner that won't be called.
func newTestManager(tmpDir string) (*Manager, error) {
	// For tests that only need Create/Cleanup/Get, the runner doesn't need to exist.
	return &Manager{
		config: ManagerConfig{
			WorkspaceRoot: filepath.Join(tmpDir, "workspaces"),
			Defaults:      DefaultSandboxConfig(),
		},
		active: make(map[string]*Sandbox),
		runner: "/nonexistent/kyvik-sandbox",
	}, nil
}
