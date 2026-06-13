package identity

import (
	"testing"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

func TestBuildSystemPrompt_SoulAndIdentity(t *testing.T) {
	config := types.AgentConfig{
		SoulContent:     "You are friendly.",
		IdentityContent: "You are a researcher.",
	}
	got := BuildSystemPrompt(config)
	want := "You are friendly.\n\nYou are a researcher."
	if got != want {
		t.Errorf("BuildSystemPrompt() = %q, want %q", got, want)
	}
}

func TestBuildSystemPrompt_SoulOnly(t *testing.T) {
	config := types.AgentConfig{
		SoulContent: "You are friendly.",
	}
	got := BuildSystemPrompt(config)
	if got != "You are friendly." {
		t.Errorf("BuildSystemPrompt() = %q, want %q", got, "You are friendly.")
	}
}

func TestBuildSystemPrompt_IdentityOnly(t *testing.T) {
	config := types.AgentConfig{
		IdentityContent: "You are a researcher.",
	}
	got := BuildSystemPrompt(config)
	if got != "You are a researcher." {
		t.Errorf("BuildSystemPrompt() = %q, want %q", got, "You are a researcher.")
	}
}

func TestBuildSystemPrompt_FallbackToSystemPrompt(t *testing.T) {
	config := types.AgentConfig{
		SystemPrompt: "Legacy prompt.",
	}
	got := BuildSystemPrompt(config)
	if got != "Legacy prompt." {
		t.Errorf("BuildSystemPrompt() = %q, want %q", got, "Legacy prompt.")
	}
}

func TestBuildSystemPrompt_SoulOverridesSystemPrompt(t *testing.T) {
	config := types.AgentConfig{
		SystemPrompt: "Legacy prompt.",
		SoulContent:  "Soul takes priority.",
	}
	got := BuildSystemPrompt(config)
	if got != "Soul takes priority." {
		t.Errorf("BuildSystemPrompt() = %q, want %q", got, "Soul takes priority.")
	}
}

func TestBuildSystemPrompt_Default(t *testing.T) {
	config := types.AgentConfig{}
	got := BuildSystemPrompt(config)
	if got != DefaultSystemPrompt {
		t.Errorf("BuildSystemPrompt() = %q, want %q", got, DefaultSystemPrompt)
	}
}

func TestBuildSystemPrompt_TrimsWhitespace(t *testing.T) {
	config := types.AgentConfig{
		SoulContent:     "  Soul content  \n",
		IdentityContent: "\n  Identity content  ",
	}
	got := BuildSystemPrompt(config)
	want := "Soul content\n\nIdentity content"
	if got != want {
		t.Errorf("BuildSystemPrompt() = %q, want %q", got, want)
	}
}

func TestGetSoulPreset(t *testing.T) {
	p := GetSoulPreset("friendly-helper")
	if p == nil {
		t.Fatal("expected to find friendly-helper preset")
	}
	if p.Name != "Friendly Helper" {
		t.Errorf("Name = %q, want %q", p.Name, "Friendly Helper")
	}
	if p.Content == "" {
		t.Error("expected non-empty Content")
	}
}

func TestGetSoulPreset_NotFound(t *testing.T) {
	p := GetSoulPreset("nonexistent")
	if p != nil {
		t.Errorf("expected nil for nonexistent preset, got %+v", p)
	}
}

func TestGetRoleTemplate(t *testing.T) {
	r := GetRoleTemplate("researcher")
	if r == nil {
		t.Fatal("expected to find researcher template")
	}
	if r.Name != "Researcher" {
		t.Errorf("Name = %q, want %q", r.Name, "Researcher")
	}
	if r.Content == "" {
		t.Error("expected non-empty Content")
	}
}

func TestGetRoleTemplate_NotFound(t *testing.T) {
	r := GetRoleTemplate("nonexistent")
	if r != nil {
		t.Errorf("expected nil for nonexistent template, got %+v", r)
	}
}

func TestGetSoulPresets_Count(t *testing.T) {
	presets := GetSoulPresets()
	if len(presets) != 5 {
		t.Errorf("expected 5 soul presets, got %d", len(presets))
	}
}

func TestGetRoleTemplates_Count(t *testing.T) {
	templates := GetRoleTemplates()
	if len(templates) != 5 {
		t.Errorf("expected 5 role templates, got %d", len(templates))
	}
}

func TestBuildSystemPrompt_IdentityOverridesSystemPrompt(t *testing.T) {
	config := types.AgentConfig{
		SystemPrompt:    "Legacy prompt.",
		IdentityContent: "Identity takes priority.",
	}
	got := BuildSystemPrompt(config)
	if got != "Identity takes priority." {
		t.Errorf("BuildSystemPrompt() = %q, want %q", got, "Identity takes priority.")
	}
}

func TestBuildSystemPrompt_AllThreeSet(t *testing.T) {
	config := types.AgentConfig{
		SystemPrompt:    "Legacy prompt.",
		SoulContent:     "Soul content.",
		IdentityContent: "Identity content.",
	}
	got := BuildSystemPrompt(config)
	want := "Soul content.\n\nIdentity content."
	if got != want {
		t.Errorf("BuildSystemPrompt() = %q, want %q", got, want)
	}
}

func TestSoulPresetIDsUnique(t *testing.T) {
	presets := GetSoulPresets()
	seen := make(map[string]bool)
	for _, p := range presets {
		if seen[p.ID] {
			t.Errorf("duplicate soul preset ID: %q", p.ID)
		}
		seen[p.ID] = true
	}
}

func TestRoleTemplateIDsUnique(t *testing.T) {
	templates := GetRoleTemplates()
	seen := make(map[string]bool)
	for _, tmpl := range templates {
		if seen[tmpl.ID] {
			t.Errorf("duplicate role template ID: %q", tmpl.ID)
		}
		seen[tmpl.ID] = true
	}
}

func TestAllSoulPresetsHaveContent(t *testing.T) {
	for _, p := range GetSoulPresets() {
		if p.Content == "" {
			t.Errorf("soul preset %q has empty Content", p.ID)
		}
		if p.Name == "" {
			t.Errorf("soul preset %q has empty Name", p.ID)
		}
	}
}

func TestAllRoleTemplatesHaveContent(t *testing.T) {
	for _, tmpl := range GetRoleTemplates() {
		if tmpl.Content == "" {
			t.Errorf("role template %q has empty Content", tmpl.ID)
		}
		if tmpl.Name == "" {
			t.Errorf("role template %q has empty Name", tmpl.ID)
		}
	}
}
