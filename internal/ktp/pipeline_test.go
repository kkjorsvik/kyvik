package ktp

import (
	"context"
	"strings"
	"testing"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

// --- Pipeline test tools ---

// readerTool has MinTier=reader.
type readerTool struct{}

func (t *readerTool) Declaration() ToolDeclaration {
	return ToolDeclaration{
		Name:    "reader-tool",
		Version: "1.0.0",
		MinTier: TierReader,
		Actions: []ActionSpec{{
			Name:        "read",
			Description: "Read something",
			Parameters:  JSONSchema{Type: "object"},
		}},
	}
}

func (t *readerTool) Execute(_ context.Context, req ToolRequest) (*ToolResponse, error) {
	resp := NewToolResponse(req.ID, true, nil, "", 0)
	return &resp, nil
}

// writerTool has MinTier=writer.
type writerTool struct{}

func (t *writerTool) Declaration() ToolDeclaration {
	return ToolDeclaration{
		Name:    "writer-tool",
		Version: "1.0.0",
		MinTier: TierWriter,
		Actions: []ActionSpec{{
			Name:        "write",
			Description: "Write something",
			Parameters:  JSONSchema{Type: "object"},
		}},
	}
}

func (t *writerTool) Execute(_ context.Context, req ToolRequest) (*ToolResponse, error) {
	resp := NewToolResponse(req.ID, true, nil, "", 0)
	return &resp, nil
}

// operatorTool has MinTier=operator.
type operatorTool struct{}

func (t *operatorTool) Declaration() ToolDeclaration {
	return ToolDeclaration{
		Name:    "operator-tool",
		Version: "1.0.0",
		MinTier: TierOperator,
		Actions: []ActionSpec{{
			Name:        "exec",
			Description: "Execute something",
			Parameters:  JSONSchema{Type: "object"},
		}},
	}
}

func (t *operatorTool) Execute(_ context.Context, req ToolRequest) (*ToolResponse, error) {
	resp := NewToolResponse(req.ID, true, nil, "", 0)
	return &resp, nil
}

// adminTool has MinTier=admin.
type adminTool struct{}

func (t *adminTool) Declaration() ToolDeclaration {
	return ToolDeclaration{
		Name:    "admin-tool",
		Version: "1.0.0",
		MinTier: TierAdmin,
		Actions: []ActionSpec{{
			Name:        "manage",
			Description: "Manage something",
			Parameters:  JSONSchema{Type: "object"},
		}},
	}
}

func (t *adminTool) Execute(_ context.Context, req ToolRequest) (*ToolResponse, error) {
	resp := NewToolResponse(req.ID, true, nil, "", 0)
	return &resp, nil
}

// --- Pipeline tests ---

func TestTierBasedToolVisibility(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&readerTool{})
	reg.Register(&writerTool{})
	reg.Register(&operatorTool{})
	reg.Register(&adminTool{})

	tests := []struct {
		tier     string
		wantMin  int // minimum expected tool actions visible
		wantMax  int // maximum expected tool actions visible
	}{
		{TierReader, 1, 1},     // only reader-tool
		{TierWriter, 2, 2},     // reader-tool + writer-tool
		{TierOperator, 3, 3},   // reader + writer + operator
		{TierAdmin, 4, 4},      // all 4 tools
	}

	for _, tt := range tests {
		defs := reg.GetToolDefinitionsForModel("test-agent", tt.tier, nil)
		if len(defs) < tt.wantMin || len(defs) > tt.wantMax {
			t.Errorf("tier %q: expected %d-%d tool actions, got %d", tt.tier, tt.wantMin, tt.wantMax, len(defs))
		}
	}
}

func TestEmptyTemplateSeesNoTools(t *testing.T) {
	// Verify that an empty template resolves to "" tier, which sees 0 tools.
	tier := ResolveAgentTier("")
	if tier != "" {
		t.Fatalf("expected empty tier for empty template, got %q", tier)
	}

	reg := NewRegistry()
	reg.Register(&readerTool{})
	reg.Register(&writerTool{})

	defs := reg.GetToolDefinitionsForModel("test-agent", tier, nil)
	if len(defs) != 0 {
		t.Errorf("expected 0 tools for empty tier, got %d", len(defs))
	}
}

func TestToolGrantsFilter(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&readerTool{})
	reg.Register(&writerTool{})
	reg.Register(&operatorTool{})

	// Writer tier with grants restricting to reader-tool only.
	defs := reg.GetToolDefinitionsForModel("test-agent", TierAdmin, []string{"reader-tool"})
	if len(defs) != 1 {
		t.Fatalf("expected 1 tool with grants=[reader-tool], got %d", len(defs))
	}
	if defs[0].Name != "reader-tool__read" {
		t.Errorf("expected 'reader-tool__read', got %q", defs[0].Name)
	}
}

func TestStaleToolGrantsYieldNoTools(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&readerTool{})
	reg.Register(&writerTool{})

	// Grants contain names that don't match any registered tool.
	defs := reg.GetToolDefinitionsForModel("test-agent", TierAdmin, []string{"nonexistent-tool", "also-gone"})
	if len(defs) != 0 {
		t.Errorf("expected 0 tools for stale grants, got %d", len(defs))
	}
}

func TestToolDefinitionsInCompletionRequest(t *testing.T) {
	// Full pipeline: register tools → resolve tier → get definitions → verify
	// they have the correct structure for a CompletionRequest.
	reg := NewRegistry()
	reg.Register(&readerTool{})
	reg.Register(&writerTool{})

	agentTier := ResolveAgentTier("worker") // worker → writer tier
	defs := reg.GetToolDefinitionsForModel("test-agent", agentTier, nil)

	if len(defs) != 2 {
		t.Fatalf("expected 2 tool definitions (reader + writer), got %d", len(defs))
	}

	// Verify each definition has the required fields for model API.
	names := make(map[string]bool)
	for _, def := range defs {
		if def.Name == "" {
			t.Error("tool definition has empty name")
		}
		if def.Parameters == nil {
			t.Errorf("tool %q has nil parameters", def.Name)
		}
		names[def.Name] = true
	}

	if !names["reader-tool__read"] {
		t.Error("expected 'reader-tool__read' in definitions")
	}
	if !names["writer-tool__write"] {
		t.Error("expected 'writer-tool__write' in definitions")
	}
}

func TestPermissionDenialAuditLog(t *testing.T) {
	// Reader agent tries to use a writer-tier tool → denied → audit logged.
	agents := map[string]*types.AgentConfig{
		"reader-1": {ID: "reader-1", Template: "reader"},
	}
	exec, db := newIntegrationExecutor(t, agents, []Tool{&writerTool{}})

	req := NewToolRequest("reader-1", "writer-tool", "write", nil)
	resp, err := exec.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Success {
		t.Fatal("expected denial for reader calling writer-tier tool")
	}
	if !strings.Contains(resp.Error, "tier") {
		t.Fatalf("expected tier-related denial, got: %s", resp.Error)
	}

	// Verify audit entry was persisted.
	entries := queryAuditEntries(t, db, "reader-1")
	if len(entries) == 0 {
		t.Fatal("expected at least 1 audit entry for denied request")
	}

	var foundDenied bool
	for _, e := range entries {
		if e.Decision == "denied" {
			foundDenied = true
			break
		}
	}
	if !foundDenied {
		t.Fatal("expected a 'denied' audit entry in the database")
	}
}
