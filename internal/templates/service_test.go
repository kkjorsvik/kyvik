package templates_test

import (
	"context"
	"testing"

	"github.com/kkjorsvik/kyvik/internal/templates"
	"github.com/kkjorsvik/kyvik/internal/testutil"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

func setupService(t *testing.T) *templates.Service {
	t.Helper()
	tdb := testutil.RequirePostgres(t)
	return templates.New(tdb.Store)
}

func TestCreateAndGet(t *testing.T) {
	svc := setupService(t)
	ctx := context.Background()

	cfg := types.AgentConfig{
		Name:        "Test Agent",
		Description: "A test agent",
		SystemPrompt: "You are helpful.",
		ModelConfig: types.ModelConfig{Provider: "openrouter", Model: "deepseek/deepseek-chat"},
		Template:    "worker",
	}

	tmpl, err := svc.Create(ctx, "My Template", "Template description", "", "", cfg, nil, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if tmpl.ID == "" {
		t.Fatal("expected non-empty ID")
	}
	if tmpl.Name != "My Template" {
		t.Errorf("name = %q, want %q", tmpl.Name, "My Template")
	}

	got, err := svc.Get(ctx, tmpl.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "My Template" {
		t.Errorf("got name = %q", got.Name)
	}
}

func TestList(t *testing.T) {
	svc := setupService(t)
	ctx := context.Background()

	cfg := types.AgentConfig{Name: "A", ModelConfig: types.ModelConfig{Provider: "p", Model: "m"}}
	_, _ = svc.Create(ctx, "Template A", "", "", "", cfg, nil, nil)
	_, _ = svc.Create(ctx, "Template B", "", "", "", cfg, nil, nil)

	list, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("len = %d, want 2", len(list))
	}
}

func TestConfigFromTemplate(t *testing.T) {
	svc := setupService(t)
	ctx := context.Background()

	cfg := types.AgentConfig{
		Name:         "Blueprint",
		SystemPrompt: "System prompt here",
		ModelConfig:  types.ModelConfig{Provider: "openrouter", Model: "deepseek/deepseek-chat"},
		Template:     "worker",
		HistoryLimit: 25,
	}

	tmpl, _ := svc.Create(ctx, "Config Test", "", "", "", cfg, nil, nil)

	restored, err := svc.ConfigFromTemplate(ctx, tmpl.ID)
	if err != nil {
		t.Fatalf("config from template: %v", err)
	}
	if restored.SystemPrompt != "System prompt here" {
		t.Errorf("system_prompt = %q", restored.SystemPrompt)
	}
	if restored.HistoryLimit != 25 {
		t.Errorf("history_limit = %d, want 25", restored.HistoryLimit)
	}
	// Runtime fields should be zeroed
	if restored.ID != "" {
		t.Errorf("ID should be empty, got %q", restored.ID)
	}
}

func TestValidateOverrides_LockedField(t *testing.T) {
	tmpl := &types.AgentTemplate{
		LockedFields:      []string{"system_prompt", "template"},
		ConstrainedFields: map[string]types.ConstraintRule{},
	}

	err := templates.ValidateOverrides(tmpl, map[string]any{
		"name": "New Name",
	})
	if err != nil {
		t.Fatalf("should allow unlocked: %v", err)
	}

	err = templates.ValidateOverrides(tmpl, map[string]any{
		"system_prompt": "hacked",
	})
	if err == nil {
		t.Fatal("should reject locked field override")
	}
}

func TestValidateOverrides_ConstrainedBounds(t *testing.T) {
	min := float64(5)
	max := float64(100)
	tmpl := &types.AgentTemplate{
		LockedFields: []string{},
		ConstrainedFields: map[string]types.ConstraintRule{
			"history_limit": {Min: &min, Max: &max},
			"template":      {Options: []string{"reader", "worker"}},
		},
	}

	// Within bounds
	err := templates.ValidateOverrides(tmpl, map[string]any{
		"history_limit": float64(50),
	})
	if err != nil {
		t.Fatalf("should allow within bounds: %v", err)
	}

	// Below min
	err = templates.ValidateOverrides(tmpl, map[string]any{
		"history_limit": float64(2),
	})
	if err == nil {
		t.Fatal("should reject below min")
	}

	// Above max
	err = templates.ValidateOverrides(tmpl, map[string]any{
		"history_limit": float64(200),
	})
	if err == nil {
		t.Fatal("should reject above max")
	}

	// Valid option
	err = templates.ValidateOverrides(tmpl, map[string]any{
		"template": "reader",
	})
	if err != nil {
		t.Fatalf("should allow valid option: %v", err)
	}

	// Invalid option
	err = templates.ValidateOverrides(tmpl, map[string]any{
		"template": "admin",
	})
	if err == nil {
		t.Fatal("should reject invalid option")
	}
}

func TestSaveFromAgent(t *testing.T) {
	svc := setupService(t)
	ctx := context.Background()

	cfg := types.AgentConfig{
		ID:           "agent-123",
		Name:         "My Agent",
		SystemPrompt: "You are great",
		ModelConfig:  types.ModelConfig{Provider: "openrouter", Model: "gpt-4"},
		Template:     "worker",
		HistoryLimit: 50,
	}

	tmpl, err := svc.SaveFromAgent(ctx, cfg, "Saved Template", "From agent", "", "")
	if err != nil {
		t.Fatalf("save from agent: %v", err)
	}

	restored, err := svc.ConfigFromTemplate(ctx, tmpl.ID)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	// ID should be stripped
	if restored.ID != "" {
		t.Errorf("ID should be empty after save-from-agent, got %q", restored.ID)
	}
	if restored.Name != "My Agent" {
		t.Errorf("name = %q", restored.Name)
	}
}

func TestDelete(t *testing.T) {
	svc := setupService(t)
	ctx := context.Background()

	cfg := types.AgentConfig{Name: "A", ModelConfig: types.ModelConfig{Provider: "p", Model: "m"}}
	tmpl, _ := svc.Create(ctx, "To Delete", "", "", "", cfg, nil, nil)

	if err := svc.Delete(ctx, tmpl.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	_, err := svc.Get(ctx, tmpl.ID)
	if err == nil {
		t.Fatal("should not find deleted template")
	}
}
