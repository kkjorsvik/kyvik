// Package shell implements a KTP tool for executing shell commands within an
// agent's sandbox. Commands are executed directly (no shell interpretation) with
// deny-by-default security: blocked commands, optional allowlists, and workspace
// confinement.
package shell

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/kkjorsvik/kyvik/internal/ktp"
	"github.com/kkjorsvik/kyvik/internal/tools/executil"
)

// AllowedCommandsFunc returns the list of allowed commands for an agent.
// An empty or nil slice means no commands are allowed (deny-by-default).
type AllowedCommandsFunc func(agentID string) ([]string, error)

// WorkspaceFunc resolves an agent's workspace directory.
type WorkspaceFunc func(agentID string) (string, error)

// AgentTierFunc resolves an agent's KTP tier (e.g. "admin", "operator").
type AgentTierFunc func(agentID string) (string, error)

const (
	defaultTimeout = 30 * time.Second
	maxTimeout     = 300 * time.Second
)

// blockedCommands are never allowed, regardless of allowlist.
var blockedCommands = map[string]bool{
	"mkfs":        true,
	"shutdown":    true,
	"reboot":      true,
	"poweroff":    true,
	"halt":        true,
	"init":        true,
	"systemctl":   true,
	"fdisk":       true,
	"parted":      true,
	"cryptsetup":  true,
	"iptables":    true,
	"ip6tables":   true,
	"nftables":    true,
	"chroot":      true,
	"mount":       true,
	"umount":      true,
	"swapon":      true,
	"swapoff":     true,
	"insmod":      true,
	"rmmod":       true,
	"modprobe":    true,
}

// blockedArgPatterns are substring patterns that are never allowed
// in the combined command+args string.
var blockedArgPatterns = []string{
	"rm -rf /",
	"rm -rf /*",
	"dd if=/dev/zero",
	"dd if=/dev/random",
}

// protectedEnvKeys are environment variable names that cannot be overridden via the env parameter.
var protectedEnvKeys = map[string]bool{
	"PATH": true, "HOME": true, "TMPDIR": true, "USER": true,
}

// ShellTool implements ktp.Tool for executing shell commands.
type ShellTool struct {
	allowedCmds AllowedCommandsFunc
	workspace   WorkspaceFunc
	tier        AgentTierFunc
}

// New creates a ShellTool with the given callbacks.
func New(allowedCmds AllowedCommandsFunc, workspace WorkspaceFunc, tier AgentTierFunc) *ShellTool {
	return &ShellTool{
		allowedCmds: allowedCmds,
		workspace:   workspace,
		tier:        tier,
	}
}

// Declaration returns the shell tool's KTP declaration.
func (t *ShellTool) Declaration() ktp.ToolDeclaration {
	return ktp.ToolDeclaration{
		Name:        "shell",
		Version:     "1.0.0",
		Description: "Execute shell commands directly (no shell interpretation)",
		MinTier:      ktp.TierOperator,
		DefaultTiers: []string{ktp.TierAdmin},
		Actions: []ktp.ActionSpec{
			{
				Name:        "exec",
				Description: "Execute a command with arguments",
				Destructive: true,
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"command":         {Type: "string", Description: "Command to execute (absolute path or basename)"},
						"args":            {Type: "array", Items: &ktp.JSONSchema{Type: "string"}, Description: "Command arguments"},
						"working_dir":     {Type: "string", Description: "Working directory (relative to workspace or absolute for unrestricted tier)"},
						"timeout_seconds": {Type: "integer", Description: "Timeout in seconds (default 30, max 300)"},
						"env":             {Type: "object", Description: "Additional environment variables"},
					},
					Required: []string{"command"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"stdout":     {Type: "string"},
						"stderr":     {Type: "string"},
						"exit_code":  {Type: "integer"},
						"elapsed_ms": {Type: "integer"},
						"timed_out":  {Type: "boolean"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "shell", Access: "execute", Resource: "*"}},
			},
		},
	}
}

// Execute dispatches to the exec action.
func (t *ShellTool) Execute(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	if req.Action != "exec" {
		return errResp(req.ID, fmt.Sprintf("unknown action: %s", req.Action)), nil
	}
	return t.exec(ctx, req)
}

func (t *ShellTool) exec(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	start := time.Now()

	// Parse parameters.
	command, err := stringParam(req.Parameters, "command")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}
	args := stringSliceParam(req.Parameters, "args")
	workingDir := stringParamDefault(req.Parameters, "working_dir", "")
	timeoutSec := intParamDefault(req.Parameters, "timeout_seconds", 30)
	extraEnv := mapParam(req.Parameters, "env")

	// Clamp timeout.
	timeout := time.Duration(timeoutSec) * time.Second
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	if timeout > maxTimeout {
		timeout = maxTimeout
	}

	// 1. Blocked commands deny list.
	baseName := filepath.Base(command)
	if blockedCommands[baseName] {
		return errResp(req.ID, fmt.Sprintf("command %q is blocked", baseName)), nil
	}

	// 2. Blocked arg patterns.
	fullCmdStr := command
	if len(args) > 0 {
		fullCmdStr += " " + strings.Join(args, " ")
	}
	for _, pattern := range blockedArgPatterns {
		if strings.Contains(fullCmdStr, pattern) {
			return errResp(req.ID, fmt.Sprintf("command matches blocked pattern: %q", pattern)), nil
		}
	}

	// 3. Command allowlist (admin tier skips allowlist).
	tier, tierErr := t.tier(req.AgentID)
	if tierErr != nil {
		return errResp(req.ID, fmt.Sprintf("failed to resolve agent tier: %s", tierErr)), nil
	}
	if tier != ktp.TierAdmin {
		allowed, err := t.allowedCmds(req.AgentID)
		if err != nil {
			return errResp(req.ID, fmt.Sprintf("failed to resolve allowed commands: %s", err)), nil
		}
		if len(allowed) == 0 {
			return errResp(req.ID, "no commands are allowed (empty allowlist)"), nil
		}
		// Reject path-qualified commands to prevent /tmp/ls from bypassing allowlist.
		if strings.Contains(command, string(os.PathSeparator)) {
			return errResp(req.ID, fmt.Sprintf("path-qualified command %q not allowed when allowlist is active", command)), nil
		}
		if !slices.Contains(allowed, baseName) {
			return errResp(req.ID, fmt.Sprintf("command %q not in allowlist", baseName)), nil
		}
	}

	// 4. Resolve workspace and working directory.
	workspace, err := t.workspace(req.AgentID)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("failed to resolve workspace: %s", err)), nil
	}

	resolvedDir, err := t.resolveWorkingDir(req.AgentID, workspace, workingDir)
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	// 5. Build environment.
	env := executil.BuildMinimalEnv(
		os.Getenv("PATH"),
		workspace,
		filepath.Join(workspace, "tmp"),
		"",
	)
	for k, v := range extraEnv {
		if protectedEnvKeys[strings.ToUpper(k)] {
			continue
		}
		env = append(env, k+"="+v)
	}

	// Execute.
	result, execErr := executil.RunProcess(ctx, executil.ProcessConfig{
		Command:    command,
		Args:       args,
		WorkingDir: resolvedDir,
		Env:        env,
		Timeout:    timeout,
	})
	if execErr != nil {
		return errResp(req.ID, execErr.Error()), nil
	}

	resp := ktp.NewToolResponse(req.ID, true, map[string]any{
		"stdout":     result.Stdout,
		"stderr":     result.Stderr,
		"exit_code":  result.ExitCode,
		"elapsed_ms": result.ElapsedMs,
		"timed_out":  result.TimedOut,
	}, "", time.Since(start).Milliseconds())
	return &resp, nil
}

// resolveWorkingDir validates and resolves the working directory.
// Empty → workspace root. Relative → resolved within workspace.
// Absolute → allowed for admin tier.
func (t *ShellTool) resolveWorkingDir(agentID, workspace, workingDir string) (string, error) {
	if workingDir == "" {
		return workspace, nil
	}

	if filepath.IsAbs(workingDir) {
		tier, err := t.tier(agentID)
		if err != nil {
			return "", fmt.Errorf("failed to resolve agent tier: %s", err)
		}
		if tier != ktp.TierAdmin {
			return "", fmt.Errorf("absolute working directories are only allowed for admin tier agents")
		}
		return workingDir, nil
	}

	return executil.SafePath(workspace, workingDir)
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

func stringParamDefault(params map[string]any, key, def string) string {
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
	}
	return def
}

func stringSliceParam(params map[string]any, key string) []string {
	raw, ok := params[key]
	if !ok {
		return nil
	}
	if ss, ok := raw.([]string); ok {
		return ss
	}
	slice, ok := raw.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(slice))
	for _, v := range slice {
		if s, ok := v.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

func mapParam(params map[string]any, key string) map[string]string {
	raw, ok := params[key]
	if !ok {
		return nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	result := make(map[string]string, len(m))
	for k, v := range m {
		if s, ok := v.(string); ok {
			result[k] = s
		}
	}
	return result
}

func errResp(reqID, msg string) *ktp.ToolResponse {
	resp := ktp.NewToolResponse(reqID, false, nil, msg, 0)
	return &resp
}
