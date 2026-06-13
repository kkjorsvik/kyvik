package file

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kkjorsvik/kyvik/internal/ktp"
)

func testToolWithSkillPaths(t *testing.T, readPaths, writePaths []string) (*FileTool, string) {
	t.Helper()
	workspace := t.TempDir()
	tool := New(
		func(agentID string) (string, error) { return workspace, nil },
		WithSkillPaths(readPaths, writePaths),
	)
	return tool, workspace
}

func TestSkillPaths_ReadAllowed(t *testing.T) {
	tool, workspace := testToolWithSkillPaths(t,
		[]string{"data/"},  // read allowed
		[]string{"output/"}, // write allowed
	)

	// Create data directory and file.
	dataDir := filepath.Join(workspace, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "input.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	req := ktp.NewToolRequest("test-agent", "file", "read", map[string]any{"path": "data/input.txt"})
	resp, err := tool.Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success for reading within skill read paths, got error: %s", resp.Error)
	}
	result := resp.Result.(map[string]any)
	if result["content"] != "hello" {
		t.Errorf("expected 'hello', got %q", result["content"])
	}
}

func TestSkillPaths_ReadDenied(t *testing.T) {
	tool, workspace := testToolWithSkillPaths(t,
		[]string{"data/"},  // read allowed only in data/
		[]string{"output/"}, // write allowed
	)

	// Create a file outside the allowed read paths.
	secretDir := filepath.Join(workspace, "secrets")
	if err := os.MkdirAll(secretDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secretDir, "key.pem"), []byte("private"), 0o644); err != nil {
		t.Fatal(err)
	}

	req := ktp.NewToolRequest("test-agent", "file", "read", map[string]any{"path": "secrets/key.pem"})
	resp, err := tool.Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Fatal("expected denial for reading outside skill read paths")
	}
	if !strings.Contains(resp.Error, "skill sandbox policy denies read access") {
		t.Fatalf("expected skill sandbox denial error, got: %s", resp.Error)
	}
}

func TestSkillPaths_WriteAllowed(t *testing.T) {
	tool, workspace := testToolWithSkillPaths(t,
		[]string{"data/"},   // read allowed
		[]string{"output/"}, // write allowed
	)

	// Create output directory.
	if err := os.MkdirAll(filepath.Join(workspace, "output"), 0o755); err != nil {
		t.Fatal(err)
	}

	req := ktp.NewToolRequest("test-agent", "file", "write", map[string]any{
		"path":    "output/result.txt",
		"content": "results here",
	})
	resp, err := tool.Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success for writing within skill write paths, got error: %s", resp.Error)
	}

	// Verify file was written.
	data, err := os.ReadFile(filepath.Join(workspace, "output", "result.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "results here" {
		t.Errorf("expected 'results here', got %q", string(data))
	}
}

func TestSkillPaths_WriteDenied(t *testing.T) {
	tool, workspace := testToolWithSkillPaths(t,
		[]string{"data/"},   // read allowed
		[]string{"output/"}, // write allowed only in output/
	)

	// Try to write outside the allowed write paths.
	if err := os.MkdirAll(filepath.Join(workspace, "data"), 0o755); err != nil {
		t.Fatal(err)
	}

	req := ktp.NewToolRequest("test-agent", "file", "write", map[string]any{
		"path":    "data/tampered.txt",
		"content": "hacked!",
	})
	resp, err := tool.Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Fatal("expected denial for writing outside skill write paths")
	}
	if !strings.Contains(resp.Error, "skill sandbox policy denies write access") {
		t.Fatalf("expected skill sandbox denial error, got: %s", resp.Error)
	}
}

func TestSkillPaths_DeleteDenied(t *testing.T) {
	tool, workspace := testToolWithSkillPaths(t,
		[]string{"data/"},   // read allowed
		[]string{"output/"}, // write allowed only in output/
	)

	// Create a file in data/ and try to delete it.
	dataDir := filepath.Join(workspace, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "file.txt"), []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}

	req := ktp.NewToolRequest("test-agent", "file", "delete", map[string]any{"path": "data/file.txt"})
	resp, err := tool.Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Fatal("expected denial for deleting outside skill write paths")
	}
	if !strings.Contains(resp.Error, "skill sandbox policy denies write access") {
		t.Fatalf("expected skill sandbox denial error, got: %s", resp.Error)
	}
}

func TestSkillPaths_NoRestrictionsAllowsAll(t *testing.T) {
	// No skill paths means no restrictions (agent defaults apply).
	tool, workspace := testToolWithSkillPaths(t, nil, nil)

	// Write anywhere in workspace.
	if err := os.MkdirAll(filepath.Join(workspace, "anywhere"), 0o755); err != nil {
		t.Fatal(err)
	}

	req := ktp.NewToolRequest("test-agent", "file", "write", map[string]any{
		"path":    "anywhere/file.txt",
		"content": "free access",
	})
	resp, err := tool.Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success with no skill restrictions, got error: %s", resp.Error)
	}
}

func TestSkillPaths_ListDenied(t *testing.T) {
	tool, workspace := testToolWithSkillPaths(t,
		[]string{"data/"},   // read allowed only in data/
		[]string{"output/"}, // write allowed
	)

	// Create a directory outside allowed read paths.
	if err := os.MkdirAll(filepath.Join(workspace, "secrets"), 0o755); err != nil {
		t.Fatal(err)
	}

	req := ktp.NewToolRequest("test-agent", "file", "list", map[string]any{"path": "secrets/"})
	resp, err := tool.Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Fatal("expected denial for listing outside skill read paths")
	}
	if !strings.Contains(resp.Error, "skill sandbox policy denies read access") {
		t.Fatalf("expected skill sandbox denial error, got: %s", resp.Error)
	}
}

func TestSkillPaths_MkdirDenied(t *testing.T) {
	tool, _ := testToolWithSkillPaths(t,
		[]string{"data/"},   // read allowed
		[]string{"output/"}, // write allowed only in output/
	)

	req := ktp.NewToolRequest("test-agent", "file", "mkdir", map[string]any{"path": "forbidden-dir"})
	resp, err := tool.Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Fatal("expected denial for mkdir outside skill write paths")
	}
	if !strings.Contains(resp.Error, "skill sandbox policy denies write access") {
		t.Fatalf("expected skill sandbox denial error, got: %s", resp.Error)
	}
}

func TestSkillPaths_StatDenied(t *testing.T) {
	tool, workspace := testToolWithSkillPaths(t,
		[]string{"data/"},   // read allowed only in data/
		[]string{"output/"}, // write allowed
	)

	// Create a file outside allowed read paths.
	if err := os.WriteFile(filepath.Join(workspace, "root.txt"), []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}

	req := ktp.NewToolRequest("test-agent", "file", "stat", map[string]any{"path": "root.txt"})
	resp, err := tool.Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Fatal("expected denial for stat outside skill read paths")
	}
	if !strings.Contains(resp.Error, "skill sandbox policy denies read access") {
		t.Fatalf("expected skill sandbox denial error, got: %s", resp.Error)
	}
}
