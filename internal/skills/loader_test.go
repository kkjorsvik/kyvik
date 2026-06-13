package skills

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

// setupSkillDir creates a skill directory with optional components.
func setupSkillDir(t *testing.T, base, name string, opts ...func(string)) string {
	t.Helper()
	dir := filepath.Join(base, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, opt := range opts {
		opt(dir)
	}
	return dir
}

func withManifest(yaml string) func(string) {
	return func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, "skill.yaml"), []byte(yaml), 0o644); err != nil {
			panic(err)
		}
	}
}

func withDoc(content string) func(string) {
	return func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
			panic(err)
		}
	}
}

func withPrompt(name, content string) func(string) {
	return func(dir string) {
		promptsDir := filepath.Join(dir, "prompts")
		os.MkdirAll(promptsDir, 0o755)
		if err := os.WriteFile(filepath.Join(promptsDir, name), []byte(content), 0o644); err != nil {
			panic(err)
		}
	}
}

func withToolsDir() func(string) {
	return func(dir string) {
		os.MkdirAll(filepath.Join(dir, "tools"), 0o755)
	}
}

const validManifest = `name: test-skill
version: "1.0.0"
description: A test skill
author: tester
`

const fullManifest = `name: web-search
version: "2.0.0"
description: Search the web
author: kyvik-team
required_tools:
  - http
required_capabilities:
  - tool: http
    action: get
    resource: "*"
`

func TestLoadSkill_Full(t *testing.T) {
	base := t.TempDir()
	loader, err := NewLoader(base)
	if err != nil {
		t.Fatal(err)
	}

	setupSkillDir(t, filepath.Join(base, "local"), "web-search",
		withManifest(fullManifest),
		withDoc("# Web Search\nSearch the web."),
		withPrompt("instructions.md", "You are a web search assistant."),
		withToolsDir(),
	)

	sk, err := loader.LoadSkill(filepath.Join(base, "local", "web-search"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if sk.Name != "web-search" {
		t.Errorf("name = %q, want %q", sk.Name, "web-search")
	}
	if sk.Version != "2.0.0" {
		t.Errorf("version = %q, want %q", sk.Version, "2.0.0")
	}
	if sk.Trust != types.TrustLocal {
		t.Errorf("trust = %q, want %q", sk.Trust, types.TrustLocal)
	}
	if !sk.HasDocs {
		t.Error("expected HasDocs to be true")
	}
	if sk.DocContent != "# Web Search\nSearch the web." {
		t.Errorf("DocContent = %q", sk.DocContent)
	}
	if !sk.HasPrompts {
		t.Error("expected HasPrompts to be true")
	}
	if sk.PromptContent != "You are a web search assistant." {
		t.Errorf("PromptContent = %q", sk.PromptContent)
	}
	if !sk.HasTools {
		t.Error("expected HasTools to be true")
	}
	if len(sk.Manifest.RequiredTools) != 1 {
		t.Errorf("required_tools length = %d, want 1", len(sk.Manifest.RequiredTools))
	}
}

func TestLoadSkill_NoPrompts(t *testing.T) {
	base := t.TempDir()
	loader, err := NewLoader(base)
	if err != nil {
		t.Fatal(err)
	}

	setupSkillDir(t, filepath.Join(base, "local"), "no-prompts",
		withManifest(validManifest),
		withDoc("# Docs"),
	)

	sk, err := loader.LoadSkill(filepath.Join(base, "local", "no-prompts"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if sk.HasPrompts {
		t.Error("expected HasPrompts to be false")
	}
	if sk.PromptContent != "" {
		t.Errorf("PromptContent = %q, want empty", sk.PromptContent)
	}
	if !sk.HasDocs {
		t.Error("expected HasDocs to be true")
	}
}

func TestLoadSkill_NoDocs(t *testing.T) {
	base := t.TempDir()
	loader, err := NewLoader(base)
	if err != nil {
		t.Fatal(err)
	}

	setupSkillDir(t, filepath.Join(base, "local"), "no-docs",
		withManifest(validManifest),
	)

	sk, err := loader.LoadSkill(filepath.Join(base, "local", "no-docs"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if sk.HasDocs {
		t.Error("expected HasDocs to be false")
	}
	if sk.DocContent != "" {
		t.Errorf("DocContent = %q, want empty", sk.DocContent)
	}
}

func TestLoadSkill_MissingManifest(t *testing.T) {
	base := t.TempDir()
	loader, err := NewLoader(base)
	if err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(base, "local", "empty")
	os.MkdirAll(dir, 0o755)

	_, err = loader.LoadSkill(dir)
	if err == nil {
		t.Fatal("expected error for missing skill.yaml")
	}
}

func TestLoadSkill_InvalidManifest(t *testing.T) {
	base := t.TempDir()
	loader, err := NewLoader(base)
	if err != nil {
		t.Fatal(err)
	}

	setupSkillDir(t, filepath.Join(base, "local"), "invalid",
		withManifest("name: [broken\n  bad: yaml:"),
	)

	_, err = loader.LoadSkill(filepath.Join(base, "local", "invalid"))
	if err == nil {
		t.Fatal("expected error for invalid skill.yaml")
	}
}

func TestLoadSkill_TrustTierAssignment(t *testing.T) {
	base := t.TempDir()
	loader, err := NewLoader(base)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		tier string
		want types.TrustTier
	}{
		{"built-in", types.TrustBuiltIn},
		{"community", types.TrustCommunity},
		{"local", types.TrustLocal},
	}

	for _, tt := range tests {
		setupSkillDir(t, filepath.Join(base, tt.tier), "skill-"+tt.tier,
			withManifest(`name: skill-`+tt.tier+`
version: "1.0.0"
description: Trust test skill
`),
		)

		sk, err := loader.LoadSkill(filepath.Join(base, tt.tier, "skill-"+tt.tier))
		if err != nil {
			t.Fatalf("error loading %s: %v", tt.tier, err)
		}
		if sk.Trust != tt.want {
			t.Errorf("tier %s: trust = %q, want %q", tt.tier, sk.Trust, tt.want)
		}
	}

	// Bare directory directly under base → TrustLocal.
	setupSkillDir(t, base, "bare-skill",
		withManifest(`name: bare-skill
version: "1.0.0"
description: Bare skill
`),
	)
	sk, err := loader.LoadSkill(filepath.Join(base, "bare-skill"))
	if err != nil {
		t.Fatalf("error loading bare skill: %v", err)
	}
	if sk.Trust != types.TrustLocal {
		t.Errorf("bare: trust = %q, want %q", sk.Trust, types.TrustLocal)
	}
}

func TestLoadAll_MultipleMixedSkills(t *testing.T) {
	base := t.TempDir()
	loader, err := NewLoader(base)
	if err != nil {
		t.Fatal(err)
	}

	// Valid skill in built-in.
	setupSkillDir(t, filepath.Join(base, "built-in"), "core-skill",
		withManifest(`name: core-skill
version: "1.0.0"
description: Core builtin skill
`),
	)

	// Valid skill in local.
	setupSkillDir(t, filepath.Join(base, "local"), "my-skill",
		withManifest(validManifest),
	)

	// Invalid skill — should be skipped.
	setupSkillDir(t, filepath.Join(base, "local"), "broken",
		withManifest("name: [broken"),
	)

	skills, err := loader.LoadAll()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(skills) != 2 {
		t.Fatalf("loaded %d skills, want 2", len(skills))
	}

	// Verify both valid skills are present.
	names := map[string]bool{}
	for _, s := range skills {
		names[s.Name] = true
	}
	if !names["core-skill"] {
		t.Error("missing core-skill")
	}
	if !names["test-skill"] {
		t.Error("missing test-skill")
	}
}

func TestNewLoader_CreatesDirectories(t *testing.T) {
	base := filepath.Join(t.TempDir(), "skills")
	_, err := NewLoader(base)
	if err != nil {
		t.Fatal(err)
	}

	for _, sub := range []string{"built-in", "community", "local"} {
		info, err := os.Stat(filepath.Join(base, sub))
		if err != nil {
			t.Errorf("directory %s not created: %v", sub, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%s is not a directory", sub)
		}
	}
}

func TestLoadSkill_PromptConcatenation(t *testing.T) {
	base := t.TempDir()
	loader, err := NewLoader(base)
	if err != nil {
		t.Fatal(err)
	}

	setupSkillDir(t, filepath.Join(base, "local"), "multi-prompt",
		withManifest(`name: multi-prompt
version: "1.0.0"
description: Multi-prompt skill
`),
		withPrompt("01-setup.md", "Setup instructions."),
		withPrompt("02-behavior.md", "Behavior rules."),
	)

	sk, err := loader.LoadSkill(filepath.Join(base, "local", "multi-prompt"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !sk.HasPrompts {
		t.Error("expected HasPrompts to be true")
	}

	expected := "Setup instructions.\n\nBehavior rules."
	if sk.PromptContent != expected {
		t.Errorf("PromptContent = %q, want %q", sk.PromptContent, expected)
	}
}

func TestTrustWarning(t *testing.T) {
	if w := TrustWarning(types.TrustBuiltIn); w != "" {
		t.Errorf("built-in warning = %q, want empty", w)
	}
	if w := TrustWarning(types.TrustVerified); w != "" {
		t.Errorf("verified warning = %q, want empty", w)
	}
	if w := TrustWarning(types.TrustCommunity); w == "" {
		t.Error("community warning should not be empty")
	}
	if w := TrustWarning(types.TrustLocal); w == "" {
		t.Error("local warning should not be empty")
	}
}

func TestRequiresApproval(t *testing.T) {
	if RequiresApproval(types.TrustBuiltIn) {
		t.Error("built-in should not require approval")
	}
	if RequiresApproval(types.TrustVerified) {
		t.Error("verified should not require approval")
	}
	if !RequiresApproval(types.TrustCommunity) {
		t.Error("community should require approval")
	}
	if RequiresApproval(types.TrustLocal) {
		t.Error("local should not require approval")
	}
}
