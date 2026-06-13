package browser

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/kkjorsvik/kyvik/internal/ktp"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

func requireBrowser(t *testing.T) {
	t.Helper()
	if os.Getenv("KYVIK_SKIP_BROWSER_TESTS") == "1" {
		t.Skip("browser tests skipped by env")
	}
	if os.Getenv("ROD_BROWSER_BIN") != "" {
		return
	}
	for _, bin := range []string{"google-chrome", "chromium", "chromium-browser", "chrome"} {
		if _, err := exec.LookPath(bin); err == nil {
			return
		}
	}
	t.Skip("no browser binary found")
}

func newTestTool(t *testing.T, host string) *Tool {
	t.Helper()
	cfg := Config{
		TimeoutSeconds:      10,
		SettleMillis:        100,
		MaxTextBytes:        1 << 20,
		ViewportWidth:       1280,
		ViewportHeight:      720,
		MaxViewportWidth:    1920,
		MaxViewportHeight:   1080,
		MaxScreenshotWidth:  1920,
		MaxScreenshotHeight: 1080,
		MaxResults:          5,
	}
	tool := New(cfg,
		WithAllowInsecureTLS(true),
		WithAllowedHostsFunc(func(agentID string) ([]string, error) {
			return []string{host}, nil
		}),
	)
	return tool
}

func newTLSServer(t *testing.T, handler http.Handler) *httptest.Server {
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

func TestBrowser_FetchPage(t *testing.T) {
	requireBrowser(t)

	server := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`
<html>
<head><title>Test Page</title></head>
<body>
  <nav>Navigation</nav>
  <main><h1>Main Content</h1><p>Hello world</p></main>
  <footer>Footer content</footer>
</body>
</html>`))
	}))
	defer server.Close()

	u, _ := url.Parse(server.URL)
	tool := newTestTool(t, u.Hostname())

	resp, err := tool.Execute(context.Background(), ktp.NewToolRequest("agent-1", "browser", "fetch_page", map[string]any{
		"url": server.URL,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
	result := resp.Result.(map[string]any)
	content := result["content"].(string)
	if !strings.Contains(content, "Main Content") {
		t.Fatalf("expected main content, got %q", content)
	}
	if strings.Contains(content, "Navigation") {
		t.Fatalf("expected nav removed, got %q", content)
	}
	if result["title"] != "Test Page" {
		t.Fatalf("expected title, got %v", result["title"])
	}
}

func TestBrowser_Screenshot(t *testing.T) {
	requireBrowser(t)

	server := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><body><h1>Hello</h1></body></html>`))
	}))
	defer server.Close()

	u, _ := url.Parse(server.URL)
	tool := newTestTool(t, u.Hostname())

	resp, err := tool.Execute(context.Background(), ktp.NewToolRequest("agent-1", "browser", "screenshot", map[string]any{
		"url": server.URL,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
	result := resp.Result.(map[string]any)
	encoded := result["image_base64"].(string)
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("invalid base64: %v", err)
	}
	if len(data) < 8 || string(data[:8]) != "\x89PNG\r\n\x1a\n" {
		t.Fatalf("expected PNG header, got %v", data[:8])
	}
}

func TestBrowser_ExtractLinks(t *testing.T) {
	requireBrowser(t)

	server := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`
<html>
<body>
  <nav><a href="https://example.com/nav">Nav</a></nav>
  <main>
    <a href="/a">Link A</a>
    <a href="https://example.org/b">Link B</a>
  </main>
</body>
</html>`))
	}))
	defer server.Close()

	u, _ := url.Parse(server.URL)
	tool := newTestTool(t, u.Hostname())

	resp, err := tool.Execute(context.Background(), ktp.NewToolRequest("agent-1", "browser", "extract_links", map[string]any{
		"url": server.URL,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
	result := resp.Result.(map[string]any)
	linksAny, ok := result["links"].([]map[string]any)
	if !ok {
		t.Fatalf("expected links to be []map[string]any")
	}
	if len(linksAny) != 2 {
		t.Fatalf("expected 2 links, got %d", len(linksAny))
	}
}

func TestBrowser_SearchWeb_Parse(t *testing.T) {
	html := `
<html><body>
  <div class="results">
    <div class="result">
      <a class="result__a" href="https://example.com/a">Result A</a>
      <a class="result__snippet">Snippet A</a>
    </div>
    <div class="result">
      <a class="result__a" href="https://example.com/b">Result B</a>
      <a class="result__snippet">Snippet B</a>
    </div>
  </div>
</body></html>`

	results, err := parseDuckDuckGo(strings.NewReader(html), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
}

func TestBrowser_BlocksPrivateIP(t *testing.T) {
	cfg := Config{
		TimeoutSeconds:      10,
		SettleMillis:        100,
		MaxTextBytes:        1 << 20,
		ViewportWidth:       1280,
		ViewportHeight:      720,
		MaxViewportWidth:    1920,
		MaxViewportHeight:   1080,
		MaxScreenshotWidth:  1920,
		MaxScreenshotHeight: 1080,
		MaxResults:          5,
	}
	tool := New(cfg, WithAllowedHostsFunc(func(agentID string) ([]string, error) {
		return nil, nil
	}))

	resp, err := tool.Execute(context.Background(), ktp.NewToolRequest("agent-1", "browser", "fetch_page", map[string]any{
		"url": "https://127.0.0.1",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Fatalf("expected private IP blocked")
	}
}

type mockAgentStore struct {
	agent *types.AgentConfig
}

func (m *mockAgentStore) GetAgent(ctx context.Context, id string) (*types.AgentConfig, error) {
	return m.agent, nil
}

func TestBrowser_PermissionGate_DeniesWorker(t *testing.T) {
	store := &mockAgentStore{agent: &types.AgentConfig{
		ID:       "a1",
		Template: "worker",
	}}
	gate := ktp.NewPermissionGate(store, nil)
	tool := New(Config{TimeoutSeconds: 10})

	res, err := gate.Check(context.Background(), "a1", tool.Declaration(), "fetch_page", map[string]any{
		"url": "https://example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Allowed {
		t.Fatalf("expected worker tier denied for browser tool")
	}
}
