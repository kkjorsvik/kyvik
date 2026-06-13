package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/auth"
	"github.com/kkjorsvik/kyvik/internal/authprovider/local"
	"github.com/kkjorsvik/kyvik/internal/store/postgres"
	"github.com/kkjorsvik/kyvik/internal/testutil"
	"github.com/kkjorsvik/kyvik/internal/users"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

func newUserService(t *testing.T) (*users.Service, *postgres.PostgresStore) {
	t.Helper()
	tdb := testutil.RequirePostgres(t)
	s := tdb.Store

	us := users.New(s, users.AuthConfig{SessionTTL: time.Hour, MaxSessionsPerUser: 3})
	created, _, err := us.BootstrapAdminIfEmpty(context.Background(), "admin", "secret-secret")
	if err != nil {
		t.Fatalf("BootstrapAdminIfEmpty: %v", err)
	}
	if !created {
		t.Fatal("expected bootstrap admin to be created")
	}
	return us, s
}

func createHandlerWithUsers(t *testing.T) (*Handlers, *users.Service, *postgres.PostgresStore) {
	t.Helper()
	us, s := newUserService(t)
	h := newTestHandlers()
	h.SetAuthProvider(local.New(us))
	return h, us, s
}

func TestUsersCreateUpdateResetDelete(t *testing.T) {
	ctx := context.Background()
	h, us, s := createHandlerWithUsers(t)

	form := url.Values{}
	form.Set("username", "alice")
	form.Set("display_name", "Alice")
	form.Set("password", "strong-pass-01")
	req := httptest.NewRequest("POST", "/users", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.UsersCreate(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("UsersCreate code = %d, want 303", rec.Code)
	}

	alice, err := s.GetUserByUsername(ctx, "alice")
	if err != nil {
		t.Fatalf("GetUserByUsername(alice): %v", err)
	}

	update := url.Values{}
	update.Set("display_name", "Alice Updated")
	update.Set("is_active", "on")
	req = httptest.NewRequest("POST", "/users/"+alice.ID+"/edit", strings.NewReader(update.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", alice.ID)
	rec = httptest.NewRecorder()
	h.UsersUpdate(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("UsersUpdate code = %d, want 303", rec.Code)
	}
	alice, err = s.GetUser(ctx, alice.ID)
	if err != nil {
		t.Fatalf("GetUser(alice): %v", err)
	}
	if alice.DisplayName != "Alice Updated" {
		t.Fatalf("display_name = %q, want %q", alice.DisplayName, "Alice Updated")
	}

	reset := url.Values{}
	reset.Set("new_password", "strong-pass-02")
	req = httptest.NewRequest("POST", "/users/"+alice.ID+"/reset-password", strings.NewReader(reset.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", alice.ID)
	rec = httptest.NewRecorder()
	h.UsersResetPassword(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("UsersResetPassword code = %d, want 303", rec.Code)
	}
	lr, err := us.Authenticate(ctx, "alice", "strong-pass-02", "127.0.0.1", "ua")
	if err != nil {
		t.Fatalf("Authenticate(alice): %v", err)
	}
	if !lr.ForcePasswordChange {
		t.Fatal("expected ForcePasswordChange=true after admin reset")
	}

	admin, err := s.GetUserByUsername(ctx, "admin")
	if err != nil {
		t.Fatalf("GetUserByUsername(admin): %v", err)
	}
	deleteReq := httptest.NewRequest("POST", "/users/"+alice.ID+"/delete", nil)
	deleteReq.SetPathValue("id", alice.ID)
	deleteReq = deleteReq.WithContext(context.WithValue(deleteReq.Context(), userCtxKey{}, dashboardUser{
		ID:      admin.ID,
		IsAdmin: true,
		Role:    auth.RoleAdmin,
	}))
	rec = httptest.NewRecorder()
	h.UsersDelete(rec, deleteReq)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("UsersDelete code = %d, want 303", rec.Code)
	}
	if _, err := s.GetUser(ctx, alice.ID); err == nil {
		t.Fatal("expected deleted user lookup to fail")
	}
}

func TestGroupsCreateAssignRoleAndAgent(t *testing.T) {
	ctx := context.Background()
	h, us, s := createHandlerWithUsers(t)

	agent := types.AgentConfig{
		ID:          "agent-1",
		Name:        "Agent One",
		ModelConfig: types.ModelConfig{Provider: "openrouter", Model: "test-model"},
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	if err := s.CreateAgent(ctx, agent); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	u, err := us.CreateUser(ctx, users.CreateUserParams{
		Username:    "bob",
		Password:    "strong-pass-03",
		DisplayName: "Bob",
	})
	if err != nil {
		t.Fatalf("CreateUser(bob): %v", err)
	}

	createGroup := url.Values{}
	createGroup.Set("name", "Ops")
	createGroup.Set("description", "Operations")
	req := httptest.NewRequest("POST", "/groups", strings.NewReader(createGroup.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.GroupsCreate(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("GroupsCreate code = %d, want 303", rec.Code)
	}

	groups, err := us.ListGroups(ctx)
	if err != nil {
		t.Fatalf("ListGroups: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("groups len = %d, want 1", len(groups))
	}
	groupID := groups[0].ID

	assignRole := url.Values{}
	assignRole.Set("user_id", u.ID)
	assignRole.Set("role", auth.RoleOperator)
	req = httptest.NewRequest("POST", "/groups/"+groupID+"/users", strings.NewReader(assignRole.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", groupID)
	rec = httptest.NewRecorder()
	h.GroupSetUserRole(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("GroupSetUserRole code = %d, want 303", rec.Code)
	}

	roles, err := us.UserGroupRoles(ctx, u.ID)
	if err != nil {
		t.Fatalf("UserGroupRoles: %v", err)
	}
	if len(roles) != 1 || roles[0].GroupID != groupID || roles[0].Role != auth.RoleOperator {
		t.Fatalf("unexpected roles: %+v", roles)
	}

	addAgent := url.Values{}
	addAgent.Set("agent_id", agent.ID)
	req = httptest.NewRequest("POST", "/groups/"+groupID+"/agents", strings.NewReader(addAgent.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", groupID)
	rec = httptest.NewRecorder()
	h.GroupAddAgent(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("GroupAddAgent code = %d, want 303", rec.Code)
	}

	agentIDs, err := us.GroupAgentIDs(ctx, groupID)
	if err != nil {
		t.Fatalf("GroupAgentIDs: %v", err)
	}
	if len(agentIDs) != 1 || agentIDs[0] != agent.ID {
		t.Fatalf("unexpected group agent ids: %+v", agentIDs)
	}
}
