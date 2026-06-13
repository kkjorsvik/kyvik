// Package file implements a KTP file-system tool for agent workspace access.
package file

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kkjorsvik/kyvik/internal/ktp"
	"github.com/kkjorsvik/kyvik/internal/tools/executil"
)

// WorkspaceFunc resolves an agent's workspace directory.
type WorkspaceFunc func(agentID string) (string, error)

// TierFunc resolves an agent's KTP tier.
type TierFunc func(agentID string) (string, error)

// HostPathsFunc resolves an agent's host path configuration.
type HostPathsFunc func(agentID string) (*HostPathConfig, error)

// HostPathConfig defines allowed and denied host filesystem paths for power-tier agents.
// Mirrors types.HostPathConfig but avoids circular import.
type HostPathConfig struct {
	Read  []string
	Write []string
	Deny  []string
}

// Option configures a FileTool.
type Option func(*FileTool)

// WithTierFunc sets the tier resolver callback.
func WithTierFunc(fn TierFunc) Option {
	return func(t *FileTool) { t.tierFunc = fn }
}

// WithHostPathsFunc sets the host paths resolver callback.
func WithHostPathsFunc(fn HostPathsFunc) Option {
	return func(t *FileTool) { t.hostPathsFunc = fn }
}

// WithSkillPaths sets skill-level path restrictions.
// ReadPaths are the only workspace-relative paths the skill may read.
// WritePaths are the only workspace-relative paths the skill may write.
// Empty slices mean no skill-level restrictions (agent defaults apply).
func WithSkillPaths(readPaths, writePaths []string) Option {
	return func(t *FileTool) {
		t.skillReadPaths = readPaths
		t.skillWritePaths = writePaths
	}
}

// FileTool implements ktp.Tool for file-system operations within an agent workspace.
type FileTool struct {
	resolveWorkspace WorkspaceFunc
	tierFunc         TierFunc
	hostPathsFunc    HostPathsFunc
	skillReadPaths   []string // skill-level read path restrictions (workspace-relative)
	skillWritePaths  []string // skill-level write path restrictions (workspace-relative)
}

// New creates a FileTool with the given workspace resolver and optional callbacks.
func New(resolve WorkspaceFunc, opts ...Option) *FileTool {
	t := &FileTool{resolveWorkspace: resolve}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// Declaration returns the file tool's KTP declaration.
func (t *FileTool) Declaration() ktp.ToolDeclaration {
	return ktp.ToolDeclaration{
		Name:        "file",
		Version:     "1.0.0",
		Description: "Read, write, edit, list, delete, and inspect files within the agent workspace",
		MinTier:      ktp.TierReader,
		DefaultTiers: []string{ktp.TierReader, ktp.TierWriter, ktp.TierOperator, ktp.TierAdmin},
		Actions: []ktp.ActionSpec{
			{
				Name:        "read",
				Description: "Read the contents of a file",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"path": {Type: "string", Description: "Relative path within workspace"},
					},
					Required: []string{"path"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"content": {Type: "string"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "filesystem", Access: "read", Resource: "{workspace}/*"}},
			},
			{
				Name:        "write",
				Description: "Write content to a file (overwrite or append)",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"path":    {Type: "string", Description: "Relative path within workspace"},
						"content": {Type: "string", Description: "Content to write"},
						"mode":    {Type: "string", Description: "Write mode", Enum: []string{"overwrite", "append"}, Default: "overwrite"},
					},
					Required: []string{"path", "content"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"bytes_written": {Type: "integer"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "filesystem", Access: "write", Resource: "{workspace}/*"}},
			},
			{
				Name:        "list",
				Description: "List files and directories",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"path":      {Type: "string", Description: "Relative path within workspace", Default: "."},
						"recursive": {Type: "boolean", Description: "List recursively", Default: false},
					},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"entries": {Type: "array", Items: &ktp.JSONSchema{Type: "object"}},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "filesystem", Access: "read", Resource: "{workspace}/*"}},
			},
			{
				Name:        "delete",
				Description: "Delete a file",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"path": {Type: "string", Description: "Relative path within workspace"},
					},
					Required: []string{"path"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"deleted": {Type: "boolean"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "filesystem", Access: "write", Resource: "{workspace}/*"}},
				Destructive:          true,
			},
			{
				Name:        "mkdir",
				Description: "Create a directory (and parents)",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"path": {Type: "string", Description: "Relative path within workspace"},
					},
					Required: []string{"path"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"created": {Type: "boolean"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "filesystem", Access: "write", Resource: "{workspace}/*"}},
			},
			{
				Name:        "stat",
				Description: "Get file or directory metadata",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"path": {Type: "string", Description: "Relative path within workspace"},
					},
					Required: []string{"path"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"name":     {Type: "string"},
						"size":     {Type: "integer"},
						"is_dir":   {Type: "boolean"},
						"modified": {Type: "string"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "filesystem", Access: "read", Resource: "{workspace}/*"}},
			},
			{
				Name:        "edit",
				Description: "Replace an exact string in a file with a new string. Fails if old_string is not found or matches multiple locations (unless replace_all is true).",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"path":        {Type: "string", Description: "File path (relative to workspace or absolute for admin tier)"},
						"old_string":  {Type: "string", Description: "Exact string to find in the file"},
						"new_string":  {Type: "string", Description: "Replacement string"},
						"replace_all": {Type: "boolean", Description: "Replace all occurrences (default: false, requires unique match)"},
					},
					Required: []string{"path", "old_string", "new_string"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"replacements": {Type: "integer", Description: "Number of replacements made"},
						"path":         {Type: "string", Description: "Resolved file path"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "filesystem", Access: "write", Resource: "{workspace}/*"}},
			},
		},
	}
}

// Execute dispatches to the requested action.
func (t *FileTool) Execute(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	workspace, err := t.resolveWorkspace(req.AgentID)
	if err != nil {
		return errorResponse(req.ID, fmt.Sprintf("failed to resolve workspace: %s", err)), nil
	}

	start := time.Now()

	switch req.Action {
	case "read":
		return t.read(req, workspace, start)
	case "write":
		return t.write(req, workspace, start)
	case "list":
		return t.list(req, workspace, start)
	case "delete":
		return t.doDelete(req, workspace, start)
	case "mkdir":
		return t.mkdir(req, workspace, start)
	case "stat":
		return t.stat(req, workspace, start)
	case "edit":
		return t.edit(req, workspace, start)
	default:
		return errorResponse(req.ID, fmt.Sprintf("unknown action: %s", req.Action)), nil
	}
}

func (t *FileTool) read(req ktp.ToolRequest, workspace string, start time.Time) (*ktp.ToolResponse, error) {
	path, err := stringParam(req.Parameters, "path")
	if err != nil {
		return errorResponse(req.ID, err.Error()), nil
	}

	absPath, err := t.resolvePath(req.AgentID, workspace, path, false)
	if err != nil {
		return errorResponse(req.ID, err.Error()), nil
	}

	if err := t.checkSkillPaths(absPath, workspace, false); err != nil {
		return errorResponse(req.ID, err.Error()), nil
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return errorResponse(req.ID, fmt.Sprintf("read failed: %s", err)), nil
	}

	resp := ktp.NewToolResponse(req.ID, true, map[string]any{"content": string(data)}, "", ms(start))
	return &resp, nil
}

func (t *FileTool) write(req ktp.ToolRequest, workspace string, start time.Time) (*ktp.ToolResponse, error) {
	path, err := stringParam(req.Parameters, "path")
	if err != nil {
		return errorResponse(req.ID, err.Error()), nil
	}

	content, err := stringParam(req.Parameters, "content")
	if err != nil {
		return errorResponse(req.ID, err.Error()), nil
	}

	mode := stringParamDefault(req.Parameters, "mode", "overwrite")

	absPath, err := t.resolvePath(req.AgentID, workspace, path, true)
	if err != nil {
		return errorResponse(req.ID, err.Error()), nil
	}

	if err := t.checkSkillPaths(absPath, workspace, true); err != nil {
		return errorResponse(req.ID, err.Error()), nil
	}

	// Ensure parent directory exists.
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return errorResponse(req.ID, fmt.Sprintf("failed to create parent directory: %s", err)), nil
	}

	var n int
	switch mode {
	case "append":
		f, err := os.OpenFile(absPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return errorResponse(req.ID, fmt.Sprintf("write failed: %s", err)), nil
		}
		n, err = f.WriteString(content)
		closeErr := f.Close()
		if err != nil {
			return errorResponse(req.ID, fmt.Sprintf("write failed: %s", err)), nil
		}
		if closeErr != nil {
			return errorResponse(req.ID, fmt.Sprintf("write failed: %s", closeErr)), nil
		}
	default: // overwrite
		err := os.WriteFile(absPath, []byte(content), 0o644)
		if err != nil {
			return errorResponse(req.ID, fmt.Sprintf("write failed: %s", err)), nil
		}
		n = len(content)
	}

	resp := ktp.NewToolResponse(req.ID, true, map[string]any{"bytes_written": n}, "", ms(start))
	return &resp, nil
}

func (t *FileTool) edit(req ktp.ToolRequest, workspace string, start time.Time) (*ktp.ToolResponse, error) {
	path, err := stringParam(req.Parameters, "path")
	if err != nil {
		return errorResponse(req.ID, err.Error()), nil
	}
	oldStr, err := stringParam(req.Parameters, "old_string")
	if err != nil {
		return errorResponse(req.ID, err.Error()), nil
	}
	newStr, err := stringParam(req.Parameters, "new_string")
	if err != nil {
		return errorResponse(req.ID, err.Error()), nil
	}
	replaceAll := boolParamDefault(req.Parameters, "replace_all", false)

	absPath, err := t.resolvePath(req.AgentID, workspace, path, true)
	if err != nil {
		return errorResponse(req.ID, err.Error()), nil
	}
	if err := t.checkSkillPaths(absPath, workspace, true); err != nil {
		return errorResponse(req.ID, err.Error()), nil
	}

	if oldStr == "" {
		return errorResponse(req.ID, "edit: old_string must not be empty"), nil
	}
	if oldStr == newStr {
		return errorResponse(req.ID, "edit: old_string and new_string are identical"), nil
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return errorResponse(req.ID, fmt.Sprintf("edit: %v", err)), nil
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return errorResponse(req.ID, fmt.Sprintf("edit: read file: %v", err)), nil
	}
	content := string(data)

	count := strings.Count(content, oldStr)
	if count == 0 {
		return errorResponse(req.ID, "edit: old_string not found in file"), nil
	}
	if count > 1 && !replaceAll {
		return errorResponse(req.ID, fmt.Sprintf("edit: old_string matches %d locations (use replace_all or provide more context)", count)), nil
	}

	var updated string
	if replaceAll {
		updated = strings.ReplaceAll(content, oldStr, newStr)
	} else {
		updated = strings.Replace(content, oldStr, newStr, 1)
	}

	if err := os.WriteFile(absPath, []byte(updated), info.Mode()); err != nil {
		return errorResponse(req.ID, fmt.Sprintf("edit: write file: %v", err)), nil
	}

	resp := ktp.NewToolResponse(req.ID, true, map[string]any{
		"replacements": count,
		"path":         absPath,
	}, "", ms(start))
	return &resp, nil
}

// listEntry represents a single directory entry in list results.
type listEntry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
}

func (t *FileTool) list(req ktp.ToolRequest, workspace string, start time.Time) (*ktp.ToolResponse, error) {
	path := stringParamDefault(req.Parameters, "path", ".")
	recursive := boolParamDefault(req.Parameters, "recursive", false)

	absPath, err := t.resolvePath(req.AgentID, workspace, path, false)
	if err != nil {
		return errorResponse(req.ID, err.Error()), nil
	}

	if err := t.checkSkillPaths(absPath, workspace, false); err != nil {
		return errorResponse(req.ID, err.Error()), nil
	}

	var entries []listEntry

	if recursive {
		err = filepath.WalkDir(absPath, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			// Skip the root directory itself.
			if p == absPath {
				return nil
			}
			rel, _ := filepath.Rel(absPath, p)
			info, err := d.Info()
			if err != nil {
				return err
			}
			entries = append(entries, listEntry{Name: rel, IsDir: d.IsDir(), Size: info.Size()})
			return nil
		})
	} else {
		dirEntries, readErr := os.ReadDir(absPath)
		if readErr != nil {
			return errorResponse(req.ID, fmt.Sprintf("list failed: %s", readErr)), nil
		}
		for _, d := range dirEntries {
			info, err := d.Info()
			if err != nil {
				continue
			}
			entries = append(entries, listEntry{Name: d.Name(), IsDir: d.IsDir(), Size: info.Size()})
		}
	}

	if err != nil {
		return errorResponse(req.ID, fmt.Sprintf("list failed: %s", err)), nil
	}

	resp := ktp.NewToolResponse(req.ID, true, map[string]any{"entries": entries}, "", ms(start))
	return &resp, nil
}

func (t *FileTool) doDelete(req ktp.ToolRequest, workspace string, start time.Time) (*ktp.ToolResponse, error) {
	path, err := stringParam(req.Parameters, "path")
	if err != nil {
		return errorResponse(req.ID, err.Error()), nil
	}

	absPath, err := t.resolvePath(req.AgentID, workspace, path, true)
	if err != nil {
		return errorResponse(req.ID, err.Error()), nil
	}

	if err := t.checkSkillPaths(absPath, workspace, true); err != nil {
		return errorResponse(req.ID, err.Error()), nil
	}

	if err := os.Remove(absPath); err != nil {
		return errorResponse(req.ID, fmt.Sprintf("delete failed: %s", err)), nil
	}

	resp := ktp.NewToolResponse(req.ID, true, map[string]any{"deleted": true}, "", ms(start))
	return &resp, nil
}

func (t *FileTool) mkdir(req ktp.ToolRequest, workspace string, start time.Time) (*ktp.ToolResponse, error) {
	path, err := stringParam(req.Parameters, "path")
	if err != nil {
		return errorResponse(req.ID, err.Error()), nil
	}

	absPath, err := t.resolvePath(req.AgentID, workspace, path, true)
	if err != nil {
		return errorResponse(req.ID, err.Error()), nil
	}

	if err := t.checkSkillPaths(absPath, workspace, true); err != nil {
		return errorResponse(req.ID, err.Error()), nil
	}

	if err := os.MkdirAll(absPath, 0o755); err != nil {
		return errorResponse(req.ID, fmt.Sprintf("mkdir failed: %s", err)), nil
	}

	resp := ktp.NewToolResponse(req.ID, true, map[string]any{"created": true}, "", ms(start))
	return &resp, nil
}

func (t *FileTool) stat(req ktp.ToolRequest, workspace string, start time.Time) (*ktp.ToolResponse, error) {
	path, err := stringParam(req.Parameters, "path")
	if err != nil {
		return errorResponse(req.ID, err.Error()), nil
	}

	absPath, err := t.resolvePath(req.AgentID, workspace, path, false)
	if err != nil {
		return errorResponse(req.ID, err.Error()), nil
	}

	if err := t.checkSkillPaths(absPath, workspace, false); err != nil {
		return errorResponse(req.ID, err.Error()), nil
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return errorResponse(req.ID, fmt.Sprintf("stat failed: %s", err)), nil
	}

	result := map[string]any{
		"name":     info.Name(),
		"size":     info.Size(),
		"is_dir":   info.IsDir(),
		"modified": info.ModTime().UTC().Format(time.RFC3339),
	}

	resp := ktp.NewToolResponse(req.ID, true, result, "", ms(start))
	return &resp, nil
}

// resolvePath validates and resolves a user-provided path based on the agent's tier.
// Standard tiers (reader→admin): workspace-confined only.
// Power tier: workspace paths work as-is; absolute paths checked against HostPathConfig.
// Unrestricted tier: all paths allowed.
func (t *FileTool) resolvePath(agentID, workspace, userPath string, isWrite bool) (string, error) {
	if userPath == "" {
		return "", fmt.Errorf("path must not be empty")
	}

	// Relative paths always resolve within workspace (all tiers).
	if !filepath.IsAbs(userPath) {
		return executil.SafePath(workspace, userPath)
	}

	// Absolute paths require tier check.
	tier, err := t.getAgentTier(agentID)
	if err != nil {
		return "", fmt.Errorf("failed to resolve agent tier: %s", err)
	}

	switch tier {
	case "admin":
		// Admin: first check host path config (if configured for host filesystem access).
		hostPaths, err := t.getHostPaths(agentID)
		if err == nil && hostPaths != nil && (len(hostPaths.Read) > 0 || len(hostPaths.Write) > 0) {
			cleaned := filepath.Clean(userPath)
			if err := checkHostPath(cleaned, hostPaths, isWrite); err != nil {
				return "", err
			}
			// Resolve symlinks and re-check to prevent symlink escapes.
			realPath, err := filepath.EvalSymlinks(cleaned)
			if err != nil {
				if os.IsNotExist(err) {
					// File doesn't exist yet (write case). Resolve parent directory.
					parentReal, parentErr := filepath.EvalSymlinks(filepath.Dir(cleaned))
					if parentErr != nil {
						return "", fmt.Errorf("failed to resolve path: %w", parentErr)
					}
					realPath = filepath.Join(parentReal, filepath.Base(cleaned))
				} else {
					return "", fmt.Errorf("failed to resolve path: %w", err)
				}
			}
			if realPath != cleaned {
				if err := checkHostPath(realPath, hostPaths, isWrite); err != nil {
					return "", fmt.Errorf("path traversal is not allowed (symlink escape)")
				}
			}
			return cleaned, nil
		}
		// No host paths configured: read-only access to Kyvik system paths.
		cleaned := filepath.Clean(userPath)
		if !isWrite {
			for _, prefix := range adminReadPaths {
				if cleaned == prefix || strings.HasPrefix(cleaned, prefix+"/") {
					// Still check default deny list.
					for _, deny := range defaultDenyPaths {
						if strings.Contains(cleaned, deny) {
							return "", fmt.Errorf("path %q is denied (security policy)", userPath)
						}
					}
					return cleaned, nil
				}
			}
		}
		return "", fmt.Errorf("path not allowed for admin tier: %q", userPath)
	default:
		// Standard tiers: check extra_paths from config if available.
		hostPaths, err := t.getHostPaths(agentID)
		if err == nil && hostPaths != nil {
			cleaned := filepath.Clean(userPath)
			if err := checkHostPath(cleaned, hostPaths, isWrite); err == nil {
				return cleaned, nil
			}
		}
		return "", fmt.Errorf("absolute paths are not allowed for %s tier", tier)
	}
}

// adminReadPaths are system paths that admin-tier agents can read.
// These paths are also in the systemd ReadOnlyPaths/ReadWritePaths.
var adminReadPaths = []string{"/etc/kyvik", "/var/log/kyvik", "/var/lib/kyvik"}

// defaultDenyPaths are always denied for power-tier host access.
var defaultDenyPaths = []string{
	"/etc/shadow",
	"/etc/passwd",
	"/.ssh/",
	"/.gnupg/",
	"/id_rsa",
	"/id_ed25519",
	".pem",
}

// checkHostPath validates an absolute path against the HostPathConfig.
// Deny list takes precedence, then allowlist is checked.
func checkHostPath(path string, config *HostPathConfig, isWrite bool) error {
	cleaned := filepath.Clean(path)

	// Always check default deny paths.
	for _, deny := range defaultDenyPaths {
		if strings.Contains(cleaned, deny) {
			return fmt.Errorf("path %q is denied (security policy)", path)
		}
	}

	// Check explicit deny list.
	if config != nil {
		for _, deny := range config.Deny {
			if matchHostPathPattern(cleaned, deny) {
				return fmt.Errorf("path %q is denied by host path config", path)
			}
		}
	}

	// Check allowlist.
	if config == nil {
		return fmt.Errorf("no host path config: absolute path %q not allowed", path)
	}

	var allowlist []string
	if isWrite {
		allowlist = config.Write
	} else {
		allowlist = config.Read
	}

	if len(allowlist) == 0 {
		return fmt.Errorf("absolute path %q not in host path allowlist", path)
	}

	for _, allowed := range allowlist {
		if matchHostPathPattern(cleaned, allowed) {
			return nil
		}
	}

	return fmt.Errorf("absolute path %q not in host path allowlist", path)
}

// matchHostPathPattern checks if a path matches an allowlist/denylist pattern.
// Supports exact match and prefix match (pattern ending with /).
func matchHostPathPattern(path, pattern string) bool {
	pattern = filepath.Clean(pattern)
	if path == pattern {
		return true
	}
	// Prefix match: if pattern is a directory, match anything under it.
	if strings.HasSuffix(pattern, string(filepath.Separator)) || strings.HasPrefix(path, pattern+string(filepath.Separator)) {
		return true
	}
	return false
}

// checkSkillPaths enforces skill-level path restrictions on a resolved absolute path.
// isWrite determines whether write paths or read paths are checked.
// If no skill paths are configured, all paths are allowed (no skill-level restriction).
func (t *FileTool) checkSkillPaths(absPath, workspace string, isWrite bool) error {
	var allowedPaths []string
	if isWrite {
		allowedPaths = t.skillWritePaths
	} else {
		allowedPaths = t.skillReadPaths
	}

	if len(allowedPaths) == 0 {
		return nil // No skill restrictions.
	}

	for _, allowed := range allowedPaths {
		var allowedAbs string
		if filepath.IsAbs(allowed) {
			allowedAbs = filepath.Clean(allowed)
		} else {
			allowedAbs = filepath.Clean(filepath.Join(workspace, allowed))
		}

		if absPath == allowedAbs || strings.HasPrefix(absPath, allowedAbs+string(filepath.Separator)) {
			return nil
		}
	}

	accessType := "read"
	if isWrite {
		accessType = "write"
	}
	return fmt.Errorf("skill sandbox policy denies %s access to path %q", accessType, absPath)
}

// getAgentTier returns the agent's tier, or empty string if no tier func is configured.
func (t *FileTool) getAgentTier(agentID string) (string, error) {
	if t.tierFunc == nil {
		return "", nil
	}
	return t.tierFunc(agentID)
}

// getHostPaths returns the agent's host path config, or nil if no func is configured.
func (t *FileTool) getHostPaths(agentID string) (*HostPathConfig, error) {
	if t.hostPathsFunc == nil {
		return nil, nil
	}
	return t.hostPathsFunc(agentID)
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

func boolParamDefault(params map[string]any, key string, def bool) bool {
	raw, ok := params[key]
	if !ok {
		return def
	}
	b, ok := raw.(bool)
	if !ok {
		// Handle JSON unmarshalled booleans.
		if jb, ok := raw.(json.Number); ok {
			if jb.String() == "1" || jb.String() == "true" {
				return true
			}
		}
		return def
	}
	return b
}

func errorResponse(reqID, msg string) *ktp.ToolResponse {
	resp := ktp.NewToolResponse(reqID, false, nil, msg, 0)
	return &resp
}

func ms(start time.Time) int64 {
	return time.Since(start).Milliseconds()
}
