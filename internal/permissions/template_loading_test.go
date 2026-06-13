package permissions

import (
	"os"
	"path/filepath"
	"testing"
)

// --- M2: Permission template loading resilience ---
// These tests verify that loadTemplatesFromDir handles errors gracefully
// and (after security audit fix) logs warnings instead of silently skipping.

func TestLoadTemplatesFromDir_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte("name: [broken\n  bad: yaml:"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := loadTemplatesFromDir(dir)
	if len(result) != 0 {
		t.Errorf("expected 0 templates from invalid YAML, got %d", len(result))
	}
}

func TestLoadTemplatesFromDir_MissingName(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "noname.yaml"), []byte("description: No name field\ncapabilities: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := loadTemplatesFromDir(dir)
	if len(result) != 0 {
		t.Errorf("expected 0 templates from nameless YAML, got %d", len(result))
	}
}

func TestLoadTemplatesFromDir_ValidTemplate(t *testing.T) {
	dir := t.TempDir()
	yaml := `name: custom
description: Custom template
capabilities:
  - tool: http
    action: get
    resource: "*"
`
	if err := os.WriteFile(filepath.Join(dir, "custom.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	result := loadTemplatesFromDir(dir)
	if len(result) != 1 {
		t.Fatalf("expected 1 template, got %d", len(result))
	}
	tmpl, ok := result["custom"]
	if !ok {
		t.Fatal("expected template with name 'custom'")
	}
	if len(tmpl.Capabilities) != 1 {
		t.Errorf("expected 1 capability, got %d", len(tmpl.Capabilities))
	}
}

func TestLoadTemplatesFromDir_NonexistentDir(t *testing.T) {
	result := loadTemplatesFromDir("/nonexistent/path/that/does/not/exist")
	if len(result) != 0 {
		t.Errorf("expected 0 templates for nonexistent dir, got %d", len(result))
	}
}

func TestLoadTemplatesFromDir_SkipsNonYAML(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("not a template"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := loadTemplatesFromDir(dir)
	if len(result) != 0 {
		t.Errorf("expected 0 templates from non-YAML file, got %d", len(result))
	}
}
