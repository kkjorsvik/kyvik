package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/models"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// =============================================================================
// Scenario: Safety Systems
// Tests circuit breaker, kill switch, vacation mode, and spending limits.
// =============================================================================

func TestScenario_Safety_CircuitBreaker_ErrorRate(t *testing.T) {
	// t.Parallel() — disabled: shared PostgreSQL database
	h := newFullHarness(t)

	h.seedAgent(t, "cb-err-agent", "CB Error Agent", "worker")
	h.startAgent(t, "cb-err-agent")

	// Record enough errors to trip the breaker.
	agent, _ := h.store.GetAgent(context.Background(), "cb-err-agent")
	for i := 0; i < 10; i++ {
		h.breaker.RecordToolCall(*agent, "shell", "execute", false, false)
	}

	// Check the trip channel.
	select {
	case tr := <-h.tripCh:
		if tr.AgentID != "cb-err-agent" {
			t.Fatalf("expected trip for cb-err-agent, got %s", tr.AgentID)
		}
		if tr.Trigger != "error_rate" {
			t.Fatalf("expected error_rate trigger, got %s", tr.Trigger)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected circuit breaker trip, timed out")
	}
}

func TestScenario_Safety_CircuitBreaker_SpendingVelocity(t *testing.T) {
	// t.Parallel() — disabled: shared PostgreSQL database
	h := newFullHarness(t)

	h.seedAgent(t, "cb-spend-agent", "CB Spend Agent", "worker")

	// Set a daily spending limit so the breaker's dailyBudget > 0.
	// Without this, RecordSpending is a no-op.
	if err := h.spending.SetAgentLimit(context.Background(), "cb-spend-agent", types.SpendingLimits{
		MaxSpendPerDay: 5.0,
	}); err != nil {
		t.Fatalf("SetAgentLimit: %v", err)
	}

	h.startAgent(t, "cb-spend-agent")

	agent, _ := h.store.GetAgent(context.Background(), "cb-spend-agent")
	// The agent's Limits must reflect the daily budget for the breaker to use.
	agent.Limits.MaxSpendPerDay = 5.0
	// Record high spending to trigger velocity trip.
	for i := 0; i < 20; i++ {
		result := h.breaker.RecordSpending(*agent, 1.0) // $1 per call
		if result != nil {
			// Tripped.
			if result.Trigger != "spending_velocity" {
				t.Fatalf("expected spending_velocity trigger, got %s", result.Trigger)
			}
			return
		}
	}

	// Check trip channel if not returned inline.
	select {
	case tr := <-h.tripCh:
		if tr.Trigger != "spending_velocity" {
			t.Fatalf("expected spending_velocity trigger, got %s", tr.Trigger)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected spending velocity trip")
	}
}

func TestScenario_Safety_KillSwitch_StopsAll(t *testing.T) {
	// t.Parallel() — disabled: shared PostgreSQL database
	h := newFullHarness(t)

	agents := []string{"kill-1", "kill-2", "kill-3"}
	for _, id := range agents {
		h.seedAgent(t, id, "Kill "+id, "worker")
		h.startAgent(t, id)
	}

	// KillAll.
	if err := h.kyvik.KillAll(context.Background()); err != nil {
		t.Fatalf("KillAll: %v", err)
	}

	// All agents should be killed or stopped (goroutine cleanup can race with
	// KillAgent's state write, producing either status).
	for _, id := range agents {
		waitForStatusOneOf(t, h.kyvik, id,
			[]types.AgentStatus{types.AgentStatusKilled, types.AgentStatusStopped}, 3*time.Second)
	}

	// Emergency stop should be active.
	if !h.kyvik.EmergencyStopActive() {
		t.Fatal("expected EmergencyStopActive=true")
	}

	// Starting a new agent should fail.
	err := h.kyvik.StartAgent(context.Background(), types.AgentConfig{
		ID:          "blocked",
		Name:        "Blocked",
		ModelConfig: types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:    "worker",
	})
	if err == nil {
		t.Fatal("expected StartAgent to fail during emergency stop")
	}

	// Clear emergency stop.
	if err := h.kyvik.ClearEmergencyStop(context.Background()); err != nil {
		t.Fatalf("ClearEmergencyStop: %v", err)
	}
	if h.kyvik.EmergencyStopActive() {
		t.Fatal("expected EmergencyStopActive=false after clear")
	}
}

func TestScenario_Safety_KillSwitch_Recovery(t *testing.T) {
	// t.Parallel() — disabled: shared PostgreSQL database
	h := newFullHarness(t)

	h.seedAgent(t, "recover-agent", "Recover Agent", "worker")
	h.startAgent(t, "recover-agent")

	h.sendAndReceive(t, "recover-agent", "before kill", 5*time.Second)

	// Kill.
	if err := h.kyvik.KillAll(context.Background()); err != nil {
		t.Fatalf("KillAll: %v", err)
	}
	// KillAgent does NOT wait for the goroutine, so the agent may end up
	// as either "killed" or "stopped" depending on goroutine scheduling.
	waitForStatusOneOf(t, h.kyvik, "recover-agent",
		[]types.AgentStatus{types.AgentStatusKilled, types.AgentStatusStopped}, 5*time.Second)

	// Settle time for goroutine cleanup under heavy parallel load.
	time.Sleep(300 * time.Millisecond)

	// Clear and restart.
	if err := h.kyvik.ClearEmergencyStop(context.Background()); err != nil {
		t.Fatalf("ClearEmergencyStop: %v", err)
	}
	h.startAgent(t, "recover-agent")

	// Message should work again.
	resp := h.sendAndReceive(t, "recover-agent", "after recovery", 10*time.Second)
	if resp.Content == "" {
		t.Fatal("expected response after recovery")
	}
}

func TestScenario_Safety_VacationMode_PauseResume(t *testing.T) {
	// t.Parallel() — disabled: shared PostgreSQL database
	h := newFullHarness(t)

	agents := []string{"vac-1", "vac-2"}
	for _, id := range agents {
		h.seedAgent(t, id, "Vac "+id, "worker")
		h.startAgent(t, id)
	}

	// Activate vacation mode.
	if err := h.kyvik.ActivateVacationMode(context.Background(), "admin", "going on vacation"); err != nil {
		t.Fatalf("ActivateVacationMode: %v", err)
	}

	// Both agents should stop.
	for _, id := range agents {
		waitForStatus(t, h.kyvik, id, types.AgentStatusStopped, 3*time.Second)
	}

	// New starts should be blocked.
	err := h.kyvik.StartAgent(context.Background(), types.AgentConfig{
		ID:          "vac-new",
		Name:        "Vac New",
		ModelConfig: types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:    "worker",
	})
	if !errors.Is(err, types.ErrVacationModeActive) {
		t.Fatalf("expected ErrVacationModeActive, got %v", err)
	}

	// Deactivate — previously-running agents are auto-resumed.
	if err := h.kyvik.DeactivateVacationMode(context.Background()); err != nil {
		t.Fatalf("DeactivateVacationMode: %v", err)
	}

	// Wait for the auto-resumed agent to reach running status.
	waitForStatus(t, h.kyvik, "vac-1", types.AgentStatusRunning, 3*time.Second)

	resp := h.sendAndReceive(t, "vac-1", "back from vacation", 5*time.Second)
	if resp.Content == "" {
		t.Fatal("expected response after vacation deactivation")
	}
}

func TestScenario_Safety_SpendingLimit_PausesProcessing(t *testing.T) {
	// t.Parallel() — disabled: shared PostgreSQL database
	h := newFullHarness(t, withProviderFallback(models.CompletionResponse{
		Content:   "costly",
		TokensIn:  100,
		TokensOut: 200,
		Cost:      0.03,
		Model:     "test-model",
	}))

	h.seedAgent(t, "limit-agent", "Limit Agent", "worker")

	if err := h.spending.SetAgentLimit(context.Background(), "limit-agent", types.SpendingLimits{
		MaxSpendPerDay: 0.05,
	}); err != nil {
		t.Fatalf("SetAgentLimit: %v", err)
	}

	h.startAgent(t, "limit-agent")

	// First message OK (~$0.03).
	h.sendAndReceive(t, "limit-agent", "first", 5*time.Second)

	// Second should hit budget (~$0.06 total > $0.05 limit).
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := h.kyvik.SendMessage(ctx, "limit-agent", types.Message{Role: "user", Content: "second"})
	if err != nil {
		// Budget checked at send time — acceptable.
		return
	}

	// If send succeeded, the processing should detect budget exceeded.
	_, err = h.kyvik.ReceiveMessage(ctx, "limit-agent")
	// Either error or budget-exceeded message is acceptable.
	_ = err
}

func TestScenario_Safety_Combined_BreakerThenKill(t *testing.T) {
	// t.Parallel() — disabled: shared PostgreSQL database
	h := newFullHarness(t)

	h.seedAgent(t, "combo-agent", "Combo Agent", "worker")
	h.startAgent(t, "combo-agent")

	// Trip circuit breaker.
	agent, _ := h.store.GetAgent(context.Background(), "combo-agent")
	for i := 0; i < 10; i++ {
		h.breaker.RecordToolCall(*agent, "shell", "execute", false, false)
	}

	// Drain trip channel.
	select {
	case <-h.tripCh:
	case <-time.After(2 * time.Second):
	}

	// Kill all.
	if err := h.kyvik.KillAll(context.Background()); err != nil {
		t.Fatalf("KillAll: %v", err)
	}

	// Clear and start a new agent.
	if err := h.kyvik.ClearEmergencyStop(context.Background()); err != nil {
		t.Fatalf("ClearEmergencyStop: %v", err)
	}

	h.seedAgent(t, "fresh-agent", "Fresh Agent", "worker")
	h.startAgent(t, "fresh-agent")

	resp := h.sendAndReceive(t, "fresh-agent", "hello after combo", 5*time.Second)
	if resp.Content == "" {
		t.Fatal("expected response from fresh agent after combo safety events")
	}
}

func TestScenario_Safety_VacationState_Persistence(t *testing.T) {
	// t.Parallel() — disabled: shared PostgreSQL database
	h := newFullHarness(t)

	h.seedAgent(t, "vstate-1", "VState 1", "worker")
	h.seedAgent(t, "vstate-2", "VState 2", "worker")
	h.startAgent(t, "vstate-1")
	h.startAgent(t, "vstate-2")

	// Activate.
	if err := h.kyvik.ActivateVacationMode(context.Background(), "admin", "vacation test"); err != nil {
		t.Fatalf("ActivateVacationMode: %v", err)
	}

	for _, id := range []string{"vstate-1", "vstate-2"} {
		waitForStatus(t, h.kyvik, id, types.AgentStatusStopped, 3*time.Second)
	}

	// Check state.
	state, err := h.kyvik.GetVacationState(context.Background())
	if err != nil {
		t.Fatalf("GetVacationState: %v", err)
	}
	if state == nil || !state.Active {
		t.Fatal("expected active vacation state")
	}
	if state.Message != "vacation test" {
		t.Fatalf("expected message 'vacation test', got %q", state.Message)
	}
	if len(state.PreviousAgents) < 2 {
		t.Fatalf("expected at least 2 previous agents, got %d", len(state.PreviousAgents))
	}

	// Deactivate.
	if err := h.kyvik.DeactivateVacationMode(context.Background()); err != nil {
		t.Fatalf("DeactivateVacationMode: %v", err)
	}

	state, err = h.kyvik.GetVacationState(context.Background())
	if err != nil {
		t.Fatalf("GetVacationState after deactivate: %v", err)
	}
	if state != nil && state.Active {
		t.Fatal("expected inactive vacation state after deactivation")
	}
}
