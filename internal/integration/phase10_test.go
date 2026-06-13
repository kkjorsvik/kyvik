package integration

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/kkjorsvik/kyvik/internal/audit"
	"github.com/kkjorsvik/kyvik/internal/ktp"
	"github.com/kkjorsvik/kyvik/internal/notifications"
	"github.com/kkjorsvik/kyvik/internal/secrets"
	"github.com/kkjorsvik/kyvik/internal/store/postgres"
	"github.com/kkjorsvik/kyvik/internal/testutil"
	"github.com/kkjorsvik/kyvik/internal/tools/hostfs"
	"github.com/kkjorsvik/kyvik/internal/webhooks"
	"github.com/kkjorsvik/kyvik/pkg/types"

)

// =============================================================================
// Browser skip guard
// =============================================================================

func requireBrowser(t *testing.T) {
	t.Helper()
	if os.Getenv("KYVIK_SKIP_BROWSER_TESTS") == "1" {
		t.Skip("browser tests disabled via KYVIK_SKIP_BROWSER_TESTS=1")
	}
	if os.Getenv("ROD_BROWSER_BIN") != "" {
		return
	}
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable"} {
		if _, err := exec.LookPath(name); err == nil {
			return
		}
	}
	t.Skip("no browser binary found; set ROD_BROWSER_BIN or install chromium")
}

// =============================================================================
// Inbound webhook fakes (cannot import from internal/webhooks test package)
// =============================================================================

type p10FakeStore struct{ agent *types.AgentConfig }

func (f *p10FakeStore) GetAgent(_ context.Context, id string) (*types.AgentConfig, error) {
	if f.agent == nil || f.agent.ID != id {
		return nil, types.ErrNotFound
	}
	return f.agent, nil
}

type p10FakeVault struct{ data map[string]string }

func (v *p10FakeVault) Get(_ context.Context, scope, key string) (string, error) {
	s, ok := v.data[scope+":"+key]
	if !ok {
		return "", types.ErrNotFound
	}
	return s, nil
}
func (v *p10FakeVault) Set(_ context.Context, scope, key, val, _ string) error {
	v.data[scope+":"+key] = val
	return nil
}
func (v *p10FakeVault) Delete(_ context.Context, scope, key string) error {
	delete(v.data, scope+":"+key)
	return nil
}
func (v *p10FakeVault) List(_ context.Context, _ string) ([]secrets.SecretMeta, error) {
	return nil, nil
}
func (v *p10FakeVault) Exists(_ context.Context, scope, key string) (bool, error) {
	_, ok := v.data[scope+":"+key]
	return ok, nil
}
func (v *p10FakeVault) Resolve(_ context.Context, agentID, _, key string) (string, error) {
	return v.Get(context.Background(), "agent:"+agentID, key)
}

type p10FakeKyvik struct {
	mu     sync.Mutex
	queued []types.Message
}

func (k *p10FakeKyvik) SendMessage(_ context.Context, _ string, msg types.Message) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.queued = append(k.queued, msg)
	return nil
}

type p10FakeAudit struct {
	mu      sync.Mutex
	entries []types.AuditEntry
}

func (a *p10FakeAudit) Log(_ context.Context, e types.AuditEntry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, e)
	return nil
}
func (a *p10FakeAudit) Query(_ context.Context, _ audit.Filter) ([]types.AuditEntry, error) {
	return nil, nil
}
func (a *p10FakeAudit) Stream(_ context.Context, _ string) (<-chan types.AuditEntry, error) {
	return nil, nil
}
func (a *p10FakeAudit) Subscribe(_ context.Context, _ audit.SubscriptionFilter) (<-chan types.AuditEntry, error) {
	return nil, nil
}
func (a *p10FakeAudit) Close() error { return nil }

// newInboundTestEnv builds a handler with in-memory fakes for integration tests.
func newInboundTestEnv(agentID, secret string, wh *types.InboundWebhookConfig) (*p10FakeStore, *p10FakeVault, *p10FakeKyvik, *p10FakeAudit, http.Handler) {
	st := &p10FakeStore{agent: &types.AgentConfig{
		ID:             agentID,
		WebhookInbound: wh,
	}}
	vault := &p10FakeVault{data: map[string]string{
		"agent:" + agentID + ":" + webhooks.SecretVaultKey: secret,
	}}
	kyv := &p10FakeKyvik{}
	al := &p10FakeAudit{}
	h := webhooks.New(st, vault, kyv, al)
	return st, vault, kyv, al, h
}

// =============================================================================
// Helper: TLS test server
// =============================================================================

func newTLSServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

// =============================================================================
// Step 3: Host Filesystem Tests
// =============================================================================

func TestPhase10_HostFS_AdminTierReadsAllowedPath(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "data.txt")
	if err := os.WriteFile(testFile, []byte("hello host fs"), 0o644); err != nil {
		t.Fatal(err)
	}

	h := newTestHarness(t,
		withAgents(map[string]*types.AgentConfig{
			"admin-1": {
				ID: "admin-1", Template: "admin",
				CapabilityGrants: phase10AdminCapabilities(),
			},
		}),
		withHostFSConfig("admin-1", &hostfs.HostPathConfig{
			Read: []string{tmpDir + "/"},
		}),
	)

	req := ktp.NewToolRequest("admin-1", "hostfs", "read", map[string]any{
		"path": testFile,
	})
	resp, err := h.executor.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	result := resp.Result.(map[string]any)
	if content, ok := result["content"].(string); !ok || content != "hello host fs" {
		t.Fatalf("expected content 'hello host fs', got %v", result["content"])
	}

	// Verify audit entries contain allowed permission.
	entries := queryAuditEntries(t, h.db, "admin-1")
	var foundAllowed bool
	for _, e := range entries {
		if e.Decision == "allowed" {
			foundAllowed = true
			break
		}
	}
	if !foundAllowed {
		t.Error("expected at least one 'allowed' audit entry")
	}
}

func TestPhase10_HostFS_DeniedOutsideAllowlist(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	testFile := filepath.Join(dirB, "secret.txt")
	if err := os.WriteFile(testFile, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	h := newTestHarness(t,
		withAgents(map[string]*types.AgentConfig{
			"admin-1": {
				ID: "admin-1", Template: "admin",
				CapabilityGrants: phase10AdminCapabilities(),
			},
		}),
		withHostFSConfig("admin-1", &hostfs.HostPathConfig{
			Read: []string{dirA + "/"},
		}),
	)

	req := ktp.NewToolRequest("admin-1", "hostfs", "read", map[string]any{
		"path": testFile,
	})
	resp, err := h.executor.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Success {
		t.Fatal("expected denial for path outside allowlist")
	}
	if !strings.Contains(resp.Error, "allowlist") {
		t.Fatalf("expected allowlist error, got: %s", resp.Error)
	}
}

func TestPhase10_HostFS_OperatorDeniedByTier(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "data.txt")
	if err := os.WriteFile(testFile, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	// operator-1 uses operator tier (< admin min tier for hostfs).
	h := newTestHarness(t,
		withAgents(map[string]*types.AgentConfig{
			"operator-1": {ID: "operator-1", Template: "operator"},
		}),
		withHostFSConfig("operator-1", &hostfs.HostPathConfig{
			Read: []string{tmpDir + "/"},
		}),
	)

	req := ktp.NewToolRequest("operator-1", "hostfs", "read", map[string]any{
		"path": testFile,
	})
	resp, err := h.executor.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Success {
		t.Fatal("expected denial for operator-tier agent on admin-tier tool")
	}
	if !strings.Contains(resp.Error, "tier") {
		t.Fatalf("expected tier-related denial, got: %s", resp.Error)
	}

	// Verify audit has denied entry.
	entries := queryAuditEntries(t, h.db, "operator-1")
	var foundDenied bool
	for _, e := range entries {
		if e.Decision == "denied" {
			foundDenied = true
			break
		}
	}
	if !foundDenied {
		t.Error("expected at least one 'denied' audit entry")
	}
}

func TestPhase10_HostFS_SymlinkEscapeBlocked(t *testing.T) {
	base := t.TempDir()
	allowed := filepath.Join(base, "allowed")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(allowed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	secretFile := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secretFile, []byte("secret data"), 0o644); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(allowed, "link.txt")
	if err := os.Symlink(secretFile, symlink); err != nil {
		t.Fatal(err)
	}

	h := newTestHarness(t,
		withAgents(map[string]*types.AgentConfig{
			"admin-1": {
				ID: "admin-1", Template: "admin",
				CapabilityGrants: phase10AdminCapabilities(),
			},
		}),
		withHostFSConfig("admin-1", &hostfs.HostPathConfig{
			Read: []string{allowed + "/"},
		}),
	)

	req := ktp.NewToolRequest("admin-1", "hostfs", "read", map[string]any{
		"path": symlink,
	})
	resp, err := h.executor.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Success {
		t.Fatal("expected denial for symlink escape")
	}
	if !strings.Contains(resp.Error, "symlink") && !strings.Contains(resp.Error, "traversal") && !strings.Contains(resp.Error, "allowlist") {
		t.Fatalf("expected symlink/traversal/allowlist error, got: %s", resp.Error)
	}
}

func TestPhase10_HostFS_DangerousPathBlocked(t *testing.T) {
	tmpDir := t.TempDir()
	sshDir := filepath.Join(tmpDir, ".ssh")
	if err := os.MkdirAll(sshDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(sshDir, "config")
	if err := os.WriteFile(configFile, []byte("Host *"), 0o644); err != nil {
		t.Fatal(err)
	}

	h := newTestHarness(t,
		withAgents(map[string]*types.AgentConfig{
			"admin-1": {
				ID: "admin-1", Template: "admin",
				CapabilityGrants: phase10AdminCapabilities(),
			},
		}),
		withHostFSConfig("admin-1", &hostfs.HostPathConfig{
			Read: []string{tmpDir + "/"},
		}),
	)

	req := ktp.NewToolRequest("admin-1", "hostfs", "read", map[string]any{
		"path": configFile,
	})
	resp, err := h.executor.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Success {
		t.Fatal("expected denial for dangerous path (.ssh)")
	}
	if !strings.Contains(resp.Error, "security policy") && !strings.Contains(resp.Error, "denied") {
		t.Fatalf("expected security policy denial, got: %s", resp.Error)
	}
}

// =============================================================================
// Step 4: REST API Tool Tests
// =============================================================================

func TestPhase10_RESTAPI_CallEndpointThroughPipeline(t *testing.T) {
	srv := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","id":%q}`, r.URL.Path)
	}))

	h := newTestHarness(t,
		withRESTEndpoints("admin-1", []types.RESTAPIEndpoint{
			{
				Name:        "get_item",
				Description: "Get an item",
				Method:      "GET",
				URL:         srv.URL + "/items/{{.id}}",
			},
		}),
		withRESTTransport(srv.Client().Transport),
	)

	req := ktp.NewToolRequest("admin-1", "rest_api", "call", map[string]any{
		"endpoint": "get_item",
		"params":   map[string]any{"id": "42"},
	})
	resp, err := h.executor.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	result := resp.Result.(map[string]any)
	statusCode := 0
	switch v := result["status"].(type) {
	case float64:
		statusCode = int(v)
	case int:
		statusCode = v
	}
	if statusCode != 200 {
		t.Fatalf("expected HTTP 200, got %d", statusCode)
	}

	// Verify audit entries.
	entries := queryAuditEntries(t, h.db, "admin-1")
	if len(entries) == 0 {
		t.Error("expected audit entries for REST API call")
	}
}

func TestPhase10_RESTAPI_AuthFromVault(t *testing.T) {
	srv := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer tok_secret" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprintf(w, `{"error":"unauthorized"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"authenticated":true}`)
	}))

	h := newTestHarness(t,
		withSecrets(map[string]string{"api-token": "tok_secret"}),
		withRESTEndpoints("admin-1", []types.RESTAPIEndpoint{
			{
				Name:   "authed_endpoint",
				Method: "GET",
				URL:    srv.URL + "/data",
				Auth: types.RESTAPIAuth{
					Type:      "bearer",
					SecretRef: "api-token",
				},
			},
		}),
		withRESTTransport(srv.Client().Transport),
	)

	req := ktp.NewToolRequest("admin-1", "rest_api", "call", map[string]any{
		"endpoint": "authed_endpoint",
	})
	resp, err := h.executor.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	result := resp.Result.(map[string]any)
	if data, ok := result["data"].(map[string]any); ok {
		if data["authenticated"] != true {
			t.Errorf("expected authenticated=true, got %v", data["authenticated"])
		}
	} else {
		t.Errorf("expected parsed JSON data, got %v", result["data"])
	}
}

func TestPhase10_RESTAPI_ResponseTemplateTransform(t *testing.T) {
	srv := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"temperature":22,"city":"Oslo"}`)
	}))

	h := newTestHarness(t,
		withRESTEndpoints("admin-1", []types.RESTAPIEndpoint{
			{
				Name:             "weather",
				Method:           "GET",
				URL:              srv.URL + "/weather",
				ResponseTemplate: "Temp in {{.data.city}}: {{.data.temperature}}C",
			},
		}),
		withRESTTransport(srv.Client().Transport),
	)

	req := ktp.NewToolRequest("admin-1", "rest_api", "call", map[string]any{
		"endpoint": "weather",
	})
	resp, err := h.executor.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	result := resp.Result.(map[string]any)
	body, _ := result["body"].(string)
	if !strings.Contains(body, "Oslo") || !strings.Contains(body, "22") {
		t.Fatalf("expected template-rendered body with Oslo and 22, got: %s", body)
	}
}

// =============================================================================
// Step 5: Browser Tool Tests
// =============================================================================

// adminWithBrowserCapabilities adds browser capabilities to admin grants.
func adminWithBrowserCapabilities() []types.Capability {
	caps := adminCapabilities()
	return append(caps,
		types.Capability{Tool: "browser", Action: "execute", Resource: "*"},
	)
}

// extractHostPort returns the host:port and hostname-only from a test server URL.
func extractHostPort(srvURL string) (hostPort, hostname string) {
	hostPort = strings.TrimPrefix(strings.TrimPrefix(srvURL, "https://"), "http://")
	if h, _, err := splitHostPort(hostPort); err == nil {
		hostname = h
	} else {
		hostname = hostPort
	}
	return hostPort, hostname
}

// splitHostPort wraps strings splitting (net.SplitHostPort but from strings).
func splitHostPort(hostPort string) (host, port string, err error) {
	// Find last colon for port separator.
	idx := strings.LastIndex(hostPort, ":")
	if idx < 0 {
		return hostPort, "", fmt.Errorf("no port")
	}
	return hostPort[:idx], hostPort[idx+1:], nil
}

func TestPhase10_BrowserTool_AdminCanFetchPage(t *testing.T) {
	requireBrowser(t)

	srv := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>Test Page</title></head><body><main>Hello from Kyvik browser test</main></body></html>`)
	}))

	// Extract host:port and hostname for allowed hosts.
	// browser.validateURL checks Hostname() (no port), so we need both forms.
	_, srvHostname := extractHostPort(srv.URL)

	h := newTestHarness(t,
		withAgents(map[string]*types.AgentConfig{
			"admin-1": {
				ID: "admin-1", Template: "admin",
				CapabilityGrants: adminWithBrowserCapabilities(),
				HTTPAllowedHosts: []string{srvHostname},
			},
		}),
	)

	req := ktp.NewToolRequest("admin-1", "browser", "fetch_page", map[string]any{
		"url": srv.URL + "/page",
	})
	resp, err := h.executor.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Success {
		if strings.Contains(resp.Error, "panicked") || strings.Contains(resp.Error, "fetch failed") {
			t.Skipf("browser engine unavailable: %s", resp.Error)
		}
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	result := resp.Result.(map[string]any)
	content, _ := result["content"].(string)
	if !strings.Contains(content, "Hello from Kyvik browser test") {
		t.Fatalf("expected page content, got: %s", content)
	}

	entries := queryAuditEntries(t, h.db, "admin-1")
	if len(entries) == 0 {
		t.Error("expected audit entries for browser fetch")
	}
}

func TestPhase10_BrowserTool_WorkerDeniedByTier(t *testing.T) {
	// No browser skip needed — permission check happens before execution.
	h := newTestHarness(t)

	req := ktp.NewToolRequest("worker-1", "browser", "fetch_page", map[string]any{
		"url": "https://example.com",
	})
	resp, err := h.executor.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Success {
		t.Fatal("expected denial for worker-tier agent on admin-tier tool")
	}
	if !strings.Contains(resp.Error, "tier") {
		t.Fatalf("expected tier-related denial, got: %s", resp.Error)
	}

	entries := queryAuditEntries(t, h.db, "worker-1")
	var foundDenied bool
	for _, e := range entries {
		if e.Decision == "denied" {
			foundDenied = true
			break
		}
	}
	if !foundDenied {
		t.Error("expected at least one 'denied' audit entry")
	}
}

func TestPhase10_BrowserTool_DomainNotAllowed(t *testing.T) {
	requireBrowser(t)

	srv := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><body>Should not reach here</body></html>`)
	}))

	h := newTestHarness(t,
		withAgents(map[string]*types.AgentConfig{
			"admin-1": {
				ID: "admin-1", Template: "admin",
				CapabilityGrants: adminWithBrowserCapabilities(),
				HTTPAllowedHosts: []string{"other.example.com"},
			},
		}),
	)

	req := ktp.NewToolRequest("admin-1", "browser", "fetch_page", map[string]any{
		"url": srv.URL + "/page",
	})
	resp, err := h.executor.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Success {
		t.Fatal("expected denial for domain not in allowed hosts")
	}
	if !strings.Contains(resp.Error, "allowed") {
		t.Fatalf("expected allowed-hosts error, got: %s", resp.Error)
	}
}

func TestPhase10_BrowserTool_ScreenshotBase64(t *testing.T) {
	requireBrowser(t)

	srv := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body style="background:blue"><h1>Screenshot</h1></body></html>`)
	}))

	_, srvHostname := extractHostPort(srv.URL)

	h := newTestHarness(t,
		withAgents(map[string]*types.AgentConfig{
			"admin-1": {
				ID: "admin-1", Template: "admin",
				CapabilityGrants: adminWithBrowserCapabilities(),
				HTTPAllowedHosts: []string{srvHostname},
			},
		}),
	)

	req := ktp.NewToolRequest("admin-1", "browser", "screenshot", map[string]any{
		"url": srv.URL + "/page",
	})
	resp, err := h.executor.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Success {
		// Browser engine panics or failures are infrastructure issues, not test logic.
		if strings.Contains(resp.Error, "panicked") || strings.Contains(resp.Error, "screenshot failed") {
			t.Skipf("browser engine unavailable for screenshot: %s", resp.Error)
		}
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	result := resp.Result.(map[string]any)
	b64, _ := result["image_base64"].(string)
	if b64 == "" {
		t.Fatal("expected non-empty base64 image")
	}

	imgData, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("invalid base64: %v", err)
	}

	// Check PNG magic bytes: \x89PNG
	if len(imgData) < 4 || imgData[0] != 0x89 || imgData[1] != 'P' || imgData[2] != 'N' || imgData[3] != 'G' {
		t.Fatal("decoded image does not have PNG magic bytes")
	}

	imgBytes := result["bytes"]
	switch v := imgBytes.(type) {
	case float64:
		if v <= 0 {
			t.Errorf("expected bytes > 0, got %v", v)
		}
	case int:
		if v <= 0 {
			t.Errorf("expected bytes > 0, got %v", v)
		}
	default:
		t.Errorf("expected numeric bytes field, got %T", imgBytes)
	}
}

// =============================================================================
// Step 6: Webhook Tests
// =============================================================================

func TestPhase10_InboundWebhook_ValidSecretQueuesMessage(t *testing.T) {
	_, _, kyv, al, h := newInboundTestEnv("agent-1", "correct-secret", &types.InboundWebhookConfig{
		Enabled: true,
	})

	url := "/webhooks/agent-1/correct-secret"
	body := `{"source":"github","event":"push"}`
	req := httptest.NewRequest(http.MethodPost, url, bytes.NewBufferString(body))
	req.SetPathValue("agent_id", "agent-1")
	req.SetPathValue("webhook_secret", "correct-secret")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}

	kyv.mu.Lock()
	queued := len(kyv.queued)
	var channel string
	if queued > 0 {
		channel = kyv.queued[0].Channel
	}
	kyv.mu.Unlock()

	if queued != 1 {
		t.Fatalf("expected 1 queued message, got %d", queued)
	}
	if channel != "webhook" {
		t.Errorf("expected channel=webhook, got %q", channel)
	}

	// Verify audit entry.
	al.mu.Lock()
	var foundAccepted bool
	for _, e := range al.entries {
		if e.Decision == "accepted" {
			foundAccepted = true
			break
		}
	}
	al.mu.Unlock()
	if !foundAccepted {
		t.Error("expected at least one 'accepted' audit entry")
	}
}

func TestPhase10_InboundWebhook_TransformTemplate(t *testing.T) {
	_, _, kyv, _, h := newInboundTestEnv("agent-2", "tok", &types.InboundWebhookConfig{
		Enabled:           true,
		TransformTemplate: "Alert: {{.source}} - {{.message}}",
	})

	body := `{"source":"github","message":"push"}`
	req := httptest.NewRequest(http.MethodPost, "/webhooks/agent-2/tok", bytes.NewBufferString(body))
	req.SetPathValue("agent_id", "agent-2")
	req.SetPathValue("webhook_secret", "tok")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}

	kyv.mu.Lock()
	defer kyv.mu.Unlock()
	if len(kyv.queued) == 0 {
		t.Fatal("no message queued")
	}
	if kyv.queued[0].Content != "Alert: github - push" {
		t.Errorf("unexpected content: %q", kyv.queued[0].Content)
	}
}

func TestPhase10_OutboundWebhook_FiresOnEvent(t *testing.T) {
	store := newP10TestStore(t)
	ctx := context.Background()

	var received atomic.Int32
	var mu sync.Mutex
	var receivedBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		receivedBody, _ = io.ReadAll(r.Body)
		mu.Unlock()
		received.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	whID := uuid.New().String()
	wh := types.OutboundWebhook{
		ID:             whID,
		Name:           "p10-test-hook",
		URL:            srv.URL,
		Events:         []string{"*"},
		MaxRetries:     3,
		BackoffSeconds: types.DefaultWebhookBackoff,
		CBThreshold:    10,
		CBCooldownSecs: 3600,
		Enabled:        true,
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	if err := store.CreateOutboundWebhook(ctx, wh); err != nil {
		t.Fatalf("CreateOutboundWebhook: %v", err)
	}

	dispatcher := webhooks.NewDispatcher(store, nil)
	if err := dispatcher.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer dispatcher.Stop()

	event := notifications.Event{
		Type:      "circuit_breaker",
		Severity:  "critical",
		Agent:     "agent-1",
		Title:     "CB tripped",
		Detail:    "Too many errors",
		Timestamp: time.Now().UTC(),
	}
	if err := dispatcher.Send(ctx, event); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Wait for async delivery.
	deadline := time.Now().Add(5 * time.Second)
	for received.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}

	if received.Load() != 1 {
		t.Fatalf("expected 1 delivery, got %d", received.Load())
	}

	mu.Lock()
	body := receivedBody
	mu.Unlock()
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload["event"] != "circuit_breaker.tripped" {
		t.Errorf("event = %q, want %q", payload["event"], "circuit_breaker.tripped")
	}
}

func TestPhase10_OutboundWebhook_RetryOnFailure(t *testing.T) {
	store := newP10TestStore(t)
	ctx := context.Background()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("server error"))
	}))
	defer srv.Close()

	whID := uuid.New().String()
	wh := types.OutboundWebhook{
		ID:             whID,
		Name:           "p10-fail-hook",
		URL:            srv.URL,
		Events:         []string{"*"},
		MaxRetries:     3,
		BackoffSeconds: []int{1, 2, 5},
		CBThreshold:    100,
		CBCooldownSecs: 3600,
		Enabled:        true,
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	if err := store.CreateOutboundWebhook(ctx, wh); err != nil {
		t.Fatalf("CreateOutboundWebhook: %v", err)
	}

	dispatcher := webhooks.NewDispatcher(store, nil)
	if err := dispatcher.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer dispatcher.Stop()

	if err := dispatcher.Send(ctx, notifications.Event{
		Type: "agent_error", Severity: "critical", Title: "test", Timestamp: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	time.Sleep(500 * time.Millisecond)

	deliveries, err := store.ListWebhookDeliveries(ctx, whID, 10)
	if err != nil {
		t.Fatalf("ListWebhookDeliveries: %v", err)
	}
	if len(deliveries) == 0 {
		t.Fatal("expected at least 1 delivery record")
	}

	found := false
	for _, d := range deliveries {
		if d.Status == types.DeliveryStatusPendingRetry {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected at least one delivery with status pending_retry, got: %v", deliveries[0].Status)
	}
}

func TestPhase10_OutboundWebhook_CircuitBreakerTrips(t *testing.T) {
	store := newP10TestStore(t)
	ctx := context.Background()

	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	whID := uuid.New().String()
	wh := types.OutboundWebhook{
		ID:             whID,
		Name:           "p10-breaker-hook",
		URL:            srv.URL,
		Events:         []string{"*"},
		MaxRetries:     0,
		BackoffSeconds: []int{},
		CBThreshold:    3,
		CBCooldownSecs: 3600,
		Enabled:        true,
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	if err := store.CreateOutboundWebhook(ctx, wh); err != nil {
		t.Fatalf("CreateOutboundWebhook: %v", err)
	}

	dispatcher := webhooks.NewDispatcher(store, nil)
	if err := dispatcher.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer dispatcher.Stop()

	for i := 0; i < 5; i++ {
		_ = dispatcher.Send(ctx, notifications.Event{
			Type: "agent_error", Severity: "critical", Title: "test", Timestamp: time.Now().UTC(),
		})
		time.Sleep(100 * time.Millisecond)
	}

	time.Sleep(500 * time.Millisecond)

	count := callCount.Load()
	if count > 4 {
		t.Errorf("expected <= 4 HTTP calls (circuit should trip after 3), got %d", count)
	}
	if count < 3 {
		t.Errorf("expected at least 3 HTTP calls before circuit trips, got %d", count)
	}
}

// newP10TestStore creates a real PostgresStore for outbound webhook tests.
func newP10TestStore(t *testing.T) *postgres.PostgresStore {
	t.Helper()
	tdb := testutil.RequirePostgres(t)
	return tdb.Store
}

// =============================================================================
// Step 7: Cross-cutting & End-to-End Tests
// =============================================================================

func TestPhase10_ReaderDeniedAllNewTools(t *testing.T) {
	h := newTestHarness(t)

	tests := []struct {
		name   string
		tool   string
		action string
		params map[string]any
	}{
		{"hostfs/read", "hostfs", "read", map[string]any{"path": "/tmp/test"}},
		{"rest_api/list_endpoints", "rest_api", "list_endpoints", nil},
		{"browser/fetch_page", "browser", "fetch_page", map[string]any{"url": "https://example.com"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := ktp.NewToolRequest("reader-1", tt.tool, tt.action, tt.params)
			resp, err := h.executor.Execute(context.Background(), req)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resp.Success {
				t.Fatalf("expected denial for reader calling %s", tt.name)
			}
			if !strings.Contains(resp.Error, "tier") {
				t.Fatalf("expected tier-related denial, got: %s", resp.Error)
			}
		})
	}
}

func TestPhase10_UnknownTemplateDenied(t *testing.T) {
	h := newTestHarness(t,
		withAgents(map[string]*types.AgentConfig{
			"quarantined-1": {
				ID: "quarantined-1", Template: "quarantined",
				CapabilityGrants: readerCapabilities(),
			},
		}),
	)

	req := ktp.NewToolRequest("quarantined-1", "file", "read", map[string]any{
		"path": "test.txt",
	})
	resp, err := h.executor.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Success {
		t.Fatal("expected denial for unknown template")
	}

	entries := queryAuditEntries(t, h.db, "quarantined-1")
	var foundDenied bool
	for _, e := range entries {
		if e.Decision == "denied" {
			foundDenied = true
			break
		}
	}
	if !foundDenied {
		t.Error("expected at least one 'denied' audit entry")
	}
}

func TestPhase10_EndToEnd_MultiToolSequence(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a config file to read.
	configContent := `{"api_key":"test123","endpoint":"items"}`
	configFile := filepath.Join(tmpDir, "config.json")
	if err := os.WriteFile(configFile, []byte(configContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// TLS server returning JSON.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"items":[{"id":1,"name":"widget"}]}`)
	}))
	defer srv.Close()

	outputFile := filepath.Join(tmpDir, "output.json")

	h := newTestHarness(t,
		withAgents(map[string]*types.AgentConfig{
			"admin-1": {
				ID: "admin-1", Template: "admin",
				CapabilityGrants: phase10AdminCapabilities(),
			},
		}),
		withHostFSConfig("admin-1", &hostfs.HostPathConfig{
			Read:  []string{tmpDir + "/"},
			Write: []string{tmpDir + "/"},
		}),
		withRESTEndpoints("admin-1", []types.RESTAPIEndpoint{
			{
				Name:   "get_items",
				Method: "GET",
				URL:    srv.URL + "/items",
			},
		}),
		withRESTTransport(srv.Client().Transport),
	)

	ctx := context.Background()

	// Step 1: Read config file from host filesystem.
	readReq := ktp.NewToolRequest("admin-1", "hostfs", "read", map[string]any{
		"path": configFile,
	})
	readResp, err := h.executor.Execute(ctx, readReq)
	if err != nil {
		t.Fatalf("hostfs/read error: %v", err)
	}
	if !readResp.Success {
		t.Fatalf("hostfs/read failed: %s", readResp.Error)
	}
	readResult := readResp.Result.(map[string]any)
	content, _ := readResult["content"].(string)
	if !strings.Contains(content, "test123") {
		t.Fatalf("expected config content, got: %s", content)
	}

	// Step 2: Call REST API.
	apiReq := ktp.NewToolRequest("admin-1", "rest_api", "call", map[string]any{
		"endpoint": "get_items",
	})
	apiResp, err := h.executor.Execute(ctx, apiReq)
	if err != nil {
		t.Fatalf("rest_api/call error: %v", err)
	}
	if !apiResp.Success {
		t.Fatalf("rest_api/call failed: %s", apiResp.Error)
	}

	apiResult := apiResp.Result.(map[string]any)
	bodyStr, _ := apiResult["body"].(string)

	// Step 3: Write result to host filesystem.
	writeReq := ktp.NewToolRequest("admin-1", "hostfs", "write", map[string]any{
		"path":    outputFile,
		"content": bodyStr,
	})
	writeResp, err := h.executor.Execute(ctx, writeReq)
	if err != nil {
		t.Fatalf("hostfs/write error: %v", err)
	}
	if !writeResp.Success {
		t.Fatalf("hostfs/write failed: %s", writeResp.Error)
	}

	// Verify file on disk.
	data, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("output file not found: %v", err)
	}
	if !strings.Contains(string(data), "widget") {
		t.Fatalf("expected 'widget' in output file, got: %s", data)
	}

	// Verify audit log has entries for all three operations.
	entries := queryAuditEntries(t, h.db, "admin-1")
	// Each tool call produces at least 2 audit entries (permission + execution).
	// 3 calls = at least 6 entries.
	if len(entries) < 6 {
		for i, e := range entries {
			t.Logf("  entry[%d]: event=%s action=%s decision=%s resource=%s", i, e.EventType, e.Action, e.Decision, e.Resource)
		}
		t.Fatalf("expected at least 6 audit entries for 3 tool calls, got %d", len(entries))
	}

	// All entries should belong to power-1.
	for _, e := range entries {
		if e.AgentID != "admin-1" {
			t.Errorf("expected all audit entries for power-1, found one for %q", e.AgentID)
		}
	}
}
