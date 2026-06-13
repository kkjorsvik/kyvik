package delegated_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/authprovider"
	"github.com/kkjorsvik/kyvik/internal/authprovider/delegated"
	"github.com/kkjorsvik/kyvik/internal/store/postgres"
	"github.com/kkjorsvik/kyvik/internal/testutil"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// Compile-time interface check.
var _ authprovider.AuthProvider = (*delegated.Provider)(nil)

const (
	testSettURL      = "https://sett.example.com"
	testInstanceID   = "inst-abc-123"
	testSharedSecret = "super-secret-shared-key-for-hmac"
	testCallbackURL  = "https://my-instance.example.com/auth/callback"
)

// testClaims is the JWT claims structure matching the provider's internal format.
type testClaims struct {
	Sub         string     `json:"sub"`
	Username    string     `json:"username"`
	DisplayName string     `json:"display_name"`
	IsAdmin     bool       `json:"is_admin"`
	Roles       []testRole `json:"roles"`
	Exp         int64      `json:"exp"`
	Aud         string     `json:"aud"`
}

type testRole struct {
	GroupID string `json:"group_id"`
	Role    string `json:"role"`
}

// makeJWT builds a signed JWT with the given claims and secret.
func makeJWT(t *testing.T, claims testClaims, secret string) string {
	t.Helper()

	header := map[string]string{"alg": "HS256", "typ": "JWT"}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}

	headerB64 := base64.RawURLEncoding.EncodeToString(headerJSON)
	claimsB64 := base64.RawURLEncoding.EncodeToString(claimsJSON)
	signingInput := headerB64 + "." + claimsB64

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signingInput))
	signature := mac.Sum(nil)
	sigB64 := base64.RawURLEncoding.EncodeToString(signature)

	return signingInput + "." + sigB64
}

func validClaims() testClaims {
	return testClaims{
		Sub:         "user-uuid-from-sett",
		Username:    "alice",
		DisplayName: "Alice Smith",
		IsAdmin:     false,
		Roles: []testRole{
			{GroupID: "group-1", Role: "operator"},
		},
		Exp: time.Now().Add(10 * time.Minute).Unix(),
		Aud: testInstanceID,
	}
}

// mustNew is a test helper that calls delegated.New and fails the test on error.
func mustNew(t *testing.T, store delegated.Store, cfg delegated.Config) *delegated.Provider {
	t.Helper()
	p, err := delegated.New(store, cfg)
	if err != nil {
		t.Fatalf("delegated.New() error: %v", err)
	}
	return p
}

func newTestProvider(t *testing.T) (*delegated.Provider, *postgres.PostgresStore) {
	t.Helper()
	tdb := testutil.RequirePostgres(t)
	s := tdb.Store

	// Pre-create agent groups referenced by validClaims() and common tests.
	ctx := context.Background()
	for _, gid := range []string{"group-1", "group-2", "group-3"} {
		if err := s.CreateAgentGroup(ctx, types.AgentGroup{
			ID:   gid,
			Name: gid,
		}); err != nil {
			t.Fatalf("create agent group %s: %v", gid, err)
		}
	}

	p := mustNew(t, s, delegated.Config{
		SettURL:      testSettURL,
		InstanceID:   testInstanceID,
		SharedSecret: testSharedSecret,
		CallbackURL:  testCallbackURL,
		SessionTTL:   24 * time.Hour,
	})
	return p, s
}

// createStubAgent inserts a minimal agent row so FK constraints on
// agent_group_members are satisfied. Only the id column matters for these tests.
func createStubAgent(t *testing.T, s *postgres.PostgresStore, agentID string) {
	t.Helper()
	if err := s.CreateAgent(context.Background(), types.AgentConfig{
		ID:   agentID,
		Name: agentID,
	}); err != nil {
		t.Fatalf("create stub agent %s: %v", agentID, err)
	}
}

func callbackRequest(t *testing.T, token string) *http.Request {
	t.Helper()
	u := fmt.Sprintf("https://my-instance.example.com/auth/callback?assertion=%s", url.QueryEscape(token))
	r := httptest.NewRequest(http.MethodGet, u, nil)
	return r
}

// --- Constructor validation tests ---

func TestNew_RejectsEmptySettURL(t *testing.T) {
	tdb := testutil.RequirePostgres(t)

	_, err := delegated.New(tdb.Store, delegated.Config{
		InstanceID:   testInstanceID,
		SharedSecret: testSharedSecret,
		CallbackURL:  testCallbackURL,
	})
	if err == nil {
		t.Fatal("expected error for empty SettURL")
	}
	if !strings.Contains(err.Error(), "sett_url") {
		t.Errorf("error = %q, want mention of 'sett_url'", err)
	}
}

func TestNew_RejectsEmptyInstanceID(t *testing.T) {
	tdb := testutil.RequirePostgres(t)

	_, err := delegated.New(tdb.Store, delegated.Config{
		SettURL:      testSettURL,
		SharedSecret: testSharedSecret,
		CallbackURL:  testCallbackURL,
	})
	if err == nil {
		t.Fatal("expected error for empty InstanceID")
	}
	if !strings.Contains(err.Error(), "instance_id") {
		t.Errorf("error = %q, want mention of 'instance_id'", err)
	}
}

func TestNew_RejectsEmptySharedSecret(t *testing.T) {
	tdb := testutil.RequirePostgres(t)

	_, err := delegated.New(tdb.Store, delegated.Config{
		SettURL:    testSettURL,
		InstanceID: testInstanceID,
		CallbackURL: testCallbackURL,
	})
	if err == nil {
		t.Fatal("expected error for empty SharedSecret")
	}
	if !strings.Contains(err.Error(), "shared_secret") {
		t.Errorf("error = %q, want mention of 'shared_secret'", err)
	}
}

func TestNew_RejectsEmptyCallbackURL(t *testing.T) {
	tdb := testutil.RequirePostgres(t)

	_, err := delegated.New(tdb.Store, delegated.Config{
		SettURL:      testSettURL,
		InstanceID:   testInstanceID,
		SharedSecret: testSharedSecret,
	})
	if err == nil {
		t.Fatal("expected error for empty CallbackURL")
	}
	if !strings.Contains(err.Error(), "callback_url") {
		t.Errorf("error = %q, want mention of 'callback_url'", err)
	}
}

// --- Login tests ---

func TestLogin_ReturnsRedirectURL(t *testing.T) {
	p, _ := newTestProvider(t)
	result, redirectURL, err := p.Login(context.Background(), "", "", "", "")
	if err != nil {
		t.Fatalf("Login() error: %v", err)
	}
	if result != nil {
		t.Fatalf("Login() should return nil result for redirect, got %+v", result)
	}

	expected := fmt.Sprintf("%s/login?instance_id=%s&redirect_back=%s",
		testSettURL,
		url.QueryEscape(testInstanceID),
		url.QueryEscape(testCallbackURL),
	)
	if redirectURL != expected {
		t.Errorf("Login() redirect URL:\ngot  %s\nwant %s", redirectURL, expected)
	}
}

// --- HandleCallback tests ---

func TestHandleCallback_ValidToken(t *testing.T) {
	p, s := newTestProvider(t)
	ctx := context.Background()

	claims := validClaims()
	token := makeJWT(t, claims, testSharedSecret)

	r := callbackRequest(t, token)
	result, err := p.HandleCallback(ctx, r)
	if err != nil {
		t.Fatalf("HandleCallback() error: %v", err)
	}

	if result.UserID != claims.Sub {
		t.Errorf("UserID = %q, want %q", result.UserID, claims.Sub)
	}
	if result.Username != claims.Username {
		t.Errorf("Username = %q, want %q", result.Username, claims.Username)
	}
	if result.SessionID == "" {
		t.Error("SessionID is empty")
	}
	if result.ForcePasswordChange {
		t.Error("ForcePasswordChange should be false for delegated auth")
	}
	if result.ExpiresAt.IsZero() {
		t.Error("ExpiresAt should not be zero")
	}

	// Verify user was created in the store.
	user, err := s.GetUser(ctx, claims.Sub)
	if err != nil {
		t.Fatalf("GetUser() error: %v", err)
	}
	if user.Username != claims.Username {
		t.Errorf("stored Username = %q, want %q", user.Username, claims.Username)
	}
	if user.DisplayName != claims.DisplayName {
		t.Errorf("stored DisplayName = %q, want %q", user.DisplayName, claims.DisplayName)
	}
	if user.IsAdmin != claims.IsAdmin {
		t.Errorf("stored IsAdmin = %v, want %v", user.IsAdmin, claims.IsAdmin)
	}
	if !user.IsActive {
		t.Error("stored user should be active")
	}

	// Verify session was created.
	sess, err := s.GetSession(ctx, result.SessionID)
	if err != nil {
		t.Fatalf("GetSession() error: %v", err)
	}
	if sess.UserID != claims.Sub {
		t.Errorf("session UserID = %q, want %q", sess.UserID, claims.Sub)
	}

	// Verify roles were synced.
	roles, err := s.ListUserGroupRoles(ctx, claims.Sub)
	if err != nil {
		t.Fatalf("ListUserGroupRoles() error: %v", err)
	}
	if len(roles) != 1 {
		t.Fatalf("expected 1 role, got %d", len(roles))
	}
	if roles[0].GroupID != "group-1" || roles[0].Role != "operator" {
		t.Errorf("role = {%s, %s}, want {group-1, operator}", roles[0].GroupID, roles[0].Role)
	}
}

func TestHandleCallback_MissingAssertion(t *testing.T) {
	p, _ := newTestProvider(t)
	r := httptest.NewRequest(http.MethodGet, "https://example.com/auth/callback", nil)
	_, err := p.HandleCallback(context.Background(), r)
	if err == nil {
		t.Fatal("expected error for missing assertion")
	}
}

func TestHandleCallback_ExpiredToken(t *testing.T) {
	p, _ := newTestProvider(t)
	claims := validClaims()
	claims.Exp = time.Now().Add(-10 * time.Minute).Unix()
	token := makeJWT(t, claims, testSharedSecret)

	r := callbackRequest(t, token)
	_, err := p.HandleCallback(context.Background(), r)
	if err == nil {
		t.Fatal("expected error for expired token")
	}
	if got := err.Error(); !strings.Contains(got,"expired") {
		t.Errorf("error = %q, want it to mention 'expired'", got)
	}
}

func TestHandleCallback_WrongAudience(t *testing.T) {
	p, _ := newTestProvider(t)
	claims := validClaims()
	claims.Aud = "wrong-instance-id"
	token := makeJWT(t, claims, testSharedSecret)

	r := callbackRequest(t, token)
	_, err := p.HandleCallback(context.Background(), r)
	if err == nil {
		t.Fatal("expected error for wrong audience")
	}
	if got := err.Error(); !strings.Contains(got,"audience") {
		t.Errorf("error = %q, want it to mention 'audience'", got)
	}
}

func TestHandleCallback_WrongSignature(t *testing.T) {
	p, _ := newTestProvider(t)
	claims := validClaims()
	token := makeJWT(t, claims, "wrong-secret")

	r := callbackRequest(t, token)
	_, err := p.HandleCallback(context.Background(), r)
	if err == nil {
		t.Fatal("expected error for wrong signature")
	}
	if got := err.Error(); !strings.Contains(got,"signature") {
		t.Errorf("error = %q, want it to mention 'signature'", got)
	}
}

func TestHandleCallback_MalformedToken(t *testing.T) {
	p, _ := newTestProvider(t)
	tests := []struct {
		name  string
		token string
	}{
		{"empty", ""},
		{"one part", "abc"},
		{"two parts", "abc.def"},
		{"four parts", "abc.def.ghi.jkl"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := callbackRequest(t, tt.token)
			_, err := p.HandleCallback(context.Background(), r)
			if err == nil {
				t.Fatal("expected error for malformed token")
			}
		})
	}
}

func TestHandleCallback_MissingSub(t *testing.T) {
	p, _ := newTestProvider(t)
	claims := validClaims()
	claims.Sub = ""
	token := makeJWT(t, claims, testSharedSecret)

	r := callbackRequest(t, token)
	_, err := p.HandleCallback(context.Background(), r)
	if err == nil {
		t.Fatal("expected error for missing sub")
	}
}

func TestHandleCallback_MissingUsername(t *testing.T) {
	p, _ := newTestProvider(t)
	claims := validClaims()
	claims.Username = ""
	token := makeJWT(t, claims, testSharedSecret)

	r := callbackRequest(t, token)
	_, err := p.HandleCallback(context.Background(), r)
	if err == nil {
		t.Fatal("expected error for missing username")
	}
}

// --- User upsert tests ---

func TestHandleCallback_ExistingUserUpdated(t *testing.T) {
	p, s := newTestProvider(t)
	ctx := context.Background()

	// Pre-create a user with the same ID that Sett will send.
	existing := types.User{
		ID:          "user-uuid-from-sett",
		Username:    "old-alice",
		DisplayName: "Old Name",
		IsAdmin:     false,
		IsActive:    true,
		CreatedAt:   time.Now().Add(-24 * time.Hour).UTC(),
	}
	if err := s.CreateUser(ctx, existing); err != nil {
		t.Fatalf("create existing user: %v", err)
	}

	// Callback with updated claims.
	claims := validClaims()
	claims.Username = "alice-updated"
	claims.DisplayName = "Alice Updated"
	claims.IsAdmin = true
	token := makeJWT(t, claims, testSharedSecret)

	r := callbackRequest(t, token)
	result, err := p.HandleCallback(ctx, r)
	if err != nil {
		t.Fatalf("HandleCallback() error: %v", err)
	}

	if result.UserID != claims.Sub {
		t.Errorf("UserID = %q, want %q", result.UserID, claims.Sub)
	}

	// Verify user was updated.
	user, err := s.GetUser(ctx, claims.Sub)
	if err != nil {
		t.Fatalf("GetUser() error: %v", err)
	}
	if user.Username != "alice-updated" {
		t.Errorf("Username = %q, want %q", user.Username, "alice-updated")
	}
	if user.DisplayName != "Alice Updated" {
		t.Errorf("DisplayName = %q, want %q", user.DisplayName, "Alice Updated")
	}
	if !user.IsAdmin {
		t.Error("IsAdmin should be true after update")
	}
	// Original created_at should be preserved.
	if user.CreatedAt.After(existing.CreatedAt.Add(time.Second)) {
		t.Error("CreatedAt should be preserved from original user")
	}
}

func TestHandleCallback_RolesReplaced(t *testing.T) {
	p, s := newTestProvider(t)
	ctx := context.Background()

	// First callback creates user with role in group-1.
	claims := validClaims()
	claims.Roles = []testRole{
		{GroupID: "group-1", Role: "operator"},
		{GroupID: "group-2", Role: "viewer"},
	}
	token := makeJWT(t, claims, testSharedSecret)
	r := callbackRequest(t, token)
	_, err := p.HandleCallback(ctx, r)
	if err != nil {
		t.Fatalf("first HandleCallback() error: %v", err)
	}

	// Verify initial roles.
	roles, err := s.ListUserGroupRoles(ctx, claims.Sub)
	if err != nil {
		t.Fatalf("ListUserGroupRoles() error: %v", err)
	}
	if len(roles) != 2 {
		t.Fatalf("expected 2 roles, got %d", len(roles))
	}

	// Second callback with different roles should replace them.
	claims.Roles = []testRole{
		{GroupID: "group-3", Role: "admin"},
	}
	token = makeJWT(t, claims, testSharedSecret)
	r = callbackRequest(t, token)
	_, err = p.HandleCallback(ctx, r)
	if err != nil {
		t.Fatalf("second HandleCallback() error: %v", err)
	}

	// Verify roles were replaced.
	roles, err = s.ListUserGroupRoles(ctx, claims.Sub)
	if err != nil {
		t.Fatalf("ListUserGroupRoles() error: %v", err)
	}
	if len(roles) != 1 {
		t.Fatalf("expected 1 role after replacement, got %d", len(roles))
	}
	if roles[0].GroupID != "group-3" || roles[0].Role != "admin" {
		t.Errorf("role = {%s, %s}, want {group-3, admin}", roles[0].GroupID, roles[0].Role)
	}
}

// --- Session management tests ---

func TestValidateSession_Valid(t *testing.T) {
	p, _ := newTestProvider(t)
	ctx := context.Background()

	// Create a user via callback.
	claims := validClaims()
	token := makeJWT(t, claims, testSharedSecret)
	r := callbackRequest(t, token)
	result, err := p.HandleCallback(ctx, r)
	if err != nil {
		t.Fatalf("HandleCallback() error: %v", err)
	}

	// Validate the session.
	user, err := p.ValidateSession(ctx, result.SessionID)
	if err != nil {
		t.Fatalf("ValidateSession() error: %v", err)
	}
	if user.ID != claims.Sub {
		t.Errorf("user.ID = %q, want %q", user.ID, claims.Sub)
	}
}

func TestValidateSession_EmptyID(t *testing.T) {
	p, _ := newTestProvider(t)
	_, err := p.ValidateSession(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty session ID")
	}
}

func TestValidateSession_NonexistentSession(t *testing.T) {
	p, _ := newTestProvider(t)
	_, err := p.ValidateSession(context.Background(), "does-not-exist")
	if err == nil {
		t.Fatal("expected error for nonexistent session")
	}
}

func TestValidateSession_InactiveUser(t *testing.T) {
	p, s := newTestProvider(t)
	ctx := context.Background()

	// Create user via callback.
	claims := validClaims()
	token := makeJWT(t, claims, testSharedSecret)
	r := callbackRequest(t, token)
	result, err := p.HandleCallback(ctx, r)
	if err != nil {
		t.Fatalf("HandleCallback() error: %v", err)
	}

	// Deactivate the user.
	user, _ := s.GetUser(ctx, claims.Sub)
	user.IsActive = false
	if err := s.UpdateUser(ctx, *user); err != nil {
		t.Fatalf("deactivate user: %v", err)
	}

	// Validate should fail.
	_, err = p.ValidateSession(ctx, result.SessionID)
	if err == nil {
		t.Fatal("expected error for inactive user")
	}
}

func TestLogout(t *testing.T) {
	p, _ := newTestProvider(t)
	ctx := context.Background()

	// Create user via callback.
	claims := validClaims()
	token := makeJWT(t, claims, testSharedSecret)
	r := callbackRequest(t, token)
	result, err := p.HandleCallback(ctx, r)
	if err != nil {
		t.Fatalf("HandleCallback() error: %v", err)
	}

	// Logout.
	if err := p.Logout(ctx, result.SessionID); err != nil {
		t.Fatalf("Logout() error: %v", err)
	}

	// Session should be invalid now.
	_, err = p.ValidateSession(ctx, result.SessionID)
	if err == nil {
		t.Fatal("expected error after logout")
	}
}

func TestLogout_EmptySessionID(t *testing.T) {
	p, _ := newTestProvider(t)
	// Should not error.
	if err := p.Logout(context.Background(), ""); err != nil {
		t.Fatalf("Logout('') error: %v", err)
	}
}

// --- DeleteUserSessions tests ---

func TestDeleteUserSessions(t *testing.T) {
	p, _ := newTestProvider(t)
	ctx := context.Background()

	claims := validClaims()

	// Create multiple sessions for the same user.
	for i := 0; i < 3; i++ {
		token := makeJWT(t, claims, testSharedSecret)
		r := callbackRequest(t, token)
		_, err := p.HandleCallback(ctx, r)
		if err != nil {
			t.Fatalf("HandleCallback() #%d error: %v", i, err)
		}
	}

	// Delete all sessions.
	n, err := p.DeleteUserSessions(ctx, claims.Sub)
	if err != nil {
		t.Fatalf("DeleteUserSessions() error: %v", err)
	}
	if n != 3 {
		t.Errorf("deleted %d sessions, want 3", n)
	}

	// Verify: creating and validating a new session still works (user still exists).
	token := makeJWT(t, claims, testSharedSecret)
	r := callbackRequest(t, token)
	result, err := p.HandleCallback(ctx, r)
	if err != nil {
		t.Fatalf("HandleCallback() after delete error: %v", err)
	}
	_, err = p.ValidateSession(ctx, result.SessionID)
	if err != nil {
		t.Fatalf("ValidateSession() after delete error: %v", err)
	}
}

// --- SessionTTL test ---

func TestSessionTTL(t *testing.T) {
	p, _ := newTestProvider(t)
	if got := p.SessionTTL(); got != 24*time.Hour {
		t.Errorf("SessionTTL() = %v, want %v", got, 24*time.Hour)
	}
}

func TestSessionTTL_Custom(t *testing.T) {
	tdb := testutil.RequirePostgres(t)

	p := mustNew(t, tdb.Store, delegated.Config{
		SettURL:      testSettURL,
		InstanceID:   testInstanceID,
		SharedSecret: testSharedSecret,
		CallbackURL:  testCallbackURL,
		SessionTTL:   4 * time.Hour,
	})
	if got := p.SessionTTL(); got != 4*time.Hour {
		t.Errorf("SessionTTL() = %v, want %v", got, 4*time.Hour)
	}
}

func TestSessionTTL_DefaultWhenZero(t *testing.T) {
	tdb := testutil.RequirePostgres(t)

	p := mustNew(t, tdb.Store, delegated.Config{
		SettURL:      testSettURL,
		InstanceID:   testInstanceID,
		SharedSecret: testSharedSecret,
		CallbackURL:  testCallbackURL,
	})
	if got := p.SessionTTL(); got != 24*time.Hour {
		t.Errorf("SessionTTL() = %v, want %v (default)", got, 24*time.Hour)
	}
}

// --- Capabilities test ---

func TestCapabilities(t *testing.T) {
	p, _ := newTestProvider(t)
	caps := p.Capabilities()

	if caps.CanManageUsers {
		t.Error("CanManageUsers should be false")
	}
	if caps.CanManagePasswords {
		t.Error("CanManagePasswords should be false")
	}
	if caps.CanChangePassword {
		t.Error("CanChangePassword should be false")
	}
	if caps.ManagedBy != testSettURL {
		t.Errorf("ManagedBy = %q, want %q", caps.ManagedBy, testSettURL)
	}
	if caps.LoginMode != "redirect" {
		t.Errorf("LoginMode = %q, want %q", caps.LoginMode, "redirect")
	}
}

// --- Role resolution tests ---

func TestResolveGlobalRole(t *testing.T) {
	p, _ := newTestProvider(t)
	ctx := context.Background()

	// Create user with multiple group roles.
	claims := validClaims()
	claims.Roles = []testRole{
		{GroupID: "group-1", Role: "viewer"},
		{GroupID: "group-2", Role: "admin"},
	}
	token := makeJWT(t, claims, testSharedSecret)
	r := callbackRequest(t, token)
	_, err := p.HandleCallback(ctx, r)
	if err != nil {
		t.Fatalf("HandleCallback() error: %v", err)
	}

	role, found, err := p.ResolveGlobalRole(ctx, claims.Sub)
	if err != nil {
		t.Fatalf("ResolveGlobalRole() error: %v", err)
	}
	if !found {
		t.Fatal("expected role to be found")
	}
	if role != "admin" {
		t.Errorf("role = %q, want %q", role, "admin")
	}
}

func TestResolveGlobalRole_NoRoles(t *testing.T) {
	p, _ := newTestProvider(t)
	_, found, err := p.ResolveGlobalRole(context.Background(), "nonexistent-user")
	if err != nil {
		t.Fatalf("ResolveGlobalRole() error: %v", err)
	}
	if found {
		t.Error("expected no role found for nonexistent user")
	}
}

func TestResolveAgentRole(t *testing.T) {
	p, s := newTestProvider(t)
	ctx := context.Background()

	// Create user with operator role in group-1.
	claims := validClaims()
	claims.Roles = []testRole{
		{GroupID: "group-1", Role: "operator"},
	}
	token := makeJWT(t, claims, testSharedSecret)
	r := callbackRequest(t, token)
	if _, err := p.HandleCallback(ctx, r); err != nil {
		t.Fatalf("HandleCallback() error: %v", err)
	}

	// Place agent-A into group-1.
	createStubAgent(t, s, "agent-A")
	if err := s.SetAgentGroupMember(ctx, "agent-A", "group-1"); err != nil {
		t.Fatalf("SetAgentGroupMember() error: %v", err)
	}

	role, found, err := p.ResolveAgentRole(ctx, claims.Sub, "agent-A")
	if err != nil {
		t.Fatalf("ResolveAgentRole() error: %v", err)
	}
	if !found {
		t.Fatal("expected role to be found")
	}
	if role != "operator" {
		t.Errorf("role = %q, want %q", role, "operator")
	}
}

func TestResolveAgentRole_NoOverlap(t *testing.T) {
	p, s := newTestProvider(t)
	ctx := context.Background()

	// User has role in group-1 only.
	claims := validClaims()
	claims.Roles = []testRole{
		{GroupID: "group-1", Role: "operator"},
	}
	token := makeJWT(t, claims, testSharedSecret)
	r := callbackRequest(t, token)
	if _, err := p.HandleCallback(ctx, r); err != nil {
		t.Fatalf("HandleCallback() error: %v", err)
	}

	// Agent-B is in group-2, which the user has no role in.
	createStubAgent(t, s, "agent-B")
	if err := s.SetAgentGroupMember(ctx, "agent-B", "group-2"); err != nil {
		t.Fatalf("SetAgentGroupMember() error: %v", err)
	}

	_, found, err := p.ResolveAgentRole(ctx, claims.Sub, "agent-B")
	if err != nil {
		t.Fatalf("ResolveAgentRole() error: %v", err)
	}
	if found {
		t.Error("expected no role found when user's groups don't overlap with agent's groups")
	}
}

func TestResolveAgentRole_MultipleGroups(t *testing.T) {
	p, s := newTestProvider(t)
	ctx := context.Background()

	// User has viewer in group-1 and admin in group-2.
	claims := validClaims()
	claims.Roles = []testRole{
		{GroupID: "group-1", Role: "viewer"},
		{GroupID: "group-2", Role: "admin"},
	}
	token := makeJWT(t, claims, testSharedSecret)
	r := callbackRequest(t, token)
	if _, err := p.HandleCallback(ctx, r); err != nil {
		t.Fatalf("HandleCallback() error: %v", err)
	}

	// Agent-C is in both group-1 and group-2.
	createStubAgent(t, s, "agent-C")
	if err := s.SetAgentGroupMember(ctx, "agent-C", "group-1"); err != nil {
		t.Fatalf("SetAgentGroupMember(group-1) error: %v", err)
	}
	if err := s.SetAgentGroupMember(ctx, "agent-C", "group-2"); err != nil {
		t.Fatalf("SetAgentGroupMember(group-2) error: %v", err)
	}

	role, found, err := p.ResolveAgentRole(ctx, claims.Sub, "agent-C")
	if err != nil {
		t.Fatalf("ResolveAgentRole() error: %v", err)
	}
	if !found {
		t.Fatal("expected role to be found")
	}
	// Should return the highest role across overlapping groups.
	if role != "admin" {
		t.Errorf("role = %q, want %q (highest of viewer + admin)", role, "admin")
	}
}

func TestListVisibleAgentIDs(t *testing.T) {
	p, s := newTestProvider(t)
	ctx := context.Background()

	// User has roles in group-1 and group-2.
	claims := validClaims()
	claims.Roles = []testRole{
		{GroupID: "group-1", Role: "viewer"},
		{GroupID: "group-2", Role: "operator"},
	}
	token := makeJWT(t, claims, testSharedSecret)
	r := callbackRequest(t, token)
	if _, err := p.HandleCallback(ctx, r); err != nil {
		t.Fatalf("HandleCallback() error: %v", err)
	}

	// Place agents into groups.
	for _, aid := range []string{"agent-X", "agent-Y", "agent-Z"} {
		createStubAgent(t, s, aid)
	}
	if err := s.SetAgentGroupMember(ctx, "agent-X", "group-1"); err != nil {
		t.Fatalf("SetAgentGroupMember() error: %v", err)
	}
	if err := s.SetAgentGroupMember(ctx, "agent-Y", "group-2"); err != nil {
		t.Fatalf("SetAgentGroupMember() error: %v", err)
	}
	// agent-Z is in group-3 which the user has no role in.
	if err := s.SetAgentGroupMember(ctx, "agent-Z", "group-3"); err != nil {
		t.Fatalf("SetAgentGroupMember() error: %v", err)
	}

	visible, err := p.ListVisibleAgentIDs(ctx, claims.Sub)
	if err != nil {
		t.Fatalf("ListVisibleAgentIDs() error: %v", err)
	}

	if _, ok := visible["agent-X"]; !ok {
		t.Error("agent-X should be visible (in group-1)")
	}
	if _, ok := visible["agent-Y"]; !ok {
		t.Error("agent-Y should be visible (in group-2)")
	}
	if _, ok := visible["agent-Z"]; ok {
		t.Error("agent-Z should NOT be visible (in group-3, user has no role there)")
	}
	if len(visible) != 2 {
		t.Errorf("expected 2 visible agents, got %d", len(visible))
	}
}

func TestListVisibleAgentIDs_NoGroups(t *testing.T) {
	p, _ := newTestProvider(t)
	ctx := context.Background()

	// Create user with no group roles.
	claims := validClaims()
	claims.Roles = nil
	token := makeJWT(t, claims, testSharedSecret)
	r := callbackRequest(t, token)
	if _, err := p.HandleCallback(ctx, r); err != nil {
		t.Fatalf("HandleCallback() error: %v", err)
	}

	visible, err := p.ListVisibleAgentIDs(ctx, claims.Sub)
	if err != nil {
		t.Fatalf("ListVisibleAgentIDs() error: %v", err)
	}
	if len(visible) != 0 {
		t.Errorf("expected 0 visible agents, got %d", len(visible))
	}
}

// --- HandleCallback with empty display name ---

func TestHandleCallback_EmptyDisplayNameUsesUsername(t *testing.T) {
	p, s := newTestProvider(t)
	ctx := context.Background()

	claims := validClaims()
	claims.DisplayName = ""
	token := makeJWT(t, claims, testSharedSecret)

	r := callbackRequest(t, token)
	_, err := p.HandleCallback(ctx, r)
	if err != nil {
		t.Fatalf("HandleCallback() error: %v", err)
	}

	user, err := s.GetUser(ctx, claims.Sub)
	if err != nil {
		t.Fatalf("GetUser() error: %v", err)
	}
	if user.DisplayName != claims.Username {
		t.Errorf("DisplayName = %q, want %q (username fallback)", user.DisplayName, claims.Username)
	}
}

// --- HandleCallback with unknown roles ---

func TestHandleCallback_SkipsInvalidRoles(t *testing.T) {
	p, s := newTestProvider(t)
	ctx := context.Background()

	claims := validClaims()
	claims.Roles = []testRole{
		{GroupID: "group-1", Role: "operator"},
		{GroupID: "group-2", Role: "invalid-role-name"},
	}
	token := makeJWT(t, claims, testSharedSecret)

	r := callbackRequest(t, token)
	_, err := p.HandleCallback(ctx, r)
	if err != nil {
		t.Fatalf("HandleCallback() error: %v", err)
	}

	roles, err := s.ListUserGroupRoles(ctx, claims.Sub)
	if err != nil {
		t.Fatalf("ListUserGroupRoles() error: %v", err)
	}
	// Only the valid role should be persisted.
	if len(roles) != 1 {
		t.Fatalf("expected 1 valid role, got %d", len(roles))
	}
	if roles[0].Role != "operator" {
		t.Errorf("role = %q, want %q", roles[0].Role, "operator")
	}
}

// --- HandleCallback with unsupported JWT algorithm ---

func TestHandleCallback_UnsupportedAlgorithm(t *testing.T) {
	p, _ := newTestProvider(t)

	// Manually build a JWT with RS256 header but HMAC signature.
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	claims := validClaims()
	headerJSON, _ := json.Marshal(header)
	claimsJSON, _ := json.Marshal(claims)
	headerB64 := base64.RawURLEncoding.EncodeToString(headerJSON)
	claimsB64 := base64.RawURLEncoding.EncodeToString(claimsJSON)
	signingInput := headerB64 + "." + claimsB64
	mac := hmac.New(sha256.New, []byte(testSharedSecret))
	mac.Write([]byte(signingInput))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	token := signingInput + "." + sig

	r := callbackRequest(t, token)
	_, err := p.HandleCallback(context.Background(), r)
	if err == nil {
		t.Fatal("expected error for unsupported algorithm")
	}
	if got := err.Error(); !strings.Contains(got,"unsupported algorithm") {
		t.Errorf("error = %q, want mention of 'unsupported algorithm'", got)
	}
}

