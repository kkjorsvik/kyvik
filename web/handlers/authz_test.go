package handlers

import (
	"context"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/kkjorsvik/kyvik/internal/auth"
	"github.com/kkjorsvik/kyvik/internal/authprovider/local"
	"github.com/kkjorsvik/kyvik/internal/testutil"
	"github.com/kkjorsvik/kyvik/internal/users"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

func TestVisibleAgentIDsScopedByGroups(t *testing.T) {
	ctx := context.Background()
	tdb := testutil.RequirePostgres(t)
	s := tdb.Store

	us := users.New(s, users.AuthConfig{SessionTTL: time.Hour, MaxSessionsPerUser: 3})
	hash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	if err := s.CreateUser(ctx, types.User{
		ID:           "u1",
		Username:     "u1",
		PasswordHash: string(hash),
		DisplayName:  "U1",
		IsActive:     true,
		CreatedAt:    time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	for _, id := range []string{"a1", "a2"} {
		if err := s.CreateAgent(ctx, types.AgentConfig{
			ID:          id,
			Name:        id,
			ModelConfig: types.ModelConfig{Provider: "openrouter", Model: "test-model"},
			CreatedAt:   time.Now().UTC(),
			UpdatedAt:   time.Now().UTC(),
		}); err != nil {
			t.Fatalf("CreateAgent(%s): %v", id, err)
		}
	}
	if err := s.CreateAgentGroup(ctx, types.AgentGroup{ID: "g1", Name: "g1", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("CreateAgentGroup: %v", err)
	}
	if err := s.SetAgentGroupMember(ctx, "a1", "g1"); err != nil {
		t.Fatalf("SetAgentGroupMember: %v", err)
	}
	if err := s.SetUserGroupRole(ctx, types.UserGroupRole{UserID: "u1", GroupID: "g1", Role: auth.RoleViewer}); err != nil {
		t.Fatalf("SetUserGroupRole: %v", err)
	}

	h := newTestHandlers()
	h.SetAuthProvider(local.New(us))
	ctx = context.WithValue(ctx, userCtxKey{}, dashboardUser{ID: "u1", Username: "u1", Role: auth.RoleViewer, IsAdmin: false})
	filtered, err := h.visibleAgentIDs(ctx, []string{"a1", "a2"})
	if err != nil {
		t.Fatalf("visibleAgentIDs: %v", err)
	}
	if len(filtered) != 1 || filtered[0] != "a1" {
		t.Fatalf("filtered = %+v, want [a1]", filtered)
	}
}
