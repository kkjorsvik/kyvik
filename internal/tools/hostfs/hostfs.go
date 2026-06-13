// Package hostfs implements a KTP tool for host filesystem access.
package hostfs

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kkjorsvik/kyvik/internal/audit"
	"github.com/kkjorsvik/kyvik/internal/ktp"
)

// Config controls host filesystem tool limits.
type Config struct {
	MaxReadBytes   int64
	MaxWriteBytes  int64
	MaxListDepth   int
	MaxListEntries int
}

// HostPathConfig defines allowed and denied host filesystem paths.
type HostPathConfig struct {
	Read  []string
	Write []string
	Deny  []string
}

// AllowlistFunc resolves an agent's host filesystem allowlist.
type AllowlistFunc func(agentID string) (*HostPathConfig, error)

// Option configures the host filesystem tool.
type Option func(*Tool)

// WithAllowlistFunc sets the allowlist resolver.
func WithAllowlistFunc(fn AllowlistFunc) Option {
	return func(t *Tool) { t.allowlist = fn }
}

// WithAuditLogger sets the audit logger.
func WithAuditLogger(al audit.Logger) Option {
	return func(t *Tool) { t.audit = al }
}

// Tool implements host filesystem access for power-tier agents.
type Tool struct {
	cfg       Config
	allowlist AllowlistFunc
	audit     audit.Logger
}

// New creates a HostFS tool with the given config.
func New(cfg Config, opts ...Option) *Tool {
	if cfg.MaxReadBytes <= 0 {
		cfg.MaxReadBytes = 10 << 20
	}
	if cfg.MaxWriteBytes <= 0 {
		cfg.MaxWriteBytes = 10 << 20
	}
	if cfg.MaxListDepth <= 0 {
		cfg.MaxListDepth = 1
	}
	if cfg.MaxListEntries <= 0 {
		cfg.MaxListEntries = 5000
	}
	t := &Tool{cfg: cfg}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// Inline returns true so execution happens in-process.
//
// hostfs requires a per-agent allowlist resolver provided by the main process.
// The generic sandbox binary does not have that wiring, which can make hostfs
// unavailable at execution time even when it is exposed to the model.
func (t *Tool) Inline() bool { return true }

// Declaration returns the tool's KTP declaration.
func (t *Tool) Declaration() ktp.ToolDeclaration {
	return ktp.ToolDeclaration{
		Name:         "hostfs",
		Version:      "1.0.0",
		Description:  "Read, write, list, delete, and inspect files on the host filesystem (allowlist enforced).",
		MinTier:      ktp.TierAdmin,
		DefaultTiers: []string{ktp.TierAdmin},
		Actions: []ktp.ActionSpec{
			{
				Name:        "read",
				Description: "Read the contents of a host file",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"path":  {Type: "string", Description: "Absolute host path"},
						"range": {Type: "object", Description: "Optional byte range {offset, length}"},
					},
					Required: []string{"path"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"content":   {Type: "string"},
						"encoding":  {Type: "string"},
						"mime_type": {Type: "string"},
						"size":      {Type: "integer"},
						"truncated": {Type: "boolean"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "host_filesystem", Access: "read", Resource: "{path}"}},
			},
			{
				Name:        "write",
				Description: "Write content to a host file (overwrite or append)",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"path":     {Type: "string", Description: "Absolute host path"},
						"content":  {Type: "string", Description: "Content to write"},
						"encoding": {Type: "string", Description: "content encoding", Enum: []string{"text", "base64"}, Default: "text"},
						"mode":     {Type: "string", Description: "Write mode", Enum: []string{"overwrite", "append"}, Default: "overwrite"},
					},
					Required: []string{"path", "content"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"bytes_written": {Type: "integer"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "host_filesystem", Access: "write", Resource: "{path}"}},
			},
			{
				Name:        "list",
				Description: "List directory contents on the host filesystem",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"path":      {Type: "string", Description: "Absolute host path"},
						"recursive": {Type: "boolean", Description: "List recursively", Default: false},
						"depth":     {Type: "integer", Description: "Max recursion depth", Default: 1},
						"pattern":   {Type: "string", Description: "Optional glob pattern filter"},
					},
					Required: []string{"path"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"entries":   {Type: "array", Items: &ktp.JSONSchema{Type: "object"}},
						"truncated": {Type: "boolean"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "host_filesystem", Access: "read", Resource: "{path}"}},
			},
			{
				Name:        "stat",
				Description: "Get file metadata (size, modified time, permissions)",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"path": {Type: "string", Description: "Absolute host path"},
					},
					Required: []string{"path"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"size":         {Type: "integer"},
						"modified":     {Type: "string"},
						"is_directory": {Type: "boolean"},
						"permissions":  {Type: "string"},
						"mime_type":    {Type: "string"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "host_filesystem", Access: "read", Resource: "{path}"}},
			},
			{
				Name:        "delete",
				Description: "Delete a file or directory on the host filesystem",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"path":      {Type: "string", Description: "Absolute host path"},
						"recursive": {Type: "boolean", Description: "Delete recursively", Default: false},
					},
					Required: []string{"path"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"deleted": {Type: "boolean"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "host_filesystem", Access: "delete", Resource: "{path}"}},
			},
			{
				Name:        "mkdir",
				Description: "Create a directory on the host filesystem",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"path": {Type: "string", Description: "Absolute host path"},
					},
					Required: []string{"path"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"created": {Type: "boolean"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "host_filesystem", Access: "write", Resource: "{path}"}},
			},
		},
	}
}

// Execute performs a host filesystem action.
func (t *Tool) Execute(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	start := time.Now()
	switch req.Action {
	case "read":
		return t.read(ctx, req, start)
	case "write":
		return t.write(ctx, req, start)
	case "list":
		return t.list(ctx, req, start)
	case "stat":
		return t.stat(ctx, req, start)
	case "delete":
		return t.remove(ctx, req, start)
	case "mkdir":
		return t.mkdir(ctx, req, start)
	default:
		return errorResponse(req.ID, fmt.Sprintf("unknown action: %s", req.Action)), nil
	}
}

func (t *Tool) read(ctx context.Context, req ktp.ToolRequest, start time.Time) (*ktp.ToolResponse, error) {
	path, err := stringParam(req.Parameters, "path")
	if err != nil {
		return errorResponse(req.ID, err.Error()), nil
	}

	absPath, err := t.resolvePath(req.AgentID, path, false)
	if err != nil {
		return errorResponse(req.ID, err.Error()), nil
	}

	var rng *readRange
	if raw, ok := req.Parameters["range"]; ok {
		rng, err = parseRange(raw)
		if err != nil {
			return errorResponse(req.ID, err.Error()), nil
		}
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return errorResponse(req.ID, fmt.Sprintf("stat failed: %s", err)), nil
	}
	size := info.Size()

	var data []byte
	var truncated bool
	if rng != nil {
		data, err = readRangeBytes(absPath, rng)
		if err != nil {
			return errorResponse(req.ID, fmt.Sprintf("read failed: %s", err)), nil
		}
	} else {
		limit := t.cfg.MaxReadBytes
		if size > limit {
			truncated = true
		}
		data, err = readLimited(absPath, limit)
		if err != nil {
			return errorResponse(req.ID, fmt.Sprintf("read failed: %s", err)), nil
		}
	}

	mimeType := http.DetectContentType(data)
	encoding := "text"
	content := string(data)
	if !utf8.Valid(data) || isBinaryMime(mimeType) {
		encoding = "base64"
		content = base64.StdEncoding.EncodeToString(data)
	}

	result := map[string]any{
		"content":   content,
		"encoding":  encoding,
		"mime_type": mimeType,
		"size":      size,
		"truncated": truncated,
	}
	resp := ktp.NewToolResponse(req.ID, true, result, "", ms(start))
	t.auditAction(ctx, req.AgentID, "read", absPath, true, result, nil)
	return &resp, nil
}

func (t *Tool) write(ctx context.Context, req ktp.ToolRequest, start time.Time) (*ktp.ToolResponse, error) {
	path, err := stringParam(req.Parameters, "path")
	if err != nil {
		return errorResponse(req.ID, err.Error()), nil
	}
	content, err := stringParam(req.Parameters, "content")
	if err != nil {
		return errorResponse(req.ID, err.Error()), nil
	}
	encoding := stringParamDefault(req.Parameters, "encoding", "text")
	mode := stringParamDefault(req.Parameters, "mode", "overwrite")

	data := []byte(content)
	if encoding == "base64" {
		decoded, err := base64.StdEncoding.DecodeString(content)
		if err != nil {
			return errorResponse(req.ID, fmt.Sprintf("invalid base64: %s", err)), nil
		}
		data = decoded
	}
	if int64(len(data)) > t.cfg.MaxWriteBytes {
		return errorResponse(req.ID, fmt.Sprintf("content exceeds max_write_bytes (%d)", t.cfg.MaxWriteBytes)), nil
	}

	absPath, err := t.resolvePath(req.AgentID, path, true)
	if err != nil {
		return errorResponse(req.ID, err.Error()), nil
	}

	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return errorResponse(req.ID, fmt.Sprintf("failed to create parent directory: %s", err)), nil
	}

	switch mode {
	case "append":
		f, err := os.OpenFile(absPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return errorResponse(req.ID, fmt.Sprintf("write failed: %s", err)), nil
		}
		n, err := f.Write(data)
		closeErr := f.Close()
		if err != nil {
			return errorResponse(req.ID, fmt.Sprintf("write failed: %s", err)), nil
		}
		if closeErr != nil {
			return errorResponse(req.ID, fmt.Sprintf("write failed: %s", closeErr)), nil
		}
		result := map[string]any{"bytes_written": n}
		resp := ktp.NewToolResponse(req.ID, true, result, "", ms(start))
		t.auditAction(ctx, req.AgentID, "write", absPath, true, result, nil)
		return &resp, nil
	default:
		n, err := atomicWrite(absPath, data)
		if err != nil {
			return errorResponse(req.ID, fmt.Sprintf("write failed: %s", err)), nil
		}
		result := map[string]any{"bytes_written": n}
		resp := ktp.NewToolResponse(req.ID, true, result, "", ms(start))
		t.auditAction(ctx, req.AgentID, "write", absPath, true, result, nil)
		return &resp, nil
	}
}

type listEntry struct {
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	Modified    string `json:"modified"`
	IsDirectory bool   `json:"is_directory"`
	Permissions string `json:"permissions"`
}

func (t *Tool) list(ctx context.Context, req ktp.ToolRequest, start time.Time) (*ktp.ToolResponse, error) {
	path, err := stringParam(req.Parameters, "path")
	if err != nil {
		return errorResponse(req.ID, err.Error()), nil
	}
	recursive := boolParamDefault(req.Parameters, "recursive", false)
	depth := intParamDefault(req.Parameters, "depth", t.cfg.MaxListDepth)
	pattern := stringParamDefault(req.Parameters, "pattern", "")

	absPath, err := t.resolvePath(req.AgentID, path, false)
	if err != nil {
		return errorResponse(req.ID, err.Error()), nil
	}

	var entries []listEntry
	truncated := false

	if !recursive {
		dirEntries, err := os.ReadDir(absPath)
		if err != nil {
			return errorResponse(req.ID, fmt.Sprintf("list failed: %s", err)), nil
		}
		for _, entry := range dirEntries {
			info, err := entry.Info()
			if err != nil {
				return errorResponse(req.ID, fmt.Sprintf("list failed: %s", err)), nil
			}
			name := entry.Name()
			if pattern != "" {
				if ok, _ := filepath.Match(pattern, name); !ok {
					continue
				}
			}
			entries = append(entries, listEntry{
				Name:        name,
				Size:        info.Size(),
				Modified:    info.ModTime().UTC().Format(time.RFC3339),
				IsDirectory: entry.IsDir(),
				Permissions: info.Mode().String(),
			})
			if len(entries) >= t.cfg.MaxListEntries {
				truncated = true
				break
			}
		}
	} else {
		err = filepath.WalkDir(absPath, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if p == absPath {
				return nil
			}
			rel, _ := filepath.Rel(absPath, p)
			depthNow := pathDepth(rel)
			if depthNow > depth {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if pattern != "" {
				if ok, _ := filepath.Match(pattern, rel); !ok {
					return nil
				}
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			entries = append(entries, listEntry{
				Name:        rel,
				Size:        info.Size(),
				Modified:    info.ModTime().UTC().Format(time.RFC3339),
				IsDirectory: d.IsDir(),
				Permissions: info.Mode().String(),
			})
			if len(entries) >= t.cfg.MaxListEntries {
				truncated = true
				return filepath.SkipDir
			}
			return nil
		})
		if err != nil {
			return errorResponse(req.ID, fmt.Sprintf("list failed: %s", err)), nil
		}
	}

	result := map[string]any{"entries": entries, "truncated": truncated}
	resp := ktp.NewToolResponse(req.ID, true, result, "", ms(start))
	t.auditAction(ctx, req.AgentID, "list", absPath, true, result, nil)
	return &resp, nil
}

func (t *Tool) stat(ctx context.Context, req ktp.ToolRequest, start time.Time) (*ktp.ToolResponse, error) {
	path, err := stringParam(req.Parameters, "path")
	if err != nil {
		return errorResponse(req.ID, err.Error()), nil
	}
	absPath, err := t.resolvePath(req.AgentID, path, false)
	if err != nil {
		return errorResponse(req.ID, err.Error()), nil
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return errorResponse(req.ID, fmt.Sprintf("stat failed: %s", err)), nil
	}
	mimeType := "application/octet-stream"
	if !info.IsDir() {
		data, _ := readLimited(absPath, 512)
		if len(data) > 0 {
			mimeType = http.DetectContentType(data)
		}
	}
	result := map[string]any{
		"size":         info.Size(),
		"modified":     info.ModTime().UTC().Format(time.RFC3339),
		"is_directory": info.IsDir(),
		"permissions":  info.Mode().String(),
		"mime_type":    mimeType,
	}
	resp := ktp.NewToolResponse(req.ID, true, result, "", ms(start))
	t.auditAction(ctx, req.AgentID, "stat", absPath, true, result, nil)
	return &resp, nil
}

func (t *Tool) mkdir(ctx context.Context, req ktp.ToolRequest, start time.Time) (*ktp.ToolResponse, error) {
	path, err := stringParam(req.Parameters, "path")
	if err != nil {
		return errorResponse(req.ID, err.Error()), nil
	}
	absPath, err := t.resolvePath(req.AgentID, path, true)
	if err != nil {
		return errorResponse(req.ID, err.Error()), nil
	}
	if err := os.MkdirAll(absPath, 0o755); err != nil {
		return errorResponse(req.ID, fmt.Sprintf("mkdir failed: %s", err)), nil
	}
	result := map[string]any{"created": true}
	resp := ktp.NewToolResponse(req.ID, true, result, "", ms(start))
	t.auditAction(ctx, req.AgentID, "mkdir", absPath, true, result, nil)
	return &resp, nil
}

func (t *Tool) remove(ctx context.Context, req ktp.ToolRequest, start time.Time) (*ktp.ToolResponse, error) {
	path, err := stringParam(req.Parameters, "path")
	if err != nil {
		return errorResponse(req.ID, err.Error()), nil
	}
	recursive := boolParamDefault(req.Parameters, "recursive", false)

	absPath, err := t.resolvePath(req.AgentID, path, true)
	if err != nil {
		return errorResponse(req.ID, err.Error()), nil
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return errorResponse(req.ID, fmt.Sprintf("stat failed: %s", err)), nil
	}
	if info.IsDir() && !recursive {
		return errorResponse(req.ID, "refusing to delete directory without recursive=true"), nil
	}

	hashes, err := hashPaths(absPath, recursive)
	if err != nil {
		return errorResponse(req.ID, fmt.Sprintf("hash failed: %s", err)), nil
	}

	if recursive {
		if err := os.RemoveAll(absPath); err != nil {
			return errorResponse(req.ID, fmt.Sprintf("delete failed: %s", err)), nil
		}
	} else {
		if err := os.Remove(absPath); err != nil {
			return errorResponse(req.ID, fmt.Sprintf("delete failed: %s", err)), nil
		}
	}

	result := map[string]any{"deleted": true}
	resp := ktp.NewToolResponse(req.ID, true, result, "", ms(start))
	details := map[string]any{"hashes": hashes, "recursive": recursive}
	t.auditAction(ctx, req.AgentID, "delete", absPath, true, details, nil)
	return &resp, nil
}

// --- helpers ---

func (t *Tool) resolvePath(agentID, userPath string, isWrite bool) (string, error) {
	if userPath == "" {
		return "", fmt.Errorf("path must not be empty")
	}
	if strings.Contains(userPath, "..") {
		return "", fmt.Errorf("path must not contain '..'")
	}
	if !filepath.IsAbs(userPath) {
		return "", fmt.Errorf("path must be absolute")
	}
	allowlist, err := t.getAllowlist(agentID)
	if err != nil {
		return "", err
	}
	if allowlist == nil {
		return "", fmt.Errorf("no host filesystem allowlist configured")
	}
	cleaned := filepath.Clean(userPath)
	if err := checkHostPath(cleaned, allowlist, isWrite); err != nil {
		return "", err
	}
	realPath, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		if os.IsNotExist(err) {
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
		if err := checkHostPath(realPath, allowlist, isWrite); err != nil {
			return "", fmt.Errorf("path traversal is not allowed (symlink escape)")
		}
	}
	return realPath, nil
}

func (t *Tool) getAllowlist(agentID string) (*HostPathConfig, error) {
	if t.allowlist == nil {
		return nil, nil
	}
	return t.allowlist(agentID)
}

var dangerousPrefixPaths = []string{
	"/etc/",
	"/boot/",
	"/sys/",
	"/proc/",
	"/dev/",
	"/root/",
}

var dangerousSubstrings = []string{
	".ssh",
	".gnupg",
	".aws",
	"id_rsa",
	"id_ed25519",
	".pem",
}

func checkHostPath(path string, config *HostPathConfig, isWrite bool) error {
	cleaned := filepath.Clean(path)
	if isDangerousPath(cleaned) {
		return fmt.Errorf("path %q is denied (security policy)", path)
	}
	if config == nil {
		return fmt.Errorf("no host filesystem allowlist configured")
	}
	for _, deny := range config.Deny {
		if matchHostPathPattern(cleaned, deny) {
			return fmt.Errorf("path %q is denied by host filesystem config", path)
		}
	}
	var allowlist []string
	if isWrite {
		allowlist = config.Write
	} else {
		allowlist = config.Read
	}
	if len(allowlist) == 0 {
		return fmt.Errorf("path %q not in host filesystem allowlist", path)
	}
	for _, allowed := range allowlist {
		if matchHostPathPattern(cleaned, allowed) {
			return nil
		}
	}
	return fmt.Errorf("path %q not in host filesystem allowlist", path)
}

func isDangerousPath(path string) bool {
	cleaned := filepath.Clean(path)
	for _, prefix := range dangerousPrefixPaths {
		if cleaned == strings.TrimSuffix(prefix, "/") || strings.HasPrefix(cleaned, prefix) {
			return true
		}
	}
	lower := strings.ToLower(cleaned)
	for _, deny := range dangerousSubstrings {
		if strings.Contains(lower, deny) {
			return true
		}
	}
	return false
}

// matchHostPathPattern checks if a path matches an allowlist/denylist pattern.
// Supports exact match and prefix match (pattern ending with /).
func matchHostPathPattern(path, pattern string) bool {
	pattern = filepath.Clean(pattern)
	if path == pattern {
		return true
	}
	if strings.HasSuffix(pattern, string(filepath.Separator)) || strings.HasPrefix(path, pattern+string(filepath.Separator)) {
		return true
	}
	if strings.HasSuffix(pattern, "/*") {
		base := strings.TrimSuffix(pattern, "/*")
		return path == base || strings.HasPrefix(path, base+string(filepath.Separator))
	}
	return false
}

type readRange struct {
	Offset int64
	Length int64
}

func parseRange(raw any) (*readRange, error) {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("range must be an object")
	}
	offset, err := int64Param(m, "offset")
	if err != nil {
		return nil, err
	}
	length, err := int64Param(m, "length")
	if err != nil {
		return nil, err
	}
	if offset < 0 || length <= 0 {
		return nil, fmt.Errorf("range offset/length must be positive")
	}
	return &readRange{Offset: offset, Length: length}, nil
}

func readLimited(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, limit))
}

func readRangeBytes(path string, rng *readRange) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(rng.Offset, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(io.LimitReader(f, rng.Length))
}

func atomicWrite(path string, data []byte) (int, error) {
	dir := filepath.Dir(path)
	tmpFile, err := os.CreateTemp(dir, ".kyvik-hostfs-*")
	if err != nil {
		return 0, err
	}
	tmpName := tmpFile.Name()
	defer os.Remove(tmpName)
	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		return 0, err
	}
	if err := tmpFile.Sync(); err != nil {
		tmpFile.Close()
		return 0, err
	}
	if err := tmpFile.Close(); err != nil {
		return 0, err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return 0, err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return 0, err
	}
	return len(data), nil
}

func hashPaths(path string, recursive bool) ([]map[string]any, error) {
	var hashes []map[string]any
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		sum, size, err := hashFile(path)
		if err != nil {
			return nil, err
		}
		return []map[string]any{{"path": path, "sha256": sum, "size": size}}, nil
	}
	if !recursive {
		return nil, nil
	}
	err = filepath.WalkDir(path, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		sum, size, err := hashFile(p)
		if err != nil {
			return err
		}
		hashes = append(hashes, map[string]any{"path": p, "sha256": sum, "size": size})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return hashes, nil
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func pathDepth(rel string) int {
	if rel == "." || rel == "" {
		return 0
	}
	return strings.Count(rel, string(filepath.Separator)) + 1
}

func isBinaryMime(mimeType string) bool {
	return strings.HasPrefix(mimeType, "application/") ||
		strings.HasPrefix(mimeType, "image/") ||
		strings.HasPrefix(mimeType, "audio/") ||
		strings.HasPrefix(mimeType, "video/")
}

func (t *Tool) auditAction(ctx context.Context, agentID, action, resource string, allowed bool, details any, err error) {
	if t.audit == nil {
		return
	}
	decision := "allowed"
	if !allowed || err != nil {
		decision = "denied"
	}
	data, _ := json.Marshal(details)
	_ = audit.LogToolCall(ctx, t.audit, agentID, "hostfs", action, resource, decision, string(data))
}

func errorResponse(id, msg string) *ktp.ToolResponse {
	resp := ktp.NewToolResponse(id, false, nil, msg, 0)
	return &resp
}

func ms(start time.Time) int64 {
	return time.Since(start).Milliseconds()
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
	switch v := raw.(type) {
	case bool:
		return v
	case json.Number:
		s := strings.ToLower(v.String())
		return s == "1" || s == "true"
	case string:
		s := strings.ToLower(strings.TrimSpace(v))
		return s == "1" || s == "true"
	default:
		return def
	}
}

func intParamDefault(params map[string]any, key string, def int) int {
	raw, ok := params[key]
	if !ok {
		return def
	}
	switch v := raw.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return int(n)
		}
	}
	return def
}

func int64Param(params map[string]any, key string) (int64, error) {
	raw, ok := params[key]
	if !ok {
		return 0, fmt.Errorf("missing required parameter: %s", key)
	}
	switch v := raw.(type) {
	case int64:
		return v, nil
	case int:
		return int64(v), nil
	case float64:
		return int64(v), nil
	case json.Number:
		return v.Int64()
	default:
		return 0, fmt.Errorf("parameter %s must be a number", key)
	}
}
