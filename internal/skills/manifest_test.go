package skills

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

func writeManifest(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "skill.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseManifest_Full(t *testing.T) {
	yaml := `
name: web-search
version: "1.0.0"
description: Search the web and summarize results
author: kyvik-team
license: MIT
required_tools:
  - http
  - filesystem
required_capabilities:
  - tool: http
    action: get
    resource: "https://*.example.com/*"
prompts:
  system: "You are a web search assistant."
  task: "Search for: {{query}}"
sandbox:
  allow_network: true
  allowed_hosts:
    - "api.example.com"
  read_paths:
    - "/tmp/cache"
  write_paths:
    - "/tmp/output"
`
	m, err := ParseManifest(writeManifest(t, yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if m.Name != "web-search" {
		t.Errorf("name = %q, want %q", m.Name, "web-search")
	}
	if m.Version != "1.0.0" {
		t.Errorf("version = %q, want %q", m.Version, "1.0.0")
	}
	if m.Description != "Search the web and summarize results" {
		t.Errorf("description = %q", m.Description)
	}
	if m.Author != "kyvik-team" {
		t.Errorf("author = %q", m.Author)
	}
	if len(m.RequiredTools) != 2 {
		t.Errorf("required_tools length = %d, want 2", len(m.RequiredTools))
	}
	if len(m.RequiredCapabilities) != 1 {
		t.Errorf("required_capabilities length = %d, want 1", len(m.RequiredCapabilities))
	}
	if len(m.Prompts) != 2 {
		t.Errorf("prompts length = %d, want 2", len(m.Prompts))
	}
	if m.Sandbox == nil {
		t.Fatal("sandbox is nil")
	}
	if !m.Sandbox.AllowNetwork {
		t.Error("sandbox.allow_network should be true")
	}
	if len(m.Sandbox.AllowedHosts) != 1 {
		t.Errorf("sandbox.allowed_hosts length = %d, want 1", len(m.Sandbox.AllowedHosts))
	}
}

func TestParseManifest_Minimal(t *testing.T) {
	yaml := `
name: simple-skill
version: "0.1.0"
description: A minimal skill
`
	m, err := ParseManifest(writeManifest(t, yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Name != "simple-skill" {
		t.Errorf("name = %q, want %q", m.Name, "simple-skill")
	}
	if m.Sandbox != nil {
		t.Errorf("sandbox should be nil for minimal manifest")
	}
}

func TestValidateManifest_MissingName(t *testing.T) {
	m := &types.SkillManifest{
		Version:     "1.0.0",
		Description: "test",
	}
	err := ValidateManifest(m)
	if err == nil {
		t.Fatal("expected error for missing name")
	}
	if got := err.Error(); got != "manifest validation: name is required" {
		t.Errorf("error = %q", got)
	}
}

func TestValidateManifest_InvalidNameFormat(t *testing.T) {
	tests := []string{"My Skill", "UPPERCASE", "-starts-with-dash", "has spaces"}
	for _, name := range tests {
		m := &types.SkillManifest{
			Name:        name,
			Version:     "1.0.0",
			Description: "test",
		}
		if err := ValidateManifest(m); err == nil {
			t.Errorf("expected error for name %q", name)
		}
	}
}

func TestValidateManifest_MissingVersion(t *testing.T) {
	m := &types.SkillManifest{
		Name:        "test-skill",
		Description: "test",
	}
	err := ValidateManifest(m)
	if err == nil {
		t.Fatal("expected error for missing version")
	}
	if got := err.Error(); got != "manifest validation: version is required" {
		t.Errorf("error = %q", got)
	}
}

func TestParseManifest_EmptyFile(t *testing.T) {
	_, err := ParseManifest(writeManifest(t, ""))
	if err == nil {
		t.Fatal("expected error for empty file")
	}
}

func TestParseManifest_MalformedYAML(t *testing.T) {
	_, err := ParseManifest(writeManifest(t, "name: [invalid\n  bad: yaml:"))
	if err == nil {
		t.Fatal("expected error for malformed YAML")
	}
}
