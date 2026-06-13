// Package code implements a KTP tool for executing code snippets and scripts
// within an agent's workspace. Supports python3, bash, and go via interpreter
// basenames resolved through the system PATH (no shell allowlist needed).
package code

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/kkjorsvik/kyvik/internal/ktp"
	"github.com/kkjorsvik/kyvik/internal/tools/executil"
)

// WorkspaceFunc resolves an agent's workspace directory.
type WorkspaceFunc func(agentID string) (string, error)

type languageConfig struct {
	interpreter string
	extension   string
	runPrefix   []string // e.g. ["run"] for "go run"
}

var supportedLanguages = map[string]languageConfig{
	"python3": {interpreter: "python3", extension: ".py"},
	"bash":    {interpreter: "bash", extension: ".sh"},
	"go":      {interpreter: "go", extension: ".go", runPrefix: []string{"run"}},
}

// extensionToLanguage maps file extensions to language names.
var extensionToLanguage = map[string]string{
	".py": "python3",
	".sh": "bash",
	".go": "go",
}

// CodeTool implements ktp.Tool for executing code snippets and script files.
type CodeTool struct {
	workspace WorkspaceFunc
}

// New creates a CodeTool with the given workspace resolver.
func New(workspace WorkspaceFunc) *CodeTool {
	return &CodeTool{workspace: workspace}
}

// Declaration returns the code tool's KTP declaration.
func (t *CodeTool) Declaration() ktp.ToolDeclaration {
	return ktp.ToolDeclaration{
		Name:        "code",
		Version:     "1.0.0",
		Description: "Execute code snippets or script files within the agent workspace",
		MinTier:      ktp.TierOperator,
		DefaultTiers: []string{ktp.TierAdmin},
		Actions: []ktp.ActionSpec{
			{
				Name:        "run",
				Description: "Execute a code snippet in a supported language",
				Destructive: true,
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"language":        {Type: "string", Description: "Programming language", Enum: []string{"python3", "bash", "go"}},
						"code":            {Type: "string", Description: "Code to execute"},
						"timeout_seconds": {Type: "integer", Description: "Timeout in seconds (default 60, max 600)"},
						"args":            {Type: "array", Items: &ktp.JSONSchema{Type: "string"}, Description: "Arguments passed to the script"},
					},
					Required: []string{"language", "code"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"stdout":        {Type: "string"},
						"stderr":        {Type: "string"},
						"exit_code":     {Type: "integer"},
						"elapsed_ms":    {Type: "integer"},
						"files_created": {Type: "array", Items: &ktp.JSONSchema{Type: "string"}},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "code", Access: "execute", Resource: "*"}},
			},
			{
				Name:        "run_file",
				Description: "Execute an existing script file from the workspace",
				Destructive: true,
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"path":            {Type: "string", Description: "Path to script file (relative to workspace)"},
						"args":            {Type: "array", Items: &ktp.JSONSchema{Type: "string"}, Description: "Arguments passed to the script"},
						"timeout_seconds": {Type: "integer", Description: "Timeout in seconds (default 60, max 600)"},
					},
					Required: []string{"path"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"stdout":        {Type: "string"},
						"stderr":        {Type: "string"},
						"exit_code":     {Type: "integer"},
						"elapsed_ms":    {Type: "integer"},
						"files_created": {Type: "array", Items: &ktp.JSONSchema{Type: "string"}},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "code", Access: "execute", Resource: "*"}},
			},
		},
	}
}

const (
	defaultTimeout = 60 * time.Second
	maxTimeout     = 600 * time.Second
)

// Execute dispatches to the requested action.
func (t *CodeTool) Execute(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	switch req.Action {
	case "run":
		return t.run(ctx, req)
	case "run_file":
		return t.runFile(ctx, req)
	default:
		return errResp(req.ID, fmt.Sprintf("unknown action: %s", req.Action)), nil
	}
}

func (t *CodeTool) run(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	start := time.Now()

	language, err := stringParam(req.Parameters, "language")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	code, err := stringParam(req.Parameters, "code")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	langCfg, ok := supportedLanguages[language]
	if !ok {
		return errResp(req.ID, fmt.Sprintf("unsupported language: %s", language)), nil
	}

	args := stringSliceParam(req.Parameters, "args")
	timeoutSec := intParamDefault(req.Parameters, "timeout_seconds", 60)

	// Resolve workspace.
	workspace, err := t.workspace(req.AgentID)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("failed to resolve workspace: %s", err)), nil
	}

	// Ensure tmp directory exists.
	tmpDir := filepath.Join(workspace, "tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return errResp(req.ID, fmt.Sprintf("failed to create tmp directory: %s", err)), nil
	}

	// Write code to temp file.
	tmpFile := filepath.Join(tmpDir, ulid.Make().String()+langCfg.extension)
	if err := os.WriteFile(tmpFile, []byte(code), 0o644); err != nil {
		return errResp(req.ID, fmt.Sprintf("failed to write temp file: %s", err)), nil
	}
	defer os.Remove(tmpFile)

	// Snapshot files before execution.
	before := snapshotFiles(workspace)

	// Build command args.
	cmdArgs := buildCmdArgs(langCfg, tmpFile, args)

	// Execute.
	timeout := clampTimeout(timeoutSec)
	result, execErr := executil.RunProcess(ctx, executil.ProcessConfig{
		Command:    langCfg.interpreter,
		Args:       cmdArgs,
		WorkingDir: workspace,
		Env:        buildEnv(workspace),
		Timeout:    timeout,
	})
	if execErr != nil {
		return errResp(req.ID, execErr.Error()), nil
	}

	// Diff files to find newly created ones.
	created := diffFiles(workspace, before, tmpFile)

	resp := ktp.NewToolResponse(req.ID, true, map[string]any{
		"stdout":        result.Stdout,
		"stderr":        result.Stderr,
		"exit_code":     result.ExitCode,
		"elapsed_ms":    result.ElapsedMs,
		"files_created": created,
	}, "", time.Since(start).Milliseconds())
	return &resp, nil
}

func (t *CodeTool) runFile(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	start := time.Now()

	path, err := stringParam(req.Parameters, "path")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	args := stringSliceParam(req.Parameters, "args")
	timeoutSec := intParamDefault(req.Parameters, "timeout_seconds", 60)

	workspace, err := t.workspace(req.AgentID)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("failed to resolve workspace: %s", err)), nil
	}

	// Validate path is within workspace.
	absPath, err := executil.SafePath(workspace, path)
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	// Verify file exists and is a regular file.
	fi, err := os.Stat(absPath)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("file not found: %s", err)), nil
	}
	if !fi.Mode().IsRegular() {
		return errResp(req.ID, fmt.Sprintf("path is not a regular file: %s", path)), nil
	}

	// Detect language from extension.
	ext := filepath.Ext(absPath)
	language, ok := extensionToLanguage[ext]
	if !ok {
		return errResp(req.ID, fmt.Sprintf("unsupported file extension: %s", ext)), nil
	}

	langCfg := supportedLanguages[language]

	// Snapshot files before execution.
	before := snapshotFiles(workspace)

	// Build command args.
	cmdArgs := buildCmdArgs(langCfg, absPath, args)

	// Execute.
	timeout := clampTimeout(timeoutSec)
	result, execErr := executil.RunProcess(ctx, executil.ProcessConfig{
		Command:    langCfg.interpreter,
		Args:       cmdArgs,
		WorkingDir: workspace,
		Env:        buildEnv(workspace),
		Timeout:    timeout,
	})
	if execErr != nil {
		return errResp(req.ID, execErr.Error()), nil
	}

	created := diffFiles(workspace, before, "")

	resp := ktp.NewToolResponse(req.ID, true, map[string]any{
		"stdout":        result.Stdout,
		"stderr":        result.Stderr,
		"exit_code":     result.ExitCode,
		"elapsed_ms":    result.ElapsedMs,
		"files_created": created,
	}, "", time.Since(start).Milliseconds())
	return &resp, nil
}

// buildCmdArgs constructs the argument list for the interpreter.
func buildCmdArgs(cfg languageConfig, filePath string, userArgs []string) []string {
	var args []string
	args = append(args, cfg.runPrefix...)
	args = append(args, filePath)
	args = append(args, userArgs...)
	return args
}

// buildEnv creates a minimal environment for code execution.
func buildEnv(workspace string) []string {
	return executil.BuildMinimalEnv(
		"/usr/local/bin:/usr/bin:/bin",
		workspace,
		filepath.Join(workspace, "tmp"),
		"",
	)
}

// clampTimeout clamps timeout to [1, maxTimeout].
func clampTimeout(seconds int) time.Duration {
	timeout := time.Duration(seconds) * time.Second
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	if timeout > maxTimeout {
		timeout = maxTimeout
	}
	return timeout
}

// snapshotFiles collects all file paths within workspace.
func snapshotFiles(workspace string) map[string]struct{} {
	files := make(map[string]struct{})
	_ = filepath.WalkDir(workspace, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			files[path] = struct{}{}
		}
		return nil
	})
	return files
}

// diffFiles finds files in workspace that weren't present before execution.
// Excludes the temp file used for code execution.
func diffFiles(workspace string, before map[string]struct{}, excludeFile string) []string {
	var created []string
	_ = filepath.WalkDir(workspace, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if path == excludeFile {
			return nil
		}
		if _, existed := before[path]; !existed {
			rel, relErr := filepath.Rel(workspace, path)
			if relErr != nil {
				return nil
			}
			created = append(created, rel)
		}
		return nil
	})
	return created
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

func errResp(reqID, msg string) *ktp.ToolResponse {
	resp := ktp.NewToolResponse(reqID, false, nil, msg, 0)
	return &resp
}
