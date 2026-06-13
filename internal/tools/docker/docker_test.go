package docker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kkjorsvik/kyvik/internal/ktp"
)

func skipIfNoDocker(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not found")
	}
}

func newTestTool(dir string) *Tool {
	return New(func(agentID string) (string, error) {
		return dir, nil
	})
}

func makeReq(action string, params map[string]any) ktp.ToolRequest {
	return ktp.NewToolRequest("test-agent", "docker", action, params)
}

func TestDeclaration(t *testing.T) {
	tool := newTestTool(t.TempDir())
	decl := tool.Declaration()
	if decl.Name != "docker" {
		t.Errorf("expected name docker, got %s", decl.Name)
	}
	if decl.Version != "1.0.0" {
		t.Errorf("expected version 1.0.0, got %s", decl.Version)
	}
	if decl.MinTier != ktp.TierOperator {
		t.Errorf("expected min tier operator, got %s", decl.MinTier)
	}
	if len(decl.Actions) != 10 {
		t.Errorf("expected 10 actions, got %d", len(decl.Actions))
	}
}

func TestExecute_UnknownAction(t *testing.T) {
	tool := newTestTool(t.TempDir())
	resp, err := tool.Execute(context.Background(), makeReq("nonexistent", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Error("expected unknown action to fail")
	}
	if !strings.Contains(resp.Error, "unknown action") {
		t.Errorf("expected 'unknown action' error, got: %s", resp.Error)
	}
}

func TestExecute_Ps(t *testing.T) {
	skipIfNoDocker(t)
	tool := newTestTool(t.TempDir())
	resp, err := tool.Execute(context.Background(), makeReq("ps", nil))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
	result := resp.Result.(map[string]any)
	if _, ok := result["containers"]; !ok {
		t.Error("expected containers key in result")
	}
}

func TestExecute_Images(t *testing.T) {
	skipIfNoDocker(t)
	tool := newTestTool(t.TempDir())
	resp, err := tool.Execute(context.Background(), makeReq("images", nil))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
	result := resp.Result.(map[string]any)
	if _, ok := result["images"]; !ok {
		t.Error("expected images key in result")
	}
}

func TestExecute_Run(t *testing.T) {
	skipIfNoDocker(t)
	dir := t.TempDir()
	tool := newTestTool(dir)

	resp, err := tool.Execute(context.Background(), makeReq("run", map[string]any{
		"image":   "alpine:latest",
		"command": []any{"echo", "hello-kyvik"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
	result := resp.Result.(map[string]any)
	stdout, _ := result["stdout"].(string)
	if !strings.Contains(stdout, "hello-kyvik") {
		t.Errorf("expected stdout to contain 'hello-kyvik', got: %s", stdout)
	}
}

func TestExecute_Build(t *testing.T) {
	skipIfNoDocker(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM alpine:latest\nRUN echo built\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := newTestTool(dir)
	resp, err := tool.Execute(context.Background(), makeReq("build", map[string]any{
		"tag": "kyvik-test-build:latest",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
	result := resp.Result.(map[string]any)
	if result["tag"] != "kyvik-test-build:latest" {
		t.Errorf("expected tag kyvik-test-build:latest, got %v", result["tag"])
	}

	// Cleanup.
	exec.Command("docker", "rmi", "kyvik-test-build:latest").Run()
}

func TestExecute_Lifecycle(t *testing.T) {
	skipIfNoDocker(t)
	dir := t.TempDir()
	tool := newTestTool(dir)

	// Run a detached container.
	resp, err := tool.Execute(context.Background(), makeReq("run", map[string]any{
		"image":   "alpine:latest",
		"command": []any{"sleep", "30"},
		"detach":  true,
		"name":    "lifecycle-test",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("run failed: %s", resp.Error)
	}
	result := resp.Result.(map[string]any)
	containerID := result["container_id"].(string)
	t.Cleanup(func() {
		exec.Command("docker", "rm", "-f", containerID).Run()
	})

	// Logs.
	resp, err = tool.Execute(context.Background(), makeReq("logs", map[string]any{
		"container": containerID,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("logs failed: %s", resp.Error)
	}

	// Stop.
	resp, err = tool.Execute(context.Background(), makeReq("stop", map[string]any{
		"container": containerID,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("stop failed: %s", resp.Error)
	}

	// Rm.
	resp, err = tool.Execute(context.Background(), makeReq("rm", map[string]any{
		"container": containerID,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("rm failed: %s", resp.Error)
	}
}

func TestExecute_Pull(t *testing.T) {
	skipIfNoDocker(t)
	tool := newTestTool(t.TempDir())

	resp, err := tool.Execute(context.Background(), makeReq("pull", map[string]any{
		"image": "alpine:latest",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
	result := resp.Result.(map[string]any)
	if result["image"] != "alpine:latest" {
		t.Errorf("expected image alpine:latest, got %v", result["image"])
	}
}

func TestExecute_VolumeValidation(t *testing.T) {
	dir := t.TempDir()
	tool := newTestTool(dir)

	// Path traversal in volume mount should be rejected.
	resp, err := tool.Execute(context.Background(), makeReq("run", map[string]any{
		"image":   "alpine:latest",
		"volumes": []any{"../../etc:/mnt"},
		"command": []any{"ls", "/mnt"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Error("expected volume path traversal to be rejected")
	}
	if !strings.Contains(resp.Error, "traversal") {
		t.Errorf("expected traversal error, got: %s", resp.Error)
	}

	// Absolute host path should be rejected.
	resp, err = tool.Execute(context.Background(), makeReq("run", map[string]any{
		"image":   "alpine:latest",
		"volumes": []any{"/etc/passwd:/mnt/passwd"},
		"command": []any{"cat", "/mnt/passwd"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Error("expected absolute host path to be rejected")
	}
}

func TestExecute_BlockedFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"privileged", []string{"run", "--privileged", "alpine"}},
		{"pid host", []string{"run", "--pid=host", "alpine"}},
		{"network host", []string{"run", "--network=host", "alpine"}},
		{"ipc host", []string{"run", "--ipc=host", "alpine"}},
		{"cap-add", []string{"run", "--cap-add=SYS_ADMIN", "alpine"}},
		{"device", []string{"run", "--device=/dev/sda", "alpine"}},
		{"system", []string{"system", "prune"}},
		{"network", []string{"network", "create", "test"}},
		{"volume", []string{"volume", "create", "test"}},
		{"exec", []string{"exec", "container", "sh"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateArgs(tt.args)
			if err == nil {
				t.Errorf("expected error for args %v", tt.args)
			}
		})
	}
}

func TestValidateContainerName(t *testing.T) {
	tests := []struct {
		name  string
		valid bool
	}{
		{"my-container", true},
		{"abc123", true},
		{"a1b2c3d4e5f6", true},
		{"kyvik-agent-test", true},
		{"container.name", true},
		{"", false},
		{"-starts-with-dash", false},
		{"has spaces", false},
		{"has;semicolon", false},
		{"has$(cmd)", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateContainerName(tt.name)
			if tt.valid && err != nil {
				t.Errorf("expected valid, got error: %v", err)
			}
			if !tt.valid && err == nil {
				t.Error("expected invalid, got nil error")
			}
		})
	}
}

func TestValidateVolumeMounts(t *testing.T) {
	dir := t.TempDir()
	// Create a subdirectory to use as valid source.
	os.MkdirAll(filepath.Join(dir, "data"), 0755)

	tests := []struct {
		name    string
		volume  string
		wantErr bool
	}{
		{"valid relative", "data:/mnt", false},
		{"path traversal", "../../etc:/mnt", true},
		{"absolute path", "/etc/passwd:/mnt", true},
		{"no colon", "just-a-path", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateVolumeMounts(dir, []string{tt.volume})
			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("expected no error, got: %v", err)
			}
		})
	}
}
