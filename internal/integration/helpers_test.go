package integration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/apikeys"
	"github.com/kkjorsvik/kyvik/internal/audit"
	"github.com/kkjorsvik/kyvik/internal/channels/busadapter"
	"github.com/kkjorsvik/kyvik/internal/channels/webui"
	"github.com/kkjorsvik/kyvik/internal/circuitbreaker"
	"github.com/kkjorsvik/kyvik/internal/config"
	"github.com/kkjorsvik/kyvik/internal/core"
	"github.com/kkjorsvik/kyvik/internal/history"
	"github.com/kkjorsvik/kyvik/internal/ktp"
	"github.com/kkjorsvik/kyvik/internal/memory"
	"github.com/kkjorsvik/kyvik/internal/models"
	"github.com/kkjorsvik/kyvik/internal/permissions"
	"github.com/kkjorsvik/kyvik/internal/queue"
	"github.com/kkjorsvik/kyvik/internal/secrets"
	"github.com/kkjorsvik/kyvik/internal/security"
	"github.com/kkjorsvik/kyvik/internal/spending"
	"github.com/kkjorsvik/kyvik/internal/store/postgres"
	"github.com/kkjorsvik/kyvik/internal/teams"
	"github.com/kkjorsvik/kyvik/internal/templates"
	"github.com/kkjorsvik/kyvik/internal/tools"
	"github.com/kkjorsvik/kyvik/internal/tools/browser"
	"github.com/kkjorsvik/kyvik/internal/tools/code"
	"github.com/kkjorsvik/kyvik/internal/tools/file"
	"github.com/kkjorsvik/kyvik/internal/tools/hostfs"
	httptool "github.com/kkjorsvik/kyvik/internal/tools/httptool"
	memorytool "github.com/kkjorsvik/kyvik/internal/tools/memory"
	"github.com/kkjorsvik/kyvik/internal/tools/restapi"
	"github.com/kkjorsvik/kyvik/internal/tools/shell"
	"github.com/kkjorsvik/kyvik/internal/authprovider/local"
	"github.com/kkjorsvik/kyvik/internal/sqlutil"
	"github.com/kkjorsvik/kyvik/internal/users"
	"github.com/kkjorsvik/kyvik/internal/testutil"
	"github.com/kkjorsvik/kyvik/pkg/types"
	"github.com/kkjorsvik/kyvik/web"
)

// --- Mock types ---

func mustNewPQ(db *sql.DB) *queue.PostgresQueue {
	q, err := queue.NewPostgresQueue(db, "", "", queue.DefaultConfig())
	if err != nil {
		panic("mustNewPQ: " + err.Error())
	}
	return q
}

// mockAgentStore satisfies ktp.AgentStore.
type mockAgentStore struct {
	agents map[string]*types.AgentConfig
}

func (m *mockAgentStore) GetAgent(_ context.Context, id string) (*types.AgentConfig, error) {
	agent, ok := m.agents[id]
	if !ok {
		return nil, fmt.Errorf("agent %s: %w", id, types.ErrNotFound)
	}
	return agent, nil
}

// mockSandboxExecutor records sandbox calls and returns canned responses.
type mockSandboxExecutor struct {
	mu           sync.Mutex
	createCalls  []mockCreateCall
	executeCalls []mockExecuteCall
	secretCalls  []mockSecretCall
	response     *ktp.ToolResponse
	createErr    error
	executeErr   error
	sandboxID    string
}

type mockCreateCall struct {
	AgentID       string
	TierOverrides map[string]any
}

type mockExecuteCall struct {
	SandboxID string
	Req       ktp.ToolRequest
}

type mockSecretCall struct {
	SandboxID string
	Secrets   map[string]string
}

func (m *mockSandboxExecutor) GetOrCreateSandbox(agentID string, tierOverrides map[string]any) (ktp.SandboxInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.createCalls = append(m.createCalls, mockCreateCall{AgentID: agentID, TierOverrides: tierOverrides})
	if m.createErr != nil {
		return ktp.SandboxInfo{}, m.createErr
	}
	id := m.sandboxID
	if id == "" {
		id = "sb-test-001"
	}
	return ktp.SandboxInfo{ID: id, AgentID: agentID}, nil
}

func (m *mockSandboxExecutor) ExecuteInSandbox(_ context.Context, sandboxID string, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.executeCalls = append(m.executeCalls, mockExecuteCall{SandboxID: sandboxID, Req: req})
	if m.executeErr != nil {
		return nil, m.executeErr
	}
	if m.response != nil {
		return m.response, nil
	}
	return &ktp.ToolResponse{
		RequestID: req.ID,
		Success:   true,
		Result:    req.Parameters,
	}, nil
}

func (m *mockSandboxExecutor) SetSandboxSecrets(sandboxID string, secrets map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.secretCalls = append(m.secretCalls, mockSecretCall{SandboxID: sandboxID, Secrets: secrets})
}

// mockSecretResolver returns secrets from a static map.
type mockSecretResolver struct {
	secrets map[string]string
}

func (m *mockSecretResolver) Resolve(_ context.Context, _, _, key string) (string, error) {
	v, ok := m.secrets[key]
	if !ok {
		return "", fmt.Errorf("secret %q not found", key)
	}
	return v, nil
}

// mockSecurityStore records security events.
type mockSecurityStore struct {
	mu     sync.Mutex
	events []types.SecurityEvent
}

func (m *mockSecurityStore) InsertSecurityEvent(_ context.Context, event types.SecurityEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, event)
	return nil
}

func (m *mockSecurityStore) QuerySecurityEvents(_ context.Context, agentID string, limit int) ([]types.SecurityEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var filtered []types.SecurityEvent
	for _, e := range m.events {
		if agentID == "" || e.AgentID == agentID {
			filtered = append(filtered, e)
		}
	}
	if limit > 0 && len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered, nil
}

// mockMemoryStore implements memory.MemoryStore for testing.
type mockMemoryStore struct {
	mu      sync.Mutex
	entries map[int64]memory.Memory
	nextID  int64
}

func newMockMemoryStore() *mockMemoryStore {
	return &mockMemoryStore{entries: make(map[int64]memory.Memory), nextID: 1}
}

func (m *mockMemoryStore) Create(_ context.Context, mem memory.Memory) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mem.ID = m.nextID
	m.nextID++
	if !mem.Reviewed && mem.Source != memory.SourceAuto {
		mem.Reviewed = true
	}
	m.entries[mem.ID] = mem
	return mem.ID, nil
}

func (m *mockMemoryStore) Get(_ context.Context, id int64) (memory.Memory, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mem, ok := m.entries[id]
	if !ok {
		return memory.Memory{}, fmt.Errorf("not found")
	}
	return mem, nil
}

func (m *mockMemoryStore) Update(_ context.Context, mem memory.Memory) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.entries[mem.ID]; !ok {
		return fmt.Errorf("not found")
	}
	m.entries[mem.ID] = mem
	return nil
}

func (m *mockMemoryStore) Delete(_ context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.entries[id]; !ok {
		return fmt.Errorf("not found")
	}
	delete(m.entries, id)
	return nil
}

func (m *mockMemoryStore) List(_ context.Context, agentID string, opts memory.ListOptions) ([]memory.Memory, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []memory.Memory
	for _, mem := range m.entries {
		if mem.AgentID != agentID {
			continue
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
	if opts.Offset > 0 && opts.Offset < len(result) {
		result = result[opts.Offset:]
	} else if opts.Offset >= len(result) {
		return nil, nil
	}
	if opts.Limit > 0 && len(result) > opts.Limit {
		result = result[:opts.Limit]
	}
	return result, nil
}

func (m *mockMemoryStore) CountFiltered(_ context.Context, agentID string, opts memory.ListOptions) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, mem := range m.entries {
		if mem.AgentID != agentID {
			continue
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

func (m *mockMemoryStore) CreateFromAgent(_ context.Context, agentID, category, content string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mem := memory.Memory{
		ID:        m.nextID,
		AgentID:   agentID,
		Category:  category,
		Content:   content,
		Source:    memory.SourceAgent,
		Reviewed:  true,
		CreatedAt: time.Now(),
	}
	m.nextID++
	m.entries[mem.ID] = mem
	return mem.ID, nil
}

// Unused interface stubs.
func (m *mockMemoryStore) ListPinned(context.Context, string) ([]memory.Memory, error) {
	return nil, nil
}
func (m *mockMemoryStore) Touch(context.Context, int64) error                           { return nil }
func (m *mockMemoryStore) SetEmbedding(context.Context, int64, []float32, string) error { return nil }
func (m *mockMemoryStore) ListWithEmbeddings(context.Context, string) ([]memory.Memory, error) {
	return nil, nil
}
func (m *mockMemoryStore) GetUnembedded(context.Context, string) ([]memory.Memory, error) {
	return nil, nil
}
func (m *mockMemoryStore) GetAllUnembedded(context.Context, int) ([]memory.Memory, error) {
	return nil, nil
}
func (m *mockMemoryStore) ListRecent(context.Context, string, int) ([]memory.Memory, error) {
	return nil, nil
}
func (m *mockMemoryStore) Count(context.Context, string) (int, error)  { return 0, nil }
func (m *mockMemoryStore) DeleteByAgent(context.Context, string) error { return nil }
func (m *mockMemoryStore) Import(context.Context, string, []memory.Memory) (int, error) {
	return 0, nil
}
func (m *mockMemoryStore) Archive(context.Context, int64) error   { return nil }
func (m *mockMemoryStore) Unarchive(context.Context, int64) error { return nil }
func (m *mockMemoryStore) ArchiveStale(context.Context, string, time.Duration) (int, error) {
	return 0, nil
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

// --- audit helpers ---

// dbAuditStore wraps *sql.DB to satisfy ktp.AuditStore.
type dbAuditStore struct{ db *sql.DB }

func (s *dbAuditStore) InsertAuditEntry(ctx context.Context, entry types.AuditEntry) error {
	_, err := sqlutil.ExecContext(ctx, s.db,
		`INSERT INTO audit_log (agent_id, event_type, action, resource, decision, details, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		entry.AgentID, string(entry.EventType), entry.Action,
		entry.Resource, entry.Decision, entry.Details, entry.Timestamp,
	)
	return err
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	tdb := testutil.RequirePostgres(t)
	return tdb.DB
}

func queryAuditEntries(t *testing.T, db *sql.DB, agentID string) []types.AuditEntry {
	t.Helper()
	rows, err := sqlutil.QueryContext(context.Background(), db,
		`SELECT agent_id, event_type, action, resource, decision, details, created_at
		 FROM audit_log WHERE agent_id = ? ORDER BY created_at`, agentID)
	if err != nil {
		t.Fatalf("query audit entries: %v", err)
	}
	defer rows.Close()

	var entries []types.AuditEntry
	for rows.Next() {
		var e types.AuditEntry
		var eventType string
		if err := rows.Scan(&e.AgentID, &eventType, &e.Action,
			&e.Resource, &e.Decision, &e.Details, &e.Timestamp); err != nil {
			t.Fatalf("scan audit entry: %v", err)
		}
		e.EventType = types.EventType(eventType)
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows iteration error: %v", err)
	}
	return entries
}

func queryAllAuditEntries(t *testing.T, db *sql.DB) []types.AuditEntry {
	t.Helper()
	rows, err := db.Query(
		`SELECT agent_id, event_type, action, resource, decision, details, created_at
		 FROM audit_log ORDER BY created_at`)
	if err != nil {
		t.Fatalf("query all audit entries: %v", err)
	}
	defer rows.Close()

	var entries []types.AuditEntry
	for rows.Next() {
		var e types.AuditEntry
		var eventType string
		if err := rows.Scan(&e.AgentID, &eventType, &e.Action,
			&e.Resource, &e.Decision, &e.Details, &e.Timestamp); err != nil {
			t.Fatalf("scan audit entry: %v", err)
		}
		e.EventType = types.EventType(eventType)
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows iteration error: %v", err)
	}
	return entries
}

// --- Test harness ---

type harnessOption func(*testHarness)

type testHarness struct {
	executor  *ktp.Executor
	registry  *ktp.Registry
	gate      *ktp.PermissionGate
	db        *sql.DB
	defense   *security.Defense
	sandbox   *mockSandboxExecutor
	secrets   *mockSecretResolver
	secStore  *mockSecurityStore
	memStore  *mockMemoryStore
	agents    map[string]*types.AgentConfig
	hostPaths map[string]*file.HostPathConfig

	hostFSConfigs map[string]*hostfs.HostPathConfig
	restEndpoints map[string][]types.RESTAPIEndpoint
	restTransport http.RoundTripper

	allowUnrestricted bool
	workspaceDir      string
	workspaceDirs     map[string]string // per-agent workspace overrides
}

func withAgents(agents map[string]*types.AgentConfig) harnessOption {
	return func(h *testHarness) { h.agents = agents }
}

func withAllowUnrestricted() harnessOption {
	return func(h *testHarness) { h.allowUnrestricted = true }
}

func withSecrets(secrets map[string]string) harnessOption {
	return func(h *testHarness) { h.secrets = &mockSecretResolver{secrets: secrets} }
}

func withSandboxResponse(resp *ktp.ToolResponse) harnessOption {
	return func(h *testHarness) { h.sandbox.response = resp }
}

func withSandboxError(err error) harnessOption {
	return func(h *testHarness) { h.sandbox.executeErr = err }
}

func withHostPaths(agentID string, cfg *file.HostPathConfig) harnessOption {
	return func(h *testHarness) {
		if h.hostPaths == nil {
			h.hostPaths = make(map[string]*file.HostPathConfig)
		}
		h.hostPaths[agentID] = cfg
	}
}

func withWorkspaceDir(agentID, dir string) harnessOption {
	return func(h *testHarness) {
		if h.workspaceDirs == nil {
			h.workspaceDirs = make(map[string]string)
		}
		h.workspaceDirs[agentID] = dir
	}
}

func withHostFSConfig(agentID string, cfg *hostfs.HostPathConfig) harnessOption {
	return func(h *testHarness) {
		if h.hostFSConfigs == nil {
			h.hostFSConfigs = make(map[string]*hostfs.HostPathConfig)
		}
		h.hostFSConfigs[agentID] = cfg
	}
}

func withRESTEndpoints(agentID string, endpoints []types.RESTAPIEndpoint) harnessOption {
	return func(h *testHarness) {
		if h.restEndpoints == nil {
			h.restEndpoints = make(map[string][]types.RESTAPIEndpoint)
		}
		h.restEndpoints[agentID] = endpoints
	}
}

func withRESTTransport(rt http.RoundTripper) harnessOption {
	return func(h *testHarness) { h.restTransport = rt }
}

// enableSandbox wires the mock sandbox into the executor.
// Call after newTestHarness for tests that need sandbox routing.
func (h *testHarness) enableSandbox() {
	h.executor.SetSandbox(h.sandbox)
}

func newTestHarness(t *testing.T, opts ...harnessOption) *testHarness {
	t.Helper()

	h := &testHarness{
		sandbox:  &mockSandboxExecutor{},
		secrets:  &mockSecretResolver{secrets: make(map[string]string)},
		secStore: &mockSecurityStore{},
		memStore: newMockMemoryStore(),
		agents: map[string]*types.AgentConfig{
			"worker-1": {
				ID: "worker-1", Template: "worker",
				CapabilityGrants: workerCapabilities(),
			},
			"reader-1": {
				ID: "reader-1", Template: "reader",
				CapabilityGrants: readerCapabilities(),
			},
			"admin-1": {
				ID: "admin-1", Template: "admin",
				CapabilityGrants: adminCapabilities(),
			},
		},
		hostPaths:     make(map[string]*file.HostPathConfig),
		hostFSConfigs: make(map[string]*hostfs.HostPathConfig),
		restEndpoints: make(map[string][]types.RESTAPIEndpoint),
		workspaceDirs: make(map[string]string),
	}

	// Apply options (may override defaults).
	for _, opt := range opts {
		opt(h)
	}

	// Set up workspace.
	h.workspaceDir = t.TempDir()

	// Set up audit store.
	h.db = openTestDB(t)
	auditStore := &dbAuditStore{db: h.db}
	auditLogger := ktp.NewStoreAuditLogger(auditStore)

	// Workspace resolver: per-agent override or default.
	workspaceFunc := func(agentID string) (string, error) {
		if dir, ok := h.workspaceDirs[agentID]; ok {
			return dir, nil
		}
		return h.workspaceDir, nil
	}

	// Tier resolver.
	tierFunc := func(agentID string) (string, error) {
		agent, ok := h.agents[agentID]
		if !ok {
			return "", fmt.Errorf("agent %s not found", agentID)
		}
		return ktp.ResolveAgentTier(agent.Template), nil
	}

	// Host paths resolver.
	hostPathsFunc := func(agentID string) (*file.HostPathConfig, error) {
		cfg, ok := h.hostPaths[agentID]
		if !ok {
			return nil, nil
		}
		return cfg, nil
	}

	// Allowed commands resolver (return agent's configured commands).
	allowedCmdsFunc := func(agentID string) ([]string, error) {
		agent, ok := h.agents[agentID]
		if !ok {
			return nil, fmt.Errorf("agent %s not found", agentID)
		}
		return agent.ShellAllowedCommands, nil
	}

	// Allowed hosts resolver.
	allowedHostsFunc := func(agentID string) ([]string, error) {
		agent, ok := h.agents[agentID]
		if !ok {
			return nil, fmt.Errorf("agent %s not found", agentID)
		}
		return agent.HTTPAllowedHosts, nil
	}

	// Create real tools.
	fileTool := file.New(workspaceFunc,
		file.WithTierFunc(tierFunc),
		file.WithHostPathsFunc(hostPathsFunc),
	)
	memoryTool := memorytool.New(h.memStore)
	shellTool := shell.New(allowedCmdsFunc, workspaceFunc, tierFunc)
	codeTool := code.New(workspaceFunc)
	httpTool := httptool.New(allowedHostsFunc, httptool.WithTierFunc(tierFunc))

	// Host filesystem tool.
	hostFSAllowlistFunc := func(agentID string) (*hostfs.HostPathConfig, error) {
		cfg, ok := h.hostFSConfigs[agentID]
		if !ok {
			return nil, nil
		}
		return cfg, nil
	}
	hostfsTool := hostfs.New(hostfs.Config{}, hostfs.WithAllowlistFunc(hostFSAllowlistFunc))

	// REST API tool.
	endpointFunc := restapi.EndpointConfigsFunc(func(agentID string) ([]types.RESTAPIEndpoint, error) {
		return h.restEndpoints[agentID], nil
	})
	secretFunc := restapi.SecretResolverFunc(func(ctx context.Context, agentID, teamID, key string) (string, error) {
		return h.secrets.Resolve(ctx, agentID, teamID, key)
	})
	var restapiOpts []restapi.Option
	restapiOpts = append(restapiOpts, restapi.WithTierFunc(restapi.TierFunc(tierFunc)))
	if h.restTransport != nil {
		restapiOpts = append(restapiOpts, restapi.WithTestTransport(h.restTransport))
	}
	restapiTool := restapi.New(endpointFunc, secretFunc, restapiOpts...)
	t.Cleanup(func() { restapiTool.Stop() })

	// Browser tool.
	browserTool := browser.New(browser.Config{},
		browser.WithAllowedHostsFunc(allowedHostsFunc),
	)

	// Register tools.
	h.registry = ktp.NewRegistry()
	tools := []ktp.Tool{fileTool, memoryTool, shellTool, codeTool, httpTool, hostfsTool, restapiTool, browserTool}
	for _, tool := range tools {
		if err := h.registry.Register(tool); err != nil {
			t.Fatalf("register tool: %v", err)
		}
	}

	// Create permission gate.
	agentStore := &mockAgentStore{agents: h.agents}
	gate := ktp.NewPermissionGate(agentStore, auditLogger)
	if h.allowUnrestricted {
		gate.SetAllowUnrestricted(true)
	}
	h.gate = gate

	// Create executor. Sandbox is NOT set by default so tools run in-process.
	// Tests that need sandbox behavior should call h.executor.SetSandbox(h.sandbox).
	h.executor = ktp.NewExecutor(h.registry, gate, auditLogger, ktp.ExecutorConfig{})
	h.executor.SetSecretResolver(h.secrets)

	// Create defense.
	h.defense = security.NewDefense(h.secStore, nil)

	return h
}

// --- Capability grant helpers ---
// The real tool declarations use Resource:"*" in their RequiredCapabilities.
// Template-based tier defaults use "{workspace}/*" which doesn't match "*".
// These explicit grants bridge that gap for integration testing.

func readerCapabilities() []types.Capability {
	return []types.Capability{
		{Tool: "filesystem", Action: "read", Resource: "*"},
		{Tool: "memory", Action: "read", Resource: "*"},
	}
}

func workerCapabilities() []types.Capability {
	return []types.Capability{
		{Tool: "filesystem", Action: "read", Resource: "*"},
		{Tool: "filesystem", Action: "write", Resource: "*"},
		{Tool: "memory", Action: "read", Resource: "*"},
		{Tool: "memory", Action: "write", Resource: "*"},
		{Tool: "network", Action: "read", Resource: "*"},
	}
}

func adminCapabilities() []types.Capability {
	return []types.Capability{
		{Tool: "filesystem", Action: "read", Resource: "*"},
		{Tool: "filesystem", Action: "write", Resource: "*"},
		{Tool: "memory", Action: "read", Resource: "*"},
		{Tool: "memory", Action: "write", Resource: "*"},
		{Tool: "network", Action: "read", Resource: "*"},
		{Tool: "network", Action: "write", Resource: "*"},
		{Tool: "shell", Action: "execute", Resource: "*"},
		{Tool: "process", Action: "execute", Resource: "*"},
		{Tool: "code", Action: "execute", Resource: "*"},
	}
}

func phase10AdminCapabilities() []types.Capability {
	caps := adminCapabilities()
	return append(caps,
		types.Capability{Tool: "host_filesystem", Action: "read", Resource: "*"},
		types.Capability{Tool: "host_filesystem", Action: "write", Resource: "*"},
		types.Capability{Tool: "host_filesystem", Action: "delete", Resource: "*"},
		types.Capability{Tool: "network", Action: "write", Resource: "*"},
	)
}

// =============================================================================
// Phase 7 helpers — mock types, harnesses, and utilities for lifecycle /
// operational subsystem integration tests.
// =============================================================================

// --- Phase 7 mock types (satisfy core.New dependencies) ---

// p7MockGate satisfies permissions.Gate with always-allow.
type p7MockGate struct{}

func (m *p7MockGate) Check(_ context.Context, _ string, _ types.ToolCall) (*permissions.Decision, error) {
	return &permissions.Decision{Allowed: true, Reason: "mock"}, nil
}
func (m *p7MockGate) GetAgentCapabilities(_ context.Context, _ string) ([]types.Capability, error) {
	return nil, nil
}
func (m *p7MockGate) LoadTemplate(_ context.Context, _ string) (*permissions.Template, error) {
	return nil, nil
}
func (m *p7MockGate) ListTemplates(_ context.Context) ([]permissions.Template, error) {
	return nil, nil
}
func (m *p7MockGate) AddOverride(_ context.Context, _ permissions.Override) error { return nil }
func (m *p7MockGate) RemoveOverride(_ context.Context, _ string, _ types.Capability) error {
	return nil
}
func (m *p7MockGate) ListOverrides(_ context.Context, _ string) ([]permissions.Override, error) {
	return nil, nil
}
func (m *p7MockGate) RemoveAllOverrides(_ context.Context, _ string) error { return nil }

// p7MockAuditLogger satisfies audit.Logger.
type p7MockAuditLogger struct {
	mu      sync.Mutex
	entries []types.AuditEntry
}

func (m *p7MockAuditLogger) Log(_ context.Context, entry types.AuditEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, entry)
	return nil
}
func (m *p7MockAuditLogger) Query(_ context.Context, _ audit.Filter) ([]types.AuditEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.entries, nil
}
func (m *p7MockAuditLogger) Stream(_ context.Context, _ string) (<-chan types.AuditEntry, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *p7MockAuditLogger) Subscribe(_ context.Context, _ audit.SubscriptionFilter) (<-chan types.AuditEntry, error) {
	ch := make(chan types.AuditEntry)
	close(ch)
	return ch, nil
}
func (m *p7MockAuditLogger) Close() error { return nil }

// p7MockSpendingTracker satisfies spending.Tracker.
type p7MockSpendingTracker struct{}

func (m *p7MockSpendingTracker) Record(_ context.Context, _ string, _, _ int64, _ float64, _ spending.RecordOptions) error {
	return nil
}
func (m *p7MockSpendingTracker) CheckBudget(_ context.Context, _ string) (*spending.BudgetStatus, error) {
	return &spending.BudgetStatus{WithinBudget: true}, nil
}
func (m *p7MockSpendingTracker) GetSummary(_ context.Context, _ spending.Filter) (*spending.Summary, error) {
	return &spending.Summary{}, nil
}
func (m *p7MockSpendingTracker) GetSlotBreakdown(_ context.Context, _, _ string) ([]spending.SlotUsageSummary, error) {
	return nil, nil
}
func (m *p7MockSpendingTracker) GetProviderBreakdown(_ context.Context, _, _ string) ([]spending.ProviderUsageSummary, error) {
	return nil, nil
}
func (m *p7MockSpendingTracker) SetGlobalLimit(_ context.Context, _ types.SpendingLimits) error {
	return nil
}
func (m *p7MockSpendingTracker) SetAgentLimit(_ context.Context, _ string, _ types.SpendingLimits) error {
	return nil
}
func (m *p7MockSpendingTracker) GetDailyTimeSeries(_ context.Context, _ string, _ int) ([]spending.DailyUsage, error) {
	return nil, nil
}

// p7MockToolsRegistry satisfies tools.Registry.
type p7MockToolsRegistry struct{}

func (m *p7MockToolsRegistry) Register(_ tools.Tool) error      { return nil }
func (m *p7MockToolsRegistry) Get(_ string) (tools.Tool, error) { return nil, types.ErrNotFound }
func (m *p7MockToolsRegistry) List() []tools.Declaration        { return nil }
func (m *p7MockToolsRegistry) GetDeclaration(_ string) (*tools.Declaration, error) {
	return nil, types.ErrNotFound
}

// p7MockProvider satisfies models.Provider.
type p7MockProvider struct {
	name     string
	response *models.CompletionResponse
}

func newP7MockProvider(name string) *p7MockProvider {
	return &p7MockProvider{
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

func (m *p7MockProvider) Complete(_ context.Context, _ models.CompletionRequest) (*models.CompletionResponse, error) {
	return m.response, nil
}
func (m *p7MockProvider) Stream(_ context.Context, _ models.CompletionRequest) (<-chan models.StreamChunk, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *p7MockProvider) ListModels(_ context.Context) ([]models.ModelInfo, error) { return nil, nil }
func (m *p7MockProvider) Name() string                                             { return m.name }

// --- Phase 7 store harness ---

// p7StoreHarness opens a real PostgresStore for integration tests.
type p7StoreHarness struct {
	store *postgres.PostgresStore
	db    *sql.DB
}

func newP7StoreHarness(t *testing.T) *p7StoreHarness {
	t.Helper()
	tdb := testutil.RequirePostgres(t)

	return &p7StoreHarness{
		store: tdb.Store,
		db:    tdb.DB,
	}
}

// --- Phase 7 core harness ---

// p7CoreHarness wraps a real core.Kyvik instance with minimal mock dependencies.
type p7CoreHarness struct {
	kyvik    *core.Kyvik
	sh       *p7StoreHarness
	provider *p7MockProvider
	audit    *p7MockAuditLogger
}

func newP7CoreHarness(t *testing.T) *p7CoreHarness {
	t.Helper()

	sh := newP7StoreHarness(t)
	prov := newP7MockProvider("test-provider")
	al := &p7MockAuditLogger{}

	k := core.New(&testutil.NoCloseStore{Store: sh.store}, &p7MockGate{}, nil, al, &p7MockToolsRegistry{}, &p7MockSpendingTracker{})
	k.RegisterModel(prov)

	// Set up a real queue backed by the same DB.
	q := mustNewPQ(sh.db)
	k.SetQueue(q)

	t.Cleanup(func() {
		k.Shutdown(context.Background())
	})

	return &p7CoreHarness{
		kyvik:    k,
		sh:       sh,
		provider: prov,
		audit:    al,
	}
}

// --- Phase 7 helper functions ---

// waitForStatus polls until an agent reaches the desired status or times out.
func waitForStatus(t *testing.T, k *core.Kyvik, agentID string, want types.AgentStatus, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for {
		status, err := k.GetAgentStatus(ctx, agentID)
		if err != nil {
			t.Fatalf("GetAgentStatus(%s): %v", agentID, err)
		}
		if status == want {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("agent %s did not reach status %s within %s (current: %s)", agentID, want, timeout, status)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// waitForStatusOneOf waits until the agent reaches one of the given statuses.
// Useful after KillAgent where the goroutine cleanup can race with the status write.
func waitForStatusOneOf(t *testing.T, k *core.Kyvik, agentID string, want []types.AgentStatus, timeout time.Duration) types.AgentStatus {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for {
		status, err := k.GetAgentStatus(ctx, agentID)
		if err != nil {
			t.Fatalf("GetAgentStatus(%s): %v", agentID, err)
		}
		for _, w := range want {
			if status == w {
				return status
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("agent %s did not reach any of %v within %s (current: %s)", agentID, want, timeout, status)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// p7AgentConfig returns a standard test AgentConfig for Phase 7 tests.
func p7AgentConfig(id string) types.AgentConfig {
	return types.AgentConfig{
		ID:           id,
		Name:         "P7 Agent " + id,
		SystemPrompt: "You are a test agent.",
		ModelConfig: types.ModelConfig{
			Provider: "test-provider",
			Model:    "test-model",
		},
		Template: "worker",
	}
}

// p7SeedAgent inserts an agent into the real store for tests that need one
// present without going through core.StartAgent.
func p7SeedAgent(t *testing.T, s *postgres.PostgresStore, id, name string) {
	t.Helper()
	ctx := context.Background()
	err := s.CreateAgent(ctx, types.AgentConfig{
		ID:           id,
		Name:         name,
		SystemPrompt: "Test agent",
		ModelConfig: types.ModelConfig{
			Provider: "test-provider",
			Model:    "test-model",
		},
		Template:     "worker",
		DesiredState: types.DesiredStateStopped,
		ActualState:  types.AgentStatusStopped,
	})
	if err != nil {
		t.Fatalf("p7SeedAgent(%s): %v", id, err)
	}
}

// --- Phase 7 pruner helpers ---

// p7PrunerDB returns a PostgreSQL database connection for pruner tests.
func p7PrunerDB(t testing.TB) *sql.DB {
	t.Helper()
	return testutil.RequirePostgres(t).DB
}


// p7StateStore implements retention.StateStore backed by a *sql.DB.
type p7StateStore struct{ db *sql.DB }

func (s *p7StateStore) GetSystemState(_ context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRow("SELECT value FROM system_state WHERE key = $1", key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

func (s *p7StateStore) SetSystemState(_ context.Context, key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO system_state (key, value, updated_at)
		 VALUES ($1, $2, CURRENT_TIMESTAMP)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=CURRENT_TIMESTAMP`,
		key, value)
	return err
}

// =============================================================================
// Scenario helpers — enhanced mock provider and full-system harness for
// comprehensive integration tests (phase 12.5).
// =============================================================================

// scenarioMockProvider is an enhanced mock that supports a response queue,
// per-agent overrides, and call recording for assertions.
type scenarioMockProvider struct {
	name     string
	mu       sync.Mutex
	calls    []models.CompletionRequest
	queue    []models.CompletionResponse  // pop-first; used when non-empty
	fallback *models.CompletionResponse   // used when queue is empty
	perAgent map[string][]models.CompletionResponse
}

func newScenarioMockProvider(name string) *scenarioMockProvider {
	return &scenarioMockProvider{
		name: name,
		fallback: &models.CompletionResponse{
			Content:   "mock response",
			TokensIn:  10,
			TokensOut: 20,
			Cost:      0.001,
			Model:     "test-model",
		},
		perAgent: make(map[string][]models.CompletionResponse),
	}
}

func (m *scenarioMockProvider) Complete(_ context.Context, req models.CompletionRequest) (*models.CompletionResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, req)

	// Extract agent ID from system message if possible (convention: "agent_id:xxx" prefix).
	agentID := ""
	for _, msg := range req.Messages {
		if msg.Role == "system" && len(msg.Content) > 9 {
			agentID = msg.Content // best-effort; tests use per-agent queue keyed by config
			break
		}
	}

	// Per-agent overrides.
	if agentID != "" {
		if q, ok := m.perAgent[agentID]; ok && len(q) > 0 {
			resp := q[0]
			m.perAgent[agentID] = q[1:]
			return &resp, nil
		}
	}

	// Global queue.
	if len(m.queue) > 0 {
		resp := m.queue[0]
		m.queue = m.queue[1:]
		return &resp, nil
	}

	// Fallback — copy and append call index to make each response unique
	// so the circuit breaker's "identical response" detector doesn't trip.
	callIdx := len(m.calls)
	if m.fallback != nil {
		resp := *m.fallback
		resp.Content = fmt.Sprintf("%s [%d]", resp.Content, callIdx)
		return &resp, nil
	}

	return &models.CompletionResponse{
		Content:   fmt.Sprintf("default [%d]", callIdx),
		TokensIn:  10,
		TokensOut: 20,
		Cost:      0.001,
		Model:     "test-model",
	}, nil
}

func (m *scenarioMockProvider) Stream(_ context.Context, _ models.CompletionRequest) (<-chan models.StreamChunk, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *scenarioMockProvider) ListModels(_ context.Context) ([]models.ModelInfo, error) {
	return nil, nil
}

func (m *scenarioMockProvider) Name() string { return m.name }

// PushResponse adds a response to the global queue.
func (m *scenarioMockProvider) PushResponse(resp models.CompletionResponse) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queue = append(m.queue, resp)
}

// PushAgentResponse adds a response to a per-agent queue.
func (m *scenarioMockProvider) PushAgentResponse(agentID string, resp models.CompletionResponse) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.perAgent[agentID] = append(m.perAgent[agentID], resp)
}

// SetFallback overrides the default response returned when queues are empty.
func (m *scenarioMockProvider) SetFallback(resp *models.CompletionResponse) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fallback = resp
}

// CallCount returns the number of Complete() calls recorded.
func (m *scenarioMockProvider) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

// LastCall returns the most recent CompletionRequest (panics if no calls).
func (m *scenarioMockProvider) LastCall() models.CompletionRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls[len(m.calls)-1]
}

// Calls returns a copy of all recorded CompletionRequests.
func (m *scenarioMockProvider) Calls() []models.CompletionRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]models.CompletionRequest, len(m.calls))
	copy(out, m.calls)
	return out
}

// --- fullHarness: wires ALL subsystems for comprehensive scenario tests ---

type fullHarnessOption func(*fullHarness)

type fullHarness struct {
	store     *postgres.PostgresStore
	db        *sql.DB
	kyvik     *core.Kyvik
	audit     *audit.StoreLogger
	gate      *permissions.StoreGate
	spending  *spending.StoreTracker
	users     *users.Service
	apikeys   *apikeys.Service
	secrets   *secrets.Vault
	templates *templates.Service
	teamBus   *teams.Bus
	teamMgr   *teams.Manager
	breaker   *circuitbreaker.Manager
	provider  *scenarioMockProvider
	queue     *queue.PostgresQueue
	server    *httptest.Server
	client    *http.Client
	tripCh    chan circuitbreaker.TripResult
}

func withProviderFallback(resp models.CompletionResponse) fullHarnessOption {
	return func(h *fullHarness) {
		h.provider.SetFallback(&resp)
	}
}

func newFullHarness(t *testing.T, opts ...fullHarnessOption) *fullHarness {
	t.Helper()

	tdb := testutil.RequirePostgres(t)
	s := tdb.Store

	al := audit.NewStoreLoggerWithPollInterval(s, 50*time.Millisecond, 10)
	gate := permissions.NewStoreGate(s, al, "")
	sp := spending.NewStoreTracker(s, al, "test-model")

	prov := newScenarioMockProvider("test-provider")

	k := core.New(&testutil.NoCloseStore{Store: s}, gate, nil, al, &p7MockToolsRegistry{}, sp)
	k.RegisterModel(prov)

	// History + memory.
	historyStore := history.New(s.DB())
	k.SetHistory(historyStore)
	k.SetConversationStore(historyStore)
	k.SetMemory(memory.New(s.DB()))

	// Queue.
	q := mustNewPQ(s.DB())
	k.SetQueue(q)

	// Teams bus + manager.
	teamBus := teams.NewBus(s.DB(), al)
	teamMgr := teams.NewManager(s, teamBus, al)
	k.SetInternalBus(teamBus)
	k.SetTeamManager(teamMgr)

	// NOTE: We intentionally do NOT call k.RegisterChannel for the bus adapter
	// or webui adapter. Registering channels causes StartAgent to spawn an
	// outboxConsumer goroutine that drains runner.outbox, preventing
	// ReceiveMessage() from reading responses in tests.
	// The bus/team manager are wired via SetInternalBus/SetTeamManager,
	// and the webui adapter is passed to the web server via web.WithWebUI.
	_ = busadapter.New(teamBus) // kept to verify import compiles

	// Circuit breaker.
	tripCh := make(chan circuitbreaker.TripResult, 16)
	breakerMgr := circuitbreaker.NewManager(func(tr circuitbreaker.TripResult) {
		select {
		case tripCh <- tr:
		default:
		}
	})
	k.SetCircuitBreakerManager(breakerMgr)

	// Templates.
	tmplSvc := templates.New(s)
	k.SetTemplateService(tmplSvc)

	// Web UI adapter (passed to web server only, NOT registered on Kyvik).
	webuiAdapter := webui.New()

	// Users, API keys, secrets.
	us := users.New(s, users.AuthConfig{SessionTTL: time.Hour, MaxSessionsPerUser: 3})
	keySvc := apikeys.New(s)
	secretVault := secrets.NewVault(s.DB(), []byte("test-master-key-32-bytes-long!!!"), al)

	h := &fullHarness{
		store:     s,
		db:        s.DB(),
		kyvik:     k,
		audit:     al,
		gate:      gate,
		spending:  sp,
		users:     us,
		apikeys:   keySvc,
		secrets:   secretVault,
		templates: tmplSvc,
		teamBus:   teamBus,
		teamMgr:   teamMgr,
		breaker:   breakerMgr,
		provider:  prov,
		queue:     q,
		tripCh:    tripCh,
	}

	// Mark setup as complete so SetupCheck middleware doesn't redirect.
	_ = s.SetSystemState(context.Background(), "setup_complete", "true")

	// Apply options.
	for _, opt := range opts {
		opt(h)
	}

	// Set up web server.
	handler := web.SetupRoutes(k,
		web.WithWebUI(webuiAdapter),
		web.WithAuthProvider(local.New(us)),
		web.WithAPIKeys(keySvc),
		web.WithTemplateService(tmplSvc),
		web.WithSecretStore(secretVault),
		web.WithAuditStreamConfig(config.AuditStreamConfig{
			MaxConnections: 1,
			HeartbeatSec:   1,
		}),
	)

	srv := newHTTPServer(t, handler)
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	h.server = srv
	h.client = client

	t.Cleanup(func() {
		srv.Close()
		_ = al.Close()
		k.Shutdown(context.Background())
		// Do NOT close s — it is a shared PostgresStore used across tests.
	})

	return h
}

// --- fullHarness helper methods ---

func (h *fullHarness) seedAgent(t *testing.T, id, name, template string) {
	t.Helper()
	now := time.Now().UTC()
	if err := h.store.CreateAgent(context.Background(), types.AgentConfig{
		ID:           id,
		Name:         name,
		SystemPrompt: "You are a test agent.",
		ModelConfig:  types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:     template,
		DesiredState: types.DesiredStateStopped,
		ActualState:  types.AgentStatusStopped,
		CreatedAt:    now,
		UpdatedAt:    now,
	}); err != nil {
		t.Fatalf("seedAgent(%s): %v", id, err)
	}
}

func (h *fullHarness) startAgent(t *testing.T, id string) {
	t.Helper()
	agent, err := h.store.GetAgent(context.Background(), id)
	if err != nil {
		t.Fatalf("GetAgent(%s): %v", id, err)
	}
	cfg := *agent
	// StartAgent calls CreateAgent internally. Delete the existing row to avoid
	// UNIQUE constraint violations when the agent was previously seeded or
	// was left behind by an agent goroutine that hasn't fully cleaned up yet.
	_ = h.store.DeleteAgent(context.Background(), id)
	if err := h.kyvik.StartAgent(context.Background(), cfg); err != nil {
		t.Fatalf("StartAgent(%s): %v", id, err)
	}
	waitForStatus(t, h.kyvik, id, types.AgentStatusRunning, 3*time.Second)
}

func (h *fullHarness) sendAndReceive(t *testing.T, agentID, content string, timeout time.Duration) types.Message {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if err := h.kyvik.SendMessage(ctx, agentID, types.Message{
		Role:    "user",
		Content: content,
	}); err != nil {
		t.Fatalf("SendMessage(%s): %v", agentID, err)
	}

	resp, err := h.kyvik.ReceiveMessage(ctx, agentID)
	if err != nil {
		t.Fatalf("ReceiveMessage(%s): %v", agentID, err)
	}
	return resp
}

func (h *fullHarness) seedUser(t *testing.T, username, password string, isAdmin bool) *types.User {
	t.Helper()
	user, err := h.users.CreateUser(context.Background(), users.CreateUserParams{
		Username: username,
		Password: password,
		IsAdmin:  isAdmin,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return user
}

func (h *fullHarness) createAPIKey(t *testing.T, name, scope string, agentIDs []string) string {
	t.Helper()
	result, err := h.apikeys.Create(context.Background(), name, scope, agentIDs, nil)
	if err != nil {
		t.Fatalf("Create key: %v", err)
	}
	return result.PlainKey
}

func (h *fullHarness) login(t *testing.T, username, password string) *http.Cookie {
	t.Helper()
	form := url.Values{}
	form.Set("username", username)
	form.Set("password", password)
	resp, err := h.client.PostForm(h.server.URL+"/login", form)
	if err != nil {
		t.Fatalf("login request: %v", err)
	}
	defer resp.Body.Close()
	for _, c := range resp.Cookies() {
		if c.Name == "kyvik_session" {
			return c
		}
	}
	t.Fatalf("no session cookie")
	return nil
}

func (h *fullHarness) apiRequest(t *testing.T, method, path, token string, body any) *http.Response {
	t.Helper()
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		bodyReader = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, h.server.URL+path, bodyReader)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("api %s %s: %v", method, path, err)
	}
	return resp
}

func (h *fullHarness) assertAuditContains(t *testing.T, agentID, action string) {
	t.Helper()
	// Brief delay for async audit logger.
	time.Sleep(100 * time.Millisecond)

	entries, err := h.audit.Query(context.Background(), audit.Filter{AgentID: agentID, Limit: 500})
	if err != nil {
		t.Fatalf("audit query: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Action, action) {
			return
		}
	}
	// Retry once after more time.
	time.Sleep(200 * time.Millisecond)
	entries, err = h.audit.Query(context.Background(), audit.Filter{AgentID: agentID, Limit: 500})
	if err != nil {
		t.Fatalf("audit query (retry): %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Action, action) {
			return
		}
	}
	t.Fatalf("audit for agent %s missing action containing %q (found %d entries)", agentID, action, len(entries))
}

func (h *fullHarness) createTeam(t *testing.T, id, name, leaderID string, memberIDs []string) {
	t.Helper()
	if err := h.teamMgr.CreateTeam(context.Background(), types.Team{
		ID:        id,
		Name:      name,
		LeaderID:  leaderID,
		MemberIDs: memberIDs,
	}); err != nil {
		t.Fatalf("CreateTeam(%s): %v", id, err)
	}
}

func (h *fullHarness) createGroup(t *testing.T, name string) string {
	t.Helper()
	g, err := h.users.CreateGroup(context.Background(), name, "desc")
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	return g.ID
}

func (h *fullHarness) assignUserRole(t *testing.T, userID, groupID, role string) {
	t.Helper()
	if err := h.users.SetUserRoleInGroup(context.Background(), userID, groupID, role); err != nil {
		t.Fatalf("SetUserRoleInGroup: %v", err)
	}
}

func (h *fullHarness) authedGet(t *testing.T, path string, cookie *http.Cookie) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", h.server.URL+path, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

func (h *fullHarness) authedPostForm(t *testing.T, path string, cookie *http.Cookie, form url.Values) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", h.server.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}
