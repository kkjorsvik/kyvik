package retention

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/config"
)

func workspaceTestConfig(wsRoot string) config.RetentionConfig {
	cfg := defaultTestConfig()
	cfg.WorkspaceGraceDays = 7
	cfg.WorkspaceRoot = wsRoot
	return cfg
}

func TestCleanOrphanWorkspaces_DeletesAfterGracePeriod(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	wsRoot := t.TempDir()
	os.MkdirAll(filepath.Join(wsRoot, "orphan-agent-id"), 0o755)
	os.WriteFile(filepath.Join(wsRoot, "orphan-agent-id", "file.txt"), []byte("data"), 0o644)

	ss := &testStateStore{db}
	cfg := workspaceTestConfig(wsRoot)
	p := New(db, ss, cfg)

	// Pre-seed orphan state with a firstSeen time older than 7 days
	oldTime := time.Now().Add(-8 * 24 * time.Hour)
	orphanState := map[string]time.Time{"orphan-agent-id": oldTime}
	data, _ := json.Marshal(orphanState)
	ss.SetSystemState(ctx, "workspace_orphans", string(data))

	deleted, errs := p.cleanOrphanWorkspaces(ctx)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if deleted != 1 {
		t.Errorf("expected 1 deleted, got %d", deleted)
	}

	// Verify directory was removed
	if _, err := os.Stat(filepath.Join(wsRoot, "orphan-agent-id")); !os.IsNotExist(err) {
		t.Error("expected orphan workspace directory to be removed")
	}
}

func TestCleanOrphanWorkspaces_PreservesWithinGracePeriod(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	wsRoot := t.TempDir()
	os.MkdirAll(filepath.Join(wsRoot, "recent-orphan"), 0o755)
	os.WriteFile(filepath.Join(wsRoot, "recent-orphan", "file.txt"), []byte("data"), 0o644)

	ss := &testStateStore{db}
	cfg := workspaceTestConfig(wsRoot)
	p := New(db, ss, cfg)

	// Pre-seed orphan state with a firstSeen time within 7 days
	recentTime := time.Now().Add(-2 * 24 * time.Hour)
	orphanState := map[string]time.Time{"recent-orphan": recentTime}
	data, _ := json.Marshal(orphanState)
	ss.SetSystemState(ctx, "workspace_orphans", string(data))

	deleted, errs := p.cleanOrphanWorkspaces(ctx)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if deleted != 0 {
		t.Errorf("expected 0 deleted, got %d", deleted)
	}

	// Verify directory still exists
	if _, err := os.Stat(filepath.Join(wsRoot, "recent-orphan")); os.IsNotExist(err) {
		t.Error("expected orphan workspace directory to still exist")
	}

	// Verify orphan state was preserved
	raw, _ := ss.GetSystemState(ctx, "workspace_orphans")
	var state map[string]time.Time
	json.Unmarshal([]byte(raw), &state)
	if _, ok := state["recent-orphan"]; !ok {
		t.Error("expected recent-orphan to remain in orphan state")
	}
}

func TestCleanOrphanWorkspaces_SkipsActiveAgents(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	// Insert an active agent
	agentID := "active-agent-ws-test"
	db.ExecContext(ctx,
		"INSERT INTO agents (id, name, model_provider, model_name) VALUES ($1, $2, $3, $4)",
		agentID, "Test Agent", "openrouter", "test-model")
	t.Cleanup(func() {
		db.ExecContext(ctx, "DELETE FROM agents WHERE id = $1", agentID)
	})

	wsRoot := t.TempDir()
	os.MkdirAll(filepath.Join(wsRoot, agentID), 0o755)
	os.WriteFile(filepath.Join(wsRoot, agentID, "work.txt"), []byte("important"), 0o644)

	ss := &testStateStore{db}
	cfg := workspaceTestConfig(wsRoot)
	p := New(db, ss, cfg)

	deleted, errs := p.cleanOrphanWorkspaces(ctx)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if deleted != 0 {
		t.Errorf("expected 0 deleted, got %d", deleted)
	}

	// Verify directory still exists
	if _, err := os.Stat(filepath.Join(wsRoot, agentID)); os.IsNotExist(err) {
		t.Error("expected active agent workspace to still exist")
	}

	// Verify agent is NOT in orphan state
	raw, _ := ss.GetSystemState(ctx, "workspace_orphans")
	if raw != "" {
		var state map[string]time.Time
		json.Unmarshal([]byte(raw), &state)
		if _, ok := state[agentID]; ok {
			t.Error("expected active agent to not be in orphan state")
		}
	}
}

func TestDirSize(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), make([]byte, 100), 0o644)
	os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	os.WriteFile(filepath.Join(dir, "sub", "b.txt"), make([]byte, 200), 0o644)

	size, err := dirSize(dir)
	if err != nil {
		t.Fatal(err)
	}
	if size != 300 {
		t.Errorf("expected 300 bytes, got %d", size)
	}
}
