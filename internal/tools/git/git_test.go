package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kkjorsvik/kyvik/internal/ktp"
)

func skipIfNoGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
}

// initTestRepo creates a git repo with an initial commit in a temp dir.
func initTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "git", "init")
	run(t, dir, "git", "config", "user.name", "Test")
	run(t, dir, "git", "config", "user.email", "test@test.com")
	writeFile(t, dir, "README.md", "# test")
	run(t, dir, "git", "add", "README.md")
	run(t, dir, "git", "commit", "-m", "initial commit")
	return dir
}

func run(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s failed: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func newTestTool(dir string) *Tool {
	return New(func(agentID string) (string, error) {
		return dir, nil
	})
}

func makeReq(action string, params map[string]any) ktp.ToolRequest {
	return ktp.NewToolRequest("test-agent", "git", action, params)
}

func TestDeclaration(t *testing.T) {
	skipIfNoGit(t)
	tool := newTestTool(t.TempDir())
	decl := tool.Declaration()
	if decl.Name != "git" {
		t.Errorf("expected name git, got %s", decl.Name)
	}
	if decl.Version != "1.0.0" {
		t.Errorf("expected version 1.0.0, got %s", decl.Version)
	}
	if decl.MinTier != ktp.TierWriter {
		t.Errorf("expected min tier writer, got %s", decl.MinTier)
	}
	if len(decl.Actions) != 11 {
		t.Errorf("expected 11 actions, got %d", len(decl.Actions))
	}
}

func TestExecute_Status(t *testing.T) {
	skipIfNoGit(t)
	dir := initTestRepo(t)
	tool := newTestTool(dir)

	resp, err := tool.Execute(context.Background(), makeReq("status", nil))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
	result := resp.Result.(map[string]any)
	if result["clean"] != true {
		t.Error("expected clean repo")
	}

	// Create a modified file.
	writeFile(t, dir, "new.txt", "hello")
	resp, err = tool.Execute(context.Background(), makeReq("status", nil))
	if err != nil {
		t.Fatal(err)
	}
	result = resp.Result.(map[string]any)
	if result["clean"] != false {
		t.Error("expected dirty repo")
	}
	files := result["files"].([]map[string]any)
	if len(files) == 0 {
		t.Error("expected at least one file in status")
	}
}

func TestExecute_Log(t *testing.T) {
	skipIfNoGit(t)
	dir := initTestRepo(t)
	tool := newTestTool(dir)

	resp, err := tool.Execute(context.Background(), makeReq("log", nil))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
	result := resp.Result.(map[string]any)
	commits := result["commits"].([]map[string]any)
	if len(commits) < 1 {
		t.Error("expected at least one commit")
	}
	if commits[0]["message"] != "initial commit" {
		t.Errorf("expected 'initial commit', got %q", commits[0]["message"])
	}
}

func TestExecute_Diff(t *testing.T) {
	skipIfNoGit(t)
	dir := initTestRepo(t)
	tool := newTestTool(dir)

	// Modify a file.
	writeFile(t, dir, "README.md", "# test\nmodified")

	resp, err := tool.Execute(context.Background(), makeReq("diff", nil))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
	result := resp.Result.(map[string]any)
	diff := result["diff"].(string)
	if !strings.Contains(diff, "modified") {
		t.Error("expected diff to contain 'modified'")
	}
}

func TestExecute_BranchCreate(t *testing.T) {
	skipIfNoGit(t)
	dir := initTestRepo(t)
	tool := newTestTool(dir)

	resp, err := tool.Execute(context.Background(), makeReq("branch_create", map[string]any{
		"name": "feature/test",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
	result := resp.Result.(map[string]any)
	if result["name"] != "feature/test" {
		t.Errorf("expected branch name feature/test, got %v", result["name"])
	}
	if result["sha"] == "" {
		t.Error("expected non-empty sha")
	}
}

func TestExecute_BranchList(t *testing.T) {
	skipIfNoGit(t)
	dir := initTestRepo(t)
	tool := newTestTool(dir)

	// Create an extra branch.
	run(t, dir, "git", "branch", "dev")

	resp, err := tool.Execute(context.Background(), makeReq("branch_list", nil))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
	result := resp.Result.(map[string]any)
	branches := result["branches"].([]map[string]any)
	if len(branches) < 2 {
		t.Errorf("expected at least 2 branches, got %d", len(branches))
	}
}

func TestExecute_Checkout(t *testing.T) {
	skipIfNoGit(t)
	dir := initTestRepo(t)
	tool := newTestTool(dir)

	run(t, dir, "git", "branch", "feature")

	resp, err := tool.Execute(context.Background(), makeReq("checkout", map[string]any{
		"ref": "feature",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
	result := resp.Result.(map[string]any)
	if result["ref"] != "feature" {
		t.Errorf("expected ref feature, got %v", result["ref"])
	}
}

func TestExecute_Add(t *testing.T) {
	skipIfNoGit(t)
	dir := initTestRepo(t)
	tool := newTestTool(dir)

	writeFile(t, dir, "new.txt", "content")

	resp, err := tool.Execute(context.Background(), makeReq("add", map[string]any{
		"paths": []any{"new.txt"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
}

func TestExecute_Commit(t *testing.T) {
	skipIfNoGit(t)
	dir := initTestRepo(t)
	tool := newTestTool(dir)

	writeFile(t, dir, "new.txt", "content")
	run(t, dir, "git", "add", "new.txt")

	resp, err := tool.Execute(context.Background(), makeReq("commit", map[string]any{
		"message": "add new file",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
	result := resp.Result.(map[string]any)
	if result["sha"] == "" {
		t.Error("expected non-empty sha")
	}
	if result["message"] != "add new file" {
		t.Errorf("expected message 'add new file', got %v", result["message"])
	}
}

func TestExecute_Clone(t *testing.T) {
	skipIfNoGit(t)

	// Create a bare repo to clone from.
	bareDir := t.TempDir()
	run(t, bareDir, "git", "init", "--bare")

	// Create a source repo, add a commit, push to the bare repo.
	srcDir := t.TempDir()
	run(t, srcDir, "git", "init")
	run(t, srcDir, "git", "config", "user.name", "Test")
	run(t, srcDir, "git", "config", "user.email", "test@test.com")
	writeFile(t, srcDir, "README.md", "# test")
	run(t, srcDir, "git", "add", "README.md")
	run(t, srcDir, "git", "commit", "-m", "initial")
	run(t, srcDir, "git", "remote", "add", "origin", bareDir)
	// Get the default branch name and push it.
	branchOut := strings.TrimSpace(run(t, srcDir, "git", "rev-parse", "--abbrev-ref", "HEAD"))
	run(t, srcDir, "git", "push", "origin", branchOut)

	// Clone from the bare repo.
	workDir := t.TempDir()
	tool := newTestTool(workDir)

	resp, err := tool.Execute(context.Background(), makeReq("clone", map[string]any{
		"url":       bareDir,
		"directory": "cloned",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
	result := resp.Result.(map[string]any)
	if result["directory"] != "cloned" {
		t.Errorf("expected directory 'cloned', got %v", result["directory"])
	}

	// Verify cloned repo exists.
	if _, err := os.Stat(filepath.Join(workDir, "cloned", "README.md")); err != nil {
		t.Errorf("expected cloned README.md to exist: %v", err)
	}
}

func TestExecute_PathTraversal(t *testing.T) {
	skipIfNoGit(t)
	dir := initTestRepo(t)
	tool := newTestTool(dir)

	// Try to add a file with path traversal.
	resp, err := tool.Execute(context.Background(), makeReq("add", map[string]any{
		"paths": []any{"../../etc/passwd"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Error("expected path traversal to be rejected")
	}
	if !strings.Contains(resp.Error, "traversal") {
		t.Errorf("expected traversal error, got: %s", resp.Error)
	}
}

func TestExecute_UnknownAction(t *testing.T) {
	skipIfNoGit(t)
	dir := initTestRepo(t)
	tool := newTestTool(dir)

	resp, err := tool.Execute(context.Background(), makeReq("nonexistent", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Error("expected unknown action to fail")
	}
	if !strings.Contains(resp.Error, "unknown action") {
		t.Errorf("expected 'unknown action' error, got: %s", resp.Error)
	}
}

func TestValidateArgs_BlockedOperations(t *testing.T) {
	tests := []struct {
		name string
		args []string
		err  string
	}{
		{"clean blocked", []string{"clean", "-fd"}, "git clean is not allowed"},
		{"reset hard blocked", []string{"reset", "--hard"}, "git reset --hard is not allowed"},
		{"config global blocked", []string{"config", "--global", "user.name", "x"}, "git config --global/--system is not allowed"},
		{"exec flag blocked", []string{"log", "--exec=sh"}, "--exec flag is not allowed"},
		{"upload-pack blocked", []string{"clone", "--upload-pack=evil"}, "--upload-pack flag is not allowed"},
		{"valid args pass", []string{"status"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateArgs(tt.args)
			if tt.err == "" {
				if err != nil {
					t.Errorf("expected no error, got: %v", err)
				}
			} else {
				if err == nil {
					t.Error("expected error, got nil")
				} else if !strings.Contains(err.Error(), tt.err) {
					t.Errorf("expected error containing %q, got: %v", tt.err, err)
				}
			}
		})
	}
}

func TestValidateRef(t *testing.T) {
	tests := []struct {
		ref   string
		valid bool
	}{
		{"main", true},
		{"feature/foo", true},
		{"v1.0.0", true},
		{"-flag", false},
		{"ref with space", false},
		{"ref;cmd", false},
		{"ref$(cmd)", false},
		{"ref`cmd`", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			err := validateRef(tt.ref)
			if tt.valid && err != nil {
				t.Errorf("expected valid, got error: %v", err)
			}
			if !tt.valid && err == nil {
				t.Error("expected invalid, got nil error")
			}
		})
	}
}
