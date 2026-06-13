package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/kkjorsvik/kyvik/internal/auth"
	"github.com/kkjorsvik/kyvik/internal/authprovider/local"
	"github.com/kkjorsvik/kyvik/internal/store/postgres"
	"github.com/kkjorsvik/kyvik/internal/testutil"
	"github.com/kkjorsvik/kyvik/internal/users"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

func newTestHandlers() *Handlers {
	return &Handlers{
		sessionKey: generateKey(),
	}
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

// newTestSession creates a users.Service with a test store, creates an admin user,
// authenticates, and returns the handlers with auth provider set and the session ID.
func newTestSession(t *testing.T) (*Handlers, string) {
	t.Helper()
	ctx := context.Background()
	tdb := testutil.RequirePostgres(t)
	s := tdb.Store

	us := users.New(s, users.AuthConfig{SessionTTL: time.Hour, MaxSessionsPerUser: 3})
	created, _, err := us.BootstrapAdminIfEmpty(ctx, "admin", "secret-secret")
	if err != nil {
		t.Fatalf("BootstrapAdminIfEmpty: %v", err)
	}
	if !created {
		t.Fatal("expected admin to be created")
	}

	lr, err := us.Authenticate(ctx, "admin", "secret-secret", "127.0.0.1", "ua")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	h := newTestHandlers()
	h.SetAuthProvider(local.New(us))
	return h, lr.SessionID
}

func TestRequestIDInContext(t *testing.T) {
	var capturedID string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedID = requestIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	handler := RequestID(next)
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if capturedID == "" {
		t.Fatal("expected request ID in context, got empty string")
	}
	if len(capturedID) != 16 {
		t.Errorf("expected 16-char hex ID, got %q (len %d)", capturedID, len(capturedID))
	}
}

func TestRequestIDUnique(t *testing.T) {
	ids := make([]string, 0, 2)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ids = append(ids, requestIDFromContext(r.Context()))
		w.WriteHeader(http.StatusOK)
	})

	handler := RequestID(next)
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("GET", "/", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}

	if len(ids) != 2 {
		t.Fatalf("expected 2 IDs, got %d", len(ids))
	}
	if ids[0] == ids[1] {
		t.Errorf("expected unique IDs per request, got same ID %q for both", ids[0])
	}
}

func TestServerErrorDoesNotLeakDetails(t *testing.T) {
	h := newTestHandlers()

	req := httptest.NewRequest("GET", "/test", nil)
	rec := httptest.NewRecorder()

	sensitiveErr := errors.New("database connection string: postgres://user:password@host/db")
	h.serverError(rec, req, "internal operation failed", sensitiveErr)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "postgres://") {
		t.Errorf("response body leaks sensitive error detail: %q", body)
	}
	if strings.Contains(body, "password") {
		t.Errorf("response body leaks password in error detail: %q", body)
	}
	if !strings.Contains(body, "Internal server error") {
		t.Errorf("expected generic error message, got: %q", body)
	}
}

func TestRequireAuthRedirect(t *testing.T) {
	h := newTestHandlers()

	protected := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("protected"))
	})

	handler := h.RequireAuth(protected)

	// No cookie — should redirect
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("expected 303 redirect, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/login" {
		t.Errorf("expected redirect to /login, got %q", loc)
	}
}

// TestRequireAuthHTMXRedirect moved to auth_test.go

func TestRequireAuthWithValidCookie(t *testing.T) {
	h, sessionID := newTestSession(t)

	called := false
	protected := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	handler := h.RequireAuth(protected)

	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{
		Name:  cookieName,
		Value: sessionID,
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !called {
		t.Error("protected handler was not called with valid cookie")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestRequireAuthRejectsCrossSiteStateChange(t *testing.T) {
	h, sessionID := newTestSession(t)

	called := false
	protected := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	handler := h.RequireAuth(protected)

	req := httptest.NewRequest("POST", "/agents", nil)
	req.Host = "kyvik.local"
	req.Header.Set("Origin", "https://evil.example")
	req.AddCookie(&http.Cookie{
		Name:  cookieName,
		Value: sessionID,
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
	if called {
		t.Fatal("expected protected handler not to be called")
	}
}

func TestRequireAuthAllowsSameOriginStateChange(t *testing.T) {
	h, sessionID := newTestSession(t)

	called := false
	protected := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	handler := h.RequireAuth(protected)

	req := httptest.NewRequest("POST", "/agents", nil)
	req.Host = "kyvik.local"
	req.Header.Set("Origin", "https://kyvik.local")
	req.AddCookie(&http.Cookie{
		Name:  cookieName,
		Value: sessionID,
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !called {
		t.Fatal("expected protected handler to be called")
	}
}

func TestRequirePermissionViewerDenied(t *testing.T) {
	ctx := context.Background()
	tdb := testutil.RequirePostgres(t)
	s := tdb.Store

	us := users.New(s, users.AuthConfig{SessionTTL: time.Hour, MaxSessionsPerUser: 3})
	hash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	if err := s.CreateUser(ctx, types.User{
		ID:           "u-viewer",
		Username:     "viewer",
		PasswordHash: string(hash),
		DisplayName:  "Viewer",
		IsAdmin:      false,
		IsActive:     true,
		CreatedAt:    time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	lr, err := us.Authenticate(ctx, "viewer", "secret", "127.0.0.1", "ua")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	h := newTestHandlers()
	h.SetAuthProvider(local.New(us))

	protected := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := h.RequireAuth(h.RequirePermission(auth.PermSecretsManage, protected))

	req := httptest.NewRequest("GET", "/secrets", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: lr.SessionID})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestRequirePermissionAdminAllowed(t *testing.T) {
	ctx := context.Background()
	tdb := testutil.RequirePostgres(t)
	s := tdb.Store

	us := users.New(s, users.AuthConfig{SessionTTL: time.Hour, MaxSessionsPerUser: 3})
	created, _, err := us.BootstrapAdminIfEmpty(ctx, "admin", "secret")
	if err != nil {
		t.Fatalf("BootstrapAdminIfEmpty: %v", err)
	}
	if !created {
		t.Fatal("expected bootstrap admin creation")
	}

	lr, err := us.Authenticate(ctx, "admin", "secret", "127.0.0.1", "ua")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	h := newTestHandlers()
	h.SetAuthProvider(local.New(us))

	called := false
	protected := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	handler := h.RequireAuth(h.RequirePermission(auth.PermSecretsManage, protected))

	req := httptest.NewRequest("GET", "/secrets", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: lr.SessionID})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !called {
		t.Fatal("expected protected handler to be called")
	}
}

func TestRequireAgentPermissionScopedRoleAllowed(t *testing.T) {
	ctx := context.Background()
	tdb := testutil.RequirePostgres(t)
	s := tdb.Store

	us := users.New(s, users.AuthConfig{SessionTTL: time.Hour, MaxSessionsPerUser: 3})
	hash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	if err := s.CreateUser(ctx, types.User{
		ID:           "u-op",
		Username:     "operator",
		PasswordHash: string(hash),
		DisplayName:  "Operator",
		IsAdmin:      false,
		IsActive:     true,
		CreatedAt:    time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := s.CreateAgentGroup(ctx, types.AgentGroup{ID: "g-1", Name: "group-1", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("CreateAgentGroup: %v", err)
	}
	createScopedTestAgent(t, s, "agent-1")
	if err := s.SetUserGroupRole(ctx, types.UserGroupRole{UserID: "u-op", GroupID: "g-1", Role: auth.RoleOperator}); err != nil {
		t.Fatalf("SetUserGroupRole: %v", err)
	}
	if err := s.SetAgentGroupMember(ctx, "agent-1", "g-1"); err != nil {
		t.Fatalf("SetAgentGroupMember: %v", err)
	}

	lr, err := us.Authenticate(ctx, "operator", "secret", "127.0.0.1", "ua")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	h := newTestHandlers()
	h.SetAuthProvider(local.New(us))
	called := false
	protected := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	handler := h.RequireAuth(h.RequireAgentPermission(auth.PermAgentStart, protected))

	req := httptest.NewRequest("POST", "/agents/agent-1/start", nil)
	req.SetPathValue("id", "agent-1")
	req.AddCookie(&http.Cookie{Name: cookieName, Value: lr.SessionID})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !called {
		t.Fatal("expected handler call")
	}
}

func TestRequireAgentPermissionScopedRoleDenied(t *testing.T) {
	ctx := context.Background()
	tdb := testutil.RequirePostgres(t)
	s := tdb.Store

	us := users.New(s, users.AuthConfig{SessionTTL: time.Hour, MaxSessionsPerUser: 3})
	hash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	if err := s.CreateUser(ctx, types.User{
		ID:           "u-view",
		Username:     "viewer2",
		PasswordHash: string(hash),
		DisplayName:  "Viewer Two",
		IsAdmin:      false,
		IsActive:     true,
		CreatedAt:    time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := s.CreateAgentGroup(ctx, types.AgentGroup{ID: "g-1", Name: "group-1", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("CreateAgentGroup: %v", err)
	}
	createScopedTestAgent(t, s, "agent-1")
	if err := s.SetUserGroupRole(ctx, types.UserGroupRole{UserID: "u-view", GroupID: "g-1", Role: auth.RoleViewer}); err != nil {
		t.Fatalf("SetUserGroupRole: %v", err)
	}
	if err := s.SetAgentGroupMember(ctx, "agent-1", "g-1"); err != nil {
		t.Fatalf("SetAgentGroupMember: %v", err)
	}

	lr, err := us.Authenticate(ctx, "viewer2", "secret", "127.0.0.1", "ua")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	h := newTestHandlers()
	h.SetAuthProvider(local.New(us))
	protected := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := h.RequireAuth(h.RequireAgentPermission(auth.PermAgentStart, protected))

	req := httptest.NewRequest("POST", "/agents/agent-1/start", nil)
	req.SetPathValue("id", "agent-1")
	req.AddCookie(&http.Cookie{Name: cookieName, Value: lr.SessionID})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}
