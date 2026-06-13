package myspending

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

	if decl.Name != "my_spending" {
		t.Errorf("expected name=my_spending, got %q", decl.Name)
	}
	if decl.MinTier != ktp.TierReader {
		t.Errorf("expected MinTier=reader, got %q", decl.MinTier)
	}
	if len(decl.Actions) != 1 {
		t.Errorf("expected 1 action, got %d", len(decl.Actions))
	}
	if len(decl.DefaultTiers) != 4 {
		t.Errorf("expected 4 DefaultTiers, got %d", len(decl.DefaultTiers))
	}
}

func TestInline(t *testing.T) {
	tool := New(newTestStore(t))
	if !tool.Inline() {
		t.Error("expected Inline()=true")
	}
}

func TestSummary(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC()
	_ = store.CreateAgent(ctx, types.AgentConfig{
		ID: "agent-1", Name: "Agent One", Template: "reader",
		DesiredState: types.DesiredStateRunning,
		CreatedAt:    now, UpdatedAt: now,
	})

	// Insert a usage record so there is spending data to aggregate.
	_ = store.InsertUsageRecord(ctx, "agent-1", 100, 200, 0.05, "gpt-4o", "default", "default", "openrouter", "")

	tool := New(store)
	resp, err := tool.Execute(ctx, ktp.ToolRequest{
		ID:         "req-1",
		AgentID:    "agent-1",
		Action:     "summary",
		Parameters: map[string]any{"period": "day"},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	data := resultMap(t, resp)
	if data["agent_id"] != "agent-1" {
		t.Errorf("expected agent_id=agent-1, got %v", data["agent_id"])
	}
	if data["period"] != "day" {
		t.Errorf("expected period=day, got %v", data["period"])
	}
	if _, ok := data["total_tokens"]; !ok {
		t.Error("expected total_tokens key in response")
	}
	if _, ok := data["total_cost"]; !ok {
		t.Error("expected total_cost key in response")
	}
	if _, ok := data["requests"]; !ok {
		t.Error("expected requests key in response")
	}
}

func TestSummary_DefaultPeriod(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	tool := New(store)
	resp, err := tool.Execute(ctx, ktp.ToolRequest{
		ID:         "req-2",
		AgentID:    "agent-1",
		Action:     "summary",
		Parameters: map[string]any{},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	data := resultMap(t, resp)
	if data["period"] != "day" {
		t.Errorf("expected default period=day, got %v", data["period"])
	}
}

func TestSummary_MissingAgentID(t *testing.T) {
	tool := New(newTestStore(t))
	resp, err := tool.Execute(context.Background(), ktp.ToolRequest{
		ID:         "req-3",
		AgentID:    "",
		Action:     "summary",
		Parameters: map[string]any{},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if resp.Success {
		t.Fatal("expected failure for missing AgentID")
	}
}

func TestUnknownAction(t *testing.T) {
	tool := New(newTestStore(t))
	resp, err := tool.Execute(context.Background(), ktp.ToolRequest{
		ID:      "req-4",
		AgentID: "agent-1",
		Action:  "nonexistent",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if resp.Success {
		t.Fatal("expected failure for unknown action")
	}
}

// Verify SpendingStore interface is satisfied by PostgresStore.
var _ SpendingStore = (*postgres.PostgresStore)(nil)
