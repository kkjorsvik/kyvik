package ktp_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kkjorsvik/kyvik/internal/ktp"
	"github.com/kkjorsvik/kyvik/internal/ktp/testtools"
)

// mockTool returns a minimal valid ktp.Tool with the given name and minTier.
type mockTool struct {
	name    string
	minTier string
}

func (m *mockTool) Declaration() ktp.ToolDeclaration {
	return ktp.ToolDeclaration{
		Name:    m.name,
		Version: "1.0.0",
		MinTier: m.minTier,
		Actions: []ktp.ActionSpec{{
			Name:        "run",
			Description: "Run the tool",
			Parameters:  ktp.JSONSchema{Type: "object"},
			Returns:     ktp.JSONSchema{Type: "object"},
		}},
	}
}

func (m *mockTool) Execute(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	resp := ktp.NewToolResponse(req.ID, true, nil, "", 0)
	return &resp, nil
}

// invalidTool returns a tool with an empty name to trigger validation failure.
type invalidTool struct{}

func (i *invalidTool) Declaration() ktp.ToolDeclaration {
	return ktp.ToolDeclaration{
		Name:    "",
		Version: "1.0.0",
		Actions: []ktp.ActionSpec{{
			Name:       "x",
			Parameters: ktp.JSONSchema{Type: "object"},
		}},
	}
}

func (i *invalidTool) Execute(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	return nil, nil
}

// multiActionTool is a mock with two actions for testing multi-action definitions.
type multiActionTool struct{}

func (m *multiActionTool) Declaration() ktp.ToolDeclaration {
	return ktp.ToolDeclaration{
		Name:    "multi",
		Version: "1.0.0",
		MinTier: ktp.TierReader,
		Actions: []ktp.ActionSpec{
			{
				Name:        "alpha",
				Description: "First action",
				Parameters:  ktp.JSONSchema{Type: "object"},
				Returns:     ktp.JSONSchema{Type: "object"},
			},
			{
				Name:        "beta",
				Description: "Second action",
				Parameters:  ktp.JSONSchema{Type: "object"},
				Returns:     ktp.JSONSchema{Type: "object"},
			},
		},
	}
}

func (m *multiActionTool) Execute(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	resp := ktp.NewToolResponse(req.ID, true, nil, "", 0)
	return &resp, nil
}

func TestRegistryRegisterValid(t *testing.T) {
	r := ktp.NewRegistry()
	if err := r.Register(&testtools.EchoTool{}); err != nil {
		t.Fatalf("Register valid tool: %v", err)
	}
}

func TestRegistryRegisterInvalid(t *testing.T) {
	r := ktp.NewRegistry()
	err := r.Register(&invalidTool{})
	if err == nil {
		t.Fatal("expected error for invalid declaration, got nil")
	}
	if !errors.Is(err, ktp.ErrInvalidDeclaration) {
		t.Fatalf("expected ErrInvalidDeclaration, got: %v", err)
	}
}

func TestRegistryRegisterDuplicate(t *testing.T) {
	r := ktp.NewRegistry()
	if err := r.Register(&testtools.EchoTool{}); err != nil {
		t.Fatalf("first register: %v", err)
	}
	err := r.Register(&testtools.EchoTool{})
	if err == nil {
		t.Fatal("expected error for duplicate registration, got nil")
	}
	if !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("expected 'already registered' in error, got: %v", err)
	}
}

func TestRegistryGetRegistered(t *testing.T) {
	r := ktp.NewRegistry()
	r.Register(&testtools.EchoTool{})

	tool, ok := r.Get("echo")
	if !ok {
		t.Fatal("expected to find 'echo' tool")
	}
	if tool.Declaration().Name != "echo" {
		t.Fatalf("expected name 'echo', got %q", tool.Declaration().Name)
	}
}

func TestRegistryGetUnregistered(t *testing.T) {
	r := ktp.NewRegistry()
	tool, ok := r.Get("nonexistent")
	if ok {
		t.Fatal("expected ok=false for unregistered tool")
	}
	if tool != nil {
		t.Fatal("expected nil tool for unregistered name")
	}
}

func TestRegistryList(t *testing.T) {
	r := ktp.NewRegistry()
	r.Register(&testtools.EchoTool{})
	r.Register(&mockTool{name: "mock-a", minTier: ktp.TierReader})

	decls := r.List()
	if len(decls) != 2 {
		t.Fatalf("expected 2 declarations, got %d", len(decls))
	}
}

func TestRegistryListForTierFiltering(t *testing.T) {
	r := ktp.NewRegistry()
	r.Register(&mockTool{name: "reader-tool", minTier: ktp.TierReader})
	r.Register(&mockTool{name: "admin-tool", minTier: ktp.TierAdmin})

	// Admin should see both.
	adminDecls := r.ListForTier(ktp.TierAdmin)
	if len(adminDecls) != 2 {
		t.Fatalf("admin: expected 2 tools, got %d", len(adminDecls))
	}

	// Reader should see only reader-tool, not admin-tool.
	readerDecls := r.ListForTier(ktp.TierReader)
	if len(readerDecls) != 1 {
		t.Fatalf("reader: expected 1 tool, got %d", len(readerDecls))
	}
	if readerDecls[0].Name != "reader-tool" {
		t.Fatalf("reader: expected 'reader-tool', got %q", readerDecls[0].Name)
	}
}

func TestRegistryListForTierEmptyMinTier(t *testing.T) {
	r := ktp.NewRegistry()
	r.Register(&mockTool{name: "open-tool", minTier: ""})

	// Even reader tier should see a tool with empty MinTier.
	decls := r.ListForTier(ktp.TierReader)
	if len(decls) != 1 {
		t.Fatalf("expected 1 tool with empty MinTier, got %d", len(decls))
	}
	if decls[0].Name != "open-tool" {
		t.Fatalf("expected 'open-tool', got %q", decls[0].Name)
	}
}

func TestRegistryListForAgentTierAndGrants(t *testing.T) {
	r := ktp.NewRegistry()
	r.Register(&mockTool{name: "tool-a", minTier: ktp.TierReader})
	r.Register(&mockTool{name: "tool-b", minTier: ktp.TierReader})
	r.Register(&mockTool{name: "tool-c", minTier: ktp.TierAdmin})

	// Writer tier with grants for tool-a only.
	decls := r.ListForAgent("agent-1", ktp.TierWriter, []string{"tool-a"})
	if len(decls) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(decls))
	}
	if decls[0].Name != "tool-a" {
		t.Fatalf("expected 'tool-a', got %q", decls[0].Name)
	}
}

func TestRegistryListForAgentEmptyGrants(t *testing.T) {
	r := ktp.NewRegistry()
	r.Register(&mockTool{name: "tool-a", minTier: ktp.TierReader})
	r.Register(&mockTool{name: "tool-b", minTier: ktp.TierWriter})

	// Writer tier, empty grants = no grant restriction.
	decls := r.ListForAgent("agent-1", ktp.TierWriter, nil)
	if len(decls) != 2 {
		t.Fatalf("expected 2 tools with empty grants, got %d", len(decls))
	}
}

func TestRegistryGetToolDefinitionsForModel(t *testing.T) {
	r := ktp.NewRegistry()
	r.Register(&testtools.EchoTool{})

	defs := r.GetToolDefinitionsForModel("agent-1", ktp.TierAdmin, nil)
	if len(defs) != 1 {
		t.Fatalf("expected 1 definition, got %d", len(defs))
	}

	def := defs[0]
	// Should use "tool__action" naming.
	if def.Name != "echo__echo" {
		t.Errorf("expected name 'echo__echo', got %q", def.Name)
	}
	if def.Description != "Echo the message back" {
		t.Errorf("expected description 'Echo the message back', got %q", def.Description)
	}
	if def.Parameters["type"] != "object" {
		t.Errorf("expected parameters type 'object', got %v", def.Parameters["type"])
	}
}

func TestRegistryGetToolDefinitionsForModel_MultiAction(t *testing.T) {
	r := ktp.NewRegistry()
	// Register a tool with multiple actions.
	r.Register(&multiActionTool{})

	defs := r.GetToolDefinitionsForModel("agent-1", ktp.TierAdmin, nil)
	if len(defs) != 2 {
		t.Fatalf("expected 2 definitions (one per action), got %d", len(defs))
	}

	names := map[string]bool{}
	for _, def := range defs {
		names[def.Name] = true
	}
	if !names["multi__alpha"] {
		t.Error("expected 'multi__alpha' in definitions")
	}
	if !names["multi__beta"] {
		t.Error("expected 'multi__beta' in definitions")
	}
}

func TestRegistryGetToolDefinitionsForModel_TierFiltering(t *testing.T) {
	r := ktp.NewRegistry()
	r.Register(&testtools.EchoTool{})                                  // MinTier: writer
	r.Register(&mockTool{name: "admin-only", minTier: ktp.TierAdmin}) // MinTier: admin

	// Reader should see nothing (echo requires writer, admin-only requires admin).
	defs := r.GetToolDefinitionsForModel("agent-1", ktp.TierReader, nil)
	if len(defs) != 0 {
		t.Errorf("reader: expected 0 definitions, got %d", len(defs))
	}

	// Writer should see only echo.
	defs = r.GetToolDefinitionsForModel("agent-1", ktp.TierWriter, nil)
	if len(defs) != 1 {
		t.Fatalf("writer: expected 1 definition, got %d", len(defs))
	}
	if defs[0].Name != "echo__echo" {
		t.Errorf("writer: expected 'echo__echo', got %q", defs[0].Name)
	}

	// Admin should see both.
	defs = r.GetToolDefinitionsForModel("agent-1", ktp.TierAdmin, nil)
	if len(defs) != 2 {
		t.Errorf("admin: expected 2 definitions, got %d", len(defs))
	}
}

func TestRegistryGetModelToolDefinitions(t *testing.T) {
	r := ktp.NewRegistry()
	r.Register(&testtools.EchoTool{})

	defs := r.GetModelToolDefinitions("agent-1", ktp.TierAdmin, nil)
	if len(defs) != 1 {
		t.Fatalf("expected 1 definition, got %d", len(defs))
	}

	def := defs[0]
	if def["type"] != "function" {
		t.Fatalf("expected type 'function', got %v", def["type"])
	}

	fn, ok := def["function"].(map[string]any)
	if !ok {
		t.Fatal("expected 'function' key to be map[string]any")
	}
	if fn["name"] != "echo" {
		t.Fatalf("expected function name 'echo', got %v", fn["name"])
	}
	if fn["description"] != "Echo the message back" {
		t.Fatalf("expected description 'Echo the message back', got %v", fn["description"])
	}

	params, ok := fn["parameters"].(map[string]any)
	if !ok {
		t.Fatal("expected 'parameters' to be map[string]any")
	}
	if params["type"] != "object" {
		t.Fatalf("expected parameters type 'object', got %v", params["type"])
	}
}

// defaultTierMockTool is a mock that includes DefaultTiers for testing.
type defaultTierMockTool struct {
	name         string
	minTier      string
	defaultTiers []string
}

func (m *defaultTierMockTool) Declaration() ktp.ToolDeclaration {
	return ktp.ToolDeclaration{
		Name:         m.name,
		Version:      "1.0.0",
		MinTier:      m.minTier,
		DefaultTiers: m.defaultTiers,
		Actions: []ktp.ActionSpec{{
			Name:       "run",
			Parameters: ktp.JSONSchema{Type: "object"},
		}},
	}
}

func (m *defaultTierMockTool) Execute(_ context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	resp := ktp.NewToolResponse(req.ID, true, nil, "", 0)
	return &resp, nil
}

func TestRegistryDefaultToolsForTier(t *testing.T) {
	r := ktp.NewRegistry()
	r.Register(&defaultTierMockTool{
		name: "file", minTier: ktp.TierReader,
		defaultTiers: []string{ktp.TierReader, ktp.TierWriter, ktp.TierOperator, ktp.TierAdmin},
	})
	r.Register(&defaultTierMockTool{
		name: "shell", minTier: ktp.TierOperator,
		defaultTiers: []string{ktp.TierAdmin},
	})
	r.Register(&defaultTierMockTool{
		name: "github", minTier: ktp.TierWriter,
		defaultTiers: nil, // opt-in only
	})

	// Reader gets file (in DefaultTiers).
	defaults := r.DefaultToolsForTier(ktp.TierReader)
	if len(defaults) != 1 || defaults[0] != "file" {
		t.Fatalf("reader: expected [file], got %v", defaults)
	}

	// Admin gets file + shell (both have admin in DefaultTiers).
	defaults = r.DefaultToolsForTier(ktp.TierAdmin)
	if len(defaults) != 2 {
		t.Fatalf("admin: expected 2 defaults, got %d: %v", len(defaults), defaults)
	}

	// Operator gets only file (shell defaults to admin, not operator).
	defaults = r.DefaultToolsForTier(ktp.TierOperator)
	if len(defaults) != 1 || defaults[0] != "file" {
		t.Fatalf("operator: expected [file], got %v", defaults)
	}

	// Writer gets file (writer is in file's DefaultTiers).
	defaults = r.DefaultToolsForTier(ktp.TierWriter)
	if len(defaults) != 1 || defaults[0] != "file" {
		t.Fatalf("writer: expected [file], got %v", defaults)
	}
}

func TestRegistryDefaultToolsForTier_Empty(t *testing.T) {
	r := ktp.NewRegistry()
	r.Register(&defaultTierMockTool{
		name: "github", minTier: ktp.TierWriter,
		defaultTiers: nil,
	})

	// No tools have reader in DefaultTiers.
	defaults := r.DefaultToolsForTier(ktp.TierReader)
	if len(defaults) != 0 {
		t.Fatalf("expected 0 defaults for reader, got %v", defaults)
	}

	// github has no DefaultTiers, so even writer gets nothing.
	defaults = r.DefaultToolsForTier(ktp.TierWriter)
	if len(defaults) != 0 {
		t.Fatalf("expected 0 defaults for writer with opt-in tool, got %v", defaults)
	}
}
