package integration

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/kkjorsvik/kyvik/internal/auth"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// =============================================================================
// Scenario: Multi-User Access Control
// Tests RBAC with groups, scoping, API keys, and authentication boundaries.
// =============================================================================

func TestScenario_Access_FourRoles_Visibility(t *testing.T) {
	// t.Parallel() — disabled: shared PostgreSQL database
	h := newFullHarness(t)

	admin := h.seedUser(t, "admin", "secretsecret", true)
	manager := h.seedUser(t, "manager", "secretsecret", false)
	operator := h.seedUser(t, "operator", "secretsecret", false)
	viewer := h.seedUser(t, "viewer", "secretsecret", false)
	_ = admin

	g1 := h.createGroup(t, "team-alpha")
	g2 := h.createGroup(t, "team-beta")

	h.assignUserRole(t, manager.ID, g1, auth.RoleManager)
	h.assignUserRole(t, operator.ID, g1, auth.RoleOperator)
	h.assignUserRole(t, viewer.ID, g2, auth.RoleViewer)

	h.seedAgent(t, "alpha-1", "Alpha 1", "worker")
	h.seedAgent(t, "alpha-2", "Alpha 2", "worker")
	h.seedAgent(t, "beta-1", "Beta 1", "worker")

	_ = h.users.AddAgentToGroup(context.Background(), g1, "alpha-1")
	_ = h.users.AddAgentToGroup(context.Background(), g1, "alpha-2")
	_ = h.users.AddAgentToGroup(context.Background(), g2, "beta-1")

	// Admin sees everything.
	adminCookie := h.login(t, "admin", "secretsecret")
	resp := h.authedGet(t, "/agents", adminCookie)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	for _, name := range []string{"Alpha 1", "Alpha 2", "Beta 1"} {
		if !strings.Contains(string(body), name) {
			t.Fatalf("admin should see %s", name)
		}
	}

	// Manager sees only their group.
	managerCookie := h.login(t, "manager", "secretsecret")
	resp = h.authedGet(t, "/agents", managerCookie)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "Alpha 1") {
		t.Fatal("manager should see Alpha 1")
	}
	if strings.Contains(string(body), "Beta 1") {
		t.Fatal("manager should not see Beta 1")
	}

	// Operator sees only their group.
	opCookie := h.login(t, "operator", "secretsecret")
	resp = h.authedGet(t, "/agents", opCookie)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "Alpha 1") {
		t.Fatal("operator should see Alpha 1")
	}
	if strings.Contains(string(body), "Beta 1") {
		t.Fatal("operator should not see Beta 1")
	}

	// Viewer sees only their group.
	viewerCookie := h.login(t, "viewer", "secretsecret")
	resp = h.authedGet(t, "/agents", viewerCookie)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "Beta 1") {
		t.Fatal("viewer should see Beta 1")
	}
	if strings.Contains(string(body), "Alpha 1") {
		t.Fatal("viewer should not see Alpha 1")
	}
}

func TestScenario_Access_ManagerCanCreate(t *testing.T) {
	// t.Parallel() — disabled: shared PostgreSQL database
	h := newFullHarness(t)

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
	form.Set("name", "Manager Created")
	form.Set("provider", "test-provider")
	form.Set("model", "test-model")
	form.Set("template", "worker")
	form.Set("from_template_id", tmpl.ID)
	resp := h.authedPostForm(t, "/agents", cookie, form)
	expectStatus(t, resp, http.StatusOK)

	// Verify the agent was created.
	agents, _ := h.store.ListAgents(context.Background())
	found := false
	for _, a := range agents {
		if a.Name == "Manager Created" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("agent not created by manager")
	}
}

func TestScenario_Access_OperatorStartStopOnly(t *testing.T) {
	// t.Parallel() — disabled: shared PostgreSQL database
	h := newFullHarness(t)

	op := h.seedUser(t, "op", "secretsecret", false)
	g1 := h.createGroup(t, "g1")
	h.assignUserRole(t, op.ID, g1, auth.RoleOperator)
	h.seedAgent(t, "op-agent", "Op Agent", "worker")
	_ = h.users.AddAgentToGroup(context.Background(), g1, "op-agent")

	cookie := h.login(t, "op", "secretsecret")

	// Start should succeed.
	resp := h.authedPostForm(t, "/agents/op-agent/start", cookie, url.Values{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("start expected 200, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Stop should succeed.
	resp = h.authedPostForm(t, "/agents/op-agent/stop", cookie, url.Values{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stop expected 200, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Edit should fail.
	resp = h.authedGet(t, "/agents/op-agent/edit", cookie)
	expectStatus(t, resp, http.StatusForbidden)
}

func TestScenario_Access_ViewerReadOnly(t *testing.T) {
	// t.Parallel() — disabled: shared PostgreSQL database
	h := newFullHarness(t)

	viewer := h.seedUser(t, "viewer", "secretsecret", false)
	g1 := h.createGroup(t, "g1")
	h.assignUserRole(t, viewer.ID, g1, auth.RoleViewer)
	h.seedAgent(t, "view-agent", "View Agent", "worker")
	_ = h.users.AddAgentToGroup(context.Background(), g1, "view-agent")

	cookie := h.login(t, "viewer", "secretsecret")

	// GET should succeed.
	resp := h.authedGet(t, "/agents", cookie)
	expectStatus(t, resp, http.StatusOK)

	// Start should fail.
	resp = h.authedPostForm(t, "/agents/view-agent/start", cookie, url.Values{})
	expectStatus(t, resp, http.StatusForbidden)

	// Stop should fail.
	resp = h.authedPostForm(t, "/agents/view-agent/stop", cookie, url.Values{})
	expectStatus(t, resp, http.StatusForbidden)
}

func TestScenario_Access_APIKey_ScopeEnforcement(t *testing.T) {
	// t.Parallel() — disabled: shared PostgreSQL database
	h := newFullHarness(t)

	h.seedUser(t, "admin", "secretsecret", true)
	adminKey := h.createAPIKey(t, "admin-key", auth.RoleAdmin, nil)
	viewerKey := h.createAPIKey(t, "viewer-key", auth.RoleViewer, nil)

	// Seed agents directly.
	for _, id := range []string{"scope-a", "scope-b"} {
		h.seedAgent(t, id, id, "worker")
	}

	// Admin key → full access.
	resp := h.apiRequest(t, "GET", "/api/v1/agents", adminKey, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin list: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Viewer key → read-only.
	resp = h.apiRequest(t, "POST", "/api/v1/agents", viewerKey, types.AgentConfig{ID: "nope"})
	expectStatus(t, resp, http.StatusForbidden)

	// Agent-scoped key → only sees listed agents.
	scopedKey := h.createAPIKey(t, "scoped-key", auth.RoleManager, []string{"scope-a"})
	listResp := h.apiRequest(t, "GET", "/api/v1/agents", scopedKey, nil)
	defer listResp.Body.Close()
	var listBody map[string]json.RawMessage
	_ = json.NewDecoder(listResp.Body).Decode(&listBody)
	var agents []types.AgentConfig
	_ = json.Unmarshal(listBody["data"], &agents)
	if len(agents) != 1 || agents[0].ID != "scope-a" {
		t.Fatalf("scoped key should see only scope-a, got %d agents", len(agents))
	}

	getResp := h.apiRequest(t, "GET", "/api/v1/agents/scope-b", scopedKey, nil)
	expectStatus(t, getResp, http.StatusForbidden)
}

func TestScenario_Access_MultiGroupResolution(t *testing.T) {
	// t.Parallel() — disabled: shared PostgreSQL database
	h := newFullHarness(t)

	user := h.seedUser(t, "multi", "secretsecret", false)
	g1 := h.createGroup(t, "g1")
	g2 := h.createGroup(t, "g2")
	h.assignUserRole(t, user.ID, g1, auth.RoleViewer)
	h.assignUserRole(t, user.ID, g2, auth.RoleManager)

	h.seedAgent(t, "multi-agent", "Multi Agent", "worker")
	_ = h.users.AddAgentToGroup(context.Background(), g1, "multi-agent")
	_ = h.users.AddAgentToGroup(context.Background(), g2, "multi-agent")

	role, found, err := h.users.ResolveAgentRole(context.Background(), user.ID, "multi-agent")
	if err != nil || !found {
		t.Fatalf("ResolveAgentRole: %v found=%v", err, found)
	}
	if role != auth.RoleManager {
		t.Fatalf("expected manager (highest), got %s", role)
	}
}

func TestScenario_Access_AdminSeesEverything(t *testing.T) {
	// t.Parallel() — disabled: shared PostgreSQL database
	h := newFullHarness(t)

	h.seedUser(t, "admin", "secretsecret", true)

	// Agents not in any group.
	h.seedAgent(t, "ungroup-1", "Ungrouped 1", "worker")
	h.seedAgent(t, "ungroup-2", "Ungrouped 2", "worker")

	cookie := h.login(t, "admin", "secretsecret")
	resp := h.authedGet(t, "/agents", cookie)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if !strings.Contains(string(body), "Ungrouped 1") || !strings.Contains(string(body), "Ungrouped 2") {
		t.Fatal("admin should see all agents including ungrouped")
	}
}

func TestScenario_Access_UnauthenticatedRejected(t *testing.T) {
	// t.Parallel() — disabled: shared PostgreSQL database
	h := newFullHarness(t)

	// Web request without auth → redirect to login (302 or 303).
	req, _ := http.NewRequest("GET", h.server.URL+"/agents", nil)
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther && resp.StatusCode != http.StatusFound {
		t.Fatalf("expected redirect for unauthenticated web request, got %d", resp.StatusCode)
	}

	// API request without auth → 401.
	apiResp := h.apiRequest(t, "GET", "/api/v1/agents", "", nil)
	expectStatus(t, apiResp, http.StatusUnauthorized)
}
