package shell

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kkjorsvik/kyvik/internal/ktp"
)

func testTool(t *testing.T, opts ...func(*testOpts)) (*ShellTool, string) {
	t.Helper()
	workspace := t.TempDir()

	// Create tmp subdirectory.
	if err := os.MkdirAll(filepath.Join(workspace, "tmp"), 0o755); err != nil {
		t.Fatal(err)
	}

	o := &testOpts{
		allowedCmds: []string{"echo", "pwd", "false", "ls", "sleep", "cat", "sh", "rm"},
		tier:        ktp.TierOperator,
	}
	for _, fn := range opts {
		fn(o)
	}

	tool := New(
		func(agentID string) ([]string, error) {
			return o.allowedCmds, nil
		},
		func(agentID string) (string, error) {
			return workspace, nil
		},
		func(agentID string) (string, error) {
			return o.tier, nil
		},
	)
	return tool, workspace
}

type testOpts struct {
	allowedCmds []string
	tier        string
}

func withAllowedCmds(cmds []string) func(*testOpts) {
	return func(o *testOpts) {
		o.allowedCmds = cmds
	}
}

func withTier(tier string) func(*testOpts) {
	return func(o *testOpts) {
		o.tier = tier
	}
}

func makeReq(action string, params map[string]any) ktp.ToolRequest {
	return ktp.NewToolRequest("test-agent", "shell", action, params)
}

func TestShellTool_Declaration(t *testing.T) {
	tool, _ := testTool(t)
	decl := tool.Declaration()

	if decl.Name != "shell" {
		t.Errorf("expected name shell, got %s", decl.Name)
	}
	if decl.Version != "1.0.0" {
		t.Errorf("expected version 1.0.0, got %s", decl.Version)
	}
	if decl.MinTier != ktp.TierOperator {
		t.Errorf("expected min tier operator, got %s", decl.MinTier)
	}
	if len(decl.Actions) != 1 {
		t.Errorf("expected 1 action, got %d", len(decl.Actions))
	}
	if err := decl.Validate(); err != nil {
		t.Errorf("declaration validation failed: %v", err)
	}
}

func TestShellTool_ExecEcho(t *testing.T) {
	tool, _ := testTool(t)

	resp, err := tool.Execute(context.Background(), makeReq("exec", map[string]any{
		"command": "echo",
		"args":    []any{"hello", "world"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	result := resp.Result.(map[string]any)
	stdout := result["stdout"].(string)
	if stdout != "hello world\n" {
		t.Errorf("expected %q, got %q", "hello world\n", stdout)
	}
}

func TestShellTool_ExecWithWorkingDir(t *testing.T) {
	tool, workspace := testTool(t)

	subdir := filepath.Join(workspace, "subdir")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatal(err)
	}

	resp, err := tool.Execute(context.Background(), makeReq("exec", map[string]any{
		"command":     "pwd",
		"working_dir": "subdir",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	result := resp.Result.(map[string]any)
	stdout := result["stdout"].(string)
	// pwd output ends with newline.
	expected := subdir + "\n"
	if stdout != expected {
		t.Errorf("expected %q, got %q", expected, stdout)
	}
}

func TestShellTool_ExecWithEnv(t *testing.T) {
	tool, _ := testTool(t)

	resp, err := tool.Execute(context.Background(), makeReq("exec", map[string]any{
		"command": "sh",
		"args":    []any{"-c", "echo $MY_VAR"},
		"env":     map[string]any{"MY_VAR": "test_value"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	result := resp.Result.(map[string]any)
	stdout := result["stdout"].(string)
	if stdout != "test_value\n" {
		t.Errorf("expected %q, got %q", "test_value\n", stdout)
	}
}

func TestShellTool_ExecNonZeroExit(t *testing.T) {
	tool, _ := testTool(t)

	resp, err := tool.Execute(context.Background(), makeReq("exec", map[string]any{
		"command": "false",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success (non-zero exit is still a successful execution), got error: %s", resp.Error)
	}

	result := resp.Result.(map[string]any)
	exitCode := result["exit_code"].(int)
	if exitCode == 0 {
		t.Error("expected non-zero exit code")
	}
}

func TestShellTool_ExecTimeout(t *testing.T) {
	tool, _ := testTool(t)

	resp, err := tool.Execute(context.Background(), makeReq("exec", map[string]any{
		"command":         "sleep",
		"args":            []any{"10"},
		"timeout_seconds": 1,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	result := resp.Result.(map[string]any)
	timedOut := result["timed_out"].(bool)
	if !timedOut {
		t.Error("expected timed_out to be true")
	}
}

func TestShellTool_BlockedCommand(t *testing.T) {
	tool, _ := testTool(t)

	resp, err := tool.Execute(context.Background(), makeReq("exec", map[string]any{
		"command": "shutdown",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Error("expected blocked command to be rejected")
	}
	if resp.Error == "" {
		t.Error("expected error message")
	}
}

func TestShellTool_BlockedArgPattern(t *testing.T) {
	tool, _ := testTool(t)

	resp, err := tool.Execute(context.Background(), makeReq("exec", map[string]any{
		"command": "rm",
		"args":    []any{"-rf", "/"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Error("expected blocked arg pattern to be rejected")
	}
}

func TestShellTool_AllowlistEnforced(t *testing.T) {
	tool, _ := testTool(t, withAllowedCmds([]string{"ls", "cat"}))

	// "echo" should be denied.
	resp, err := tool.Execute(context.Background(), makeReq("exec", map[string]any{
		"command": "echo",
		"args":    []any{"hello"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Error("expected echo to be denied by allowlist")
	}

	// "ls" should be allowed.
	resp, err = tool.Execute(context.Background(), makeReq("exec", map[string]any{
		"command": "ls",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Errorf("expected ls to be allowed, got error: %s", resp.Error)
	}
}

func TestShellTool_AllowlistEmptyDeniesAll(t *testing.T) {
	tool, _ := testTool(t, withAllowedCmds(nil))

	resp, err := tool.Execute(context.Background(), makeReq("exec", map[string]any{
		"command": "echo",
		"args":    []any{"allowed"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Error("expected echo to be denied with empty allowlist")
	}
	if !strings.Contains(resp.Error, "empty allowlist") {
		t.Errorf("expected error about empty allowlist, got: %s", resp.Error)
	}
}

func TestShellTool_WorkingDirTraversal(t *testing.T) {
	tool, _ := testTool(t)

	resp, err := tool.Execute(context.Background(), makeReq("exec", map[string]any{
		"command":     "pwd",
		"working_dir": "../..",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Error("expected path traversal to be rejected")
	}
}

func TestShellTool_AbsoluteWorkingDirDenied(t *testing.T) {
	tool, _ := testTool(t, withTier(ktp.TierOperator))

	resp, err := tool.Execute(context.Background(), makeReq("exec", map[string]any{
		"command":     "pwd",
		"working_dir": "/tmp",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Error("expected absolute working dir to be denied for operator tier")
	}
}

func TestShellTool_NoShellInterpretation(t *testing.T) {
	tool, _ := testTool(t)

	resp, err := tool.Execute(context.Background(), makeReq("exec", map[string]any{
		"command": "echo",
		"args":    []any{"$(whoami)"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	result := resp.Result.(map[string]any)
	stdout := result["stdout"].(string)
	// Since we use exec.Command directly (no sh -c), $(whoami) should be literal.
	if stdout != "$(whoami)\n" {
		t.Errorf("expected literal %q, got %q (shell interpretation occurred)", "$(whoami)\n", stdout)
	}
}

// --- Admin tier tests ---

func TestShellTool_Admin_AbsoluteWorkingDirAllowed(t *testing.T) {
	absDir := t.TempDir()
	tool, _ := testTool(t, withTier(ktp.TierAdmin))

	resp, err := tool.Execute(context.Background(), makeReq("exec", map[string]any{
		"command":     "pwd",
		"working_dir": absDir,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected admin tier to allow absolute working dir, got error: %s", resp.Error)
	}

	result := resp.Result.(map[string]any)
	stdout := result["stdout"].(string)
	expected := absDir + "\n"
	if stdout != expected {
		t.Errorf("expected %q, got %q", expected, stdout)
	}
}

func TestShellTool_Admin_SkipsAllowlist(t *testing.T) {
	tool, _ := testTool(t, withTier(ktp.TierAdmin), withAllowedCmds([]string{"ls"}))

	// "echo" should be allowed for admin tier even with an allowlist.
	resp, err := tool.Execute(context.Background(), makeReq("exec", map[string]any{
		"command": "echo",
		"args":    []any{"hello"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected admin tier to skip allowlist, got error: %s", resp.Error)
	}
}

func TestShellTool_Admin_BlockedCommandsStillApply(t *testing.T) {
	tool, _ := testTool(t, withTier(ktp.TierAdmin))

	// Blocked commands should still be denied for admin tier.
	resp, err := tool.Execute(context.Background(), makeReq("exec", map[string]any{
		"command": "shutdown",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Error("expected blocked command to be rejected even for admin tier")
	}
}

func TestShellTool_EnvironmentNotInherited(t *testing.T) {
	// Set a parent env var.
	os.Setenv("KYVIK_TEST_PARENT_VAR", "should_not_be_visible")
	defer os.Unsetenv("KYVIK_TEST_PARENT_VAR")

	tool, _ := testTool(t)

	resp, err := tool.Execute(context.Background(), makeReq("exec", map[string]any{
		"command": "sh",
		"args":    []any{"-c", "echo ${KYVIK_TEST_PARENT_VAR:-empty}"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	result := resp.Result.(map[string]any)
	stdout := result["stdout"].(string)
	if stdout != "empty\n" {
		t.Errorf("expected parent env var to not be inherited, got %q", stdout)
	}
}

func TestShellTool_ProtectedEnvCannotBeOverridden(t *testing.T) {
	tool, _ := testTool(t)

	resp, err := tool.Execute(context.Background(), makeReq("exec", map[string]any{
		"command": "sh",
		"args":    []any{"-c", "echo $PATH"},
		"env":     map[string]any{"PATH": "/evil/path"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	result := resp.Result.(map[string]any)
	stdout := result["stdout"].(string)
	if strings.Contains(stdout, "/evil/path") {
		t.Errorf("expected PATH override to be filtered, got %q", stdout)
	}
}
