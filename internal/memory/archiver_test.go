package memory_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/memory"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// mockAgentLister implements memory.AgentLister for testing.
type mockAgentLister struct {
	agents []types.AgentConfig
}

func (m *mockAgentLister) ListAgents(_ context.Context) ([]types.AgentConfig, error) {
	return m.agents, nil
}

func TestArchiver_RunOnce(t *testing.T) {
	db := newTestDB(t)
	ms := memory.New(db)
	ctx := context.Background()

	// Create memories with old accessed_at.
	id1, _ := ms.Create(ctx, memory.Memory{
		AgentID: "a1", Category: memory.CategoryFact,
		Content: "old memory", Source: memory.SourceUser, RelevanceScore: 0.5,
	})
	ms.Create(ctx, memory.Memory{
		AgentID: "a1", Category: memory.CategoryFact,
		Content: "recent memory", Source: memory.SourceUser, RelevanceScore: 0.5,
	})

	// Backdate old memory.
	oldTime := time.Now().Add(-100 * 24 * time.Hour).Format("2006-01-02 15:04:05")
	db.Exec(fmt.Sprintf("UPDATE memories SET accessed_at = '%s' WHERE id = %d", oldTime, id1))

	agentStore := &mockAgentLister{
		agents: []types.AgentConfig{{ID: "a1"}},
	}

	archiver := memory.NewArchiver(ms, agentStore, 90, "03:00")
	archiver.RunOnce(ctx)

	// Old memory should be archived.
	mem, _ := ms.Get(ctx, id1)
	if !mem.Archived {
		t.Error("expected old memory to be archived after RunOnce")
	}

	// Recent memory should still be active.
	mems, _ := ms.List(ctx, "a1", memory.ListOptions{})
	if len(mems) != 1 {
		t.Errorf("expected 1 active memory, got %d", len(mems))
	}
}

func TestArchiver_SkipsPinned(t *testing.T) {
	db := newTestDB(t)
	ms := memory.New(db)
	ctx := context.Background()

	pinnedID, _ := ms.Create(ctx, memory.Memory{
		AgentID: "a1", Category: memory.CategoryFact,
		Content: "pinned old", Source: memory.SourceUser, RelevanceScore: 0.5, Pinned: true,
	})

	oldTime := time.Now().Add(-100 * 24 * time.Hour).Format("2006-01-02 15:04:05")
	db.Exec(fmt.Sprintf("UPDATE memories SET accessed_at = '%s' WHERE id = %d", oldTime, pinnedID))

	agentStore := &mockAgentLister{
		agents: []types.AgentConfig{{ID: "a1"}},
	}

	archiver := memory.NewArchiver(ms, agentStore, 90, "03:00")
	archiver.RunOnce(ctx)

	mem, _ := ms.Get(ctx, pinnedID)
	if mem.Archived {
		t.Error("pinned memory should not be archived")
	}
}

func TestArchiver_SkipsRecent(t *testing.T) {
	db := newTestDB(t)
	ms := memory.New(db)
	ctx := context.Background()

	recentID, _ := ms.Create(ctx, memory.Memory{
		AgentID: "a1", Category: memory.CategoryFact,
		Content: "recently accessed", Source: memory.SourceUser, RelevanceScore: 0.5,
	})

	agentStore := &mockAgentLister{
		agents: []types.AgentConfig{{ID: "a1"}},
	}

	archiver := memory.NewArchiver(ms, agentStore, 90, "03:00")
	archiver.RunOnce(ctx)

	mem, _ := ms.Get(ctx, recentID)
	if mem.Archived {
		t.Error("recently accessed memory should not be archived")
	}
}

func TestArchiver_MultipleAgents(t *testing.T) {
	db := newTestDB(t)
	ms := memory.New(db)
	ctx := context.Background()

	// Create old memories for two agents.
	id1, _ := ms.Create(ctx, memory.Memory{
		AgentID: "a1", Category: memory.CategoryFact,
		Content: "agent1 old", Source: memory.SourceUser, RelevanceScore: 0.5,
	})
	id2, _ := ms.Create(ctx, memory.Memory{
		AgentID: "a2", Category: memory.CategoryFact,
		Content: "agent2 old", Source: memory.SourceUser, RelevanceScore: 0.5,
	})

	oldTime := time.Now().Add(-100 * 24 * time.Hour).Format("2006-01-02 15:04:05")
	db.Exec(fmt.Sprintf("UPDATE memories SET accessed_at = '%s' WHERE id IN (%d, %d)", oldTime, id1, id2))

	agentStore := &mockAgentLister{
		agents: []types.AgentConfig{{ID: "a1"}, {ID: "a2"}},
	}

	archiver := memory.NewArchiver(ms, agentStore, 90, "03:00")
	archiver.RunOnce(ctx)

	mem1, _ := ms.Get(ctx, id1)
	mem2, _ := ms.Get(ctx, id2)

	if !mem1.Archived {
		t.Error("agent1 old memory should be archived")
	}
	if !mem2.Archived {
		t.Error("agent2 old memory should be archived")
	}
}
