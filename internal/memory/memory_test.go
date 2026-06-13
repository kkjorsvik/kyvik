package memory_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/memory"
	"github.com/kkjorsvik/kyvik/internal/testutil"
)

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	tdb := testutil.RequirePostgres(t)
	return tdb.DB
}

func TestCreateAndGet(t *testing.T) {
	db := newTestDB(t)
	ms := memory.New(db)
	ctx := context.Background()

	id, err := ms.Create(ctx, memory.Memory{
		AgentID:        "a1",
		Category:       memory.CategoryFact,
		Content:        "User prefers dark mode",
		Source:         memory.SourceUser,
		RelevanceScore: 0.8,
		Pinned:         true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id == 0 {
		t.Fatal("expected non-zero ID")
	}

	mem, err := ms.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if mem.AgentID != "a1" {
		t.Errorf("AgentID = %q, want %q", mem.AgentID, "a1")
	}
	if mem.Category != memory.CategoryFact {
		t.Errorf("Category = %q, want %q", mem.Category, memory.CategoryFact)
	}
	if mem.Content != "User prefers dark mode" {
		t.Errorf("Content = %q, want %q", mem.Content, "User prefers dark mode")
	}
	if mem.Source != memory.SourceUser {
		t.Errorf("Source = %q, want %q", mem.Source, memory.SourceUser)
	}
	if mem.RelevanceScore != 0.8 {
		t.Errorf("RelevanceScore = %f, want 0.8", mem.RelevanceScore)
	}
	if !mem.Pinned {
		t.Error("expected Pinned = true")
	}
	if mem.AccessCount != 0 {
		t.Errorf("AccessCount = %d, want 0", mem.AccessCount)
	}
	if mem.CreatedAt.IsZero() {
		t.Error("CreatedAt should not be zero")
	}
}

func TestGetNotFound(t *testing.T) {
	db := newTestDB(t)
	ms := memory.New(db)
	ctx := context.Background()

	_, err := ms.Get(ctx, 999)
	if err == nil {
		t.Fatal("expected error for nonexistent memory")
	}
}

func TestUpdate(t *testing.T) {
	db := newTestDB(t)
	ms := memory.New(db)
	ctx := context.Background()

	id, _ := ms.Create(ctx, memory.Memory{
		AgentID:        "a1",
		Category:       memory.CategoryFact,
		Content:        "Original",
		Source:         memory.SourceUser,
		RelevanceScore: 0.5,
	})

	err := ms.Update(ctx, memory.Memory{
		ID:             id,
		Content:        "Updated content",
		Category:       memory.CategoryDecision,
		Source:         memory.SourceUser,
		RelevanceScore: 0.9,
		Pinned:         true,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	mem, _ := ms.Get(ctx, id)
	if mem.Content != "Updated content" {
		t.Errorf("Content = %q, want %q", mem.Content, "Updated content")
	}
	if mem.Category != memory.CategoryDecision {
		t.Errorf("Category = %q, want %q", mem.Category, memory.CategoryDecision)
	}
	if mem.RelevanceScore != 0.9 {
		t.Errorf("RelevanceScore = %f, want 0.9", mem.RelevanceScore)
	}
	if !mem.Pinned {
		t.Error("expected Pinned = true after update")
	}
}

func TestUpdateNotFound(t *testing.T) {
	db := newTestDB(t)
	ms := memory.New(db)
	ctx := context.Background()

	err := ms.Update(ctx, memory.Memory{
		ID:       999,
		Content:  "nope",
		Category: memory.CategoryFact,
		Source:   memory.SourceUser,
	})
	if err == nil {
		t.Fatal("expected error for nonexistent memory")
	}
}

func TestDelete(t *testing.T) {
	db := newTestDB(t)
	ms := memory.New(db)
	ctx := context.Background()

	id, _ := ms.Create(ctx, memory.Memory{
		AgentID:        "a1",
		Category:       memory.CategoryFact,
		Content:        "To be deleted",
		Source:         memory.SourceUser,
		RelevanceScore: 0.5,
	})

	err := ms.Delete(ctx, id)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err = ms.Get(ctx, id)
	if err == nil {
		t.Fatal("expected error after delete")
	}
}

func TestDeleteNotFound(t *testing.T) {
	db := newTestDB(t)
	ms := memory.New(db)
	ctx := context.Background()

	err := ms.Delete(ctx, 999)
	if err == nil {
		t.Fatal("expected error for nonexistent memory")
	}
}

func TestList(t *testing.T) {
	db := newTestDB(t)
	ms := memory.New(db)
	ctx := context.Background()

	// Create memories for two agents
	ms.Create(ctx, memory.Memory{AgentID: "a1", Category: memory.CategoryFact, Content: "fact 1", Source: memory.SourceUser, RelevanceScore: 0.5})
	ms.Create(ctx, memory.Memory{AgentID: "a1", Category: memory.CategoryDecision, Content: "decision 1", Source: memory.SourceAgent, RelevanceScore: 0.5})
	ms.Create(ctx, memory.Memory{AgentID: "a1", Category: memory.CategoryFact, Content: "fact 2", Source: memory.SourceUser, RelevanceScore: 0.5})
	ms.Create(ctx, memory.Memory{AgentID: "a2", Category: memory.CategoryFact, Content: "other agent", Source: memory.SourceUser, RelevanceScore: 0.5})

	// List all for a1
	mems, err := ms.List(ctx, "a1", memory.ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(mems) != 3 {
		t.Fatalf("got %d memories, want 3", len(mems))
	}

	// Filter by category
	mems, err = ms.List(ctx, "a1", memory.ListOptions{Category: memory.CategoryFact})
	if err != nil {
		t.Fatalf("List with category: %v", err)
	}
	if len(mems) != 2 {
		t.Errorf("got %d facts, want 2", len(mems))
	}

	// Filter by source
	mems, err = ms.List(ctx, "a1", memory.ListOptions{Source: memory.SourceAgent})
	if err != nil {
		t.Fatalf("List with source: %v", err)
	}
	if len(mems) != 1 {
		t.Errorf("got %d agent-sourced, want 1", len(mems))
	}

	// Limit and offset
	mems, err = ms.List(ctx, "a1", memory.ListOptions{Limit: 2})
	if err != nil {
		t.Fatalf("List with limit: %v", err)
	}
	if len(mems) != 2 {
		t.Errorf("got %d with limit 2, want 2", len(mems))
	}

	mems, err = ms.List(ctx, "a1", memory.ListOptions{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("List with offset: %v", err)
	}
	if len(mems) != 1 {
		t.Errorf("got %d with offset 2, want 1", len(mems))
	}
}

func TestListPinned(t *testing.T) {
	db := newTestDB(t)
	ms := memory.New(db)
	ctx := context.Background()

	ms.Create(ctx, memory.Memory{AgentID: "a1", Category: memory.CategoryFact, Content: "not pinned", Source: memory.SourceUser, RelevanceScore: 0.5, Pinned: false})
	ms.Create(ctx, memory.Memory{AgentID: "a1", Category: memory.CategoryFact, Content: "pinned", Source: memory.SourceUser, RelevanceScore: 0.5, Pinned: true})
	ms.Create(ctx, memory.Memory{AgentID: "a1", Category: memory.CategoryDecision, Content: "also pinned", Source: memory.SourceUser, RelevanceScore: 0.5, Pinned: true})

	pinned, err := ms.ListPinned(ctx, "a1")
	if err != nil {
		t.Fatalf("ListPinned: %v", err)
	}
	if len(pinned) != 2 {
		t.Errorf("got %d pinned, want 2", len(pinned))
	}
}

func TestTouch(t *testing.T) {
	db := newTestDB(t)
	ms := memory.New(db)
	ctx := context.Background()

	id, _ := ms.Create(ctx, memory.Memory{
		AgentID:        "a1",
		Category:       memory.CategoryFact,
		Content:        "touchable",
		Source:         memory.SourceUser,
		RelevanceScore: 0.5,
	})

	// Touch twice
	if err := ms.Touch(ctx, id); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	if err := ms.Touch(ctx, id); err != nil {
		t.Fatalf("Touch 2: %v", err)
	}

	mem, _ := ms.Get(ctx, id)
	if mem.AccessCount != 2 {
		t.Errorf("AccessCount = %d, want 2", mem.AccessCount)
	}
}

func TestCount(t *testing.T) {
	db := newTestDB(t)
	ms := memory.New(db)
	ctx := context.Background()

	ms.Create(ctx, memory.Memory{AgentID: "a1", Category: memory.CategoryFact, Content: "1", Source: memory.SourceUser, RelevanceScore: 0.5})
	ms.Create(ctx, memory.Memory{AgentID: "a1", Category: memory.CategoryFact, Content: "2", Source: memory.SourceUser, RelevanceScore: 0.5})
	ms.Create(ctx, memory.Memory{AgentID: "a2", Category: memory.CategoryFact, Content: "other", Source: memory.SourceUser, RelevanceScore: 0.5})

	count, err := ms.Count(ctx, "a1")
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != 2 {
		t.Errorf("count = %d, want 2", count)
	}

	count, _ = ms.Count(ctx, "a2")
	if count != 1 {
		t.Errorf("a2 count = %d, want 1", count)
	}

	count, _ = ms.Count(ctx, "nonexistent")
	if count != 0 {
		t.Errorf("nonexistent count = %d, want 0", count)
	}
}

func TestDeleteByAgent(t *testing.T) {
	db := newTestDB(t)
	ms := memory.New(db)
	ctx := context.Background()

	ms.Create(ctx, memory.Memory{AgentID: "a1", Category: memory.CategoryFact, Content: "1", Source: memory.SourceUser, RelevanceScore: 0.5})
	ms.Create(ctx, memory.Memory{AgentID: "a1", Category: memory.CategoryFact, Content: "2", Source: memory.SourceUser, RelevanceScore: 0.5})
	ms.Create(ctx, memory.Memory{AgentID: "a2", Category: memory.CategoryFact, Content: "other", Source: memory.SourceUser, RelevanceScore: 0.5})

	if err := ms.DeleteByAgent(ctx, "a1"); err != nil {
		t.Fatalf("DeleteByAgent: %v", err)
	}

	c1, _ := ms.Count(ctx, "a1")
	c2, _ := ms.Count(ctx, "a2")
	if c1 != 0 {
		t.Errorf("a1 count after delete = %d, want 0", c1)
	}
	if c2 != 1 {
		t.Errorf("a2 count = %d, want 1 (unaffected)", c2)
	}
}

func TestImport(t *testing.T) {
	db := newTestDB(t)
	ms := memory.New(db)
	ctx := context.Background()

	mems := []memory.Memory{
		{Category: memory.CategoryFact, Content: "imported 1", Source: memory.SourceUser, RelevanceScore: 0.5},
		{Category: memory.CategoryDecision, Content: "imported 2", Source: memory.SourceAgent, RelevanceScore: 0.7},
	}

	count, err := ms.Import(ctx, "a1", mems)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if count != 2 {
		t.Errorf("imported %d, want 2", count)
	}

	total, _ := ms.Count(ctx, "a1")
	if total != 2 {
		t.Errorf("total = %d, want 2", total)
	}
}

func TestCreateFromAgent(t *testing.T) {
	db := newTestDB(t)
	ms := memory.New(db)
	ctx := context.Background()

	id, err := ms.CreateFromAgent(ctx, "a1", memory.CategoryContext, "The meeting was about Q4 plans")
	if err != nil {
		t.Fatalf("CreateFromAgent: %v", err)
	}

	mem, _ := ms.Get(ctx, id)
	if mem.Source != memory.SourceAgent {
		t.Errorf("Source = %q, want %q", mem.Source, memory.SourceAgent)
	}
	if mem.RelevanceScore != 0.5 {
		t.Errorf("RelevanceScore = %f, want 0.5", mem.RelevanceScore)
	}
	if mem.Content != "The meeting was about Q4 plans" {
		t.Errorf("Content = %q", mem.Content)
	}
}

func TestSetEmbedding(t *testing.T) {
	db := newTestDB(t)
	ms := memory.New(db)
	ctx := context.Background()

	id, _ := ms.Create(ctx, memory.Memory{
		AgentID:        "a1",
		Category:       memory.CategoryFact,
		Content:        "embeddable",
		Source:         memory.SourceUser,
		RelevanceScore: 0.5,
	})

	// Set embedding
	embedding := []float32{0.1, 0.2, 0.3}
	if err := ms.SetEmbedding(ctx, id, embedding, "test-model"); err != nil {
		t.Fatalf("SetEmbedding: %v", err)
	}

	mem, _ := ms.Get(ctx, id)
	if mem.EmbeddingModel != "test-model" {
		t.Errorf("EmbeddingModel = %q, want %q", mem.EmbeddingModel, "test-model")
	}
}

func TestGetUnembedded(t *testing.T) {
	db := newTestDB(t)
	ms := memory.New(db)
	ctx := context.Background()

	id1, _ := ms.Create(ctx, memory.Memory{AgentID: "a1", Category: memory.CategoryFact, Content: "has embedding", Source: memory.SourceUser, RelevanceScore: 0.5})
	ms.Create(ctx, memory.Memory{AgentID: "a1", Category: memory.CategoryFact, Content: "no embedding", Source: memory.SourceUser, RelevanceScore: 0.5})

	ms.SetEmbedding(ctx, id1, []float32{0.1}, "model")

	unembedded, err := ms.GetUnembedded(ctx, "a1")
	if err != nil {
		t.Fatalf("GetUnembedded: %v", err)
	}
	if len(unembedded) != 1 {
		t.Errorf("got %d unembedded, want 1", len(unembedded))
	}
	if unembedded[0].Content != "no embedding" {
		t.Errorf("Content = %q, want %q", unembedded[0].Content, "no embedding")
	}
}

func TestListWithPinnedFilter(t *testing.T) {
	db := newTestDB(t)
	ms := memory.New(db)
	ctx := context.Background()

	ms.Create(ctx, memory.Memory{AgentID: "a1", Category: memory.CategoryFact, Content: "not pinned", Source: memory.SourceUser, RelevanceScore: 0.5, Pinned: false})
	ms.Create(ctx, memory.Memory{AgentID: "a1", Category: memory.CategoryFact, Content: "pinned", Source: memory.SourceUser, RelevanceScore: 0.5, Pinned: true})

	pinTrue := true
	mems, err := ms.List(ctx, "a1", memory.ListOptions{Pinned: &pinTrue})
	if err != nil {
		t.Fatalf("List pinned=true: %v", err)
	}
	if len(mems) != 1 {
		t.Errorf("got %d, want 1", len(mems))
	}
	if mems[0].Content != "pinned" {
		t.Errorf("Content = %q, want %q", mems[0].Content, "pinned")
	}

	pinFalse := false
	mems, err = ms.List(ctx, "a1", memory.ListOptions{Pinned: &pinFalse})
	if err != nil {
		t.Fatalf("List pinned=false: %v", err)
	}
	if len(mems) != 1 {
		t.Errorf("got %d, want 1", len(mems))
	}
	if mems[0].Content != "not pinned" {
		t.Errorf("Content = %q, want %q", mems[0].Content, "not pinned")
	}
}

func TestArchiveUnarchive(t *testing.T) {
	db := newTestDB(t)
	ms := memory.New(db)
	ctx := context.Background()

	id, err := ms.Create(ctx, memory.Memory{
		AgentID: "a1", Category: memory.CategoryFact,
		Content: "archivable", Source: memory.SourceUser, RelevanceScore: 0.5,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Archive it.
	if err := ms.Archive(ctx, id); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	// Verify excluded from default List.
	mems, _ := ms.List(ctx, "a1", memory.ListOptions{})
	if len(mems) != 0 {
		t.Errorf("expected 0 active memories after archive, got %d", len(mems))
	}

	// Verify visible with ArchivedOnly.
	mems, _ = ms.List(ctx, "a1", memory.ListOptions{ArchivedOnly: true})
	if len(mems) != 1 {
		t.Fatalf("expected 1 archived memory, got %d", len(mems))
	}
	if !mems[0].Archived {
		t.Error("expected Archived = true")
	}

	// Verify visible with IncludeArchived.
	includeAll := true
	mems, _ = ms.List(ctx, "a1", memory.ListOptions{IncludeArchived: &includeAll})
	if len(mems) != 1 {
		t.Errorf("expected 1 with IncludeArchived, got %d", len(mems))
	}

	// Unarchive it.
	if err := ms.Unarchive(ctx, id); err != nil {
		t.Fatalf("Unarchive: %v", err)
	}

	// Verify back in default List.
	mems, _ = ms.List(ctx, "a1", memory.ListOptions{})
	if len(mems) != 1 {
		t.Errorf("expected 1 active memory after unarchive, got %d", len(mems))
	}
	if mems[0].Archived {
		t.Error("expected Archived = false after unarchive")
	}
}

func TestArchiveStale(t *testing.T) {
	db := newTestDB(t)
	ms := memory.New(db)
	ctx := context.Background()

	// Create two memories: one old, one recent.
	oldID, _ := ms.Create(ctx, memory.Memory{
		AgentID: "a1", Category: memory.CategoryFact,
		Content: "old memory", Source: memory.SourceUser, RelevanceScore: 0.5,
	})
	ms.Create(ctx, memory.Memory{
		AgentID: "a1", Category: memory.CategoryFact,
		Content: "recent memory", Source: memory.SourceUser, RelevanceScore: 0.5,
	})

	// Backdate the old memory's accessed_at.
	oldTime := time.Now().Add(-100 * 24 * time.Hour).UTC().Format("2006-01-02 15:04:05")
	db.Exec(fmt.Sprintf("UPDATE memories SET accessed_at = '%s' WHERE id = %d", oldTime, oldID))

	// Archive memories not accessed in 90 days.
	count, err := ms.ArchiveStale(ctx, "a1", 90*24*time.Hour)
	if err != nil {
		t.Fatalf("ArchiveStale: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 archived, got %d", count)
	}

	// Verify old memory is archived.
	mem, _ := ms.Get(ctx, oldID)
	if !mem.Archived {
		t.Error("expected old memory to be archived")
	}

	// Verify recent memory is still active.
	mems, _ := ms.List(ctx, "a1", memory.ListOptions{})
	if len(mems) != 1 {
		t.Errorf("expected 1 active memory, got %d", len(mems))
	}
	if mems[0].Content != "recent memory" {
		t.Errorf("expected 'recent memory', got %q", mems[0].Content)
	}
}

func TestArchiveStaleSkipsPinned(t *testing.T) {
	db := newTestDB(t)
	ms := memory.New(db)
	ctx := context.Background()

	// Create a pinned old memory.
	pinnedID, _ := ms.Create(ctx, memory.Memory{
		AgentID: "a1", Category: memory.CategoryFact,
		Content: "pinned old", Source: memory.SourceUser, RelevanceScore: 0.5, Pinned: true,
	})

	// Backdate it.
	oldTime := time.Now().Add(-100 * 24 * time.Hour).UTC().Format("2006-01-02 15:04:05")
	db.Exec(fmt.Sprintf("UPDATE memories SET accessed_at = '%s' WHERE id = %d", oldTime, pinnedID))

	// Archive stale — should skip pinned.
	count, err := ms.ArchiveStale(ctx, "a1", 90*24*time.Hour)
	if err != nil {
		t.Fatalf("ArchiveStale: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 archived (pinned should be skipped), got %d", count)
	}

	// Verify still active.
	mem, _ := ms.Get(ctx, pinnedID)
	if mem.Archived {
		t.Error("pinned memory should not be archived")
	}
}

func TestCountExcludesArchived(t *testing.T) {
	db := newTestDB(t)
	ms := memory.New(db)
	ctx := context.Background()

	id1, _ := ms.Create(ctx, memory.Memory{
		AgentID: "a1", Category: memory.CategoryFact,
		Content: "active", Source: memory.SourceUser, RelevanceScore: 0.5,
	})
	ms.Create(ctx, memory.Memory{
		AgentID: "a1", Category: memory.CategoryFact,
		Content: "will archive", Source: memory.SourceUser, RelevanceScore: 0.5,
	})

	// Count before archive.
	count, _ := ms.Count(ctx, "a1")
	if count != 2 {
		t.Fatalf("expected count 2 before archive, got %d", count)
	}

	// Archive one and ignore the other.
	_ = id1
	ms.Archive(ctx, id1)

	// Count after archive.
	count, _ = ms.Count(ctx, "a1")
	if count != 1 {
		t.Errorf("expected count 1 after archive, got %d", count)
	}
}

func TestListRecent(t *testing.T) {
	db := newTestDB(t)
	ms := memory.New(db)
	ctx := context.Background()

	// Create several memories for agent a1.
	id1, _ := ms.Create(ctx, memory.Memory{AgentID: "a1", Category: memory.CategoryFact, Content: "oldest", Source: memory.SourceUser, RelevanceScore: 0.5})
	id2, _ := ms.Create(ctx, memory.Memory{AgentID: "a1", Category: memory.CategoryFact, Content: "middle", Source: memory.SourceUser, RelevanceScore: 0.5})
	id3, _ := ms.Create(ctx, memory.Memory{AgentID: "a1", Category: memory.CategoryFact, Content: "newest", Source: memory.SourceUser, RelevanceScore: 0.5})
	// Different agent — should not appear.
	ms.Create(ctx, memory.Memory{AgentID: "a2", Category: memory.CategoryFact, Content: "other agent", Source: memory.SourceUser, RelevanceScore: 0.5})

	// Backdate accessed_at to create distinct ordering.
	db.Exec(fmt.Sprintf("UPDATE memories SET accessed_at = '2025-01-01 00:00:00' WHERE id = %d", id1))
	db.Exec(fmt.Sprintf("UPDATE memories SET accessed_at = '2025-01-01 00:00:01' WHERE id = %d", id3))
	db.Exec(fmt.Sprintf("UPDATE memories SET accessed_at = '2025-01-01 00:00:02' WHERE id = %d", id2))

	mems, err := ms.ListRecent(ctx, "a1", 10)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(mems) != 3 {
		t.Fatalf("expected 3 memories, got %d", len(mems))
	}
	// First result should be the one with latest accessed_at.
	if mems[0].Content != "middle" {
		t.Errorf("expected most recently accessed first, got %q", mems[0].Content)
	}

	// Test limit.
	mems, err = ms.ListRecent(ctx, "a1", 2)
	if err != nil {
		t.Fatalf("ListRecent with limit: %v", err)
	}
	if len(mems) != 2 {
		t.Errorf("expected 2 with limit=2, got %d", len(mems))
	}

	// Test excludes archived.
	ms.Archive(ctx, id2)
	mems, err = ms.ListRecent(ctx, "a1", 10)
	if err != nil {
		t.Fatalf("ListRecent after archive: %v", err)
	}
	if len(mems) != 2 {
		t.Errorf("expected 2 after archiving one, got %d", len(mems))
	}
	for _, m := range mems {
		if m.Content == "middle" {
			t.Error("archived memory should not appear in ListRecent")
		}
	}
}

func TestListWithArchivedFilter(t *testing.T) {
	db := newTestDB(t)
	ms := memory.New(db)
	ctx := context.Background()

	id1, _ := ms.Create(ctx, memory.Memory{
		AgentID: "a1", Category: memory.CategoryFact,
		Content: "active one", Source: memory.SourceUser, RelevanceScore: 0.5,
	})
	ms.Create(ctx, memory.Memory{
		AgentID: "a1", Category: memory.CategoryFact,
		Content: "active two", Source: memory.SourceUser, RelevanceScore: 0.5,
	})
	ms.Archive(ctx, id1)

	// Default: active only.
	mems, _ := ms.List(ctx, "a1", memory.ListOptions{})
	if len(mems) != 1 {
		t.Errorf("default list: expected 1 active, got %d", len(mems))
	}

	// ArchivedOnly.
	mems, _ = ms.List(ctx, "a1", memory.ListOptions{ArchivedOnly: true})
	if len(mems) != 1 {
		t.Errorf("archived only: expected 1, got %d", len(mems))
	}
	if mems[0].Content != "active one" {
		t.Errorf("expected 'active one' (now archived), got %q", mems[0].Content)
	}

	// IncludeArchived: all.
	includeAll := true
	mems, _ = ms.List(ctx, "a1", memory.ListOptions{IncludeArchived: &includeAll})
	if len(mems) != 2 {
		t.Errorf("include all: expected 2, got %d", len(mems))
	}
}
