package file

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/kkjorsvik/kyvik/internal/ktp"
)

func testTool(t *testing.T) (*FileTool, string) {
	t.Helper()
	workspace := t.TempDir()
	tool := New(func(agentID string) (string, error) {
		return workspace, nil
	})
	return tool, workspace
}

func makeReq(action string, params map[string]any) ktp.ToolRequest {
	return ktp.NewToolRequest("test-agent", "file", action, params)
}

func TestFileTool_Read(t *testing.T) {
	tool, workspace := testTool(t)

	// Write a file to the workspace.
	content := "hello world"
	if err := os.WriteFile(filepath.Join(workspace, "test.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	resp, err := tool.Execute(context.Background(), makeReq("read", map[string]any{"path": "test.txt"}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	result := resp.Result.(map[string]any)
	if result["content"] != content {
		t.Errorf("expected %q, got %q", content, result["content"])
	}
}

func TestFileTool_Write(t *testing.T) {
	tool, workspace := testTool(t)

	resp, err := tool.Execute(context.Background(), makeReq("write", map[string]any{
		"path":    "out.txt",
		"content": "written content",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	data, err := os.ReadFile(filepath.Join(workspace, "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "written content" {
		t.Errorf("expected %q, got %q", "written content", string(data))
	}
}

func TestFileTool_WriteAppend(t *testing.T) {
	tool, workspace := testTool(t)

	// Write initial content.
	if err := os.WriteFile(filepath.Join(workspace, "append.txt"), []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}

	resp, err := tool.Execute(context.Background(), makeReq("write", map[string]any{
		"path":    "append.txt",
		"content": " second",
		"mode":    "append",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	data, err := os.ReadFile(filepath.Join(workspace, "append.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "first second" {
		t.Errorf("expected %q, got %q", "first second", string(data))
	}
}

func TestFileTool_List(t *testing.T) {
	tool, workspace := testTool(t)

	// Create files.
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(workspace, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}

	resp, err := tool.Execute(context.Background(), makeReq("list", map[string]any{"path": "."}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	result := resp.Result.(map[string]any)
	entries := result["entries"].([]listEntry)
	if len(entries) != 3 {
		t.Errorf("expected 3 entries, got %d", len(entries))
	}
}

func TestFileTool_ListRecursive(t *testing.T) {
	tool, workspace := testTool(t)

	// Create nested structure.
	nested := filepath.Join(workspace, "dir", "sub")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "deep.txt"), []byte("deep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "dir", "shallow.txt"), []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}

	resp, err := tool.Execute(context.Background(), makeReq("list", map[string]any{
		"path":      ".",
		"recursive": true,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	result := resp.Result.(map[string]any)
	entries := result["entries"].([]listEntry)

	// Should find: dir, dir/shallow.txt, dir/sub, dir/sub/deep.txt
	if len(entries) < 4 {
		t.Errorf("expected at least 4 entries in recursive listing, got %d", len(entries))
	}
}

func TestFileTool_Delete(t *testing.T) {
	tool, workspace := testTool(t)

	target := filepath.Join(workspace, "delete-me.txt")
	if err := os.WriteFile(target, []byte("bye"), 0o644); err != nil {
		t.Fatal(err)
	}

	resp, err := tool.Execute(context.Background(), makeReq("delete", map[string]any{"path": "delete-me.txt"}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Error("expected file to be deleted")
	}
}

func TestFileTool_Mkdir(t *testing.T) {
	tool, workspace := testTool(t)

	resp, err := tool.Execute(context.Background(), makeReq("mkdir", map[string]any{"path": "new/nested/dir"}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	info, err := os.Stat(filepath.Join(workspace, "new", "nested", "dir"))
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Error("expected directory")
	}
}

func TestFileTool_Stat(t *testing.T) {
	tool, workspace := testTool(t)

	content := "hello stat"
	if err := os.WriteFile(filepath.Join(workspace, "stat.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	resp, err := tool.Execute(context.Background(), makeReq("stat", map[string]any{"path": "stat.txt"}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	result := resp.Result.(map[string]any)
	if result["name"] != "stat.txt" {
		t.Errorf("expected name stat.txt, got %v", result["name"])
	}
	if result["size"] != int64(len(content)) {
		t.Errorf("expected size %d, got %v", len(content), result["size"])
	}
	if result["is_dir"] != false {
		t.Errorf("expected is_dir false, got %v", result["is_dir"])
	}
	if result["modified"] == nil || result["modified"] == "" {
		t.Error("expected modified timestamp")
	}
}

func TestFileTool_PathTraversal(t *testing.T) {
	tool, _ := testTool(t)

	cases := []struct {
		name string
		path string
	}{
		{"dotdot", "../etc/passwd"},
		{"absolute", "/etc/passwd"},
		{"sneaky", "foo/../../.."},
		{"dotdot prefix", ".."},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := tool.Execute(context.Background(), makeReq("read", map[string]any{"path": tc.path}))
			if err != nil {
				t.Fatal(err)
			}
			if resp.Success {
				t.Errorf("expected path traversal to be rejected for %q", tc.path)
			}
		})
	}
}

func TestFileTool_ReadNonexistent(t *testing.T) {
	tool, _ := testTool(t)

	resp, err := tool.Execute(context.Background(), makeReq("read", map[string]any{"path": "does-not-exist.txt"}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Error("expected failure for nonexistent file")
	}
	if resp.Error == "" {
		t.Error("expected error message")
	}
}

// --- Power / Unrestricted tier tests ---

func testToolWithTier(t *testing.T, tier string, hostPaths *HostPathConfig) (*FileTool, string) {
	t.Helper()
	workspace := t.TempDir()
	var opts []Option
	opts = append(opts, WithTierFunc(func(agentID string) (string, error) {
		return tier, nil
	}))
	if hostPaths != nil {
		opts = append(opts, WithHostPathsFunc(func(agentID string) (*HostPathConfig, error) {
			return hostPaths, nil
		}))
	}
	tool := New(func(agentID string) (string, error) {
		return workspace, nil
	}, opts...)
	return tool, workspace
}

func TestFileTool_Admin_WorkspacePaths(t *testing.T) {
	tool, workspace := testToolWithTier(t, "admin", &HostPathConfig{
		Read:  []string{"/opt/data"},
		Write: []string{"/opt/data"},
	})

	// Relative workspace paths should still work.
	if err := os.WriteFile(filepath.Join(workspace, "ws.txt"), []byte("workspace"), 0o644); err != nil {
		t.Fatal(err)
	}

	resp, err := tool.Execute(context.Background(), makeReq("read", map[string]any{"path": "ws.txt"}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success for workspace path, got error: %s", resp.Error)
	}
	result := resp.Result.(map[string]any)
	if result["content"] != "workspace" {
		t.Errorf("expected 'workspace', got %q", result["content"])
	}
}

func TestFileTool_Admin_HostReadAllowed(t *testing.T) {
	// Create a temp dir to act as allowed host read path.
	hostDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(hostDir, "data.txt"), []byte("host-data"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool, _ := testToolWithTier(t, "admin", &HostPathConfig{
		Read: []string{hostDir},
	})

	resp, err := tool.Execute(context.Background(), makeReq("read", map[string]any{
		"path": filepath.Join(hostDir, "data.txt"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success for allowed host read, got error: %s", resp.Error)
	}
	result := resp.Result.(map[string]any)
	if result["content"] != "host-data" {
		t.Errorf("expected 'host-data', got %q", result["content"])
	}
}

func TestFileTool_Admin_HostWriteAllowed(t *testing.T) {
	hostDir := t.TempDir()

	tool, _ := testToolWithTier(t, "admin", &HostPathConfig{
		Write: []string{hostDir},
	})

	outPath := filepath.Join(hostDir, "out.txt")
	resp, err := tool.Execute(context.Background(), makeReq("write", map[string]any{
		"path":    outPath,
		"content": "admin-write",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success for allowed host write, got error: %s", resp.Error)
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "admin-write" {
		t.Errorf("expected 'admin-write', got %q", string(data))
	}
}

func TestFileTool_Admin_DeniedPathRejected(t *testing.T) {
	tool, _ := testToolWithTier(t, "admin", &HostPathConfig{
		Read: []string{"/opt/data"},
		Deny: []string{"/opt/data/secret"},
	})

	resp, err := tool.Execute(context.Background(), makeReq("read", map[string]any{
		"path": "/opt/data/secret/file.txt",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Error("expected denied path to be rejected")
	}
}

func TestFileTool_Admin_DefaultDenyRejected(t *testing.T) {
	tool, _ := testToolWithTier(t, "admin", &HostPathConfig{
		Read: []string{"/etc"},
	})

	// /etc/shadow is in defaultDenyPaths.
	resp, err := tool.Execute(context.Background(), makeReq("read", map[string]any{
		"path": "/etc/shadow",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Error("expected /etc/shadow to be rejected by default deny")
	}
}

func TestFileTool_Admin_NonAllowlistedAbsRejected(t *testing.T) {
	tool, _ := testToolWithTier(t, "admin", &HostPathConfig{
		Read: []string{"/opt/data"},
	})

	resp, err := tool.Execute(context.Background(), makeReq("read", map[string]any{
		"path": "/var/log/syslog",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Error("expected non-allowlisted absolute path to be rejected for admin tier")
	}
}

func TestFileTool_Admin_ReadSystemPathAllowed(t *testing.T) {
	// Admin tier without host paths configured can still read system paths.
	// This is covered by TestFileTool_Admin_ReadSystemPath.
	// This test verifies admin without host paths rejects arbitrary absolute paths.
	hostDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(hostDir, "any.txt"), []byte("some-data"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool, _ := testToolWithTier(t, "admin", nil)

	resp, err := tool.Execute(context.Background(), makeReq("read", map[string]any{
		"path": filepath.Join(hostDir, "any.txt"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Error("expected admin tier without host paths to deny arbitrary absolute path")
	}
}

func TestFileTool_Standard_AbsolutePathRejected(t *testing.T) {
	// Standard tiers (no tier func or admin) should reject absolute paths.
	tool, _ := testToolWithTier(t, "admin", nil)

	resp, err := tool.Execute(context.Background(), makeReq("read", map[string]any{
		"path": "/etc/hostname",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Error("expected absolute path to be rejected for admin tier")
	}
}

func TestFileTool_Admin_SymlinkEscapeRejected(t *testing.T) {
	// Create two temp dirs: one allowed, one not.
	allowedDir := t.TempDir()
	outsideDir := t.TempDir()

	// Create a target file outside the allowed dir.
	targetFile := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(targetFile, []byte("secret-data"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a symlink inside the allowed dir pointing to the outside file.
	symlinkPath := filepath.Join(allowedDir, "escape.txt")
	if err := os.Symlink(targetFile, symlinkPath); err != nil {
		t.Fatal(err)
	}

	tool, _ := testToolWithTier(t, "admin", &HostPathConfig{
		Read: []string{allowedDir},
	})

	resp, err := tool.Execute(context.Background(), makeReq("read", map[string]any{
		"path": symlinkPath,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Error("expected symlink escape to be rejected")
	}
}

func TestFileTool_Admin_ReadSystemPath(t *testing.T) {
	// Create a temp dir to simulate /etc/kyvik.
	tmpDir := t.TempDir()
	// Override adminReadPaths for testing.
	origPaths := adminReadPaths
	adminReadPaths = []string{tmpDir}
	defer func() { adminReadPaths = origPaths }()

	if err := os.WriteFile(filepath.Join(tmpDir, "kyvik.yaml"), []byte("test-config"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool, _ := testToolWithTier(t, "admin", nil)

	resp, err := tool.Execute(context.Background(), makeReq("read", map[string]any{
		"path": filepath.Join(tmpDir, "kyvik.yaml"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected admin to read system path, got error: %s", resp.Error)
	}
	result := resp.Result.(map[string]any)
	if result["content"] != "test-config" {
		t.Errorf("expected 'test-config', got %q", result["content"])
	}
}

func TestFileTool_Admin_WriteSystemPathRejected(t *testing.T) {
	tmpDir := t.TempDir()
	origPaths := adminReadPaths
	adminReadPaths = []string{tmpDir}
	defer func() { adminReadPaths = origPaths }()

	tool, _ := testToolWithTier(t, "admin", nil)

	resp, err := tool.Execute(context.Background(), makeReq("write", map[string]any{
		"path":    filepath.Join(tmpDir, "malicious.txt"),
		"content": "hacked",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Error("expected admin write to system path to be rejected")
	}
}

func TestFileTool_Admin_NonSystemPathRejected(t *testing.T) {
	tool, _ := testToolWithTier(t, "admin", nil)

	resp, err := tool.Execute(context.Background(), makeReq("read", map[string]any{
		"path": "/var/log/syslog",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Error("expected admin to be rejected for non-system paths")
	}
}

func TestFileTool_Worker_ExtraPathsAllowed(t *testing.T) {
	// Create temp dir to act as extra mount path.
	mountDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(mountDir, "shared.txt"), []byte("shared-data"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool, _ := testToolWithTier(t, "writer", &HostPathConfig{
		Read:  []string{mountDir},
		Write: []string{mountDir},
	})

	resp, err := tool.Execute(context.Background(), makeReq("read", map[string]any{
		"path": filepath.Join(mountDir, "shared.txt"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected worker with extra_paths to read mount, got error: %s", resp.Error)
	}
	result := resp.Result.(map[string]any)
	if result["content"] != "shared-data" {
		t.Errorf("expected 'shared-data', got %q", result["content"])
	}
}

func TestFileTool_Worker_NoExtraPathsDenied(t *testing.T) {
	tool, _ := testToolWithTier(t, "writer", nil)

	resp, err := tool.Execute(context.Background(), makeReq("read", map[string]any{
		"path": "/mnt/nfs/data.txt",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Error("expected worker without extra_paths to be denied absolute paths")
	}
}

func TestFileTool_Declaration(t *testing.T) {
	tool, _ := testTool(t)
	decl := tool.Declaration()

	if decl.Name != "file" {
		t.Errorf("expected name file, got %s", decl.Name)
	}
	if decl.Version != "1.0.0" {
		t.Errorf("expected version 1.0.0, got %s", decl.Version)
	}
	if decl.MinTier != ktp.TierReader {
		t.Errorf("expected min tier reader, got %s", decl.MinTier)
	}
	if len(decl.Actions) != 7 {
		t.Errorf("expected 7 actions, got %d", len(decl.Actions))
	}
	if err := decl.Validate(); err != nil {
		t.Errorf("declaration validation failed: %v", err)
	}

	// Verify destructive flags.
	for _, a := range decl.Actions {
		if a.Name == "delete" && !a.Destructive {
			t.Error("delete action should be destructive")
		}
		if a.Name == "read" && a.Destructive {
			t.Error("read action should not be destructive")
		}
	}
}

func TestFileTool_Edit(t *testing.T) {
	tool, workspace := testTool(t)

	// Create a file to edit.
	original := "Hello world\nThis is a test\nGoodbye world\n"
	if err := os.WriteFile(filepath.Join(workspace, "edit.txt"), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	resp, err := tool.Execute(context.Background(), makeReq("edit", map[string]any{
		"path":       "edit.txt",
		"old_string": "This is a test",
		"new_string": "This has been edited",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatal("expected map result")
	}
	if result["replacements"] != 1 {
		t.Errorf("expected 1 replacement, got %v", result["replacements"])
	}

	// Verify file content.
	data, _ := os.ReadFile(filepath.Join(workspace, "edit.txt"))
	if string(data) != "Hello world\nThis has been edited\nGoodbye world\n" {
		t.Errorf("unexpected content: %s", string(data))
	}
}

func TestFileTool_Edit_NotFound(t *testing.T) {
	tool, workspace := testTool(t)

	os.WriteFile(filepath.Join(workspace, "edit.txt"), []byte("hello"), 0o644)

	resp, err := tool.Execute(context.Background(), makeReq("edit", map[string]any{
		"path":       "edit.txt",
		"old_string": "nonexistent",
		"new_string": "replacement",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Fatal("expected failure for old_string not found")
	}
}

func TestFileTool_Edit_MultipleMatches(t *testing.T) {
	tool, workspace := testTool(t)

	os.WriteFile(filepath.Join(workspace, "edit.txt"), []byte("foo bar foo baz foo"), 0o644)

	// Without replace_all — should fail.
	resp, err := tool.Execute(context.Background(), makeReq("edit", map[string]any{
		"path":       "edit.txt",
		"old_string": "foo",
		"new_string": "qux",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Fatal("expected failure for multiple matches without replace_all")
	}

	// With replace_all — should succeed.
	resp, err = tool.Execute(context.Background(), makeReq("edit", map[string]any{
		"path":        "edit.txt",
		"old_string":  "foo",
		"new_string":  "qux",
		"replace_all": true,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success with replace_all, got: %s", resp.Error)
	}

	result := resp.Result.(map[string]any)
	if result["replacements"] != 3 {
		t.Errorf("expected 3 replacements, got %v", result["replacements"])
	}

	data, _ := os.ReadFile(filepath.Join(workspace, "edit.txt"))
	if string(data) != "qux bar qux baz qux" {
		t.Errorf("unexpected content: %s", string(data))
	}
}

func TestFileTool_Edit_Identical(t *testing.T) {
	tool, workspace := testTool(t)

	os.WriteFile(filepath.Join(workspace, "edit.txt"), []byte("hello"), 0o644)

	resp, err := tool.Execute(context.Background(), makeReq("edit", map[string]any{
		"path":       "edit.txt",
		"old_string": "hello",
		"new_string": "hello",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Fatal("expected failure for identical strings")
	}
}

func TestFileTool_Edit_Deletion(t *testing.T) {
	tool, workspace := testTool(t)

	os.WriteFile(filepath.Join(workspace, "edit.txt"), []byte("keep this remove this keep this too"), 0o644)

	resp, err := tool.Execute(context.Background(), makeReq("edit", map[string]any{
		"path":       "edit.txt",
		"old_string": " remove this",
		"new_string": "",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got: %s", resp.Error)
	}

	data, _ := os.ReadFile(filepath.Join(workspace, "edit.txt"))
	if string(data) != "keep this keep this too" {
		t.Errorf("unexpected content: %s", string(data))
	}
}

func TestFileTool_Edit_PreservesPermissions(t *testing.T) {
	tool, workspace := testTool(t)

	fp := filepath.Join(workspace, "script.sh")
	os.WriteFile(fp, []byte("#!/bin/bash\necho hello\n"), 0o755)

	resp, err := tool.Execute(context.Background(), makeReq("edit", map[string]any{
		"path":       "script.sh",
		"old_string": "echo hello",
		"new_string": "echo goodbye",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got: %s", resp.Error)
	}

	info, _ := os.Stat(fp)
	if info.Mode().Perm() != 0o755 {
		t.Errorf("expected 0755 permissions, got %o", info.Mode().Perm())
	}
}
