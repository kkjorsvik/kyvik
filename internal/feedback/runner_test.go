package feedback

import (
	"context"
	"strings"
	"testing"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

func TestMatchAction(t *testing.T) {
	tests := []struct {
		pattern, action string
		want            bool
	}{
		{"file.edit", "file.edit", true},
		{"file.edit", "file.write", false},
		{"file.*", "file.edit", true},
		{"file.*", "file.write", true},
		{"file.*", "shell.exec", false},
		{"*.edit", "file.edit", true},
		{"*.edit", "hostfs.edit", true},
		{"*.edit", "file.write", false},
		{"*.*", "anything.here", true},
		{"bad", "file.edit", false},
	}
	for _, tt := range tests {
		if got := matchAction(tt.pattern, tt.action); got != tt.want {
			t.Errorf("matchAction(%q, %q) = %v, want %v", tt.pattern, tt.action, got, tt.want)
		}
	}
}

func TestShellEscape(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"hello", "'hello'"},
		{"", "''"},
		{"it's", "'it'\"'\"'s'"},
		{"/path/to/file", "'/path/to/file'"},
	}
	for _, tt := range tests {
		if got := shellEscape(tt.input); got != tt.want {
			t.Errorf("shellEscape(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestExpandTemplate(t *testing.T) {
	cmd := "cd {{workspace}} && lint {{path}} --tool={{tool}} --action={{action}}"
	got := expandTemplate(cmd, "file", "edit", "/tmp/foo.go", "/home/ws")

	if !strings.Contains(got, "'/home/ws'") {
		t.Errorf("workspace not expanded: %s", got)
	}
	if !strings.Contains(got, "'/tmp/foo.go'") {
		t.Errorf("path not expanded: %s", got)
	}
	if !strings.Contains(got, "'file'") {
		t.Errorf("tool not expanded: %s", got)
	}
	if !strings.Contains(got, "'edit'") {
		t.Errorf("action not expanded: %s", got)
	}
}

func TestRunHooks_Disabled(t *testing.T) {
	r := New()
	cfg := types.FeedbackHooksConfig{
		Enabled: false,
		Hooks:   []types.FeedbackHook{{After: "*.*", Run: "echo hello"}},
	}
	got := r.RunHooks(context.Background(), cfg, "file", "edit", nil, "/tmp", true)
	if got != "" {
		t.Errorf("expected empty for disabled, got %q", got)
	}
}

func TestRunHooks_ToolFailure(t *testing.T) {
	r := New()
	cfg := types.FeedbackHooksConfig{
		Enabled: true,
		Hooks:   []types.FeedbackHook{{After: "*.*", Run: "echo hello"}},
	}
	got := r.RunHooks(context.Background(), cfg, "file", "edit", nil, "/tmp", false)
	if got != "" {
		t.Errorf("expected empty for failed tool, got %q", got)
	}
}

func TestRunHooks_BasicExecution(t *testing.T) {
	r := New()
	cfg := types.FeedbackHooksConfig{
		Enabled: true,
		Hooks: []types.FeedbackHook{
			{After: "file.edit", Run: "echo 'lint output here'"},
		},
	}
	got := r.RunHooks(context.Background(), cfg, "file", "edit", nil, "/tmp", true)
	if !strings.Contains(got, "Feedback:") {
		t.Errorf("missing feedback prefix: %q", got)
	}
	if !strings.Contains(got, "lint output here") {
		t.Errorf("missing hook output: %q", got)
	}
}

func TestRunHooks_NoMatch(t *testing.T) {
	r := New()
	cfg := types.FeedbackHooksConfig{
		Enabled: true,
		Hooks: []types.FeedbackHook{
			{After: "file.write", Run: "echo should not run"},
		},
	}
	got := r.RunHooks(context.Background(), cfg, "file", "edit", nil, "/tmp", true)
	if got != "" {
		t.Errorf("expected empty for non-matching hook, got %q", got)
	}
}

func TestRunHooks_MatchGlob(t *testing.T) {
	r := New()
	cfg := types.FeedbackHooksConfig{
		Enabled: true,
		Hooks: []types.FeedbackHook{
			{After: "file.edit", Match: "*.go", Run: "echo go file"},
			{After: "file.edit", Match: "*.py", Run: "echo py file"},
		},
	}
	params := map[string]any{"path": "main.go"}
	got := r.RunHooks(context.Background(), cfg, "file", "edit", params, "/tmp", true)
	if !strings.Contains(got, "go file") {
		t.Errorf("expected go hook to match: %q", got)
	}
	if strings.Contains(got, "py file") {
		t.Errorf("py hook should not match: %q", got)
	}
}

func TestRunHooks_NonZeroExit(t *testing.T) {
	r := New()
	cfg := types.FeedbackHooksConfig{
		Enabled: true,
		Hooks: []types.FeedbackHook{
			{After: "*.*", Run: "echo 'error on line 5' && exit 1"},
		},
	}
	got := r.RunHooks(context.Background(), cfg, "file", "edit", nil, "/tmp", true)
	if !strings.Contains(got, "error on line 5") {
		t.Errorf("expected error output even on non-zero exit: %q", got)
	}
}

func TestRunHooks_Timeout(t *testing.T) {
	r := New()
	cfg := types.FeedbackHooksConfig{
		Enabled: true,
		Hooks: []types.FeedbackHook{
			{After: "*.*", Run: "sleep 10", TimeoutSec: 1},
		},
	}
	got := r.RunHooks(context.Background(), cfg, "file", "edit", nil, "/tmp", true)
	if !strings.Contains(got, "timed out") {
		t.Errorf("expected timeout message: %q", got)
	}
}

func TestRunHooks_OutputTruncation(t *testing.T) {
	r := New()
	cfg := types.FeedbackHooksConfig{
		Enabled:        true,
		MaxOutputBytes: 20,
		Hooks: []types.FeedbackHook{
			{After: "*.*", Run: "echo 'this is a very long output that should be truncated'"},
		},
	}
	got := r.RunHooks(context.Background(), cfg, "file", "edit", nil, "/tmp", true)
	if !strings.Contains(got, "(output truncated)") {
		t.Errorf("expected truncation marker: %q", got)
	}
}

func TestRunHooks_MultipleMatches(t *testing.T) {
	r := New()
	cfg := types.FeedbackHooksConfig{
		Enabled: true,
		Hooks: []types.FeedbackHook{
			{After: "file.edit", Run: "echo 'hook1'"},
			{After: "file.*", Run: "echo 'hook2'"},
		},
	}
	got := r.RunHooks(context.Background(), cfg, "file", "edit", nil, "/tmp", true)
	if !strings.Contains(got, "hook1") || !strings.Contains(got, "hook2") {
		t.Errorf("expected both hooks to match: %q", got)
	}
}

func TestRunHooks_WildcardAll(t *testing.T) {
	r := New()
	cfg := types.FeedbackHooksConfig{
		Enabled: true,
		Hooks: []types.FeedbackHook{
			{After: "*.*", Run: "echo 'global hook'"},
		},
	}
	got := r.RunHooks(context.Background(), cfg, "shell", "exec", nil, "/tmp", true)
	if !strings.Contains(got, "global hook") {
		t.Errorf("expected global hook to match: %q", got)
	}
}
