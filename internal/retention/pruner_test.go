package retention

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/config"
	"github.com/kkjorsvik/kyvik/internal/testutil"
)

// testDB returns a *sql.DB backed by the shared PostgreSQL test database.
// Tables already exist from schema bootstrap.
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	tdb := testutil.RequirePostgres(t)
	return tdb.DB
}

// testStateStore implements StateStore backed by the test DB.
type testStateStore struct {
	db *sql.DB
}

func (s *testStateStore) GetSystemState(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM system_state WHERE key = $1", key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

func (s *testStateStore) SetSystemState(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO system_state (key, value, updated_at)
		 VALUES ($1, $2, CURRENT_TIMESTAMP)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=CURRENT_TIMESTAMP`,
		key, value)
	return err
}

func defaultTestConfig() config.RetentionConfig {
	enabled := true
	return config.RetentionConfig{
		Enabled:                 &enabled,
		AuditLogsDays:           90,
		ConversationHistoryDays: 180,
		CompletedQueueHours:     24,
		SecurityEventsDays:      90,
		ArchivedMemoriesDays:    365,
		WebConversationsDays:    365,
		Schedule:                "0 4 * * *",
	}
}

const timeFmt = "2006-01-02 15:04:05"

func TestPruneAuditLogs(t *testing.T) {
	db := testDB(t)

	ctx := context.Background()

	// Insert old and recent audit entries.
	old := time.Now().AddDate(0, 0, -100).UTC().Format(timeFmt)
	recent := time.Now().AddDate(0, 0, -10).UTC().Format(timeFmt)

	db.Exec("INSERT INTO audit_log (agent_id, action, decision, created_at) VALUES ('a1', 'test', 'allow', $1)", old)
	db.Exec("INSERT INTO audit_log (agent_id, action, decision, created_at) VALUES ('a1', 'test', 'allow', $1)", old)
	db.Exec("INSERT INTO audit_log (agent_id, action, decision, created_at) VALUES ('a1', 'test', 'allow', $1)", recent)

	cfg := defaultTestConfig()
	p := New(db, &testStateStore{db}, cfg)

	result := p.RunNow(ctx)

	if result.AuditLogsDeleted != 2 {
		t.Errorf("expected 2 audit logs deleted, got %d", result.AuditLogsDeleted)
	}

	// Verify recent entry survived.
	var count int
	db.QueryRow("SELECT COUNT(*) FROM audit_log").Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 remaining audit entry, got %d", count)
	}
}

func TestPruneConversationHistory(t *testing.T) {
	db := testDB(t)

	ctx := context.Background()

	old := time.Now().AddDate(0, 0, -200).UTC().Format(timeFmt)
	recent := time.Now().AddDate(0, 0, -10).UTC().Format(timeFmt)

	db.Exec("INSERT INTO conversation_history (agent_id, channel, role, content, created_at) VALUES ('a1', 'slack', 'user', 'old msg', $1)", old)
	db.Exec("INSERT INTO conversation_history (agent_id, channel, role, content, created_at) VALUES ('a1', 'slack', 'assistant', 'old reply', $1)", old)
	db.Exec("INSERT INTO conversation_history (agent_id, channel, role, content, created_at) VALUES ('a1', 'slack', 'user', 'new msg', $1)", recent)

	cfg := defaultTestConfig()
	p := New(db, &testStateStore{db}, cfg)

	result := p.RunNow(ctx)

	if result.ConversationsArchived != 2 {
		t.Errorf("expected 2 conversations archived, got %d", result.ConversationsArchived)
	}
	if result.ConversationsDeleted != 2 {
		t.Errorf("expected 2 conversations deleted, got %d", result.ConversationsDeleted)
	}

	// Verify archive table has the old entries.
	var archiveCount int
	db.QueryRow("SELECT COUNT(*) FROM conversation_history_archive").Scan(&archiveCount)
	if archiveCount != 2 {
		t.Errorf("expected 2 archived entries, got %d", archiveCount)
	}

	// Verify main table has only recent.
	var mainCount int
	db.QueryRow("SELECT COUNT(*) FROM conversation_history").Scan(&mainCount)
	if mainCount != 1 {
		t.Errorf("expected 1 remaining entry, got %d", mainCount)
	}
}

func TestPruneSecurityEvents(t *testing.T) {
	db := testDB(t)

	ctx := context.Background()

	old := time.Now().AddDate(0, 0, -100).UTC().Format(timeFmt)
	recent := time.Now().AddDate(0, 0, -10).UTC().Format(timeFmt)

	db.Exec("INSERT INTO security_events (id, agent_id, event_type, severity, details, created_at) VALUES ('e1', 'a1', 'injection', 'high', 'old', $1)", old)
	db.Exec("INSERT INTO security_events (id, agent_id, event_type, severity, details, created_at) VALUES ('e2', 'a1', 'injection', 'high', 'recent', $1)", recent)

	cfg := defaultTestConfig()
	p := New(db, &testStateStore{db}, cfg)

	result := p.RunNow(ctx)

	if result.SecurityEventsDeleted != 1 {
		t.Errorf("expected 1 security event deleted, got %d", result.SecurityEventsDeleted)
	}

	var count int
	db.QueryRow("SELECT COUNT(*) FROM security_events").Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 remaining security event, got %d", count)
	}
}

func TestPruneArchivedMemories(t *testing.T) {
	db := testDB(t)

	ctx := context.Background()

	old := time.Now().AddDate(-2, 0, 0).UTC().Format(timeFmt)
	recent := time.Now().AddDate(0, 0, -10).UTC().Format(timeFmt)

	// Old archived memory — should be deleted.
	db.Exec("INSERT INTO memories (agent_id, category, content, archived, accessed_at) VALUES ('a1', 'fact', 'old archived', true, $1)", old)
	// Old but NOT archived — should be preserved.
	db.Exec("INSERT INTO memories (agent_id, category, content, archived, accessed_at) VALUES ('a1', 'fact', 'old active', false, $1)", old)
	// Recent archived — should be preserved.
	db.Exec("INSERT INTO memories (agent_id, category, content, archived, accessed_at) VALUES ('a1', 'fact', 'recent archived', true, $1)", recent)

	cfg := defaultTestConfig()
	p := New(db, &testStateStore{db}, cfg)

	result := p.RunNow(ctx)

	if result.ArchivedMemoriesDeleted != 1 {
		t.Errorf("expected 1 archived memory deleted, got %d", result.ArchivedMemoriesDeleted)
	}

	var count int
	db.QueryRow("SELECT COUNT(*) FROM memories").Scan(&count)
	if count != 2 {
		t.Errorf("expected 2 remaining memories, got %d", count)
	}
}

func TestPruneEmptyTables(t *testing.T) {
	db := testDB(t)

	ctx := context.Background()
	cfg := defaultTestConfig()
	p := New(db, &testStateStore{db}, cfg)

	result := p.RunNow(ctx)

	if result.TotalDeleted() != 0 {
		t.Errorf("expected 0 total deleted on empty tables, got %d", result.TotalDeleted())
	}
	if len(result.Errors) != 0 {
		t.Errorf("expected no errors on empty tables, got %v", result.Errors)
	}
}

func TestRunNowPersistsResult(t *testing.T) {
	db := testDB(t)

	ctx := context.Background()
	ss := &testStateStore{db}
	cfg := defaultTestConfig()
	p := New(db, ss, cfg)

	p.RunNow(ctx)

	// Verify system_state was written.
	val, err := ss.GetSystemState(ctx, "last_prune_result")
	if err != nil {
		t.Fatalf("get system state: %v", err)
	}
	if val == "" {
		t.Fatal("expected last_prune_result to be set")
	}

	timeVal, err := ss.GetSystemState(ctx, "last_prune_time")
	if err != nil {
		t.Fatalf("get system state: %v", err)
	}
	if timeVal == "" {
		t.Fatal("expected last_prune_time to be set")
	}

	// Verify LoadLastResult works.
	loaded, err := p.LoadLastResult(ctx)
	if err != nil {
		t.Fatalf("load last result: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected non-nil loaded result")
	}

	// Verify LastResult() returns the cached value.
	cached := p.LastResult()
	if cached == nil {
		t.Fatal("expected non-nil cached result")
	}
}

func TestStats(t *testing.T) {
	db := testDB(t)

	ctx := context.Background()

	// Insert some data.
	db.Exec("INSERT INTO audit_log (agent_id, action, decision) VALUES ('a1', 'test', 'allow')")
	db.Exec("INSERT INTO audit_log (agent_id, action, decision) VALUES ('a1', 'test2', 'deny')")
	db.Exec("INSERT INTO conversation_history (agent_id, channel, role, content) VALUES ('a1', 'slack', 'user', 'hello')")

	cfg := defaultTestConfig()
	p := New(db, &testStateStore{db}, cfg)

	stats, err := p.Stats(ctx)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}

	if stats.AuditLog != 2 {
		t.Errorf("expected 2 audit log entries, got %d", stats.AuditLog)
	}
	if stats.ConversationHistory != 1 {
		t.Errorf("expected 1 conversation history entry, got %d", stats.ConversationHistory)
	}
}

func TestPruneQueueDirectSQL(t *testing.T) {
	db := testDB(t)

	ctx := context.Background()

	old := time.Now().Add(-48 * time.Hour).UTC().Format(timeFmt)
	recent := time.Now().Add(-1 * time.Hour).UTC().Format(timeFmt)

	// Old completed message — should be deleted.
	db.Exec("INSERT INTO message_queue (agent_id, content, status, completed_at) VALUES ('a1', 'old', 'completed', $1)", old)
	// Recent completed — should be preserved.
	db.Exec("INSERT INTO message_queue (agent_id, content, status, completed_at) VALUES ('a1', 'recent', 'completed', $1)", recent)
	// Old pending — should be preserved (not completed).
	db.Exec("INSERT INTO message_queue (agent_id, content, status) VALUES ('a1', 'pending', 'pending')")

	cfg := defaultTestConfig()
	p := New(db, &testStateStore{db}, cfg)
	// No queue set — uses direct SQL fallback.

	result := p.RunNow(ctx)

	if result.QueueMessagesDeleted != 1 {
		t.Errorf("expected 1 queue message deleted, got %d", result.QueueMessagesDeleted)
	}

	var count int
	db.QueryRow("SELECT COUNT(*) FROM message_queue").Scan(&count)
	if count != 2 {
		t.Errorf("expected 2 remaining queue messages, got %d", count)
	}
}

func TestPruneWebConversations(t *testing.T) {
	db := testDB(t)

	ctx := context.Background()

	old := time.Now().AddDate(-2, 0, 0).UTC().Format(timeFmt)
	recent := time.Now().AddDate(0, 0, -10).UTC().Format(timeFmt)

	// Old web conversation with no remaining history messages — should be deleted.
	db.Exec("INSERT INTO web_conversations (id, agent_id, title, updated_at) VALUES ('conv1', 'a1', 'Old', $1)", old)

	// Old web conversation with remaining history — should be preserved.
	db.Exec("INSERT INTO web_conversations (id, agent_id, title, updated_at) VALUES ('conv2', 'a1', 'Old with msgs', $1)", old)
	db.Exec("INSERT INTO conversation_history (agent_id, channel, channel_id, role, content) VALUES ('a1', 'webui', 'conv2', 'user', 'hello')")

	// Recent web conversation — should be preserved.
	db.Exec("INSERT INTO web_conversations (id, agent_id, title, updated_at) VALUES ('conv3', 'a1', 'Recent', $1)", recent)

	cfg := defaultTestConfig()
	p := New(db, &testStateStore{db}, cfg)

	n, err := p.pruneWebConversations(ctx)
	if err != nil {
		t.Fatalf("prune web conversations: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 web conversation deleted, got %d", n)
	}

	var count int
	db.QueryRow("SELECT COUNT(*) FROM web_conversations").Scan(&count)
	if count != 2 {
		t.Errorf("expected 2 remaining web conversations, got %d", count)
	}
}

func TestHumanizeBytes(t *testing.T) {
	tests := []struct {
		input    int64
		expected string
	}{
		{0, "0 B"},
		{500, "500 B"},
		{1024, "1.0 KB"},
		{1024 * 1024, "1.0 MB"},
		{1024 * 1024 * 1024, "1.0 GB"},
		{1536 * 1024, "1.5 MB"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d", tt.input), func(t *testing.T) {
			got := humanizeBytes(tt.input)
			if got != tt.expected {
				t.Errorf("humanizeBytes(%d) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}
