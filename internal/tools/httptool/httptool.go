// Package httptool implements a KTP HTTP client tool with SSRF protection.
package httptool

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kkjorsvik/kyvik/internal/ktp"
)

// MaxResponseBody is the maximum response body size (1 MB).
const MaxResponseBody = 1 << 20

// AllowedHostsFunc resolves the allowed hosts list for an agent.
type AllowedHostsFunc func(agentID string) ([]string, error)

// TierFunc resolves an agent's KTP tier.
type TierFunc func(agentID string) (string, error)

// HTTPOption configures an HTTPTool.
type HTTPOption func(*HTTPTool)

// WithTierFunc sets the tier resolver callback.
func WithTierFunc(fn TierFunc) HTTPOption {
	return func(t *HTTPTool) { t.tierFunc = fn }
}

// HTTPTool implements ktp.Tool for making HTTP requests.
type HTTPTool struct {
	resolveAllowedHosts AllowedHostsFunc
	tierFunc            TierFunc
	testTransport       http.RoundTripper // override transport for tests
}

// New creates an HTTPTool with the given allowed-hosts resolver and optional callbacks.
func New(resolve AllowedHostsFunc, opts ...HTTPOption) *HTTPTool {
	t := &HTTPTool{resolveAllowedHosts: resolve}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// Declaration returns the HTTP tool's KTP declaration.
func (t *HTTPTool) Declaration() ktp.ToolDeclaration {
	return ktp.ToolDeclaration{
		Name:        "http",
		Version:     "1.0.0",
		Description: "Make HTTP requests to external services",
		MinTier:      ktp.TierWriter,
		DefaultTiers: []string{ktp.TierWriter, ktp.TierOperator, ktp.TierAdmin},
		Actions: []ktp.ActionSpec{
			{
				Name:        "fetch",
				Description: "Fetch a URL (GET request)",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"url": {Type: "string", Description: "URL to fetch (HTTPS only)"},
					},
					Required: []string{"url"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"status":       {Type: "integer"},
						"headers":      {Type: "object"},
						"body":         {Type: "string"},
						"content_type": {Type: "string"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "network", Access: "read", Resource: "*"}},
			},
			{
				Name:        "request",
				Description: "Make an HTTP request with full control over method, headers, and body",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"url":     {Type: "string", Description: "URL to request (HTTPS only)"},
						"method":  {Type: "string", Description: "HTTP method", Enum: []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD"}},
						"headers": {Type: "object", Description: "Request headers"},
						"body":    {Type: "string", Description: "Request body"},
						"timeout": {Type: "integer", Description: "Timeout in seconds (default 30, max 120)"},
					},
					Required: []string{"url", "method"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"status":       {Type: "integer"},
						"headers":      {Type: "object"},
						"body":         {Type: "string"},
						"content_type": {Type: "string"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "network", Access: "write", Resource: "*"}},
			},
		},
	}
}

// Execute dispatches to the requested action.
func (t *HTTPTool) Execute(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	start := time.Now()

	switch req.Action {
	case "fetch":
		return t.fetch(ctx, req, start)
	case "request":
		return t.request(ctx, req, start)
	default:
		return errResp(req.ID, fmt.Sprintf("unknown action: %s", req.Action)), nil
	}
}

func (t *HTTPTool) fetch(ctx context.Context, req ktp.ToolRequest, start time.Time) (*ktp.ToolResponse, error) {
	rawURL, err := strParam(req.Parameters, "url")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	tier := t.getAgentTier(req.AgentID)
	skipSSRF := tier == "admin"

	allowedHosts, err := t.resolveAllowedHosts(req.AgentID)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("failed to resolve allowed hosts: %s", err)), nil
	}

	if err := validateURL(rawURL, allowedHosts); err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	client := t.newSafeClient(30*time.Second, allowedHosts, skipSSRF)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("invalid request: %s", err)), nil
	}

	return t.doRequest(client, httpReq, req.ID, start)
}

func (t *HTTPTool) request(ctx context.Context, req ktp.ToolRequest, start time.Time) (*ktp.ToolResponse, error) {
	rawURL, err := strParam(req.Parameters, "url")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	method, err := strParam(req.Parameters, "method")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	tier := t.getAgentTier(req.AgentID)
	skipSSRF := tier == "admin"

	allowedHosts, err := t.resolveAllowedHosts(req.AgentID)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("failed to resolve allowed hosts: %s", err)), nil
	}

	if err := validateURL(rawURL, allowedHosts); err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	timeout := intDefault(req.Parameters, "timeout", 30)
	if timeout > 120 {
		timeout = 120
	}
	if timeout < 1 {
		timeout = 1
	}

	var body io.Reader
	if bodyStr := strDefault(req.Parameters, "body", ""); bodyStr != "" {
		body = strings.NewReader(bodyStr)
	}

	client := t.newSafeClient(time.Duration(timeout)*time.Second, allowedHosts, skipSSRF)
	httpReq, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("invalid request: %s", err)), nil
	}

	// Set custom headers.
	if headersRaw, ok := req.Parameters["headers"]; ok {
		if headers, ok := headersRaw.(map[string]any); ok {
			for k, v := range headers {
				if s, ok := v.(string); ok {
					httpReq.Header.Set(k, s)
				}
			}
		}
	}

	return t.doRequest(client, httpReq, req.ID, start)
}

func (t *HTTPTool) doRequest(client *http.Client, httpReq *http.Request, reqID string, start time.Time) (*ktp.ToolResponse, error) {
	httpResp, err := client.Do(httpReq)
	if err != nil {
		return errResp(reqID, fmt.Sprintf("request failed: %s", err)), nil
	}
	defer httpResp.Body.Close()

	// Read body with size limit.
	bodyBytes, err := io.ReadAll(io.LimitReader(httpResp.Body, MaxResponseBody+1))
	if err != nil {
		return errResp(reqID, fmt.Sprintf("failed to read response: %s", err)), nil
	}

	bodyStr := string(bodyBytes)
	truncated := false
	if len(bodyBytes) > MaxResponseBody {
		bodyStr = string(bodyBytes[:MaxResponseBody])
		truncated = true
	}

	// Flatten headers (first value only).
	headers := make(map[string]string, len(httpResp.Header))
	for k, vals := range httpResp.Header {
		if len(vals) > 0 {
			headers[k] = vals[0]
		}
	}

	result := map[string]any{
		"status":       httpResp.StatusCode,
		"headers":      headers,
		"body":         bodyStr,
		"content_type": httpResp.Header.Get("Content-Type"),
	}
	if truncated {
		result["truncated"] = true
	}

	resp := ktp.NewToolResponse(reqID, true, result, "", ms(start))
	return &resp, nil
}

// getAgentTier returns the agent's tier, or empty string if no tier func is configured.
func (t *HTTPTool) getAgentTier(agentID string) string {
	if t.tierFunc == nil {
		return ""
	}
	tier, err := t.tierFunc(agentID)
	if err != nil {
		return ""
	}
	return tier
}

// newSafeClient creates an http.Client with optional SSRF-safe dialer and redirect checks.
// When skipSSRF is true, private IP checks are disabled (power/unrestricted tiers).
func (t *HTTPTool) newSafeClient(timeout time.Duration, allowedHosts []string, skipSSRF bool) *http.Client {
	var transport http.RoundTripper

	if t.testTransport != nil {
		transport = t.testTransport
	} else {
		dialer := &net.Dialer{Timeout: 10 * time.Second}
		transport = &http.Transport{
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, port, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, fmt.Errorf("invalid address: %s", err)
				}

				// Resolve and check all IPs (skip for elevated tiers).
				if !skipSSRF {
					ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
					if err != nil {
						return nil, fmt.Errorf("DNS resolution failed: %s", err)
					}

					for _, ip := range ips {
						if isPrivateIP(ip.IP) {
							return nil, fmt.Errorf("connection to private IP %s is blocked (SSRF protection)", ip.IP)
						}
					}

					// Dial using the resolved IPs directly to prevent DNS rebinding.
					// TLS SNI uses the request URL hostname, not the dial address.
					var lastErr error
					for _, ip := range ips {
						conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
						if dialErr != nil {
							lastErr = dialErr
							continue
						}
						return conn, nil
					}
					if lastErr != nil {
						return nil, lastErr
					}
					return nil, fmt.Errorf("no usable IP addresses for %s", host)
				}

				return dialer.DialContext(ctx, network, net.JoinHostPort(host, port))
			},
		}
	}

	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			// Revalidate each redirect target.
			if err := validateURL(req.URL.String(), allowedHosts); err != nil {
				return fmt.Errorf("redirect blocked: %w", err)
			}
			return nil
		},
	}
}

// validateURL checks scheme, host allowlist.
func validateURL(rawURL string, allowedHosts []string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %s", err)
	}

	// Only HTTPS allowed.
	if parsed.Scheme != "https" {
		return fmt.Errorf("only HTTPS is allowed (got %s)", parsed.Scheme)
	}

	// Host allowlist check.
	if len(allowedHosts) > 0 {
		host := parsed.Hostname()
		allowed := false
		for _, h := range allowedHosts {
			if strings.EqualFold(host, h) {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("host %q is not in the allowed hosts list", host)
		}
	}

	return nil
}

// privateRanges contains the CIDR ranges considered private/internal.
var privateRanges []*net.IPNet

func init() {
	for _, cidr := range []string{
		"127.0.0.0/8",    // loopback
		"10.0.0.0/8",     // RFC 1918
		"172.16.0.0/12",  // RFC 1918
		"192.168.0.0/16", // RFC 1918
		"169.254.0.0/16", // link-local
		"::1/128",        // IPv6 loopback
		"fc00::/7",       // IPv6 unique local
		"fe80::/10",      // IPv6 link-local
	} {
		_, network, _ := net.ParseCIDR(cidr)
		privateRanges = append(privateRanges, network)
	}
}

// isPrivateIP checks if an IP is in any private/reserved range.
func isPrivateIP(ip net.IP) bool {
	// Check 0.0.0.0 explicitly.
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

// --- parameter helpers ---

func strParam(params map[string]any, key string) (string, error) {
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

func strDefault(params map[string]any, key, def string) string {
	raw, ok := params[key]
	if !ok {
		return def
	}
	s, ok := raw.(string)
	if !ok {
		return def
	}
	return s
}

func intDefault(params map[string]any, key string, def int) int {
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

func errResp(reqID, msg string) *ktp.ToolResponse {
	resp := ktp.NewToolResponse(reqID, false, nil, msg, 0)
	return &resp
}

func ms(start time.Time) int64 {
	return time.Since(start).Milliseconds()
}
