package hostfs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kkjorsvik/kyvik/internal/ktp"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

func newTestTool(t *testing.T, cfg *HostPathConfig) *Tool {
	t.Helper()
	return New(Config{}, WithAllowlistFunc(func(agentID string) (*HostPathConfig, error) {
		return cfg, nil
	}))
}

func TestHostFS_ReadWithinAllowlist(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "file.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := newTestTool(t, &HostPathConfig{
		Read: []string{tmp + string(filepath.Separator)},
	})
	resp, err := tool.Execute(context.Background(), ktp.NewToolRequest("agent-1", "hostfs", "read", map[string]any{
		"path": path,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
}

func TestHostFS_ReadOutsideAllowlistDenied(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "file.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := newTestTool(t, &HostPathConfig{Read: []string{"/other"}})
	resp, err := tool.Execute(context.Background(), ktp.NewToolRequest("agent-1", "hostfs", "read", map[string]any{
		"path": path,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Fatalf("expected denied read, got success")
	}
}

func TestHostFS_SymlinkTraversalDenied(t *testing.T) {
	base := t.TempDir()
	allowed := filepath.Join(base, "allowed")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(allowed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(allowed, "escape.txt")
	if err := os.Symlink(outsideFile, link); err != nil {
		t.Fatal(err)
	}

	tool := newTestTool(t, &HostPathConfig{Read: []string{allowed + string(filepath.Separator)}})
	resp, err := tool.Execute(context.Background(), ktp.NewToolRequest("agent-1", "hostfs", "read", map[string]any{
		"path": link,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Fatalf("expected symlink traversal to be denied")
	}
}

func TestHostFS_PathTraversalDenied(t *testing.T) {
	base := t.TempDir()
	allowed := filepath.Join(base, "allowed")
	if err := os.MkdirAll(allowed, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(allowed, "..", "outside.txt")
	tool := newTestTool(t, &HostPathConfig{Read: []string{allowed + string(filepath.Separator)}})
	resp, err := tool.Execute(context.Background(), ktp.NewToolRequest("agent-1", "hostfs", "read", map[string]any{
		"path": path,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Fatalf("expected path traversal to be denied")
	}
}

func TestHostFS_DangerousPathsBlocked(t *testing.T) {
	base := t.TempDir()
	danger := filepath.Join(base, ".ssh")
	if err := os.MkdirAll(danger, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(danger, "config")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := newTestTool(t, &HostPathConfig{Read: []string{base + string(filepath.Separator)}})
	resp, err := tool.Execute(context.Background(), ktp.NewToolRequest("agent-1", "hostfs", "read", map[string]any{
		"path": path,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Fatalf("expected dangerous path to be blocked")
	}
}

func TestHostFS_WriteAtomicOverwrite(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "out.txt")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := newTestTool(t, &HostPathConfig{Write: []string{base + string(filepath.Separator)}})
	resp, err := tool.Execute(context.Background(), ktp.NewToolRequest("agent-1", "hostfs", "write", map[string]any{
		"path":    path,
		"content": "new-content",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected write success, got %s", resp.Error)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new-content" {
		t.Fatalf("expected content to be overwritten, got %q", string(data))
	}
}

type mockAgentStore struct {
	agents map[string]*types.AgentConfig
}

func (m mockAgentStore) GetAgent(_ context.Context, id string) (*types.AgentConfig, error) {
	agent, ok := m.agents[id]
	if !ok {
		return nil, types.ErrNotFound
	}
	return agent, nil
}

func TestHostFS_DeleteRequiresCapability(t *testing.T) {
	tool := New(Config{})
	gate := ktp.NewPermissionGate(mockAgentStore{
		agents: map[string]*types.AgentConfig{
			"a1": {
				ID:       "a1",
				Name:     "a1",
				Template: "admin",
				HostFilesystem: &types.HostFilesystemConfig{
					Allowlist: []types.HostFilesystemAllowlistEntry{
						{Path: "/tmp/allowed", Access: "write"},
					},
				},
			},
		},
	}, nil)
	result, err := gate.Check(context.Background(), "a1", tool.Declaration(), "delete", map[string]any{
		"path": "/tmp/allowed/file.txt",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Allowed {
		t.Fatalf("expected delete to be denied without explicit delete capability")
	}
	if !strings.Contains(result.Reason, "missing capability") {
		t.Fatalf("expected missing capability, got %q", result.Reason)
	}
}

func TestHostFS_MinTierAdmin(t *testing.T) {
	tool := New(Config{})
	gate := ktp.NewPermissionGate(mockAgentStore{
		agents: map[string]*types.AgentConfig{
			"worker":   {ID: "worker", Template: "worker"},
			"operator": {ID: "operator", Template: "operator"},
		},
	}, nil)
	for _, id := range []string{"worker", "operator"} {
		result, err := gate.Check(context.Background(), id, tool.Declaration(), "read", map[string]any{
			"path": "/tmp/allowed/file.txt",
		})
		if err != nil {
			t.Fatal(err)
		}
		if result.Allowed {
			t.Fatalf("expected %s to be denied by tier", id)
		}
	}
}
