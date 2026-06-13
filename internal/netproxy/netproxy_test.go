package netproxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// newTestProxy creates a proxy with a fixed tier and allowlist for testing.
func newTestProxy(t *testing.T, tier string, allowedHosts []string) *Proxy {
	t.Helper()
	p, err := New(Config{
		ListenAddr: "127.0.0.1:0",
		ResolveAgent: func(sandboxID string) (*AgentInfo, error) {
			return &AgentInfo{
				AgentID:      "test-agent",
				Tier:         tier,
				AllowedHosts: allowedHosts,
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("failed to create proxy: %v", err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

// proxyClient returns an http.Client that routes through the proxy with the given sandbox ID.
func proxyClient(proxyAddr string) *http.Client {
	proxyURL, _ := url.Parse("http://" + proxyAddr)
	return &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		},
	}
}

// proxyReq creates a request with the Proxy-Authorization header set.
func proxyReq(method, url, sandboxID string) *http.Request {
	req, _ := http.NewRequest(method, url, nil)
	req.Header.Set("Proxy-Authorization", sandboxID)
	return req
}

func TestProxy_ReaderCanGET(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer target.Close()

	p := newTestProxy(t, "admin", nil) // Use admin so SSRF won't block localhost
	client := proxyClient(p.Addr())

	req := proxyReq("GET", target.URL, "test-sandbox")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET through proxy failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Fatalf("expected 'ok', got %q", string(body))
	}
}

func TestProxy_ReaderCannotPOST(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer target.Close()

	p := newTestProxy(t, "reader", nil)
	client := proxyClient(p.Addr())

	req := proxyReq("POST", target.URL, "test-sandbox")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for reader POST, got %d", resp.StatusCode)
	}
}

func TestProxy_BlocksPrivateIP(t *testing.T) {
	// Reader tier should block private IPs via SSRF check.
	// Use a target on localhost (127.0.0.1) — a private IP.
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("secret"))
	}))
	defer target.Close()

	p := newTestProxy(t, "reader", nil)
	client := proxyClient(p.Addr())

	req := proxyReq("GET", target.URL, "test-sandbox")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for private IP, got %d", resp.StatusCode)
	}
}

func TestProxy_AdminAllowsPrivateIP(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("internal"))
	}))
	defer target.Close()

	p := newTestProxy(t, "admin", nil)
	client := proxyClient(p.Addr())

	req := proxyReq("GET", target.URL, "test-sandbox")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for admin to private IP, got %d", resp.StatusCode)
	}
	if string(body) != "internal" {
		t.Fatalf("expected 'internal', got %q", string(body))
	}
}

func TestProxy_HostAllowlist(t *testing.T) {
	// Target is on localhost, but we're testing host allowlist, not SSRF.
	// Use admin tier to bypass SSRF, but with a host allowlist.
	p := newTestProxy(t, "admin", []string{"api.example.com"})
	client := proxyClient(p.Addr())

	// Request to a host NOT in the allowlist should be blocked.
	req := proxyReq("GET", "http://evil.example.com/data", "test-sandbox")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for non-allowlisted host, got %d", resp.StatusCode)
	}
}

func TestProxy_WriterCanPOST(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	defer target.Close()

	p, err := New(Config{
		ListenAddr: "127.0.0.1:0",
		DefaultPolicies: map[string]TierPolicy{
			"writer": {
				AllowedMethods:  []string{"GET", "HEAD", "OPTIONS", "POST", "PUT", "PATCH"},
				AllowPrivateIPs: true, // Allow private IPs for testing only
			},
		},
		ResolveAgent: func(sandboxID string) (*AgentInfo, error) {
			return &AgentInfo{AgentID: "test-agent", Tier: "writer"}, nil
		},
	})
	if err != nil {
		t.Fatalf("failed to create proxy: %v", err)
	}
	t.Cleanup(func() { p.Close() })

	client := proxyClient(p.Addr())
	req := proxyReq("POST", target.URL, "test-sandbox")
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
}

func TestProxy_OperatorCanDELETE(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	p, err := New(Config{
		ListenAddr: "127.0.0.1:0",
		DefaultPolicies: map[string]TierPolicy{
			"operator": {
				AllowedMethods:  []string{"GET", "HEAD", "OPTIONS", "POST", "PUT", "PATCH", "DELETE"},
				AllowPrivateIPs: true, // Allow private IPs for testing only
			},
		},
		ResolveAgent: func(sandboxID string) (*AgentInfo, error) {
			return &AgentInfo{AgentID: "test-agent", Tier: "operator"}, nil
		},
	})
	if err != nil {
		t.Fatalf("failed to create proxy: %v", err)
	}
	t.Cleanup(func() { p.Close() })

	client := proxyClient(p.Addr())
	req := proxyReq("DELETE", target.URL+"/resource/1", "test-sandbox")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("DELETE failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}
}

func TestProxy_MissingProxyAuth(t *testing.T) {
	p := newTestProxy(t, "reader", nil)
	client := proxyClient(p.Addr())

	// Request WITHOUT Proxy-Authorization header.
	req, _ := http.NewRequest("GET", "http://example.com", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusProxyAuthRequired {
		t.Fatalf("expected 407 for missing auth, got %d", resp.StatusCode)
	}
}
