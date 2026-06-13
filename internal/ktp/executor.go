package ktp

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

// SandboxExecutor is the interface for executing tool requests in a sandbox.
// Implemented by sandbox.KTPAdapter; defined here to avoid circular imports.
type SandboxExecutor interface {
	GetOrCreateSandbox(agentID string, tierOverrides map[string]any) (SandboxInfo, error)
	ExecuteInSandbox(ctx context.Context, sandboxID string, req ToolRequest) (*ToolResponse, error)
	SetSandboxSecrets(sandboxID string, secrets map[string]string)
}

// SandboxInfo identifies a sandbox instance.
type SandboxInfo struct {
	ID      string
	AgentID string
}

// SecretResolver resolves secrets by cascading scope (agent → team → global).
// *secrets.Vault satisfies this interface implicitly.
type SecretResolver interface {
	Resolve(ctx context.Context, agentID, teamID, key string) (string, error)
}

// ExecutorConfig controls executor behavior.
type ExecutorConfig struct {
	DefaultTimeout time.Duration // default 30s
	MaxConcurrent  int           // default 50
}

// Executor orchestrates KTP tool execution: lookup, validation, permission
// checking, concurrency limiting, timeout/panic safety, and audit logging.
type Executor struct {
	registry *Registry
	gate     *PermissionGate
	audit    AuditLogger
	timeout  time.Duration
	sem      chan struct{}
	sandbox  SandboxExecutor
	secrets  SecretResolver
}

// NewExecutor creates an Executor with the given dependencies and config.
// Zero-value config fields get sensible defaults (30s timeout, 50 max concurrent).
func NewExecutor(registry *Registry, gate *PermissionGate, audit AuditLogger, cfg ExecutorConfig) *Executor {
	timeout := cfg.DefaultTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	maxConcurrent := cfg.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = 50
	}
	return &Executor{
		registry: registry,
		gate:     gate,
		audit:    audit,
		timeout:  timeout,
		sem:      make(chan struct{}, maxConcurrent),
	}
}

// SetSandbox sets the sandbox executor for tool isolation.
func (e *Executor) SetSandbox(sb SandboxExecutor) {
	e.sandbox = sb
}

// SetSecretResolver sets the secret resolver for tool secret injection.
func (e *Executor) SetSecretResolver(sr SecretResolver) {
	e.secrets = sr
}

// Execute runs the full KTP tool execution pipeline:
// lookup → validate → permission check → semaphore → execute → audit.
//
// Tool-level errors (unknown tool, validation failure, permission denied, tool
// panics) are returned as *ToolResponse with Success=false. The error return
// is reserved for infrastructure failures (e.g. store unavailable).
func (e *Executor) Execute(ctx context.Context, req ToolRequest) (*ToolResponse, error) {
	// 1. Lookup tool.
	tool, ok := e.registry.Get(req.Tool)
	if !ok {
		slog.Warn("ktp: unknown tool requested", "tool", req.Tool, "agent_id", req.AgentID)
		return errorResponse(req.ID, fmt.Sprintf("unknown tool: %s", req.Tool), 0), nil
	}
	decl := tool.Declaration()

	// 2. Lookup action.
	action, ok := decl.GetAction(req.Action)
	if !ok {
		slog.Warn("ktp: unknown action requested", "tool", req.Tool, "action", req.Action, "agent_id", req.AgentID)
		return errorResponse(req.ID, fmt.Sprintf("unknown action: %s.%s", req.Tool, req.Action), 0), nil
	}

	// 3. Validate parameters.
	if errs := ValidateParams(req.Parameters, action.Parameters); len(errs) > 0 {
		msgs := make([]string, len(errs))
		for i, e := range errs {
			if e.Field != "" {
				msgs[i] = fmt.Sprintf("%s: %s", e.Field, e.Message)
			} else {
				msgs[i] = e.Message
			}
		}
		reason := strings.Join(msgs, "; ")
		slog.Warn("ktp: parameter validation failed", "tool", req.Tool, "action", req.Action, "agent_id", req.AgentID, "errors", reason)
		return errorResponse(req.ID, "validation failed: "+reason, 0), nil
	}

	// 4. Permission check.
	permResult, err := e.gate.Check(ctx, req.AgentID, decl, req.Action, req.Parameters)
	if err != nil {
		return nil, err
	}
	if !permResult.Allowed {
		slog.Warn("ktp: permission denied", "tool", req.Tool, "action", req.Action, "agent_id", req.AgentID, "tier", permResult.Tier, "reason", permResult.Reason)
		return errorResponse(req.ID, permResult.Reason, 0), nil
	}
	req.PermissionToken = permResult.Token
	req.Tier = permResult.Tier

	// 5. Acquire semaphore.
	select {
	case e.sem <- struct{}{}:
	case <-ctx.Done():
		slog.Warn("ktp: execution queue full", "tool", req.Tool, "action", req.Action, "agent_id", req.AgentID)
		return errorResponse(req.ID, "execution queue full: context deadline exceeded", 0), nil
	}

	// 6. Execute tool with safety boundaries.
	resp, err := func() (*ToolResponse, error) {
		defer func() { <-e.sem }()
		return e.executeWithSafety(ctx, tool, req, e.timeout, permResult.Tier)
	}()
	if err != nil {
		return nil, err
	}

	// 7. Validate result (warnings only).
	if resultMap, ok := resp.Result.(map[string]any); ok {
		ValidateResult(resultMap, action.Returns)
	}

	// 8. Audit log (best-effort).
	if e.audit != nil {
		if err := e.audit.LogToolExecution(ctx, req, resp); err != nil {
			slog.Warn("failed to audit log tool execution",
				"tool", req.Tool,
				"action", req.Action,
				"error", err,
			)
		}
	}

	// 9. Return response.
	return resp, nil
}

// executeWithSafety wraps tool execution with panic recovery and a timeout context.
// If a sandbox is configured and the tool is not inline, execution is delegated
// to the sandbox. Otherwise, the tool runs in-process.
func (e *Executor) executeWithSafety(ctx context.Context, tool Tool, req ToolRequest, timeout time.Duration, tier string) (resp *ToolResponse, err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("tool panicked",
				"tool", req.Tool,
				"action", req.Action,
				"panic", r,
			)
			resp = errorResponse(req.ID, fmt.Sprintf("tool panicked: %v", r), 0)
			err = nil
		}
	}()

	// Route to sandbox if configured and tool is not inline.
	if e.sandbox != nil && !isInlineTool(tool) {
		return e.executeSandboxed(ctx, tool, req, tier)
	}

	// In-process execution (inline tool or no sandbox configured).
	// Resolve secrets and inject as env vars for inline tools that need them.
	decl := tool.Declaration()
	var secretEnvKeys []string
	if len(decl.RequiredSecrets) > 0 && e.secrets != nil {
		for _, key := range decl.RequiredSecrets {
			val, err := e.secrets.Resolve(ctx, req.AgentID, req.TeamID, key)
			if err != nil {
				return errorResponse(req.ID, fmt.Sprintf(
					"failed to resolve required secret %q: %v", key, err), 0), nil
			}
			// Set as KYVIK_SECRET_<KEY> env var (normalized: non-alnum → underscore).
			envKey := secretKeyToEnvVar(key)
			os.Setenv(envKey, val)
			secretEnvKeys = append(secretEnvKeys, envKey)
		}
	}
	// Clean up secret env vars after execution.
	defer func() {
		for _, envKey := range secretEnvKeys {
			os.Unsetenv(envKey)
		}
	}()

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	resp, err = tool.Execute(ctx, req)
	execMs := time.Since(start).Milliseconds()

	if err != nil {
		return errorResponse(req.ID, err.Error(), execMs), nil
	}

	if resp == nil {
		return errorResponse(req.ID, "tool returned nil response", execMs), nil
	}

	resp.ExecutionMs = execMs
	return resp, nil
}

// executeSandboxed runs a tool request in an isolated sandbox process.
func (e *Executor) executeSandboxed(ctx context.Context, tool Tool, req ToolRequest, tier string) (*ToolResponse, error) {
	decl := tool.Declaration()

	// 1. Build tier overrides.
	overrides := tierToSandboxOverrides(tier, decl)

	// 1b. Apply skill sandbox constraints (intersection semantics: can only restrict).
	applySkillConstraints(overrides, req.SkillSandboxConfig)

	// 2. Get or create sandbox.
	sbInfo, err := e.sandbox.GetOrCreateSandbox(req.AgentID, overrides)
	if err != nil {
		return errorResponse(req.ID, fmt.Sprintf("sandbox creation failed: %v", err), 0), nil
	}

	// 3. Resolve and inject secrets.
	// Secrets are strictly per-call: clear before and after every execution.
	e.sandbox.SetSandboxSecrets(sbInfo.ID, nil)
	defer e.sandbox.SetSandboxSecrets(sbInfo.ID, nil)

	resolvedSecrets := make(map[string]string)
	if len(decl.RequiredSecrets) > 0 {
		if e.secrets == nil {
			return errorResponse(req.ID, fmt.Sprintf(
				"secret resolver not configured but tool requires secrets: %v", decl.RequiredSecrets), 0), nil
		}
		for _, key := range decl.RequiredSecrets {
			val, err := e.secrets.Resolve(ctx, req.AgentID, req.TeamID, key)
			if err != nil {
				return errorResponse(req.ID, fmt.Sprintf(
					"failed to resolve required secret %q: %v", key, err), 0), nil
			}
			resolvedSecrets[key] = val
		}
	}
	if len(resolvedSecrets) > 0 {
		e.sandbox.SetSandboxSecrets(sbInfo.ID, resolvedSecrets)
	}

	// 4. Execute in sandbox.
	resp, err := e.sandbox.ExecuteInSandbox(ctx, sbInfo.ID, req)
	if err != nil {
		return errorResponse(req.ID, fmt.Sprintf("sandbox execution failed: %v", err), 0), nil
	}

	// 5. Redact secrets from response.
	if len(resolvedSecrets) > 0 {
		redactSecretsFromResponse(resp, resolvedSecrets)
	}

	// 6. Tag response with sandbox ID.
	resp.SandboxID = sbInfo.ID
	return resp, nil
}

// secretKeyToEnvVar converts a secret key like "weather:api_key" to "KYVIK_SECRET_WEATHER_API_KEY".
func secretKeyToEnvVar(key string) string {
	var b strings.Builder
	b.WriteString("KYVIK_SECRET_")
	for _, r := range key {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return strings.ToUpper(b.String())
}

// isInlineTool returns true if the tool implements InlineTool and returns true from Inline().
func isInlineTool(tool Tool) bool {
	if it, ok := tool.(InlineTool); ok {
		return it.Inline()
	}
	return false
}

// tierToSandboxOverrides builds a sandbox overrides map from the tier and tool declaration.
func tierToSandboxOverrides(tier string, decl ToolDeclaration) map[string]any {
	overrides := map[string]any{}

	switch tier {
	case TierReader:
		overrides["max_memory_mb"] = 256
		overrides["timeout_seconds"] = 10
		overrides["allow_network"] = hasNetworkCapability(decl)
	case TierWriter:
		overrides["max_memory_mb"] = 512
		overrides["timeout_seconds"] = 30
		overrides["allow_network"] = hasNetworkCapability(decl)
	case TierOperator:
		overrides["allow_network"] = hasNetworkCapability(decl)
	case TierAdmin:
		overrides["max_memory_mb"] = 1024
		overrides["timeout_seconds"] = 120
		overrides["allow_network"] = hasNetworkCapability(decl)
	default:
		// Unknown tier → strictest.
		overrides["max_memory_mb"] = 256
		overrides["timeout_seconds"] = 10
		overrides["allow_network"] = false
	}

	return overrides
}

// hasNetworkCapability returns true if the tool declares a network capability.
func hasNetworkCapability(decl ToolDeclaration) bool {
	for _, cap := range decl.Capabilities {
		if cap.Type == "network" {
			return true
		}
	}
	return false
}

// redactSecretsFromResponse replaces secret values with [REDACTED] in the response.
func redactSecretsFromResponse(resp *ToolResponse, secrets map[string]string) {
	if resp == nil {
		return
	}

	// Redact from error string.
	if resp.Error != "" {
		for _, v := range secrets {
			if v != "" {
				resp.Error = strings.ReplaceAll(resp.Error, v, "[REDACTED]")
			}
		}
	}

	// Redact from result.
	resp.Result = redactValue(resp.Result, secrets)
}

// redactValue recursively replaces secret values with [REDACTED] in any value.
func redactValue(val any, secrets map[string]string) any {
	switch v := val.(type) {
	case string:
		for _, secret := range secrets {
			if secret != "" {
				v = strings.ReplaceAll(v, secret, "[REDACTED]")
			}
		}
		return v
	case map[string]any:
		for key, mapVal := range v {
			v[key] = redactValue(mapVal, secrets)
		}
		return v
	case map[string]string:
		for key, mapVal := range v {
			for _, secret := range secrets {
				if secret != "" {
					mapVal = strings.ReplaceAll(mapVal, secret, "[REDACTED]")
				}
			}
			v[key] = mapVal
		}
		return v
	case []any:
		for i, elem := range v {
			v[i] = redactValue(elem, secrets)
		}
		return v
	case []string:
		for i, elem := range v {
			for _, secret := range secrets {
				if secret != "" {
					elem = strings.ReplaceAll(elem, secret, "[REDACTED]")
				}
			}
			v[i] = elem
		}
		return v
	default:
		return val
	}
}

// applySkillConstraints applies intersection semantics from a SkillSandboxConfig to
// the tier overrides map. Skills can only restrict agent capabilities, never expand them.
//
// - AllowNetwork: if the skill says false, override to false (never enable network).
// - AllowedHosts: stored in overrides for later intersection in buildEnvironment.
// - ReadPaths / WritePaths: stored in overrides for injection as env vars.
func applySkillConstraints(overrides map[string]any, skill *types.SkillSandboxConfig) {
	if skill == nil {
		return
	}

	// Network: skill can only disable, never enable.
	if !skill.AllowNetwork {
		overrides["allow_network"] = false
	}

	// Allowed hosts: pass through for intersection in buildEnvironment.
	if len(skill.AllowedHosts) > 0 {
		overrides["skill_allowed_hosts"] = skill.AllowedHosts
	}

	// Path restrictions: pass through for env var injection.
	if len(skill.ReadPaths) > 0 {
		overrides["skill_read_paths"] = skill.ReadPaths
	}
	if len(skill.WritePaths) > 0 {
		overrides["skill_write_paths"] = skill.WritePaths
	}
}

// errorResponse builds a failed ToolResponse with the given message.
func errorResponse(reqID string, msg string, execMs int64) *ToolResponse {
	return &ToolResponse{
		RequestID:   reqID,
		Success:     false,
		Error:       msg,
		ExecutionMs: execMs,
		Timestamp:   time.Now(),
	}
}
