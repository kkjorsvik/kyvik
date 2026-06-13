// Package netproxy implements a tier-aware HTTP forward proxy for sandboxed
// tool execution. It enforces method restrictions, host allowlists, and SSRF
// rules centrally — tools route outbound requests through it via HTTP_PROXY.
package netproxy

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

// TierPolicy defines network access rules for a KTP tier.
type TierPolicy struct {
	// AllowedMethods is the set of HTTP methods this tier can use.
	// Empty/nil means all methods allowed.
	AllowedMethods []string

	// AllowPrivateIPs controls whether SSRF protection is applied.
	AllowPrivateIPs bool
}

// AgentInfo provides the agent context needed for policy decisions.
type AgentInfo struct {
	AgentID      string
	Tier         string
	AllowedHosts []string // per-agent host allowlist (empty = all public hosts)
}

// AgentResolver looks up agent info by sandbox ID.
type AgentResolver func(sandboxID string) (*AgentInfo, error)

// Config configures the network proxy.
type Config struct {
	// ListenAddr is the address to listen on (default "127.0.0.1:0" for random port).
	ListenAddr string

	// DefaultPolicies maps tier names to their network policies.
	DefaultPolicies map[string]TierPolicy

	// ResolveAgent maps a sandbox/agent ID to its agent info.
	ResolveAgent AgentResolver
}

// DefaultPolicies returns the standard tier policies.
func DefaultPolicies() map[string]TierPolicy {
	return map[string]TierPolicy{
		"reader": {
			AllowedMethods:  []string{"GET", "HEAD", "OPTIONS"},
			AllowPrivateIPs: false,
		},
		"writer": {
			AllowedMethods:  []string{"GET", "HEAD", "OPTIONS", "POST", "PUT", "PATCH"},
			AllowPrivateIPs: false,
		},
		"operator": {
			AllowedMethods:  []string{"GET", "HEAD", "OPTIONS", "POST", "PUT", "PATCH", "DELETE"},
			AllowPrivateIPs: false,
		},
		"admin": {
			AllowedMethods:  nil, // all methods
			AllowPrivateIPs: true,
		},
	}
}

// Proxy is a tier-aware HTTP forward proxy.
type Proxy struct {
	policies     map[string]TierPolicy
	resolveAgent AgentResolver
	listener     net.Listener
	server       *http.Server
	addr         string
}

// New creates and starts a Proxy on the configured address.
func New(cfg Config) (*Proxy, error) {
	if cfg.DefaultPolicies == nil {
		cfg.DefaultPolicies = DefaultPolicies()
	}

	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}

	p := &Proxy{
		policies:     cfg.DefaultPolicies,
		resolveAgent: cfg.ResolveAgent,
		listener:     ln,
		addr:         ln.Addr().String(),
	}

	p.server = &http.Server{
		Handler:      p,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
	}

	go func() {
		if err := p.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			slog.Error("netproxy: server error", "error", err)
		}
	}()

	slog.Info("netproxy: started", "addr", p.addr)
	return p, nil
}

// Addr returns the proxy's listen address (useful when port is 0).
func (p *Proxy) Addr() string {
	return p.addr
}

// Close shuts down the proxy server.
func (p *Proxy) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return p.server.Shutdown(ctx)
}

// ServeHTTP handles proxy requests.
// The sandbox ID is passed via the Proxy-Authorization header.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Extract agent info from Proxy-Authorization header.
	sandboxID := r.Header.Get("Proxy-Authorization")
	if sandboxID == "" {
		http.Error(w, "missing Proxy-Authorization (sandbox ID)", http.StatusProxyAuthRequired)
		return
	}
	r.Header.Del("Proxy-Authorization")

	agent, err := p.resolveAgent(sandboxID)
	if err != nil {
		slog.Warn("netproxy: unknown sandbox", "sandbox_id", sandboxID, "error", err)
		http.Error(w, "unknown sandbox", http.StatusForbidden)
		return
	}

	// 1. Method check.
	policy, ok := p.policies[agent.Tier]
	if !ok {
		http.Error(w, fmt.Sprintf("no policy for tier %q", agent.Tier), http.StatusForbidden)
		return
	}

	if len(policy.AllowedMethods) > 0 {
		methodAllowed := false
		for _, m := range policy.AllowedMethods {
			if strings.EqualFold(r.Method, m) {
				methodAllowed = true
				break
			}
		}
		if !methodAllowed {
			http.Error(w, fmt.Sprintf("method %s not allowed for tier %s", r.Method, agent.Tier), http.StatusForbidden)
			return
		}
	}

	// 2. Host allowlist check.
	host := r.URL.Hostname()
	if host == "" {
		host = r.Host
	}
	if !isHostAllowed(host, agent.AllowedHosts) {
		http.Error(w, fmt.Sprintf("host %s not in allowlist", host), http.StatusForbidden)
		return
	}

	// 3. SSRF check (skip for tiers that allow private IPs).
	if !policy.AllowPrivateIPs {
		if err := checkSSRF(host); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
	}

	// 4. Forward the request.
	slog.Debug("netproxy: forwarding",
		"agent_id", agent.AgentID,
		"tier", agent.Tier,
		"method", r.Method,
		"url", r.URL.String(),
	)

	resp, err := http.DefaultTransport.RoundTrip(r)
	if err != nil {
		http.Error(w, fmt.Sprintf("upstream error: %s", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Copy response headers and body.
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}
