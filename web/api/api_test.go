package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/apikeys"
	"github.com/kkjorsvik/kyvik/internal/testutil"
	webapi "github.com/kkjorsvik/kyvik/web/api"
)

// testEnv holds the test environment for API integration tests.
type testEnv struct {
	server  *httptest.Server
	apiKeys *apikeys.Service
	api     *webapi.API
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	tdb := testutil.RequirePostgres(t)

	keySvc := apikeys.New(tdb.Store)

	// Create a minimal API (kyvik=nil — tests that need kyvik will skip).
	api := webapi.New(nil, keySvc, webapi.RateLimits{
		Viewer:   5, // Low limits for rate limit testing.
		Operator: 10,
		Manager:  10,
		Admin:    20,
	})
	t.Cleanup(func() { api.Stop() })

	server := newHTTPServer(t, api.Routes())
	t.Cleanup(server.Close)

	return &testEnv{
		server:  server,
		apiKeys: keySvc,
		api:     api,
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

func (e *testEnv) createKey(t *testing.T, name, scope string) (string, string) {
	t.Helper()
	result, err := e.apiKeys.Create(t.Context(), name, scope, nil, nil)
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	return result.Key.ID, result.PlainKey
}

func (e *testEnv) doRequest(t *testing.T, method, path, token string, body any) *http.Response {
	t.Helper()
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, e.server.URL+path, bodyReader)
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

func TestAuth_MissingKey(t *testing.T) {
	env := newTestEnv(t)
	resp := env.doRequest(t, "GET", "/status", "", nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
	assertErrorFormat(t, resp)
}

func TestAuth_InvalidKey(t *testing.T) {
	env := newTestEnv(t)
	resp := env.doRequest(t, "GET", "/status", "kv_invalidinvalidinvalidinvalidinvalidinvalidinvalidinvalidinvalid00", nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

func TestAuth_ValidKey(t *testing.T) {
	env := newTestEnv(t)
	_, key := env.createKey(t, "test", "viewer")

	resp := env.doRequest(t, "GET", "/status", key, nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestAuth_ExpiredKey(t *testing.T) {
	env := newTestEnv(t)

	past := time.Now().Add(-time.Hour)
	result, err := env.apiKeys.Create(t.Context(), "expired", "viewer", nil, &past)
	if err != nil {
		t.Fatalf("create expired key: %v", err)
	}

	resp := env.doRequest(t, "GET", "/status", result.PlainKey, nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for expired key, got %d", resp.StatusCode)
	}
}

func TestScope_ViewerCannotManageKeys(t *testing.T) {
	env := newTestEnv(t)
	_, key := env.createKey(t, "viewer-key", "viewer")

	resp := env.doRequest(t, "GET", "/keys", key, nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

func TestScope_AdminCanManageKeys(t *testing.T) {
	env := newTestEnv(t)
	_, key := env.createKey(t, "admin-key", "admin")

	resp := env.doRequest(t, "GET", "/keys", key, nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestKeys_CreateAndList(t *testing.T) {
	env := newTestEnv(t)
	_, adminKey := env.createKey(t, "admin-key", "admin")

	// Create a key via API.
	body := map[string]any{
		"name":  "ci-key",
		"scope": "operator",
	}
	resp := env.doRequest(t, "POST", "/keys", adminKey, body)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, b)
	}

	var created map[string]any
	json.NewDecoder(resp.Body).Decode(&created)
	if created["key"] == nil || created["key"] == "" {
		t.Error("response should include plaintext key")
	}

	// List keys.
	listResp := env.doRequest(t, "GET", "/keys", adminKey, nil)
	defer listResp.Body.Close()

	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", listResp.StatusCode)
	}

	var keys []map[string]any
	json.NewDecoder(listResp.Body).Decode(&keys)
	// Should have at least the admin key + the ci-key we created.
	if len(keys) < 2 {
		t.Errorf("expected at least 2 keys, got %d", len(keys))
	}
}

func TestKeys_Delete(t *testing.T) {
	env := newTestEnv(t)
	_, adminKey := env.createKey(t, "admin-key", "admin")
	deleteID, _ := env.createKey(t, "delete-me", "viewer")

	resp := env.doRequest(t, "DELETE", "/keys/"+deleteID, adminKey, nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("expected 204, got %d", resp.StatusCode)
	}
}

func TestRateLimit(t *testing.T) {
	env := newTestEnv(t)
	_, key := env.createKey(t, "rate-test", "viewer")

	// Viewer limit is 5 per minute in test env.
	var lastStatus int
	for i := 0; i < 7; i++ {
		resp := env.doRequest(t, "GET", "/status", key, nil)
		lastStatus = resp.StatusCode
		resp.Body.Close()
	}

	if lastStatus != http.StatusTooManyRequests {
		t.Errorf("expected 429 after exceeding rate limit, got %d", lastStatus)
	}
}

func TestRateLimit_RetryAfterHeader(t *testing.T) {
	env := newTestEnv(t)
	_, key := env.createKey(t, "retry-after-test", "viewer")

	// Exhaust the limit.
	for i := 0; i < 6; i++ {
		resp := env.doRequest(t, "GET", "/status", key, nil)
		resp.Body.Close()
	}

	resp := env.doRequest(t, "GET", "/status", key, nil)
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		retryAfter := resp.Header.Get("Retry-After")
		if retryAfter == "" {
			t.Error("429 response should include Retry-After header")
		}
	}
}

func TestErrorFormat(t *testing.T) {
	env := newTestEnv(t)
	resp := env.doRequest(t, "GET", "/status", "", nil)
	defer resp.Body.Close()
	assertErrorFormat(t, resp)
}

func TestStatusEndpoint(t *testing.T) {
	env := newTestEnv(t)
	_, key := env.createKey(t, "status-test", "viewer")

	resp := env.doRequest(t, "GET", "/status", key, nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var status map[string]any
	json.NewDecoder(resp.Body).Decode(&status)

	// Should have basic fields.
	for _, field := range []string{"version", "uptime", "agent_count", "running_count", "providers"} {
		if _, ok := status[field]; !ok {
			t.Errorf("status response missing field %q", field)
		}
	}
}

func TestAgentScope_Restricted(t *testing.T) {
	env := newTestEnv(t)

	// Create a key scoped to specific agents.
	result, err := env.apiKeys.Create(t.Context(), "scoped-key", "viewer", []string{"agent-1"}, nil)
	if err != nil {
		t.Fatalf("create scoped key: %v", err)
	}

	// Agent-1 should pass scope check (503 because kyvik is nil, but NOT 403).
	resp := env.doRequest(t, "GET", "/agents/agent-1", result.PlainKey, nil)
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		t.Error("scoped key should be able to access agent-1")
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503 (nil kyvik), got %d", resp.StatusCode)
	}

	// Agent-2 should be denied with 403.
	resp2 := env.doRequest(t, "GET", "/agents/agent-2", result.PlainKey, nil)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 for agent-2, got %d", resp2.StatusCode)
	}
}

func TestScope_ViewerCannotStartAgent(t *testing.T) {
	env := newTestEnv(t)
	_, key := env.createKey(t, "viewer-start-test", "viewer")

	resp := env.doRequest(t, "POST", "/agents/test-agent/start", key, nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("viewer should not be able to start agents, got %d", resp.StatusCode)
	}
}

func TestScope_OperatorCannotDeleteAgent(t *testing.T) {
	env := newTestEnv(t)
	_, key := env.createKey(t, "operator-delete-test", "operator")

	resp := env.doRequest(t, "DELETE", "/agents/test-agent", key, nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("operator should not be able to delete agents, got %d", resp.StatusCode)
	}
}

// assertErrorFormat checks that the response body matches {"error":{...}} format.
func assertErrorFormat(t *testing.T, resp *http.Response) {
	t.Helper()

	if resp.Header.Get("Content-Type") != "application/json" {
		t.Error("error response should have Content-Type: application/json")
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Status  int    `json:"status"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("unmarshal error envelope: %v (body: %s)", err, body)
	}
	if envelope.Error.Code == "" {
		t.Errorf("error.code should not be empty (body: %s)", body)
	}
	if envelope.Error.Message == "" {
		t.Errorf("error.message should not be empty (body: %s)", body)
	}
	if envelope.Error.Status == 0 {
		t.Errorf("error.status should not be 0 (body: %s)", body)
	}

	_ = fmt.Sprintf("validated error: code=%s, message=%s, status=%d",
		envelope.Error.Code, envelope.Error.Message, envelope.Error.Status)
}
