package core_test

import (
	"context"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/core"
	"github.com/kkjorsvik/kyvik/internal/models"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

func TestKillAgent_CancelsContext(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	config := testAgentConfig("agent-kill-ctx")

	if err := h.kyvik.StartAgent(ctx, config); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	waitForRunning(t, h, "agent-kill-ctx")

	if err := h.kyvik.KillAgent(ctx, "agent-kill-ctx"); err != nil {
		t.Fatalf("KillAgent: %v", err)
	}

	// Agent goroutine should exit (done channel closes).
	// Since kill doesn't wait, give it a moment.
	time.Sleep(50 * time.Millisecond)

	// Agent should no longer be running in memory.
	status, _ := h.kyvik.GetAgentStatus(ctx, "agent-kill-ctx")
	if status != types.AgentStatusKilled {
		t.Errorf("expected status killed, got %s", status)
	}
}

func TestKillAgent_SetsKilledState(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	config := testAgentConfig("agent-kill-state")

	if err := h.kyvik.StartAgent(ctx, config); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	waitForRunning(t, h, "agent-kill-state")

	if err := h.kyvik.KillAgent(ctx, "agent-kill-state"); err != nil {
		t.Fatalf("KillAgent: %v", err)
	}

	cfg, ok := h.store.getAgent("agent-kill-state")
	if !ok {
		t.Fatal("agent not in store")
	}
	if cfg.DesiredState != types.DesiredStateKilled {
		t.Errorf("expected desired_state killed, got %s", cfg.DesiredState)
	}
	if cfg.ActualState != types.AgentStatusKilled {
		t.Errorf("expected actual_state killed, got %s", cfg.ActualState)
	}
}

func TestKillAgent_AuditLogged(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	config := testAgentConfig("agent-kill-audit")

	if err := h.kyvik.StartAgent(ctx, config); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	waitForRunning(t, h, "agent-kill-audit")

	if err := h.kyvik.KillAgent(ctx, "agent-kill-audit"); err != nil {
		t.Fatalf("KillAgent: %v", err)
	}

	if !h.audit.hasAction("agent_killed") {
		t.Error("audit log missing 'agent_killed' action")
	}
}

func TestKillAgent_NotRunning(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	// Put an agent in the store but don't start it.
	h.store.CreateAgent(ctx, types.AgentConfig{
		ID:           "kill-not-running",
		Name:         "Not Running Agent",
		ModelConfig:  types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		DesiredState: types.DesiredStateStopped,
		ActualState:  types.AgentStatusStopped,
	})

	if err := h.kyvik.KillAgent(ctx, "kill-not-running"); err != nil {
		t.Fatalf("KillAgent: %v", err)
	}

	cfg, ok := h.store.getAgent("kill-not-running")
	if !ok {
		t.Fatal("agent not in store")
	}
	if cfg.DesiredState != types.DesiredStateKilled {
		t.Errorf("expected desired_state killed, got %s", cfg.DesiredState)
	}
	if cfg.ActualState != types.AgentStatusKilled {
		t.Errorf("expected actual_state killed, got %s", cfg.ActualState)
	}
}

func TestKillAgent_NotFound(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	err := h.kyvik.KillAgent(ctx, "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent agent")
	}
}

func TestKillAll_StopsAllRunning(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	for i := 1; i <= 3; i++ {
		id := "kill-all-" + string(rune('0'+i))
		if err := h.kyvik.StartAgent(ctx, testAgentConfig(id)); err != nil {
			t.Fatalf("StartAgent(%s): %v", id, err)
		}
		waitForRunning(t, h, id)
	}

	if err := h.kyvik.KillAll(ctx); err != nil {
		t.Fatalf("KillAll: %v", err)
	}

	// Allow goroutines to finish.
	time.Sleep(50 * time.Millisecond)

	for i := 1; i <= 3; i++ {
		id := "kill-all-" + string(rune('0'+i))
		cfg, ok := h.store.getAgent(id)
		if !ok {
			t.Fatalf("agent %s not in store", id)
		}
		if cfg.DesiredState != types.DesiredStateKilled {
			t.Errorf("agent %s: expected desired_state killed, got %s", id, cfg.DesiredState)
		}
	}
}

func TestKillAll_SetsEmergencyStopFlag(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	config := testAgentConfig("estop-agent")
	if err := h.kyvik.StartAgent(ctx, config); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	waitForRunning(t, h, "estop-agent")

	if err := h.kyvik.KillAll(ctx); err != nil {
		t.Fatalf("KillAll: %v", err)
	}

	if !h.kyvik.EmergencyStopActive() {
		t.Error("expected emergency stop to be active")
	}

	// New starts should be blocked.
	err := h.kyvik.StartAgent(ctx, testAgentConfig("blocked-agent"))
	if err == nil {
		t.Error("expected error starting agent during emergency stop")
	}

	// ResumeAgent should also be blocked.
	h.store.CreateAgent(ctx, types.AgentConfig{
		ID:           "blocked-resume",
		Name:         "Blocked Resume",
		ModelConfig:  types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		DesiredState: types.DesiredStateStopped,
		ActualState:  types.AgentStatusStopped,
	})
	err = h.kyvik.ResumeAgent(ctx, "blocked-resume")
	if err == nil {
		t.Error("expected error resuming agent during emergency stop")
	}

	// Clear should allow starts again.
	if err := h.kyvik.ClearEmergencyStop(ctx); err != nil {
		t.Fatalf("ClearEmergencyStop: %v", err)
	}

	if h.kyvik.EmergencyStopActive() {
		t.Error("expected emergency stop to be cleared")
	}
}

func TestKilledAgent_NotAutoStartedOnReconcile(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	// Pre-populate store with a killed agent.
	h.store.CreateAgent(ctx, types.AgentConfig{
		ID:           "reconcile-killed",
		Name:         "Killed Agent",
		ModelConfig:  types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:     "worker",
		DesiredState: types.DesiredStateKilled,
		ActualState:  types.AgentStatusKilled,
	})

	if err := h.kyvik.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// Should remain killed, not started.
	status, _ := h.kyvik.GetAgentStatus(ctx, "reconcile-killed")
	if status != types.AgentStatusKilled {
		t.Errorf("expected killed after reconcile, got %s", status)
	}
}

func TestStartKilledAgent_Works(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	config := testAgentConfig("agent-restart-killed")

	if err := h.kyvik.StartAgent(ctx, config); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	waitForRunning(t, h, "agent-restart-killed")

	// Kill it.
	if err := h.kyvik.KillAgent(ctx, "agent-restart-killed"); err != nil {
		t.Fatalf("KillAgent: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	// Resume should work.
	if err := h.kyvik.ResumeAgent(ctx, "agent-restart-killed"); err != nil {
		t.Fatalf("ResumeAgent after kill: %v", err)
	}
	waitForRunning(t, h, "agent-restart-killed")

	status, _ := h.kyvik.GetAgentStatus(ctx, "agent-restart-killed")
	if status != types.AgentStatusRunning {
		t.Errorf("expected running after resume, got %s", status)
	}

	_ = h.kyvik.StopAgent(ctx, "agent-restart-killed")
}

func TestKillAgent_DoesNotWait(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	// Use a provider that blocks indefinitely.
	h.provider.response = nil
	h.provider.err = nil
	blockingProv := &blockingProvider{name: "test-provider", unblock: make(chan struct{})}
	h.kyvik = core.New(h.store, &mockGate{}, nil, h.audit, &mockToolsRegistry{}, h.spending)
	h.kyvik.RegisterModel(blockingProv)

	config := testAgentConfig("agent-kill-nowait")
	if err := h.kyvik.StartAgent(ctx, config); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	waitForRunning(t, h, "agent-kill-nowait")

	// Send a message that will block the provider.
	_ = h.kyvik.SendMessage(ctx, "agent-kill-nowait", types.Message{
		AgentID: "agent-kill-nowait",
		Role:    "user",
		Content: "this will block",
	})

	// Give the message a moment to start processing.
	time.Sleep(20 * time.Millisecond)

	// KillAgent should return immediately, not wait for the blocked goroutine.
	done := make(chan error, 1)
	go func() {
		done <- h.kyvik.KillAgent(ctx, "agent-kill-nowait")
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("KillAgent: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("KillAgent did not return within 1 second (should be immediate)")
	}

	// Unblock the provider so the goroutine can exit.
	close(blockingProv.unblock)
}

// blockingProvider blocks on Complete until unblock channel is closed.
type blockingProvider struct {
	name    string
	unblock chan struct{}
}

func (m *blockingProvider) Complete(ctx context.Context, _ models.CompletionRequest) (*models.CompletionResponse, error) {
	select {
	case <-m.unblock:
		return &models.CompletionResponse{Content: "unblocked", Model: "test-model"}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (m *blockingProvider) Stream(_ context.Context, _ models.CompletionRequest) (<-chan models.StreamChunk, error) {
	return nil, nil
}

func (m *blockingProvider) ListModels(_ context.Context) ([]models.ModelInfo, error) {
	return nil, nil
}

func (m *blockingProvider) Name() string { return m.name }
