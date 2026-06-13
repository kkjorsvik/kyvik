package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kkjorsvik/kyvik/internal/ktp"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// Manager manages sandbox lifecycles for agents.
type Manager struct {
	config ManagerConfig
	active map[string]*Sandbox // agentID → *Sandbox
	mu     sync.Mutex
	runner string // resolved path to kyvik-sandbox binary
}

// NewManager creates a Manager, resolving the runner binary and creating the workspace root.
func NewManager(cfg ManagerConfig) (*Manager, error) {
	if cfg.Defaults == (SandboxConfig{}) {
		cfg.Defaults = DefaultSandboxConfig()
	}

	runner, err := resolveRunner(cfg.RunnerPath)
	if err != nil {
		return nil, fmt.Errorf("resolve sandbox runner: %w", err)
	}

	if cfg.WorkspaceRoot != "" {
		if err := os.MkdirAll(cfg.WorkspaceRoot, 0o755); err != nil {
			return nil, fmt.Errorf("create workspace root: %w", err)
		}
	}

	return &Manager{
		config: cfg,
		active: make(map[string]*Sandbox),
		runner: runner,
	}, nil
}

// Create creates a sandbox for the given agent. Returns the existing sandbox
// if one is already active. Creates workspace subdirectories: data/, tmp/, output/.
func (m *Manager) Create(agentID string, tierOverrides map[string]any) (*Sandbox, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Return existing sandbox if already active.
	if sb, ok := m.active[agentID]; ok {
		return sb, nil
	}

	// Build config from defaults + tier overrides.
	cfg := m.config.Defaults
	if tierOverrides != nil {
		if v, ok := tierOverrides["allow_network"]; ok {
			if b, ok := v.(bool); ok {
				cfg.AllowNetwork = b
			}
		}
		if v, ok := tierOverrides["max_memory_mb"]; ok {
			if n, ok := toInt(v); ok {
				cfg.MaxMemoryMB = n
			}
		}
		if v, ok := tierOverrides["timeout_seconds"]; ok {
			if n, ok := toInt(v); ok {
				cfg.TimeoutSeconds = n
			}
		}
	}

	// Validate agentID to prevent path traversal.
	if agentID == "" || agentID == "." || agentID == ".." ||
		strings.ContainsAny(agentID, "/\\\x00") {
		return nil, fmt.Errorf("invalid agent ID: %q", agentID)
	}
	// Verify resolved path stays under WorkspaceRoot.
	workspace := filepath.Join(m.config.WorkspaceRoot, agentID)
	if rel, err := filepath.Rel(m.config.WorkspaceRoot, workspace); err != nil || strings.HasPrefix(rel, "..") {
		return nil, fmt.Errorf("agent ID resolves outside workspace root: %q", agentID)
	}

	// Create workspace directories.
	for _, sub := range []string{"data", "tmp", "output"} {
		dir := filepath.Join(workspace, sub)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create workspace dir %s: %w", dir, err)
		}
	}

	sb := NewSandbox(agentID, cfg, workspace)
	m.active[agentID] = sb
	return sb, nil
}

// Execute runs a tool request inside the sandbox by spawning the kyvik-sandbox binary.
// Protocol: JSON ToolRequest on stdin → JSON ToolResponse on stdout.
func (m *Manager) Execute(ctx context.Context, sb *Sandbox, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	// Marshal the request.
	reqJSON, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal tool request: %w", err)
	}

	// Start secrets server if the sandbox has secrets.
	var secretsSocketPath string
	if len(sb.Secrets) > 0 {
		srv := NewSecretsServer(sb.Workspace, sb.Secrets)
		if err := srv.Start(); err != nil {
			return nil, fmt.Errorf("start secrets server: %w", err)
		}
		defer srv.Close()
		secretsSocketPath = srv.SocketPath()
	}

	// Create timeout context.
	execCtx, cancel := context.WithTimeout(ctx, sb.Config.Timeout())
	defer cancel()

	cmd := exec.CommandContext(execCtx, m.runner)
	cmd.Dir = sb.Workspace
	cmd.Env = buildEnvironment(sb, req.SkillSandboxConfig, secretsSocketPath)

	// Network proxy: sandbox processes route HTTP through the tier-aware proxy.
	if m.config.ProxyAddr != "" && sb.Config.AllowNetwork {
		proxyURL := "http://" + m.config.ProxyAddr
		cmd.Env = append(cmd.Env,
			"HTTP_PROXY="+proxyURL,
			"HTTPS_PROXY="+proxyURL,
			"KYVIK_PROXY_SANDBOX_ID="+sb.ID,
		)
	}

	cmd.Stdin = bytes.NewReader(reqJSON)

	// Capture stdout/stderr with size limits.
	stdout := &limitedWriter{limit: sb.Config.MaxOutputBytes}
	stderr := &limitedWriter{limit: sb.Config.MaxOutputBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	// Set process group isolation (Linux only).
	configureSysProcAttr(cmd)

	start := time.Now()
	err = cmd.Start()
	if err != nil {
		return makeErrorResponse(req.ID, fmt.Sprintf("failed to start sandbox: %s", err), time.Since(start).Milliseconds()), nil
	}
	slog.Debug("sandbox process started", "agent_id", sb.AgentID, "sandbox_id", sb.ID, "tool", req.Tool, "pid", cmd.Process.Pid)

	// Wait for completion.
	waitErr := cmd.Wait()
	execMs := time.Since(start).Milliseconds()

	if isLikelyOOM(waitErr) {
		slog.Error("sandbox process killed by SIGKILL (possible OOM or RLIMIT_AS)",
			"agent_id", sb.AgentID, "sandbox_id", sb.ID,
			"tool", req.Tool, "action", req.Action,
			"exec_ms", execMs, "max_memory_mb", sb.Config.MaxMemoryMB,
			"hint", "consider increasing MaxMemoryMB for this tier",
		)
	}

	// On timeout, kill the process group.
	if execCtx.Err() == context.DeadlineExceeded {
		if cmd.Process != nil {
			killProcessGroup(cmd.Process.Pid)
		}
		return makeErrorResponse(req.ID, "sandbox execution timed out", execMs), nil
	}

	// Parse stdout as ToolResponse.
	outBytes := stdout.Bytes()
	if len(outBytes) > 0 {
		var resp ktp.ToolResponse
		if jsonErr := json.Unmarshal(outBytes, &resp); jsonErr == nil {
			resp.ExecutionMs = execMs
			resp.SandboxID = sb.ID
			slog.Debug("sandbox process completed", "agent_id", sb.AgentID, "sandbox_id", sb.ID, "tool", req.Tool, "success", resp.Success, "exec_ms", execMs)
			return &resp, nil
		}
		// Log metadata only — never log raw output (may contain secrets).
		preview := string(outBytes)
		if len(preview) > 200 {
			preview = preview[:200]
		}
		preview = strings.ReplaceAll(preview, "\n", " ")
		for _, v := range sb.Secrets {
			if v != "" {
				preview = strings.ReplaceAll(preview, v, "[REDACTED]")
			}
		}
		slog.Warn("sandbox stdout not valid JSON",
			"agent_id", sb.AgentID,
			"sandbox_id", sb.ID,
			"exec_ms", execMs,
			"output_len", len(outBytes),
			"preview", preview,
		)
	}

	// Process exited with error or unparseable output.
	errMsg := "sandbox process failed"
	if waitErr != nil {
		errMsg = fmt.Sprintf("sandbox process failed: %s", waitErr)
	}
	if isLikelyOOM(waitErr) {
		errMsg += fmt.Sprintf(" [likely OOM: memory limit %dMB, RLIMIT_AS %dMB]", sb.Config.MaxMemoryMB, sb.Config.MaxMemoryMB*4)
	}
	if stderrOut := stderr.String(); stderrOut != "" {
		errMsg += ": " + stderrOut
	}
	return makeErrorResponse(req.ID, errMsg, execMs), nil
}

// Cleanup removes a sandbox's workspace and removes it from the active map.
// The lock is held across the entire operation to prevent a concurrent Create
// from racing with the directory removal.
func (m *Manager) Cleanup(sb *Sandbox) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.active, sb.AgentID)
	if err := os.RemoveAll(sb.Workspace); err != nil {
		return fmt.Errorf("remove workspace: %w", err)
	}
	return nil
}

// Get returns the active sandbox for an agent, if any.
func (m *Manager) Get(agentID string) (*Sandbox, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sb, ok := m.active[agentID]
	return sb, ok
}

// GetBySandboxID returns the sandbox with the given ID.
func (m *Manager) GetBySandboxID(sandboxID string) (*Sandbox, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, sb := range m.active {
		if sb.ID == sandboxID {
			return sb, true
		}
	}
	return nil, false
}

// SetProxyAddr sets the network proxy address for sandbox processes.
func (m *Manager) SetProxyAddr(addr string) {
	m.config.ProxyAddr = addr
}

// RunnerPath returns the resolved path to the kyvik-sandbox binary.
func (m *Manager) RunnerPath() string {
	return m.runner
}

// resolveRunner finds the kyvik-sandbox binary:
// 1. Explicit config path (if non-empty)
// 2. Same directory as the current executable
// 3. PATH lookup
func resolveRunner(configPath string) (string, error) {
	if configPath != "" {
		if _, err := os.Stat(configPath); err == nil {
			return configPath, nil
		}
		return "", fmt.Errorf("configured runner path not found: %s", configPath)
	}

	// Try same directory as current executable.
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "kyvik-sandbox")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}

	// Fall back to PATH.
	if path, err := exec.LookPath("kyvik-sandbox"); err == nil {
		return path, nil
	}

	return "", fmt.Errorf("kyvik-sandbox binary not found (set sandbox.runner_path in config or add to PATH)")
}

// buildEnvironment creates a minimal, whitelisted environment for the sandbox process.
// If a skill sandbox config is provided, it applies intersection semantics for allowed
// hosts (skill can only restrict, never expand) and passes read/write path restrictions.
// If secretsSocketPath is non-empty, it is passed as KYVIK_SECRETS_SOCKET instead of
// individual KYVIK_SECRET_* env vars (secrets served via Unix socket for security).
func buildEnvironment(sb *Sandbox, skillCfg *types.SkillSandboxConfig, secretsSocketPath ...string) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + sb.Workspace,
		"TMPDIR=" + filepath.Join(sb.Workspace, "tmp"),
		"KYVIK_SANDBOX_ID=" + sb.ID,
		"KYVIK_AGENT_ID=" + sb.AgentID,
		"KYVIK_WORKSPACE=" + sb.Workspace,
		fmt.Sprintf("KYVIK_SANDBOX_MAX_MEMORY_MB=%d", sb.Config.MaxMemoryMB),
		fmt.Sprintf("GOMEMLIMIT=%dMiB", sb.Config.MaxMemoryMB),
		"GODEBUG=netdns=go",
	}

	// Compute effective allowed hosts: intersection of agent and skill lists.
	effectiveHosts := sb.HTTPAllowedHosts
	if skillCfg != nil && len(skillCfg.AllowedHosts) > 0 {
		effectiveHosts = intersectHosts(sb.HTTPAllowedHosts, skillCfg.AllowedHosts)
	}
	if len(effectiveHosts) > 0 {
		env = append(env, "KYVIK_HTTP_ALLOWED_HOSTS="+strings.Join(effectiveHosts, ","))
	}

	if len(sb.ShellAllowedCommands) > 0 {
		env = append(env, "KYVIK_SHELL_ALLOWED_COMMANDS="+strings.Join(sb.ShellAllowedCommands, ","))
	}

	// Skill path restrictions.
	if skillCfg != nil {
		if len(skillCfg.ReadPaths) > 0 {
			env = append(env, "KYVIK_SKILL_READ_PATHS="+strings.Join(skillCfg.ReadPaths, ","))
		}
		if len(skillCfg.WritePaths) > 0 {
			env = append(env, "KYVIK_SKILL_WRITE_PATHS="+strings.Join(skillCfg.WritePaths, ","))
		}
	}

	// Secrets: prefer Unix socket (avoids /proc/*/environ exposure).
	socketPath := ""
	if len(secretsSocketPath) > 0 {
		socketPath = secretsSocketPath[0]
	}
	if socketPath != "" {
		env = append(env, "KYVIK_SECRETS_SOCKET="+socketPath)
	} else {
		// Fallback: inject as env vars (backward compat for tests or when no secrets).
		for k, v := range sb.Secrets {
			env = append(env, "KYVIK_SECRET_"+strings.ToUpper(k)+"="+v)
		}
	}
	return env
}

// intersectHosts computes the intersection of two host lists.
// If agentHosts is empty, it is treated as "all allowed" and the skill list is returned directly.
// If skillHosts is empty, the agent list is returned unchanged.
// Otherwise, only hosts present in both lists are returned.
func intersectHosts(agentHosts, skillHosts []string) []string {
	if len(agentHosts) == 0 {
		// Agent has no restrictions → use skill list as-is.
		return skillHosts
	}
	if len(skillHosts) == 0 {
		// Skill has no restrictions → use agent list as-is.
		return agentHosts
	}

	// Build a set from agent hosts for O(n) lookup.
	agentSet := make(map[string]struct{}, len(agentHosts))
	for _, h := range agentHosts {
		agentSet[strings.TrimSpace(h)] = struct{}{}
	}

	var result []string
	for _, h := range skillHosts {
		h = strings.TrimSpace(h)
		if _, ok := agentSet[h]; ok {
			result = append(result, h)
		}
	}
	return result
}

// makeErrorResponse builds a failed ToolResponse.
func makeErrorResponse(reqID string, msg string, execMs int64) *ktp.ToolResponse {
	return &ktp.ToolResponse{
		RequestID:   reqID,
		Success:     false,
		Error:       msg,
		ExecutionMs: execMs,
		Timestamp:   time.Now(),
	}
}

// limitedWriter wraps a buffer and silently discards writes beyond the limit.
type limitedWriter struct {
	buf   bytes.Buffer
	limit int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	remaining := w.limit - w.buf.Len()
	if remaining <= 0 {
		return len(p), nil // silently discard
	}
	if len(p) > remaining {
		p = p[:remaining]
	}
	return w.buf.Write(p)
}

func (w *limitedWriter) Bytes() []byte  { return w.buf.Bytes() }
func (w *limitedWriter) String() string { return w.buf.String() }

// Ensure limitedWriter implements io.Writer.
var _ io.Writer = (*limitedWriter)(nil)

// isLikelyOOM returns true if the error indicates the process was killed by
// SIGKILL, which is the typical signal for OOM kills and RLIMIT_AS violations.
func isLikelyOOM(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "signal: killed")
}

// toInt converts an interface{} to int, handling float64 and int.
func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case float64:
		return int(n), true
	case int64:
		return int(n), true
	}
	return 0, false
}
