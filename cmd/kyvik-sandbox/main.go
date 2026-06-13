// kyvik-sandbox is the sandboxed tool execution binary.
// It reads a KTP ToolRequest from stdin, executes the tool, and writes
// a KTP ToolResponse to stdout. Resource limits are applied at startup
// as defense-in-depth (the parent Manager also enforces limits).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/kkjorsvik/kyvik/internal/ktp"
	"github.com/kkjorsvik/kyvik/internal/ktp/testtools"
	"github.com/kkjorsvik/kyvik/internal/sandbox"
	"github.com/kkjorsvik/kyvik/internal/tools"
)

func main() {
	// 1. Apply resource limits (defense-in-depth, Linux only).
	//    Read parent-provided memory limit from env var, falling back to compiled default.
	cfg := sandbox.DefaultSandboxConfig()
	if envMem := os.Getenv("KYVIK_SANDBOX_MAX_MEMORY_MB"); envMem != "" {
		if n, err := strconv.Atoi(envMem); err == nil && n > 0 {
			cfg.MaxMemoryMB = n
		}
	}
	// Set GOMEMLIMIT so the GC keeps heap usage under the configured budget.
	// This is a no-op if the parent already set the GOMEMLIMIT env var (same value),
	// but covers manual invocation of kyvik-sandbox without the parent Manager.
	debug.SetMemoryLimit(int64(cfg.MaxMemoryMB) * 1024 * 1024)

	if err := sandbox.ApplyRLimits(cfg); err != nil {
		slog.Debug("failed to apply rlimits", "error", err)
	}

	// 1b. Configure HTTP proxy authentication if proxy is available.
	// The parent Manager injects KYVIK_PROXY_SANDBOX_ID when the network proxy
	// is enabled. We wrap the default transport to inject the Proxy-Authorization
	// header on every outbound request so the proxy can identify this sandbox.
	if sandboxID := os.Getenv("KYVIK_PROXY_SANDBOX_ID"); sandboxID != "" {
		http.DefaultTransport = &proxyAuthTransport{
			base:      http.DefaultTransport,
			sandboxID: sandboxID,
		}
	}

	// 2. Read all of stdin.
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		writeError("", fmt.Sprintf("failed to read stdin: %s", err))
		os.Exit(1)
	}

	// 3. Unmarshal as ToolRequest.
	var req ktp.ToolRequest
	if err := json.Unmarshal(input, &req); err != nil {
		writeError("", fmt.Sprintf("invalid tool request JSON: %s", err))
		os.Exit(1)
	}

	// 4. Build registry with built-in tools.
	registry := ktp.NewRegistry()

	// Register echo tool for testing.
	if err := registry.Register(&testtools.EchoTool{}); err != nil {
		writeError(req.ID, fmt.Sprintf("failed to register echo tool: %s", err))
		os.Exit(1)
	}

	// Register file, HTTP, shell, and code tools (memory is in-process only).
	workspace := os.Getenv("KYVIK_WORKSPACE")
	var allowedHostsEnv string
	if v := os.Getenv("KYVIK_HTTP_ALLOWED_HOSTS"); v != "" {
		allowedHostsEnv = v
	}
	var shellAllowedCmdsEnv string
	if v := os.Getenv("KYVIK_SHELL_ALLOWED_COMMANDS"); v != "" {
		shellAllowedCmdsEnv = v
	}

	// Skill-level path restrictions (set by sandbox manager from SkillSandboxConfig).
	var skillReadPaths, skillWritePaths []string
	if v := os.Getenv("KYVIK_SKILL_READ_PATHS"); v != "" {
		skillReadPaths = strings.Split(v, ",")
	}
	if v := os.Getenv("KYVIK_SKILL_WRITE_PATHS"); v != "" {
		skillWritePaths = strings.Split(v, ",")
	}

	// Set up secret resolver: prefer Unix socket, fall back to env vars.
	var secretResolver tools.SandboxSecretResolver
	if socketPath := os.Getenv("KYVIK_SECRETS_SOCKET"); socketPath != "" {
		secretResolver = func(key string) (string, error) {
			return resolveSecretViaSocket(socketPath, key)
		}
	} else {
		// Backward compat: fall back to env var lookup.
		secretResolver = func(key string) (string, error) {
			envKey := "KYVIK_SECRET_" + strings.ToUpper(key)
			if v, ok := os.LookupEnv(envKey); ok {
				return v, nil
			}
			return "", fmt.Errorf("secret not found: %s", key)
		}
	}

	toolOpts := tools.RegistrationOptions{
		WorkspaceFunc: func(agentID string) (string, error) {
			if workspace == "" {
				return "", fmt.Errorf("KYVIK_WORKSPACE not set")
			}
			return workspace, nil
		},
		AllowedHostsFunc: func(agentID string) ([]string, error) {
			if allowedHostsEnv == "" {
				return nil, nil
			}
			return strings.Split(allowedHostsEnv, ","), nil
		},
		AllowedCommandsFunc: func(agentID string) ([]string, error) {
			if shellAllowedCmdsEnv == "" {
				return nil, nil
			}
			return strings.Split(shellAllowedCmdsEnv, ","), nil
		},
		AgentTierFunc: func(agentID string) (string, error) {
			// Read tier from the request (set by executor permission gate).
			if req.Tier != "" && ktp.TierLevel(req.Tier) >= 0 {
				return req.Tier, nil
			}
			// Fall back to environment variable, then "admin".
			if envTier := os.Getenv("DEFAULT_AGENT_TIER"); envTier != "" && ktp.TierLevel(envTier) >= 0 {
				return envTier, nil
			}
			return "admin", nil
		},
		SkillReadPaths:        skillReadPaths,
		SkillWritePaths:       skillWritePaths,
		SandboxSecretResolver: secretResolver,
		// MemoryStore: nil — memory tool is not available in sandbox
	}
	if err := tools.RegisterBuiltinTools(registry, toolOpts); err != nil {
		writeError(req.ID, fmt.Sprintf("failed to register built-in tools: %s", err))
		os.Exit(1)
	}

	// 5. Lookup tool.
	tool, ok := registry.Get(req.Tool)
	if !ok {
		writeError(req.ID, fmt.Sprintf("unknown tool: %s", req.Tool))
		os.Exit(0) // tool-level error, not infrastructure
	}

	// 6. Execute tool.
	ctx := context.Background()
	start := time.Now()
	resp, execErr := tool.Execute(ctx, req)
	execMs := time.Since(start).Milliseconds()

	if execErr != nil {
		writeError(req.ID, execErr.Error())
		os.Exit(0)
	}

	if resp == nil {
		writeError(req.ID, "tool returned nil response")
		os.Exit(0)
	}

	resp.ExecutionMs = execMs
	resp.SandboxID = os.Getenv("KYVIK_SANDBOX_ID")

	// 7. Write response to stdout.
	if err := json.NewEncoder(os.Stdout).Encode(resp); err != nil {
		fmt.Fprintf(os.Stderr, "failed to write response: %s\n", err)
		os.Exit(1)
	}
}

// writeError writes an error ToolResponse to stdout.
func writeError(reqID, msg string) {
	resp := ktp.ToolResponse{
		RequestID: reqID,
		Success:   false,
		Error:     msg,
		Timestamp: time.Now(),
	}
	_ = json.NewEncoder(os.Stdout).Encode(resp)
}

// proxyAuthTransport wraps an http.RoundTripper to inject the Proxy-Authorization
// header on every outbound request. This lets the network proxy identify which
// sandbox is making the request for tier-based policy enforcement.
type proxyAuthTransport struct {
	base      http.RoundTripper
	sandboxID string
}

func (t *proxyAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Proxy-Authorization", t.sandboxID)
	return t.base.RoundTrip(req)
}

// resolveSecretViaSocket connects to the secrets Unix socket, sends a request
// for the given key, and returns the secret value.
func resolveSecretViaSocket(socketPath, key string) (string, error) {
	conn, err := net.DialTimeout("unix", socketPath, 5*time.Second)
	if err != nil {
		return "", fmt.Errorf("connect to secrets socket: %w", err)
	}
	defer conn.Close()

	// Set deadline for the entire exchange.
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return "", fmt.Errorf("set socket deadline: %w", err)
	}

	// Send request.
	req := sandbox.SecretsRequest{Key: key}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return "", fmt.Errorf("send secrets request: %w", err)
	}

	// Read response.
	var resp sandbox.SecretsResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return "", fmt.Errorf("read secrets response: %w", err)
	}

	if resp.Error != "" {
		return "", fmt.Errorf("secrets server: %s", resp.Error)
	}

	return resp.Value, nil
}
