// Package testutil provides shared test helpers for PostgreSQL-backed tests.
package testutil

import (
	"context"
	"database/sql"
	"os"
	"sync"
	"testing"

	"github.com/kkjorsvik/kyvik/internal/store"
	"github.com/kkjorsvik/kyvik/internal/store/postgres"
)

// NoCloseStore wraps a store.Store and makes Close() a no-op.
// Use this when passing a shared test store to components (like core.Kyvik)
// that close the store during shutdown.
type NoCloseStore struct {
	store.Store
}

// Close is a no-op — the underlying store is shared across tests.
func (s *NoCloseStore) Close() error { return nil }

const defaultTestDSN = "postgres://kyvik:kyvik@localhost:5432/kyvik_test?sslmode=disable"

// TestDB bundles a PostgresStore and its underlying *sql.DB for tests.
type TestDB struct {
	Store *postgres.PostgresStore
	DB    *sql.DB
}

var (
	sharedStore *postgres.PostgresStore
	sharedOnce  sync.Once
	sharedErr   error
)

func testDSN() string {
	if dsn := os.Getenv("KYVIK_TEST_DSN"); dsn != "" {
		return dsn
	}
	return defaultTestDSN
}

// TestDSN returns the PostgreSQL DSN used for integration tests.
// It reads KYVIK_TEST_DSN from the environment, falling back to the default
// local development DSN. Use this when a raw DSN is needed (e.g. for pgx.Connect).
func TestDSN() string {
	return testDSN()
}

// RequirePostgres returns a TestDB backed by a shared PostgresStore.
// It truncates all application tables at the start of each test for isolation.
// If PostgreSQL is unreachable, it calls t.Skip.
func RequirePostgres(t testing.TB) *TestDB {
	t.Helper()

	sharedOnce.Do(func() {
		sharedStore, sharedErr = postgres.New(testDSN(), postgres.StoreOptions{
			MaxConnections: 10,
		})
	})

	if sharedErr != nil {
		t.Skipf("PostgreSQL not available: %v", sharedErr)
	}

	db := sharedStore.DB()
	truncateAll(t, db)

	return &TestDB{
		Store: sharedStore,
		DB:    db,
	}
}

// truncateAll empties all application tables for test isolation.
func truncateAll(t testing.TB, db *sql.DB) {
	t.Helper()
	// Order matters: truncate child tables before parent tables, or use CASCADE.
	// pricing_catalog is excluded — it's reference data seeded once by postgres.New().
	tables := []string{
		"webhook_deliveries",
		"outbound_webhooks",
		"paired_messages",
		"paired_conversations",
		"internal_message_acks",
		"internal_messages",
		"teams",
		"skill_grants",
		"agent_templates",
		"api_keys",
		"user_group_roles",
		"agent_group_members",
		"agent_groups",
		"sessions",
		"users",
		"web_conversations",
		"conversation_history_archive",
		"conversation_history",
		"message_queue",
		"memories",
		"security_events",
		"alert_acknowledgments",
		"schedules",
		"secrets",
		"system_state",
		"spending_limits",
		"usage_records",
		"permission_overrides",
		"audit_log",
		"providers",
		"agent_workspaces",
		"agents",
		"agent_assignments",
		"cluster_nodes",
		"obsidian_vaults",
	}

	ctx := context.Background()
	for _, table := range tables {
		// Use TRUNCATE CASCADE to handle foreign keys.
		if _, err := db.ExecContext(ctx, "TRUNCATE TABLE "+table+" CASCADE"); err != nil {
			// Table might not exist in all schema versions — skip silently.
			t.Logf("truncate %s: %v (skipped)", table, err)
		}
	}
}
