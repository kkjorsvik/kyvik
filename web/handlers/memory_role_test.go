package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/audit"
	"github.com/kkjorsvik/kyvik/internal/auth"
	"github.com/kkjorsvik/kyvik/internal/core"
	"github.com/kkjorsvik/kyvik/internal/memory"
	"github.com/kkjorsvik/kyvik/internal/permissions"
	"github.com/kkjorsvik/kyvik/internal/spending"
	"github.com/kkjorsvik/kyvik/internal/store"
	"github.com/kkjorsvik/kyvik/internal/tools"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

type roleTestStore struct {
	agent types.AgentConfig
}

func (s *roleTestStore) CreateAgent(context.Context, types.AgentConfig) error { return nil }
func (s *roleTestStore) GetAgent(_ context.Context, id string) (*types.AgentConfig, error) {
	if id == s.agent.ID {
		return &s.agent, nil
	}
	return nil, types.ErrNotFound
}
func (s *roleTestStore) ListAgents(context.Context) ([]types.AgentConfig, error) { return nil, nil }
func (s *roleTestStore) UpdateAgent(context.Context, types.AgentConfig) error    { return nil }
func (s *roleTestStore) DeleteAgent(context.Context, string) error               { return nil }
func (s *roleTestStore) SetDesiredState(context.Context, string, types.DesiredState) error {
	return nil
}
func (s *roleTestStore) SetActualState(context.Context, string, types.AgentStatus, string) error {
	return nil
}
func (s *roleTestStore) InsertAuditEntry(context.Context, types.AuditEntry) error { return nil }
func (s *roleTestStore) InsertAuditEntries(context.Context, []types.AuditEntry) error {
	return nil
}
func (s *roleTestStore) ListAuditEntries(context.Context, audit.Filter) ([]types.AuditEntry, error) {
	return nil, nil
}
func (s *roleTestStore) InsertUsageRecord(context.Context, string, int64, int64, float64, string, string, string, string, string) error {
	return nil
}
func (s *roleTestStore) AggregateUsage(context.Context, string, string) (*spending.Summary, error) {
	return nil, nil
}
func (s *roleTestStore) AggregateSlotUsage(context.Context, string, string) ([]spending.SlotUsageSummary, error) {
	return nil, nil
}
func (s *roleTestStore) AggregateProviderUsage(context.Context, string, string) ([]spending.ProviderUsageSummary, error) {
	return nil, nil
}
func (s *roleTestStore) InsertSecurityEvent(context.Context, types.SecurityEvent) error { return nil }
func (s *roleTestStore) QuerySecurityEvents(context.Context, string, int) ([]types.SecurityEvent, error) {
	return nil, nil
}
func (s *roleTestStore) GetSystemState(context.Context, string) (string, error) { return "", nil }
func (s *roleTestStore) SetSystemState(context.Context, string, string) error   { return nil }
func (s *roleTestStore) AcknowledgeAlert(context.Context, string, string) error { return nil }
func (s *roleTestStore) IsAlertAcknowledged(context.Context, string, string) (bool, error) {
	return false, nil
}
func (s *roleTestStore) ListAcknowledgedAlerts(context.Context) (map[string]time.Time, error) {
	return nil, nil
}
func (s *roleTestStore) QueryAllSecurityEvents(context.Context, string, int) ([]types.SecurityEvent, error) {
	return nil, nil
}
func (s *roleTestStore) CreateSchedule(context.Context, types.Schedule) error { return nil }
func (s *roleTestStore) GetSchedule(context.Context, string) (*types.Schedule, error) {
	return nil, types.ErrNotFound
}
func (s *roleTestStore) UpdateSchedule(context.Context, types.Schedule) error { return nil }
func (s *roleTestStore) DeleteSchedule(context.Context, string) error         { return nil }
func (s *roleTestStore) ListSchedules(context.Context, string) ([]types.Schedule, error) {
	return nil, nil
}
func (s *roleTestStore) ListSchedulesByType(context.Context, string, string) ([]types.Schedule, error) {
	return nil, nil
}
func (s *roleTestStore) ListAllEnabledSchedules(context.Context) ([]types.Schedule, error) {
	return nil, nil
}
func (s *roleTestStore) DeleteSchedulesByAgent(context.Context, string) error { return nil }
func (s *roleTestStore) GrantSkill(context.Context, types.SkillGrant) error   { return nil }
func (s *roleTestStore) RevokeSkill(context.Context, string, string) error    { return nil }
func (s *roleTestStore) ListSkillGrants(context.Context, string) ([]types.SkillGrant, error) {
	return nil, nil
}
func (s *roleTestStore) DeleteSkillGrantsByAgent(context.Context, string) error { return nil }
func (s *roleTestStore) CreateTeam(context.Context, types.Team) error           { return nil }
func (s *roleTestStore) GetTeam(context.Context, string) (*types.Team, error) {
	return nil, types.ErrNotFound
}
func (s *roleTestStore) UpdateTeam(context.Context, types.Team) error    { return nil }
func (s *roleTestStore) DeleteTeam(context.Context, string) error        { return nil }
func (s *roleTestStore) ListTeams(context.Context) ([]types.Team, error) { return nil, nil }
func (s *roleTestStore) GetTeamByAgent(context.Context, string) (*types.Team, error) {
	return nil, types.ErrNotFound
}

func (s *roleTestStore) CreateOutboundWebhook(context.Context, types.OutboundWebhook) error {
	return nil
}
func (s *roleTestStore) GetOutboundWebhook(context.Context, string) (*types.OutboundWebhook, error) {
	return nil, types.ErrOutboundWebhookNotFound
}
func (s *roleTestStore) UpdateOutboundWebhook(context.Context, types.OutboundWebhook) error {
	return nil
}
func (s *roleTestStore) DeleteOutboundWebhook(context.Context, string) error { return nil }
func (s *roleTestStore) ListOutboundWebhooks(context.Context, string) ([]types.OutboundWebhook, error) {
	return nil, nil
}
func (s *roleTestStore) ListAllEnabledOutboundWebhooks(context.Context) ([]types.OutboundWebhook, error) {
	return nil, nil
}
func (s *roleTestStore) InsertWebhookDelivery(context.Context, types.WebhookDelivery) error {
	return nil
}
func (s *roleTestStore) ListWebhookDeliveries(context.Context, string, int) ([]types.WebhookDelivery, error) {
	return nil, nil
}
func (s *roleTestStore) ListPendingRetries(context.Context) ([]types.WebhookDelivery, error) {
	return nil, nil
}
func (s *roleTestStore) UpdateDeliveryStatus(context.Context, string, types.WebhookDeliveryStatus, int, string, string) error {
	return nil
}
func (s *roleTestStore) PruneWebhookDeliveries(context.Context, time.Duration) (int64, error) {
	return 0, nil
}

func (s *roleTestStore) CreateProvider(context.Context, types.ProviderRecord) error { return nil }
func (s *roleTestStore) GetProvider(context.Context, string) (*types.ProviderRecord, error) {
	return nil, types.ErrProviderNotFound
}
func (s *roleTestStore) UpdateProvider(context.Context, types.ProviderRecord) error { return nil }
func (s *roleTestStore) DeleteProvider(context.Context, string) error               { return nil }
func (s *roleTestStore) ListProviders(context.Context) ([]types.ProviderRecord, error) {
	return nil, nil
}

func (s *roleTestStore) CreateDiscordAuth(context.Context, types.DiscordAuthorization) error {
	return nil
}
func (s *roleTestStore) GetDiscordAuth(context.Context, string, string) (*types.DiscordAuthorization, error) {
	return nil, nil
}
func (s *roleTestStore) GetDiscordAuthByCode(context.Context, string) (*types.DiscordAuthorization, error) {
	return nil, nil
}
func (s *roleTestStore) UpdateDiscordAuth(context.Context, types.DiscordAuthorization) error {
	return nil
}
func (s *roleTestStore) ListDiscordAuths(context.Context, string) ([]types.DiscordAuthorization, error) {
	return nil, nil
}
func (s *roleTestStore) DeleteDiscordAuth(context.Context, string) error { return nil }

func (s *roleTestStore) Close() error { return nil }

var _ store.Store = (*roleTestStore)(nil)

type roleTestMemoryStore struct{}

func (m *roleTestMemoryStore) Create(context.Context, memory.Memory) (int64, error) { return 0, nil }
func (m *roleTestMemoryStore) Get(context.Context, int64) (memory.Memory, error) {
	return memory.Memory{}, nil
}
func (m *roleTestMemoryStore) Update(context.Context, memory.Memory) error { return nil }
func (m *roleTestMemoryStore) Delete(context.Context, int64) error         { return nil }
func (m *roleTestMemoryStore) List(context.Context, string, memory.ListOptions) ([]memory.Memory, error) {
	return nil, nil
}
func (m *roleTestMemoryStore) CountFiltered(context.Context, string, memory.ListOptions) (int, error) {
	return 0, nil
}
func (m *roleTestMemoryStore) ListPinned(context.Context, string) ([]memory.Memory, error) {
	return nil, nil
}
func (m *roleTestMemoryStore) Touch(context.Context, int64) error { return nil }
func (m *roleTestMemoryStore) SetEmbedding(context.Context, int64, []float32, string) error {
	return nil
}
func (m *roleTestMemoryStore) ListWithEmbeddings(context.Context, string) ([]memory.Memory, error) {
	return nil, nil
}
func (m *roleTestMemoryStore) GetUnembedded(context.Context, string) ([]memory.Memory, error) {
	return nil, nil
}
func (m *roleTestMemoryStore) GetAllUnembedded(context.Context, int) ([]memory.Memory, error) {
	return nil, nil
}
func (m *roleTestMemoryStore) ListRecent(context.Context, string, int) ([]memory.Memory, error) {
	return nil, nil
}
func (m *roleTestMemoryStore) Count(context.Context, string) (int, error)  { return 0, nil }
func (m *roleTestMemoryStore) DeleteByAgent(context.Context, string) error { return nil }
func (m *roleTestMemoryStore) Import(context.Context, string, []memory.Memory) (int, error) {
	return 0, nil
}
func (m *roleTestMemoryStore) CreateFromAgent(context.Context, string, string, string) (int64, error) {
	return 0, nil
}
func (m *roleTestMemoryStore) Archive(context.Context, int64) error   { return nil }
func (m *roleTestMemoryStore) Unarchive(context.Context, int64) error { return nil }
func (m *roleTestMemoryStore) ArchiveStale(context.Context, string, time.Duration) (int, error) {
	return 0, nil
}
func (m *roleTestMemoryStore) ListCandidates(_ context.Context, _ string) ([]memory.Memory, error) {
	return nil, nil
}
func (m *roleTestMemoryStore) CountCandidates(context.Context, string) (int, error) {
	return 0, nil
}
func (m *roleTestMemoryStore) PromoteCandidate(_ context.Context, _ int64) error { return nil }
func (m *roleTestMemoryStore) RejectCandidate(_ context.Context, _ int64) error  { return nil }
func (m *roleTestMemoryStore) EnforceCapAndStore(_ context.Context, _ memory.Memory, _ int) (int64, error) {
	return 0, nil
}

var _ memory.MemoryStore = (*roleTestMemoryStore)(nil)

type roleTestGate struct{}

func (g *roleTestGate) Check(context.Context, string, types.ToolCall) (*permissions.Decision, error) {
	return &permissions.Decision{Allowed: true}, nil
}
func (g *roleTestGate) GetAgentCapabilities(context.Context, string) ([]types.Capability, error) {
	return nil, nil
}
func (g *roleTestGate) LoadTemplate(context.Context, string) (*permissions.Template, error) {
	return nil, nil
}
func (g *roleTestGate) ListTemplates(context.Context) ([]permissions.Template, error) {
	return nil, nil
}
func (g *roleTestGate) AddOverride(context.Context, permissions.Override) error { return nil }
func (g *roleTestGate) RemoveOverride(context.Context, string, types.Capability) error {
	return nil
}
func (g *roleTestGate) ListOverrides(context.Context, string) ([]permissions.Override, error) {
	return nil, nil
}
func (g *roleTestGate) RemoveAllOverrides(context.Context, string) error { return nil }

var _ permissions.Gate = (*roleTestGate)(nil)

type roleTestAudit struct{}

func (a *roleTestAudit) Log(context.Context, types.AuditEntry) error { return nil }
func (a *roleTestAudit) Query(context.Context, audit.Filter) ([]types.AuditEntry, error) {
	return nil, nil
}
func (a *roleTestAudit) Stream(context.Context, string) (<-chan types.AuditEntry, error) {
	return nil, nil
}
func (a *roleTestAudit) Subscribe(context.Context, audit.SubscriptionFilter) (<-chan types.AuditEntry, error) {
	return nil, nil
}
func (a *roleTestAudit) Close() error { return nil }

var _ audit.Logger = (*roleTestAudit)(nil)

type roleTestTools struct{}

func (t *roleTestTools) Register(tools.Tool) error      { return nil }
func (t *roleTestTools) Get(string) (tools.Tool, error) { return nil, types.ErrNotFound }
func (t *roleTestTools) List() []tools.Declaration      { return nil }
func (t *roleTestTools) GetDeclaration(string) (*tools.Declaration, error) {
	return nil, types.ErrNotFound
}

var _ tools.Registry = (*roleTestTools)(nil)

type roleTestSpending struct{}

func (s *roleTestSpending) Record(context.Context, string, int64, int64, float64, spending.RecordOptions) error {
	return nil
}
func (s *roleTestSpending) CheckBudget(context.Context, string) (*spending.BudgetStatus, error) {
	return &spending.BudgetStatus{WithinBudget: true}, nil
}
func (s *roleTestSpending) GetSummary(context.Context, spending.Filter) (*spending.Summary, error) {
	return nil, nil
}
func (s *roleTestSpending) GetSlotBreakdown(context.Context, string, string) ([]spending.SlotUsageSummary, error) {
	return nil, nil
}
func (s *roleTestSpending) GetProviderBreakdown(context.Context, string, string) ([]spending.ProviderUsageSummary, error) {
	return nil, nil
}
func (s *roleTestSpending) SetGlobalLimit(context.Context, types.SpendingLimits) error { return nil }
func (s *roleTestSpending) SetAgentLimit(context.Context, string, types.SpendingLimits) error {
	return nil
}
func (s *roleTestSpending) GetDailyTimeSeries(context.Context, string, int) ([]spending.DailyUsage, error) {
	return nil, nil
}

var _ spending.Tracker = (*roleTestSpending)(nil)

func TestBulkDeleteRequiresAdmin(t *testing.T) {
	store := &roleTestStore{agent: types.AgentConfig{ID: "role-agent", Name: "Role Agent"}}
	st := core.New(store, &roleTestGate{}, nil, &roleTestAudit{}, &roleTestTools{}, &roleTestSpending{})
	st.SetMemory(&roleTestMemoryStore{})
	defer st.Shutdown(context.Background())

	h := New(st, nil)

	form := url.Values{}
	form.Set("action", "delete")
	form.Add("memory_id", "1")
	req := httptest.NewRequest(http.MethodPost, "/agents/role-agent/memories/bulk", strings.NewReader(form.Encode()))
	req.SetPathValue("id", "role-agent")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	ctx := context.WithValue(req.Context(), userCtxKey{}, dashboardUser{Role: auth.RoleManager, IsAdmin: false})
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	h.AgentMemoriesBulk(w, req)

	if w.Result().StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for manager bulk delete, got %d", w.Result().StatusCode)
	}
}

// Workflow store stubs (satisfy store.Store interface)
func (s *roleTestStore) CreateWorkflow(_ context.Context, _ types.Workflow) error              { return nil }
func (s *roleTestStore) GetWorkflow(_ context.Context, _ string) (*types.Workflow, error)      { return nil, types.ErrNotFound }
func (s *roleTestStore) GetWorkflowByName(_ context.Context, _, _ string) (*types.Workflow, error) { return nil, types.ErrNotFound }
func (s *roleTestStore) UpdateWorkflow(_ context.Context, _ types.Workflow) error              { return nil }
func (s *roleTestStore) DeleteWorkflow(_ context.Context, _ string) error                      { return nil }
func (s *roleTestStore) ListWorkflows(_ context.Context, _ string) ([]types.Workflow, error)   { return nil, nil }
func (s *roleTestStore) CreateWorkflowRun(_ context.Context, _ types.WorkflowRun) error        { return nil }
func (s *roleTestStore) GetWorkflowRun(_ context.Context, _ string) (*types.WorkflowRun, error) { return nil, types.ErrNotFound }
func (s *roleTestStore) UpdateWorkflowRun(_ context.Context, _ types.WorkflowRun) error        { return nil }
func (s *roleTestStore) ListWorkflowRuns(_ context.Context, _ string, _ int) ([]types.WorkflowRun, error) { return nil, nil }

func (s *roleTestStore) RegisterNode(_ context.Context, _ types.NodeInfo) error { return nil }
func (s *roleTestStore) UpdateHeartbeat(_ context.Context, _ string, _ types.NodeCapacity) error {
	return nil
}
func (s *roleTestStore) ListNodes(_ context.Context) ([]types.NodeInfo, error)  { return nil, nil }
func (s *roleTestStore) GetDeadNodes(_ context.Context, _ time.Duration) ([]types.NodeInfo, error) {
	return nil, nil
}
func (s *roleTestStore) SetNodeStatus(_ context.Context, _, _ string) error     { return nil }
func (s *roleTestStore) DeleteNode(_ context.Context, _ string) error           { return nil }
func (s *roleTestStore) AssignAgent(_ context.Context, _, _ string) error       { return nil }
func (s *roleTestStore) GetAssignment(_ context.Context, _ string) (*types.Assignment, error) {
	return nil, nil
}
func (s *roleTestStore) GetNodeAgents(_ context.Context, _ string) ([]types.Assignment, error) {
	return nil, nil
}
func (s *roleTestStore) GetOrphanedAgents(_ context.Context, _ string) ([]types.Assignment, error) {
	return nil, nil
}
func (s *roleTestStore) DeleteAssignment(_ context.Context, _ string) error { return nil }
