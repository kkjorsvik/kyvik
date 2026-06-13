package spending_test

import (
	"context"
	"testing"

	"github.com/kkjorsvik/kyvik/internal/audit"
	"github.com/kkjorsvik/kyvik/internal/spending"
	"github.com/kkjorsvik/kyvik/internal/testutil"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

func newTestTracker(t *testing.T) (*spending.StoreTracker, *audit.StoreLogger) {
	t.Helper()
	tdb := testutil.RequirePostgres(t)
	logger := audit.NewStoreLogger(tdb.Store, 10)
	t.Cleanup(func() { logger.Close() })
	tracker := spending.NewStoreTracker(tdb.Store, logger, "test-model")
	return tracker, logger
}

func TestRecordUsage(t *testing.T) {
	tracker, _ := newTestTracker(t)
	ctx := context.Background()

	if err := tracker.Record(ctx, "a1", 100, 50, 0.01, spending.RecordOptions{}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := tracker.Record(ctx, "a1", 200, 100, 0.02, spending.RecordOptions{}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	summary, err := tracker.GetSummary(ctx, spending.Filter{AgentID: "a1"})
	if err != nil {
		t.Fatalf("GetSummary: %v", err)
	}
	if summary.TotalTokens != 450 {
		t.Errorf("TotalTokens = %d, want 450", summary.TotalTokens)
	}
	if summary.TotalCost < 0.029 || summary.TotalCost > 0.031 {
		t.Errorf("TotalCost = %f, want ~0.03", summary.TotalCost)
	}
	if summary.RequestCount != 2 {
		t.Errorf("RequestCount = %d, want 2", summary.RequestCount)
	}
}

func TestRecordUsage_ComputesCatalogCostWhenMissing(t *testing.T) {
	tracker, _ := newTestTracker(t)
	ctx := context.Background()

	err := tracker.Record(ctx, "a1", 1000, 500, 0, spending.RecordOptions{
		Provider: "openai",
		Model:    "gpt-4o",
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}

	summary, err := tracker.GetSummary(ctx, spending.Filter{AgentID: "a1"})
	if err != nil {
		t.Fatalf("GetSummary: %v", err)
	}
	if summary.TotalCost < 0.0074 || summary.TotalCost > 0.0076 {
		t.Errorf("TotalCost = %f, want ~0.0075", summary.TotalCost)
	}
}

func TestCheckBudgetWithinLimits(t *testing.T) {
	tracker, _ := newTestTracker(t)
	ctx := context.Background()

	if err := tracker.SetAgentLimit(ctx, "a1", types.SpendingLimits{
		MaxSpendPerDay: 10.0, MaxSpendPerMonth: 100.0,
		MaxTokensPerDay: 100000, MaxTokensPerMonth: 1000000,
	}); err != nil {
		t.Fatalf("SetAgentLimit: %v", err)
	}

	if err := tracker.Record(ctx, "a1", 100, 50, 0.01, spending.RecordOptions{}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	status, err := tracker.CheckBudget(ctx, "a1")
	if err != nil {
		t.Fatalf("CheckBudget: %v", err)
	}
	if !status.WithinBudget {
		t.Error("expected WithinBudget = true")
	}
	if status.DailyLimit != 10.0 {
		t.Errorf("DailyLimit = %f, want 10.0", status.DailyLimit)
	}
	if status.MonthlyLimit != 100.0 {
		t.Errorf("MonthlyLimit = %f, want 100.0", status.MonthlyLimit)
	}
	if status.DailyUsed < 0.009 || status.DailyUsed > 0.011 {
		t.Errorf("DailyUsed = %f, want ~0.01", status.DailyUsed)
	}
	if status.TokensToday != 150 {
		t.Errorf("TokensToday = %d, want 150", status.TokensToday)
	}
}

func TestCheckBudgetOverDailySpend(t *testing.T) {
	tracker, _ := newTestTracker(t)
	ctx := context.Background()

	if err := tracker.SetAgentLimit(ctx, "a1", types.SpendingLimits{
		MaxSpendPerDay: 0.01,
	}); err != nil {
		t.Fatalf("SetAgentLimit: %v", err)
	}

	if err := tracker.Record(ctx, "a1", 100, 50, 0.02, spending.RecordOptions{}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	status, err := tracker.CheckBudget(ctx, "a1")
	if err != nil {
		t.Fatalf("CheckBudget: %v", err)
	}
	if status.WithinBudget {
		t.Error("expected WithinBudget = false (daily spend exceeded)")
	}
}

func TestCheckBudgetOverMonthlySpend(t *testing.T) {
	tracker, _ := newTestTracker(t)
	ctx := context.Background()

	if err := tracker.SetAgentLimit(ctx, "a1", types.SpendingLimits{
		MaxSpendPerMonth: 0.01,
	}); err != nil {
		t.Fatalf("SetAgentLimit: %v", err)
	}

	if err := tracker.Record(ctx, "a1", 100, 50, 0.02, spending.RecordOptions{}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	status, err := tracker.CheckBudget(ctx, "a1")
	if err != nil {
		t.Fatalf("CheckBudget: %v", err)
	}
	if status.WithinBudget {
		t.Error("expected WithinBudget = false (monthly spend exceeded)")
	}
}

func TestCheckBudgetOverTokenLimit(t *testing.T) {
	tracker, _ := newTestTracker(t)
	ctx := context.Background()

	if err := tracker.SetAgentLimit(ctx, "a1", types.SpendingLimits{
		MaxTokensPerDay: 100,
	}); err != nil {
		t.Fatalf("SetAgentLimit: %v", err)
	}

	// 100 + 50 = 150 tokens total, exceeds limit of 100
	if err := tracker.Record(ctx, "a1", 100, 50, 0.01, spending.RecordOptions{}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	status, err := tracker.CheckBudget(ctx, "a1")
	if err != nil {
		t.Fatalf("CheckBudget: %v", err)
	}
	if status.WithinBudget {
		t.Error("expected WithinBudget = false (token limit exceeded)")
	}
	if status.TokensToday != 150 {
		t.Errorf("TokensToday = %d, want 150", status.TokensToday)
	}
}

func TestLayeredLimitsGlobalMoreRestrictive(t *testing.T) {
	tracker, _ := newTestTracker(t)
	ctx := context.Background()

	// Global = $10/day, agent = $20/day → effective = $10/day
	if err := tracker.SetGlobalLimit(ctx, types.SpendingLimits{MaxSpendPerDay: 10.0}); err != nil {
		t.Fatalf("SetGlobalLimit: %v", err)
	}
	if err := tracker.SetAgentLimit(ctx, "a1", types.SpendingLimits{MaxSpendPerDay: 20.0}); err != nil {
		t.Fatalf("SetAgentLimit: %v", err)
	}

	// Record $15 in spending (exceeds global $10, within agent $20)
	if err := tracker.Record(ctx, "a1", 1000, 500, 15.0, spending.RecordOptions{}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	status, err := tracker.CheckBudget(ctx, "a1")
	if err != nil {
		t.Fatalf("CheckBudget: %v", err)
	}
	if status.WithinBudget {
		t.Error("expected WithinBudget = false (global limit is more restrictive)")
	}
	if status.DailyLimit != 10.0 {
		t.Errorf("DailyLimit = %f, want 10.0 (global wins)", status.DailyLimit)
	}
}

func TestLayeredLimitsAgentMoreRestrictive(t *testing.T) {
	tracker, _ := newTestTracker(t)
	ctx := context.Background()

	// Global = $20/day, agent = $10/day → effective = $10/day
	if err := tracker.SetGlobalLimit(ctx, types.SpendingLimits{MaxSpendPerDay: 20.0}); err != nil {
		t.Fatalf("SetGlobalLimit: %v", err)
	}
	if err := tracker.SetAgentLimit(ctx, "a1", types.SpendingLimits{MaxSpendPerDay: 10.0}); err != nil {
		t.Fatalf("SetAgentLimit: %v", err)
	}

	// Record $15 in spending (within global $20, exceeds agent $10)
	if err := tracker.Record(ctx, "a1", 1000, 500, 15.0, spending.RecordOptions{}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	status, err := tracker.CheckBudget(ctx, "a1")
	if err != nil {
		t.Fatalf("CheckBudget: %v", err)
	}
	if status.WithinBudget {
		t.Error("expected WithinBudget = false (agent limit is more restrictive)")
	}
	if status.DailyLimit != 10.0 {
		t.Errorf("DailyLimit = %f, want 10.0 (agent wins)", status.DailyLimit)
	}
}

func TestLayeredLimitsOneZero(t *testing.T) {
	tracker, _ := newTestTracker(t)
	ctx := context.Background()

	// Global = $10/day, agent = 0 (unlimited) → effective = $10/day
	if err := tracker.SetGlobalLimit(ctx, types.SpendingLimits{MaxSpendPerDay: 10.0}); err != nil {
		t.Fatalf("SetGlobalLimit: %v", err)
	}

	// Record $15 in spending
	if err := tracker.Record(ctx, "a1", 1000, 500, 15.0, spending.RecordOptions{}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	status, err := tracker.CheckBudget(ctx, "a1")
	if err != nil {
		t.Fatalf("CheckBudget: %v", err)
	}
	if status.WithinBudget {
		t.Error("expected WithinBudget = false (global limit applies when agent is zero)")
	}
	if status.DailyLimit != 10.0 {
		t.Errorf("DailyLimit = %f, want 10.0", status.DailyLimit)
	}
}

func TestNoLimitsUnlimited(t *testing.T) {
	tracker, _ := newTestTracker(t)
	ctx := context.Background()

	// No limits set at all — record large usage
	if err := tracker.Record(ctx, "a1", 999999, 999999, 9999.99, spending.RecordOptions{}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	status, err := tracker.CheckBudget(ctx, "a1")
	if err != nil {
		t.Fatalf("CheckBudget: %v", err)
	}
	if !status.WithinBudget {
		t.Error("expected WithinBudget = true (no limits set)")
	}
}

func TestCheckBudgetAuditLog(t *testing.T) {
	tracker, logger := newTestTracker(t)
	ctx := context.Background()

	if err := tracker.SetAgentLimit(ctx, "a1", types.SpendingLimits{
		MaxSpendPerDay: 0.01,
	}); err != nil {
		t.Fatalf("SetAgentLimit: %v", err)
	}

	if err := tracker.Record(ctx, "a1", 100, 50, 0.05, spending.RecordOptions{}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	_, err := tracker.CheckBudget(ctx, "a1")
	if err != nil {
		t.Fatalf("CheckBudget: %v", err)
	}

	logger.Flush()

	// Query audit log for spending denial
	entries, err := logger.Query(ctx, audit.Filter{
		EventType: types.EventSpending,
		Decision:  "denied",
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected at least one denied spending audit entry")
	}

	found := false
	for _, e := range entries {
		if e.AgentID == "a1" && e.Action == "check_budget" && e.Decision == "denied" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected audit entry with action=check_budget decision=denied for agent a1")
	}
}

func TestRecordWithSlotInfo(t *testing.T) {
	tracker, _ := newTestTracker(t)
	ctx := context.Background()

	err := tracker.Record(ctx, "a1", 100, 50, 0.01, spending.RecordOptions{
		Model:     "gpt-4o",
		ModelSlot: "heavy",
		RoutedBy:  "prefix",
		Provider:  "openrouter",
	})
	if err != nil {
		t.Fatalf("Record with slot info: %v", err)
	}

	// Verify via slot breakdown
	slots, err := tracker.GetSlotBreakdown(ctx, "a1", "day")
	if err != nil {
		t.Fatalf("GetSlotBreakdown: %v", err)
	}
	if len(slots) != 1 {
		t.Fatalf("got %d slot summaries, want 1", len(slots))
	}
	if slots[0].SlotName != "heavy" {
		t.Errorf("SlotName = %q, want %q", slots[0].SlotName, "heavy")
	}
	if slots[0].Provider != "openrouter" {
		t.Errorf("Provider = %q, want %q", slots[0].Provider, "openrouter")
	}
	if slots[0].Model != "gpt-4o" {
		t.Errorf("Model = %q, want %q", slots[0].Model, "gpt-4o")
	}
	if slots[0].TotalTokens != 150 {
		t.Errorf("TotalTokens = %d, want 150", slots[0].TotalTokens)
	}
}

func TestGetSlotBreakdown(t *testing.T) {
	tracker, _ := newTestTracker(t)
	ctx := context.Background()

	// Record across multiple slots
	_ = tracker.Record(ctx, "a1", 100, 50, 0.01, spending.RecordOptions{
		Model: "gpt-4o-mini", ModelSlot: "default", RoutedBy: "default", Provider: "openrouter",
	})
	_ = tracker.Record(ctx, "a1", 200, 100, 0.05, spending.RecordOptions{
		Model: "gpt-4o", ModelSlot: "heavy", RoutedBy: "classifier", Provider: "openrouter",
	})
	_ = tracker.Record(ctx, "a1", 50, 25, 0.005, spending.RecordOptions{
		Model: "gpt-4o-mini", ModelSlot: "classifier", RoutedBy: "system:classifier", Provider: "openrouter",
	})
	_ = tracker.Record(ctx, "a1", 150, 75, 0.03, spending.RecordOptions{
		Model: "gpt-4o", ModelSlot: "heavy", RoutedBy: "prefix", Provider: "openrouter",
	})

	slots, err := tracker.GetSlotBreakdown(ctx, "a1", "day")
	if err != nil {
		t.Fatalf("GetSlotBreakdown: %v", err)
	}
	if len(slots) != 3 {
		t.Fatalf("got %d slot groups, want 3", len(slots))
	}

	// Verify ordering (by cost DESC): heavy ($0.08), default ($0.01), classifier ($0.005)
	if slots[0].SlotName != "heavy" {
		t.Errorf("first slot = %q, want %q", slots[0].SlotName, "heavy")
	}
	if slots[0].RequestCount != 2 {
		t.Errorf("heavy RequestCount = %d, want 2", slots[0].RequestCount)
	}
}

func TestGetProviderBreakdown(t *testing.T) {
	tracker, _ := newTestTracker(t)
	ctx := context.Background()

	// Record across multiple providers
	_ = tracker.Record(ctx, "a1", 100, 50, 0.01, spending.RecordOptions{
		Model: "gpt-4o", Provider: "openrouter",
	})
	_ = tracker.Record(ctx, "a1", 200, 100, 0.02, spending.RecordOptions{
		Model: "gpt-4o", Provider: "openrouter",
	})
	_ = tracker.Record(ctx, "a1", 50, 25, 0.001, spending.RecordOptions{
		Model: "llama3", Provider: "ollama",
	})

	providers, err := tracker.GetProviderBreakdown(ctx, "a1", "day")
	if err != nil {
		t.Fatalf("GetProviderBreakdown: %v", err)
	}
	if len(providers) != 2 {
		t.Fatalf("got %d provider groups, want 2", len(providers))
	}

	// Verify ordering (by cost DESC): openrouter ($0.03), ollama ($0.001)
	if providers[0].Provider != "openrouter" {
		t.Errorf("first provider = %q, want %q", providers[0].Provider, "openrouter")
	}
	if providers[0].RequestCount != 2 {
		t.Errorf("openrouter RequestCount = %d, want 2", providers[0].RequestCount)
	}
	if providers[1].Provider != "ollama" {
		t.Errorf("second provider = %q, want %q", providers[1].Provider, "ollama")
	}
}
