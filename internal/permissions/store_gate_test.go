package permissions_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/audit"
	"github.com/kkjorsvik/kyvik/internal/permissions"
	"github.com/kkjorsvik/kyvik/internal/store/postgres"
	"github.com/kkjorsvik/kyvik/internal/testutil"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// helper creates a StoreGate backed by a real PostgreSQL database.
func newTestGate(t *testing.T) (*permissions.StoreGate, *audit.StoreLogger, *postgres.PostgresStore) {
	t.Helper()
	tdb := testutil.RequirePostgres(t)

	logger := audit.NewStoreLogger(tdb.Store, 10)
	t.Cleanup(func() { logger.Close() })
	gate := permissions.NewStoreGate(tdb.Store, logger, "")
	return gate, logger, tdb.Store
}

// seedAgent inserts a test agent with the given template.
func seedAgent(t *testing.T, s *postgres.PostgresStore, id, template string) {
	t.Helper()
	now := time.Now().UTC()
	err := s.CreateAgent(context.Background(), types.AgentConfig{
		ID:       id,
		Name:     id,
		Template: template,
		ModelConfig: types.ModelConfig{
			Provider: "test",
			Model:    "test-model",
		},
		Channels: []types.ChannelMapping{},
		Metadata: map[string]string{},
		CreatedAt: now,
		UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("CreateAgent(%s): %v", id, err)
	}
}

func TestReaderDeniesWrite(t *testing.T) {
	gate, _, s := newTestGate(t)
	seedAgent(t, s, "r1", "reader")

	d, err := gate.Check(context.Background(), "r1", types.ToolCall{
		ToolName: "filesystem",
		Action:   "write",
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if d.Allowed {
		t.Error("reader should be denied filesystem.write")
	}
	if d.Rule != "default_deny" {
		t.Errorf("Rule = %q, want %q", d.Rule, "default_deny")
	}
}

func TestWorkerAllowsReadAndWrite(t *testing.T) {
	gate, _, s := newTestGate(t)
	seedAgent(t, s, "w1", "worker")
	ctx := context.Background()

	for _, action := range []string{"read", "write"} {
		d, err := gate.Check(ctx, "w1", types.ToolCall{
			ToolName: "filesystem",
			Action:   action,
		})
		if err != nil {
			t.Fatalf("Check(filesystem.%s): %v", action, err)
		}
		if !d.Allowed {
			t.Errorf("worker should be allowed filesystem.%s", action)
		}
	}
}

func TestWorkerDeniesDelete(t *testing.T) {
	gate, _, s := newTestGate(t)
	seedAgent(t, s, "w1", "worker")

	d, err := gate.Check(context.Background(), "w1", types.ToolCall{
		ToolName: "filesystem",
		Action:   "delete",
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if d.Allowed {
		t.Error("worker should be denied filesystem.delete")
	}
}

func TestAdminAllowsEverything(t *testing.T) {
	gate, _, s := newTestGate(t)
	seedAgent(t, s, "a1", "admin")

	d, err := gate.Check(context.Background(), "a1", types.ToolCall{
		ToolName:   "anything",
		Action:     "whatever",
		Parameters: map[string]any{"resource": "/secret/path"},
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !d.Allowed {
		t.Error("admin should be allowed everything")
	}
	if d.Rule != "template:admin" {
		t.Errorf("Rule = %q, want %q", d.Rule, "template:admin")
	}
}

func TestDefaultDeny(t *testing.T) {
	gate, _, s := newTestGate(t)
	seedAgent(t, s, "u1", "unknown_template")

	d, err := gate.Check(context.Background(), "u1", types.ToolCall{
		ToolName: "filesystem",
		Action:   "read",
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if d.Allowed {
		t.Error("unknown template should result in default deny")
	}
	if d.Rule != "default_deny" {
		t.Errorf("Rule = %q, want %q", d.Rule, "default_deny")
	}
}

func TestGrantOverride(t *testing.T) {
	gate, _, s := newTestGate(t)
	seedAgent(t, s, "r1", "reader")
	ctx := context.Background()

	// Reader can't write by default.
	d, err := gate.Check(ctx, "r1", types.ToolCall{ToolName: "filesystem", Action: "write"})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if d.Allowed {
		t.Fatal("reader should not be allowed filesystem.write without override")
	}

	// Add grant override.
	err = gate.AddOverride(ctx, permissions.Override{
		AgentID:    "r1",
		Capability: types.Capability{Tool: "filesystem", Action: "write", Resource: "*"},
		Grant:      true,
	})
	if err != nil {
		t.Fatalf("AddOverride: %v", err)
	}

	// Now should be allowed.
	d, err = gate.Check(ctx, "r1", types.ToolCall{ToolName: "filesystem", Action: "write"})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !d.Allowed {
		t.Error("reader with grant override should be allowed filesystem.write")
	}
	if d.Rule != "grant_override" {
		t.Errorf("Rule = %q, want %q", d.Rule, "grant_override")
	}
}

func TestDenyOverride(t *testing.T) {
	gate, _, s := newTestGate(t)
	seedAgent(t, s, "a1", "admin")
	ctx := context.Background()

	// Admin can delete by default.
	d, err := gate.Check(ctx, "a1", types.ToolCall{ToolName: "filesystem", Action: "delete"})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !d.Allowed {
		t.Fatal("admin should be allowed filesystem.delete")
	}

	// Add deny override.
	err = gate.AddOverride(ctx, permissions.Override{
		AgentID:    "a1",
		Capability: types.Capability{Tool: "filesystem", Action: "delete", Resource: "*"},
		Grant:      false,
	})
	if err != nil {
		t.Fatalf("AddOverride: %v", err)
	}

	// Now should be denied.
	d, err = gate.Check(ctx, "a1", types.ToolCall{ToolName: "filesystem", Action: "delete"})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if d.Allowed {
		t.Error("admin with deny override should be denied filesystem.delete")
	}
	if d.Rule != "deny_override" {
		t.Errorf("Rule = %q, want %q", d.Rule, "deny_override")
	}
}

func TestDenyOverrideTrumpsGrant(t *testing.T) {
	gate, _, s := newTestGate(t)
	seedAgent(t, s, "a1", "reader")
	ctx := context.Background()

	cap := types.Capability{Tool: "filesystem", Action: "write", Resource: "*"}

	// Add both grant and deny overrides.
	err := gate.AddOverride(ctx, permissions.Override{AgentID: "a1", Capability: cap, Grant: true})
	if err != nil {
		t.Fatalf("AddOverride(grant): %v", err)
	}
	// The same capability tuple will be upserted, so use a different approach:
	// deny a specific resource while granting wildcard, or vice versa.
	// Actually per the plan: "both deny and grant override for same cap → denied"
	// But upsert means the same (tool, action, resource) tuple only stores one value.
	// So we test with overlapping but non-identical capabilities.
	denyCap := types.Capability{Tool: "filesystem", Action: "write", Resource: "/secret"}
	err = gate.AddOverride(ctx, permissions.Override{AgentID: "a1", Capability: denyCap, Grant: false})
	if err != nil {
		t.Fatalf("AddOverride(deny): %v", err)
	}

	// Check for the specific denied resource — deny override should win.
	d, err := gate.Check(ctx, "a1", types.ToolCall{
		ToolName:   "filesystem",
		Action:     "write",
		Parameters: map[string]any{"path": "/secret"},
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if d.Allowed {
		t.Error("deny override should trump grant override for /secret")
	}
	if d.Rule != "deny_override" {
		t.Errorf("Rule = %q, want %q", d.Rule, "deny_override")
	}

	// Check for a different resource — grant override should apply.
	d, err = gate.Check(ctx, "a1", types.ToolCall{
		ToolName:   "filesystem",
		Action:     "write",
		Parameters: map[string]any{"path": "/public"},
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !d.Allowed {
		t.Error("grant override should allow filesystem.write for /public")
	}
}

func TestAddAndRemoveOverride(t *testing.T) {
	gate, _, s := newTestGate(t)
	seedAgent(t, s, "r1", "reader")
	ctx := context.Background()

	cap := types.Capability{Tool: "filesystem", Action: "write", Resource: "*"}

	// Add override — reader can now write.
	err := gate.AddOverride(ctx, permissions.Override{AgentID: "r1", Capability: cap, Grant: true})
	if err != nil {
		t.Fatalf("AddOverride: %v", err)
	}

	d, err := gate.Check(ctx, "r1", types.ToolCall{ToolName: "filesystem", Action: "write"})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !d.Allowed {
		t.Error("expected allowed after adding grant override")
	}

	// Remove override — reader can't write again.
	err = gate.RemoveOverride(ctx, "r1", cap)
	if err != nil {
		t.Fatalf("RemoveOverride: %v", err)
	}

	d, err = gate.Check(ctx, "r1", types.ToolCall{ToolName: "filesystem", Action: "write"})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if d.Allowed {
		t.Error("expected denied after removing grant override")
	}

	// Removing again should return ErrNotFound.
	err = gate.RemoveOverride(ctx, "r1", cap)
	if !errors.Is(err, types.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

func TestCheckLogsToAudit(t *testing.T) {
	gate, logger, s := newTestGate(t)
	seedAgent(t, s, "r1", "reader")
	ctx := context.Background()

	_, err := gate.Check(ctx, "r1", types.ToolCall{
		ToolName:   "filesystem",
		Action:     "read",
		Parameters: map[string]any{"path": "/data/file.txt"},
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}

	logger.Flush()

	entries, err := logger.Query(ctx, audit.Filter{
		AgentID:   "r1",
		EventType: types.EventPermission,
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d audit entries, want 1", len(entries))
	}

	got := entries[0]
	if got.EventType != types.EventPermission {
		t.Errorf("EventType = %q, want %q", got.EventType, types.EventPermission)
	}
	if got.Decision != "allowed" {
		t.Errorf("Decision = %q, want %q", got.Decision, "allowed")
	}
	if got.Action != "filesystem.read" {
		t.Errorf("Action = %q, want %q", got.Action, "filesystem.read")
	}
	if got.Resource != "/data/file.txt" {
		t.Errorf("Resource = %q, want %q", got.Resource, "/data/file.txt")
	}
}

func TestYAMLTemplateLoading(t *testing.T) {
	dir := t.TempDir()

	yamlContent := `name: custom
description: "Custom template for testing"
capabilities:
  - tool: special
    action: do
    resource: "*"
  - tool: filesystem
    action: read
    resource: "/custom/*"
`
	err := os.WriteFile(filepath.Join(dir, "custom.yaml"), []byte(yamlContent), 0o644)
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	tdb := testutil.RequirePostgres(t)

	logger := audit.NewStoreLogger(tdb.Store, 10)
	t.Cleanup(func() { logger.Close() })
	gate := permissions.NewStoreGate(tdb.Store, logger, dir)

	ctx := context.Background()

	// Verify the custom template loaded.
	tmpl, err := gate.LoadTemplate(ctx, "custom")
	if err != nil {
		t.Fatalf("LoadTemplate: %v", err)
	}
	if tmpl.Name != "custom" {
		t.Errorf("Name = %q, want %q", tmpl.Name, "custom")
	}
	if len(tmpl.Capabilities) != 2 {
		t.Fatalf("got %d capabilities, want 2", len(tmpl.Capabilities))
	}

	// Verify built-in templates still exist.
	_, err = gate.LoadTemplate(ctx, "reader")
	if err != nil {
		t.Errorf("built-in reader template should still exist: %v", err)
	}

	// Verify the custom template works in checks.
	seedAgent(t, tdb.Store, "c1", "custom")
	d, err := gate.Check(ctx, "c1", types.ToolCall{ToolName: "special", Action: "do"})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !d.Allowed {
		t.Error("custom template should allow special.do")
	}
	if d.Rule != "template:custom" {
		t.Errorf("Rule = %q, want %q", d.Rule, "template:custom")
	}
}

func TestOperatorAllowsExecuteAndDelete(t *testing.T) {
	gate, _, s := newTestGate(t)
	seedAgent(t, s, "op1", "operator")
	ctx := context.Background()

	tests := []struct {
		tool   string
		action string
	}{
		{"shell", "execute"},
		{"code_exec", "execute"},
		{"filesystem", "delete"},
		{"filesystem", "read"},
		{"filesystem", "write"},
		{"http", "get"},
		{"http", "post"},
		{"http", "delete"},
		{"database", "select"},
		{"database", "insert"},
		{"database", "update"},
		{"database", "delete"},
	}

	for _, tt := range tests {
		d, err := gate.Check(ctx, "op1", types.ToolCall{
			ToolName: tt.tool,
			Action:   tt.action,
		})
		if err != nil {
			t.Fatalf("Check(%s.%s): %v", tt.tool, tt.action, err)
		}
		if !d.Allowed {
			t.Errorf("operator should be allowed %s.%s", tt.tool, tt.action)
		}
		if d.Rule != "template:operator" {
			t.Errorf("Rule = %q, want %q for %s.%s", d.Rule, "template:operator", tt.tool, tt.action)
		}
	}

	// Operator should NOT have wildcard access — verify something not in the list is denied.
	d, err := gate.Check(ctx, "op1", types.ToolCall{
		ToolName: "admin_panel",
		Action:   "configure",
	})
	if err != nil {
		t.Fatalf("Check(admin_panel.configure): %v", err)
	}
	if d.Allowed {
		t.Error("operator should NOT be allowed admin_panel.configure (not in capability list)")
	}
}

func TestGuideTemplateAllowsKyvikRead(t *testing.T) {
	gate, _, s := newTestGate(t)
	seedAgent(t, s, "g1", "guide")
	ctx := context.Background()

	// Guide should be allowed kyvik.read.docs
	d, err := gate.Check(ctx, "g1", types.ToolCall{
		ToolName:   "kyvik",
		Action:     "read",
		Parameters: map[string]any{"resource": "docs"},
	})
	if err != nil {
		t.Fatalf("Check(kyvik.read.docs): %v", err)
	}
	if !d.Allowed {
		t.Error("guide should be allowed kyvik.read.docs")
	}
	if d.Rule != "template:guide" {
		t.Errorf("Rule = %q, want %q", d.Rule, "template:guide")
	}

	// Guide should be allowed kyvik.read.logs
	d, err = gate.Check(ctx, "g1", types.ToolCall{
		ToolName:   "kyvik",
		Action:     "read",
		Parameters: map[string]any{"resource": "logs"},
	})
	if err != nil {
		t.Fatalf("Check(kyvik.read.logs): %v", err)
	}
	if !d.Allowed {
		t.Error("guide should be allowed kyvik.read.logs")
	}

	// Guide (basic) should NOT be allowed filesystem.write
	d, err = gate.Check(ctx, "g1", types.ToolCall{
		ToolName: "filesystem",
		Action:   "write",
	})
	if err != nil {
		t.Fatalf("Check(filesystem.write): %v", err)
	}
	if d.Allowed {
		t.Error("guide should NOT be allowed filesystem.write")
	}

	// Guide (basic) should NOT be allowed kyvik.read.agents (only in full mode)
	d, err = gate.Check(ctx, "g1", types.ToolCall{
		ToolName:   "kyvik",
		Action:     "read",
		Parameters: map[string]any{"resource": "agents"},
	})
	if err != nil {
		t.Fatalf("Check(kyvik.read.agents): %v", err)
	}
	if d.Allowed {
		t.Error("guide (basic) should NOT be allowed kyvik.read.agents")
	}
}

func TestGuideSetGuideMode(t *testing.T) {
	gate, _, s := newTestGate(t)
	seedAgent(t, s, "g1", "guide")
	ctx := context.Background()

	// Default (basic) mode: kyvik.read.agents should be denied.
	d, err := gate.Check(ctx, "g1", types.ToolCall{
		ToolName:   "kyvik",
		Action:     "read",
		Parameters: map[string]any{"resource": "agents"},
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if d.Allowed {
		t.Error("guide (basic) should NOT allow kyvik.read.agents")
	}

	// Switch to full mode.
	gate.SetGuideMode("full")

	// Now kyvik.read.agents should be allowed.
	d, err = gate.Check(ctx, "g1", types.ToolCall{
		ToolName:   "kyvik",
		Action:     "read",
		Parameters: map[string]any{"resource": "agents"},
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !d.Allowed {
		t.Error("guide (full) should allow kyvik.read.agents")
	}

	// kyvik.read.status should also be allowed in full mode.
	d, err = gate.Check(ctx, "g1", types.ToolCall{
		ToolName:   "kyvik",
		Action:     "read",
		Parameters: map[string]any{"resource": "status"},
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !d.Allowed {
		t.Error("guide (full) should allow kyvik.read.status")
	}

	// Switch back to basic.
	gate.SetGuideMode("basic")

	d, err = gate.Check(ctx, "g1", types.ToolCall{
		ToolName:   "kyvik",
		Action:     "read",
		Parameters: map[string]any{"resource": "agents"},
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if d.Allowed {
		t.Error("guide (basic again) should NOT allow kyvik.read.agents")
	}
}

func TestLoadTemplateNotFound(t *testing.T) {
	gate, _, _ := newTestGate(t)

	_, err := gate.LoadTemplate(context.Background(), "nonexistent")
	if !errors.Is(err, types.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

func TestEscalationLimit_ReaderCannotGetExecute(t *testing.T) {
	gate, _, s := newTestGate(t)
	seedAgent(t, s, "read-agent", "reader")
	ctx := context.Background()

	err := gate.AddOverride(ctx, permissions.Override{
		AgentID:    "read-agent",
		Capability: types.Capability{Tool: "shell", Action: "execute", Resource: "*"},
		Grant:      true,
	})
	if err == nil {
		t.Error("expected error: reader cannot be granted execute capabilities")
	}
}

func TestEscalationLimit_ReaderCanGetWrite(t *testing.T) {
	gate, _, s := newTestGate(t)
	seedAgent(t, s, "read-agent", "reader")
	ctx := context.Background()

	err := gate.AddOverride(ctx, permissions.Override{
		AgentID:    "read-agent",
		Capability: types.Capability{Tool: "filesystem", Action: "write", Resource: "*"},
		Grant:      true,
	})
	if err != nil {
		t.Errorf("reader should be able to get write override, got: %v", err)
	}
}

func TestEscalationLimit_WorkerCanGetExecute(t *testing.T) {
	gate, _, s := newTestGate(t)
	seedAgent(t, s, "work-agent", "worker")
	ctx := context.Background()

	err := gate.AddOverride(ctx, permissions.Override{
		AgentID:    "work-agent",
		Capability: types.Capability{Tool: "shell", Action: "execute", Resource: "*"},
		Grant:      true,
	})
	if err != nil {
		t.Errorf("worker should be able to get execute override, got: %v", err)
	}
}

func TestEscalationLimit_WorkerCannotGetWildcard(t *testing.T) {
	gate, _, s := newTestGate(t)
	seedAgent(t, s, "work-agent", "worker")
	ctx := context.Background()

	err := gate.AddOverride(ctx, permissions.Override{
		AgentID:    "work-agent",
		Capability: types.Capability{Tool: "*", Action: "*", Resource: "*"},
		Grant:      true,
	})
	if err == nil {
		t.Error("expected error: worker cannot be granted wildcard capabilities")
	}
}

func TestEscalationLimit_OperatorCanGetSpecificButNotWildcard(t *testing.T) {
	gate, _, s := newTestGate(t)
	seedAgent(t, s, "ops-agent", "operator")
	ctx := context.Background()

	// Operator can get specific admin-level capabilities
	err := gate.AddOverride(ctx, permissions.Override{
		AgentID:    "ops-agent",
		Capability: types.Capability{Tool: "custom_tool", Action: "read", Resource: "/special/*"},
		Grant:      true,
	})
	if err != nil {
		t.Errorf("operator should be able to get specific capability, got: %v", err)
	}
}

func TestEscalationLimit_GuideCannotGetGrants(t *testing.T) {
	gate, _, s := newTestGate(t)
	seedAgent(t, s, "guide-agent", "guide")
	ctx := context.Background()

	err := gate.AddOverride(ctx, permissions.Override{
		AgentID:    "guide-agent",
		Capability: types.Capability{Tool: "filesystem", Action: "read", Resource: "*"},
		Grant:      true,
	})
	if err == nil {
		t.Error("expected error: guide agent cannot receive grant overrides")
	}
}

func TestEscalationLimit_DenyOverridesAlwaysAllowed(t *testing.T) {
	gate, _, s := newTestGate(t)
	seedAgent(t, s, "admin-agent", "admin")
	ctx := context.Background()

	err := gate.AddOverride(ctx, permissions.Override{
		AgentID:    "admin-agent",
		Capability: types.Capability{Tool: "shell", Action: "execute", Resource: "*"},
		Grant:      false,
	})
	if err != nil {
		t.Errorf("deny overrides should always be allowed, got: %v", err)
	}
}
