package integration

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/kkjorsvik/kyvik/internal/apikeys"
	"github.com/kkjorsvik/kyvik/internal/audit"
	"github.com/kkjorsvik/kyvik/internal/auth"
	"github.com/kkjorsvik/kyvik/internal/channels/webui"
	"github.com/kkjorsvik/kyvik/internal/config"
	"github.com/kkjorsvik/kyvik/internal/core"
	"github.com/kkjorsvik/kyvik/internal/history"
	"github.com/kkjorsvik/kyvik/internal/memory"
	"github.com/kkjorsvik/kyvik/internal/permissions"
	"github.com/kkjorsvik/kyvik/internal/secrets"
	"github.com/kkjorsvik/kyvik/internal/spending"
	"github.com/kkjorsvik/kyvik/internal/store/postgres"
	"github.com/kkjorsvik/kyvik/internal/templates"
	"github.com/kkjorsvik/kyvik/internal/testutil"
	"github.com/kkjorsvik/kyvik/internal/authprovider/local"
	"github.com/kkjorsvik/kyvik/internal/users"
	"github.com/kkjorsvik/kyvik/pkg/types"
	"github.com/kkjorsvik/kyvik/web"
	webapi "github.com/kkjorsvik/kyvik/web/api"
)

type phase11Harness struct {
	store     *postgres.PostgresStore
	db        *sql.DB
	kyvik     *core.Kyvik
	audit     *audit.StoreLogger
	history   *history.Store
	users     *users.Service
	templates *templates.Service
	apikeys   *apikeys.Service
	secrets   *secrets.Vault
	spending  *spending.StoreTracker
	webui     *webui.Adapter
	server    *httptest.Server
	client    *http.Client
}

func newPhase11Harness(t *testing.T) *phase11Harness {
	t.Helper()

	tdb := testutil.RequirePostgres(t)
	s := tdb.Store
	noClose := &testutil.NoCloseStore{Store: s}

	al := audit.NewStoreLoggerWithPollInterval(s, 50*time.Millisecond, 10)
	gate := permissions.NewStoreGate(s, al, "")
	sp := spending.NewStoreTracker(s, al, "test-model")

	k := core.New(noClose, gate, nil, al, &p7MockToolsRegistry{}, sp)
	k.RegisterModel(newP7MockProvider("test-provider"))

	historyStore := history.New(s.DB())
	k.SetHistory(historyStore)
	k.SetConversationStore(historyStore)
	k.SetMemory(memory.New(s.DB()))

	tmplSvc := templates.New(s)
	k.SetTemplateService(tmplSvc)

	webuiAdapter := webui.New()
	k.RegisterChannel(webuiAdapter)

	us := users.New(s, users.AuthConfig{SessionTTL: time.Hour, MaxSessionsPerUser: 3})
	keySvc := apikeys.New(s)
	secretVault := secrets.NewVault(s.DB(), []byte("test-master-key-32-bytes-long!!!!"), al)

	// Mark setup as complete so SetupCheck middleware doesn't redirect.
	_ = s.SetSystemState(context.Background(), "setup_complete", "true")

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

	t.Cleanup(func() {
		srv.Close()
		_ = al.Close()
		k.Shutdown(context.Background())
		// Do NOT close s — it is a shared PostgresStore used across tests.
	})

	return &phase11Harness{
		store:     s,
		db:        s.DB(),
		kyvik:     k,
		audit:     al,
		history:   historyStore,
		users:     us,
		templates: tmplSvc,
		apikeys:   keySvc,
		secrets:   secretVault,
		spending:  sp,
		webui:     webuiAdapter,
		server:    srv,
		client:    client,
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

func (h *phase11Harness) seedUser(t *testing.T, username, password string, isAdmin bool) *types.User {
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

func (h *phase11Harness) createGroup(t *testing.T, name string) string {
	t.Helper()
	g, err := h.users.CreateGroup(context.Background(), name, "desc")
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	return g.ID
}

func (h *phase11Harness) assignUserRole(t *testing.T, userID, groupID, role string) {
	t.Helper()
	if err := h.users.SetUserRoleInGroup(context.Background(), userID, groupID, role); err != nil {
		t.Fatalf("SetUserRoleInGroup: %v", err)
	}
}

func (h *phase11Harness) seedAgent(t *testing.T, id, name string) {
	t.Helper()
	now := time.Now().UTC()
	if err := h.store.CreateAgent(context.Background(), types.AgentConfig{
		ID:          id,
		Name:        name,
		ModelConfig: types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:    "worker",
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
}

func (h *phase11Harness) login(t *testing.T, username, password string) *http.Cookie {
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

func (h *phase11Harness) authedGet(t *testing.T, path string, cookie *http.Cookie) *http.Response {
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

func (h *phase11Harness) authedPostForm(t *testing.T, path string, cookie *http.Cookie, form url.Values) *http.Response {
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

func (h *phase11Harness) apiRequest(t *testing.T, method, path, token string, body any) *http.Response {
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

func (h *phase11Harness) createAPIKey(t *testing.T, name, scope string, agentIDs []string) string {
	t.Helper()
	result, err := h.apikeys.Create(context.Background(), name, scope, agentIDs, nil)
	if err != nil {
		t.Fatalf("Create key: %v", err)
	}
	return result.PlainKey
}

// waitForResponse polls the DB until an assistant response appears
// for the given agent in any channel, or the context expires.
func (h *phase11Harness) waitForResponse(ctx context.Context, t *testing.T, agentID string) {
	t.Helper()
	for {
		var count int
		err := h.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM conversation_history WHERE agent_id = $1 AND role = 'assistant'`,
			agentID,
		).Scan(&count)
		if err == nil && count > 0 {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waitForResponse(%s): %v", agentID, ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func expectStatus(t *testing.T, resp *http.Response, want int) {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != want {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected %d, got %d: %s", want, resp.StatusCode, body)
	}
}

func readAuditEvent(t *testing.T, r io.Reader, timeout time.Duration) map[string]any {
	t.Helper()
	reader := bufio.NewReader(r)
	deadline := time.Now().Add(timeout)
	var dataLine string
	for time.Now().Before(deadline) {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data:") {
			dataLine = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			break
		}
	}
	if dataLine == "" {
		t.Fatal("no SSE data received")
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(dataLine), &payload); err != nil {
		t.Fatalf("unmarshal SSE data: %v", err)
	}
	return payload
}

// =============================================================================
// REST API integration
// =============================================================================

func TestPhase11_API_CreateAgent_AppearsInDashboard(t *testing.T) {
	h := newPhase11Harness(t)

	admin := h.seedUser(t, "admin", "secretsecret", true)
	_ = admin

	key := h.createAPIKey(t, "admin-key", auth.RoleAdmin, nil)

	agentID := "api-agent-1"
	resp := h.apiRequest(t, "POST", "/api/v1/agents", key, types.AgentConfig{
		ID:          agentID,
		Name:        "API Agent",
		ModelConfig: types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:    "worker",
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	})
	expectStatus(t, resp, http.StatusCreated)

	cookie := h.login(t, "admin", "secretsecret")
	dash := h.authedGet(t, "/agents", cookie)
	defer dash.Body.Close()
	body, _ := io.ReadAll(dash.Body)
	if !strings.Contains(string(body), "API Agent") {
		t.Fatalf("dashboard missing agent")
	}
}

func TestPhase11_API_SendMessage_AgentProcesses(t *testing.T) {
	h := newPhase11Harness(t)
	h.seedUser(t, "admin", "secretsecret", true)
	key := h.createAPIKey(t, "admin-key", auth.RoleAdmin, nil)

	agentID := "api-msg-1"
	_ = h.kyvik.CreateAgent(context.Background(), types.AgentConfig{
		ID:          agentID,
		Name:        "API Msg Agent",
		ModelConfig: types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:    "worker",
	})
	if err := h.kyvik.StartAgent(context.Background(), types.AgentConfig{
		ID:          agentID,
		Name:        "API Msg Agent",
		ModelConfig: types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:    "worker",
	}); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}

	msg := types.Message{Role: "user", Content: "hello"}
	resp := h.apiRequest(t, "POST", "/api/v1/agents/"+agentID+"/message", key, msg)
	expectStatus(t, resp, http.StatusAccepted)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	h.waitForResponse(ctx, t, agentID)
}

func TestPhase11_API_ScopeEnforcement(t *testing.T) {
	h := newPhase11Harness(t)
	h.seedUser(t, "admin", "secretsecret", true)
	viewerKey := h.createAPIKey(t, "viewer-key", auth.RoleViewer, nil)

	resp := h.apiRequest(t, "POST", "/api/v1/agents", viewerKey, types.AgentConfig{ID: "nope"})
	expectStatus(t, resp, http.StatusForbidden)
}

func TestPhase11_API_AgentScopedKey(t *testing.T) {
	h := newPhase11Harness(t)
	h.seedUser(t, "admin", "secretsecret", true)
	key := h.createAPIKey(t, "admin-key", auth.RoleAdmin, nil)

	agentA := "agent-a"
	agentB := "agent-b"
	for _, id := range []string{agentA, agentB} {
		resp := h.apiRequest(t, "POST", "/api/v1/agents", key, types.AgentConfig{
			ID:          id,
			Name:        id,
			ModelConfig: types.ModelConfig{Provider: "test-provider", Model: "test-model"},
			Template:    "worker",
		})
		expectStatus(t, resp, http.StatusCreated)
	}

	scopedKey := h.createAPIKey(t, "scoped", auth.RoleManager, []string{agentA})

	listResp := h.apiRequest(t, "GET", "/api/v1/agents", scopedKey, nil)
	defer listResp.Body.Close()
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("list status: %d", listResp.StatusCode)
	}
	var list webapi.ListResponse[types.AgentConfig]
	if err := json.NewDecoder(listResp.Body).Decode(&list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Data) != 1 || list.Data[0].ID != agentA {
		t.Fatalf("scoped list mismatch: %+v", list.Data)
	}

	getResp := h.apiRequest(t, "GET", "/api/v1/agents/"+agentB, scopedKey, nil)
	expectStatus(t, getResp, http.StatusForbidden)
}

func TestPhase11_API_Pagination(t *testing.T) {
	h := newPhase11Harness(t)
	h.seedUser(t, "admin", "secretsecret", true)
	key := h.createAPIKey(t, "admin-key", auth.RoleAdmin, nil)

	for i := 0; i < 120; i++ {
		id := fmt.Sprintf("page-%03d", i)
		_ = h.kyvik.CreateAgent(context.Background(), types.AgentConfig{
			ID:          id,
			Name:        id,
			ModelConfig: types.ModelConfig{Provider: "test-provider", Model: "test-model"},
			Template:    "worker",
		})
	}

	resp1 := h.apiRequest(t, "GET", "/api/v1/agents?limit=50&offset=0", key, nil)
	defer resp1.Body.Close()
	var list1 webapi.ListResponse[types.AgentConfig]
	_ = json.NewDecoder(resp1.Body).Decode(&list1)
	if len(list1.Data) != 50 || !list1.HasMore {
		t.Fatalf("page1 mismatch: %d hasMore=%v", len(list1.Data), list1.HasMore)
	}

	resp2 := h.apiRequest(t, "GET", "/api/v1/agents?limit=50&offset=50", key, nil)
	defer resp2.Body.Close()
	var list2 webapi.ListResponse[types.AgentConfig]
	_ = json.NewDecoder(resp2.Body).Decode(&list2)
	if len(list2.Data) != 50 || !list2.HasMore {
		t.Fatalf("page2 mismatch: %d hasMore=%v", len(list2.Data), list2.HasMore)
	}

	resp3 := h.apiRequest(t, "GET", "/api/v1/agents?limit=50&offset=100", key, nil)
	defer resp3.Body.Close()
	var list3 webapi.ListResponse[types.AgentConfig]
	_ = json.NewDecoder(resp3.Body).Decode(&list3)
	if len(list3.Data) == 0 || list3.HasMore {
		t.Fatalf("page3 mismatch: %d hasMore=%v", len(list3.Data), list3.HasMore)
	}
}

// =============================================================================
// Multi-user access control
// =============================================================================

func TestPhase11_AccessControl_Visibility(t *testing.T) {
	h := newPhase11Harness(t)

	admin := h.seedUser(t, "admin", "secretsecret", true)
	manager := h.seedUser(t, "manager", "secretsecret", false)
	viewer := h.seedUser(t, "viewer", "secretsecret", false)
	_ = admin

	g1 := h.createGroup(t, "g1")
	g2 := h.createGroup(t, "g2")

	h.assignUserRole(t, manager.ID, g1, auth.RoleManager)
	h.assignUserRole(t, viewer.ID, g1, auth.RoleViewer)

	h.seedAgent(t, "a1", "A1")
	h.seedAgent(t, "a2", "A2")
	h.seedAgent(t, "nogroup", "NoGroup")

	_ = h.users.AddAgentToGroup(context.Background(), g1, "a1")
	_ = h.users.AddAgentToGroup(context.Background(), g2, "a2")

	// Use unique agent names that won't match SVG path data or HTML attributes.
	// The previous short names ("A1", "A2") matched SVG arc commands like "A2 2 0 0 0".
	// Use ">Name<" to match HTML element content specifically.
	containsAgent := func(body, name string) bool {
		return strings.Contains(body, ">"+name+"<")
	}

	adminCookie := h.login(t, "admin", "secretsecret")
	resp := h.authedGet(t, "/agents", adminCookie)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	adminBody := string(body)
	if !containsAgent(adminBody, "A1") || !containsAgent(adminBody, "A2") || !containsAgent(adminBody, "NoGroup") {
		t.Fatalf("admin should see all agents: hasA1=%v hasA2=%v hasNoGroup=%v",
			containsAgent(adminBody, "A1"), containsAgent(adminBody, "A2"), containsAgent(adminBody, "NoGroup"))
	}

	managerCookie := h.login(t, "manager", "secretsecret")
	resp = h.authedGet(t, "/agents", managerCookie)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	mgrBody := string(body)
	if !containsAgent(mgrBody, "A1") || containsAgent(mgrBody, "A2") || containsAgent(mgrBody, "NoGroup") {
		t.Fatalf("manager visibility mismatch: hasA1=%v hasA2=%v hasNoGroup=%v bodyLen=%d",
			containsAgent(mgrBody, "A1"),
			containsAgent(mgrBody, "A2"),
			containsAgent(mgrBody, "NoGroup"),
			len(body))
	}

	viewerCookie := h.login(t, "viewer", "secretsecret")
	resp = h.authedGet(t, "/agents", viewerCookie)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	viewerBody := string(body)
	if !containsAgent(viewerBody, "A1") || containsAgent(viewerBody, "A2") || containsAgent(viewerBody, "NoGroup") {
		t.Fatalf("viewer visibility mismatch")
	}
}

func TestPhase11_AccessControl_ManagerTemplateAssignsGroup(t *testing.T) {
	h := newPhase11Harness(t)
	manager := h.seedUser(t, "manager", "secretsecret", false)
	g1 := h.createGroup(t, "g1")
	h.assignUserRole(t, manager.ID, g1, auth.RoleManager)

	tmpl, err := h.templates.Create(context.Background(), "tmpl-1", "", g1, manager.ID, types.AgentConfig{
		Name:        "Base",
		ModelConfig: types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:    "worker",
	}, nil, nil)
	if err != nil {
		t.Fatalf("Create template: %v", err)
	}

	cookie := h.login(t, "manager", "secretsecret")
	form := url.Values{}
	form.Set("name", "Managed Agent")
	form.Set("provider", "test-provider")
	form.Set("model", "test-model")
	form.Set("template", "worker")
	form.Set("from_template_id", tmpl.ID)
	resp := h.authedPostForm(t, "/agents", cookie, form)
	expectStatus(t, resp, http.StatusOK)

	agents, _ := h.store.ListAgents(context.Background())
	var createdID string
	for _, a := range agents {
		if a.Name == "Managed Agent" {
			createdID = a.ID
			break
		}
	}
	if createdID == "" {
		t.Fatalf("created agent not found")
	}

	groupIDs, _ := h.store.ListGroupIDsByAgent(context.Background(), createdID)
	found := false
	for _, gid := range groupIDs {
		if gid == g1 {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected agent to be auto-assigned to manager group")
	}
}

func TestPhase11_AccessControl_OperatorStartStopNoEdit(t *testing.T) {
	h := newPhase11Harness(t)
	op := h.seedUser(t, "op", "secretsecret", false)
	g1 := h.createGroup(t, "g1")
	h.assignUserRole(t, op.ID, g1, auth.RoleOperator)
	h.seedAgent(t, "a1", "A1")
	_ = h.users.AddAgentToGroup(context.Background(), g1, "a1")

	cookie := h.login(t, "op", "secretsecret")
	resp := h.authedPostForm(t, "/agents/a1/start", cookie, url.Values{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("start expected 200, got %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = h.authedPostForm(t, "/agents/a1/stop", cookie, url.Values{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stop expected 200, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = h.authedGet(t, "/agents/a1/edit", cookie)
	expectStatus(t, resp, http.StatusForbidden)
}

func TestPhase11_AccessControl_ViewerViewOnly(t *testing.T) {
	h := newPhase11Harness(t)
	view := h.seedUser(t, "view", "secretsecret", false)
	g1 := h.createGroup(t, "g1")
	h.assignUserRole(t, view.ID, g1, auth.RoleViewer)
	h.seedAgent(t, "a1", "A1")
	_ = h.users.AddAgentToGroup(context.Background(), g1, "a1")

	cookie := h.login(t, "view", "secretsecret")
	resp := h.authedGet(t, "/agents", cookie)
	expectStatus(t, resp, http.StatusOK)

	resp = h.authedPostForm(t, "/agents/a1/start", cookie, url.Values{})
	expectStatus(t, resp, http.StatusForbidden)
}

func TestPhase11_AccessControl_MultiGroupRoleResolution(t *testing.T) {
	h := newPhase11Harness(t)
	u := h.seedUser(t, "multi", "secretsecret", false)
	g1 := h.createGroup(t, "g1")
	g2 := h.createGroup(t, "g2")
	h.assignUserRole(t, u.ID, g1, auth.RoleViewer)
	h.assignUserRole(t, u.ID, g2, auth.RoleManager)

	h.seedAgent(t, "a1", "A1")
	_ = h.users.AddAgentToGroup(context.Background(), g1, "a1")
	_ = h.users.AddAgentToGroup(context.Background(), g2, "a1")

	role, found, err := h.users.ResolveAgentRole(context.Background(), u.ID, "a1")
	if err != nil || !found {
		t.Fatalf("ResolveAgentRole: %v found=%v", err, found)
	}
	if role != auth.RoleManager {
		t.Fatalf("expected manager, got %s", role)
	}
}

func TestPhase11_AccessControl_NoRoleStringComparisons(t *testing.T) {
	files, err := filepath.Glob("web/handlers/*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	roleLiteral := regexp.MustCompile(`\"(viewer|operator|manager|admin)\"`)
	roleConst := regexp.MustCompile(`auth\.Role(Viewer|Operator|Manager|Admin)`)
	compare := regexp.MustCompile(`==|!=`)

	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		lines := strings.Split(string(data), "\n")
		for _, line := range lines {
			if !compare.MatchString(line) {
				continue
			}
			if roleLiteral.MatchString(line) || roleConst.MatchString(line) {
				t.Fatalf("role string comparison found in %s: %s", f, strings.TrimSpace(line))
			}
		}
	}
}

// =============================================================================
// Templates and cloning
// =============================================================================

func TestPhase11_Templates_CreateAndUse(t *testing.T) {
	h := newPhase11Harness(t)
	admin := h.seedUser(t, "admin", "secretsecret", true)

	h.seedAgent(t, "source", "Source")
	cookie := h.login(t, "admin", "secretsecret")

	form := url.Values{}
	form.Set("template_name", "Saved Template")
	form.Set("template_description", "desc")
	resp := h.authedPostForm(t, "/agents/source/save-template", cookie, form)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save template expected 303, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	templates, err := h.templates.List(context.Background())
	if err != nil || len(templates) == 0 {
		t.Fatalf("expected templates")
	}

	form = url.Values{}
	form.Set("name", "From Template")
	form.Set("provider", "test-provider")
	form.Set("model", "test-model")
	form.Set("template", "worker")
	form.Set("from_template_id", templates[0].ID)
	resp = h.authedPostForm(t, "/agents", cookie, form)
	expectStatus(t, resp, http.StatusOK)

	agents, _ := h.store.ListAgents(context.Background())
	found := false
	for _, a := range agents {
		if a.Name == "From Template" {
			found = true
		}
	}
	if !found {
		t.Fatalf("created agent not found")
	}
	_ = admin
}

func TestPhase11_Templates_LockedFieldsAndConstraints(t *testing.T) {
	h := newPhase11Harness(t)
	admin := h.seedUser(t, "admin", "secretsecret", true)
	_ = admin

	min := 5.0
	max := 10.0
	tmpl, err := h.templates.Create(context.Background(), "tmpl-locked", "", "", "", types.AgentConfig{
		Name:        "Base",
		ModelConfig: types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:    "worker",
	}, []string{"max_spend_per_day"}, map[string]types.ConstraintRule{
		"max_spend_per_day": {Min: &min, Max: &max},
	})
	if err != nil {
		t.Fatalf("Create template: %v", err)
	}

	cookie := h.login(t, "admin", "secretsecret")
	form := url.Values{}
	form.Set("name", "Locked Fail")
	form.Set("provider", "test-provider")
	form.Set("model", "test-model")
	form.Set("template", "worker")
	form.Set("max_spend_per_day", "1")
	form.Set("from_template_id", tmpl.ID)
	resp := h.authedPostForm(t, "/agents", cookie, form)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Template constraint violation") {
		t.Fatalf("expected template constraint violation")
	}
}

func TestPhase11_Templates_CloneNoMemoriesSecrets(t *testing.T) {
	h := newPhase11Harness(t)
	h.seedUser(t, "admin", "secretsecret", true)
	h.seedAgent(t, "source", "Source")

	// Seed memory.
	memStore := memory.New(h.db)
	_, _ = memStore.Create(context.Background(), memory.Memory{
		AgentID: "source",
		Content: "secret",
	})
	// Seed secret.
	_ = h.secrets.Set(context.Background(), "agent:source", "key", "val", "desc")

	cookie := h.login(t, "admin", "secretsecret")
	resp := h.authedGet(t, "/agents/source/clone", cookie)
	expectStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	form := url.Values{}
	form.Set("name", "Clone Agent")
	form.Set("provider", "test-provider")
	form.Set("model", "test-model")
	form.Set("template", "worker")
	resp = h.authedPostForm(t, "/agents", cookie, form)
	expectStatus(t, resp, http.StatusOK)

	agents, _ := h.store.ListAgents(context.Background())
	var cloneID string
	for _, a := range agents {
		if a.Name == "Clone Agent" {
			cloneID = a.ID
			break
		}
	}
	if cloneID == "" {
		t.Fatalf("clone not found")
	}

	memories, _ := memStore.List(context.Background(), cloneID, memory.ListOptions{})
	if len(memories) != 0 {
		t.Fatalf("clone should not have memories")
	}
	if _, err := h.secrets.Get(context.Background(), "agent:"+cloneID, "key"); !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("clone should not have secrets")
	}
}

func TestPhase11_Templates_ManagerGroupRestriction(t *testing.T) {
	h := newPhase11Harness(t)
	manager := h.seedUser(t, "manager", "secretsecret", false)
	g1 := h.createGroup(t, "g1")
	g2 := h.createGroup(t, "g2")
	h.assignUserRole(t, manager.ID, g1, auth.RoleManager)

	tmpl, err := h.templates.Create(context.Background(), "tmpl-g2", "", g2, manager.ID, types.AgentConfig{
		Name:        "Base",
		ModelConfig: types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:    "worker",
	}, nil, nil)
	if err != nil {
		t.Fatalf("Create template: %v", err)
	}

	cookie := h.login(t, "manager", "secretsecret")
	form := url.Values{}
	form.Set("name", "Bad Agent")
	form.Set("provider", "test-provider")
	form.Set("model", "test-model")
	form.Set("template", "worker")
	form.Set("from_template_id", tmpl.ID)
	resp := h.authedPostForm(t, "/agents", cookie, form)
	if resp.StatusCode == http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("expected forbidden or error, got 200: %s", body)
	}
	resp.Body.Close()
}

func TestPhase11_Templates_OverrideAuditEntry(t *testing.T) {
	h := newPhase11Harness(t)
	h.seedUser(t, "admin", "secretsecret", true)

	tmpl, err := h.templates.Create(context.Background(), "tmpl-audit", "", "", "", types.AgentConfig{
		Name:        "Base",
		ModelConfig: types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:    "worker",
	}, nil, map[string]types.ConstraintRule{
		"max_spend_per_day": {Min: floatPtr(1), Max: floatPtr(10)},
	})
	if err != nil {
		t.Fatalf("Create template: %v", err)
	}

	cookie := h.login(t, "admin", "secretsecret")
	form := url.Values{}
	form.Set("name", "Audited Agent")
	form.Set("provider", "test-provider")
	form.Set("model", "test-model")
	form.Set("template", "worker")
	form.Set("max_spend_per_day", "5")
	form.Set("from_template_id", tmpl.ID)
	resp := h.authedPostForm(t, "/agents", cookie, form)
	expectStatus(t, resp, http.StatusOK)

	h.audit.Flush()
	entries, _ := h.audit.Query(context.Background(), audit.Filter{EventType: "template", Limit: 100})
	found := false
	for _, e := range entries {
		if strings.Contains(e.Action, "override") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected template override audit entry")
	}
}

// =============================================================================
// SSE streaming
// =============================================================================

func TestPhase11_AuditSSE_RealtimeAndFilter(t *testing.T) {
	h := newPhase11Harness(t)
	h.seedUser(t, "admin", "secretsecret", true)
	cookie := h.login(t, "admin", "secretsecret")

	req, _ := http.NewRequest("GET", h.server.URL+"/audit/stream?agent_id=agent-1", nil)
	req.AddCookie(cookie)
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer resp.Body.Close()

	_ = h.audit.Log(context.Background(), types.AuditEntry{
		AgentID:   "agent-1",
		EventType: "test",
		Action:    "ping",
		Details:   "hello",
		Decision:  "allowed",
		RiskLevel: "low",
	})

	payload := readAuditEvent(t, resp.Body, 2*time.Second)
	if payload["agent_id"] != "agent-1" {
		t.Fatalf("expected agent-1 event")
	}
}

func TestPhase11_AuditSSE_ConnectionCleanup(t *testing.T) {
	h := newPhase11Harness(t)
	h.seedUser(t, "admin", "secretsecret", true)
	cookie := h.login(t, "admin", "secretsecret")

	req1, _ := http.NewRequest("GET", h.server.URL+"/audit/stream", nil)
	req1.AddCookie(cookie)
	resp1, err := h.client.Do(req1)
	if err != nil {
		t.Fatalf("stream1: %v", err)
	}

	req2, _ := http.NewRequest("GET", h.server.URL+"/audit/stream", nil)
	req2.AddCookie(cookie)
	resp2, err := h.client.Do(req2)
	if err != nil {
		t.Fatalf("stream2: %v", err)
	}
	if resp2.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", resp2.StatusCode)
	}
	resp2.Body.Close()

	resp1.Body.Close()
	time.Sleep(50 * time.Millisecond)

	req3, _ := http.NewRequest("GET", h.server.URL+"/audit/stream", nil)
	req3.AddCookie(cookie)
	resp3, err := h.client.Do(req3)
	if err != nil {
		t.Fatalf("stream3: %v", err)
	}
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp3.StatusCode)
	}
	resp3.Body.Close()
}

func TestPhase11_AuditSSE_RoleScoped(t *testing.T) {
	h := newPhase11Harness(t)
	manager := h.seedUser(t, "manager", "secretsecret", false)
	g1 := h.createGroup(t, "g1")
	g2 := h.createGroup(t, "g2")
	h.assignUserRole(t, manager.ID, g1, auth.RoleManager)

	h.seedAgent(t, "a1", "A1")
	h.seedAgent(t, "a2", "A2")
	_ = h.users.AddAgentToGroup(context.Background(), g1, "a1")
	_ = h.users.AddAgentToGroup(context.Background(), g2, "a2")

	cookie := h.login(t, "manager", "secretsecret")
	req, _ := http.NewRequest("GET", h.server.URL+"/audit/stream", nil)
	req.AddCookie(cookie)
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer resp.Body.Close()

	_ = h.audit.Log(context.Background(), types.AuditEntry{
		AgentID:   "a2",
		EventType: "test",
		Action:    "outside",
		Decision:  "allowed",
		RiskLevel: "low",
	})

	reader := bufio.NewReader(resp.Body)
	lineCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		line, err := reader.ReadString('\n')
		if err != nil {
			errCh <- err
			return
		}
		lineCh <- line
	}()
	select {
	case line := <-lineCh:
		if strings.Contains(line, "outside") {
			t.Fatalf("manager should not receive audit for non-visible agent")
		}
	case <-errCh:
		// No data within read or stream closed; acceptable for scoped filtering.
	case <-time.After(200 * time.Millisecond):
		// No event received; acceptable.
	}
}

// =============================================================================
// Cross-cutting scenarios
// =============================================================================

func TestPhase11_CrossCutting_ManagerFlow(t *testing.T) {
	h := newPhase11Harness(t)
	manager := h.seedUser(t, "manager", "secretsecret", false)
	g1 := h.createGroup(t, "g1")
	h.assignUserRole(t, manager.ID, g1, auth.RoleManager)

	tmpl, err := h.templates.Create(context.Background(), "tmpl-1", "", g1, manager.ID, types.AgentConfig{
		Name:        "Base",
		ModelConfig: types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:    "worker",
	}, nil, nil)
	if err != nil {
		t.Fatalf("Create template: %v", err)
	}

	cookie := h.login(t, "manager", "secretsecret")
	form := url.Values{}
	form.Set("name", "Flow Agent")
	form.Set("provider", "test-provider")
	form.Set("model", "test-model")
	form.Set("template", "worker")
	form.Set("from_template_id", tmpl.ID)
	resp := h.authedPostForm(t, "/agents", cookie, form)
	expectStatus(t, resp, http.StatusOK)

	agents, _ := h.store.ListAgents(context.Background())
	var agentID string
	for _, a := range agents {
		if a.Name == "Flow Agent" {
			agentID = a.ID
			break
		}
	}
	if agentID == "" {
		t.Fatalf("agent not created")
	}

	_ = h.kyvik.StartAgent(context.Background(), types.AgentConfig{
		ID:          agentID,
		Name:        "Flow Agent",
		ModelConfig: types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:    "worker",
	})

	// Chat stream
	req, _ := http.NewRequest("GET", h.server.URL+"/agents/"+agentID+"/chat/stream", nil)
	req.AddCookie(cookie)
	streamResp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("chat stream: %v", err)
	}
	defer streamResp.Body.Close()

	form = url.Values{}
	form.Set("message", "hi")
	resp = h.authedPostForm(t, "/agents/"+agentID+"/chat/send", cookie, form)
	expectStatus(t, resp, http.StatusOK)

	_ = readAuditEvent(t, streamResp.Body, 2*time.Second)

	spend := h.authedGet(t, "/spending/charts", cookie)
	expectStatus(t, spend, http.StatusOK)

	h.audit.Flush()
	entries, _ := h.audit.Query(context.Background(), audit.Filter{Limit: 100})
	found := false
	for _, e := range entries {
		if e.Action == "agent.created_from_template" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected audit for created_from_template")
	}
}

func TestPhase11_CrossCutting_OperatorAPIFlow(t *testing.T) {
	h := newPhase11Harness(t)
	h.seedUser(t, "admin", "secretsecret", true)
	operatorKey := h.createAPIKey(t, "op-key", auth.RoleOperator, nil)

	agentID := "op-agent"
	_ = h.kyvik.CreateAgent(context.Background(), types.AgentConfig{
		ID:          agentID,
		Name:        "Op Agent",
		ModelConfig: types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:    "worker",
	})

	start := h.apiRequest(t, "POST", "/api/v1/agents/"+agentID+"/start", operatorKey, nil)
	expectStatus(t, start, http.StatusOK)

	msg := types.Message{Role: "user", Content: "ping"}
	send := h.apiRequest(t, "POST", "/api/v1/agents/"+agentID+"/message", operatorKey, msg)
	expectStatus(t, send, http.StatusAccepted)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	h.waitForResponse(ctx, t, agentID)

	del := h.apiRequest(t, "DELETE", "/api/v1/agents/"+agentID, operatorKey, nil)
	expectStatus(t, del, http.StatusForbidden)
}

func TestPhase11_CrossCutting_AdminCloneFlow(t *testing.T) {
	h := newPhase11Harness(t)
	h.seedUser(t, "admin", "secretsecret", true)

	baseID := "base"
	cloneID := "clone"
	_ = h.kyvik.CreateAgent(context.Background(), types.AgentConfig{
		ID:          baseID,
		Name:        "Base",
		ModelConfig: types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:    "worker",
	})

	_ = h.kyvik.CreateAgent(context.Background(), types.AgentConfig{
		ID:           cloneID,
		Name:         "Clone",
		ModelConfig:  types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:     "worker",
		SystemPrompt: "clone prompt",
	})

	if err := h.kyvik.StartAgent(context.Background(), types.AgentConfig{
		ID:          baseID,
		Name:        "Base",
		ModelConfig: types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:    "worker",
	}); err != nil {
		t.Fatalf("StartAgent base: %v", err)
	}
	if err := h.kyvik.StartAgent(context.Background(), types.AgentConfig{
		ID:           cloneID,
		Name:         "Clone",
		ModelConfig:  types.ModelConfig{Provider: "test-provider", Model: "test-model"},
		Template:     "worker",
		SystemPrompt: "clone prompt",
	}); err != nil {
		t.Fatalf("StartAgent clone: %v", err)
	}

	ctx1, cancel1 := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel1()
	_ = h.kyvik.SendMessage(ctx1, baseID, types.Message{Role: "user", Content: "base"})
	h.waitForResponse(ctx1, t, baseID)

	ctx2, cancel2 := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel2()
	_ = h.kyvik.SendMessage(ctx2, cloneID, types.Message{Role: "user", Content: "clone"})
	h.waitForResponse(ctx2, t, cloneID)
}

// =============================================================================
// Security boundary tests
// =============================================================================

func TestPhase11_Security_AdminPagesRestricted(t *testing.T) {
	h := newPhase11Harness(t)
	view := h.seedUser(t, "view", "secretsecret", false)
	g1 := h.createGroup(t, "g1")
	h.assignUserRole(t, view.ID, g1, auth.RoleViewer)

	cookie := h.login(t, "view", "secretsecret")
	resp := h.authedGet(t, "/users", cookie)
	expectStatus(t, resp, http.StatusForbidden)
}

func TestPhase11_Security_APIKeyVsDashboard(t *testing.T) {
	h := newPhase11Harness(t)
	h.seedUser(t, "admin", "secretsecret", true)
	key := h.createAPIKey(t, "admin-key", auth.RoleAdmin, nil)

	// Use h.client (no-redirect) — an API key should NOT grant dashboard access.
	req, _ := http.NewRequest("GET", h.server.URL+"/agents", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected redirect to login, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	cookie := h.login(t, "admin", "secretsecret")
	req, _ = http.NewRequest("GET", h.server.URL+"/api/v1/status", nil)
	req.AddCookie(cookie)
	resp, err = h.client.Do(req)
	if err != nil {
		t.Fatalf("status request: %v", err)
	}
	expectStatus(t, resp, http.StatusUnauthorized)
}

func TestPhase11_Security_ExpiredSession(t *testing.T) {
	h := newPhase11Harness(t)
	user := h.seedUser(t, "user", "secretsecret", false)

	now := time.Now().UTC()
	exp := now.Add(-time.Hour)
	sess := types.UserSession{
		ID:        uuid.NewString(),
		UserID:    user.ID,
		CreatedAt: now.Add(-2 * time.Hour),
		ExpiresAt: exp,
	}
	if err := h.store.CreateSession(context.Background(), sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	req, _ := http.NewRequest("GET", h.server.URL+"/agents", nil)
	req.AddCookie(&http.Cookie{Name: "kyvik_session", Value: sess.ID})
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", resp.StatusCode)
	}
}

func floatPtr(v float64) *float64 { return &v }
