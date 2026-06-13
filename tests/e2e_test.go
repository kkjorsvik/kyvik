package tests

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/audit"
	"github.com/kkjorsvik/kyvik/internal/core"
	"github.com/kkjorsvik/kyvik/internal/models"
	"github.com/kkjorsvik/kyvik/internal/permissions"
	"github.com/kkjorsvik/kyvik/internal/spending"
	"github.com/kkjorsvik/kyvik/internal/testutil"
	"github.com/kkjorsvik/kyvik/internal/tools"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// --- Mock provider (only external dependency mocked) ---

type mockProvider struct {
	response *models.CompletionResponse
}

func (m *mockProvider) Name() string { return "mock" }
func (m *mockProvider) Complete(_ context.Context, _ models.CompletionRequest) (*models.CompletionResponse, error) {
	return m.response, nil
}
func (m *mockProvider) Stream(_ context.Context, _ models.CompletionRequest) (<-chan models.StreamChunk, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *mockProvider) ListModels(_ context.Context) ([]models.ModelInfo, error) {
	return nil, nil
}

// --- Noop stub for tools.Registry ---

type noopRegistry struct{}

func (n *noopRegistry) Register(_ tools.Tool) error                        { return nil }
func (n *noopRegistry) Get(_ string) (tools.Tool, error)                   { return nil, types.ErrNotFound }
func (n *noopRegistry) List() []tools.Declaration                          { return nil }
func (n *noopRegistry) GetDeclaration(_ string) (*tools.Declaration, error) { return nil, types.ErrNotFound }

// --- Helper ---

// waitForStatus polls GetAgentStatus until it matches the expected status or the deadline expires.
func waitForStatus(t *testing.T, st *core.Kyvik, agentID string, expected types.AgentStatus) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		status, err := st.GetAgentStatus(ctx, agentID)
		if err != nil {
			t.Fatalf("GetAgentStatus(%s): %v", agentID, err)
		}
		if status == expected {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("agent %s did not reach status %s (current: %s)", agentID, expected, status)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// --- E2E Test ---

func TestAgentLifecycleE2E(t *testing.T) {
	// 1. Setup: PostgreSQL DB with real subsystems
	tdb := testutil.RequirePostgres(t)
	store := tdb.Store

	auditLogger := audit.NewStoreLogger(store, 10)
	defer auditLogger.Close()
	spendingTracker := spending.NewStoreTracker(store, auditLogger, "mock-model")
	gate := permissions.NewStoreGate(store, auditLogger, "") // built-in templates only

	st := core.New(store, gate, nil, auditLogger, &noopRegistry{}, spendingTracker)

	provider := &mockProvider{
		response: &models.CompletionResponse{
			Content:   "Hello from mock",
			TokensIn:  10,
			TokensOut: 20,
			Cost:      0.001,
			Model:     "mock-model",
		},
	}
	st.RegisterModel(provider)

	ctx := context.Background()
	agentID := "e2e-agent-1"

	// 2. Create agent config
	config := types.AgentConfig{
		ID:           agentID,
		Name:         "E2E Test Agent",
		SystemPrompt: "You are a test agent.",
		ModelConfig: types.ModelConfig{
			Provider: "mock",
			Model:    "mock-model",
		},
		Template: "worker",
	}

	// 3. Start agent
	if err := st.StartAgent(ctx, config); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}

	// 4. Wait for running
	waitForStatus(t, st, agentID, types.AgentStatusRunning)

	// 5. Send message
	msg := types.Message{
		AgentID: agentID,
		Role:    "user",
		Content: "Hello",
	}
	if err := st.SendMessage(ctx, agentID, msg); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	// 6. Receive response
	recvCtx, recvCancel := context.WithTimeout(ctx, 5*time.Second)
	defer recvCancel()

	resp, err := st.ReceiveMessage(recvCtx, agentID)
	if err != nil {
		t.Fatalf("ReceiveMessage: %v", err)
	}
	if resp.Content != "Hello from mock" {
		t.Errorf("expected response content %q, got %q", "Hello from mock", resp.Content)
	}
	if resp.Role != "assistant" {
		t.Errorf("expected role %q, got %q", "assistant", resp.Role)
	}
	if resp.AgentID != agentID {
		t.Errorf("expected agent ID %q, got %q", agentID, resp.AgentID)
	}

	// 7. Verify audit log entries
	// Small delay to let async audit writes settle (the spending Record call
	// happens in the agent goroutine).
	time.Sleep(50 * time.Millisecond)
	auditLogger.Flush()

	entries, err := store.ListAuditEntries(ctx, audit.Filter{AgentID: agentID})
	if err != nil {
		t.Fatalf("ListAuditEntries: %v", err)
	}

	hasLifecycleStart := false
	hasSpendingRecord := false
	for _, e := range entries {
		if e.EventType == types.EventAgentLifecycle && e.Action == "start" {
			hasLifecycleStart = true
		}
		if e.EventType == types.EventSpending && e.Action == "record" {
			hasSpendingRecord = true
		}
	}
	if !hasLifecycleStart {
		t.Error("audit log missing agent_lifecycle/start entry")
	}
	if !hasSpendingRecord {
		t.Error("audit log missing spending/record entry")
	}

	// 8. Verify spending aggregation
	summary, err := store.AggregateUsage(ctx, agentID, "day")
	if err != nil {
		t.Fatalf("AggregateUsage: %v", err)
	}
	if summary.TotalTokens != 30 {
		t.Errorf("expected TotalTokens 30, got %d", summary.TotalTokens)
	}
	if summary.TotalCost != 0.001 {
		t.Errorf("expected TotalCost 0.001, got %f", summary.TotalCost)
	}
	if summary.RequestCount != 1 {
		t.Errorf("expected RequestCount 1, got %d", summary.RequestCount)
	}

	// 9. Stop agent
	if err := st.StopAgent(ctx, agentID); err != nil {
		t.Fatalf("StopAgent: %v", err)
	}

	// 10. Verify stopped
	status, err := st.GetAgentStatus(ctx, agentID)
	if err != nil {
		t.Fatalf("GetAgentStatus after stop: %v", err)
	}
	if status != types.AgentStatusStopped {
		t.Errorf("expected status %s, got %s", types.AgentStatusStopped, status)
	}

	// 11. Verify stop audit entry
	// Allow async audit entry to be queued before draining.
	time.Sleep(100 * time.Millisecond)
	auditLogger.Flush()
	entries, err = store.ListAuditEntries(ctx, audit.Filter{AgentID: agentID})
	if err != nil {
		t.Fatalf("ListAuditEntries after stop: %v", err)
	}
	hasLifecycleStop := false
	for _, e := range entries {
		if e.EventType == types.EventAgentLifecycle && e.Action == "stop" {
			hasLifecycleStop = true
		}
	}
	if !hasLifecycleStop {
		t.Error("audit log missing agent_lifecycle/stop entry")
	}

	// 12. Cleanup
	if err := st.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}
