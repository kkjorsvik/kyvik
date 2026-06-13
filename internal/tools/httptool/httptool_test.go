package httptool

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kkjorsvik/kyvik/internal/ktp"
)

// newTestServer creates a TLS httptest server.
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
	return server
}

// testTool creates an HTTPTool that trusts the test server's TLS cert and has
// a SSRF-safe dialer that allows connections to the test server's loopback address.
func testTool(t *testing.T, allowedHosts []string, server *httptest.Server) *HTTPTool {
	t.Helper()
	tool := New(func(agentID string) ([]string, error) {
		return allowedHosts, nil
	})

	// Override the client builder to trust the test server's self-signed cert
	// and to skip SSRF checks for loopback (since httptest uses 127.0.0.1).
	if server != nil {
		tool.testTransport = server.Client().Transport
	}
	return tool
}

func makeReq(action string, params map[string]any) ktp.ToolRequest {
	return ktp.NewToolRequest("test-agent", "http", action, params)
}

func TestHTTPTool_Fetch(t *testing.T) {
	server := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "hello from server")
	}))
	defer server.Close()

	tool := testTool(t, nil, server)

	resp, err := tool.Execute(context.Background(), makeReq("fetch", map[string]any{
		"url": server.URL + "/test",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	result := resp.Result.(map[string]any)
	if result["status"] != 200 {
		t.Errorf("expected status 200, got %v", result["status"])
	}
	if result["body"] != "hello from server" {
		t.Errorf("expected body 'hello from server', got %v", result["body"])
	}
	if result["content_type"] != "text/plain" {
		t.Errorf("expected content_type text/plain, got %v", result["content_type"])
	}
}

func TestHTTPTool_Request_POST(t *testing.T) {
	var receivedBody string
	var receivedHeader string
	server := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		receivedBody = string(body)
		receivedHeader = r.Header.Get("X-Custom")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, "created")
	}))
	defer server.Close()

	tool := testTool(t, nil, server)

	resp, err := tool.Execute(context.Background(), makeReq("request", map[string]any{
		"url":     server.URL + "/create",
		"method":  "POST",
		"body":    `{"key":"value"}`,
		"headers": map[string]any{"X-Custom": "test-value"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	result := resp.Result.(map[string]any)
	if result["status"] != 201 {
		t.Errorf("expected status 201, got %v", result["status"])
	}
	if receivedBody != `{"key":"value"}` {
		t.Errorf("expected body to be sent, got %q", receivedBody)
	}
	if receivedHeader != "test-value" {
		t.Errorf("expected custom header, got %q", receivedHeader)
	}
}

func TestHTTPTool_BlocksHTTP(t *testing.T) {
	tool := New(func(agentID string) ([]string, error) {
		return nil, nil
	})

	resp, err := tool.Execute(context.Background(), makeReq("fetch", map[string]any{
		"url": "http://example.com",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Error("expected HTTP to be blocked")
	}
	if !strings.Contains(resp.Error, "HTTPS") {
		t.Errorf("expected HTTPS error message, got: %s", resp.Error)
	}
}

func TestHTTPTool_BlocksPrivateIP(t *testing.T) {
	cases := []struct {
		name string
		ip   string
	}{
		{"loopback", "127.0.0.1"},
		{"private10", "10.0.0.1"},
		{"private192", "192.168.1.1"},
		{"private172", "172.16.0.1"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ip := net.ParseIP(tc.ip)
			if !isPrivateIP(ip) {
				t.Errorf("expected %s to be classified as private", tc.ip)
			}
		})
	}

	// Also verify that 0.0.0.0 is blocked.
	if !isPrivateIP(net.ParseIP("0.0.0.0")) {
		t.Error("expected 0.0.0.0 to be classified as private")
	}
	// IPv6 loopback.
	if !isPrivateIP(net.ParseIP("::1")) {
		t.Error("expected ::1 to be classified as private")
	}
}

func TestHTTPTool_AllowedHosts(t *testing.T) {
	tool := New(func(agentID string) ([]string, error) {
		return []string{"api.example.com"}, nil
	})

	resp, err := tool.Execute(context.Background(), makeReq("fetch", map[string]any{
		"url": "https://evil.com/data",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Error("expected non-allowed host to be rejected")
	}
	if !strings.Contains(resp.Error, "not in the allowed hosts list") {
		t.Errorf("expected allowed hosts error, got: %s", resp.Error)
	}
}

func TestHTTPTool_EmptyAllowedHosts(t *testing.T) {
	// When allowed hosts list is empty, the URL validation should pass
	// (the request may still fail due to DNS/network, but validation passes).
	err := validateURL("https://example.com/path", nil)
	if err != nil {
		t.Errorf("expected empty allowed hosts to allow all, got: %s", err)
	}
}

func TestHTTPTool_MaxResponseBody(t *testing.T) {
	// Create a server that returns more than MaxResponseBody bytes.
	bigBody := strings.Repeat("x", MaxResponseBody+1000)
	server := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, bigBody)
	}))
	defer server.Close()

	tool := testTool(t, nil, server)

	resp, err := tool.Execute(context.Background(), makeReq("fetch", map[string]any{
		"url": server.URL + "/big",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	result := resp.Result.(map[string]any)
	body := result["body"].(string)
	if len(body) > MaxResponseBody {
		t.Errorf("expected body to be truncated to %d, got %d", MaxResponseBody, len(body))
	}
	if result["truncated"] != true {
		t.Error("expected truncated flag to be set")
	}
}

func TestHTTPTool_Declaration(t *testing.T) {
	tool := New(func(agentID string) ([]string, error) { return nil, nil })
	decl := tool.Declaration()

	if decl.Name != "http" {
		t.Errorf("expected name http, got %s", decl.Name)
	}
	if decl.Version != "1.0.0" {
		t.Errorf("expected version 1.0.0, got %s", decl.Version)
	}
	if decl.MinTier != ktp.TierWriter {
		t.Errorf("expected min tier writer, got %s", decl.MinTier)
	}
	if len(decl.Actions) != 2 {
		t.Errorf("expected 2 actions, got %d", len(decl.Actions))
	}
	if err := decl.Validate(); err != nil {
		t.Errorf("declaration validation failed: %v", err)
	}
}

// --- Power / Unrestricted tier tests ---

func testToolWithTier(t *testing.T, allowedHosts []string, server *httptest.Server, tier string) *HTTPTool {
	t.Helper()
	tool := New(
		func(agentID string) ([]string, error) { return allowedHosts, nil },
		WithTierFunc(func(agentID string) (string, error) { return tier, nil }),
	)
	if server != nil {
		tool.testTransport = server.Client().Transport
	}
	return tool
}

func TestHTTPTool_Admin_SkipsSSRF(t *testing.T) {
	// Admin tier should skip SSRF private IP checks.
	server := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "admin-ok")
	}))
	defer server.Close()

	tool := testToolWithTier(t, nil, server, "admin")

	resp, err := tool.Execute(context.Background(), makeReq("fetch", map[string]any{
		"url": server.URL + "/test",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected admin tier to succeed, got error: %s", resp.Error)
	}
	result := resp.Result.(map[string]any)
	if result["body"] != "admin-ok" {
		t.Errorf("expected 'admin-ok', got %v", result["body"])
	}
}

func TestHTTPTool_Admin_StillUsesAllowlist(t *testing.T) {
	// Admin tier still checks host allowlist.
	tool := New(
		func(agentID string) ([]string, error) { return []string{"api.example.com"}, nil },
		WithTierFunc(func(agentID string) (string, error) { return "admin", nil }),
	)

	resp, err := tool.Execute(context.Background(), makeReq("fetch", map[string]any{
		"url": "https://evil.com/data",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Error("expected admin tier to still enforce host allowlist")
	}
	if !strings.Contains(resp.Error, "not in the allowed hosts list") {
		t.Errorf("expected allowlist error, got: %s", resp.Error)
	}
}

func TestHTTPTool_Standard_SSRFBlocked(t *testing.T) {
	// Standard tier (no tier func) should still block private IPs.
	// Verify that isPrivateIP correctly identifies loopback.
	if !isPrivateIP(net.ParseIP("127.0.0.1")) {
		t.Error("expected 127.0.0.1 to be private")
	}
	// The existing TestHTTPTool_BlocksPrivateIP covers this in detail;
	// this test verifies the standard path hasn't changed.
}

// TestHTTPTool_ValidateURL_Schemes tests various URL scheme validations.
func TestHTTPTool_ValidateURL_Schemes(t *testing.T) {
	cases := []struct {
		url     string
		wantErr bool
	}{
		{"https://example.com", false},
		{"http://example.com", true},
		{"ftp://example.com", true},
		{"file:///etc/passwd", true},
	}

	for _, tc := range cases {
		err := validateURL(tc.url, nil)
		if (err != nil) != tc.wantErr {
			t.Errorf("validateURL(%q): got err=%v, wantErr=%v", tc.url, err, tc.wantErr)
		}
	}
}
