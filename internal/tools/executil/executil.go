// Package executil provides shared process execution utilities for KTP tools
// that spawn child processes (shell, code).
package executil

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// MaxOutputSize is the maximum bytes captured from stdout/stderr.
const MaxOutputSize = 1 << 20 // 1 MB

// ProcessConfig describes how to spawn and run a child process.
type ProcessConfig struct {
	Command    string
	Args       []string
	WorkingDir string
	Env        []string
	Timeout    time.Duration
}

// ProcessResult captures the outcome of a child process execution.
type ProcessResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
	ElapsedMs int64 `json:"elapsed_ms"`
	TimedOut bool   `json:"timed_out"`
}

// RunProcess spawns a child process with timeout, output capture, and process
// group isolation. On timeout, the entire process group is killed.
func RunProcess(ctx context.Context, cfg ProcessConfig) (*ProcessResult, error) {
	if cfg.Command == "" {
		return nil, fmt.Errorf("command must not be empty")
	}

	// Create timeout context.
	execCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	cmd := exec.CommandContext(execCtx, cfg.Command, cfg.Args...)
	if cfg.WorkingDir != "" {
		cmd.Dir = cfg.WorkingDir
	}
	if len(cfg.Env) > 0 {
		cmd.Env = cfg.Env
	}

	// Configure process group isolation.
	ConfigureProcGroup(cmd)

	// When the context expires, kill the entire process group (not just the
	// root process). This ensures child processes (e.g. sleep spawned by bash)
	// are also terminated. WaitDelay gives a grace period for I/O after Cancel.
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			KillProcGroup(cmd.Process.Pid)
		}
		return nil
	}
	cmd.WaitDelay = 2 * time.Second

	// Capture stdout/stderr with size limits.
	stdout := &limitedWriter{limit: MaxOutputSize}
	stderr := &limitedWriter{limit: MaxOutputSize}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	start := time.Now()

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start process: %w", err)
	}

	waitErr := cmd.Wait()
	elapsed := time.Since(start).Milliseconds()

	result := &ProcessResult{
		Stdout:    stdout.String(),
		Stderr:    stderr.String(),
		ElapsedMs: elapsed,
	}

	// Check for timeout (process group already killed by cmd.Cancel).
	if execCtx.Err() == context.DeadlineExceeded {
		result.TimedOut = true
		result.ExitCode = -1
		return result, nil
	}

	// Extract exit code.
	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		} else {
			return nil, fmt.Errorf("process wait error: %w", waitErr)
		}
	}

	return result, nil
}

// BuildMinimalEnv creates a minimal environment for child processes.
// Parent environment is NOT inherited.
func BuildMinimalEnv(path, home, tmpdir, user string) []string {
	env := []string{
		"PATH=" + path,
		"HOME=" + home,
		"TMPDIR=" + tmpdir,
	}
	if user != "" {
		env = append(env, "USER="+user)
	}
	return env
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

func (w *limitedWriter) String() string { return w.buf.String() }

// SafePath validates and resolves a relative user-provided path within the workspace.
// After lexical checks it resolves symlinks to prevent symlink escapes.
func SafePath(workspace, userPath string) (string, error) {
	if userPath == "" {
		return "", fmt.Errorf("path must not be empty")
	}
	if filepath.IsAbs(userPath) {
		return "", fmt.Errorf("absolute paths are not allowed")
	}

	cleaned := filepath.Clean(userPath)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path traversal is not allowed")
	}

	absPath := filepath.Join(workspace, cleaned)
	// Verify the resolved path is within the workspace.
	if absPath != workspace && !strings.HasPrefix(absPath, workspace+string(filepath.Separator)) {
		return "", fmt.Errorf("path traversal is not allowed")
	}

	// Resolve symlinks to prevent symlink escapes.
	realWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return "", fmt.Errorf("failed to resolve workspace: %w", err)
	}
	realPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		// Path may not exist yet (write/mkdir case) — walk up to find the
		// nearest existing ancestor and resolve from there.
		remaining := absPath
		var tail []string
		for remaining != workspace {
			parent := filepath.Dir(remaining)
			tail = append([]string{filepath.Base(remaining)}, tail...)
			resolved, resolveErr := filepath.EvalSymlinks(parent)
			if resolveErr == nil {
				realPath = filepath.Join(append([]string{resolved}, tail...)...)
				break
			}
			remaining = parent
		}
		if realPath == "" {
			// All ancestors up to workspace failed; fall back to workspace + tail.
			realPath = filepath.Join(append([]string{realWorkspace}, tail...)...)
		}
	}
	if realPath != realWorkspace && !strings.HasPrefix(realPath, realWorkspace+string(filepath.Separator)) {
		return "", fmt.Errorf("path traversal is not allowed (symlink escape)")
	}

	return absPath, nil
}
