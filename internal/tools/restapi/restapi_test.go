package restapi

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kkjorsvik/kyvik/internal/ktp"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

func newTestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("listen not permitted: %v", err)
	}
	server := &httptest.Server{
		Listener: listener,
		Config:   &http.Server{Handler: handler},
		TLS:      &tls.Config{},
	}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}

func testTool(t *testing.T, endpoints []types.RESTAPIEndpoint, secrets map[string]string, server *httptest.Server) *Tool {
	t.Helper()
	endpointFn := func(agentID string) ([]types.RESTAPIEndpoint, error) {
		return endpoints, nil
	}
	secretFn := func(ctx context.Context, agentID, teamID, key string) (string, error) {
		if v, ok := secrets[key]; ok {
			return v, nil
		}
		return "", fmt.Errorf("secret %q not found", key)
	}

	var opts []Option
	if server != nil {
		opts = append(opts, WithTestTransport(server.Client().Transport))
	}

	tool := New(endpointFn, secretFn, opts...)
	t.Cleanup(tool.Stop)
	return tool
}

func makeReq(action string, params map[string]any) ktp.ToolRequest {
	return ktp.NewToolRequest("test-agent", "rest_api", action, params)
}

func TestRESTAPI_ListEndpoints(t *testing.T) {
	endpoints := []types.RESTAPIEndpoint{
		{Name: "weather", Description: "Get weather", Method: "GET", URL: "https://api.weather.com/v1"},
		{Name: "ci", Description: "Trigger CI", Method: "POST", URL: "https://ci.example.com/trigger"},
	}
	tool := testTool(t, endpoints, nil, nil)

	resp, err := tool.Execute(context.Background(), makeReq("list_endpoints", nil))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type: %T", resp.Result)
	}
	eps, ok := result["endpoints"].([]map[string]any)
	if !ok {
		t.Fatalf("endpoints type: %T", result["endpoints"])
	}
	if len(eps) != 2 {
		t.Fatalf("expected 2 endpoints, got %d", len(eps))
	}
	if eps[0]["name"] != "weather" {
		t.Fatalf("first endpoint name = %v", eps[0]["name"])
	}
}

func TestRESTAPI_Call_GET(t *testing.T) {
	server := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/items/42" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id": 42, "name": "test"}`)
	}))

	endpoints := []types.RESTAPIEndpoint{
		{Name: "get_item", Method: "GET", URL: server.URL + "/items/{{.id}}"},
	}
	tool := testTool(t, endpoints, nil, server)

	resp, err := tool.Execute(context.Background(), makeReq("call", map[string]any{
		"endpoint": "get_item",
		"params":   map[string]any{"id": "42"},
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	result := resp.Result.(map[string]any)
	if result["status"] != 200 {
		t.Fatalf("status = %v", result["status"])
	}
	if result["cached"] != false {
		t.Fatalf("cached = %v", result["cached"])
	}
	if result["data"] == nil {
		t.Fatal("expected parsed JSON data")
	}
}

func TestRESTAPI_Call_POST(t *testing.T) {
	var receivedBody string
	server := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		b, _ := readBody(r)
		receivedBody = b
		w.WriteHeader(201)
		fmt.Fprintf(w, `{"created": true}`)
	}))

	endpoints := []types.RESTAPIEndpoint{
		{
			Name:         "create_item",
			Method:       "POST",
			URL:          server.URL + "/items",
			Headers:      map[string]string{"Content-Type": "application/json"},
			BodyTemplate: `{"name": "{{.name}}"}`,
		},
	}
	tool := testTool(t, endpoints, nil, server)

	resp, err := tool.Execute(context.Background(), makeReq("call", map[string]any{
		"endpoint": "create_item",
		"params":   map[string]any{"name": "new-item"},
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	result := resp.Result.(map[string]any)
	if result["status"] != 201 {
		t.Fatalf("status = %v", result["status"])
	}
	if !strings.Contains(receivedBody, "new-item") {
		t.Fatalf("body not rendered: %s", receivedBody)
	}
}

func TestRESTAPI_Call_AuthBearer(t *testing.T) {
	var receivedAuth string
	server := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		fmt.Fprintf(w, `{"ok": true}`)
	}))

	endpoints := []types.RESTAPIEndpoint{
		{
			Name:   "authed",
			Method: "GET",
			URL:    server.URL + "/protected",
			Auth:   types.RESTAPIAuth{Type: "bearer", SecretRef: "my-api-token"},
		},
	}
	tool := testTool(t, endpoints, map[string]string{"my-api-token": "tok_secret123"}, server)

	resp, err := tool.Execute(context.Background(), makeReq("call", map[string]any{
		"endpoint": "authed",
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
	if receivedAuth != "Bearer tok_secret123" {
		t.Fatalf("auth header = %q", receivedAuth)
	}
}

func TestRESTAPI_Call_AuthBasic(t *testing.T) {
	var receivedUser, receivedPass string
	server := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedUser, receivedPass, _ = r.BasicAuth()
		fmt.Fprintf(w, `{"ok": true}`)
	}))

	endpoints := []types.RESTAPIEndpoint{
		{
			Name:   "basic",
			Method: "GET",
			URL:    server.URL + "/api",
			Auth:   types.RESTAPIAuth{Type: "basic", SecretRef: "basic-creds"},
		},
	}
	tool := testTool(t, endpoints, map[string]string{"basic-creds": "user1:pass123"}, server)

	resp, err := tool.Execute(context.Background(), makeReq("call", map[string]any{
		"endpoint": "basic",
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
	if receivedUser != "user1" || receivedPass != "pass123" {
		t.Fatalf("basic auth: user=%q pass=%q", receivedUser, receivedPass)
	}
}

func TestRESTAPI_Call_AuthAPIKey_Header(t *testing.T) {
	var receivedKey string
	server := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedKey = r.Header.Get("X-API-Key")
		fmt.Fprintf(w, `{"ok": true}`)
	}))

	endpoints := []types.RESTAPIEndpoint{
		{
			Name:   "apikey",
			Method: "GET",
			URL:    server.URL + "/api",
			Auth:   types.RESTAPIAuth{Type: "api_key", SecretRef: "my-key", HeaderName: "X-API-Key"},
		},
	}
	tool := testTool(t, endpoints, map[string]string{"my-key": "key_abc"}, server)

	resp, err := tool.Execute(context.Background(), makeReq("call", map[string]any{
		"endpoint": "apikey",
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
	if receivedKey != "key_abc" {
		t.Fatalf("api key header = %q", receivedKey)
	}
}

func TestRESTAPI_Call_AuthAPIKey_Param(t *testing.T) {
	var receivedKey string
	server := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedKey = r.URL.Query().Get("api_key")
		fmt.Fprintf(w, `{"ok": true}`)
	}))

	endpoints := []types.RESTAPIEndpoint{
		{
			Name:   "apikey_param",
			Method: "GET",
			URL:    server.URL + "/api",
			Auth:   types.RESTAPIAuth{Type: "api_key", SecretRef: "my-key", ParamName: "api_key"},
		},
	}
	tool := testTool(t, endpoints, map[string]string{"my-key": "key_xyz"}, server)

	resp, err := tool.Execute(context.Background(), makeReq("call", map[string]any{
		"endpoint": "apikey_param",
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
	if receivedKey != "key_xyz" {
		t.Fatalf("api key param = %q", receivedKey)
	}
}

func TestRESTAPI_Call_AuthCustomHeader(t *testing.T) {
	var receivedHeader string
	server := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeader = r.Header.Get("X-Custom-Auth")
		fmt.Fprintf(w, `{"ok": true}`)
	}))

	endpoints := []types.RESTAPIEndpoint{
		{
			Name:   "custom",
			Method: "GET",
			URL:    server.URL + "/api",
			Auth:   types.RESTAPIAuth{Type: "custom_header", SecretRef: "custom-secret", HeaderName: "X-Custom-Auth"},
		},
	}
	tool := testTool(t, endpoints, map[string]string{"custom-secret": "sec_value"}, server)

	resp, err := tool.Execute(context.Background(), makeReq("call", map[string]any{
		"endpoint": "custom",
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
	if receivedHeader != "sec_value" {
		t.Fatalf("custom header = %q", receivedHeader)
	}
}

func TestRESTAPI_Call_MissingEndpoint(t *testing.T) {
	tool := testTool(t, nil, nil, nil)

	resp, err := tool.Execute(context.Background(), makeReq("call", map[string]any{
		"endpoint": "nonexistent",
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if resp.Success {
		t.Fatal("expected failure for missing endpoint")
	}
	if !strings.Contains(resp.Error, "not found") {
		t.Fatalf("error = %q", resp.Error)
	}
}

func TestRESTAPI_Call_MissingSecret(t *testing.T) {
	server := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"ok": true}`)
	}))

	endpoints := []types.RESTAPIEndpoint{
		{
			Name:   "needs_secret",
			Method: "GET",
			URL:    server.URL + "/api",
			Auth:   types.RESTAPIAuth{Type: "bearer", SecretRef: "missing-key"},
		},
	}
	tool := testTool(t, endpoints, nil, server)

	resp, err := tool.Execute(context.Background(), makeReq("call", map[string]any{
		"endpoint": "needs_secret",
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if resp.Success {
		t.Fatal("expected failure for missing secret")
	}
	if !strings.Contains(resp.Error, "not found") {
		t.Fatalf("error = %q", resp.Error)
	}
}

func TestRESTAPI_Call_HTTPSOnly(t *testing.T) {
	endpoints := []types.RESTAPIEndpoint{
		{Name: "insecure", Method: "GET", URL: "http://example.com/api"},
	}
	tool := testTool(t, endpoints, nil, nil)

	resp, err := tool.Execute(context.Background(), makeReq("call", map[string]any{
		"endpoint": "insecure",
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if resp.Success {
		t.Fatal("expected failure for HTTP URL")
	}
	if !strings.Contains(resp.Error, "HTTPS") {
		t.Fatalf("error = %q", resp.Error)
	}
}

func TestRESTAPI_Call_CacheHit(t *testing.T) {
	callCount := 0
	server := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"count": %d}`, callCount)
	}))

	endpoints := []types.RESTAPIEndpoint{
		{Name: "cached", Method: "GET", URL: server.URL + "/data", CacheTTLSeconds: 60},
	}
	tool := testTool(t, endpoints, nil, server)

	// First call.
	resp1, _ := tool.Execute(context.Background(), makeReq("call", map[string]any{
		"endpoint": "cached",
	}))
	if !resp1.Success {
		t.Fatalf("first call failed: %s", resp1.Error)
	}

	// Second call — should be cached.
	resp2, _ := tool.Execute(context.Background(), makeReq("call", map[string]any{
		"endpoint": "cached",
	}))
	if !resp2.Success {
		t.Fatalf("second call failed: %s", resp2.Error)
	}

	result2 := resp2.Result.(map[string]any)
	if result2["cached"] != true {
		t.Fatalf("expected cached=true, got %v", result2["cached"])
	}
	if callCount != 1 {
		t.Fatalf("expected 1 HTTP call, got %d", callCount)
	}
}

func TestRESTAPI_Call_RateLimit(t *testing.T) {
	server := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"ok": true}`)
	}))

	endpoints := []types.RESTAPIEndpoint{
		{Name: "limited", Method: "POST", URL: server.URL + "/api", RateLimitRPM: 2},
	}
	tool := testTool(t, endpoints, nil, server)

	blocked := false
	for i := 0; i < 20; i++ {
		resp, _ := tool.Execute(context.Background(), makeReq("call", map[string]any{
			"endpoint": "limited",
		}))
		if !resp.Success && strings.Contains(resp.Error, "rate limit") {
			blocked = true
			break
		}
	}
	if !blocked {
		t.Fatal("expected rate limiting to kick in")
	}
}

func TestRESTAPI_Call_ResponseTemplate(t *testing.T) {
	server := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"temperature": 22, "city": "Oslo"}`)
	}))

	endpoints := []types.RESTAPIEndpoint{
		{
			Name:             "weather",
			Method:           "GET",
			URL:              server.URL + "/weather",
			ResponseTemplate: `The temperature in {{.data.city}} is {{.data.temperature}}°C`,
		},
	}
	tool := testTool(t, endpoints, nil, server)

	resp, err := tool.Execute(context.Background(), makeReq("call", map[string]any{
		"endpoint": "weather",
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	result := resp.Result.(map[string]any)
	body := result["body"].(string)
	if !strings.Contains(body, "Oslo") || !strings.Contains(body, "22") {
		t.Fatalf("response template not applied: %s", body)
	}
}

func TestRESTAPI_Call_ResponseTruncation(t *testing.T) {
	// Create a response larger than MaxResponseBody.
	largeBody := strings.Repeat("x", MaxResponseBody+1000)
	server := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, largeBody)
	}))

	endpoints := []types.RESTAPIEndpoint{
		{Name: "large", Method: "GET", URL: server.URL + "/large"},
	}
	tool := testTool(t, endpoints, nil, server)

	resp, err := tool.Execute(context.Background(), makeReq("call", map[string]any{
		"endpoint": "large",
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	result := resp.Result.(map[string]any)
	body := result["body"].(string)
	if len(body) > MaxResponseBody {
		t.Fatalf("body should be truncated to %d, got %d", MaxResponseBody, len(body))
	}
	if result["truncated"] != true {
		t.Fatal("expected truncated=true")
	}
}

func TestRESTAPI_CallAllowedPrivateHost(t *testing.T) {
	server := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{"status":"ok"}`))
	}))

	endpoints := []types.RESTAPIEndpoint{
		{Name: "local-api", Method: "GET", URL: server.URL + "/test"},
	}

	endpointFn := func(agentID string) ([]types.RESTAPIEndpoint, error) {
		return endpoints, nil
	}
	secretFn := func(ctx context.Context, agentID, teamID, key string) (string, error) {
		return "", fmt.Errorf("not found")
	}

	// Extract host from server URL (it's 127.0.0.1 — a private IP).
	serverHost := strings.TrimPrefix(server.URL, "https://")
	host, _, _ := net.SplitHostPort(serverHost)

	tool := New(endpointFn, secretFn,
		WithTestTransport(server.Client().Transport),
		WithAllowedHostsFunc(func(agentID string) ([]string, error) {
			return []string{host}, nil
		}),
	)
	t.Cleanup(tool.Stop)

	resp, err := tool.Execute(context.Background(), makeReq("call", map[string]any{
		"endpoint": "local-api",
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success for allowlisted private host, got error: %s", resp.Error)
	}
}

func TestRESTAPI_CallBlockedPrivateHost(t *testing.T) {
	endpoints := []types.RESTAPIEndpoint{
		{Name: "local-api", Method: "GET", URL: "https://192.168.1.100/test"},
	}
	tool := testTool(t, endpoints, nil, nil)

	resp, err := tool.Execute(context.Background(), makeReq("call", map[string]any{
		"endpoint": "local-api",
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if resp.Success {
		t.Fatal("expected failure for non-allowlisted private host, got success")
	}
	if !strings.Contains(resp.Error, "SSRF") && !strings.Contains(resp.Error, "private") && !strings.Contains(resp.Error, "blocked") {
		t.Fatalf("expected SSRF-related error, got: %s", resp.Error)
	}
}

func TestRESTAPI_CallAllowedPrivateHostHTTP(t *testing.T) {
	// Verify that HTTP (not HTTPS) is allowed for allowlisted hosts.
	// We can't actually connect, but we can verify the URL validation passes.
	endpoints := []types.RESTAPIEndpoint{
		{Name: "local-api", Method: "GET", URL: "http://192.168.1.102/api/test"},
	}

	endpointFn := func(agentID string) ([]types.RESTAPIEndpoint, error) {
		return endpoints, nil
	}
	secretFn := func(ctx context.Context, agentID, teamID, key string) (string, error) {
		return "", fmt.Errorf("not found")
	}

	// Use test transport that returns a canned response.
	tool := New(endpointFn, secretFn,
		WithTestTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader(`{"status":"ok"}`)),
				Header:     make(http.Header),
			}, nil
		})),
		WithAllowedHostsFunc(func(agentID string) ([]string, error) {
			return []string{"192.168.1.102"}, nil
		}),
	)
	t.Cleanup(tool.Stop)

	resp, err := tool.Execute(context.Background(), makeReq("call", map[string]any{
		"endpoint": "local-api",
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success for allowlisted host with HTTP, got error: %s", resp.Error)
	}
}

func TestRESTAPI_Call_EmptyQueryParamsOmitted(t *testing.T) {
	var receivedQuery string
	server := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok": true}`)
	}))

	endpoints := []types.RESTAPIEndpoint{
		{
			Name:   "list_tasks",
			Method: "GET",
			URL:    server.URL + "/tasks",
			QueryParams: map[string]string{
				"context":  "{{.context}}",
				"status":   "{{.status}}",
				"category": "{{.category}}",
			},
		},
	}
	tool := testTool(t, endpoints, nil, server)

	// Only provide "status", leave context and category empty.
	resp, err := tool.Execute(context.Background(), makeReq("call", map[string]any{
		"endpoint": "list_tasks",
		"params":   map[string]any{"status": "active"},
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	// Only "status=active" should be in the query string.
	if !strings.Contains(receivedQuery, "status=active") {
		t.Fatalf("expected status=active in query, got %q", receivedQuery)
	}
	if strings.Contains(receivedQuery, "context=") {
		t.Fatalf("empty context param should be omitted, got %q", receivedQuery)
	}
	if strings.Contains(receivedQuery, "category=") {
		t.Fatalf("empty category param should be omitted, got %q", receivedQuery)
	}
}

func TestRESTAPI_ListEndpoints_WithParameters(t *testing.T) {
	endpoints := []types.RESTAPIEndpoint{
		{
			Name:        "create_page",
			Description: "Create a page",
			Method:      "POST",
			URL:         "https://api.example.com/pages",
			Parameters: []types.EndpointParam{
				{Name: "database_id", Description: "Parent database", Required: true},
				{Name: "title", Required: true},
				{Name: "limit", Type: "int", Default: "10"},
				{Name: "tags", Type: "array", Example: `["a","b"]`},
			},
		},
		{
			Name:        "list_pages",
			Description: "List pages",
			Method:      "GET",
			URL:         "https://api.example.com/pages",
			// No parameters — should omit "parameters" key.
		},
	}
	tool := testTool(t, endpoints, nil, nil)

	resp, err := tool.Execute(context.Background(), makeReq("list_endpoints", nil))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	result := resp.Result.(map[string]any)
	eps := result["endpoints"].([]map[string]any)
	if len(eps) != 2 {
		t.Fatalf("expected 2 endpoints, got %d", len(eps))
	}

	// First endpoint should have parameters.
	params, ok := eps[0]["parameters"].([]map[string]any)
	if !ok {
		t.Fatalf("expected parameters on first endpoint, got %T", eps[0]["parameters"])
	}
	if len(params) != 4 {
		t.Fatalf("expected 4 params, got %d", len(params))
	}

	// Check first param has expected fields.
	p0 := params[0]
	if p0["name"] != "database_id" {
		t.Fatalf("param[0].name = %v", p0["name"])
	}
	if p0["required"] != true {
		t.Fatalf("param[0].required = %v", p0["required"])
	}
	if p0["description"] != "Parent database" {
		t.Fatalf("param[0].description = %v", p0["description"])
	}

	// Second param should not have description (empty).
	p1 := params[1]
	if _, hasDesc := p1["description"]; hasDesc {
		t.Fatalf("param[1] should not have description, got %v", p1["description"])
	}

	// Third param should have type and default but no "string" type.
	p2 := params[2]
	if p2["type"] != "int" {
		t.Fatalf("param[2].type = %v", p2["type"])
	}
	if p2["default"] != "10" {
		t.Fatalf("param[2].default = %v", p2["default"])
	}

	// Fourth param should have example.
	p3 := params[3]
	if p3["example"] != `["a","b"]` {
		t.Fatalf("param[3].example = %v", p3["example"])
	}

	// Second endpoint should NOT have parameters key.
	if _, hasParams := eps[1]["parameters"]; hasParams {
		t.Fatalf("second endpoint should not have parameters key")
	}
}

func TestRESTAPI_Call_MissingRequiredParam(t *testing.T) {
	server := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"ok": true}`)
	}))

	endpoints := []types.RESTAPIEndpoint{
		{
			Name:   "get_page",
			Method: "GET",
			URL:    server.URL + "/pages/{{.page_id}}",
			Parameters: []types.EndpointParam{
				{Name: "page_id", Description: "The page ID", Required: true},
				{Name: "format", Description: "Output format"},
			},
		},
	}
	tool := testTool(t, endpoints, nil, server)

	// Call without providing the required page_id param.
	resp, err := tool.Execute(context.Background(), makeReq("call", map[string]any{
		"endpoint": "get_page",
		"params":   map[string]any{"format": "json"},
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if resp.Success {
		t.Fatal("expected failure for missing required parameter")
	}
	if !strings.Contains(resp.Error, "missing required parameter") {
		t.Fatalf("expected missing-param error, got: %s", resp.Error)
	}
	if !strings.Contains(resp.Error, "page_id") {
		t.Fatalf("error should mention param name, got: %s", resp.Error)
	}
}

// roundTripFunc adapts a function to http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

// readBody is a test helper to read the request body.
func readBody(r *http.Request) (string, error) {
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
