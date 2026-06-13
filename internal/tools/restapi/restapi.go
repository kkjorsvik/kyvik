// Package restapi implements a KTP tool for calling pre-configured REST API endpoints
// with vault-backed auth injection, template rendering, caching, and rate limiting.
package restapi

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kkjorsvik/kyvik/internal/ktp"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// MaxResponseBody is the maximum response body size (10 KB for agent-friendly responses).
const MaxResponseBody = 10 << 10

// Tool implements ktp.Tool for calling pre-configured REST API endpoints.
type Tool struct {
	endpointConfigs    EndpointConfigsFunc
	secretResolver     SecretResolverFunc
	tierFunc           TierFunc
	allowedHostsFunc   AllowedHostsFunc
	cache              *ResponseCache
	limiters           *RateLimiterSet
	testTransport      http.RoundTripper
	oauth2SecretWriter OAuth2SecretWriter
	oauth2Mgr          *OAuth2Manager
}

// New creates a REST API tool with the given resolvers and options.
func New(endpoints EndpointConfigsFunc, secrets SecretResolverFunc, opts ...Option) *Tool {
	t := &Tool{
		endpointConfigs: endpoints,
		secretResolver:  secrets,
		cache:           NewResponseCache(),
		limiters:        NewRateLimiterSet(),
	}
	for _, opt := range opts {
		opt(t)
	}
	// Initialize OAuth2 manager if secret writer is provided.
	if t.oauth2SecretWriter != nil {
		t.oauth2Mgr = NewOAuth2Manager(secrets, t.oauth2SecretWriter)
	} else {
		t.oauth2Mgr = NewOAuth2Manager(secrets, nil)
	}
	return t
}

// Inline returns true so execution happens in-process.
//
// rest_api depends on per-agent endpoint configuration and vault-backed secret
// resolution that are wired in the main process. Running it in the generic
// sandbox binary can lead to "unknown tool" or missing config/secrets.
func (t *Tool) Inline() bool { return true }

// Stop cleans up background goroutines.
func (t *Tool) Stop() {
	t.cache.Stop()
}

// Declaration returns the REST API tool's KTP declaration.
func (t *Tool) Declaration() ktp.ToolDeclaration {
	return ktp.ToolDeclaration{
		Name:         "rest_api",
		Version:      "1.0.0",
		Description:  "Call pre-configured REST API endpoints with auth, caching, and rate limiting",
		MinTier:      ktp.TierWriter,
		DefaultTiers: []string{ktp.TierWriter, ktp.TierOperator, ktp.TierAdmin},
		Actions: []ktp.ActionSpec{
			{
				Name:        "list_endpoints",
				Description: "List all configured REST API endpoints for this agent",
				Parameters: ktp.JSONSchema{
					Type:       "object",
					Properties: map[string]ktp.JSONSchema{},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"endpoints": {Type: "array", Description: "Available endpoints"},
					},
				},
			},
			{
				Name:        "call",
				Description: "Call a configured REST API endpoint",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"endpoint": {Type: "string", Description: "Endpoint name"},
						"params":   {Type: "object", Description: "Template parameters for URL, headers, body"},
					},
					Required: []string{"endpoint"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"status":       {Type: "integer"},
						"headers":      {Type: "object"},
						"body":         {Type: "string"},
						"data":         {Description: "Parsed JSON response body (object or array)"},
						"content_type": {Type: "string"},
						"cached":       {Type: "boolean"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "network", Access: "write", Resource: "*"}},
			},
		},
	}
}

// Execute dispatches to the requested action.
func (t *Tool) Execute(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	start := time.Now()
	switch req.Action {
	case "list_endpoints":
		return t.listEndpoints(req, start)
	case "call":
		return t.call(ctx, req, start)
	default:
		return errResp(req.ID, fmt.Sprintf("unknown action: %s", req.Action)), nil
	}
}

func (t *Tool) listEndpoints(req ktp.ToolRequest, start time.Time) (*ktp.ToolResponse, error) {
	endpoints, err := t.endpointConfigs(req.AgentID)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("failed to resolve endpoints: %s", err)), nil
	}

	list := make([]map[string]any, 0, len(endpoints))
	for _, ep := range endpoints {
		entry := map[string]any{
			"name":        ep.Name,
			"description": ep.Description,
			"method":      ep.Method,
			"url":         ep.URL,
		}
		if len(ep.Parameters) > 0 {
			params := make([]map[string]any, 0, len(ep.Parameters))
			for _, p := range ep.Parameters {
				pm := map[string]any{
					"name":     p.Name,
					"required": p.Required,
				}
				if p.Description != "" {
					pm["description"] = p.Description
				}
				if p.Type != "" && p.Type != "string" {
					pm["type"] = p.Type
				}
				if p.Default != "" {
					pm["default"] = p.Default
				}
				if p.Example != "" {
					pm["example"] = p.Example
				}
				params = append(params, pm)
			}
			entry["parameters"] = params
		}
		list = append(list, entry)
	}

	result := map[string]any{"endpoints": list}
	resp := ktp.NewToolResponse(req.ID, true, result, "", ms(start))
	return &resp, nil
}

func (t *Tool) call(ctx context.Context, req ktp.ToolRequest, start time.Time) (*ktp.ToolResponse, error) {
	// Resolve endpoints.
	endpoints, err := t.endpointConfigs(req.AgentID)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("failed to resolve endpoints: %s", err)), nil
	}

	// Find endpoint by name.
	endpointName, _ := strParam(req.Parameters, "endpoint")
	if endpointName == "" {
		return errResp(req.ID, "missing required parameter: endpoint"), nil
	}

	var ep *types.RESTAPIEndpoint
	for i := range endpoints {
		if endpoints[i].Name == endpointName {
			ep = &endpoints[i]
			break
		}
	}
	if ep == nil {
		return errResp(req.ID, fmt.Sprintf("endpoint %q not found", endpointName)), nil
	}

	// Check rate limit.
	if !t.limiters.Allow(req.AgentID, ep.Name, ep.RateLimitRPM) {
		return errResp(req.ID, fmt.Sprintf("rate limit exceeded for endpoint %q (%d rpm)", ep.Name, ep.RateLimitRPM)), nil
	}

	// Extract template params.
	tmplData := make(map[string]any)
	if paramsRaw, ok := req.Parameters["params"]; ok {
		if paramsMap, ok := paramsRaw.(map[string]any); ok {
			tmplData = paramsMap
		}
	}

	// Validate required parameters (check both existence and non-nil).
	for _, p := range ep.Parameters {
		if p.Required {
			v, ok := tmplData[p.Name]
			if !ok || v == nil {
				return errResp(req.ID, fmt.Sprintf("missing required parameter %q for endpoint %q", p.Name, ep.Name)), nil
			}
		}
	}

	// Render URL.
	renderedURL, err := renderURL(ep.URL, tmplData)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("URL template error: %s", err)), nil
	}

	// Resolve allowed hosts for SSRF exemption.
	var allowedHosts []string
	if t.allowedHostsFunc != nil {
		allowedHosts, _ = t.allowedHostsFunc(req.AgentID)
	}

	// Validate URL (HTTPS only) — skip for allowlisted hosts.
	if err := validateURL(renderedURL); err != nil {
		parsed, parseErr := url.Parse(renderedURL)
		if parseErr != nil || !isHostInList(parsed.Hostname(), allowedHosts) {
			return errResp(req.ID, err.Error()), nil
		}
	}

	// Render query params and append to URL.
	if len(ep.QueryParams) > 0 {
		qp, err := renderQueryParams(ep.QueryParams, tmplData)
		if err != nil {
			return errResp(req.ID, fmt.Sprintf("query param template error: %s", err)), nil
		}
		parsedURL, err := url.Parse(renderedURL)
		if err != nil {
			return errResp(req.ID, fmt.Sprintf("invalid URL: %s", err)), nil
		}
		q := parsedURL.Query()
		for k, v := range qp {
			if v != "" && v != "<no value>" {
				q.Set(k, v)
			}
		}
		parsedURL.RawQuery = q.Encode()
		renderedURL = parsedURL.String()
	}

	// Check cache (GET requests only).
	if strings.EqualFold(ep.Method, "GET") && ep.CacheTTLSeconds > 0 {
		cacheKey := CacheKey(req.AgentID, ep.Name, renderedURL)
		if cached := t.cache.Get(cacheKey); cached != nil {
			cached["cached"] = true
			resp := ktp.NewToolResponse(req.ID, true, cached, "", ms(start))
			return &resp, nil
		}
	}

	// Render headers.
	renderedHeaders, err := renderHeaders(ep.Headers, tmplData)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("header template error: %s", err)), nil
	}

	// Render body.
	var body io.Reader
	if ep.BodyTemplate != "" {
		renderedBody, err := renderBody(ep.BodyTemplate, tmplData)
		if err != nil {
			return errResp(req.ID, fmt.Sprintf("body template error: %s", err)), nil
		}
		body = strings.NewReader(renderedBody)
	}

	// Resolve auth.
	if ep.Auth.Type != "" && ep.Auth.Type != "none" {
		if renderedHeaders == nil {
			renderedHeaders = make(map[string]string)
		}

		if ep.Auth.Type == "oauth2" {
			// OAuth2 uses refresh token flow to get/renew access tokens.
			accessToken, err := t.oauth2Mgr.GetAccessToken(ctx, req.AgentID, req.TeamID, OAuth2AuthConfig{
				ClientIDRef:     ep.Auth.ClientIDRef,
				ClientSecretRef: ep.Auth.ClientSecretRef,
				TokenURL:        ep.Auth.TokenURL,
				RefreshTokenRef: ep.Auth.RefreshTokenRef,
				AccessTokenRef:  ep.Auth.AccessTokenRef,
				Scopes:          ep.Auth.Scopes,
			})
			if err != nil {
				return errResp(req.ID, fmt.Sprintf("oauth2 auth failed: %s", err)), nil
			}
			renderedHeaders["Authorization"] = "Bearer " + accessToken
		} else {
			if ep.Auth.SecretRef == "" {
				return errResp(req.ID, fmt.Sprintf("auth type %q requires secret_ref", ep.Auth.Type)), nil
			}
			secret, err := t.secretResolver(ctx, req.AgentID, req.TeamID, ep.Auth.SecretRef)
			if err != nil {
				return errResp(req.ID, fmt.Sprintf("secret %q not found: %s", ep.Auth.SecretRef, err)), nil
			}

			switch ep.Auth.Type {
			case "bearer":
				renderedHeaders["Authorization"] = "Bearer " + secret
			case "basic":
				// Secret stored as "username:password" — handled via SetBasicAuth on the request below.
				renderedHeaders["_basic_auth"] = secret
			case "api_key":
				if ep.Auth.ParamName != "" {
					// Inject as query param.
					parsedURL, err := url.Parse(renderedURL)
					if err != nil {
						return errResp(req.ID, fmt.Sprintf("invalid URL: %s", err)), nil
					}
					q := parsedURL.Query()
					q.Set(ep.Auth.ParamName, secret)
					parsedURL.RawQuery = q.Encode()
					renderedURL = parsedURL.String()
				} else if ep.Auth.HeaderName != "" {
					renderedHeaders[ep.Auth.HeaderName] = secret
				} else {
					return errResp(req.ID, "api_key auth requires header_name or param_name"), nil
				}
			case "custom_header":
				if ep.Auth.HeaderName == "" {
					return errResp(req.ID, "custom_header auth requires header_name"), nil
				}
				renderedHeaders[ep.Auth.HeaderName] = secret
			default:
				return errResp(req.ID, fmt.Sprintf("unknown auth type: %s", ep.Auth.Type)), nil
			}
		}
	}

	// Build HTTP request.
	method := strings.ToUpper(ep.Method)
	httpReq, err := http.NewRequestWithContext(ctx, method, renderedURL, body)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("invalid request: %s", err)), nil
	}

	// Apply headers.
	for k, v := range renderedHeaders {
		if k == "_basic_auth" {
			parts := strings.SplitN(v, ":", 2)
			if len(parts) == 2 {
				httpReq.SetBasicAuth(parts[0], parts[1])
			}
			continue
		}
		httpReq.Header.Set(k, v)
	}

	// Create client with timeout.
	timeout := 30 * time.Second
	if ep.TimeoutSeconds > 0 {
		timeout = time.Duration(ep.TimeoutSeconds) * time.Second
	}
	if timeout > 120*time.Second {
		timeout = 120 * time.Second
	}
	client := t.newSafeClient(timeout, allowedHosts)

	// Execute request.
	httpResp, err := client.Do(httpReq)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("request failed: %s", err)), nil
	}
	defer httpResp.Body.Close()

	// Read response with size limit.
	bodyBytes, err := io.ReadAll(io.LimitReader(httpResp.Body, int64(MaxResponseBody)+1))
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("failed to read response: %s", err)), nil
	}

	bodyStr := string(bodyBytes)
	truncated := false
	if len(bodyBytes) > MaxResponseBody {
		bodyStr = string(bodyBytes[:MaxResponseBody])
		truncated = true
	}

	// Flatten response headers into map[string]any so the KTP schema validator
	// accepts it as an "object" type (it type-asserts to map[string]any).
	respHeaders := make(map[string]any, len(httpResp.Header))
	for k, vals := range httpResp.Header {
		if len(vals) > 0 {
			respHeaders[k] = vals[0]
		}
	}

	contentType := httpResp.Header.Get("Content-Type")

	result := map[string]any{
		"status":       httpResp.StatusCode,
		"headers":      respHeaders,
		"body":         bodyStr,
		"content_type": contentType,
		"cached":       false,
	}
	if truncated {
		result["truncated"] = true
	}

	// Parse JSON if content type matches.
	if strings.Contains(contentType, "json") {
		var parsed any
		if err := json.Unmarshal(bodyBytes, &parsed); err == nil {
			result["data"] = parsed
		}
	}

	// Apply response template if configured.
	if ep.ResponseTemplate != "" {
		templateData := map[string]any{
			"status":  httpResp.StatusCode,
			"headers": respHeaders,
			"body":    bodyStr,
		}
		if data, ok := result["data"]; ok {
			templateData["data"] = data
		}
		rendered, err := renderResponse(ep.ResponseTemplate, templateData)
		if err == nil && rendered != "" {
			result["body"] = rendered
		}
	}

	// Store in cache if applicable (GET only, with TTL > 0).
	if strings.EqualFold(ep.Method, "GET") && ep.CacheTTLSeconds > 0 {
		cacheKey := CacheKey(req.AgentID, ep.Name, renderedURL)
		t.cache.Set(cacheKey, result, time.Duration(ep.CacheTTLSeconds)*time.Second)
	}

	resp := ktp.NewToolResponse(req.ID, true, result, "", ms(start))
	return &resp, nil
}

// newSafeClient creates an http.Client with SSRF-safe dialer.
func (t *Tool) newSafeClient(timeout time.Duration, allowedHosts []string) *http.Client {
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

				ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
				if err != nil {
					return nil, fmt.Errorf("DNS resolution failed: %s", err)
				}

				// Skip private IP check if host is explicitly allowlisted.
				if !isHostInList(host, allowedHosts) {
					for _, ip := range ips {
						if isPrivateIP(ip.IP) {
							return nil, fmt.Errorf("connection to private IP %s is blocked (SSRF protection)", ip.IP)
						}
					}
				}

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
			if err := validateURL(req.URL.String()); err != nil {
				if !isHostInList(req.URL.Hostname(), allowedHosts) {
					return fmt.Errorf("redirect blocked: %w", err)
				}
			}
			return nil
		},
	}
}

func isHostInList(host string, hosts []string) bool {
	for _, h := range hosts {
		if h == host {
			return true
		}
	}
	return false
}

// validateURL checks that the URL uses HTTPS.
func validateURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %s", err)
	}
	if parsed.Scheme != "https" {
		return fmt.Errorf("only HTTPS is allowed (got %s)", parsed.Scheme)
	}
	return nil
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

func errResp(reqID, msg string) *ktp.ToolResponse {
	resp := ktp.NewToolResponse(reqID, false, nil, msg, 0)
	return &resp
}

func ms(start time.Time) int64 {
	return time.Since(start).Milliseconds()
}
