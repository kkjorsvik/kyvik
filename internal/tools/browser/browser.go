// Package browser implements a KTP headless browser tool for web research.
package browser

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"

	"github.com/kkjorsvik/kyvik/internal/audit"
	"github.com/kkjorsvik/kyvik/internal/ktp"
)

// AllowedHostsFunc resolves the allowed hosts list for an agent.
type AllowedHostsFunc func(agentID string) ([]string, error)

// Config defines browser tool limits.
type Config struct {
	TimeoutSeconds      int
	SettleMillis        int
	MaxTextBytes        int
	ViewportWidth       int
	ViewportHeight      int
	MaxViewportWidth    int
	MaxViewportHeight   int
	MaxScreenshotWidth  int
	MaxScreenshotHeight int
	MaxResults          int
}

// Option configures the Browser tool.
type Option func(*Tool)

// WithAllowedHostsFunc sets the allowed host resolver.
func WithAllowedHostsFunc(fn AllowedHostsFunc) Option {
	return func(t *Tool) { t.allowedHosts = fn }
}

// WithAuditLogger sets the audit logger.
func WithAuditLogger(al audit.Logger) Option {
	return func(t *Tool) { t.audit = al }
}

// WithAllowInsecureTLS allows navigation to HTTPS sites with invalid certs (tests only).
func WithAllowInsecureTLS(allow bool) Option {
	return func(t *Tool) { t.allowInsecure = allow }
}

// Tool implements a headless browser KTP tool.
type Tool struct {
	cfg           Config
	allowedHosts  AllowedHostsFunc
	audit         audit.Logger
	allowInsecure bool

	engineMu sync.Mutex
	engine   browserEngine
}

type browserEngine interface {
	LoadHTML(ctx context.Context, url string, viewportWidth, viewportHeight int, timeout, settle time.Duration) (string, error)
	Screenshot(ctx context.Context, url string, viewportWidth, viewportHeight int, timeout, settle time.Duration, fullPage bool, maxWidth, maxHeight int) ([]byte, bool, error)
	Close() error
}

// New creates a new Browser tool with the provided config.
func New(cfg Config, opts ...Option) *Tool {
	t := &Tool{cfg: cfg}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// Inline returns true so the tool runs in-process (shared browser pool).
func (t *Tool) Inline() bool { return true }

// Close shuts down the shared browser instance.
func (t *Tool) Close() error {
	t.engineMu.Lock()
	defer t.engineMu.Unlock()
	if t.engine == nil {
		return nil
	}
	err := t.engine.Close()
	t.engine = nil
	return err
}

// Declaration returns the KTP tool declaration.
func (t *Tool) Declaration() ktp.ToolDeclaration {
	return ktp.ToolDeclaration{
		Name:        "browser",
		Version:     "1.0.0",
		Description: "Render web pages, capture screenshots, extract links, and search the web",
		MinTier:      ktp.TierOperator,
		DefaultTiers: []string{ktp.TierOperator, ktp.TierAdmin},
		Capabilities: []ktp.Capability{
			{Type: "network", Access: "read", Resource: "*"},
			{Type: "browser", Access: "execute", Resource: "*"},
		},
		Actions: []ktp.ActionSpec{
			{
				Name:        "fetch_page",
				Description: "Fetch a URL and return readable page text",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"url":             {Type: "string", Description: "URL to fetch (HTTPS only)"},
						"timeout_seconds": {Type: "integer", Description: "Timeout in seconds (default 30, max 120)"},
						"settle_millis":   {Type: "integer", Description: "Extra wait after DOMContentLoaded (default 2000)"},
					},
					Required: []string{"url"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"url":           {Type: "string"},
						"title":         {Type: "string"},
						"content":       {Type: "string"},
						"truncated":     {Type: "boolean"},
						"content_bytes": {Type: "integer"},
					},
				},
				RequiredCapabilities: []ktp.Capability{
					{Type: "network", Access: "read", Resource: "*"},
					{Type: "browser", Access: "execute", Resource: "*"},
				},
			},
			{
				Name:        "screenshot",
				Description: "Capture a PNG screenshot of a page",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"url":             {Type: "string", Description: "URL to capture (HTTPS only)"},
						"viewport_width":  {Type: "integer", Description: "Viewport width (default 1920)"},
						"viewport_height": {Type: "integer", Description: "Viewport height (default 1080)"},
						"full_page":       {Type: "boolean", Description: "Capture full page", Default: false},
						"timeout_seconds": {Type: "integer", Description: "Timeout in seconds (default 30, max 120)"},
						"settle_millis":   {Type: "integer", Description: "Extra wait after DOMContentLoaded (default 2000)"},
					},
					Required: []string{"url"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"image_base64": {Type: "string"},
						"width":        {Type: "integer"},
						"height":       {Type: "integer"},
						"full_page":    {Type: "boolean"},
						"truncated":    {Type: "boolean"},
						"bytes":        {Type: "integer"},
					},
				},
				RequiredCapabilities: []ktp.Capability{
					{Type: "network", Access: "read", Resource: "*"},
					{Type: "browser", Access: "execute", Resource: "*"},
				},
			},
			{
				Name:        "extract_links",
				Description: "Extract links from a page",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"url":                {Type: "string", Description: "URL to parse (HTTPS only)"},
						"content_links_only": {Type: "boolean", Description: "Only include links from main content", Default: true},
						"timeout_seconds":    {Type: "integer", Description: "Timeout in seconds (default 30, max 120)"},
						"settle_millis":      {Type: "integer", Description: "Extra wait after DOMContentLoaded (default 2000)"},
					},
					Required: []string{"url"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"links": {Type: "array", Items: &ktp.JSONSchema{Type: "object"}},
					},
				},
				RequiredCapabilities: []ktp.Capability{
					{Type: "network", Access: "read", Resource: "*"},
					{Type: "browser", Access: "execute", Resource: "*"},
				},
			},
			{
				Name:        "search_web",
				Description: "Search the web via DuckDuckGo HTML",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"query":       {Type: "string", Description: "Search query"},
						"max_results": {Type: "integer", Description: "Maximum results (default 5)"},
					},
					Required: []string{"query"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"results": {Type: "array", Items: &ktp.JSONSchema{Type: "object"}},
					},
				},
				RequiredCapabilities: []ktp.Capability{
					{Type: "network", Access: "read", Resource: "*"},
					{Type: "browser", Access: "execute", Resource: "*"},
				},
			},
		},
	}
}

// Execute dispatches to the requested action.
func (t *Tool) Execute(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	switch req.Action {
	case "fetch_page":
		return t.fetchPage(ctx, req)
	case "screenshot":
		return t.screenshot(ctx, req)
	case "extract_links":
		return t.extractLinks(ctx, req)
	case "search_web":
		return t.searchWeb(ctx, req)
	default:
		return errorResponse(req.ID, fmt.Sprintf("unknown action: %s", req.Action), 0), nil
	}
}

func (t *Tool) fetchPage(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	start := time.Now()
	rawURL, err := stringParam(req.Parameters, "url")
	if err != nil {
		return errorResponse(req.ID, err.Error(), 0), nil
	}
	allowedHosts, err := t.resolveAllowedHosts(req.AgentID)
	if err != nil {
		t.auditAction(ctx, req.AgentID, "fetch_page", rawURL, time.Since(start), 0, "", err)
		return errorResponse(req.ID, err.Error(), 0), nil
	}
	parsed, err := validateURL(rawURL, allowedHosts)
	if err != nil {
		t.auditAction(ctx, req.AgentID, "fetch_page", rawURL, time.Since(start), 0, "", err)
		return errorResponse(req.ID, err.Error(), 0), nil
	}
	if err := checkPrivateIPs(ctx, parsed, allowedHosts); err != nil {
		t.auditAction(ctx, req.AgentID, "fetch_page", parsed.String(), time.Since(start), 0, "", err)
		return errorResponse(req.ID, err.Error(), 0), nil
	}

	timeout := clampInt(intParamDefault(req.Parameters, "timeout_seconds", t.cfg.TimeoutSeconds), 1, 120)
	settle := clampInt(intParamDefault(req.Parameters, "settle_millis", t.cfg.SettleMillis), 0, 10000)

	html, err := t.loadHTML(ctx, parsed.String(), timeout, settle)
	if err != nil {
		t.auditAction(ctx, req.AgentID, "fetch_page", parsed.String(), time.Since(start), 0, "", err)
		return errorResponse(req.ID, fmt.Sprintf("fetch failed: %s", err), time.Since(start).Milliseconds()), nil
	}

	title, text := extractReadableText(html)
	textBytes := []byte(text)
	truncated := false
	if len(textBytes) > t.cfg.MaxTextBytes {
		textBytes = textBytes[:t.cfg.MaxTextBytes]
		text = string(textBytes) + "\n\n[TRUNCATED]"
		truncated = true
	}

	result := map[string]any{
		"url":           parsed.String(),
		"title":         title,
		"content":       text,
		"truncated":     truncated,
		"content_bytes": len([]byte(text)),
	}
	t.auditAction(ctx, req.AgentID, "fetch_page", parsed.String(), time.Since(start), len([]byte(text)), "", nil)
	return successResponse(req.ID, result, time.Since(start).Milliseconds()), nil
}

func (t *Tool) screenshot(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	start := time.Now()
	rawURL, err := stringParam(req.Parameters, "url")
	if err != nil {
		return errorResponse(req.ID, err.Error(), 0), nil
	}
	allowedHosts, err := t.resolveAllowedHosts(req.AgentID)
	if err != nil {
		t.auditAction(ctx, req.AgentID, "screenshot", rawURL, time.Since(start), 0, "", err)
		return errorResponse(req.ID, err.Error(), 0), nil
	}
	parsed, err := validateURL(rawURL, allowedHosts)
	if err != nil {
		t.auditAction(ctx, req.AgentID, "screenshot", rawURL, time.Since(start), 0, "", err)
		return errorResponse(req.ID, err.Error(), 0), nil
	}
	if err := checkPrivateIPs(ctx, parsed, allowedHosts); err != nil {
		t.auditAction(ctx, req.AgentID, "screenshot", parsed.String(), time.Since(start), 0, "", err)
		return errorResponse(req.ID, err.Error(), 0), nil
	}

	timeout := clampInt(intParamDefault(req.Parameters, "timeout_seconds", t.cfg.TimeoutSeconds), 1, 120)
	settle := clampInt(intParamDefault(req.Parameters, "settle_millis", t.cfg.SettleMillis), 0, 10000)
	fullPage := boolParamDefault(req.Parameters, "full_page", false)

	width := clampInt(intParamDefault(req.Parameters, "viewport_width", t.cfg.ViewportWidth), 320, t.cfg.MaxViewportWidth)
	height := clampInt(intParamDefault(req.Parameters, "viewport_height", t.cfg.ViewportHeight), 200, t.cfg.MaxViewportHeight)

	var img []byte
	var truncated bool
	img, truncated, err = t.captureScreenshot(ctx, parsed.String(), width, height, timeout, settle, fullPage)
	if err != nil {
		t.auditAction(ctx, req.AgentID, "screenshot", parsed.String(), time.Since(start), 0, "", err)
		return errorResponse(req.ID, fmt.Sprintf("screenshot failed: %s", err), time.Since(start).Milliseconds()), nil
	}

	hash := sha256.Sum256(img)
	hashHex := hex.EncodeToString(hash[:])
	encoded := base64.StdEncoding.EncodeToString(img)

	result := map[string]any{
		"image_base64": encoded,
		"width":        width,
		"height":       height,
		"full_page":    fullPage,
		"truncated":    truncated,
		"bytes":        len(img),
	}
	t.auditAction(ctx, req.AgentID, "screenshot", parsed.String(), time.Since(start), len(img), hashHex, nil)
	return successResponse(req.ID, result, time.Since(start).Milliseconds()), nil
}

func (t *Tool) extractLinks(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	start := time.Now()
	rawURL, err := stringParam(req.Parameters, "url")
	if err != nil {
		return errorResponse(req.ID, err.Error(), 0), nil
	}
	contentOnly := boolParamDefault(req.Parameters, "content_links_only", true)

	allowedHosts, err := t.resolveAllowedHosts(req.AgentID)
	if err != nil {
		t.auditAction(ctx, req.AgentID, "extract_links", rawURL, time.Since(start), 0, "", err)
		return errorResponse(req.ID, err.Error(), 0), nil
	}
	parsed, err := validateURL(rawURL, allowedHosts)
	if err != nil {
		t.auditAction(ctx, req.AgentID, "extract_links", rawURL, time.Since(start), 0, "", err)
		return errorResponse(req.ID, err.Error(), 0), nil
	}
	if err := checkPrivateIPs(ctx, parsed, allowedHosts); err != nil {
		t.auditAction(ctx, req.AgentID, "extract_links", parsed.String(), time.Since(start), 0, "", err)
		return errorResponse(req.ID, err.Error(), 0), nil
	}

	timeout := clampInt(intParamDefault(req.Parameters, "timeout_seconds", t.cfg.TimeoutSeconds), 1, 120)
	settle := clampInt(intParamDefault(req.Parameters, "settle_millis", t.cfg.SettleMillis), 0, 10000)

	html, err := t.loadHTML(ctx, parsed.String(), timeout, settle)
	if err != nil {
		t.auditAction(ctx, req.AgentID, "extract_links", parsed.String(), time.Since(start), 0, "", err)
		return errorResponse(req.ID, fmt.Sprintf("link extraction failed: %s", err), time.Since(start).Milliseconds()), nil
	}

	links := extractLinksFromHTML(html, parsed, contentOnly)
	result := map[string]any{
		"links": links,
	}
	t.auditAction(ctx, req.AgentID, "extract_links", parsed.String(), time.Since(start), len(links), "", nil)
	return successResponse(req.ID, result, time.Since(start).Milliseconds()), nil
}

func (t *Tool) searchWeb(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	start := time.Now()
	query, err := stringParam(req.Parameters, "query")
	if err != nil {
		return errorResponse(req.ID, err.Error(), 0), nil
	}
	maxResults := clampInt(intParamDefault(req.Parameters, "max_results", t.cfg.MaxResults), 1, 20)

	ddgURL := "https://duckduckgo.com/html/?q=" + url.QueryEscape(query)
	allowedHosts, err := t.resolveAllowedHosts(req.AgentID)
	if err != nil {
		t.auditAction(ctx, req.AgentID, "search_web", ddgURL, time.Since(start), 0, "", err)
		return errorResponse(req.ID, err.Error(), 0), nil
	}
	// The DDG endpoint is hardcoded by the tool (not agent-supplied), so bypass
	// the host allowlist check. Redirect validation still uses allowedHosts.
	parsed, err := validateURL(ddgURL, nil)
	if err != nil {
		t.auditAction(ctx, req.AgentID, "search_web", ddgURL, time.Since(start), 0, "", err)
		return errorResponse(req.ID, err.Error(), 0), nil
	}
	if err := checkPrivateIPs(ctx, parsed, allowedHosts); err != nil {
		t.auditAction(ctx, req.AgentID, "search_web", parsed.String(), time.Since(start), 0, "", err)
		return errorResponse(req.ID, err.Error(), 0), nil
	}

	client := &http.Client{
		Timeout: time.Duration(t.cfg.TimeoutSeconds) * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			_, err := validateURL(req.URL.String(), allowedHosts)
			return err
		},
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return errorResponse(req.ID, fmt.Sprintf("request failed: %s", err), 0), nil
	}
	httpReq.Header.Set("User-Agent", "KyvikBrowser/1.0 (+https://kyvik)")

	resp, err := client.Do(httpReq)
	if err != nil {
		return errorResponse(req.ID, fmt.Sprintf("search failed: %s", err), 0), nil
	}
	defer resp.Body.Close()

	results, err := parseDuckDuckGo(resp.Body, maxResults)
	if err != nil {
		return errorResponse(req.ID, fmt.Sprintf("parse failed: %s", err), 0), nil
	}

	result := map[string]any{"results": results}
	t.auditAction(ctx, req.AgentID, "search_web", parsed.String(), time.Since(start), len(results), "", nil)
	return successResponse(req.ID, result, time.Since(start).Milliseconds()), nil
}

func (t *Tool) resolveAllowedHosts(agentID string) ([]string, error) {
	if t.allowedHosts == nil {
		return nil, nil
	}
	return t.allowedHosts(agentID)
}

func (t *Tool) ensureEngine() (browserEngine, error) {
	t.engineMu.Lock()
	defer t.engineMu.Unlock()
	if t.engine != nil {
		return t.engine, nil
	}
	engine, err := newRodEngine(t.allowInsecure)
	if err != nil {
		engine, err = newChromedpEngine(t.allowInsecure)
	}
	if err != nil {
		return nil, err
	}
	t.engine = engine
	return engine, nil
}

func (t *Tool) loadHTML(ctx context.Context, url string, timeoutSec, settleMillis int) (string, error) {
	engine, err := t.ensureEngine()
	if err != nil {
		return "", err
	}
	return engine.LoadHTML(ctx, url, t.cfg.ViewportWidth, t.cfg.ViewportHeight, time.Duration(timeoutSec)*time.Second, time.Duration(settleMillis)*time.Millisecond)
}

func extractReadableText(html string) (string, string) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return "", ""
	}
	removeNoise(doc)

	root := findMainContent(doc)
	title := strings.TrimSpace(doc.Find("title").First().Text())
	text := normalizeWhitespace(root.Text())
	return title, text
}

func extractLinksFromHTML(html string, base *url.URL, contentOnly bool) []map[string]any {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return nil
	}

	scope := doc.Selection
	if contentOnly {
		removeNoise(doc)
		scope = findMainContent(doc)
	}

	links := make([]map[string]any, 0)
	seen := map[string]struct{}{}
	scope.Find("a").Each(func(_ int, s *goquery.Selection) {
		href, ok := s.Attr("href")
		if !ok {
			return
		}
		href = strings.TrimSpace(href)
		if href == "" {
			return
		}
		if strings.HasPrefix(href, "javascript:") || strings.HasPrefix(href, "#") {
			return
		}
		resolved := resolveURL(base, href)
		if resolved == "" {
			return
		}
		if _, ok := seen[resolved]; ok {
			return
		}
		seen[resolved] = struct{}{}
		text := normalizeWhitespace(s.Text())
		parsed, _ := url.Parse(resolved)
		isExternal := false
		if parsed != nil && parsed.Hostname() != "" && !strings.EqualFold(parsed.Hostname(), base.Hostname()) {
			isExternal = true
		}
		links = append(links, map[string]any{
			"text":        text,
			"href":        resolved,
			"is_external": isExternal,
		})
	})
	return links
}

func parseDuckDuckGo(r io.Reader, maxResults int) ([]map[string]any, error) {
	doc, err := goquery.NewDocumentFromReader(r)
	if err != nil {
		return nil, err
	}
	results := make([]map[string]any, 0, maxResults)
	doc.Find("a.result__a").Each(func(_ int, s *goquery.Selection) {
		if len(results) >= maxResults {
			return
		}
		title := strings.TrimSpace(s.Text())
		href, _ := s.Attr("href")
		snippet := ""
		if parent := s.ParentsFiltered(".result").First(); parent != nil {
			snippet = strings.TrimSpace(parent.Find(".result__snippet").First().Text())
		}
		results = append(results, map[string]any{
			"title":   title,
			"url":     href,
			"snippet": snippet,
		})
	})
	return results, nil
}

func (t *Tool) captureScreenshot(ctx context.Context, url string, width, height, timeoutSec, settleMillis int, fullPage bool) ([]byte, bool, error) {
	engine, err := t.ensureEngine()
	if err != nil {
		return nil, false, err
	}
	return engine.Screenshot(ctx, url, width, height, time.Duration(timeoutSec)*time.Second, time.Duration(settleMillis)*time.Millisecond, fullPage, t.cfg.MaxScreenshotWidth, t.cfg.MaxScreenshotHeight)
}

func removeNoise(doc *goquery.Document) {
	doc.Find("script, style, nav, header, footer, aside, noscript, iframe, svg, form").Remove()
	doc.Find(".nav, .navbar, .footer, .header, .sidebar, .ads, .advert, .cookie, .banner, .modal").Remove()
}

func findMainContent(doc *goquery.Document) *goquery.Selection {
	selectors := []string{"article", "main", "[role=main]", "#content", ".content", ".post", ".article"}
	for _, sel := range selectors {
		if found := doc.Find(sel).First(); found.Length() > 0 {
			return found
		}
	}
	if body := doc.Find("body").First(); body.Length() > 0 {
		return body
	}
	return doc.Selection
}

func normalizeWhitespace(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// Process line-by-line: normalize horizontal whitespace within each line
	// and collapse runs of blank lines into a single paragraph separator.
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	prevBlank := false
	for _, line := range lines {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			if !prevBlank {
				out = append(out, "")
			}
			prevBlank = true
		} else {
			out = append(out, line)
			prevBlank = false
		}
	}
	// Remove leading/trailing blank lines.
	for len(out) > 0 && out[0] == "" {
		out = out[1:]
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}

func resolveURL(base *url.URL, href string) string {
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if base == nil {
		return u.String()
	}
	return base.ResolveReference(u).String()
}

func validateURL(rawURL string, allowedHosts []string) (*url.URL, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %s", err)
	}
	if parsed.Scheme != "https" {
		return nil, fmt.Errorf("only HTTPS is allowed (got %s)", parsed.Scheme)
	}
	if parsed.Hostname() == "" {
		return nil, fmt.Errorf("invalid URL: missing host")
	}
	if len(allowedHosts) > 0 {
		host := parsed.Hostname()
		allowed := false
		for _, h := range allowedHosts {
			if strings.EqualFold(h, host) {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, fmt.Errorf("host %q is not in the allowed hosts list", host)
		}
	}
	return parsed, nil
}

func checkPrivateIPs(ctx context.Context, u *url.URL, allowedHosts []string) error {
	host := u.Hostname()
	explicitAllow := hostAllowed(host, allowedHosts)

	ip := net.ParseIP(host)
	if ip != nil {
		if isPrivateIP(ip) && !explicitAllow {
			return fmt.Errorf("connection to private IP %s is blocked (SSRF protection)", ip)
		}
		return nil
	}

	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("DNS resolution failed: %s", err)
	}
	for _, ipAddr := range ips {
		if isPrivateIP(ipAddr.IP) && !explicitAllow {
			return fmt.Errorf("connection to private IP %s is blocked (SSRF protection)", ipAddr.IP)
		}
	}
	return nil
}

func hostAllowed(host string, allowedHosts []string) bool {
	for _, h := range allowedHosts {
		if strings.EqualFold(h, host) {
			return true
		}
	}
	return false
}

// privateRanges contains the CIDR ranges considered private/internal.
var privateRanges []*net.IPNet

func init() {
	for _, cidr := range []string{
		"127.0.0.0/8",
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"169.254.0.0/16",
		"::1/128",
		"fc00::/7",
		"fe80::/10",
	} {
		_, network, _ := net.ParseCIDR(cidr)
		privateRanges = append(privateRanges, network)
	}
}

func isPrivateIP(ip net.IP) bool {
	if ip.IsUnspecified() {
		return true
	}
	for _, network := range privateRanges {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

func (t *Tool) auditAction(ctx context.Context, agentID, action, url string, duration time.Duration, size int, hash string, err error) {
	if t.audit == nil {
		return
	}
	decision := "allowed"
	details := map[string]any{
		"url":         url,
		"duration_ms": duration.Milliseconds(),
		"size":        size,
	}
	if hash != "" {
		details["thumbnail_hash"] = hash
	}
	if err != nil {
		decision = "denied"
		details["error"] = err.Error()
	}
	data, _ := json.Marshal(details)
	_ = audit.LogToolCall(ctx, t.audit, agentID, "browser", action, url, decision, string(data))
}

// --- parameter helpers ---

func stringParam(params map[string]any, key string) (string, error) {
	raw, ok := params[key]
	if !ok {
		return "", fmt.Errorf("missing required parameter: %s", key)
	}
	s, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("parameter %s must be a string", key)
	}
	return s, nil
}

func intParamDefault(params map[string]any, key string, def int) int {
	raw, ok := params[key]
	if !ok {
		return def
	}
	switch v := raw.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	default:
		return def
	}
}

func boolParamDefault(params map[string]any, key string, def bool) bool {
	raw, ok := params[key]
	if !ok {
		return def
	}
	if v, ok := raw.(bool); ok {
		return v
	}
	return def
}

// isBrowserDisconnected reports whether err indicates the browser process has
// crashed or the WebSocket connection was lost.
func isBrowserDisconnected(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "context canceled") ||
		strings.Contains(msg, "eof") ||
		strings.Contains(msg, "websocket") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "broken pipe")
}

func clampInt(val, min, max int) int {
	if val < min {
		return min
	}
	if val > max {
		return max
	}
	return val
}

func errorResponse(id, msg string, ms int64) *ktp.ToolResponse {
	resp := ktp.NewToolResponse(id, false, nil, msg, ms)
	return &resp
}

func successResponse(id string, result any, ms int64) *ktp.ToolResponse {
	resp := ktp.NewToolResponse(id, true, result, "", ms)
	return &resp
}
