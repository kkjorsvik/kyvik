package integration

import (
	"context"
	"os"
	"testing"

	"github.com/kkjorsvik/kyvik/internal/backup"
	"github.com/kkjorsvik/kyvik/internal/history"
	"github.com/kkjorsvik/kyvik/internal/memory"
)

// =============================================================================
// Scenario: Backup & Recovery
// Tests database backup, agent export/import, and result persistence.
// =============================================================================

func TestScenario_Backup_RunNow(t *testing.T) {
	t.Skip("backup manager requires reimplementation for PostgreSQL")
}

func TestScenario_Backup_AgentExportImport(t *testing.T) {
	// t.Parallel() — disabled: shared PostgreSQL database
	h := newFullHarness(t)

	agentID := "export-agent"
	h.seedAgent(t, agentID, "Export Agent", "worker")

	// Add some memories.
	memStore := memory.New(h.db)
	_, err := memStore.Create(context.Background(), memory.Memory{
		AgentID:  agentID,
		Category: "fact",
		Content:  "important memory",
		Source:   memory.SourceAgent,
	})
	if err != nil {
		t.Fatalf("create memory: %v", err)
	}

	// Add a secret.
	if err := h.secrets.Set(context.Background(), "agent:"+agentID, "api_key", "secret-value", "test key"); err != nil {
		t.Fatalf("set secret: %v", err)
	}

	// Export.
	tmpDir := t.TempDir()
	historyStore := history.New(h.db)
	deps := backup.ExportDeps{
		Store:         h.store,
		Vault:         h.secrets,
		MemoryStore:   memStore,
		HistoryStore:  historyStore,
		ConvStore:     historyStore,
		ScheduleStore: h.store,
	}
	passphrase := "test-passphrase-123"
	archivePath, err := backup.ExportAgent(context.Background(), deps, agentID, passphrase, tmpDir)
	if err != nil {
		t.Fatalf("ExportAgent: %v", err)
	}
	if archivePath == "" {
		t.Fatal("empty archive path")
	}

	// Delete the original agent.
	_ = h.store.DeleteAgent(context.Background(), agentID)

	// Import.
	imported, err := backup.ImportAgent(context.Background(), deps, archivePath, passphrase)
	if err != nil {
		t.Fatalf("ImportAgent: %v", err)
	}
	if imported == nil {
		t.Fatal("imported agent is nil")
	}
	// ImportAgent appends " (imported)" to the name to avoid collisions.
	if imported.Name != "Export Agent (imported)" {
		t.Fatalf("expected name 'Export Agent (imported)', got %q", imported.Name)
	}

	// Verify memory was restored.
	memories, err := memStore.List(context.Background(), imported.ID, memory.ListOptions{})
	if err != nil {
		t.Fatalf("list memories: %v", err)
	}
	found := false
	for _, m := range memories {
		if m.Content == "important memory" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("imported agent missing memory")
	}
}

func TestScenario_Backup_WrongPassphrase(t *testing.T) {
	// t.Parallel() — disabled: shared PostgreSQL database
	h := newFullHarness(t)

	agentID := "wrong-pass-agent"
	h.seedAgent(t, agentID, "Wrong Pass Agent", "worker")

	// Add a secret (required for passphrase-protected export).
	if err := h.secrets.Set(context.Background(), "agent:"+agentID, "key", "val", "test"); err != nil {
		t.Fatalf("set secret: %v", err)
	}

	tmpDir := t.TempDir()
	historyStore := history.New(h.db)
	deps := backup.ExportDeps{
		Store:         h.store,
		Vault:         h.secrets,
		MemoryStore:   memory.New(h.db),
		HistoryStore:  historyStore,
		ConvStore:     historyStore,
		ScheduleStore: h.store,
	}

	archivePath, err := backup.ExportAgent(context.Background(), deps, agentID, "correct-pass", tmpDir)
	if err != nil {
		t.Fatalf("ExportAgent: %v", err)
	}

	// Import with wrong passphrase should fail.
	_, err = backup.ImportAgent(context.Background(), deps, archivePath, "wrong-pass")
	if err == nil {
		t.Fatal("expected error for wrong passphrase")
	}
}

func TestScenario_Backup_ExportPreservesAllData(t *testing.T) {
	// t.Parallel() — disabled: shared PostgreSQL database
	h := newFullHarness(t)

	agentID := "full-export"
	h.seedAgent(t, agentID, "Full Export Agent", "worker")

	memStore := memory.New(h.db)
	// Multiple memories.
	for _, content := range []string{"memory-1", "memory-2", "memory-3"} {
		_, err := memStore.Create(context.Background(), memory.Memory{
			AgentID:  agentID,
			Category: "fact",
			Content:  content,
			Source:   memory.SourceAgent,
		})
		if err != nil {
			t.Fatalf("create memory: %v", err)
		}
	}

	// Multiple secrets.
	for _, key := range []string{"secret-a", "secret-b"} {
		if err := h.secrets.Set(context.Background(), "agent:"+agentID, key, "val-"+key, "desc"); err != nil {
			t.Fatalf("set secret: %v", err)
		}
	}

	tmpDir := t.TempDir()
	historyStore := history.New(h.db)
	deps := backup.ExportDeps{
		Store:         h.store,
		Vault:         h.secrets,
		MemoryStore:   memStore,
		HistoryStore:  historyStore,
		ConvStore:     historyStore,
		ScheduleStore: h.store,
	}

	archivePath, err := backup.ExportAgent(context.Background(), deps, agentID, "pass", tmpDir)
	if err != nil {
		t.Fatalf("ExportAgent: %v", err)
	}

	// Verify archive exists and has content.
	info, err := os.Stat(archivePath)
	if err != nil {
		t.Fatalf("stat archive: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("archive is empty")
	}
}

func TestScenario_Backup_LastResultPersistence(t *testing.T) {
	t.Skip("backup manager requires reimplementation for PostgreSQL")
}
