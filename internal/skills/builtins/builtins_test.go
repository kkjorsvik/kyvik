package builtins

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kkjorsvik/kyvik/internal/skills"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

func TestInstall_CreatesDirectories(t *testing.T) {
	base := t.TempDir()
	n, err := Install(base)
	if err != nil {
		t.Fatalf("Install failed: %v", err)
	}
	if n != 11 {
		t.Errorf("installed = %d, want 11", n)
	}

	for _, name := range Skills() {
		yamlPath := filepath.Join(base, "built-in", name, "skill.yaml")
		if _, err := os.Stat(yamlPath); err != nil {
			t.Errorf("skill.yaml missing for %s: %v", name, err)
		}
	}
}

func TestInstall_PromptFilesExist(t *testing.T) {
	base := t.TempDir()
	if _, err := Install(base); err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	prompts := map[string]string{
		"file-manager":    "prompts/01-instructions.md",
		"system-docs":     "prompts/instructions.md",
		"research-analyst": "prompts/01-methodology.md",
		"data-summarizer": "prompts/01-summarization.md",
		"project-tracker": "prompts/01-task-management.md",
		"email-assistant": "prompts/01-composition.md",
		"report-builder":  "prompts/01-report-structure.md",
		"code-reviewer":   "prompts/01-review-process.md",
		"web-researcher":  "prompts/01-research-methodology.md",
		"devops-runbook":  "prompts/01-operations-protocol.md",
		"system-auditor":  "prompts/01-audit-methodology.md",
	}

	for skill, promptFile := range prompts {
		path := filepath.Join(base, "built-in", skill, promptFile)
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("prompt file missing for %s: %v", skill, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("prompt file %s for %s is empty", promptFile, skill)
		}
	}
}

func TestInstall_SkillMDExists(t *testing.T) {
	base := t.TempDir()
	if _, err := Install(base); err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	for _, name := range Skills() {
		path := filepath.Join(base, "built-in", name, "SKILL.md")
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("SKILL.md missing for %s: %v", name, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("SKILL.md for %s is empty", name)
		}
	}
}

func TestInstall_Idempotent(t *testing.T) {
	base := t.TempDir()

	n1, err := Install(base)
	if err != nil {
		t.Fatalf("first Install failed: %v", err)
	}
	if n1 != 11 {
		t.Errorf("first install = %d, want 11", n1)
	}

	n2, err := Install(base)
	if err != nil {
		t.Fatalf("second Install failed: %v", err)
	}
	if n2 != 11 {
		t.Errorf("second install = %d, want 11", n2)
	}
}

func TestInstall_ManifestsValid(t *testing.T) {
	base := t.TempDir()
	if _, err := Install(base); err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	for _, name := range Skills() {
		path := filepath.Join(base, "built-in", name, "skill.yaml")
		m, err := skills.ParseManifest(path)
		if err != nil {
			t.Errorf("ParseManifest failed for %s: %v", name, err)
			continue
		}
		if m.Name != name {
			t.Errorf("manifest name = %q, want %q", m.Name, name)
		}
	}
}

func TestInstall_LoaderIntegration(t *testing.T) {
	base := t.TempDir()
	if _, err := Install(base); err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	loader, err := skills.NewLoader(base)
	if err != nil {
		t.Fatalf("NewLoader failed: %v", err)
	}

	loaded, err := loader.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll failed: %v", err)
	}

	if len(loaded) != 11 {
		t.Fatalf("loaded %d skills, want 11", len(loaded))
	}

	for _, sk := range loaded {
		if sk.Trust != types.TrustBuiltIn {
			t.Errorf("skill %s trust = %q, want %q", sk.Name, sk.Trust, types.TrustBuiltIn)
		}
	}
}

func TestInstall_FileManagerHasRequirements(t *testing.T) {
	base := t.TempDir()
	if _, err := Install(base); err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	path := filepath.Join(base, "built-in", "file-manager", "skill.yaml")
	m, err := skills.ParseManifest(path)
	if err != nil {
		t.Fatalf("ParseManifest failed: %v", err)
	}

	if len(m.RequiredCapabilities) != 2 {
		t.Errorf("required_capabilities = %d, want 2", len(m.RequiredCapabilities))
	}
	if len(m.RequiredTools) != 1 {
		t.Errorf("required_tools = %d, want 1", len(m.RequiredTools))
	}
	if m.RequiredTools[0] != "file" {
		t.Errorf("required_tools[0] = %q, want %q", m.RequiredTools[0], "file")
	}
}

func TestInstall_SystemDocsHasNoRequirements(t *testing.T) {
	base := t.TempDir()
	if _, err := Install(base); err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	path := filepath.Join(base, "built-in", "system-docs", "skill.yaml")
	m, err := skills.ParseManifest(path)
	if err != nil {
		t.Fatalf("ParseManifest failed: %v", err)
	}

	if len(m.RequiredCapabilities) != 0 {
		t.Errorf("required_capabilities = %d, want 0", len(m.RequiredCapabilities))
	}
	if len(m.RequiredTools) != 0 {
		t.Errorf("required_tools = %d, want 0", len(m.RequiredTools))
	}
}

func TestInstall_SystemDocsHasDocFiles(t *testing.T) {
	base := t.TempDir()
	if _, err := Install(base); err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	docFiles := []string{
		"getting-started.md",
		"agents.md",
		"permissions.md",
		"models.md",
		"tools.md",
		"skills.md",
		"teams.md",
		"spending.md",
		"security.md",
		"troubleshooting.md",
		"api.md",
		"faq.md",
	}

	for _, f := range docFiles {
		path := filepath.Join(base, "built-in", "system-docs", "docs", f)
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("doc file missing: docs/%s: %v", f, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("doc file docs/%s is empty", f)
		}
	}
}

func TestInstall_NewSkillsHaveRequirements(t *testing.T) {
	base := t.TempDir()
	if _, err := Install(base); err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	tests := []struct {
		name     string
		tools    []string
		capCount int
	}{
		{"research-analyst", []string{"memory"}, 2},
		{"data-summarizer", []string{"memory", "file"}, 3},
		{"project-tracker", []string{"rest_api", "memory"}, 2},
		{"email-assistant", []string{"email", "memory"}, 2},
		{"report-builder", []string{"file", "memory"}, 4},
		{"code-reviewer", []string{"file", "shell", "memory"}, 4},
		{"web-researcher", []string{"browser", "file", "memory"}, 4},
		{"devops-runbook", []string{"shell", "file", "memory"}, 5},
		{"system-auditor", []string{"hostfs", "shell", "memory"}, 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(base, "built-in", tt.name, "skill.yaml")
			m, err := skills.ParseManifest(path)
			if err != nil {
				t.Fatalf("ParseManifest failed: %v", err)
			}

			if len(m.RequiredTools) != len(tt.tools) {
				t.Errorf("required_tools count = %d, want %d", len(m.RequiredTools), len(tt.tools))
			}
			for i, tool := range tt.tools {
				if i < len(m.RequiredTools) && m.RequiredTools[i] != tool {
					t.Errorf("required_tools[%d] = %q, want %q", i, m.RequiredTools[i], tool)
				}
			}

			if len(m.RequiredCapabilities) != tt.capCount {
				t.Errorf("required_capabilities count = %d, want %d", len(m.RequiredCapabilities), tt.capCount)
			}
		})
	}
}

func TestInstall_WebResearcherHasSandboxConfig(t *testing.T) {
	base := t.TempDir()
	if _, err := Install(base); err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	path := filepath.Join(base, "built-in", "web-researcher", "skill.yaml")
	m, err := skills.ParseManifest(path)
	if err != nil {
		t.Fatalf("ParseManifest failed: %v", err)
	}

	if m.Sandbox == nil {
		t.Fatal("web-researcher should have sandbox config")
	}
	if !m.Sandbox.AllowNetwork {
		t.Error("web-researcher sandbox.allow_network should be true")
	}
}
