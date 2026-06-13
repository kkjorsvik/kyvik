package sysinfo

import (
	"context"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/ktp"
	"github.com/kkjorsvik/kyvik/internal/store/postgres"
	"github.com/kkjorsvik/kyvik/internal/testutil"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

func newTestStore(t *testing.T) *postgres.PostgresStore {
	t.Helper()
	tdb := testutil.RequirePostgres(t)
	return tdb.Store
}

// resultMap extracts the Result from a ToolResponse as map[string]any.
func resultMap(t *testing.T, resp *ktp.ToolResponse) map[string]any {
	t.Helper()
	m, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("expected Result to be map[string]any, got %T", resp.Result)
	}
	return m
}

func TestDeclaration(t *testing.T) {
	tool := New(newTestStore(t))
	decl := tool.Declaration()

	if decl.Name != "system_status" {
		t.Errorf("expected name=system_status, got %q", decl.Name)
	}
	if decl.MinTier != ktp.TierGuide {
		t.Errorf("expected MinTier=guide, got %q", decl.MinTier)
	}
	if len(decl.Actions) != 6 {
		t.Errorf("expected 6 actions, got %d", len(decl.Actions))
	}
}

func TestInline(t *testing.T) {
	tool := New(newTestStore(t))
	if !tool.Inline() {
		t.Error("expected Inline()=true")
	}
}

func TestAgentList(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC()
	_ = store.CreateAgent(ctx, types.AgentConfig{
		ID: "agent-1", Name: "Agent One", Template: "reader",
		DesiredState: types.DesiredStateRunning,
		CreatedAt:    now, UpdatedAt: now,
	})
	_ = store.CreateAgent(ctx, types.AgentConfig{
		ID: "agent-2", Name: "Agent Two", Template: "worker", IsGuide: true,
		DesiredState: types.DesiredStateRunning,
		CreatedAt:    now, UpdatedAt: now,
	})

	tool := New(store)
	resp, err := tool.Execute(ctx, ktp.ToolRequest{
		ID:     "req-1",
		Action: "agent_list",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	data := resultMap(t, resp)
	agents, ok := data["agents"].([]map[string]any)
	if !ok {
		t.Fatalf("expected agents to be []map[string]any, got %T", data["agents"])
	}
	if len(agents) != 2 {
		t.Errorf("expected 2 agents, got %d", len(agents))
	}

	// Verify is_guide flag is propagated.
	foundGuide := false
	for _, a := range agents {
		if a["id"] == "agent-2" && a["is_guide"] == true {
			foundGuide = true
		}
	}
	if !foundGuide {
		t.Error("expected agent-2 to have is_guide=true")
	}
}

func TestAgentStatus(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC()
	_ = store.CreateAgent(ctx, types.AgentConfig{
		ID: "agent-1", Name: "Agent One", Template: "reader",
		ModelConfig:  types.ModelConfig{Provider: "openai", Model: "gpt-4o-mini"},
		DesiredState: types.DesiredStateRunning,
		CreatedAt:    now, UpdatedAt: now,
	})

	tool := New(store)
	resp, err := tool.Execute(ctx, ktp.ToolRequest{
		ID:         "req-2",
		Action:     "agent_status",
		Parameters: map[string]any{"agent_id": "agent-1"},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	data := resultMap(t, resp)
	agent, ok := data["agent"].(map[string]any)
	if !ok {
		t.Fatalf("expected agent to be map[string]any, got %T", data["agent"])
	}
	if agent["id"] != "agent-1" {
		t.Errorf("expected id=agent-1, got %v", agent["id"])
	}
	if agent["model"] != "openai/gpt-4o-mini" {
		t.Errorf("expected model=openai/gpt-4o-mini, got %v", agent["model"])
	}
}

func TestAgentStatus_NotFound(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	tool := New(store)
	resp, err := tool.Execute(ctx, ktp.ToolRequest{
		ID:         "req-3",
		Action:     "agent_status",
		Parameters: map[string]any{"agent_id": "nonexistent"},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if resp.Success {
		t.Fatal("expected failure for nonexistent agent")
	}
}

func TestAgentStatus_MissingParam(t *testing.T) {
	tool := New(newTestStore(t))
	resp, err := tool.Execute(context.Background(), ktp.ToolRequest{
		ID:         "req-missing",
		Action:     "agent_status",
		Parameters: map[string]any{},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if resp.Success {
		t.Fatal("expected failure for missing agent_id")
	}
}

func TestSystemOverview(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC()
	_ = store.CreateAgent(ctx, types.AgentConfig{
		ID: "agent-1", Name: "Agent One", Template: "reader",
		DesiredState: types.DesiredStateRunning,
		CreatedAt:    now, UpdatedAt: now,
	})

	tool := New(store)
	resp, err := tool.Execute(ctx, ktp.ToolRequest{
		ID:     "req-4",
		Action: "system_overview",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	data := resultMap(t, resp)
	if data["total_agents"] != 1 {
		t.Errorf("expected total_agents=1, got %v", data["total_agents"])
	}
}

func TestRecentErrors(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Insert a denied audit entry.
	_ = store.InsertAuditEntry(ctx, types.AuditEntry{
		AgentID:   "agent-1",
		EventType: types.EventToolCall,
		Action:    "shell.execute",
		Resource:  "rm -rf /",
		Decision:  "denied",
		Details:   "command not in allowlist",
		Timestamp: time.Now().UTC(),
	})

	tool := New(store)
	resp, err := tool.Execute(ctx, ktp.ToolRequest{
		ID:         "req-5",
		Action:     "recent_errors",
		Parameters: map[string]any{"limit": float64(10)},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	data := resultMap(t, resp)
	errors, ok := data["errors"].([]map[string]any)
	if !ok {
		t.Fatalf("expected errors to be []map[string]any, got %T", data["errors"])
	}
	if len(errors) < 1 {
		t.Error("expected at least 1 error entry")
	}
}

func TestRecentAlerts(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Insert a security event.
	_ = store.InsertSecurityEvent(ctx, types.SecurityEvent{
		ID:        "sec-1",
		AgentID:   "agent-1",
		EventType: "canary_triggered",
		Severity:  "high",
		Details:   "canary token detected in output",
		CreatedAt: time.Now().UTC(),
	})

	tool := New(store)
	resp, err := tool.Execute(ctx, ktp.ToolRequest{
		ID:         "req-6",
		Action:     "recent_alerts",
		Parameters: map[string]any{"limit": float64(5)},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	data := resultMap(t, resp)
	alerts, ok := data["alerts"].([]map[string]any)
	if !ok {
		t.Fatalf("expected alerts to be []map[string]any, got %T", data["alerts"])
	}
	if len(alerts) < 1 {
		t.Error("expected at least 1 alert")
	}
}

func TestUnknownAction(t *testing.T) {
	tool := New(newTestStore(t))
	resp, err := tool.Execute(context.Background(), ktp.ToolRequest{
		ID:     "req-7",
		Action: "nonexistent",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if resp.Success {
		t.Fatal("expected failure for unknown action")
	}
}

func TestSpendingSummary(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC()
	_ = store.CreateAgent(ctx, types.AgentConfig{
		ID: "agent-1", Name: "Agent One", Template: "reader",
		DesiredState: types.DesiredStateRunning,
		CreatedAt:    now, UpdatedAt: now,
	})

	tool := New(store)
	resp, err := tool.Execute(ctx, ktp.ToolRequest{
		ID:         "req-8",
		Action:     "spending_summary",
		Parameters: map[string]any{"period": "day"},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	data := resultMap(t, resp)
	if _, ok := data["agents"]; !ok {
		t.Error("expected agents key in response")
	}
	if _, ok := data["providers"]; !ok {
		t.Error("expected providers key in response")
	}
}

// Verify StatusStore interface is satisfied by PostgresStore.
var _ StatusStore = (*postgres.PostgresStore)(nil)
