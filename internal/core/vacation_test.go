package core_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/core"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

func TestActivateVacationMode_StopsAllRunning(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	// Start 3 agents.
	for i := 1; i <= 3; i++ {
		id := agentID("vac-stop", i)
		if err := h.kyvik.StartAgent(ctx, testAgentConfig(id)); err != nil {
			t.Fatalf("StartAgent(%s): %v", id, err)
		}
		waitForRunning(t, h, id)
	}

	// Activate vacation mode.
	if err := h.kyvik.ActivateVacationMode(ctx, "admin", "going on holiday"); err != nil {
		t.Fatalf("ActivateVacationMode: %v", err)
	}

	// All agents should be stopped.
	for i := 1; i <= 3; i++ {
		id := agentID("vac-stop", i)
		status, _ := h.kyvik.GetAgentStatus(ctx, id)
		if status != types.AgentStatusStopped {
			t.Errorf("agent %s: expected stopped, got %s", id, status)
		}
	}
}

func TestActivateVacationMode_SetsFlag(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	if h.kyvik.VacationModeActive() {
		t.Fatal("vacation mode should not be active initially")
	}

	if err := h.kyvik.ActivateVacationMode(ctx, "admin", ""); err != nil {
		t.Fatalf("ActivateVacationMode: %v", err)
	}

	if !h.kyvik.VacationModeActive() {
		t.Error("vacation mode should be active after activation")
	}
}

func TestActivateVacationMode_BlocksNewStarts(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	if err := h.kyvik.ActivateVacationMode(ctx, "admin", ""); err != nil {
		t.Fatalf("ActivateVacationMode: %v", err)
	}

	// StartAgent should fail.
	err := h.kyvik.StartAgent(ctx, testAgentConfig("vac-blocked"))
	if !errors.Is(err, types.ErrVacationModeActive) {
		t.Errorf("expected ErrVacationModeActive, got %v", err)
	}

	// ResumeAgent should also fail (after creating an agent in the store).
	h.store.CreateAgent(ctx, types.AgentConfig{
		ID:           "vac-resume-blocked",
		Name:         "Blocked Agent",
		ModelConfig:  types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:     "worker",
		DesiredState: types.DesiredStateRunning,
		ActualState:  types.AgentStatusStopped,
	})
	err = h.kyvik.ResumeAgent(ctx, "vac-resume-blocked")
	if !errors.Is(err, types.ErrVacationModeActive) {
		t.Errorf("ResumeAgent: expected ErrVacationModeActive, got %v", err)
	}
}

func TestDeactivateVacationMode_ResumesAgents(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	// Start 2 agents.
	for i := 1; i <= 2; i++ {
		id := agentID("vac-resume", i)
		if err := h.kyvik.StartAgent(ctx, testAgentConfig(id)); err != nil {
			t.Fatalf("StartAgent(%s): %v", id, err)
		}
		waitForRunning(t, h, id)
	}

	// Activate vacation mode.
	if err := h.kyvik.ActivateVacationMode(ctx, "admin", ""); err != nil {
		t.Fatalf("ActivateVacationMode: %v", err)
	}

	// Deactivate vacation mode.
	if err := h.kyvik.DeactivateVacationMode(ctx); err != nil {
		t.Fatalf("DeactivateVacationMode: %v", err)
	}

	// Agents should be running again.
	for i := 1; i <= 2; i++ {
		id := agentID("vac-resume", i)
		waitForRunning(t, h, id)
		status, _ := h.kyvik.GetAgentStatus(ctx, id)
		if status != types.AgentStatusRunning {
			t.Errorf("agent %s: expected running, got %s", id, status)
		}
	}

	// Clean up.
	for i := 1; i <= 2; i++ {
		_ = h.kyvik.StopAgent(ctx, agentID("vac-resume", i))
	}
}

func TestDeactivateVacationMode_ClearsFlag(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	if err := h.kyvik.ActivateVacationMode(ctx, "admin", ""); err != nil {
		t.Fatalf("ActivateVacationMode: %v", err)
	}

	if err := h.kyvik.DeactivateVacationMode(ctx); err != nil {
		t.Fatalf("DeactivateVacationMode: %v", err)
	}

	if h.kyvik.VacationModeActive() {
		t.Error("vacation mode should not be active after deactivation")
	}
}

func TestVacationMode_AuditLogged(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	if err := h.kyvik.ActivateVacationMode(ctx, "admin", "test"); err != nil {
		t.Fatalf("ActivateVacationMode: %v", err)
	}
	if !h.audit.hasAction("vacation_mode_activated") {
		t.Error("audit log missing 'vacation_mode_activated' action")
	}

	if err := h.kyvik.DeactivateVacationMode(ctx); err != nil {
		t.Fatalf("DeactivateVacationMode: %v", err)
	}
	if !h.audit.hasAction("vacation_mode_deactivated") {
		t.Error("audit log missing 'vacation_mode_deactivated' action")
	}
}

func TestVacationMode_PersistsAcrossRestart(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	// Activate vacation mode — persists to store.
	if err := h.kyvik.ActivateVacationMode(ctx, "admin", "persist test"); err != nil {
		t.Fatalf("ActivateVacationMode: %v", err)
	}

	// Simulate restart: create new Kyvik with the same store.
	h2 := newTestHarnessWithStore(h.store)

	// Before loading, flag should be false.
	if h2.kyvik.VacationModeActive() {
		t.Fatal("vacation mode should not be active before LoadVacationState")
	}

	// Load vacation state from DB.
	if err := h2.kyvik.LoadVacationState(ctx); err != nil {
		t.Fatalf("LoadVacationState: %v", err)
	}

	if !h2.kyvik.VacationModeActive() {
		t.Error("vacation mode should be active after LoadVacationState")
	}
}

func TestReconcile_SkipsDuringVacation(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	// Pre-populate store with an agent that has desired=running.
	h.store.CreateAgent(ctx, types.AgentConfig{
		ID:           "vac-reconcile",
		Name:         "Vacation Reconcile Agent",
		ModelConfig:  types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:     "worker",
		DesiredState: types.DesiredStateRunning,
		ActualState:  types.AgentStatusStopped,
	})

	// Activate vacation mode.
	if err := h.kyvik.ActivateVacationMode(ctx, "admin", ""); err != nil {
		t.Fatalf("ActivateVacationMode: %v", err)
	}

	// Reconcile should NOT start the agent.
	if err := h.kyvik.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// Agent should still be stopped (not running).
	status, _ := h.kyvik.GetAgentStatus(ctx, "vac-reconcile")
	if status == types.AgentStatusRunning {
		t.Error("agent should not be running during vacation mode")
	}
}

// --- Test helpers ---

func agentID(prefix string, i int) string {
	return prefix + "-" + time.Now().Format("150405") + "-" + string(rune('0'+i))
}

// newTestHarnessWithStore creates a test harness using an existing mock store.
func newTestHarnessWithStore(s *mockStore) *testHarness {
	g := &mockGate{}
	al := &mockAuditLogger{}
	tr := &mockToolsRegistry{}
	sp := newMockSpendingTracker(true)
	prov := newMockProvider("test-provider")

	st := core.New(s, g, nil, al, tr, sp)
	st.RegisterModel(prov)

	return &testHarness{
		kyvik:    st,
		store:    s,
		provider: prov,
		audit:    al,
		spending: sp,
	}
}
