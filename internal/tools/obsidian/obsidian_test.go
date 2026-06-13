package obsidian

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/kkjorsvik/kyvik/internal/ktp"
)

func newTestTool(vaultDir string) *Tool {
	return New(
		WithVaultAccess(func(agentID string) ([]string, error) { return []string{"test"}, nil }),
		WithVaultPath(func(ctx context.Context, name string) (string, error) { return vaultDir, nil }),
	)
}

func makeReq(action string, params map[string]any) ktp.ToolRequest {
	return ktp.NewToolRequest("agent-1", "obsidian", action, params)
}

func TestDeclaration(t *testing.T) {
	tool := New()
	decl := tool.Declaration()

	if decl.Name != "obsidian" {
		t.Fatalf("expected name 'obsidian', got %q", decl.Name)
	}
	if decl.Version != "1.0.0" {
		t.Fatalf("expected version '1.0.0', got %q", decl.Version)
	}
	if decl.MinTier != ktp.TierReader {
		t.Fatalf("expected min tier %q, got %q", ktp.TierReader, decl.MinTier)
	}
	if len(decl.Actions) != 9 {
		t.Fatalf("expected 9 actions, got %d", len(decl.Actions))
	}
	if err := decl.Validate(); err != nil {
		t.Fatalf("declaration validation failed: %s", err)
	}
}

func TestInline(t *testing.T) {
	tool := New()
	if !tool.Inline() {
		t.Fatal("expected Inline() to return true")
	}
}

func TestReadWrite(t *testing.T) {
	vaultDir := t.TempDir()
	tool := newTestTool(vaultDir)
	ctx := context.Background()

	// Write a note.
	resp, err := tool.Execute(ctx, makeReq("write", map[string]any{
		"vault":   "test",
		"path":    "notes/hello.md",
		"content": "# Hello World\n\nThis is a test note.",
	}))
	if err != nil {
		t.Fatalf("write error: %s", err)
	}
	if !resp.Success {
		t.Fatalf("write failed: %s", resp.Error)
	}

	// Read it back.
	resp, err = tool.Execute(ctx, makeReq("read", map[string]any{
		"vault": "test",
		"path":  "notes/hello.md",
	}))
	if err != nil {
		t.Fatalf("read error: %s", err)
	}
	if !resp.Success {
		t.Fatalf("read failed: %s", resp.Error)
	}

	result := resp.Result.(map[string]any)
	if result["content"] != "# Hello World\n\nThis is a test note." {
		t.Fatalf("unexpected content: %v", result["content"])
	}
}

func TestEdit(t *testing.T) {
	vaultDir := t.TempDir()
	tool := newTestTool(vaultDir)
	ctx := context.Background()

	// Write initial note.
	tool.Execute(ctx, makeReq("write", map[string]any{
		"vault":   "test",
		"path":    "edit-me.md",
		"content": "Hello World",
	}))

	// Edit it.
	resp, err := tool.Execute(ctx, makeReq("edit", map[string]any{
		"vault":    "test",
		"path":     "edit-me.md",
		"old_text": "World",
		"new_text": "Go",
	}))
	if err != nil {
		t.Fatalf("edit error: %s", err)
	}
	if !resp.Success {
		t.Fatalf("edit failed: %s", resp.Error)
	}

	// Read back.
	resp, _ = tool.Execute(ctx, makeReq("read", map[string]any{
		"vault": "test",
		"path":  "edit-me.md",
	}))
	result := resp.Result.(map[string]any)
	if result["content"] != "Hello Go" {
		t.Fatalf("expected 'Hello Go', got %q", result["content"])
	}
}

func TestEditOldTextNotFound(t *testing.T) {
	vaultDir := t.TempDir()
	tool := newTestTool(vaultDir)
	ctx := context.Background()

	tool.Execute(ctx, makeReq("write", map[string]any{
		"vault":   "test",
		"path":    "note.md",
		"content": "some content",
	}))

	resp, err := tool.Execute(ctx, makeReq("edit", map[string]any{
		"vault":    "test",
		"path":     "note.md",
		"old_text": "nonexistent",
		"new_text": "replacement",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if resp.Success {
		t.Fatal("expected failure when old_text not found")
	}
}

func TestList(t *testing.T) {
	vaultDir := t.TempDir()
	tool := newTestTool(vaultDir)
	ctx := context.Background()

	// Create some files.
	tool.Execute(ctx, makeReq("write", map[string]any{"vault": "test", "path": "a.md", "content": "a"}))
	tool.Execute(ctx, makeReq("write", map[string]any{"vault": "test", "path": "sub/b.md", "content": "b"}))
	tool.Execute(ctx, makeReq("write", map[string]any{"vault": "test", "path": "sub/c.md", "content": "c"}))

	// Non-recursive list at root.
	resp, _ := tool.Execute(ctx, makeReq("list", map[string]any{"vault": "test"}))
	if !resp.Success {
		t.Fatalf("list failed: %s", resp.Error)
	}
	result := resp.Result.(map[string]any)
	files := result["files"].([]string)
	if len(files) != 1 {
		t.Fatalf("expected 1 file at root, got %d: %v", len(files), files)
	}

	// Recursive list.
	resp, _ = tool.Execute(ctx, makeReq("list", map[string]any{"vault": "test", "recursive": true}))
	result = resp.Result.(map[string]any)
	files = result["files"].([]string)
	if len(files) != 3 {
		t.Fatalf("expected 3 files recursively, got %d: %v", len(files), files)
	}

	// List subfolder.
	resp, _ = tool.Execute(ctx, makeReq("list", map[string]any{"vault": "test", "folder": "sub"}))
	result = resp.Result.(map[string]any)
	files = result["files"].([]string)
	if len(files) != 2 {
		t.Fatalf("expected 2 files in sub/, got %d: %v", len(files), files)
	}
}

func TestListSkipsObsidianDir(t *testing.T) {
	vaultDir := t.TempDir()
	tool := newTestTool(vaultDir)
	ctx := context.Background()

	// Create .obsidian dir with a file.
	obsDir := filepath.Join(vaultDir, ".obsidian")
	os.MkdirAll(obsDir, 0755)
	os.WriteFile(filepath.Join(obsDir, "config.md"), []byte("config"), 0644)

	// Create a normal note.
	tool.Execute(ctx, makeReq("write", map[string]any{"vault": "test", "path": "real.md", "content": "real"}))

	resp, _ := tool.Execute(ctx, makeReq("list", map[string]any{"vault": "test", "recursive": true}))
	result := resp.Result.(map[string]any)
	files := result["files"].([]string)
	if len(files) != 1 {
		t.Fatalf("expected 1 file (skipping .obsidian), got %d: %v", len(files), files)
	}
}

func TestSearch(t *testing.T) {
	vaultDir := t.TempDir()
	tool := newTestTool(vaultDir)
	ctx := context.Background()

	tool.Execute(ctx, makeReq("write", map[string]any{"vault": "test", "path": "findme.md", "content": "The quick brown fox"}))
	tool.Execute(ctx, makeReq("write", map[string]any{"vault": "test", "path": "nope.md", "content": "Nothing here"}))

	resp, _ := tool.Execute(ctx, makeReq("search", map[string]any{"vault": "test", "query": "quick brown"}))
	if !resp.Success {
		t.Fatalf("search failed: %s", resp.Error)
	}
	result := resp.Result.(map[string]any)
	results := result["results"].([]map[string]any)
	if len(results) != 1 {
		t.Fatalf("expected 1 search result, got %d", len(results))
	}
	if results[0]["path"] != "findme.md" {
		t.Fatalf("expected path 'findme.md', got %q", results[0]["path"])
	}
}

func TestDelete(t *testing.T) {
	vaultDir := t.TempDir()
	tool := newTestTool(vaultDir)
	ctx := context.Background()

	tool.Execute(ctx, makeReq("write", map[string]any{"vault": "test", "path": "delete-me.md", "content": "bye"}))

	resp, _ := tool.Execute(ctx, makeReq("delete", map[string]any{"vault": "test", "path": "delete-me.md"}))
	if !resp.Success {
		t.Fatalf("delete failed: %s", resp.Error)
	}

	// Verify it's gone.
	resp, _ = tool.Execute(ctx, makeReq("read", map[string]any{"vault": "test", "path": "delete-me.md"}))
	if resp.Success {
		t.Fatal("expected read to fail after delete")
	}
}

func TestMove(t *testing.T) {
	vaultDir := t.TempDir()
	tool := newTestTool(vaultDir)
	ctx := context.Background()

	tool.Execute(ctx, makeReq("write", map[string]any{"vault": "test", "path": "old.md", "content": "moving"}))

	resp, _ := tool.Execute(ctx, makeReq("move", map[string]any{
		"vault":     "test",
		"from_path": "old.md",
		"to_path":   "archive/old.md",
	}))
	if !resp.Success {
		t.Fatalf("move failed: %s", resp.Error)
	}

	// Old path should be gone.
	resp, _ = tool.Execute(ctx, makeReq("read", map[string]any{"vault": "test", "path": "old.md"}))
	if resp.Success {
		t.Fatal("expected old path to be gone after move")
	}

	// New path should exist.
	resp, _ = tool.Execute(ctx, makeReq("read", map[string]any{"vault": "test", "path": "archive/old.md"}))
	if !resp.Success {
		t.Fatalf("expected note at new path: %s", resp.Error)
	}
	result := resp.Result.(map[string]any)
	if result["content"] != "moving" {
		t.Fatalf("expected content 'moving', got %q", result["content"])
	}
}

func TestTags(t *testing.T) {
	vaultDir := t.TempDir()
	tool := newTestTool(vaultDir)
	ctx := context.Background()

	tool.Execute(ctx, makeReq("write", map[string]any{
		"vault":   "test",
		"path":    "tagged.md",
		"content": "# Tagged\n\n#project #go #project",
	}))

	// All tags.
	resp, _ := tool.Execute(ctx, makeReq("tags", map[string]any{"vault": "test"}))
	if !resp.Success {
		t.Fatalf("tags failed: %s", resp.Error)
	}
	result := resp.Result.(map[string]any)
	tags := result["tags"].(map[string]int)
	if tags["project"] != 1 {
		t.Fatalf("expected project tag count 1, got %d", tags["project"])
	}

	// Filtered tag.
	resp, _ = tool.Execute(ctx, makeReq("tags", map[string]any{"vault": "test", "tag": "go"}))
	result = resp.Result.(map[string]any)
	tags = result["tags"].(map[string]int)
	if len(tags) != 1 || tags["go"] != 1 {
		t.Fatalf("expected only 'go' tag, got %v", tags)
	}

	// Non-existent tag filter.
	resp, _ = tool.Execute(ctx, makeReq("tags", map[string]any{"vault": "test", "tag": "nope"}))
	result = resp.Result.(map[string]any)
	tags = result["tags"].(map[string]int)
	if len(tags) != 0 {
		t.Fatalf("expected empty tags, got %v", tags)
	}
}

func TestLinks(t *testing.T) {
	vaultDir := t.TempDir()
	tool := newTestTool(vaultDir)
	ctx := context.Background()

	// Create a note with outgoing links.
	tool.Execute(ctx, makeReq("write", map[string]any{
		"vault":   "test",
		"path":    "source.md",
		"content": "See [[target]] and [other](other.md).",
	}))

	// Create the target note that links back.
	tool.Execute(ctx, makeReq("write", map[string]any{
		"vault":   "test",
		"path":    "backlinker.md",
		"content": "References [[source]].",
	}))

	resp, _ := tool.Execute(ctx, makeReq("links", map[string]any{"vault": "test", "path": "source.md"}))
	if !resp.Success {
		t.Fatalf("links failed: %s", resp.Error)
	}
	result := resp.Result.(map[string]any)

	outgoing := result["outgoing"].([]string)
	if len(outgoing) != 2 {
		t.Fatalf("expected 2 outgoing links, got %d: %v", len(outgoing), outgoing)
	}

	backlinks := result["backlinks"].([]string)
	if len(backlinks) != 1 {
		t.Fatalf("expected 1 backlink, got %d: %v", len(backlinks), backlinks)
	}
}

func TestVaultAccessDenied(t *testing.T) {
	vaultDir := t.TempDir()
	tool := New(
		WithVaultAccess(func(agentID string) ([]string, error) { return []string{"allowed"}, nil }),
		WithVaultPath(func(ctx context.Context, name string) (string, error) { return vaultDir, nil }),
	)

	resp, err := tool.Execute(context.Background(), makeReq("read", map[string]any{
		"vault": "forbidden",
		"path":  "secret.md",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if resp.Success {
		t.Fatal("expected access denied")
	}
	if resp.Error == "" {
		t.Fatal("expected error message")
	}
}

func TestUnknownAction(t *testing.T) {
	tool := newTestTool(t.TempDir())
	resp, err := tool.Execute(context.Background(), makeReq("bogus", map[string]any{"vault": "test"}))
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if resp.Success {
		t.Fatal("expected failure for unknown action")
	}
}
