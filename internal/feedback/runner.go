// Package feedback provides post-action feedback hooks for agent tool calls.
// Hooks are shell commands that run after a tool executes and whose output
// is appended to the tool result so the agent sees diagnostic feedback.
package feedback

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

const (
	defaultTimeout    = 10 * time.Second
	defaultMaxOutput  = 4096
	feedbackPrefix    = "\n\nFeedback:\n"
)

// Runner executes post-action feedback hooks.
type Runner struct{}

// New creates a new Runner.
func New() *Runner { return &Runner{} }

// RunHooks runs all matching hooks for a tool action and returns their combined output.
// Returns empty string if no hooks match or produce output.
func (r *Runner) RunHooks(ctx context.Context, hooks types.FeedbackHooksConfig,
	tool, action string, params map[string]any, workspace string, toolSuccess bool) string {

	if !hooks.Enabled || len(hooks.Hooks) == 0 || !toolSuccess {
		return ""
	}

	maxOutput := hooks.MaxOutputBytes
	if maxOutput <= 0 {
		maxOutput = defaultMaxOutput
	}

	toolAction := tool + "." + action
	pathParam, _ := params["path"].(string)

	var outputs []string
	for _, hook := range hooks.Hooks {
		if !matchAction(hook.After, toolAction) {
			continue
		}
		if hook.Match != "" && pathParam != "" {
			matched, _ := filepath.Match(hook.Match, filepath.Base(pathParam))
			if !matched {
				continue
			}
		}

		output := r.runHook(hook, tool, action, pathParam, workspace, maxOutput)
		if output != "" {
			outputs = append(outputs, output)
		}
	}

	if len(outputs) == 0 {
		return ""
	}
	return feedbackPrefix + strings.Join(outputs, "\n\n")
}

// runHook executes a single hook command and returns its output.
func (r *Runner) runHook(hook types.FeedbackHook, tool, action, path, workspace string, maxOutput int) string {
	timeout := defaultTimeout
	if hook.TimeoutSec > 0 {
		timeout = time.Duration(hook.TimeoutSec) * time.Second
	}

	cmd := expandTemplate(hook.Run, tool, action, path, workspace)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, "sh", "-c", cmd).CombinedOutput()

	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Sprintf("(hook timed out after %ds)", int(timeout.Seconds()))
	}

	result := strings.TrimSpace(string(out))

	// Include output even on non-zero exit — that's the error feedback we want.
	// Only suppress if there's truly no output and the error is just an exit code.
	if result == "" && err != nil {
		result = fmt.Sprintf("(hook error: %v)", err)
	}

	if len(result) > maxOutput {
		result = result[:maxOutput] + "\n(output truncated)"
	}

	return result
}

// expandTemplate replaces template variables in a command string.
// Values are shell-escaped to prevent injection.
func expandTemplate(cmd, tool, action, path, workspace string) string {
	r := strings.NewReplacer(
		"{{workspace}}", shellEscape(workspace),
		"{{path}}", shellEscape(path),
		"{{tool}}", shellEscape(tool),
		"{{action}}", shellEscape(action),
	)
	return r.Replace(cmd)
}

// shellEscape wraps a value in single quotes for safe shell interpolation.
func shellEscape(s string) string {
	if s == "" {
		return "''"
	}
	// Replace single quotes with '"'"' (end quote, literal quote, start quote).
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

// matchAction checks if a tool.action string matches a pattern with wildcard support.
func matchAction(pattern, toolAction string) bool {
	if pattern == "*.*" {
		return true
	}
	patParts := strings.SplitN(pattern, ".", 2)
	actParts := strings.SplitN(toolAction, ".", 2)
	if len(patParts) != 2 || len(actParts) != 2 {
		return false
	}
	if patParts[0] != "*" && patParts[0] != actParts[0] {
		return false
	}
	if patParts[1] != "*" && patParts[1] != actParts[1] {
		return false
	}
	return true
}
