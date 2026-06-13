//go:build integration

package integration

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/kkjorsvik/kyvik/internal/ktp"
	"github.com/kkjorsvik/kyvik/internal/sandbox"
)

// TestRealSandbox_FileRoundtrip builds the kyvik-sandbox binary and exercises
// the full sandbox pipeline: Manager.Create → Manager.Execute → JSON protocol
// for file.list, file.write, and file.read through the real binary.
//
// Run with: go test ./internal/integration/... -tags integration -run TestRealSandbox -v
func TestRealSandbox_FileRoundtrip(t *testing.T) {
	// Build the sandbox binary to a temp directory.
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "kyvik-sandbox")
	buildCmd := exec.Command("go", "build", "-o", binPath, "./cmd/kyvik-sandbox/")
	buildCmd.Dir = projectRoot(t)
	buildCmd.Env = append(os.Environ(), "CGO_ENABLED=1")
	out, err := buildCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to build kyvik-sandbox: %v\n%s", err, out)
	}

	// Create sandbox manager with the built binary.
	workspaceRoot := t.TempDir()
	mgr, err := sandbox.NewManager(sandbox.ManagerConfig{
		WorkspaceRoot: workspaceRoot,
		RunnerPath:    binPath,
		Defaults:      sandbox.DefaultSandboxConfig(),
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// Create a sandbox for a test agent.
	sb, err := mgr.Create("test-agent", nil)
	if err != nil {
		t.Fatalf("Create sandbox: %v", err)
	}
	t.Cleanup(func() { mgr.Cleanup(sb) })

	ctx := context.Background()

	// 1. file.list — empty workspace should return empty/null entries.
	listReq := ktp.NewToolRequest("test-agent", "file", "list", map[string]any{
		"path": ".",
	})
	listResp, err := mgr.Execute(ctx, sb, listReq)
	if err != nil {
		t.Fatalf("file.list execute error: %v", err)
	}
	if !listResp.Success {
		t.Fatalf("file.list failed: %s", listResp.Error)
	}
	if listResp.SandboxID != sb.ID {
		t.Errorf("expected sandbox ID %s, got %s", sb.ID, listResp.SandboxID)
	}

	// 2. file.write — create a file in the workspace.
	writeReq := ktp.NewToolRequest("test-agent", "file", "write", map[string]any{
		"path":    "hello.txt",
		"content": "hello from sandbox",
	})
	writeResp, err := mgr.Execute(ctx, sb, writeReq)
	if err != nil {
		t.Fatalf("file.write execute error: %v", err)
	}
	if !writeResp.Success {
		t.Fatalf("file.write failed: %s", writeResp.Error)
	}

	// Verify file exists on disk.
	content, err := os.ReadFile(filepath.Join(sb.Workspace, "hello.txt"))
	if err != nil {
		t.Fatalf("file not found on disk: %v", err)
	}
	if string(content) != "hello from sandbox" {
		t.Errorf("unexpected file content: %q", content)
	}

	// 3. file.read — read it back through the sandbox.
	readReq := ktp.NewToolRequest("test-agent", "file", "read", map[string]any{
		"path": "hello.txt",
	})
	readResp, err := mgr.Execute(ctx, sb, readReq)
	if err != nil {
		t.Fatalf("file.read execute error: %v", err)
	}
	if !readResp.Success {
		t.Fatalf("file.read failed: %s", readResp.Error)
	}

	// Check the read response contains our content.
	result, ok := readResp.Result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T: %v", readResp.Result, readResp.Result)
	}
	if got, _ := result["content"].(string); got != "hello from sandbox" {
		t.Errorf("file.read content mismatch: got %q", got)
	}

	// 4. file.list — now should contain hello.txt.
	listReq2 := ktp.NewToolRequest("test-agent", "file", "list", map[string]any{
		"path": ".",
	})
	listResp2, err := mgr.Execute(ctx, sb, listReq2)
	if err != nil {
		t.Fatalf("file.list (2nd) execute error: %v", err)
	}
	if !listResp2.Success {
		t.Fatalf("file.list (2nd) failed: %s", listResp2.Error)
	}
	entries, ok := listResp2.Result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", listResp2.Result)
	}
	entryList, ok := entries["entries"].([]any)
	if !ok || len(entryList) == 0 {
		t.Errorf("expected at least one entry in file.list, got %v", entries)
	}
}

// TestRealSandbox_KTPAdapterRoundtrip exercises the KTPAdapter (which
// implements ktp.SandboxExecutor) end-to-end with the real binary.
func TestRealSandbox_KTPAdapterRoundtrip(t *testing.T) {
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "kyvik-sandbox")
	buildCmd := exec.Command("go", "build", "-o", binPath, "./cmd/kyvik-sandbox/")
	buildCmd.Dir = projectRoot(t)
	buildCmd.Env = append(os.Environ(), "CGO_ENABLED=1")
	out, err := buildCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to build kyvik-sandbox: %v\n%s", err, out)
	}

	workspaceRoot := t.TempDir()
	mgr, err := sandbox.NewManager(sandbox.ManagerConfig{
		WorkspaceRoot: workspaceRoot,
		RunnerPath:    binPath,
		Defaults:      sandbox.DefaultSandboxConfig(),
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	adapter := sandbox.NewKTPAdapter(mgr)

	// GetOrCreateSandbox.
	sbInfo, err := adapter.GetOrCreateSandbox("adapter-agent", nil)
	if err != nil {
		t.Fatalf("GetOrCreateSandbox: %v", err)
	}
	if sbInfo.ID == "" {
		t.Fatal("expected non-empty sandbox ID")
	}

	// SetSandboxSecrets (should not error; secrets are injected as env vars).
	adapter.SetSandboxSecrets(sbInfo.ID, map[string]string{"test_key": "test_value"})

	// ExecuteInSandbox with file.write.
	writeReq := ktp.NewToolRequest("adapter-agent", "file", "write", map[string]any{
		"path":    "adapter-test.txt",
		"content": "via adapter",
	})
	resp, err := adapter.ExecuteInSandbox(context.Background(), sbInfo.ID, writeReq)
	if err != nil {
		t.Fatalf("ExecuteInSandbox: %v", err)
	}
	if !resp.Success {
		t.Fatalf("file.write via adapter failed: %s", resp.Error)
	}
	if resp.SandboxID != sbInfo.ID {
		t.Errorf("expected sandbox ID %s, got %s", sbInfo.ID, resp.SandboxID)
	}

	// Verify file on disk.
	data, err := os.ReadFile(filepath.Join(workspaceRoot, "adapter-agent", "adapter-test.txt"))
	if err != nil {
		t.Fatalf("file not on disk: %v", err)
	}
	if string(data) != "via adapter" {
		t.Errorf("content mismatch: %q", data)
	}
}

// TestRealSandbox_InvalidTool verifies the sandbox binary returns an error
// for unknown tool/action combinations.
func TestRealSandbox_InvalidTool(t *testing.T) {
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "kyvik-sandbox")
	buildCmd := exec.Command("go", "build", "-o", binPath, "./cmd/kyvik-sandbox/")
	buildCmd.Dir = projectRoot(t)
	buildCmd.Env = append(os.Environ(), "CGO_ENABLED=1")
	out, err := buildCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to build kyvik-sandbox: %v\n%s", err, out)
	}

	workspaceRoot := t.TempDir()
	mgr, err := sandbox.NewManager(sandbox.ManagerConfig{
		WorkspaceRoot: workspaceRoot,
		RunnerPath:    binPath,
		Defaults:      sandbox.DefaultSandboxConfig(),
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	sb, err := mgr.Create("error-agent", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { mgr.Cleanup(sb) })

	req := ktp.NewToolRequest("error-agent", "nonexistent", "action", nil)
	resp, err := mgr.Execute(context.Background(), sb, req)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if resp.Success {
		t.Error("expected failure for nonexistent tool")
	}
}

// projectRoot returns the project root by walking up from the test file
// until we find go.mod.
func projectRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find project root (go.mod)")
		}
		dir = parent
	}
}
