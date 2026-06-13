package users_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/auth"
	"github.com/kkjorsvik/kyvik/internal/store/postgres"
	"github.com/kkjorsvik/kyvik/internal/testutil"
	"github.com/kkjorsvik/kyvik/internal/users"
	"github.com/kkjorsvik/kyvik/pkg/types"
	"golang.org/x/crypto/bcrypt"
)

func newService(t *testing.T) (*users.Service, *postgres.PostgresStore) {
	t.Helper()
	tdb := testutil.RequirePostgres(t)

	svc := users.New(tdb.Store, users.AuthConfig{
		SessionTTL:         time.Hour,
		MaxSessionsPerUser: 3,
		BootstrapCredsPath: filepath.Join(t.TempDir(), "initial-credentials"),
	})
	return svc, tdb.Store
}

func createScopedTestAgent(t *testing.T, s *postgres.PostgresStore, id string) {
	t.Helper()
	now := time.Now().UTC()
	if err := s.CreateAgent(context.Background(), types.AgentConfig{
		ID:          id,
		Name:        "Agent " + id,
		ModelConfig: types.ModelConfig{Provider: "openrouter", Model: "test-model"},
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("CreateAgent(%s): %v", id, err)
	}
}

func TestBootstrapAdminIfEmpty(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()

	created, pwd, err := svc.BootstrapAdminIfEmpty(ctx, "", "")
	if err != nil {
		t.Fatalf("BootstrapAdminIfEmpty: %v", err)
	}
	if !created {
		t.Fatal("created = false, want true")
	}
	if pwd == "" {
		t.Fatal("generated password is empty")
	}
}

func TestAuthenticateCreatesSession(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()

	_, _, err := svc.BootstrapAdminIfEmpty(ctx, "admin", "secret")
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	lr, err := svc.Authenticate(ctx, "admin", "secret", "127.0.0.1", "test-agent")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if lr.SessionID == "" {
		t.Fatal("SessionID is empty")
	}

	u, err := svc.ValidateSession(ctx, lr.SessionID)
	if err != nil {
		t.Fatalf("ValidateSession: %v", err)
	}
	if u.Username != "admin" {
		t.Errorf("username = %q, want admin", u.Username)
	}
}

func TestAuthenticateRejectsWrongPassword(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()

	_, _, err := svc.BootstrapAdminIfEmpty(ctx, "admin", "secret")
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	_, err = svc.Authenticate(ctx, "admin", "wrong", "127.0.0.1", "test-agent")
	if !errors.Is(err, types.ErrPermissionDenied) {
		t.Fatalf("Authenticate err = %v, want ErrPermissionDenied", err)
	}
}

func TestSessionLimitEnforced(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()

	_, _, err := svc.BootstrapAdminIfEmpty(ctx, "admin", "secret")
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	var first string
	for i := 0; i < 4; i++ {
		lr, err := svc.Authenticate(ctx, "admin", "secret", "127.0.0.1", "ua")
		if err != nil {
			t.Fatalf("Authenticate(%d): %v", i, err)
		}
		if i == 0 {
			first = lr.SessionID
		}
	}

	_, err = svc.ValidateSession(ctx, first)
	if !errors.Is(err, types.ErrSessionNotFound) {
		t.Fatalf("ValidateSession(first) err = %v, want ErrSessionNotFound", err)
	}
}

func TestValidateSessionRefreshesLastSeen(t *testing.T) {
	svc, s := newService(t)
	ctx := context.Background()

	_, _, err := svc.BootstrapAdminIfEmpty(ctx, "admin", "secret")
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	lr, err := svc.Authenticate(ctx, "admin", "secret", "127.0.0.1", "ua")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	old := time.Now().UTC().Add(-2 * time.Minute)
	if err := s.UpdateSessionLastSeen(ctx, lr.SessionID, old); err != nil {
		t.Fatalf("UpdateSessionLastSeen: %v", err)
	}

	if _, err := svc.ValidateSession(ctx, lr.SessionID); err != nil {
		t.Fatalf("ValidateSession: %v", err)
	}
	sess, err := s.GetSession(ctx, lr.SessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.LastSeenAt == nil || sess.LastSeenAt.Before(old) {
		t.Fatalf("last_seen not refreshed: got=%v old=%v", sess.LastSeenAt, old)
	}
}

func TestResolveAgentRoleHighestAcrossSharedGroups(t *testing.T) {
	svc, s := newService(t)
	ctx := context.Background()

	hash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	if err := s.CreateUser(ctx, types.User{
		ID:           "u1",
		Username:     "u1",
		PasswordHash: string(hash),
		DisplayName:  "User One",
		IsActive:     true,
		CreatedAt:    time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := s.CreateAgentGroup(ctx, types.AgentGroup{ID: "g1", Name: "g1", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("CreateAgentGroup g1: %v", err)
	}
	if err := s.CreateAgentGroup(ctx, types.AgentGroup{ID: "g2", Name: "g2", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("CreateAgentGroup g2: %v", err)
	}
	createScopedTestAgent(t, s, "a1")
	if err := s.SetUserGroupRole(ctx, types.UserGroupRole{UserID: "u1", GroupID: "g1", Role: auth.RoleViewer}); err != nil {
		t.Fatalf("SetUserGroupRole g1: %v", err)
	}
	if err := s.SetUserGroupRole(ctx, types.UserGroupRole{UserID: "u1", GroupID: "g2", Role: auth.RoleManager}); err != nil {
		t.Fatalf("SetUserGroupRole g2: %v", err)
	}
	if err := s.SetAgentGroupMember(ctx, "a1", "g1"); err != nil {
		t.Fatalf("SetAgentGroupMember a1-g1: %v", err)
	}
	if err := s.SetAgentGroupMember(ctx, "a1", "g2"); err != nil {
		t.Fatalf("SetAgentGroupMember a1-g2: %v", err)
	}

	role, found, err := svc.ResolveAgentRole(ctx, "u1", "a1")
	if err != nil {
		t.Fatalf("ResolveAgentRole: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if role != auth.RoleManager {
		t.Fatalf("role = %q, want %q", role, auth.RoleManager)
	}
}

func TestResolveAgentRoleNoGroupAccess(t *testing.T) {
	svc, s := newService(t)
	ctx := context.Background()

	hash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	if err := s.CreateUser(ctx, types.User{
		ID:           "u1",
		Username:     "u1",
		PasswordHash: string(hash),
		DisplayName:  "User One",
		IsActive:     true,
		CreatedAt:    time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := s.CreateAgentGroup(ctx, types.AgentGroup{ID: "g1", Name: "g1", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("CreateAgentGroup: %v", err)
	}
	createScopedTestAgent(t, s, "a1")
	if err := s.SetAgentGroupMember(ctx, "a1", "g1"); err != nil {
		t.Fatalf("SetAgentGroupMember: %v", err)
	}

	_, found, err := svc.ResolveAgentRole(ctx, "u1", "a1")
	if err != nil {
		t.Fatalf("ResolveAgentRole: %v", err)
	}
	if found {
		t.Fatal("expected found=false")
	}
}

func TestCreateUserAndResetPassword(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()

	u, err := svc.CreateUser(ctx, users.CreateUserParams{
		Username:    "alice",
		Password:    "verysecure1",
		DisplayName: "Alice",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if u.Username != "alice" {
		t.Fatalf("username = %q, want alice", u.Username)
	}

	if err := svc.ResetPassword(ctx, u.ID, "verysecure2"); err != nil {
		t.Fatalf("ResetPassword: %v", err)
	}
	lr, err := svc.Authenticate(ctx, "alice", "verysecure2", "127.0.0.1", "ua")
	if err != nil {
		t.Fatalf("Authenticate after reset: %v", err)
	}
	if !lr.ForcePasswordChange {
		t.Fatal("expected ForcePasswordChange=true after admin reset")
	}
}
