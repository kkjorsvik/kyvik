package code

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kkjorsvik/kyvik/internal/ktp"
)

func testTool(t *testing.T) (*CodeTool, string) {
	t.Helper()
	workspace := t.TempDir()

	// Create tmp subdirectory.
	if err := os.MkdirAll(filepath.Join(workspace, "tmp"), 0o755); err != nil {
		t.Fatal(err)
	}

	tool := New(func(agentID string) (string, error) {
		return workspace, nil
	})
	return tool, workspace
}

func makeReq(action string, params map[string]any) ktp.ToolRequest {
	return ktp.NewToolRequest("test-agent", "code", action, params)
}

func TestCodeTool_Declaration(t *testing.T) {
	tool, _ := testTool(t)
	decl := tool.Declaration()

	if decl.Name != "code" {
		t.Errorf("expected name code, got %s", decl.Name)
	}
	if decl.Version != "1.0.0" {
		t.Errorf("expected version 1.0.0, got %s", decl.Version)
	}
	if decl.MinTier != ktp.TierOperator {
		t.Errorf("expected min tier operator, got %s", decl.MinTier)
	}
	if len(decl.Actions) != 2 {
		t.Errorf("expected 2 actions, got %d", len(decl.Actions))
	}
	if err := decl.Validate(); err != nil {
		t.Errorf("declaration validation failed: %v", err)
	}
}

func TestCodeTool_RunBash(t *testing.T) {
	tool, _ := testTool(t)

	resp, err := tool.Execute(context.Background(), makeReq("run", map[string]any{
		"language": "bash",
		"code":     `echo "hello bash"`,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	result := resp.Result.(map[string]any)
	stdout := result["stdout"].(string)
	if stdout != "hello bash\n" {
		t.Errorf("expected %q, got %q", "hello bash\n", stdout)
	}
}

func TestCodeTool_RunBashWithArgs(t *testing.T) {
	tool, _ := testTool(t)

	resp, err := tool.Execute(context.Background(), makeReq("run", map[string]any{
		"language": "bash",
		"code":     `echo "arg: $1"`,
		"args":     []any{"world"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	result := resp.Result.(map[string]any)
	stdout := result["stdout"].(string)
	if stdout != "arg: world\n" {
		t.Errorf("expected %q, got %q", "arg: world\n", stdout)
	}
}

func TestCodeTool_RunPython(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not found on PATH")
	}

	tool, _ := testTool(t)

	resp, err := tool.Execute(context.Background(), makeReq("run", map[string]any{
		"language": "python3",
		"code":     `print("hello python")`,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	result := resp.Result.(map[string]any)
	stdout := result["stdout"].(string)
	if stdout != "hello python\n" {
		t.Errorf("expected %q, got %q", "hello python\n", stdout)
	}
}

func TestCodeTool_RunUnsupportedLanguage(t *testing.T) {
	tool, _ := testTool(t)

	resp, err := tool.Execute(context.Background(), makeReq("run", map[string]any{
		"language": "rust",
		"code":     `fn main() {}`,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Error("expected unsupported language to be rejected")
	}
}

func TestCodeTool_RunTimeout(t *testing.T) {
	tool, _ := testTool(t)

	resp, err := tool.Execute(context.Background(), makeReq("run", map[string]any{
		"language":        "bash",
		"code":            "sleep 60",
		"timeout_seconds": 1,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	result := resp.Result.(map[string]any)
	if result["exit_code"] != -1 {
		t.Errorf("expected exit_code -1 on timeout, got %v", result["exit_code"])
	}
}

func TestCodeTool_RunNonZeroExit(t *testing.T) {
	tool, _ := testTool(t)

	resp, err := tool.Execute(context.Background(), makeReq("run", map[string]any{
		"language": "bash",
		"code":     "exit 42",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success (non-zero exit is still successful execution), got error: %s", resp.Error)
	}

	result := resp.Result.(map[string]any)
	exitCode := result["exit_code"].(int)
	if exitCode != 42 {
		t.Errorf("expected exit code 42, got %d", exitCode)
	}
}

func TestCodeTool_RunFilesCreated(t *testing.T) {
	tool, workspace := testTool(t)

	resp, err := tool.Execute(context.Background(), makeReq("run", map[string]any{
		"language": "bash",
		"code":     `echo "data" > "$HOME/output.txt"`,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	result := resp.Result.(map[string]any)
	filesCreated := result["files_created"].([]string)

	if !slices.Contains(filesCreated, "output.txt") {
		t.Errorf("expected output.txt in files_created, got %v", filesCreated)
	}

	// Verify file actually exists.
	data, err := os.ReadFile(filepath.Join(workspace, "output.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != "data" {
		t.Errorf("expected file content %q, got %q", "data", string(data))
	}
}

func TestCodeTool_RunTempFileCleanedUp(t *testing.T) {
	tool, workspace := testTool(t)

	resp, err := tool.Execute(context.Background(), makeReq("run", map[string]any{
		"language": "bash",
		"code":     `echo "temp test"`,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	// Check that no files remain in tmp/ (temp file should have been cleaned up).
	tmpDir := filepath.Join(workspace, "tmp")
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sh") || strings.HasSuffix(e.Name(), ".py") || strings.HasSuffix(e.Name(), ".go") {
			t.Errorf("temp file %s was not cleaned up", e.Name())
		}
	}
}

func TestCodeTool_RunFile(t *testing.T) {
	tool, workspace := testTool(t)

	// Write a script to the workspace.
	script := `#!/bin/bash
echo "from file"
`
	scriptPath := filepath.Join(workspace, "test.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	resp, err := tool.Execute(context.Background(), makeReq("run_file", map[string]any{
		"path": "test.sh",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	result := resp.Result.(map[string]any)
	stdout := result["stdout"].(string)
	if stdout != "from file\n" {
		t.Errorf("expected %q, got %q", "from file\n", stdout)
	}
}

func TestCodeTool_RunFileUnsupportedExtension(t *testing.T) {
	tool, workspace := testTool(t)

	// Write a .rs file.
	rsPath := filepath.Join(workspace, "test.rs")
	if err := os.WriteFile(rsPath, []byte("fn main() {}"), 0o644); err != nil {
		t.Fatal(err)
	}

	resp, err := tool.Execute(context.Background(), makeReq("run_file", map[string]any{
		"path": "test.rs",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Error("expected unsupported extension to be rejected")
	}
}

func TestCodeTool_RunFilePathTraversal(t *testing.T) {
	tool, _ := testTool(t)

	resp, err := tool.Execute(context.Background(), makeReq("run_file", map[string]any{
		"path": "../../etc/passwd",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Error("expected path traversal to be rejected")
	}
}

func TestCodeTool_RunFileNotFound(t *testing.T) {
	tool, _ := testTool(t)

	resp, err := tool.Execute(context.Background(), makeReq("run_file", map[string]any{
		"path": "nonexistent.sh",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Error("expected nonexistent file to error")
	}
}

func TestCodeTool_RunFileDirectory(t *testing.T) {
	tool, workspace := testTool(t)

	// Create a directory in the workspace.
	dirPath := filepath.Join(workspace, "mydir.sh")
	if err := os.Mkdir(dirPath, 0o755); err != nil {
		t.Fatal(err)
	}

	resp, err := tool.Execute(context.Background(), makeReq("run_file", map[string]any{
		"path": "mydir.sh",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Error("expected directory to be rejected as run_file target")
	}
	if !strings.Contains(resp.Error, "not a regular file") {
		t.Errorf("expected 'not a regular file' error, got: %s", resp.Error)
	}
}
