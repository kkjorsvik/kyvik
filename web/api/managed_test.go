package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/kkjorsvik/kyvik/internal/store/postgres"
	"github.com/kkjorsvik/kyvik/internal/testutil"
	"github.com/kkjorsvik/kyvik/pkg/types"
	webapi "github.com/kkjorsvik/kyvik/web/api"
)

const testSharedSecret = "test-shared-secret-for-managed-api"
const testVersion = "2026.03.02-test"

// managedTestEnv holds the test environment for managed API tests.
type managedTestEnv struct {
	api    *webapi.ManagedAPI
	store  *postgres.PostgresStore
	server *httptest.Server
}

func newManagedTestEnv(t *testing.T) *managedTestEnv {
	t.Helper()
	tdb := testutil.RequirePostgres(t)
	s := tdb.Store

	// Pre-create agent groups referenced by tests (FK constraint).
	ctx := context.Background()
	for _, gid := range []string{"g1", "g2", "g3", "g4", "old-group", "new-group"} {
		if err := s.CreateAgentGroup(ctx, types.AgentGroup{
			ID:   gid,
			Name: gid,
		}); err != nil {
			t.Fatalf("create agent group %s: %v", gid, err)
		}
	}

	api := webapi.NewManagedAPI(s, testSharedSecret, testVersion)
	handler := http.StripPrefix("/api/managed", api.Routes())
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	return &managedTestEnv{
		api:    api,
		store:  s,
		server: server,
	}
}

func (e *managedTestEnv) doRequest(t *testing.T, method, path, token string, body any) *http.Response {
	t.Helper()
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, e.server.URL+"/api/managed"+path, bodyReader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	return resp
}

func readJSON(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return result
}

// --------------- PUT /users/{id} tests ---------------

func TestManagedUpsertUser_CreateNew(t *testing.T) {
	env := newManagedTestEnv(t)
	userID := uuid.NewString()

	body := map[string]any{
		"username":     "jane",
		"display_name": "Jane Doe",
		"is_admin":     false,
		"is_active":    true,
		"roles": []map[string]string{
			{"group_id": "g1", "role": "operator"},
			{"group_id": "g2", "role": "viewer"},
		},
	}

	resp := env.doRequest(t, "PUT", "/users/"+userID, testSharedSecret, body)
	result := readJSON(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if result["ok"] != true {
		t.Errorf("expected ok=true, got %v", result["ok"])
	}

	// Verify user was created in the store.
	user, err := env.store.GetUser(context.Background(), userID)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if user.Username != "jane" {
		t.Errorf("expected username 'jane', got %q", user.Username)
	}
	if user.DisplayName != "Jane Doe" {
		t.Errorf("expected display name 'Jane Doe', got %q", user.DisplayName)
	}
	if user.IsAdmin {
		t.Error("expected is_admin=false")
	}
	if !user.IsActive {
		t.Error("expected is_active=true")
	}

	// Verify roles were set.
	roles, err := env.store.ListUserGroupRoles(context.Background(), userID)
	if err != nil {
		t.Fatalf("list roles: %v", err)
	}
	if len(roles) != 2 {
		t.Fatalf("expected 2 roles, got %d", len(roles))
	}
	roleMap := make(map[string]string)
	for _, r := range roles {
		roleMap[r.GroupID] = r.Role
	}
	if roleMap["g1"] != "operator" {
		t.Errorf("expected g1=operator, got %q", roleMap["g1"])
	}
	if roleMap["g2"] != "viewer" {
		t.Errorf("expected g2=viewer, got %q", roleMap["g2"])
	}
}

func TestManagedUpsertUser_UpdateExisting(t *testing.T) {
	env := newManagedTestEnv(t)
	userID := uuid.NewString()
	ctx := context.Background()

	// Pre-create user.
	err := env.store.CreateUser(ctx, types.User{
		ID:          userID,
		Username:    "old-jane",
		DisplayName: "Old Jane",
		IsAdmin:     false,
		IsActive:    true,
		CreatedAt:   time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Set an initial role.
	err = env.store.SetUserGroupRole(ctx, types.UserGroupRole{
		UserID:  userID,
		GroupID: "old-group",
		Role:    "viewer",
	})
	if err != nil {
		t.Fatalf("set role: %v", err)
	}

	// Update via managed API.
	body := map[string]any{
		"username":     "new-jane",
		"display_name": "New Jane",
		"is_admin":     true,
		"is_active":    true,
		"roles": []map[string]string{
			{"group_id": "new-group", "role": "manager"},
		},
	}

	resp := env.doRequest(t, "PUT", "/users/"+userID, testSharedSecret, body)
	result := readJSON(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if result["ok"] != true {
		t.Errorf("expected ok=true, got %v", result["ok"])
	}

	// Verify updated fields.
	user, err := env.store.GetUser(ctx, userID)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if user.Username != "new-jane" {
		t.Errorf("expected username 'new-jane', got %q", user.Username)
	}
	if user.DisplayName != "New Jane" {
		t.Errorf("expected display name 'New Jane', got %q", user.DisplayName)
	}
	if !user.IsAdmin {
		t.Error("expected is_admin=true after update")
	}

	// Verify old role was removed and new role was set.
	roles, err := env.store.ListUserGroupRoles(ctx, userID)
	if err != nil {
		t.Fatalf("list roles: %v", err)
	}
	if len(roles) != 1 {
		t.Fatalf("expected 1 role, got %d", len(roles))
	}
	if roles[0].GroupID != "new-group" || roles[0].Role != "manager" {
		t.Errorf("expected new-group/manager, got %s/%s", roles[0].GroupID, roles[0].Role)
	}
}

func TestManagedUpsertUser_SyncRoles(t *testing.T) {
	env := newManagedTestEnv(t)
	userID := uuid.NewString()
	ctx := context.Background()

	// Create user with some roles.
	err := env.store.CreateUser(ctx, types.User{
		ID:        userID,
		Username:  "jane",
		IsActive:  true,
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	for _, g := range []string{"g1", "g2", "g3"} {
		err = env.store.SetUserGroupRole(ctx, types.UserGroupRole{
			UserID:  userID,
			GroupID: g,
			Role:    "viewer",
		})
		if err != nil {
			t.Fatalf("set role: %v", err)
		}
	}

	// Sync with only g4, clearing g1-g3.
	body := map[string]any{
		"username":  "jane",
		"is_active": true,
		"roles": []map[string]string{
			{"group_id": "g4", "role": "admin"},
		},
	}

	resp := env.doRequest(t, "PUT", "/users/"+userID, testSharedSecret, body)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	roles, err := env.store.ListUserGroupRoles(ctx, userID)
	if err != nil {
		t.Fatalf("list roles: %v", err)
	}
	if len(roles) != 1 {
		t.Fatalf("expected 1 role after sync, got %d", len(roles))
	}
	if roles[0].GroupID != "g4" || roles[0].Role != "admin" {
		t.Errorf("expected g4/admin, got %s/%s", roles[0].GroupID, roles[0].Role)
	}
}

func TestManagedUpsertUser_InvalidBody(t *testing.T) {
	env := newManagedTestEnv(t)

	// Send malformed JSON.
	req, _ := http.NewRequest("PUT", env.server.URL+"/api/managed/users/"+uuid.NewString(), bytes.NewReader([]byte("not json")))
	req.Header.Set("Authorization", "Bearer "+testSharedSecret)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid body, got %d", resp.StatusCode)
	}
}

func TestManagedUpsertUser_MissingUsername(t *testing.T) {
	env := newManagedTestEnv(t)

	body := map[string]any{
		"username":  "",
		"is_active": true,
	}

	resp := env.doRequest(t, "PUT", "/users/"+uuid.NewString(), testSharedSecret, body)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for missing username, got %d", resp.StatusCode)
	}
}

func TestManagedUpsertUser_InvalidRole(t *testing.T) {
	env := newManagedTestEnv(t)

	body := map[string]any{
		"username":  "jane",
		"is_active": true,
		"roles": []map[string]string{
			{"group_id": "g1", "role": "superadmin"},
		},
	}

	resp := env.doRequest(t, "PUT", "/users/"+uuid.NewString(), testSharedSecret, body)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid role, got %d", resp.StatusCode)
	}
}

func TestManagedUpsertUser_MissingAuth(t *testing.T) {
	env := newManagedTestEnv(t)

	body := map[string]any{
		"username":  "jane",
		"is_active": true,
	}

	resp := env.doRequest(t, "PUT", "/users/"+uuid.NewString(), "", body)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for missing auth, got %d", resp.StatusCode)
	}
}

func TestManagedUpsertUser_WrongAuth(t *testing.T) {
	env := newManagedTestEnv(t)

	body := map[string]any{
		"username":  "jane",
		"is_active": true,
	}

	resp := env.doRequest(t, "PUT", "/users/"+uuid.NewString(), "wrong-secret", body)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for wrong secret, got %d", resp.StatusCode)
	}
}

func TestManagedUpsertUser_DeactivateUser(t *testing.T) {
	env := newManagedTestEnv(t)
	userID := uuid.NewString()
	ctx := context.Background()

	// Create active user.
	err := env.store.CreateUser(ctx, types.User{
		ID:        userID,
		Username:  "jane",
		IsActive:  true,
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Deactivate via managed API.
	body := map[string]any{
		"username":  "jane",
		"is_active": false,
	}

	resp := env.doRequest(t, "PUT", "/users/"+userID, testSharedSecret, body)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	user, err := env.store.GetUser(ctx, userID)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if user.IsActive {
		t.Error("expected is_active=false after deactivation")
	}
}

// --------------- DELETE /users/{id}/sessions tests ---------------

func TestManagedRevokeSessions_Success(t *testing.T) {
	env := newManagedTestEnv(t)
	userID := uuid.NewString()
	ctx := context.Background()

	// Create user and sessions.
	err := env.store.CreateUser(ctx, types.User{
		ID:        userID,
		Username:  "jane",
		IsActive:  true,
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		err = env.store.CreateSession(ctx, types.UserSession{
			ID:        uuid.NewString(),
			UserID:    userID,
			CreatedAt: now,
			ExpiresAt: now.Add(24 * time.Hour),
		})
		if err != nil {
			t.Fatalf("create session: %v", err)
		}
	}

	resp := env.doRequest(t, "DELETE", "/users/"+userID+"/sessions", testSharedSecret, nil)
	result := readJSON(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if result["ok"] != true {
		t.Errorf("expected ok=true, got %v", result["ok"])
	}
	// The deleted count should be 3.
	if deleted, ok := result["deleted"].(float64); !ok || int(deleted) != 3 {
		t.Errorf("expected deleted=3, got %v", result["deleted"])
	}
}

func TestManagedRevokeSessions_NoSessions(t *testing.T) {
	env := newManagedTestEnv(t)
	userID := uuid.NewString()

	// No user or sessions exist, but the endpoint should still return 200 with deleted=0.
	resp := env.doRequest(t, "DELETE", "/users/"+userID+"/sessions", testSharedSecret, nil)
	result := readJSON(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if result["ok"] != true {
		t.Errorf("expected ok=true, got %v", result["ok"])
	}
	if deleted, ok := result["deleted"].(float64); !ok || int(deleted) != 0 {
		t.Errorf("expected deleted=0, got %v", result["deleted"])
	}
}

func TestManagedRevokeSessions_MissingAuth(t *testing.T) {
	env := newManagedTestEnv(t)

	resp := env.doRequest(t, "DELETE", "/users/"+uuid.NewString()+"/sessions", "", nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for missing auth, got %d", resp.StatusCode)
	}
}

func TestManagedRevokeSessions_WrongAuth(t *testing.T) {
	env := newManagedTestEnv(t)

	resp := env.doRequest(t, "DELETE", "/users/"+uuid.NewString()+"/sessions", "wrong-secret", nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for wrong secret, got %d", resp.StatusCode)
	}
}

// --------------- GET /health tests ---------------

func TestManagedHealth(t *testing.T) {
	env := newManagedTestEnv(t)

	resp := env.doRequest(t, "GET", "/health", "", nil)
	result := readJSON(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if result["status"] != "ok" {
		t.Errorf("expected status=ok, got %v", result["status"])
	}
	if result["version"] != testVersion {
		t.Errorf("expected version=%q, got %v", testVersion, result["version"])
	}
}

func TestManagedHealth_NoAuth(t *testing.T) {
	env := newManagedTestEnv(t)

	// Health should be accessible without any auth token.
	resp := env.doRequest(t, "GET", "/health", "", nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 without auth, got %d", resp.StatusCode)
	}
}

func TestManagedHealth_EmptyVersion(t *testing.T) {
	tdb := testutil.RequirePostgres(t)

	api := webapi.NewManagedAPI(tdb.Store, testSharedSecret, "")
	handler := http.StripPrefix("/api/managed", api.Routes())
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	req, _ := http.NewRequest("GET", server.URL+"/api/managed/health", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)

	if result["status"] != "ok" {
		t.Errorf("expected status=ok, got %v", result["status"])
	}
	if _, hasVersion := result["version"]; hasVersion {
		t.Error("expected no version field when version is empty")
	}
}

func TestManagedHealth_RateLimited(t *testing.T) {
	tdb := testutil.RequirePostgres(t)

	api := webapi.NewManagedAPI(tdb.Store, testSharedSecret, testVersion)
	mux := api.Routes()

	// The default limiter is 60 req/s with burst of 10.
	// Exhaust the burst with rapid requests using httptest recorders.
	var lastStatus int
	for i := 0; i < 15; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/health", nil)
		mux.ServeHTTP(rec, req)
		lastStatus = rec.Code
	}

	if lastStatus != http.StatusTooManyRequests {
		t.Errorf("expected 429 after exceeding rate limit, got %d", lastStatus)
	}
}

// --------------- Shared secret middleware tests ---------------

func TestSharedSecretMiddleware_ValidToken(t *testing.T) {
	tdb := testutil.RequirePostgres(t)

	api := webapi.NewManagedAPI(tdb.Store, "my-secret", "v1")
	handler := api.RequireSharedSecret(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer my-secret")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for valid token, got %d", rec.Code)
	}
}

func TestSharedSecretMiddleware_MissingToken(t *testing.T) {
	tdb := testutil.RequirePostgres(t)

	api := webapi.NewManagedAPI(tdb.Store, "my-secret", "v1")
	handler := api.RequireSharedSecret(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for missing token, got %d", rec.Code)
	}
}

func TestSharedSecretMiddleware_WrongToken(t *testing.T) {
	tdb := testutil.RequirePostgres(t)

	api := webapi.NewManagedAPI(tdb.Store, "my-secret", "v1")
	handler := api.RequireSharedSecret(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer wrong-secret")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for wrong token, got %d", rec.Code)
	}
}

func TestSharedSecretMiddleware_InvalidFormat(t *testing.T) {
	tdb := testutil.RequirePostgres(t)

	api := webapi.NewManagedAPI(tdb.Store, "my-secret", "v1")
	handler := api.RequireSharedSecret(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz") // not Bearer
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for non-Bearer auth, got %d", rec.Code)
	}
}
