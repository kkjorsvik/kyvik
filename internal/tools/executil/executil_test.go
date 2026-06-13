package executil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSafePath_ValidRelative(t *testing.T) {
	workspace := t.TempDir()
	// Create a file inside the workspace.
	if err := os.WriteFile(filepath.Join(workspace, "hello.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := SafePath(workspace, "hello.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(workspace, "hello.txt")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSafePath_NestedRelative(t *testing.T) {
	workspace := t.TempDir()
	nested := filepath.Join(workspace, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "c.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := SafePath(workspace, "a/b/c.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(workspace, "a", "b", "c.txt")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSafePath_EmptyPath(t *testing.T) {
	workspace := t.TempDir()
	_, err := SafePath(workspace, "")
	if err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestSafePath_AbsolutePath(t *testing.T) {
	workspace := t.TempDir()
	_, err := SafePath(workspace, "/etc/passwd")
	if err == nil {
		t.Fatal("expected error for absolute path")
	}
}

func TestSafePath_DotDotTraversal(t *testing.T) {
	workspace := t.TempDir()

	cases := []string{
		"..",
		"../etc/passwd",
		"foo/../../..",
	}

	for _, tc := range cases {
		_, err := SafePath(workspace, tc)
		if err == nil {
			t.Errorf("expected error for path %q", tc)
		}
	}
}

func TestSafePath_NonExistentPath(t *testing.T) {
	workspace := t.TempDir()

	// Non-existent file within workspace should succeed (write/mkdir case).
	got, err := SafePath(workspace, "newdir/newfile.txt")
	if err != nil {
		t.Fatalf("unexpected error for non-existent path: %v", err)
	}
	want := filepath.Join(workspace, "newdir", "newfile.txt")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSafePath_SymlinkEscape(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()

	// Create a symlink inside workspace pointing outside.
	link := filepath.Join(workspace, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	_, err := SafePath(workspace, "escape/secret.txt")
	if err == nil {
		t.Fatal("expected error for symlink escape")
	}
}

func TestSafePath_ValidSymlink(t *testing.T) {
	workspace := t.TempDir()

	// Create a subdirectory and a symlink within workspace pointing to it.
	target := filepath.Join(workspace, "real")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "data.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}

	link := filepath.Join(workspace, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	got, err := SafePath(workspace, "link/data.txt")
	if err != nil {
		t.Fatalf("unexpected error for valid symlink: %v", err)
	}
	want := filepath.Join(workspace, "link", "data.txt")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
