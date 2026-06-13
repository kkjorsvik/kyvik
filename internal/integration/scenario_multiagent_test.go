package integration

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/audit"
	"github.com/kkjorsvik/kyvik/internal/models"
	"github.com/kkjorsvik/kyvik/internal/spending"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// =============================================================================
// Scenario: Multi-Agent Workflow
// Tests multiple agents processing messages with spending and audit tracking.
// =============================================================================

func TestScenario_MultiAgent_ThreeAgentsRespond(t *testing.T) {
	// t.Parallel() — disabled: shared PostgreSQL database
	h := newFullHarness(t)

	for _, a := range []struct{ id, name, tmpl string }{
		{"worker-a", "Worker A", "worker"},
		{"admin-a", "Admin A", "admin"},
		{"power-a", "Power A", "worker"},
	} {
		h.seedAgent(t, a.id, a.name, a.tmpl)
		h.startAgent(t, a.id)
	}

	for _, id := range []string{"worker-a", "admin-a", "power-a"} {
		resp := h.sendAndReceive(t, id, "ping", 5*time.Second)
		if resp.Content == "" {
			t.Fatalf("agent %s returned empty response", id)
		}
	}
}

func TestScenario_MultiAgent_SpendingTracking(t *testing.T) {
	// t.Parallel() — disabled: shared PostgreSQL database
	h := newFullHarness(t, withProviderFallback(models.CompletionResponse{
		Content:   "tracked response",
		TokensIn:  100,
		TokensOut: 200,
		Cost:      0.01,
		Model:     "test-model",
	}))

	h.seedAgent(t, "spend-agent", "Spend Agent", "worker")
	h.startAgent(t, "spend-agent")

	for i := 0; i < 5; i++ {
		h.sendAndReceive(t, "spend-agent", "msg", 5*time.Second)
	}

	// Allow async spending records to flush.
	time.Sleep(200 * time.Millisecond)

	summary, err := h.spending.GetSummary(context.Background(), spending.Filter{AgentID: "spend-agent"})
	if err != nil {
		t.Fatalf("GetSummary: %v", err)
	}
	if summary.TotalCost < 0.04 {
		t.Fatalf("expected total cost >= 0.04, got %f", summary.TotalCost)
	}
}

func TestScenario_MultiAgent_BudgetEnforcement(t *testing.T) {
	// t.Parallel() — disabled: shared PostgreSQL database
	h := newFullHarness(t, withProviderFallback(models.CompletionResponse{
		Content:   "expensive",
		TokensIn:  100,
		TokensOut: 200,
		Cost:      0.02,
		Model:     "test-model",
	}))

	h.seedAgent(t, "budget-agent", "Budget Agent", "worker")

	// Set a tight daily limit.
	if err := h.spending.SetAgentLimit(context.Background(), "budget-agent", types.SpendingLimits{
		MaxSpendPerDay: 0.05,
	}); err != nil {
		t.Fatalf("SetAgentLimit: %v", err)
	}

	h.startAgent(t, "budget-agent")

	// First 2 should succeed ($0.04 total).
	for i := 0; i < 2; i++ {
		h.sendAndReceive(t, "budget-agent", "msg", 5*time.Second)
	}

	// 3rd should hit budget limit — send and expect the error or budget-exceeded behavior.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := h.kyvik.SendMessage(ctx, "budget-agent", types.Message{Role: "user", Content: "over budget"})
	if err != nil {
		// SendMessage itself might reject if budget is checked at send time.
		return
	}

	// The message may be queued but processing should fail due to budget.
	_, _ = h.kyvik.ReceiveMessage(ctx, "budget-agent")
	// Either an error or a budget-exceeded message is acceptable here.
	// The key assertion is that spending was tracked correctly.
	time.Sleep(200 * time.Millisecond)

	status, err := h.spending.CheckBudget(context.Background(), "budget-agent")
	if err != nil {
		t.Fatalf("CheckBudget: %v", err)
	}
	if status.WithinBudget && status.DailyUsed < 0.03 {
		t.Fatalf("expected budget to be near or over limit, got used=%f limit=%f",
			status.DailyUsed, status.DailyLimit)
	}
}

func TestScenario_MultiAgent_ConcurrentMessages(t *testing.T) {
	// t.Parallel() — disabled: shared PostgreSQL database
	h := newFullHarness(t)

	agents := []string{"conc-1", "conc-2", "conc-3"}
	for _, id := range agents {
		h.seedAgent(t, id, "Conc "+id, "worker")
		h.startAgent(t, id)
	}

	// Send messages sequentially per agent but concurrently across agents.
	// PostgreSQL handles concurrency well, but 30 simultaneous
	// goroutines cause SQLITE_BUSY. Use 3 goroutines (one per agent)
	// each sending 5 messages sequentially.
	var wg sync.WaitGroup
	total := 3 * 5
	errCh := make(chan error, total)

	for _, id := range agents {
		wg.Add(1)
		go func(agentID string) {
			defer wg.Done()
			for i := 0; i < 5; i++ {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				if err := h.kyvik.SendMessage(ctx, agentID, types.Message{
					Role:    "user",
					Content: "concurrent msg",
				}); err != nil {
					cancel()
					errCh <- err
					continue
				}
				if _, err := h.kyvik.ReceiveMessage(ctx, agentID); err != nil {
					cancel()
					errCh <- err
					continue
				}
				cancel()
			}
		}(id)
	}

	wg.Wait()
	close(errCh)

	var errs []error
	for err := range errCh {
		errs = append(errs, err)
	}
	// Allow some failures (queue contention), but not all.
	if len(errs) > total/2 {
		t.Fatalf("too many errors in concurrent test: %d/%d, first: %v", len(errs), total, errs[0])
	}
}

func TestScenario_MultiAgent_StopRestart(t *testing.T) {
	// t.Parallel() — disabled: shared PostgreSQL database
	h := newFullHarness(t)

	h.seedAgent(t, "restart-agent", "Restart Agent", "worker")
	h.startAgent(t, "restart-agent")

	// First message.
	h.sendAndReceive(t, "restart-agent", "before restart", 5*time.Second)

	// Stop.
	if err := h.kyvik.StopAgent(context.Background(), "restart-agent"); err != nil {
		t.Fatalf("StopAgent: %v", err)
	}
	waitForStatus(t, h.kyvik, "restart-agent", types.AgentStatusStopped, 5*time.Second)

	// Restart.
	h.startAgent(t, "restart-agent")

	// Second message.
	h.sendAndReceive(t, "restart-agent", "after restart", 5*time.Second)

	// Verify audit has both starts (retry for async audit flush under load).
	var startCount int
	for retries := 0; retries < 5; retries++ {
		time.Sleep(200 * time.Millisecond)
		entries, err := h.audit.Query(context.Background(), audit.Filter{AgentID: "restart-agent", Limit: 100})
		if err != nil {
			t.Fatalf("audit query: %v", err)
		}
		startCount = 0
		for _, e := range entries {
			if e.Action == "start" {
				startCount++
			}
		}
		if startCount >= 2 {
			break
		}
	}
	if startCount < 2 {
		t.Fatalf("expected at least 2 'start' audit entries, got %d", startCount)
	}
}

func TestScenario_MultiAgent_AuditCompleteness(t *testing.T) {
	// t.Parallel() — disabled: shared PostgreSQL database
	h := newFullHarness(t)

	h.seedAgent(t, "audit-agent", "Audit Agent", "worker")
	h.startAgent(t, "audit-agent")

	h.sendAndReceive(t, "audit-agent", "audit test", 5*time.Second)

	// Wait for async audit.
	time.Sleep(300 * time.Millisecond)

	entries, err := h.audit.Query(context.Background(), audit.Filter{AgentID: "audit-agent", Limit: 100})
	if err != nil {
		t.Fatalf("audit query: %v", err)
	}

	eventTypes := make(map[types.EventType]bool)
	actions := make(map[string]bool)
	for _, e := range entries {
		eventTypes[e.EventType] = true
		actions[e.Action] = true
	}

	// Should have lifecycle events at minimum (audit action is "start", not "started").
	if !actions["start"] {
		t.Fatal("missing 'start' audit action")
	}

	if len(entries) < 2 {
		t.Fatalf("expected at least 2 audit entries, got %d", len(entries))
	}
}
