package core_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/audit"
	"github.com/kkjorsvik/kyvik/internal/channels"
	"github.com/kkjorsvik/kyvik/internal/core"
	"github.com/kkjorsvik/kyvik/internal/ktp"
	"github.com/kkjorsvik/kyvik/internal/ktp/testtools"
	"github.com/kkjorsvik/kyvik/internal/models"
	"github.com/kkjorsvik/kyvik/internal/permissions"
	"github.com/kkjorsvik/kyvik/internal/spending"
	"github.com/kkjorsvik/kyvik/internal/tools"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// --- Mock implementations ---

type mockStore struct {
	mu          sync.Mutex
	agents      map[string]types.AgentConfig
	systemState map[string]string
	closed      bool
	createErr   error
}

func newMockStore() *mockStore {
	return &mockStore{
		agents:      make(map[string]types.AgentConfig),
		systemState: make(map[string]string),
	}
}

func (m *mockStore) CreateAgent(_ context.Context, config types.AgentConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.createErr != nil {
		return m.createErr
	}
	m.agents[config.ID] = config
	return nil
}

func (m *mockStore) GetAgent(_ context.Context, id string) (*types.AgentConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cfg, ok := m.agents[id]
	if !ok {
		return nil, types.ErrNotFound
	}
	return &cfg, nil
}

func (m *mockStore) ListAgents(_ context.Context) ([]types.AgentConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]types.AgentConfig, 0, len(m.agents))
	for _, cfg := range m.agents {
		out = append(out, cfg)
	}
	return out, nil
}

func (m *mockStore) UpdateAgent(_ context.Context, config types.AgentConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.agents[config.ID] = config
	return nil
}

func (m *mockStore) DeleteAgent(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.agents, id)
	return nil
}

func (m *mockStore) InsertAuditEntry(_ context.Context, _ types.AuditEntry) error     { return nil }
func (m *mockStore) InsertAuditEntries(_ context.Context, _ []types.AuditEntry) error { return nil }
func (m *mockStore) ListAuditEntries(_ context.Context, _ audit.Filter) ([]types.AuditEntry, error) {
	return nil, nil
}
func (m *mockStore) InsertUsageRecord(_ context.Context, _ string, _, _ int64, _ float64, _, _, _, _, _ string) error {
	return nil
}
func (m *mockStore) AggregateUsage(_ context.Context, _ string, _ string) (*spending.Summary, error) {
	return &spending.Summary{}, nil
}
func (m *mockStore) AggregateSlotUsage(_ context.Context, _, _ string) ([]spending.SlotUsageSummary, error) {
	return nil, nil
}
func (m *mockStore) AggregateProviderUsage(_ context.Context, _, _ string) ([]spending.ProviderUsageSummary, error) {
	return nil, nil
}
func (m *mockStore) SetDesiredState(_ context.Context, agentID string, state types.DesiredState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cfg, ok := m.agents[agentID]
	if !ok {
		return types.ErrNotFound
	}
	cfg.DesiredState = state
	m.agents[agentID] = cfg
	return nil
}

func (m *mockStore) SetActualState(_ context.Context, agentID string, state types.AgentStatus, lastError string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cfg, ok := m.agents[agentID]
	if !ok {
		return types.ErrNotFound
	}
	cfg.ActualState = state
	cfg.LastError = lastError
	m.agents[agentID] = cfg
	return nil
}

func (m *mockStore) InsertSecurityEvent(_ context.Context, _ types.SecurityEvent) error {
	return nil
}
func (m *mockStore) QuerySecurityEvents(_ context.Context, _ string, _ int) ([]types.SecurityEvent, error) {
	return nil, nil
}

func (m *mockStore) GetSystemState(_ context.Context, key string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.systemState[key], nil
}
func (m *mockStore) SetSystemState(_ context.Context, key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.systemState[key] = value
	return nil
}
func (m *mockStore) AcknowledgeAlert(_ context.Context, _, _ string) error { return nil }
func (m *mockStore) IsAlertAcknowledged(_ context.Context, _, _ string) (bool, error) {
	return false, nil
}
func (m *mockStore) ListAcknowledgedAlerts(_ context.Context) (map[string]time.Time, error) {
	return nil, nil
}
func (m *mockStore) QueryAllSecurityEvents(_ context.Context, _ string, _ int) ([]types.SecurityEvent, error) {
	return nil, nil
}

func (m *mockStore) CreateSchedule(_ context.Context, _ types.Schedule) error { return nil }
func (m *mockStore) GetSchedule(_ context.Context, _ string) (*types.Schedule, error) {
	return nil, types.ErrScheduleNotFound
}
func (m *mockStore) UpdateSchedule(_ context.Context, _ types.Schedule) error { return nil }
func (m *mockStore) DeleteSchedule(_ context.Context, _ string) error         { return nil }
func (m *mockStore) ListSchedules(_ context.Context, _ string) ([]types.Schedule, error) {
	return nil, nil
}
func (m *mockStore) ListSchedulesByType(_ context.Context, _, _ string) ([]types.Schedule, error) {
	return nil, nil
}
func (m *mockStore) ListAllEnabledSchedules(_ context.Context) ([]types.Schedule, error) {
	return nil, nil
}
func (m *mockStore) DeleteSchedulesByAgent(_ context.Context, _ string) error { return nil }

func (m *mockStore) GrantSkill(_ context.Context, _ types.SkillGrant) error { return nil }
func (m *mockStore) RevokeSkill(_ context.Context, _, _ string) error       { return nil }
func (m *mockStore) ListSkillGrants(_ context.Context, _ string) ([]types.SkillGrant, error) {
	return nil, nil
}
func (m *mockStore) DeleteSkillGrantsByAgent(_ context.Context, _ string) error { return nil }

func (m *mockStore) CreateTeam(_ context.Context, _ types.Team) error { return nil }
func (m *mockStore) GetTeam(_ context.Context, _ string) (*types.Team, error) {
	return nil, types.ErrTeamNotFound
}
func (m *mockStore) UpdateTeam(_ context.Context, _ types.Team) error  { return nil }
func (m *mockStore) DeleteTeam(_ context.Context, _ string) error      { return nil }
func (m *mockStore) ListTeams(_ context.Context) ([]types.Team, error) { return nil, nil }
func (m *mockStore) GetTeamByAgent(_ context.Context, _ string) (*types.Team, error) {
	return nil, types.ErrTeamNotFound
}

func (m *mockStore) CreateOutboundWebhook(_ context.Context, _ types.OutboundWebhook) error {
	return nil
}
func (m *mockStore) GetOutboundWebhook(_ context.Context, _ string) (*types.OutboundWebhook, error) {
	return nil, types.ErrOutboundWebhookNotFound
}
func (m *mockStore) UpdateOutboundWebhook(_ context.Context, _ types.OutboundWebhook) error {
	return nil
}
func (m *mockStore) DeleteOutboundWebhook(_ context.Context, _ string) error { return nil }
func (m *mockStore) ListOutboundWebhooks(_ context.Context, _ string) ([]types.OutboundWebhook, error) {
	return nil, nil
}
func (m *mockStore) ListAllEnabledOutboundWebhooks(_ context.Context) ([]types.OutboundWebhook, error) {
	return nil, nil
}
func (m *mockStore) InsertWebhookDelivery(_ context.Context, _ types.WebhookDelivery) error {
	return nil
}
func (m *mockStore) ListWebhookDeliveries(_ context.Context, _ string, _ int) ([]types.WebhookDelivery, error) {
	return nil, nil
}
func (m *mockStore) ListPendingRetries(_ context.Context) ([]types.WebhookDelivery, error) {
	return nil, nil
}
func (m *mockStore) UpdateDeliveryStatus(_ context.Context, _ string, _ types.WebhookDeliveryStatus, _ int, _, _ string) error {
	return nil
}
func (m *mockStore) PruneWebhookDeliveries(_ context.Context, _ time.Duration) (int64, error) {
	return 0, nil
}

func (m *mockStore) CreateProvider(context.Context, types.ProviderRecord) error { return nil }
func (m *mockStore) GetProvider(context.Context, string) (*types.ProviderRecord, error) {
	return nil, types.ErrProviderNotFound
}
func (m *mockStore) UpdateProvider(context.Context, types.ProviderRecord) error { return nil }
func (m *mockStore) DeleteProvider(context.Context, string) error               { return nil }
func (m *mockStore) ListProviders(context.Context) ([]types.ProviderRecord, error) {
	return nil, nil
}

func (m *mockStore) CreateDiscordAuth(_ context.Context, _ types.DiscordAuthorization) error {
	return nil
}
func (m *mockStore) GetDiscordAuth(_ context.Context, _, _ string) (*types.DiscordAuthorization, error) {
	return nil, nil
}
func (m *mockStore) GetDiscordAuthByCode(_ context.Context, _ string) (*types.DiscordAuthorization, error) {
	return nil, nil
}
func (m *mockStore) UpdateDiscordAuth(_ context.Context, _ types.DiscordAuthorization) error {
	return nil
}
func (m *mockStore) ListDiscordAuths(_ context.Context, _ string) ([]types.DiscordAuthorization, error) {
	return nil, nil
}
func (m *mockStore) DeleteDiscordAuth(_ context.Context, _ string) error { return nil }

func (m *mockStore) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

func (m *mockStore) isClosed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}

func (m *mockStore) hasAgent(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.agents[id]
	return ok
}

func (m *mockStore) getAgent(id string) (types.AgentConfig, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cfg, ok := m.agents[id]
	return cfg, ok
}

type mockProvider struct {
	name      string
	response  *models.CompletionResponse
	err       error
	mu        sync.Mutex
	callCount int
}

func newMockProvider(name string) *mockProvider {
	return &mockProvider{
		name: name,
		response: &models.CompletionResponse{
			Content:   "mock response",
			TokensIn:  10,
			TokensOut: 20,
			Cost:      0.001,
			Model:     "test-model",
		},
	}
}

func (m *mockProvider) Complete(_ context.Context, _ models.CompletionRequest) (*models.CompletionResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCount++
	if m.err != nil {
		return nil, m.err
	}
	return m.response, nil
}

func (m *mockProvider) Stream(_ context.Context, _ models.CompletionRequest) (<-chan models.StreamChunk, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockProvider) ListModels(_ context.Context) ([]models.ModelInfo, error) {
	return nil, nil
}

func (m *mockProvider) Name() string { return m.name }

func (m *mockProvider) getCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callCount
}

type mockGate struct{}

func (m *mockGate) Check(_ context.Context, _ string, _ types.ToolCall) (*permissions.Decision, error) {
	return &permissions.Decision{Allowed: true, Reason: "mock"}, nil
}
func (m *mockGate) GetAgentCapabilities(_ context.Context, _ string) ([]types.Capability, error) {
	return nil, nil
}
func (m *mockGate) LoadTemplate(_ context.Context, _ string) (*permissions.Template, error) {
	return nil, nil
}
func (m *mockGate) ListTemplates(_ context.Context) ([]permissions.Template, error) {
	return nil, nil
}
func (m *mockGate) AddOverride(_ context.Context, _ permissions.Override) error          { return nil }
func (m *mockGate) RemoveOverride(_ context.Context, _ string, _ types.Capability) error { return nil }
func (m *mockGate) ListOverrides(_ context.Context, _ string) ([]permissions.Override, error) {
	return nil, nil
}
func (m *mockGate) RemoveAllOverrides(_ context.Context, _ string) error { return nil }

type mockAuditLogger struct {
	mu      sync.Mutex
	entries []types.AuditEntry
}

func (m *mockAuditLogger) Log(_ context.Context, entry types.AuditEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, entry)
	return nil
}

func (m *mockAuditLogger) Query(_ context.Context, _ audit.Filter) ([]types.AuditEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.entries, nil
}

func (m *mockAuditLogger) Stream(_ context.Context, _ string) (<-chan types.AuditEntry, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockAuditLogger) Subscribe(_ context.Context, _ audit.SubscriptionFilter) (<-chan types.AuditEntry, error) {
	ch := make(chan types.AuditEntry)
	close(ch)
	return ch, nil
}

func (m *mockAuditLogger) Close() error { return nil }

func (m *mockAuditLogger) getEntries() []types.AuditEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]types.AuditEntry, len(m.entries))
	copy(cp, m.entries)
	return cp
}

func (m *mockAuditLogger) hasAction(action string) bool {
	for _, e := range m.getEntries() {
		if e.Action == action {
			return true
		}
	}
	return false
}

type mockSpendingTracker struct {
	mu           sync.Mutex
	withinBudget bool
	recorded     []spendingRecord
	checkCount   int
}

type spendingRecord struct {
	agentID   string
	tokensIn  int64
	tokensOut int64
	cost      float64
}

func newMockSpendingTracker(withinBudget bool) *mockSpendingTracker {
	return &mockSpendingTracker{withinBudget: withinBudget}
}

func (m *mockSpendingTracker) Record(_ context.Context, agentID string, tokensIn, tokensOut int64, cost float64, _ spending.RecordOptions) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recorded = append(m.recorded, spendingRecord{agentID, tokensIn, tokensOut, cost})
	return nil
}

func (m *mockSpendingTracker) CheckBudget(_ context.Context, _ string) (*spending.BudgetStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.checkCount++
	return &spending.BudgetStatus{WithinBudget: m.withinBudget}, nil
}

func (m *mockSpendingTracker) GetSummary(_ context.Context, _ spending.Filter) (*spending.Summary, error) {
	return &spending.Summary{}, nil
}

func (m *mockSpendingTracker) GetSlotBreakdown(_ context.Context, _, _ string) ([]spending.SlotUsageSummary, error) {
	return nil, nil
}

func (m *mockSpendingTracker) GetProviderBreakdown(_ context.Context, _, _ string) ([]spending.ProviderUsageSummary, error) {
	return nil, nil
}

func (m *mockSpendingTracker) SetGlobalLimit(_ context.Context, _ types.SpendingLimits) error {
	return nil
}

func (m *mockSpendingTracker) SetAgentLimit(_ context.Context, _ string, _ types.SpendingLimits) error {
	return nil
}
func (m *mockSpendingTracker) GetDailyTimeSeries(_ context.Context, _ string, _ int) ([]spending.DailyUsage, error) {
	return nil, nil
}

func (m *mockSpendingTracker) getRecords() []spendingRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]spendingRecord, len(m.recorded))
	copy(cp, m.recorded)
	return cp
}

func (m *mockSpendingTracker) getCheckCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.checkCount
}

type mockToolsRegistry struct{}

func (m *mockToolsRegistry) Register(_ tools.Tool) error      { return nil }
func (m *mockToolsRegistry) Get(_ string) (tools.Tool, error) { return nil, types.ErrNotFound }
func (m *mockToolsRegistry) List() []tools.Declaration        { return nil }
func (m *mockToolsRegistry) GetDeclaration(_ string) (*tools.Declaration, error) {
	return nil, types.ErrNotFound
}

type mockChannelAdapter struct {
	name   string
	closed bool
}

func (m *mockChannelAdapter) Send(_ context.Context, _ string, _ types.Message) error { return nil }
func (m *mockChannelAdapter) Receive(_ context.Context) (<-chan channels.IncomingMessage, error) {
	return nil, nil
}
func (m *mockChannelAdapter) ProvisionAgent(_ context.Context, _ types.AgentConfig) error { return nil }
func (m *mockChannelAdapter) DeprovisionAgent(_ context.Context, _ string) error          { return nil }
func (m *mockChannelAdapter) Name() string                                                { return m.name }
func (m *mockChannelAdapter) Close() error {
	m.closed = true
	return nil
}

// --- Test harness ---

type testHarness struct {
	kyvik    *core.Kyvik
	store    *mockStore
	provider *mockProvider
	audit    *mockAuditLogger
	spending *mockSpendingTracker
}

func newTestHarness() *testHarness {
	s := newMockStore()
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

func testAgentConfig(id string) types.AgentConfig {
	return types.AgentConfig{
		ID:           id,
		Name:         "Test Agent " + id,
		SystemPrompt: "You are a test agent.",
		ModelConfig: types.ModelConfig{
			Provider: "test-provider",
			Model:    "test-model",
		},
		Template: "worker",
	}
}

// waitForRunning polls until the agent status becomes running or the context expires.
func waitForRunning(t *testing.T, h *testHarness, agentID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		status, err := h.kyvik.GetAgentStatus(ctx, agentID)
		if err != nil {
			t.Fatalf("GetAgentStatus: %v", err)
		}
		if status == types.AgentStatusRunning {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("agent %s did not reach running status (current: %s)", agentID, status)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// --- Tests ---

func TestStartAgent(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	config := testAgentConfig("agent-1")

	if err := h.kyvik.StartAgent(ctx, config); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}

	// Wait for goroutine to reach running
	waitForRunning(t, h, "agent-1")

	// Verify status
	status, err := h.kyvik.GetAgentStatus(ctx, "agent-1")
	if err != nil {
		t.Fatalf("GetAgentStatus: %v", err)
	}
	if status != types.AgentStatusRunning {
		t.Errorf("expected status running, got %s", status)
	}

	// Verify persisted to store
	if !h.store.hasAgent("agent-1") {
		t.Error("agent not persisted to store")
	}

	// Verify audit log
	if !h.audit.hasAction("start") {
		t.Error("audit log missing 'start' action")
	}

	// Cleanup
	_ = h.kyvik.StopAgent(ctx, "agent-1")
}

func TestStartAgentValidation(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	// Empty ID
	err := h.kyvik.StartAgent(ctx, types.AgentConfig{
		ModelConfig: types.ModelConfig{Provider: "test-provider"},
	})
	if err == nil {
		t.Error("expected error for empty ID")
	}

	// Empty provider
	err = h.kyvik.StartAgent(ctx, types.AgentConfig{
		ID: "agent-1",
	})
	if err == nil {
		t.Error("expected error for empty provider")
	}

	// Unknown provider
	err = h.kyvik.StartAgent(ctx, types.AgentConfig{
		ID:          "agent-1",
		ModelConfig: types.ModelConfig{Provider: "nonexistent"},
	})
	if !errors.Is(err, types.ErrProviderUnavailable) {
		t.Errorf("expected ErrProviderUnavailable, got %v", err)
	}
}

func TestStartAgentDuplicate(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	config := testAgentConfig("agent-dup")

	if err := h.kyvik.StartAgent(ctx, config); err != nil {
		t.Fatalf("first StartAgent: %v", err)
	}
	waitForRunning(t, h, "agent-dup")

	err := h.kyvik.StartAgent(ctx, config)
	if !errors.Is(err, types.ErrAgentAlreadyRunning) {
		t.Errorf("expected ErrAgentAlreadyRunning, got %v", err)
	}

	_ = h.kyvik.StopAgent(ctx, "agent-dup")
}

func TestMessageFlow(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	config := testAgentConfig("agent-msg")

	if err := h.kyvik.StartAgent(ctx, config); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	waitForRunning(t, h, "agent-msg")

	// Send a message
	msg := types.Message{
		AgentID: "agent-msg",
		Role:    "user",
		Content: "hello",
	}
	if err := h.kyvik.SendMessage(ctx, "agent-msg", msg); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	// Receive the response with a timeout
	recvCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	resp, err := h.kyvik.ReceiveMessage(recvCtx, "agent-msg")
	if err != nil {
		t.Fatalf("ReceiveMessage: %v", err)
	}

	if resp.Role != "assistant" {
		t.Errorf("expected role assistant, got %s", resp.Role)
	}
	if resp.Content != "mock response" {
		t.Errorf("expected content 'mock response', got %q", resp.Content)
	}
	if resp.AgentID != "agent-msg" {
		t.Errorf("expected agent ID 'agent-msg', got %q", resp.AgentID)
	}

	_ = h.kyvik.StopAgent(ctx, "agent-msg")
}

func TestSpendingCheckedAndRecorded(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	config := testAgentConfig("agent-spend")

	if err := h.kyvik.StartAgent(ctx, config); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	waitForRunning(t, h, "agent-spend")

	// Send a message to trigger spending flow
	msg := types.Message{Role: "user", Content: "test"}
	if err := h.kyvik.SendMessage(ctx, "agent-spend", msg); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	recvCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := h.kyvik.ReceiveMessage(recvCtx, "agent-spend"); err != nil {
		t.Fatalf("ReceiveMessage: %v", err)
	}

	// Verify budget was checked
	if h.spending.getCheckCount() == 0 {
		t.Error("budget was not checked")
	}

	// Verify spending was recorded with correct token counts
	records := h.spending.getRecords()
	if len(records) == 0 {
		t.Fatal("no spending records")
	}
	rec := records[0]
	if rec.agentID != "agent-spend" {
		t.Errorf("expected agent ID 'agent-spend', got %q", rec.agentID)
	}
	if rec.tokensIn != 10 {
		t.Errorf("expected tokensIn 10, got %d", rec.tokensIn)
	}
	if rec.tokensOut != 20 {
		t.Errorf("expected tokensOut 20, got %d", rec.tokensOut)
	}
	if rec.cost != 0.001 {
		t.Errorf("expected cost 0.001, got %f", rec.cost)
	}

	_ = h.kyvik.StopAgent(ctx, "agent-spend")
}

func TestBudgetExceeded(t *testing.T) {
	h := newTestHarness()
	h.spending = newMockSpendingTracker(false)
	// Recreate kyvik with budget-exceeded tracker
	h.kyvik = core.New(h.store, &mockGate{}, nil, h.audit, &mockToolsRegistry{}, h.spending)
	h.kyvik.RegisterModel(h.provider)

	ctx := context.Background()
	config := testAgentConfig("agent-budget")

	if err := h.kyvik.StartAgent(ctx, config); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	waitForRunning(t, h, "agent-budget")

	msg := types.Message{Role: "user", Content: "should be rejected"}
	if err := h.kyvik.SendMessage(ctx, "agent-budget", msg); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	recvCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	resp, err := h.kyvik.ReceiveMessage(recvCtx, "agent-budget")
	if err != nil {
		t.Fatalf("ReceiveMessage: %v", err)
	}

	// Should get an error response containing budget exceeded
	if resp.Role != "assistant" {
		t.Errorf("expected role assistant, got %s", resp.Role)
	}
	if !containsSubstring(resp.Content, "budget exceeded") {
		t.Errorf("expected error about budget, got %q", resp.Content)
	}

	// Provider should never have been called
	if h.provider.getCallCount() != 0 {
		t.Errorf("provider should not have been called, but was called %d times", h.provider.getCallCount())
	}

	_ = h.kyvik.StopAgent(ctx, "agent-budget")
}

func TestStopAgent(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	config := testAgentConfig("agent-stop")

	if err := h.kyvik.StartAgent(ctx, config); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	waitForRunning(t, h, "agent-stop")

	if err := h.kyvik.StopAgent(ctx, "agent-stop"); err != nil {
		t.Fatalf("StopAgent: %v", err)
	}

	// Status should be stopped
	status, _ := h.kyvik.GetAgentStatus(ctx, "agent-stop")
	if status != types.AgentStatusStopped {
		t.Errorf("expected stopped status, got %s", status)
	}

	// Audit log should contain stop
	if !h.audit.hasAction("stop") {
		t.Error("audit log missing 'stop' action")
	}
}

func TestStopAgentNotRunning(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	err := h.kyvik.StopAgent(ctx, "nonexistent")
	if !errors.Is(err, types.ErrAgentNotRunning) {
		t.Errorf("expected ErrAgentNotRunning, got %v", err)
	}
}

func TestShutdown(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	// Start 3 agents
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("agent-shutdown-%d", i)
		if err := h.kyvik.StartAgent(ctx, testAgentConfig(id)); err != nil {
			t.Fatalf("StartAgent(%s): %v", id, err)
		}
		waitForRunning(t, h, id)
	}

	// Shutdown
	if err := h.kyvik.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	// All agents should be stopped
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("agent-shutdown-%d", i)
		status, _ := h.kyvik.GetAgentStatus(ctx, id)
		if status != types.AgentStatusStopped {
			t.Errorf("agent %s: expected stopped, got %s", id, status)
		}
	}

	// Store should be closed
	if !h.store.isClosed() {
		t.Error("store was not closed")
	}
}

func TestModelErrorRecovers(t *testing.T) {
	h := newTestHarness()
	h.provider.err = fmt.Errorf("model unavailable")

	ctx := context.Background()
	config := testAgentConfig("agent-err")

	if err := h.kyvik.StartAgent(ctx, config); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	waitForRunning(t, h, "agent-err")

	// Send message that will trigger a model error
	msg := types.Message{Role: "user", Content: "trigger error"}
	if err := h.kyvik.SendMessage(ctx, "agent-err", msg); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	recvCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	resp, err := h.kyvik.ReceiveMessage(recvCtx, "agent-err")
	if err != nil {
		t.Fatalf("ReceiveMessage: %v", err)
	}

	// Should get error message on outbox
	if !containsSubstring(resp.Content, "error") {
		t.Errorf("expected error in response, got %q", resp.Content)
	}

	// Agent should recover to running
	// Give a small window for status recovery
	time.Sleep(10 * time.Millisecond)
	status, _ := h.kyvik.GetAgentStatus(ctx, "agent-err")
	if status != types.AgentStatusRunning {
		t.Errorf("expected status running after recovery, got %s", status)
	}

	_ = h.kyvik.StopAgent(ctx, "agent-err")
}

// --- State & Reconciliation Tests ---

func TestStartAgentSetsState(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	config := testAgentConfig("agent-state-1")

	if err := h.kyvik.StartAgent(ctx, config); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	waitForRunning(t, h, "agent-state-1")

	cfg, ok := h.store.getAgent("agent-state-1")
	if !ok {
		t.Fatal("agent not in store")
	}
	if cfg.DesiredState != types.DesiredStateRunning {
		t.Errorf("expected desired_state running, got %s", cfg.DesiredState)
	}
	if cfg.ActualState != types.AgentStatusRunning {
		t.Errorf("expected actual_state running, got %s", cfg.ActualState)
	}

	_ = h.kyvik.StopAgent(ctx, "agent-state-1")
}

func TestStopAgentSetsDesiredStopped(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	config := testAgentConfig("agent-stop-state")

	if err := h.kyvik.StartAgent(ctx, config); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	waitForRunning(t, h, "agent-stop-state")

	if err := h.kyvik.StopAgent(ctx, "agent-stop-state"); err != nil {
		t.Fatalf("StopAgent: %v", err)
	}

	cfg, ok := h.store.getAgent("agent-stop-state")
	if !ok {
		t.Fatal("agent not in store")
	}
	if cfg.DesiredState != types.DesiredStateStopped {
		t.Errorf("expected desired_state stopped, got %s", cfg.DesiredState)
	}
	if cfg.ActualState != types.AgentStatusStopped {
		t.Errorf("expected actual_state stopped, got %s", cfg.ActualState)
	}
}

func TestShutdownPreservesDesiredState(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	for i := 1; i <= 2; i++ {
		id := fmt.Sprintf("agent-sd-%d", i)
		if err := h.kyvik.StartAgent(ctx, testAgentConfig(id)); err != nil {
			t.Fatalf("StartAgent(%s): %v", id, err)
		}
		waitForRunning(t, h, id)
	}

	if err := h.kyvik.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	for i := 1; i <= 2; i++ {
		id := fmt.Sprintf("agent-sd-%d", i)
		cfg, ok := h.store.getAgent(id)
		if !ok {
			t.Fatalf("agent %s not in store", id)
		}
		if cfg.DesiredState != types.DesiredStateRunning {
			t.Errorf("agent %s: expected desired_state running, got %s", id, cfg.DesiredState)
		}
		if cfg.ActualState != types.AgentStatusStopped {
			t.Errorf("agent %s: expected actual_state stopped, got %s", id, cfg.ActualState)
		}
	}
}

func TestReconcileStartsDesiredRunning(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	// Pre-populate store with an agent that has desired=running, actual=stopped
	h.store.CreateAgent(ctx, types.AgentConfig{
		ID:           "reconcile-1",
		Name:         "Reconcile Agent",
		ModelConfig:  types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:     "worker",
		DesiredState: types.DesiredStateRunning,
		ActualState:  types.AgentStatusStopped,
	})

	if err := h.kyvik.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	waitForRunning(t, h, "reconcile-1")

	status, _ := h.kyvik.GetAgentStatus(ctx, "reconcile-1")
	if status != types.AgentStatusRunning {
		t.Errorf("expected running after reconcile, got %s", status)
	}

	_ = h.kyvik.StopAgent(ctx, "reconcile-1")
}

func TestReconcileSkipsStopped(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	h.store.CreateAgent(ctx, types.AgentConfig{
		ID:           "reconcile-skip",
		Name:         "Stopped Agent",
		ModelConfig:  types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		DesiredState: types.DesiredStateStopped,
		ActualState:  types.AgentStatusStopped,
	})

	if err := h.kyvik.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	status, _ := h.kyvik.GetAgentStatus(ctx, "reconcile-skip")
	if status != types.AgentStatusStopped {
		t.Errorf("expected stopped, got %s", status)
	}
}

func TestReconcileQuarantined(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	h.store.CreateAgent(ctx, types.AgentConfig{
		ID:           "reconcile-q",
		Name:         "Quarantined Agent",
		ModelConfig:  types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		DesiredState: types.DesiredStateQuarantined,
		ActualState:  types.AgentStatusStopped,
	})

	if err := h.kyvik.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// Should not start a goroutine
	status, _ := h.kyvik.GetAgentStatus(ctx, "reconcile-q")
	if status != types.AgentStatusQuarantined {
		t.Errorf("expected quarantined, got %s", status)
	}
}

func TestReconcileResetsStaleStates(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	// An agent that claims to be running (stale after restart)
	h.store.CreateAgent(ctx, types.AgentConfig{
		ID:           "stale-1",
		Name:         "Stale Agent",
		ModelConfig:  types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		DesiredState: types.DesiredStateStopped,
		ActualState:  types.AgentStatusRunning,
	})

	if err := h.kyvik.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	cfg, _ := h.store.getAgent("stale-1")
	if cfg.ActualState != types.AgentStatusStopped {
		t.Errorf("expected stale actual_state reset to stopped, got %s", cfg.ActualState)
	}
}

func TestReconcileMaxRetries(t *testing.T) {
	h := newTestHarness()
	// Remove the provider so ResumeAgent always fails
	h.kyvik = core.New(h.store, &mockGate{}, nil, h.audit, &mockToolsRegistry{}, h.spending)
	// Don't register any provider — ResumeAgent will fail with "provider unavailable"

	ctx := context.Background()
	h.store.CreateAgent(ctx, types.AgentConfig{
		ID:           "retry-fail",
		Name:         "Retry Fail Agent",
		ModelConfig:  types.ModelConfig{Provider: "nonexistent", Model: "test-model"},
		DesiredState: types.DesiredStateRunning,
		ActualState:  types.AgentStatusStopped,
	})

	// Reconcile should fail after retries but not return error (it logs the individual failure)
	_ = h.kyvik.Reconcile(ctx)

	cfg, _ := h.store.getAgent("retry-fail")
	if cfg.ActualState != types.AgentStatusError {
		t.Errorf("expected actual_state error after max retries, got %s", cfg.ActualState)
	}
	if cfg.LastError == "" {
		t.Error("expected last_error to be set after max retries")
	}
}

func TestQuarantineAgent(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	config := testAgentConfig("agent-q")

	if err := h.kyvik.StartAgent(ctx, config); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	waitForRunning(t, h, "agent-q")

	if err := h.kyvik.QuarantineAgent(ctx, "agent-q"); err != nil {
		t.Fatalf("QuarantineAgent: %v", err)
	}

	cfg, _ := h.store.getAgent("agent-q")
	if cfg.DesiredState != types.DesiredStateQuarantined {
		t.Errorf("expected desired_state quarantined, got %s", cfg.DesiredState)
	}
	if cfg.ActualState != types.AgentStatusQuarantined {
		t.Errorf("expected actual_state quarantined, got %s", cfg.ActualState)
	}

	// Agent should no longer be in the agents map
	status, _ := h.kyvik.GetAgentStatus(ctx, "agent-q")
	if status != types.AgentStatusQuarantined {
		t.Errorf("expected status quarantined from store fallback, got %s", status)
	}
}

func TestGetAgentStatusFallsBackToStore(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	// Put an agent in the store but don't start it
	h.store.CreateAgent(ctx, types.AgentConfig{
		ID:          "store-only",
		Name:        "Store Only",
		ModelConfig: types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		ActualState: types.AgentStatusQuarantined,
	})

	status, _ := h.kyvik.GetAgentStatus(ctx, "store-only")
	if status != types.AgentStatusQuarantined {
		t.Errorf("expected quarantined from store fallback, got %s", status)
	}
}

// containsSubstring checks if s contains substr (case-sensitive).
func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstring(s, substr)
}

func searchSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// --- Tool-use loop mock providers ---

// toolCallMockProvider returns tool_calls on the first call and text on subsequent calls.
type toolCallMockProvider struct {
	mu        sync.Mutex
	callCount int
	name      string
}

func (m *toolCallMockProvider) Complete(_ context.Context, _ models.CompletionRequest) (*models.CompletionResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCount++
	if m.callCount == 1 {
		return &models.CompletionResponse{
			Content:    "",
			StopReason: "tool_use",
			ToolCalls: []models.ToolUse{{
				ID:         "call-1",
				Name:       "echo__echo",
				Parameters: map[string]any{"message": "hello"},
			}},
			TokensIn:  50,
			TokensOut: 30,
			Cost:      0.002,
			Model:     "test-model",
		}, nil
	}
	return &models.CompletionResponse{
		Content:    "Echo result: hello",
		StopReason: "end",
		TokensIn:   80,
		TokensOut:  40,
		Cost:       0.003,
		Model:      "test-model",
	}, nil
}

func (m *toolCallMockProvider) Stream(_ context.Context, _ models.CompletionRequest) (<-chan models.StreamChunk, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *toolCallMockProvider) ListModels(_ context.Context) ([]models.ModelInfo, error) {
	return nil, nil
}
func (m *toolCallMockProvider) Name() string { return m.name }
func (m *toolCallMockProvider) getCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callCount
}

// alwaysToolCallProvider always returns tool_calls (for max-iterations test).
type alwaysToolCallProvider struct {
	mu        sync.Mutex
	callCount int
	name      string
}

func (m *alwaysToolCallProvider) Complete(_ context.Context, _ models.CompletionRequest) (*models.CompletionResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCount++
	return &models.CompletionResponse{
		Content:    "calling tool...",
		StopReason: "tool_use",
		ToolCalls: []models.ToolUse{{
			ID:         fmt.Sprintf("call-%d", m.callCount),
			Name:       "echo__echo",
			Parameters: map[string]any{"message": "loop"},
		}},
		TokensIn:  10,
		TokensOut: 10,
		Cost:      0.001,
		Model:     "test-model",
	}, nil
}

func (m *alwaysToolCallProvider) Stream(_ context.Context, _ models.CompletionRequest) (<-chan models.StreamChunk, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *alwaysToolCallProvider) ListModels(_ context.Context) ([]models.ModelInfo, error) {
	return nil, nil
}
func (m *alwaysToolCallProvider) Name() string { return m.name }
func (m *alwaysToolCallProvider) getCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callCount
}

// mockKTPAgentStore satisfies ktp.AgentStore for the PermissionGate.
type mockKTPAgentStore struct {
	agents map[string]*types.AgentConfig
}

func (m *mockKTPAgentStore) GetAgent(_ context.Context, id string) (*types.AgentConfig, error) {
	agent, ok := m.agents[id]
	if !ok {
		return nil, types.ErrNotFound
	}
	return agent, nil
}

// mockKTPAuditLogger satisfies ktp.AuditLogger.
type mockKTPAuditLogger struct{}

func (m *mockKTPAuditLogger) LogToolPermission(_ context.Context, _ ktp.PermissionResult) error {
	return nil
}
func (m *mockKTPAuditLogger) LogToolExecution(_ context.Context, _ ktp.ToolRequest, _ *ktp.ToolResponse) error {
	return nil
}

// newKTPTestHarness creates a Kyvik with a real KTP registry, gate, and executor
// wired up with the EchoTool.
func newKTPTestHarness(provider models.Provider, spendTracker spending.Tracker) (*core.Kyvik, *ktp.Registry) {
	s := newMockStore()
	g := &mockGate{}
	al := &mockAuditLogger{}
	tr := &mockToolsRegistry{}

	k := core.New(s, g, nil, al, tr, spendTracker)
	k.RegisterModel(provider)

	// Set up KTP subsystem
	registry := ktp.NewRegistry()
	registry.Register(&testtools.EchoTool{})

	ktpAudit := &mockKTPAuditLogger{}
	agentStore := &mockKTPAgentStore{
		agents: map[string]*types.AgentConfig{
			"tool-agent": {ID: "tool-agent", Template: "worker"},
		},
	}
	gate := ktp.NewPermissionGate(agentStore, ktpAudit)
	executor := ktp.NewExecutor(registry, gate, ktpAudit, ktp.ExecutorConfig{})

	k.SetKTPRegistry(registry)
	k.SetKTPExecutor(executor)

	return k, registry
}

func TestHandleMessage_ToolUseLoop(t *testing.T) {
	provider := &toolCallMockProvider{name: "test-provider"}
	sp := newMockSpendingTracker(true)
	k, _ := newKTPTestHarness(provider, sp)

	ctx := context.Background()
	config := types.AgentConfig{
		ID:           "tool-agent",
		Name:         "Tool Agent",
		SystemPrompt: "You are a test agent.",
		ModelConfig: types.ModelConfig{
			Provider: "test-provider",
			Model:    "test-model",
		},
		Template: "worker",
	}

	if err := k.StartAgent(ctx, config); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	defer k.StopAgent(ctx, "tool-agent")
	waitForRunningKTP(t, k, "tool-agent")

	// Send a message
	if err := k.SendMessage(ctx, "tool-agent", types.Message{
		AgentID: "tool-agent",
		Role:    "user",
		Content: "echo hello",
	}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	recvCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	resp, err := k.ReceiveMessage(recvCtx, "tool-agent")
	if err != nil {
		t.Fatalf("ReceiveMessage: %v", err)
	}

	// Provider should have been called twice: tool_calls + final text
	if provider.getCallCount() != 2 {
		t.Errorf("expected provider called 2 times, got %d", provider.getCallCount())
	}

	// Final outbox message should have the text response
	if resp.Content != "Echo result: hello" {
		t.Errorf("expected content 'Echo result: hello', got %q", resp.Content)
	}

	// Spending should have been recorded for both iterations
	records := sp.getRecords()
	if len(records) < 2 {
		t.Errorf("expected at least 2 spending records, got %d", len(records))
	}
}

func TestHandleMessage_ToolCallError(t *testing.T) {
	// Use a provider that calls a non-existent tool action
	provider := &toolCallErrorProvider{name: "test-provider"}
	sp := newMockSpendingTracker(true)
	k, _ := newKTPTestHarness(provider, sp)

	ctx := context.Background()
	config := types.AgentConfig{
		ID:           "tool-agent",
		Name:         "Tool Agent",
		SystemPrompt: "You are a test agent.",
		ModelConfig:  types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:     "worker",
	}

	if err := k.StartAgent(ctx, config); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	defer k.StopAgent(ctx, "tool-agent")
	waitForRunningKTP(t, k, "tool-agent")

	if err := k.SendMessage(ctx, "tool-agent", types.Message{
		AgentID: "tool-agent",
		Role:    "user",
		Content: "try unknown tool",
	}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	recvCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	resp, err := k.ReceiveMessage(recvCtx, "tool-agent")
	if err != nil {
		t.Fatalf("ReceiveMessage: %v", err)
	}

	// Should still get a text response (provider returns text on call 2)
	if resp.Content != "Tool failed, here's a summary" {
		t.Errorf("expected final text response, got %q", resp.Content)
	}
}

func TestHandleMessage_MaxToolIterations(t *testing.T) {
	provider := &alwaysToolCallProvider{name: "test-provider"}
	sp := newMockSpendingTracker(true)
	k, _ := newKTPTestHarness(provider, sp)

	ctx := context.Background()
	config := types.AgentConfig{
		ID:           "tool-agent",
		Name:         "Tool Agent",
		SystemPrompt: "You are a test agent.",
		ModelConfig:  types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:     "worker",
	}

	if err := k.StartAgent(ctx, config); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	defer k.StopAgent(ctx, "tool-agent")
	waitForRunningKTP(t, k, "tool-agent")

	if err := k.SendMessage(ctx, "tool-agent", types.Message{
		AgentID: "tool-agent",
		Role:    "user",
		Content: "loop forever",
	}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	recvCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	resp, err := k.ReceiveMessage(recvCtx, "tool-agent")
	if err != nil {
		t.Fatalf("ReceiveMessage: %v", err)
	}

	// Loop should stop at max iterations (default 64 calls: iterations 0-63)
	callCount := provider.getCallCount()
	if callCount != 64 {
		t.Errorf("expected 64 provider calls (default max 64 iterations), got %d", callCount)
	}

	// Outbox should still get a message
	if resp.Role != "assistant" {
		t.Errorf("expected assistant role, got %s", resp.Role)
	}
}

func TestHandleMessage_NoExecutorIgnoresTools(t *testing.T) {
	// Provider returns tool_calls but no executor is configured
	provider := &toolCallMockProvider{name: "test-provider"}
	sp := newMockSpendingTracker(true)

	s := newMockStore()
	k := core.New(s, &mockGate{}, nil, &mockAuditLogger{}, &mockToolsRegistry{}, sp)
	k.RegisterModel(provider)
	// No SetKTPExecutor or SetKTPRegistry — tools disabled

	ctx := context.Background()
	config := types.AgentConfig{
		ID:           "no-exec-agent",
		Name:         "No Exec Agent",
		SystemPrompt: "You are a test agent.",
		ModelConfig:  types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:     "worker",
	}

	if err := k.StartAgent(ctx, config); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	defer k.StopAgent(ctx, "no-exec-agent")
	waitForRunningKTP(t, k, "no-exec-agent")

	if err := k.SendMessage(ctx, "no-exec-agent", types.Message{
		AgentID: "no-exec-agent",
		Role:    "user",
		Content: "hello",
	}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	recvCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	resp, err := k.ReceiveMessage(recvCtx, "no-exec-agent")
	if err != nil {
		t.Fatalf("ReceiveMessage: %v", err)
	}

	// Provider should only be called once — tool calls ignored
	if provider.getCallCount() != 1 {
		t.Errorf("expected 1 provider call (no executor), got %d", provider.getCallCount())
	}

	// Should still get a response (empty content from tool_use response)
	if resp.Role != "assistant" {
		t.Errorf("expected assistant role, got %s", resp.Role)
	}
}

func TestHandleMessage_BudgetExhaustedInLoop(t *testing.T) {
	provider := &alwaysToolCallProvider{name: "test-provider"}
	// Start within budget, then exhaust
	sp := &budgetExhaustedAfterNTracker{
		withinBudget: true,
		exhaustAfter: 2, // Allow initial check + 1 iteration, deny on 2nd loop check
	}
	k, _ := newKTPTestHarness(provider, sp)

	ctx := context.Background()
	config := types.AgentConfig{
		ID:           "tool-agent",
		Name:         "Tool Agent",
		SystemPrompt: "You are a test agent.",
		ModelConfig:  types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:     "worker",
	}

	if err := k.StartAgent(ctx, config); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	defer k.StopAgent(ctx, "tool-agent")
	waitForRunningKTP(t, k, "tool-agent")

	if err := k.SendMessage(ctx, "tool-agent", types.Message{
		AgentID: "tool-agent",
		Role:    "user",
		Content: "loop but budget runs out",
	}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	recvCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	resp, err := k.ReceiveMessage(recvCtx, "tool-agent")
	if err != nil {
		t.Fatalf("ReceiveMessage: %v", err)
	}

	// Loop should stop early due to budget
	callCount := provider.getCallCount()
	if callCount > 3 {
		t.Errorf("expected loop to stop early due to budget, but got %d calls", callCount)
	}

	// Outbox should still get a message
	if resp.Role != "assistant" {
		t.Errorf("expected assistant role, got %s", resp.Role)
	}
}

// toolCallErrorProvider returns a tool call to a non-existent tool on first call.
type toolCallErrorProvider struct {
	mu        sync.Mutex
	callCount int
	name      string
}

func (m *toolCallErrorProvider) Complete(_ context.Context, _ models.CompletionRequest) (*models.CompletionResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCount++
	if m.callCount == 1 {
		return &models.CompletionResponse{
			Content:    "",
			StopReason: "tool_use",
			ToolCalls: []models.ToolUse{{
				ID:         "call-err-1",
				Name:       "nonexistent__action",
				Parameters: map[string]any{},
			}},
			TokensIn:  50,
			TokensOut: 30,
			Cost:      0.002,
			Model:     "test-model",
		}, nil
	}
	return &models.CompletionResponse{
		Content:    "Tool failed, here's a summary",
		StopReason: "end",
		TokensIn:   60,
		TokensOut:  40,
		Cost:       0.003,
		Model:      "test-model",
	}, nil
}

func (m *toolCallErrorProvider) Stream(_ context.Context, _ models.CompletionRequest) (<-chan models.StreamChunk, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *toolCallErrorProvider) ListModels(_ context.Context) ([]models.ModelInfo, error) {
	return nil, nil
}
func (m *toolCallErrorProvider) Name() string { return m.name }

// budgetExhaustedAfterNTracker allows N budget checks before returning false.
type budgetExhaustedAfterNTracker struct {
	mu           sync.Mutex
	withinBudget bool
	exhaustAfter int
	checkCount   int
	recorded     []spendingRecord
}

func (m *budgetExhaustedAfterNTracker) Record(_ context.Context, agentID string, tokensIn, tokensOut int64, cost float64, _ spending.RecordOptions) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recorded = append(m.recorded, spendingRecord{agentID, tokensIn, tokensOut, cost})
	return nil
}

func (m *budgetExhaustedAfterNTracker) CheckBudget(_ context.Context, _ string) (*spending.BudgetStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.checkCount++
	within := m.checkCount <= m.exhaustAfter
	return &spending.BudgetStatus{WithinBudget: within}, nil
}

func (m *budgetExhaustedAfterNTracker) GetSummary(_ context.Context, _ spending.Filter) (*spending.Summary, error) {
	return &spending.Summary{}, nil
}
func (m *budgetExhaustedAfterNTracker) GetSlotBreakdown(_ context.Context, _, _ string) ([]spending.SlotUsageSummary, error) {
	return nil, nil
}
func (m *budgetExhaustedAfterNTracker) GetProviderBreakdown(_ context.Context, _, _ string) ([]spending.ProviderUsageSummary, error) {
	return nil, nil
}
func (m *budgetExhaustedAfterNTracker) SetGlobalLimit(_ context.Context, _ types.SpendingLimits) error {
	return nil
}
func (m *budgetExhaustedAfterNTracker) SetAgentLimit(_ context.Context, _ string, _ types.SpendingLimits) error {
	return nil
}
func (m *budgetExhaustedAfterNTracker) GetDailyTimeSeries(_ context.Context, _ string, _ int) ([]spending.DailyUsage, error) {
	return nil, nil
}

// --- DeepSeek DSML markup stripping test ---

// dsmlMockProvider returns content containing DeepSeek internal markup.
type dsmlMockProvider struct {
	mu        sync.Mutex
	callCount int
	name      string
}

func (m *dsmlMockProvider) Complete(_ context.Context, _ models.CompletionRequest) (*models.CompletionResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCount++
	return &models.CompletionResponse{
		Content:    "Here is some text.\n<｜DSML｜function_calls>\n{\"name\":\"file.list\"}\n</content>\nMore text.",
		StopReason: "end",
		TokensIn:   10,
		TokensOut:  20,
		Cost:       0.001,
		Model:      "test-model",
	}, nil
}

func (m *dsmlMockProvider) Stream(_ context.Context, _ models.CompletionRequest) (<-chan models.StreamChunk, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *dsmlMockProvider) ListModels(_ context.Context) ([]models.ModelInfo, error) {
	return nil, nil
}
func (m *dsmlMockProvider) Name() string { return m.name }

func TestHandleMessage_DSMLMarkupStripped(t *testing.T) {
	provider := &dsmlMockProvider{name: "test-provider"}
	sp := newMockSpendingTracker(true)

	s := newMockStore()
	k := core.New(s, &mockGate{}, nil, &mockAuditLogger{}, &mockToolsRegistry{}, sp)
	k.RegisterModel(provider)

	ctx := context.Background()
	config := types.AgentConfig{
		ID:           "dsml-agent",
		Name:         "DSML Agent",
		SystemPrompt: "You are a test agent.",
		ModelConfig:  types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:     "worker",
	}

	if err := k.StartAgent(ctx, config); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	defer k.StopAgent(ctx, "dsml-agent")
	waitForRunningKTP(t, k, "dsml-agent")

	if err := k.SendMessage(ctx, "dsml-agent", types.Message{
		AgentID: "dsml-agent",
		Role:    "user",
		Content: "hello",
	}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	recvCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	resp, err := k.ReceiveMessage(recvCtx, "dsml-agent")
	if err != nil {
		t.Fatalf("ReceiveMessage: %v", err)
	}

	// DSML markup should be stripped from the response
	if containsSubstring(resp.Content, "<｜DSML｜") {
		t.Errorf("response should not contain DSML markup, got %q", resp.Content)
	}
	if containsSubstring(resp.Content, "function_calls") {
		t.Errorf("response should not contain function_calls markup, got %q", resp.Content)
	}
	// Legitimate text should be preserved
	if !containsSubstring(resp.Content, "Here is some text.") {
		t.Errorf("legitimate text should be preserved, got %q", resp.Content)
	}
}

// --- 400 error retry without tools test ---

// tool400MockProvider returns a 400-like error on first call, success on retry.
type tool400MockProvider struct {
	mu        sync.Mutex
	callCount int
	name      string
}

func (m *tool400MockProvider) Complete(_ context.Context, req models.CompletionRequest) (*models.CompletionResponse, error) {
	m.mu.Lock()
	m.callCount++
	count := m.callCount
	m.mu.Unlock()

	if count == 1 && len(req.Tools) > 0 {
		return nil, fmt.Errorf("openrouter: Expected input to contain field: 'tool_call_id'. (status 400)")
	}
	return &models.CompletionResponse{
		Content:   "Fallback response without tools",
		TokensIn:  10,
		TokensOut: 20,
		Cost:      0.001,
		Model:     "test-model",
	}, nil
}

func (m *tool400MockProvider) Stream(_ context.Context, _ models.CompletionRequest) (<-chan models.StreamChunk, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *tool400MockProvider) ListModels(_ context.Context) ([]models.ModelInfo, error) {
	return nil, nil
}
func (m *tool400MockProvider) Name() string { return m.name }
func (m *tool400MockProvider) getCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callCount
}

func TestHandleMessage_400RetryWithoutTools(t *testing.T) {
	provider := &tool400MockProvider{name: "test-provider"}
	sp := newMockSpendingTracker(true)
	k, _ := newKTPTestHarness(provider, sp)

	ctx := context.Background()
	config := types.AgentConfig{
		ID:           "tool-agent",
		Name:         "Tool Agent",
		SystemPrompt: "You are a test agent.",
		ModelConfig:  types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:     "worker",
	}

	if err := k.StartAgent(ctx, config); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	defer k.StopAgent(ctx, "tool-agent")
	waitForRunningKTP(t, k, "tool-agent")

	if err := k.SendMessage(ctx, "tool-agent", types.Message{
		AgentID: "tool-agent",
		Role:    "user",
		Content: "try using a tool",
	}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	recvCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	resp, err := k.ReceiveMessage(recvCtx, "tool-agent")
	if err != nil {
		t.Fatalf("ReceiveMessage: %v", err)
	}

	// Provider should be called twice: first with tools (400), then without.
	if provider.getCallCount() != 2 {
		t.Errorf("expected 2 provider calls (400 retry), got %d", provider.getCallCount())
	}

	// Should get the fallback response
	if resp.Content != "Fallback response without tools" {
		t.Errorf("expected fallback response, got %q", resp.Content)
	}
}

// waitForRunningKTP polls until the agent status becomes running.
func waitForRunningKTP(t *testing.T, k *core.Kyvik, agentID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		status, err := k.GetAgentStatus(ctx, agentID)
		if err != nil {
			t.Fatalf("GetAgentStatus: %v", err)
		}
		if status == types.AgentStatusRunning {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("agent %s did not reach running status (current: %s)", agentID, status)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// --- Tool declaration capture tests ---

// capturingMockProvider records the CompletionRequest for later inspection.
type capturingMockProvider struct {
	mu       sync.Mutex
	name     string
	requests []models.CompletionRequest
}

func (m *capturingMockProvider) Complete(_ context.Context, req models.CompletionRequest) (*models.CompletionResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, req)
	return &models.CompletionResponse{
		Content:   "captured",
		TokensIn:  10,
		TokensOut: 20,
		Cost:      0.001,
		Model:     "test-model",
	}, nil
}

func (m *capturingMockProvider) Stream(_ context.Context, _ models.CompletionRequest) (<-chan models.StreamChunk, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *capturingMockProvider) ListModels(_ context.Context) ([]models.ModelInfo, error) {
	return nil, nil
}

func (m *capturingMockProvider) Name() string { return m.name }

func (m *capturingMockProvider) getRequests() []models.CompletionRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]models.CompletionRequest, len(m.requests))
	copy(cp, m.requests)
	return cp
}

func TestHandleMessage_ToolDeclarationsReachProvider(t *testing.T) {
	provider := &capturingMockProvider{name: "test-provider"}
	sp := newMockSpendingTracker(true)
	k, _ := newKTPTestHarness(provider, sp)

	ctx := context.Background()
	config := types.AgentConfig{
		ID:           "tool-agent",
		Name:         "Tool Agent",
		SystemPrompt: "You are a test agent.",
		ModelConfig:  types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:     "worker",
	}

	if err := k.StartAgent(ctx, config); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	defer k.StopAgent(ctx, "tool-agent")
	waitForRunningKTP(t, k, "tool-agent")

	if err := k.SendMessage(ctx, "tool-agent", types.Message{
		AgentID: "tool-agent",
		Role:    "user",
		Content: "test tool declarations",
	}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	recvCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if _, err := k.ReceiveMessage(recvCtx, "tool-agent"); err != nil {
		t.Fatalf("ReceiveMessage: %v", err)
	}

	// Inspect the captured request — it should contain tool definitions.
	requests := provider.getRequests()
	if len(requests) == 0 {
		t.Fatal("expected at least 1 provider request")
	}

	req := requests[0]
	if len(req.Tools) == 0 {
		t.Fatal("expected tool definitions in CompletionRequest.Tools, got 0")
	}

	// Verify the echo tool's action is present (echo__echo).
	found := false
	for _, tool := range req.Tools {
		if tool.Name == "echo__echo" {
			found = true
			break
		}
	}
	if !found {
		names := make([]string, len(req.Tools))
		for i, tool := range req.Tools {
			names[i] = tool.Name
		}
		t.Fatalf("expected 'echo__echo' in tool definitions, got: %v", names)
	}
}

func TestHandleMessage_EmptyTemplateNoTools(t *testing.T) {
	provider := &capturingMockProvider{name: "test-provider"}
	sp := newMockSpendingTracker(true)

	s := newMockStore()
	g := &mockGate{}
	al := &mockAuditLogger{}
	tr := &mockToolsRegistry{}

	k := core.New(s, g, nil, al, tr, sp)
	k.RegisterModel(provider)

	// Set up KTP registry with a tool, but agent has empty template.
	registry := ktp.NewRegistry()
	registry.Register(&testtools.EchoTool{})
	k.SetKTPRegistry(registry)

	ctx := context.Background()
	config := types.AgentConfig{
		ID:           "empty-template-agent",
		Name:         "Empty Template Agent",
		SystemPrompt: "You are a test agent.",
		ModelConfig:  types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:     "", // empty template → no tools
	}

	if err := k.StartAgent(ctx, config); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	defer k.StopAgent(ctx, "empty-template-agent")
	waitForRunningKTP(t, k, "empty-template-agent")

	if err := k.SendMessage(ctx, "empty-template-agent", types.Message{
		AgentID: "empty-template-agent",
		Role:    "user",
		Content: "test no tools",
	}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	recvCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if _, err := k.ReceiveMessage(recvCtx, "empty-template-agent"); err != nil {
		t.Fatalf("ReceiveMessage: %v", err)
	}

	// Inspect the captured request — should have 0 tools.
	requests := provider.getRequests()
	if len(requests) == 0 {
		t.Fatal("expected at least 1 provider request")
	}

	req := requests[0]
	if len(req.Tools) != 0 {
		names := make([]string, len(req.Tools))
		for i, tool := range req.Tools {
			names[i] = tool.Name
		}
		t.Fatalf("expected 0 tools for empty template agent, got %d: %v", len(req.Tools), names)
	}
}

// Workflow store stubs (satisfy store.Store interface)
func (m *mockStore) CreateWorkflow(_ context.Context, _ types.Workflow) error              { return nil }
func (m *mockStore) GetWorkflow(_ context.Context, _ string) (*types.Workflow, error)      { return nil, types.ErrNotFound }
func (m *mockStore) GetWorkflowByName(_ context.Context, _, _ string) (*types.Workflow, error) { return nil, types.ErrNotFound }
func (m *mockStore) UpdateWorkflow(_ context.Context, _ types.Workflow) error              { return nil }
func (m *mockStore) DeleteWorkflow(_ context.Context, _ string) error                      { return nil }
func (m *mockStore) ListWorkflows(_ context.Context, _ string) ([]types.Workflow, error)   { return nil, nil }
func (m *mockStore) CreateWorkflowRun(_ context.Context, _ types.WorkflowRun) error        { return nil }
func (m *mockStore) GetWorkflowRun(_ context.Context, _ string) (*types.WorkflowRun, error) { return nil, types.ErrNotFound }
func (m *mockStore) UpdateWorkflowRun(_ context.Context, _ types.WorkflowRun) error        { return nil }
func (m *mockStore) ListWorkflowRuns(_ context.Context, _ string, _ int) ([]types.WorkflowRun, error) { return nil, nil }

func (m *mockStore) RegisterNode(_ context.Context, _ types.NodeInfo) error { return nil }
func (m *mockStore) UpdateHeartbeat(_ context.Context, _ string, _ types.NodeCapacity) error {
	return nil
}
func (m *mockStore) ListNodes(_ context.Context) ([]types.NodeInfo, error)  { return nil, nil }
func (m *mockStore) GetDeadNodes(_ context.Context, _ time.Duration) ([]types.NodeInfo, error) {
	return nil, nil
}
func (m *mockStore) SetNodeStatus(_ context.Context, _, _ string) error     { return nil }
func (m *mockStore) DeleteNode(_ context.Context, _ string) error           { return nil }
func (m *mockStore) AssignAgent(_ context.Context, _, _ string) error       { return nil }
func (m *mockStore) GetAssignment(_ context.Context, _ string) (*types.Assignment, error) {
	return nil, nil
}
func (m *mockStore) GetNodeAgents(_ context.Context, _ string) ([]types.Assignment, error) {
	return nil, nil
}
func (m *mockStore) GetOrphanedAgents(_ context.Context, _ string) ([]types.Assignment, error) {
	return nil, nil
}
func (m *mockStore) DeleteAssignment(_ context.Context, _ string) error { return nil }
