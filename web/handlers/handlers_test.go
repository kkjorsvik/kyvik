package handlers_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/audit"
	"github.com/kkjorsvik/kyvik/internal/authprovider/local"
	"github.com/kkjorsvik/kyvik/internal/channels"
	"github.com/kkjorsvik/kyvik/internal/channels/webui"
	"github.com/kkjorsvik/kyvik/internal/core"
	"github.com/kkjorsvik/kyvik/internal/memory"
	"github.com/kkjorsvik/kyvik/internal/models"
	"github.com/kkjorsvik/kyvik/internal/permissions"
	"github.com/kkjorsvik/kyvik/internal/spending"
	"github.com/kkjorsvik/kyvik/internal/testutil"
	"github.com/kkjorsvik/kyvik/internal/tools"
	"github.com/kkjorsvik/kyvik/internal/users"
	"github.com/kkjorsvik/kyvik/pkg/types"
	"github.com/kkjorsvik/kyvik/web"
)

// --- Mock implementations (matching core test patterns) ---

type mockStore struct {
	mu          sync.Mutex
	agents      map[string]types.AgentConfig
	systemState map[string]string
}

func newMockStore() *mockStore {
	return &mockStore{
		agents:      make(map[string]types.AgentConfig),
		systemState: map[string]string{"setup_complete": "true"},
	}
}

func (m *mockStore) CreateAgent(_ context.Context, config types.AgentConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
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

func (m *mockStore) CreateDiscordAuth(context.Context, types.DiscordAuthorization) error { return nil }
func (m *mockStore) GetDiscordAuth(context.Context, string, string) (*types.DiscordAuthorization, error) {
	return nil, nil
}
func (m *mockStore) GetDiscordAuthByCode(context.Context, string) (*types.DiscordAuthorization, error) {
	return nil, nil
}
func (m *mockStore) UpdateDiscordAuth(context.Context, types.DiscordAuthorization) error { return nil }
func (m *mockStore) ListDiscordAuths(context.Context, string) ([]types.DiscordAuthorization, error) {
	return nil, nil
}
func (m *mockStore) DeleteDiscordAuth(context.Context, string) error { return nil }

func (m *mockStore) Close() error { return nil }

type mockProvider struct {
	name string
}

func (m *mockProvider) Complete(_ context.Context, _ models.CompletionRequest) (*models.CompletionResponse, error) {
	return &models.CompletionResponse{Content: "mock", TokensIn: 10, TokensOut: 20, Cost: 0.001, Model: "test-model"}, nil
}
func (m *mockProvider) Stream(_ context.Context, _ models.CompletionRequest) (<-chan models.StreamChunk, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *mockProvider) ListModels(_ context.Context) ([]models.ModelInfo, error) {
	return []models.ModelInfo{{ID: "test-model", Name: "Test Model", Provider: "test-provider"}}, nil
}
func (m *mockProvider) Name() string { return m.name }
func (m *mockProvider) Embed(_ context.Context, input string) ([]float32, error) {
	switch {
	case strings.Contains(input, "alpha"):
		return []float32{1, 0, 0}, nil
	case strings.Contains(input, "beta"):
		return []float32{0, 1, 0}, nil
	default:
		return []float32{0, 0, 1}, nil
	}
}
func (m *mockProvider) EmbedBatch(_ context.Context, inputs []string) ([][]float32, error) {
	out := make([][]float32, 0, len(inputs))
	for _, input := range inputs {
		vec, _ := m.Embed(context.Background(), input)
		out = append(out, vec)
	}
	return out, nil
}
func (m *mockProvider) Model() string   { return "mock-embed" }
func (m *mockProvider) Dimensions() int { return 3 }

type mockGate struct{}

func (m *mockGate) Check(_ context.Context, _ string, _ types.ToolCall) (*permissions.Decision, error) {
	return &permissions.Decision{Allowed: true}, nil
}
func (m *mockGate) GetAgentCapabilities(_ context.Context, _ string) ([]types.Capability, error) {
	return nil, nil
}
func (m *mockGate) LoadTemplate(_ context.Context, _ string) (*permissions.Template, error) {
	return nil, nil
}
func (m *mockGate) ListTemplates(_ context.Context) ([]permissions.Template, error) {
	return []permissions.Template{
		permissions.ReaderTemplate,
		permissions.WorkerTemplate,
		permissions.AdminTemplate,
	}, nil
}
func (m *mockGate) AddOverride(_ context.Context, _ permissions.Override) error          { return nil }
func (m *mockGate) RemoveOverride(_ context.Context, _ string, _ types.Capability) error { return nil }
func (m *mockGate) ListOverrides(_ context.Context, _ string) ([]permissions.Override, error) {
	return nil, nil
}
func (m *mockGate) RemoveAllOverrides(_ context.Context, _ string) error { return nil }

type mockAuditLogger struct{}

func (m *mockAuditLogger) Log(_ context.Context, _ types.AuditEntry) error { return nil }
func (m *mockAuditLogger) Query(_ context.Context, _ audit.Filter) ([]types.AuditEntry, error) {
	return nil, nil
}
func (m *mockAuditLogger) Stream(_ context.Context, _ string) (<-chan types.AuditEntry, error) {
	return nil, nil
}
func (m *mockAuditLogger) Subscribe(_ context.Context, _ audit.SubscriptionFilter) (<-chan types.AuditEntry, error) {
	ch := make(chan types.AuditEntry)
	close(ch)
	return ch, nil
}
func (m *mockAuditLogger) Close() error { return nil }

type mockSpendingTracker struct{}

func (m *mockSpendingTracker) Record(_ context.Context, _ string, _, _ int64, _ float64, _ spending.RecordOptions) error {
	return nil
}
func (m *mockSpendingTracker) CheckBudget(_ context.Context, _ string) (*spending.BudgetStatus, error) {
	return &spending.BudgetStatus{WithinBudget: true}, nil
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

type mockToolsRegistry struct{}

func (m *mockToolsRegistry) Register(_ tools.Tool) error      { return nil }
func (m *mockToolsRegistry) Get(_ string) (tools.Tool, error) { return nil, types.ErrNotFound }
func (m *mockToolsRegistry) List() []tools.Declaration        { return nil }
func (m *mockToolsRegistry) GetDeclaration(_ string) (*tools.Declaration, error) {
	return nil, types.ErrNotFound
}

type mockChannelAdapter struct{ name string }

func (m *mockChannelAdapter) Send(_ context.Context, _ string, _ types.Message) error { return nil }
func (m *mockChannelAdapter) Receive(_ context.Context) (<-chan channels.IncomingMessage, error) {
	return nil, nil
}
func (m *mockChannelAdapter) ProvisionAgent(_ context.Context, _ types.AgentConfig) error { return nil }
func (m *mockChannelAdapter) DeprovisionAgent(_ context.Context, _ string) error          { return nil }
func (m *mockChannelAdapter) Name() string                                                { return m.name }
func (m *mockChannelAdapter) Close() error                                                { return nil }

// --- Mock memory store ---

type mockMemoryStore struct {
	mu       sync.Mutex
	memories map[int64]memory.Memory
	nextID   int64
}

func newMockMemoryStore() *mockMemoryStore {
	return &mockMemoryStore{memories: make(map[int64]memory.Memory), nextID: 1}
}

func (m *mockMemoryStore) Create(_ context.Context, mem memory.Memory) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := m.nextID
	m.nextID++
	mem.ID = id
	if !mem.Reviewed && mem.Source != memory.SourceAuto {
		mem.Reviewed = true
	}
	if mem.CreatedAt.IsZero() {
		mem.CreatedAt = time.Now()
	}
	if mem.AccessedAt.IsZero() {
		mem.AccessedAt = mem.CreatedAt
	}
	m.memories[id] = mem
	return id, nil
}

func (m *mockMemoryStore) Get(_ context.Context, id int64) (memory.Memory, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mem, ok := m.memories[id]
	if !ok {
		return memory.Memory{}, fmt.Errorf("memory %d: not found", id)
	}
	return mem, nil
}

func (m *mockMemoryStore) Update(_ context.Context, mem memory.Memory) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.memories[mem.ID]; !ok {
		return fmt.Errorf("memory %d: not found", mem.ID)
	}
	m.memories[mem.ID] = mem
	return nil
}

func (m *mockMemoryStore) Delete(_ context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.memories, id)
	return nil
}

func (m *mockMemoryStore) List(_ context.Context, agentID string, opts memory.ListOptions) ([]memory.Memory, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []memory.Memory
	for _, mem := range m.memories {
		if mem.AgentID != agentID {
			continue
		}
		if opts.ArchivedOnly && !mem.Archived {
			continue
		}
		if !opts.ArchivedOnly && mem.Archived {
			if opts.IncludeArchived == nil || !*opts.IncludeArchived {
				continue
			}
		}
		if opts.Category != "" && mem.Category != opts.Category {
			continue
		}
		if opts.Source != "" && mem.Source != opts.Source {
			continue
		}
		if opts.Reviewed != nil && mem.Reviewed != *opts.Reviewed {
			continue
		}
		result = append(result, mem)
	}
	return result, nil
}

func (m *mockMemoryStore) CountFiltered(_ context.Context, agentID string, opts memory.ListOptions) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, mem := range m.memories {
		if mem.AgentID != agentID {
			continue
		}
		if opts.ArchivedOnly && !mem.Archived {
			continue
		}
		if !opts.ArchivedOnly && mem.Archived {
			if opts.IncludeArchived == nil || !*opts.IncludeArchived {
				continue
			}
		}
		if opts.Category != "" && mem.Category != opts.Category {
			continue
		}
		if opts.Source != "" && mem.Source != opts.Source {
			continue
		}
		if opts.Reviewed != nil && mem.Reviewed != *opts.Reviewed {
			continue
		}
		count++
	}
	return count, nil
}

func (m *mockMemoryStore) ListPinned(_ context.Context, agentID string) ([]memory.Memory, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []memory.Memory
	for _, mem := range m.memories {
		if mem.AgentID == agentID && mem.Pinned && !mem.Archived {
			result = append(result, mem)
		}
	}
	return result, nil
}

func (m *mockMemoryStore) ListRecent(_ context.Context, agentID string, limit int) ([]memory.Memory, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []memory.Memory
	for _, mem := range m.memories {
		if mem.AgentID == agentID && !mem.Archived {
			result = append(result, mem)
		}
	}
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (m *mockMemoryStore) Touch(_ context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if mem, ok := m.memories[id]; ok {
		mem.AccessCount++
		mem.AccessedAt = time.Now()
		m.memories[id] = mem
	}
	return nil
}

func (m *mockMemoryStore) SetEmbedding(_ context.Context, id int64, emb []float32, model string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if mem, ok := m.memories[id]; ok {
		mem.Embedding = emb
		mem.EmbeddingModel = model
		m.memories[id] = mem
	}
	return nil
}
func (m *mockMemoryStore) ListWithEmbeddings(_ context.Context, agentID string) ([]memory.Memory, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []memory.Memory
	for _, mem := range m.memories {
		if mem.AgentID == agentID && !mem.Archived && len(mem.Embedding) > 0 {
			result = append(result, mem)
		}
	}
	return result, nil
}
func (m *mockMemoryStore) GetUnembedded(_ context.Context, _ string) ([]memory.Memory, error) {
	return nil, nil
}
func (m *mockMemoryStore) GetAllUnembedded(_ context.Context, _ int) ([]memory.Memory, error) {
	return nil, nil
}

func (m *mockMemoryStore) Count(_ context.Context, agentID string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, mem := range m.memories {
		if mem.AgentID == agentID && !mem.Archived {
			count++
		}
	}
	return count, nil
}

func (m *mockMemoryStore) DeleteByAgent(_ context.Context, agentID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, mem := range m.memories {
		if mem.AgentID == agentID {
			delete(m.memories, id)
		}
	}
	return nil
}

func (m *mockMemoryStore) Import(_ context.Context, agentID string, memories []memory.Memory) (int, error) {
	count := 0
	for _, mem := range memories {
		mem.AgentID = agentID
		if _, err := m.Create(context.Background(), mem); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func (m *mockMemoryStore) CreateFromAgent(ctx context.Context, agentID, category, content string) (int64, error) {
	return m.Create(ctx, memory.Memory{
		AgentID:        agentID,
		Category:       category,
		Content:        content,
		Source:         memory.SourceAgent,
		RelevanceScore: 0.5,
	})
}

func (m *mockMemoryStore) Archive(_ context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	mem, ok := m.memories[id]
	if !ok {
		return fmt.Errorf("memory %d: not found", id)
	}
	mem.Archived = true
	m.memories[id] = mem
	return nil
}

func (m *mockMemoryStore) Unarchive(_ context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	mem, ok := m.memories[id]
	if !ok {
		return fmt.Errorf("memory %d: not found", id)
	}
	mem.Archived = false
	m.memories[id] = mem
	return nil
}

func (m *mockMemoryStore) ArchiveStale(_ context.Context, agentID string, staleAfter time.Duration) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cutoff := time.Now().Add(-staleAfter)
	count := 0
	for id, mem := range m.memories {
		if mem.AgentID == agentID && !mem.Pinned && !mem.Archived && mem.AccessedAt.Before(cutoff) {
			mem.Archived = true
			m.memories[id] = mem
			count++
		}
	}
	return count, nil
}

func (m *mockMemoryStore) ListCandidates(_ context.Context, _ string) ([]memory.Memory, error) {
	return nil, nil
}
func (m *mockMemoryStore) CountCandidates(_ context.Context, _ string) (int, error) { return 0, nil }
func (m *mockMemoryStore) PromoteCandidate(_ context.Context, _ int64) error        { return nil }
func (m *mockMemoryStore) RejectCandidate(_ context.Context, _ int64) error         { return nil }
func (m *mockMemoryStore) EnforceCapAndStore(_ context.Context, _ memory.Memory, _ int) (int64, error) {
	return 0, nil
}

// --- Test harness ---

type testServer struct {
	server   *httptest.Server
	kyvik    *core.Kyvik
	store    *mockStore
	memStore *mockMemoryStore
	webui    *webui.Adapter
	client   *http.Client
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()
	return newTestServerWithMemory(t, nil)
}

func newTestServerWithMemory(t *testing.T, ms *mockMemoryStore) *testServer {
	t.Helper()

	s := newMockStore()
	st := core.New(s, &mockGate{}, nil, &mockAuditLogger{}, &mockToolsRegistry{}, &mockSpendingTracker{})
	st.RegisterModel(&mockProvider{name: "test-provider"})

	if ms != nil {
		st.SetMemory(ms)
	}

	wa := webui.New()
	st.RegisterChannel(wa)

	// Set up DB-backed auth provider for login tests.
	tdb := testutil.RequirePostgres(t)
	us := users.New(tdb.Store, users.AuthConfig{SessionTTL: time.Hour, MaxSessionsPerUser: 3})
	if _, _, err := us.BootstrapAdminIfEmpty(context.Background(), "admin", "secret"); err != nil {
		t.Fatalf("BootstrapAdminIfEmpty: %v", err)
	}
	authProvider := local.New(us)

	handler := web.SetupRoutes(st, web.WithWebUI(wa), web.WithAuthProvider(authProvider))

	srv := newHTTPServer(t, handler)

	// Client that doesn't follow redirects automatically
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	t.Cleanup(func() {
		srv.Close()
		st.Shutdown(context.Background())
	})

	return &testServer{
		server:   srv,
		kyvik:    st,
		store:    s,
		memStore: ms,
		webui:    wa,
		client:   client,
	}
}

func newHTTPServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("listen not permitted: %v", err)
	}
	server := &httptest.Server{
		Listener: listener,
		Config:   &http.Server{Handler: handler},
	}
	server.Start()
	return server
}

// login performs a login and returns the session cookie.
func (ts *testServer) login(t *testing.T) *http.Cookie {
	t.Helper()

	form := url.Values{}
	form.Set("username", "admin")
	form.Set("password", "secret")

	resp, err := ts.client.PostForm(ts.server.URL+"/login", form)
	if err != nil {
		t.Fatalf("login request failed: %v", err)
	}
	defer resp.Body.Close()

	for _, c := range resp.Cookies() {
		if c.Name == "kyvik_session" {
			return c
		}
	}
	t.Fatal("no session cookie after login")
	return nil
}

// authedGet performs an authenticated GET request.
func (ts *testServer) authedGet(t *testing.T, path string, cookie *http.Cookie) *http.Response {
	t.Helper()

	req, _ := http.NewRequest("GET", ts.server.URL+path, nil)
	req.AddCookie(cookie)
	resp, err := ts.client.Do(req)
	if err != nil {
		t.Fatalf("GET %s failed: %v", path, err)
	}
	return resp
}

// authedPost performs an authenticated POST form request.
func (ts *testServer) authedPost(t *testing.T, path string, cookie *http.Cookie, form url.Values, htmx bool) *http.Response {
	t.Helper()

	req, _ := http.NewRequest("POST", ts.server.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	resp, err := ts.client.Do(req)
	if err != nil {
		t.Fatalf("POST %s failed: %v", path, err)
	}
	return resp
}

// --- Tests ---

func TestLoginPage(t *testing.T) {
	ts := newTestServer(t)

	resp, err := ts.client.Get(ts.server.URL + "/login")
	if err != nil {
		t.Fatalf("GET /login failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestLoginSuccess(t *testing.T) {
	ts := newTestServer(t)

	form := url.Values{}
	form.Set("username", "admin")
	form.Set("password", "secret")

	resp, err := ts.client.PostForm(ts.server.URL+"/login", form)
	if err != nil {
		t.Fatalf("POST /login failed: %v", err)
	}
	defer resp.Body.Close()

	// Should redirect
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("expected 303, got %d", resp.StatusCode)
	}

	// Should have session cookie
	hasCookie := false
	for _, c := range resp.Cookies() {
		if c.Name == "kyvik_session" {
			hasCookie = true
			if c.SameSite != http.SameSiteLaxMode {
				t.Errorf("expected SameSite=Lax, got %v", c.SameSite)
			}
		}
	}
	if !hasCookie {
		t.Error("no session cookie set after login")
	}
}

func TestLoginFailure(t *testing.T) {
	ts := newTestServer(t)

	form := url.Values{}
	form.Set("username", "admin")
	form.Set("password", "wrong")

	resp, err := ts.client.PostForm(ts.server.URL+"/login", form)
	if err != nil {
		t.Fatalf("POST /login failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

func TestAuthRedirectWithoutCookie(t *testing.T) {
	ts := newTestServer(t)

	resp, err := ts.client.Get(ts.server.URL + "/")
	if err != nil {
		t.Fatalf("GET / failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("expected 303 redirect, got %d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/login" {
		t.Errorf("expected redirect to /login, got %q", loc)
	}
}

func TestDashboardAuthed(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	resp := ts.authedGet(t, "/", cookie)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestAgentListEmpty(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	resp := ts.authedGet(t, "/agents", cookie)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestAgentWizardStep1(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	resp := ts.authedGet(t, "/agents/new", cookie)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestWizardStep2Validation(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	// Step 2 without name should re-render step 1 with error
	form := url.Values{}
	form.Set("name", "")
	form.Set("template", "worker")

	resp := ts.authedPost(t, "/agents/new/step2", cookie, form, true)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestWizardStep2Success(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	form := url.Values{}
	form.Set("name", "Test Agent")
	form.Set("description", "A test agent")
	form.Set("template", "worker")

	resp := ts.authedPost(t, "/agents/new/step2", cookie, form, true)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestWizardStep4Validation(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	// Step 4 without provider should re-render step 3
	form := url.Values{}
	form.Set("name", "Test Agent")
	form.Set("provider", "")
	form.Set("model", "")
	form.Set("template", "worker")

	resp := ts.authedPost(t, "/agents/new/step4", cookie, form, true)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestWizardFullFlow(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	// Step 2 (soul)
	form := url.Values{}
	form.Set("name", "My Agent")
	form.Set("description", "Test desc")
	form.Set("template", "worker")

	resp := ts.authedPost(t, "/agents/new/step2", cookie, form, true)
	resp.Body.Close()

	// Step 3 (models)
	form.Set("soul_tab", "presets")
	form.Set("soul_preset", "friendly-helper")
	form.Set("identity_tab", "presets")
	form.Set("identity_preset", "general-assistant")

	resp = ts.authedPost(t, "/agents/new/step3", cookie, form, true)
	resp.Body.Close()

	// Step 4 (skills)
	form.Set("provider", "test-provider")
	form.Set("model", "test-model")

	resp = ts.authedPost(t, "/agents/new/step4", cookie, form, true)
	resp.Body.Close()

	// Step 5 (channels)
	resp = ts.authedPost(t, "/agents/new/step5", cookie, form, true)
	resp.Body.Close()

	// Step 10 (team/review)
	resp = ts.authedPost(t, "/agents/new/step10", cookie, form, true)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for step 10, got %d", resp.StatusCode)
	}
}

func TestAgentCreate(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	form := url.Values{}
	form.Set("name", "Created Agent")
	form.Set("description", "Test")
	form.Set("soul_content", "You are friendly.")
	form.Set("identity_content", "You are a researcher.")
	form.Set("provider", "test-provider")
	form.Set("model", "test-model")
	form.Set("template", "worker")
	form.Set("max_tokens_per_day", "1000")
	form.Set("max_tokens_per_month", "10000")
	form.Set("max_spend_per_day", "1.50")
	form.Set("max_spend_per_month", "15.00")
	form.Set("slack_mode", "primary")
	form.Set("slack_channel", "C12345")

	resp := ts.authedPost(t, "/agents", cookie, form, true)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	// Should have HX-Redirect header
	if redir := resp.Header.Get("HX-Redirect"); redir != "/agents" {
		t.Errorf("expected HX-Redirect to /agents, got %q", redir)
	}

	// Agent should exist in store with channel mapping and soul/identity
	agents, err := ts.kyvik.ListAgents(context.Background())
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if agents[0].Name != "Created Agent" {
		t.Errorf("expected name 'Created Agent', got %q", agents[0].Name)
	}
	if agents[0].SoulContent != "You are friendly." {
		t.Errorf("expected soul_content 'You are friendly.', got %q", agents[0].SoulContent)
	}
	if agents[0].IdentityContent != "You are a researcher." {
		t.Errorf("expected identity_content 'You are a researcher.', got %q", agents[0].IdentityContent)
	}
	if agents[0].SlackMode != "primary" {
		t.Errorf("expected slack_mode 'primary', got %q", agents[0].SlackMode)
	}
	if agents[0].SlackChannel != "C12345" {
		t.Errorf("expected slack_channel 'C12345', got %q", agents[0].SlackChannel)
	}
	// Primary mode should also create a legacy channel mapping
	if len(agents[0].Channels) != 1 {
		t.Fatalf("expected 1 channel mapping, got %d", len(agents[0].Channels))
	}
	if agents[0].Channels[0].ChannelType != "slack" {
		t.Errorf("expected channel_type 'slack', got %q", agents[0].Channels[0].ChannelType)
	}
	if agents[0].Channels[0].ChannelID != "C12345" {
		t.Errorf("expected channel_id 'C12345', got %q", agents[0].Channels[0].ChannelID)
	}
}

func TestWizardRendersAllSteps(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	form := url.Values{}
	form.Set("name", "Wizard Agent")
	form.Set("description", "Test")
	form.Set("template", "worker")

	resp := ts.authedPost(t, "/agents/new/step2", cookie, form, true)
	resp.Body.Close()

	form.Set("soul_tab", "presets")
	form.Set("soul_preset", "friendly-helper")
	form.Set("identity_tab", "presets")
	form.Set("identity_preset", "general-assistant")
	resp = ts.authedPost(t, "/agents/new/step3", cookie, form, true)
	resp.Body.Close()

	form.Set("provider", "test-provider")
	form.Set("model", "test-model")
	resp = ts.authedPost(t, "/agents/new/step4", cookie, form, true)
	resp.Body.Close()

	resp = ts.authedPost(t, "/agents/new/step5", cookie, form, true)
	resp.Body.Close()

	form.Set("show_advanced", "true")
	resp = ts.authedPost(t, "/agents/new/step6", cookie, form, true)
	resp.Body.Close()
	resp = ts.authedPost(t, "/agents/new/step7", cookie, form, true)
	resp.Body.Close()
	resp = ts.authedPost(t, "/agents/new/step8", cookie, form, true)
	resp.Body.Close()
	resp = ts.authedPost(t, "/agents/new/step9", cookie, form, true)
	resp.Body.Close()
	resp = ts.authedPost(t, "/agents/new/step10", cookie, form, true)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestScheduleCronPreviewValidation(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	resp := ts.authedGet(t, "/schedules/preview?cron_expr=invalid", cookie)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestPermissionTierWarningsRender(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	resp := ts.authedGet(t, "/agents/new", cookie)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Full System Access") {
		t.Errorf("expected warning text 'Full System Access' in response")
	}
}

func TestAgentCreateValidation(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	// Missing required fields
	form := url.Values{}
	form.Set("name", "")
	form.Set("provider", "test-provider")
	form.Set("model", "test-model")
	form.Set("template", "worker")

	resp := ts.authedPost(t, "/agents", cookie, form, true)
	defer resp.Body.Close()

	// Should re-render step 8 (200 with error), not redirect
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if redir := resp.Header.Get("HX-Redirect"); redir != "" {
		t.Errorf("should not redirect on validation error, got %q", redir)
	}
}

func TestAgentStartStop(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	// Create an agent first
	ctx := context.Background()
	config := types.AgentConfig{
		ID:          "test-agent-1",
		Name:        "Test Agent",
		ModelConfig: types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:    "worker",
	}
	if err := ts.kyvik.StartAgent(ctx, config); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}

	// Wait for running
	waitForStatus(t, ts.kyvik, "test-agent-1", types.AgentStatusRunning)

	// Stop it
	form := url.Values{}
	resp := ts.authedPost(t, "/agents/test-agent-1/stop", cookie, form, true)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for stop, got %d", resp.StatusCode)
	}

	// Start it again
	resp = ts.authedPost(t, "/agents/test-agent-1/start", cookie, form, true)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for start, got %d", resp.StatusCode)
	}
}

func TestAgentStatusFragment(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	// Create an agent
	ctx := context.Background()
	config := types.AgentConfig{
		ID:          "status-agent",
		Name:        "Status Agent",
		ModelConfig: types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:    "reader",
	}
	if err := ts.kyvik.StartAgent(ctx, config); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	waitForStatus(t, ts.kyvik, "status-agent", types.AgentStatusRunning)

	resp := ts.authedGet(t, "/agents/status-agent/status", cookie)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	// Cleanup
	ts.kyvik.StopAgent(ctx, "status-agent")
}

func TestLogout(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	form := url.Values{}
	resp := ts.authedPost(t, "/logout", cookie, form, false)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("expected 303 redirect, got %d", resp.StatusCode)
	}

	// Session cookie should be cleared
	cleared := false
	for _, c := range resp.Cookies() {
		if c.Name == "kyvik_session" && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("session cookie not cleared after logout")
	}
}

func TestHTMXFragmentDetection(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	// HTMX request to dashboard should return fragment, not full page
	req, _ := http.NewRequest("GET", ts.server.URL+"/", nil)
	req.AddCookie(cookie)
	req.Header.Set("HX-Request", "true")

	resp, err := ts.client.Do(req)
	if err != nil {
		t.Fatalf("HTMX GET / failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestStaticAssets(t *testing.T) {
	ts := newTestServer(t)

	// CSS should be accessible without auth
	resp, err := ts.client.Get(ts.server.URL + "/static/css/style.css")
	if err != nil {
		t.Fatalf("GET /static/css/style.css failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for CSS, got %d", resp.StatusCode)
	}

	// JS should be accessible without auth
	resp2, err := ts.client.Get(ts.server.URL + "/static/js/htmx.min.js")
	if err != nil {
		t.Fatalf("GET /static/js/htmx.min.js failed: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for JS, got %d", resp2.StatusCode)
	}
}

func TestAgentChat(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	// Create and start an agent
	ctx := context.Background()
	config := types.AgentConfig{
		ID:          "chat-agent",
		Name:        "Chat Agent",
		ModelConfig: types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:    "worker",
	}
	if err := ts.kyvik.StartAgent(ctx, config); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	waitForStatus(t, ts.kyvik, "chat-agent", types.AgentStatusRunning)
	defer ts.kyvik.StopAgent(ctx, "chat-agent")

	resp := ts.authedGet(t, "/agents/chat-agent/chat", cookie)
	defer resp.Body.Close()

	// Chat v2 is the default, so /chat redirects to /chat2
	if resp.StatusCode != http.StatusTemporaryRedirect {
		t.Errorf("expected 307 redirect to chat2, got %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if loc != "/agents/chat-agent/chat2" {
		t.Errorf("redirect location = %q, want /agents/chat-agent/chat2", loc)
	}
}

func TestAgentChatSend(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	// Create and start an agent
	ctx := context.Background()
	config := types.AgentConfig{
		ID:          "send-agent",
		Name:        "Send Agent",
		ModelConfig: types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:    "worker",
	}
	if err := ts.kyvik.StartAgent(ctx, config); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	waitForStatus(t, ts.kyvik, "send-agent", types.AgentStatusRunning)
	defer ts.kyvik.StopAgent(ctx, "send-agent")

	form := url.Values{}
	form.Set("message", "Hello agent!")

	resp := ts.authedPost(t, "/agents/send-agent/chat/send", cookie, form, true)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestAgentChatNotFound(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	// Use explicit v1 path to test the 404 directly (the default /chat
	// redirects to /chat2 before checking agent existence).
	resp := ts.authedGet(t, "/agents/nonexistent/chat/v1", cookie)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestAgentChatSendNotRunning(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	// Create agent in store but don't start it
	ts.store.CreateAgent(context.Background(), types.AgentConfig{
		ID:   "stopped-agent",
		Name: "Stopped Agent",
	})

	form := url.Values{}
	form.Set("message", "Hello")

	resp := ts.authedPost(t, "/agents/stopped-agent/chat/send", cookie, form, true)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for stopped agent, got %d", resp.StatusCode)
	}
}

func TestAgentDelete(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	// Create agent in store
	ts.store.CreateAgent(context.Background(), types.AgentConfig{
		ID:          "delete-agent",
		Name:        "Delete Me",
		ModelConfig: types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:    "worker",
	})

	form := url.Values{}
	resp := ts.authedPost(t, "/agents/delete-agent/delete", cookie, form, true)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if redir := resp.Header.Get("HX-Redirect"); redir != "/agents" {
		t.Errorf("expected HX-Redirect to /agents, got %q", redir)
	}

	// Verify agent is gone
	_, err := ts.store.GetAgent(context.Background(), "delete-agent")
	if err != types.ErrNotFound {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestAgentDeleteRunning(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	// Create and start a running agent
	ctx := context.Background()
	config := types.AgentConfig{
		ID:          "delete-running-agent",
		Name:        "Running Agent",
		ModelConfig: types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:    "worker",
	}
	if err := ts.kyvik.StartAgent(ctx, config); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	waitForStatus(t, ts.kyvik, "delete-running-agent", types.AgentStatusRunning)

	form := url.Values{}
	resp := ts.authedPost(t, "/agents/delete-running-agent/delete", cookie, form, true)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if redir := resp.Header.Get("HX-Redirect"); redir != "/agents" {
		t.Errorf("expected HX-Redirect to /agents, got %q", redir)
	}

	// Verify agent is gone from store
	_, err := ts.store.GetAgent(ctx, "delete-running-agent")
	if err != types.ErrNotFound {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}

	// Verify agent is no longer running
	status, _ := ts.kyvik.GetAgentStatus(ctx, "delete-running-agent")
	if status != types.AgentStatusStopped {
		t.Errorf("expected stopped status, got %s", status)
	}
}

func TestAgentSoulsFragment(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	// Create agents: one with soul, one without
	ts.store.CreateAgent(context.Background(), types.AgentConfig{
		ID:          "has-soul",
		Name:        "Has Soul",
		SoulContent: "Some soul content.",
		ModelConfig: types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:    "worker",
	})
	ts.store.CreateAgent(context.Background(), types.AgentConfig{
		ID:          "no-soul",
		Name:        "No Soul",
		ModelConfig: types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:    "reader",
	})

	resp := ts.authedGet(t, "/agents/souls", cookie)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for /agents/souls, got %d", resp.StatusCode)
	}
}

func TestAgentIdentitiesFragment(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	// Create agents: one with identity, one without
	ts.store.CreateAgent(context.Background(), types.AgentConfig{
		ID:              "has-identity",
		Name:            "Has Identity",
		IdentityContent: "Some identity content.",
		ModelConfig:     types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:        "worker",
	})
	ts.store.CreateAgent(context.Background(), types.AgentConfig{
		ID:          "no-identity",
		Name:        "No Identity",
		ModelConfig: types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:    "reader",
	})

	resp := ts.authedGet(t, "/agents/identities", cookie)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for /agents/identities, got %d", resp.StatusCode)
	}
}

func TestAgentHistory(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	// Create agent in store
	ts.store.CreateAgent(context.Background(), types.AgentConfig{
		ID:          "history-agent",
		Name:        "History Agent",
		ModelConfig: types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:    "worker",
	})

	resp := ts.authedGet(t, "/agents/history-agent/history", cookie)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for history page, got %d", resp.StatusCode)
	}
}

func TestAgentHistoryNotFound(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	resp := ts.authedGet(t, "/agents/nonexistent/history", cookie)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for nonexistent agent, got %d", resp.StatusCode)
	}
}

func TestAgentHistoryClear(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	// Create agent in store
	ts.store.CreateAgent(context.Background(), types.AgentConfig{
		ID:          "clear-history-agent",
		Name:        "Clear History",
		ModelConfig: types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:    "worker",
	})

	form := url.Values{}
	resp := ts.authedPost(t, "/agents/clear-history-agent/history/clear", cookie, form, true)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for history clear, got %d", resp.StatusCode)
	}
	if redir := resp.Header.Get("HX-Redirect"); redir != "/agents/clear-history-agent/history" {
		t.Errorf("expected HX-Redirect to history page, got %q", redir)
	}
}

func TestAgentHistoryFragment(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	// Create agent in store
	ts.store.CreateAgent(context.Background(), types.AgentConfig{
		ID:          "frag-history-agent",
		Name:        "Frag History",
		ModelConfig: types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:    "worker",
	})

	resp := ts.authedGet(t, "/agents/frag-history-agent/history/entries", cookie)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for history entries fragment, got %d", resp.StatusCode)
	}
}

func TestAgentCreateWithHistoryLimit(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	form := url.Values{}
	form.Set("name", "History Limit Agent")
	form.Set("description", "Test history limit")
	form.Set("provider", "test-provider")
	form.Set("model", "test-model")
	form.Set("template", "worker")
	form.Set("history_limit", "100")

	resp := ts.authedPost(t, "/agents", cookie, form, true)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	// Verify history_limit persisted
	agents, err := ts.kyvik.ListAgents(context.Background())
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	found := false
	for _, a := range agents {
		if a.Name == "History Limit Agent" {
			found = true
			if a.HistoryLimit != 100 {
				t.Errorf("HistoryLimit = %d, want 100", a.HistoryLimit)
			}
		}
	}
	if !found {
		t.Error("agent 'History Limit Agent' not found")
	}
}

func TestAgentCreateWithMemoryLimit(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	form := url.Values{}
	form.Set("name", "Memory Limit Agent")
	form.Set("description", "Test memory limit")
	form.Set("provider", "test-provider")
	form.Set("model", "test-model")
	form.Set("template", "worker")
	form.Set("memory_limit", "20")

	resp := ts.authedPost(t, "/agents", cookie, form, true)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	// Verify memory_limit persisted
	agents, err := ts.kyvik.ListAgents(context.Background())
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	found := false
	for _, a := range agents {
		if a.Name == "Memory Limit Agent" {
			found = true
			if a.MemoryLimit != 20 {
				t.Errorf("MemoryLimit = %d, want 20", a.MemoryLimit)
			}
		}
	}
	if !found {
		t.Error("agent 'Memory Limit Agent' not found")
	}
}

func TestAgentCreateWithContextBudget(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	form := url.Values{}
	form.Set("name", "Budget Agent")
	form.Set("description", "Test context budget")
	form.Set("provider", "test-provider")
	form.Set("model", "test-model")
	form.Set("template", "worker")
	form.Set("max_total_tokens", "4000")
	form.Set("soul_identity_pct", "20")
	form.Set("skills_pct", "5")
	form.Set("memories_pct", "30")
	form.Set("history_pct", "45")

	resp := ts.authedPost(t, "/agents", cookie, form, true)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	// Verify context budget persisted
	agents, err := ts.kyvik.ListAgents(context.Background())
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	found := false
	for _, a := range agents {
		if a.Name == "Budget Agent" {
			found = true
			if a.ContextBudget.MaxTotalTokens != 4000 {
				t.Errorf("MaxTotalTokens = %d, want 4000", a.ContextBudget.MaxTotalTokens)
			}
			if a.ContextBudget.SoulIdentityPct != 20 {
				t.Errorf("SoulIdentityPct = %d, want 20", a.ContextBudget.SoulIdentityPct)
			}
			if a.ContextBudget.SkillsPct != 5 {
				t.Errorf("SkillsPct = %d, want 5", a.ContextBudget.SkillsPct)
			}
			if a.ContextBudget.MemoriesPct != 30 {
				t.Errorf("MemoriesPct = %d, want 30", a.ContextBudget.MemoriesPct)
			}
			if a.ContextBudget.HistoryPct != 45 {
				t.Errorf("HistoryPct = %d, want 45", a.ContextBudget.HistoryPct)
			}
		}
	}
	if !found {
		t.Error("agent 'Budget Agent' not found")
	}
}

func TestAgentCreateWithAutoExtract(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	form := url.Values{}
	form.Set("name", "Auto Extract Agent")
	form.Set("description", "Test auto extract")
	form.Set("provider", "test-provider")
	form.Set("model", "test-model")
	form.Set("template", "worker")
	form.Set("auto_extract_memories", "true")

	resp := ts.authedPost(t, "/agents", cookie, form, true)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	// Verify auto_extract_memories persisted
	agents, err := ts.kyvik.ListAgents(context.Background())
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	found := false
	for _, a := range agents {
		if a.Name == "Auto Extract Agent" {
			found = true
			if !a.AutoExtractMemories {
				t.Error("AutoExtractMemories = false, want true")
			}
		}
	}
	if !found {
		t.Error("agent 'Auto Extract Agent' not found")
	}
}

func TestWizardCustomSoulTab(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	// Step 2 (basics → soul)
	form := url.Values{}
	form.Set("name", "Custom Soul Agent")
	form.Set("description", "Testing custom tab")

	resp := ts.authedPost(t, "/agents/new/step2", cookie, form, true)
	resp.Body.Close()

	// Step 3 (soul → role) with custom tab
	form.Set("soul_tab", "custom")
	form.Set("soul_custom", "My custom soul content.")

	resp = ts.authedPost(t, "/agents/new/step3", cookie, form, true)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	// Step 4 (role → model) with custom tab
	form.Set("identity_tab", "custom")
	form.Set("identity_custom", "My custom identity content.")

	resp = ts.authedPost(t, "/agents/new/step4", cookie, form, true)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestAgentMemories(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	// Create agent in store
	ts.store.CreateAgent(context.Background(), types.AgentConfig{
		ID:          "mem-agent",
		Name:        "Memory Agent",
		ModelConfig: types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:    "worker",
	})

	resp := ts.authedGet(t, "/agents/mem-agent/memories", cookie)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for memories page, got %d", resp.StatusCode)
	}
}

func TestAgentMemoriesNotFound(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	resp := ts.authedGet(t, "/agents/nonexistent/memories", cookie)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for nonexistent agent, got %d", resp.StatusCode)
	}
}

func TestAgentMemoriesClear(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	ts.store.CreateAgent(context.Background(), types.AgentConfig{
		ID:          "clear-mem-agent",
		Name:        "Clear Memory",
		ModelConfig: types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:    "worker",
	})

	form := url.Values{}
	resp := ts.authedPost(t, "/agents/clear-mem-agent/memories/clear", cookie, form, true)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for memories clear, got %d", resp.StatusCode)
	}
	if redir := resp.Header.Get("HX-Redirect"); redir != "/agents/clear-mem-agent/memories" {
		t.Errorf("expected HX-Redirect to memories page, got %q", redir)
	}
}

func TestAgentMemoriesFragment(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	ts.store.CreateAgent(context.Background(), types.AgentConfig{
		ID:          "frag-mem-agent",
		Name:        "Frag Memory",
		ModelConfig: types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:    "worker",
	})

	resp := ts.authedGet(t, "/agents/frag-mem-agent/memories/list", cookie)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for memory list fragment, got %d", resp.StatusCode)
	}
}

func TestAgentMemoryArchive(t *testing.T) {
	ms := newMockMemoryStore()
	ts := newTestServerWithMemory(t, ms)
	cookie := ts.login(t)

	ts.store.CreateAgent(context.Background(), types.AgentConfig{
		ID:          "arch-agent",
		Name:        "Archive Agent",
		ModelConfig: types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:    "worker",
	})

	// Create a memory to archive.
	memID, _ := ms.Create(context.Background(), memory.Memory{
		AgentID:  "arch-agent",
		Category: "fact",
		Content:  "test memory for archiving",
		Source:   "user",
	})

	// Verify it starts as not archived.
	mem, _ := ms.Get(context.Background(), memID)
	if mem.Archived {
		t.Fatal("expected memory to start as not archived")
	}

	// Archive it via handler.
	form := url.Values{}
	resp := ts.authedPost(t, fmt.Sprintf("/agents/arch-agent/memories/%d/archive", memID), cookie, form, true)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for archive, got %d", resp.StatusCode)
	}

	// Verify memory is now archived.
	mem, _ = ms.Get(context.Background(), memID)
	if !mem.Archived {
		t.Error("expected memory to be archived after POST")
	}
}

func TestAgentMemoryUnarchive(t *testing.T) {
	ms := newMockMemoryStore()
	ts := newTestServerWithMemory(t, ms)
	cookie := ts.login(t)

	ts.store.CreateAgent(context.Background(), types.AgentConfig{
		ID:          "unarch-agent",
		Name:        "Unarchive Agent",
		ModelConfig: types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:    "worker",
	})

	// Create and archive a memory.
	memID, _ := ms.Create(context.Background(), memory.Memory{
		AgentID:  "unarch-agent",
		Category: "fact",
		Content:  "test memory for unarchiving",
		Source:   "user",
		Archived: true,
	})

	// Verify it starts as archived.
	mem, _ := ms.Get(context.Background(), memID)
	if !mem.Archived {
		t.Fatal("expected memory to start as archived")
	}

	// Unarchive it via handler.
	form := url.Values{}
	resp := ts.authedPost(t, fmt.Sprintf("/agents/unarch-agent/memories/%d/unarchive", memID), cookie, form, true)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for unarchive, got %d", resp.StatusCode)
	}

	// Verify memory is now active.
	mem, _ = ms.Get(context.Background(), memID)
	if mem.Archived {
		t.Error("expected memory to be unarchived after POST")
	}
}

func TestAgentMemoryArchiveInvalidID(t *testing.T) {
	ms := newMockMemoryStore()
	ts := newTestServerWithMemory(t, ms)
	cookie := ts.login(t)

	ts.store.CreateAgent(context.Background(), types.AgentConfig{
		ID:          "inv-agent",
		Name:        "Invalid Agent",
		ModelConfig: types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:    "worker",
	})

	form := url.Values{}
	resp := ts.authedPost(t, "/agents/inv-agent/memories/notanumber/archive", cookie, form, true)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid memory ID, got %d", resp.StatusCode)
	}
}

func TestAgentMemoryArchiveNoMemoryStore(t *testing.T) {
	ts := newTestServer(t) // no memory store wired
	cookie := ts.login(t)

	ts.store.CreateAgent(context.Background(), types.AgentConfig{
		ID:          "noms-agent",
		Name:        "No MS Agent",
		ModelConfig: types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:    "worker",
	})

	form := url.Values{}
	resp := ts.authedPost(t, "/agents/noms-agent/memories/1/archive", cookie, form, true)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected 500 when memory store is nil, got %d", resp.StatusCode)
	}
}

func TestAgentMemoriesArchivedTab(t *testing.T) {
	ms := newMockMemoryStore()
	ts := newTestServerWithMemory(t, ms)
	cookie := ts.login(t)

	ts.store.CreateAgent(context.Background(), types.AgentConfig{
		ID:          "tab-agent",
		Name:        "Tab Agent",
		ModelConfig: types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:    "worker",
	})

	// Create one active and one archived memory.
	ms.Create(context.Background(), memory.Memory{
		AgentID:  "tab-agent",
		Category: "fact",
		Content:  "active memory",
		Source:   "user",
	})
	ms.Create(context.Background(), memory.Memory{
		AgentID:  "tab-agent",
		Category: "fact",
		Content:  "archived memory",
		Source:   "user",
		Archived: true,
	})

	// Get the active tab — should return 200.
	resp := ts.authedGet(t, "/agents/tab-agent/memories", cookie)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for active memories, got %d", resp.StatusCode)
	}

	// Get the archived tab — should return 200.
	resp2 := ts.authedGet(t, "/agents/tab-agent/memories?archived=true", cookie)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for archived memories, got %d", resp2.StatusCode)
	}
}

func TestAgentMemoriesSemanticSearch(t *testing.T) {
	ms := newMockMemoryStore()
	ts := newTestServerWithMemory(t, ms)
	cookie := ts.login(t)

	ts.store.CreateAgent(context.Background(), types.AgentConfig{
		ID:          "search-agent",
		Name:        "Search Agent",
		ModelConfig: types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:    "worker",
	})

	memID1, _ := ms.Create(context.Background(), memory.Memory{
		AgentID:  "search-agent",
		Category: "fact",
		Content:  "alpha memory",
		Source:   "user",
	})
	_ = ms.SetEmbedding(context.Background(), memID1, []float32{1, 0, 0}, "mock-embed")

	memID2, _ := ms.Create(context.Background(), memory.Memory{
		AgentID:  "search-agent",
		Category: "fact",
		Content:  "beta memory",
		Source:   "user",
	})
	_ = ms.SetEmbedding(context.Background(), memID2, []float32{0, 1, 0}, "mock-embed")

	resp := ts.authedGet(t, "/agents/search-agent/memories/list?q=alpha", cookie)
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	content := string(body)
	if !strings.Contains(content, "alpha memory") {
		t.Fatalf("expected alpha memory in response")
	}
	if !strings.Contains(content, "beta memory") {
		t.Fatalf("expected beta memory in response")
	}
	if strings.Index(content, "alpha memory") > strings.Index(content, "beta memory") {
		t.Errorf("expected alpha memory ranked before beta memory")
	}
}

func TestAgentMemoryReviewApproveReject(t *testing.T) {
	ms := newMockMemoryStore()
	ts := newTestServerWithMemory(t, ms)
	cookie := ts.login(t)

	ts.store.CreateAgent(context.Background(), types.AgentConfig{
		ID:          "review-agent",
		Name:        "Review Agent",
		ModelConfig: types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:    "worker",
	})

	memID, _ := ms.Create(context.Background(), memory.Memory{
		AgentID:  "review-agent",
		Category: "fact",
		Content:  "auto memory",
		Source:   "auto",
		Reviewed: false,
	})

	form := url.Values{}
	form.Set("tab", "review")
	resp := ts.authedPost(t, fmt.Sprintf("/agents/review-agent/memories/review/%d/approve", memID), cookie, form, true)
	resp.Body.Close()

	mem, _ := ms.Get(context.Background(), memID)
	if !mem.Reviewed {
		t.Fatalf("expected memory to be reviewed after approve")
	}

	memID2, _ := ms.Create(context.Background(), memory.Memory{
		AgentID:  "review-agent",
		Category: "fact",
		Content:  "auto memory 2",
		Source:   "auto",
		Reviewed: false,
	})
	resp2 := ts.authedPost(t, fmt.Sprintf("/agents/review-agent/memories/review/%d/reject", memID2), cookie, form, true)
	resp2.Body.Close()

	if _, err := ms.Get(context.Background(), memID2); err == nil {
		t.Fatalf("expected memory to be deleted after reject")
	}
}

func TestAgentMemoriesBulkPin(t *testing.T) {
	ms := newMockMemoryStore()
	ts := newTestServerWithMemory(t, ms)
	cookie := ts.login(t)

	ts.store.CreateAgent(context.Background(), types.AgentConfig{
		ID:          "bulk-agent",
		Name:        "Bulk Agent",
		ModelConfig: types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:    "worker",
	})

	id1, _ := ms.Create(context.Background(), memory.Memory{
		AgentID:  "bulk-agent",
		Category: "fact",
		Content:  "bulk one",
		Source:   "user",
	})
	id2, _ := ms.Create(context.Background(), memory.Memory{
		AgentID:  "bulk-agent",
		Category: "fact",
		Content:  "bulk two",
		Source:   "user",
	})

	form := url.Values{}
	form.Set("action", "pin")
	form.Add("memory_id", fmt.Sprintf("%d", id1))
	form.Add("memory_id", fmt.Sprintf("%d", id2))
	resp := ts.authedPost(t, "/agents/bulk-agent/memories/bulk", cookie, form, true)
	resp.Body.Close()

	mem1, _ := ms.Get(context.Background(), id1)
	mem2, _ := ms.Get(context.Background(), id2)
	if !mem1.Pinned || !mem2.Pinned {
		t.Fatalf("expected bulk pin to pin all selected memories")
	}
}

func TestAgentMemoriesExportImport(t *testing.T) {
	ms := newMockMemoryStore()
	ts := newTestServerWithMemory(t, ms)
	cookie := ts.login(t)

	ts.store.CreateAgent(context.Background(), types.AgentConfig{
		ID:          "export-agent",
		Name:        "Export Agent",
		ModelConfig: types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:    "worker",
	})

	_, _ = ms.Create(context.Background(), memory.Memory{
		AgentID:  "export-agent",
		Category: "fact",
		Content:  "export memory",
		Source:   "user",
	})

	resp := ts.authedGet(t, "/agents/export-agent/memories/export", cookie)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for export, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var payload []map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("invalid export JSON: %v", err)
	}
	if len(payload) == 0 {
		t.Fatalf("expected export JSON to include at least one memory")
	}

	importPayload := `[{"category":"fact","content":"imported memory","source":"user","pinned":true}]`
	form := url.Values{}
	form.Set("payload", importPayload)
	resp2 := ts.authedPost(t, "/agents/export-agent/memories/import", cookie, form, true)
	resp2.Body.Close()

	includeAll := true
	memories, _ := ms.List(context.Background(), "export-agent", memory.ListOptions{IncludeArchived: &includeAll})
	found := false
	for _, mem := range memories {
		if mem.Content == "imported memory" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected imported memory to be created")
	}
}

func TestAgentMemoriesFragmentWithMemoryStore(t *testing.T) {
	ms := newMockMemoryStore()
	ts := newTestServerWithMemory(t, ms)
	cookie := ts.login(t)

	ts.store.CreateAgent(context.Background(), types.AgentConfig{
		ID:          "fragms-agent",
		Name:        "FragMS Agent",
		ModelConfig: types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:    "worker",
	})

	// Create memories.
	ms.Create(context.Background(), memory.Memory{
		AgentID:  "fragms-agent",
		Category: "fact",
		Content:  "a fact",
		Source:   "user",
	})
	ms.Create(context.Background(), memory.Memory{
		AgentID:  "fragms-agent",
		Category: "decision",
		Content:  "a decision",
		Source:   "agent",
	})

	// Filter by category.
	resp := ts.authedGet(t, "/agents/fragms-agent/memories/list?category=fact", cookie)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for filtered fragment, got %d", resp.StatusCode)
	}

	// Archived fragment.
	resp2 := ts.authedGet(t, "/agents/fragms-agent/memories/list?archived=true", cookie)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for archived fragment, got %d", resp2.StatusCode)
	}
}

func TestAgentCreateWithMultipleSlots(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	form := url.Values{}
	form.Set("name", "Multi Slot Agent")
	form.Set("description", "Agent with multiple model slots")
	form.Set("provider", "test-provider")
	form.Set("model", "test-model")
	form.Set("template", "worker")
	form.Set("model_slots_json", `[{"name":"default","provider":"test-provider","model":"test-model"},{"name":"fast","provider":"test-provider","model":"fast-model"}]`)
	form.Set("routing_config_json", `{"default_slot":"default","auto_route":true,"trigger_prefix":true}`)

	resp := ts.authedPost(t, "/agents", cookie, form, true)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if redir := resp.Header.Get("HX-Redirect"); redir != "/agents" {
		t.Errorf("expected HX-Redirect to /agents, got %q", redir)
	}

	// Verify slots persisted
	agents, err := ts.kyvik.ListAgents(context.Background())
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	found := false
	for _, a := range agents {
		if a.Name == "Multi Slot Agent" {
			found = true
			if a.ModelSlotsJSON == "" {
				t.Error("ModelSlotsJSON should not be empty")
			}
			if a.RoutingConfigJSON == "" {
				t.Error("RoutingConfigJSON should not be empty")
			}
			// Default slot provider/model should be set for backward compat
			if a.ModelConfig.Provider != "test-provider" {
				t.Errorf("ModelConfig.Provider = %q, want %q", a.ModelConfig.Provider, "test-provider")
			}
			if a.ModelConfig.Model != "test-model" {
				t.Errorf("ModelConfig.Model = %q, want %q", a.ModelConfig.Model, "test-model")
			}
		}
	}
	if !found {
		t.Error("agent 'Multi Slot Agent' not found")
	}
}

func TestWizardStep5WithSlotForm(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	// Submit step 5 with indexed slot fields (advanced mode)
	form := url.Values{}
	form.Set("name", "Slot Test Agent")
	form.Set("description", "")
	form.Set("slot_count", "2")
	form.Set("slot_name_0", "default")
	form.Set("slot_provider_0", "test-provider")
	form.Set("slot_model_0", "test-model")
	form.Set("slot_default_0", "true")
	form.Set("slot_name_1", "fast")
	form.Set("slot_provider_1", "test-provider")
	form.Set("slot_model_1", "fast-model")
	form.Set("slot_default_1", "false")

	resp := ts.authedPost(t, "/agents/new/step5", cookie, form, true)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestWizardStep5SlotValidation(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	// Submit with invalid slot name
	form := url.Values{}
	form.Set("name", "Bad Slot Agent")
	form.Set("slot_count", "1")
	form.Set("slot_name_0", "INVALID")
	form.Set("slot_provider_0", "test-provider")
	form.Set("slot_model_0", "test-model")
	form.Set("slot_default_0", "true")

	resp := ts.authedPost(t, "/agents/new/step5", cookie, form, true)
	defer resp.Body.Close()

	// Should re-render step 4 with error (200, not redirect)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestAgentDetailWithSlots(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	// Create agent with multiple slots
	ts.store.CreateAgent(context.Background(), types.AgentConfig{
		ID:                "detail-slot-agent",
		Name:              "Detail Slot Agent",
		ModelConfig:       types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:          "worker",
		ModelSlotsJSON:    `[{"name":"default","provider":"test-provider","model":"test-model"},{"name":"fast","provider":"test-provider","model":"fast-model"}]`,
		RoutingConfigJSON: `{"default_slot":"default","auto_route":true}`,
	})

	resp := ts.authedGet(t, "/agents/detail-slot-agent", cookie)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestSlotRowEndpoint(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	form := url.Values{}
	form.Set("slot_count", "2")

	resp := ts.authedPost(t, "/agents/new/slot-row", cookie, form, true)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestAgentCreateWithWorkers(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	form := url.Values{}
	form.Set("name", "Worker Agent")
	form.Set("description", "Agent with workers")
	form.Set("provider", "test-provider")
	form.Set("model", "test-model")
	form.Set("template", "worker")
	form.Set("workers_enabled", "true")
	form.Set("workers_max_concurrent", "5")
	form.Set("workers_ttl_seconds", "120")
	form.Set("workers_model_slot", "fast")

	resp := ts.authedPost(t, "/agents", cookie, form, true)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	// Verify worker config persisted
	agents, err := ts.kyvik.ListAgents(context.Background())
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	found := false
	for _, a := range agents {
		if a.Name == "Worker Agent" {
			found = true
			if !a.Workers.Enabled {
				t.Error("Workers.Enabled = false, want true")
			}
			if a.Workers.MaxConcurrent != 5 {
				t.Errorf("Workers.MaxConcurrent = %d, want 5", a.Workers.MaxConcurrent)
			}
			if a.Workers.TTLSeconds != 120 {
				t.Errorf("Workers.TTLSeconds = %d, want 120", a.Workers.TTLSeconds)
			}
			if a.Workers.ModelSlot != "fast" {
				t.Errorf("Workers.ModelSlot = %q, want %q", a.Workers.ModelSlot, "fast")
			}
		}
	}
	if !found {
		t.Error("agent 'Worker Agent' not found")
	}
}

func TestAgentDetailWithWorkers(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	// Create agent with workers enabled
	ts.store.CreateAgent(context.Background(), types.AgentConfig{
		ID:          "detail-worker-agent",
		Name:        "Detail Worker Agent",
		ModelConfig: types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:    "worker",
		Workers: types.WorkerConfig{
			Enabled:       true,
			MaxConcurrent: 3,
			TTLSeconds:    300,
			ModelSlot:     "fast",
		},
	})

	resp := ts.authedGet(t, "/agents/detail-worker-agent", cookie)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestAgentCreate_AdminRejectedWithoutConfirmation(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	form := url.Values{}
	form.Set("name", "Admin Agent")
	form.Set("provider", "test-provider")
	form.Set("model", "test-model")
	form.Set("template", "admin")

	resp := ts.authedPost(t, "/agents", cookie, form, true)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	// Should NOT redirect — validation error.
	if redir := resp.Header.Get("HX-Redirect"); redir != "" {
		t.Errorf("should not redirect on validation error, got %q", redir)
	}

	// No agent should have been created.
	agents, _ := ts.kyvik.ListAgents(context.Background())
	if len(agents) != 0 {
		t.Errorf("expected 0 agents, got %d", len(agents))
	}
}

func TestAgentCreate_AdminAcceptedWithConfirmation(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	form := url.Values{}
	form.Set("name", "Admin Agent")
	form.Set("provider", "test-provider")
	form.Set("model", "test-model")
	form.Set("template", "admin")
	form.Set("tier_acknowledged", "true")
	form.Set("tier_confirm_name", "Admin Agent")

	resp := ts.authedPost(t, "/agents", cookie, form, true)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if redir := resp.Header.Get("HX-Redirect"); redir != "/agents" {
		t.Errorf("expected HX-Redirect to /agents, got %q", redir)
	}

	agents, _ := ts.kyvik.ListAgents(context.Background())
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if agents[0].Template != "admin" {
		t.Errorf("expected template 'admin', got %q", agents[0].Template)
	}
}

// TestAgentCreate_AdminWithSlotJSON simulates the real wizard flow for admin tier,
// where provider/model come through slot JSON carry-forward and tier confirmation
// comes through hidden field carry-forward.
func TestAgentCreate_AdminWithSlotJSON(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	// Simulate the form data as it appears on step 10 after going through the wizard.
	// The slot JSON and provider/model hidden fields are set during step 4 processing.
	form := url.Values{}
	form.Set("name", "Admin Slot Agent")
	form.Set("template", "admin")
	form.Set("provider", "test-provider")
	form.Set("model", "test-model")
	form.Set("model_slots_json", `[{"name":"default","provider":"test-provider","model":"test-model"}]`)
	form.Set("routing_config_json", `{"default_slot":"default"}`)
	form.Set("tier_acknowledged", "true")
	form.Set("tier_confirm_name", "Admin Slot Agent")

	resp := ts.authedPost(t, "/agents", cookie, form, true)
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if redir := resp.Header.Get("HX-Redirect"); redir != "/agents" {
		t.Errorf("expected HX-Redirect to /agents, got %q; body: %s", redir, string(body))
	}

	agents, _ := ts.kyvik.ListAgents(context.Background())
	found := false
	for _, a := range agents {
		if a.Name == "Admin Slot Agent" {
			found = true
			if a.Template != "admin" {
				t.Errorf("expected template 'admin', got %q", a.Template)
			}
		}
	}
	if !found {
		t.Errorf("agent 'Admin Slot Agent' not found")
	}
}

// waitForStatus polls until the agent reaches the expected status.
func waitForStatus(t *testing.T, s *core.Kyvik, agentID string, expected types.AgentStatus) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		status, _ := s.GetAgentStatus(ctx, agentID)
		if status == expected {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("agent %s did not reach %s status", agentID, expected)
		case <-time.After(5 * time.Millisecond):
		}
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
