//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/kkjorsvik/kyvik/internal/store/postgres"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

func testDB(t *testing.T) *postgres.PostgresStore {
	t.Helper()
	dsn := os.Getenv("KYVIK_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("KYVIK_TEST_POSTGRES_DSN not set; skipping integration test")
	}
	s, err := postgres.New(dsn, postgres.StoreOptions{})
	if err != nil {
		t.Fatalf("postgres.New: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Logf("close store: %v", err)
		}
	})
	return s
}

func TestPostgresCreateAndGetAgent(t *testing.T) {
	s := testDB(t)
	ctx := context.Background()

	cfg := types.AgentConfig{
		ID:       "test-agent-create-get",
		Name:     "Test Agent",
		Template: "reader",
	}

	t.Cleanup(func() {
		_ = s.DeleteAgent(ctx, cfg.ID)
	})

	if err := s.CreateAgent(ctx, cfg); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	got, err := s.GetAgent(ctx, cfg.ID)
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if got.Name != cfg.Name {
		t.Errorf("Name = %q, want %q", got.Name, cfg.Name)
	}
}

func TestPostgresListAgents(t *testing.T) {
	s := testDB(t)
	ctx := context.Background()

	agents := []types.AgentConfig{
		{ID: "test-agent-list-1", Name: "List Agent 1", Template: "reader"},
		{ID: "test-agent-list-2", Name: "List Agent 2", Template: "reader"},
	}

	t.Cleanup(func() {
		for _, a := range agents {
			_ = s.DeleteAgent(ctx, a.ID)
		}
	})

	for _, a := range agents {
		if err := s.CreateAgent(ctx, a); err != nil {
			t.Fatalf("CreateAgent %s: %v", a.ID, err)
		}
	}

	list, err := s.ListAgents(ctx)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(list) < 2 {
		t.Errorf("ListAgents returned %d agents, want >= 2", len(list))
	}
}

func TestPostgresDeleteAgent(t *testing.T) {
	s := testDB(t)
	ctx := context.Background()

	cfg := types.AgentConfig{
		ID:       "test-agent-delete",
		Name:     "Delete Me",
		Template: "reader",
	}

	if err := s.CreateAgent(ctx, cfg); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	if err := s.DeleteAgent(ctx, cfg.ID); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}

	_, err := s.GetAgent(ctx, cfg.ID)
	if err == nil {
		t.Fatal("GetAgent after delete: expected error, got nil")
	}
	if !errors.Is(err, types.ErrNotFound) {
		t.Logf("GetAgent after delete returned: %v (not types.ErrNotFound, which may be acceptable)", err)
	}
}
