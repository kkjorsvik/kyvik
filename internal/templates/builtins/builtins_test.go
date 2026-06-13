package builtins

import (
	"context"
	"testing"

	"github.com/kkjorsvik/kyvik/internal/templates"
	"github.com/kkjorsvik/kyvik/internal/testutil"
)

func TestLoadDefinitions(t *testing.T) {
	defs, err := LoadDefinitions()
	if err != nil {
		t.Fatalf("LoadDefinitions() error: %v", err)
	}
	if len(defs) != 8 {
		t.Fatalf("expected 8 definitions, got %d", len(defs))
	}

	for _, def := range defs {
		t.Run(def.Name, func(t *testing.T) {
			if def.Name == "" {
				t.Error("name is empty")
			}
			if def.Description == "" {
				t.Error("description is empty")
			}
			if def.Category != "ready" && def.Category != "setup_required" {
				t.Errorf("invalid category: %q", def.Category)
			}
			if len(def.Config.ToolGrants) == 0 {
				t.Error("no tool_grants defined")
			}
			if def.Config.Metadata["builtin"] != "true" {
				t.Error("metadata.builtin != true")
			}
			tier := def.Config.Metadata["setup_tier"]
			if tier != "ready" && tier != "setup_required" {
				t.Errorf("invalid setup_tier: %q", tier)
			}
			if tier != def.Category {
				t.Errorf("setup_tier %q != category %q", tier, def.Category)
			}
			if def.Config.SoulContent == "" {
				t.Error("soul_content is empty")
			}
			if def.Config.IdentityContent == "" {
				t.Error("identity_content is empty")
			}
			if def.Config.Template == "" {
				t.Error("template (permission tier) is empty")
			}
		})
	}
}

func TestEnsureBuiltinTemplates(t *testing.T) {
	tdb := testutil.RequirePostgres(t)
	store := tdb.Store
	svc := templates.New(store)
	ctx := context.Background()

	// First call should seed templates.
	count, err := EnsureBuiltinTemplates(ctx, svc, store)
	if err != nil {
		t.Fatalf("first call error: %v", err)
	}
	if count != 8 {
		t.Fatalf("expected 8 seeded, got %d", count)
	}

	// Verify templates exist.
	all, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("list error: %v", err)
	}
	if len(all) != 8 {
		t.Fatalf("expected 8 templates in store, got %d", len(all))
	}

	// All should have empty createdBy (NULL in DB, built-in templates).
	for _, tmpl := range all {
		if tmpl.CreatedBy != "" {
			t.Errorf("template %q has createdBy=%q, want empty", tmpl.Name, tmpl.CreatedBy)
		}
	}

	// Second call should be idempotent.
	count2, err := EnsureBuiltinTemplates(ctx, svc, store)
	if err != nil {
		t.Fatalf("second call error: %v", err)
	}
	if count2 != 0 {
		t.Fatalf("expected 0 on second call, got %d", count2)
	}
}

func TestEnsureBuiltinTemplatesIdempotent(t *testing.T) {
	tdb := testutil.RequirePostgres(t)
	store := tdb.Store
	svc := templates.New(store)
	ctx := context.Background()

	// First call seeds.
	count1, err := EnsureBuiltinTemplates(ctx, svc, store)
	if err != nil {
		t.Fatalf("first call error: %v", err)
	}
	if count1 != 8 {
		t.Fatalf("expected 8 seeded on first call, got %d", count1)
	}

	// Second and third calls return 0.
	for i := 0; i < 3; i++ {
		count, err := EnsureBuiltinTemplates(ctx, svc, store)
		if err != nil {
			t.Fatalf("call %d error: %v", i+2, err)
		}
		if count != 0 {
			t.Fatalf("call %d: expected 0, got %d", i+2, count)
		}
	}

	// Still exactly 8 templates.
	all, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("list error: %v", err)
	}
	if len(all) != 8 {
		t.Fatalf("expected 8 templates, got %d", len(all))
	}
}
