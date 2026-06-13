package audit_test

import (
	"context"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/audit"
	"github.com/kkjorsvik/kyvik/internal/testutil"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

func newTestLogger(t *testing.T) *audit.StoreLogger {
	t.Helper()
	tdb := testutil.RequirePostgres(t)
	logger := audit.NewStoreLoggerWithPollInterval(tdb.Store, 50*time.Millisecond, 10)
	t.Cleanup(func() { logger.Close() })
	return logger
}

func TestLog(t *testing.T) {
	logger := newTestLogger(t)
	ctx := context.Background()

	entry := types.AuditEntry{
		AgentID:   "agent-1",
		EventType: types.EventToolCall,
		Action:    "filesystem.read",
		Resource:  "/data/file.txt",
		Decision:  "allowed",
		Details:   "read file",
	}

	if err := logger.Log(ctx, entry); err != nil {
		t.Fatalf("Log: %v", err)
	}

	logger.Flush()

	entries, err := logger.Query(ctx, audit.Filter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}

	got := entries[0]
	if got.AgentID != "agent-1" {
		t.Errorf("AgentID = %q, want %q", got.AgentID, "agent-1")
	}
	if got.EventType != types.EventToolCall {
		t.Errorf("EventType = %q, want %q", got.EventType, types.EventToolCall)
	}
	if got.Action != "filesystem.read" {
		t.Errorf("Action = %q, want %q", got.Action, "filesystem.read")
	}
	if got.Resource != "/data/file.txt" {
		t.Errorf("Resource = %q, want %q", got.Resource, "/data/file.txt")
	}
	if got.Decision != "allowed" {
		t.Errorf("Decision = %q, want %q", got.Decision, "allowed")
	}
	if got.Details != "read file" {
		t.Errorf("Details = %q, want %q", got.Details, "read file")
	}
	if got.ID == "" {
		t.Error("expected non-empty ID")
	}
}

func TestLogAutoPopulates(t *testing.T) {
	logger := newTestLogger(t)
	ctx := context.Background()

	// Log with empty ID and zero Timestamp.
	entry := types.AuditEntry{
		AgentID:   "agent-1",
		EventType: types.EventToolCall,
		Action:    "read",
		Decision:  "allowed",
	}

	before := time.Now().UTC().Add(-time.Second)
	if err := logger.Log(ctx, entry); err != nil {
		t.Fatalf("Log: %v", err)
	}
	after := time.Now().UTC().Add(time.Second)

	logger.Flush()

	entries, err := logger.Query(ctx, audit.Filter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}

	got := entries[0]
	// The DB auto-generates the ID (integer), so it should be non-empty.
	if got.ID == "" {
		t.Error("expected auto-populated ID")
	}
	// Timestamp should be roughly now (DB auto-generates via CURRENT_TIMESTAMP).
	if got.Timestamp.Before(before) || got.Timestamp.After(after) {
		t.Errorf("Timestamp = %v, expected between %v and %v", got.Timestamp, before, after)
	}
}

func TestQuery(t *testing.T) {
	logger := newTestLogger(t)
	ctx := context.Background()

	entries := []types.AuditEntry{
		{AgentID: "a1", EventType: types.EventToolCall, Action: "read", Decision: "allowed"},
		{AgentID: "a1", EventType: types.EventPermission, Action: "write", Decision: "denied"},
		{AgentID: "a2", EventType: types.EventToolCall, Action: "exec", Decision: "allowed"},
		{AgentID: "a2", EventType: types.EventSpending, Action: "charge", Decision: "allowed"},
	}
	for _, e := range entries {
		if err := logger.Log(ctx, e); err != nil {
			t.Fatalf("Log: %v", err)
		}
	}

	logger.Flush()

	// Filter by agent.
	got, err := logger.Query(ctx, audit.Filter{AgentID: "a1"})
	if err != nil {
		t.Fatalf("Query(agent=a1): %v", err)
	}
	if len(got) != 2 {
		t.Errorf("agent a1: got %d entries, want 2", len(got))
	}

	// Filter by event type.
	got, err = logger.Query(ctx, audit.Filter{EventType: types.EventToolCall})
	if err != nil {
		t.Fatalf("Query(event=tool_call): %v", err)
	}
	if len(got) != 2 {
		t.Errorf("tool_call events: got %d entries, want 2", len(got))
	}

	// Filter by decision.
	got, err = logger.Query(ctx, audit.Filter{Decision: "denied"})
	if err != nil {
		t.Fatalf("Query(decision=denied): %v", err)
	}
	if len(got) != 1 {
		t.Errorf("denied: got %d entries, want 1", len(got))
	}

	// Limit.
	got, err = logger.Query(ctx, audit.Filter{Limit: 2})
	if err != nil {
		t.Fatalf("Query(limit=2): %v", err)
	}
	if len(got) != 2 {
		t.Errorf("limit 2: got %d entries, want 2", len(got))
	}

	// Limit + Offset.
	got, err = logger.Query(ctx, audit.Filter{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("Query(limit=2, offset=2): %v", err)
	}
	if len(got) != 2 {
		t.Errorf("limit 2 offset 2: got %d entries, want 2", len(got))
	}
}

func TestStream(t *testing.T) {
	logger := newTestLogger(t)
	ctx := t.Context()

	ch, err := logger.Stream(ctx, "")
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	// Insert entries after stream starts.
	time.Sleep(20 * time.Millisecond) // let the goroutine start
	for i := range 3 {
		if err := logger.Log(ctx, types.AuditEntry{
			AgentID:   "a1",
			EventType: types.EventToolCall,
			Action:    "read",
			Decision:  "allowed",
			Details:   string(rune('A' + i)),
		}); err != nil {
			t.Fatalf("Log: %v", err)
		}
	}
	logger.Flush()

	// Receive entries with timeout.
	var received []types.AuditEntry
	timeout := time.After(2 * time.Second)
	for len(received) < 3 {
		select {
		case e := <-ch:
			received = append(received, e)
		case <-timeout:
			t.Fatalf("timed out waiting for entries, got %d of 3", len(received))
		}
	}

	// Verify chronological order.
	for i := 1; i < len(received); i++ {
		prevID := received[i-1].ID
		curID := received[i].ID
		if prevID >= curID {
			t.Errorf("entries not in chronological order: %s >= %s", prevID, curID)
		}
	}
}

func TestStreamFiltersByAgent(t *testing.T) {
	logger := newTestLogger(t)
	ctx := t.Context()

	ch, err := logger.Stream(ctx, "a1")
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	time.Sleep(20 * time.Millisecond)

	// Insert for both agents.
	if err := logger.Log(ctx, types.AuditEntry{
		AgentID: "a2", EventType: types.EventToolCall, Action: "read", Decision: "allowed",
	}); err != nil {
		t.Fatalf("Log(a2): %v", err)
	}
	if err := logger.Log(ctx, types.AuditEntry{
		AgentID: "a1", EventType: types.EventToolCall, Action: "write", Decision: "allowed",
	}); err != nil {
		t.Fatalf("Log(a1): %v", err)
	}
	logger.Flush()

	// Should only receive the a1 entry.
	timeout := time.After(2 * time.Second)
	select {
	case e := <-ch:
		if e.AgentID != "a1" {
			t.Errorf("AgentID = %q, want %q", e.AgentID, "a1")
		}
		if e.Action != "write" {
			t.Errorf("Action = %q, want %q", e.Action, "write")
		}
	case <-timeout:
		t.Fatal("timed out waiting for a1 entry")
	}

	// Verify no more entries arrive within a few poll intervals.
	select {
	case e := <-ch:
		t.Errorf("unexpected entry: %+v", e)
	case <-time.After(200 * time.Millisecond):
		// expected — no a2 entries
	}
}

func TestStreamContextCancel(t *testing.T) {
	logger := newTestLogger(t)
	ctx, cancel := context.WithCancel(context.Background())

	ch, err := logger.Stream(ctx, "")
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	cancel()

	// Channel should close.
	timeout := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return // channel closed, test passes
			}
		case <-timeout:
			t.Fatal("timed out waiting for channel to close")
		}
	}
}

func TestLogToolCall(t *testing.T) {
	logger := newTestLogger(t)
	ctx := context.Background()

	err := audit.LogToolCall(ctx, logger, "a1", "filesystem", "read", "/data", "allowed", "ok")
	if err != nil {
		t.Fatalf("LogToolCall: %v", err)
	}

	logger.Flush()

	entries, err := logger.Query(ctx, audit.Filter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}

	got := entries[0]
	if got.EventType != types.EventToolCall {
		t.Errorf("EventType = %q, want %q", got.EventType, types.EventToolCall)
	}
	if got.Action != "filesystem.read" {
		t.Errorf("Action = %q, want %q", got.Action, "filesystem.read")
	}
	if got.Resource != "/data" {
		t.Errorf("Resource = %q, want %q", got.Resource, "/data")
	}
	if got.Decision != "allowed" {
		t.Errorf("Decision = %q, want %q", got.Decision, "allowed")
	}
}

func TestLogPermissionCheck(t *testing.T) {
	logger := newTestLogger(t)
	ctx := context.Background()

	err := audit.LogPermissionCheck(ctx, logger, "a1", "write", "/secret", "denied", "no access")
	if err != nil {
		t.Fatalf("LogPermissionCheck: %v", err)
	}

	logger.Flush()

	entries, err := logger.Query(ctx, audit.Filter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}

	got := entries[0]
	if got.EventType != types.EventPermission {
		t.Errorf("EventType = %q, want %q", got.EventType, types.EventPermission)
	}
	if got.Decision != "denied" {
		t.Errorf("Decision = %q, want %q", got.Decision, "denied")
	}
}

func TestLogAgentLifecycle(t *testing.T) {
	logger := newTestLogger(t)
	ctx := context.Background()

	err := audit.LogAgentLifecycle(ctx, logger, "a1", "start", "agent started")
	if err != nil {
		t.Fatalf("LogAgentLifecycle: %v", err)
	}

	logger.Flush()

	entries, err := logger.Query(ctx, audit.Filter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}

	got := entries[0]
	if got.EventType != types.EventAgentLifecycle {
		t.Errorf("EventType = %q, want %q", got.EventType, types.EventAgentLifecycle)
	}
	if got.Decision != "allowed" {
		t.Errorf("Decision = %q, want %q (lifecycle is always allowed)", got.Decision, "allowed")
	}
	if got.Action != "start" {
		t.Errorf("Action = %q, want %q", got.Action, "start")
	}
}

func TestLogSpending(t *testing.T) {
	logger := newTestLogger(t)
	ctx := context.Background()

	err := audit.LogSpending(ctx, logger, "a1", "charge", "allowed", "0.05 USD")
	if err != nil {
		t.Fatalf("LogSpending: %v", err)
	}

	logger.Flush()

	entries, err := logger.Query(ctx, audit.Filter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}

	got := entries[0]
	if got.EventType != types.EventSpending {
		t.Errorf("EventType = %q, want %q", got.EventType, types.EventSpending)
	}
	if got.Decision != "allowed" {
		t.Errorf("Decision = %q, want %q", got.Decision, "allowed")
	}
}

func TestCloseFlushesRemaining(t *testing.T) {
	tdb := testutil.RequirePostgres(t)
	s := tdb.Store

	// Use a long batch window so entries stay buffered.
	logger := audit.NewStoreLogger(s, 60000) // 60s — won't tick during test
	ctx := context.Background()

	for i := range 5 {
		_ = logger.Log(ctx, types.AuditEntry{
			AgentID:   "a1",
			EventType: types.EventToolCall,
			Action:    "read",
			Decision:  "allowed",
			Details:   string(rune('A' + i)),
		})
	}

	// Entries should be buffered, not in DB yet.
	entries, _ := s.ListAuditEntries(ctx, audit.Filter{})
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries before Close, got %d", len(entries))
	}

	// Close flushes.
	if err := logger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	entries, err := s.ListAuditEntries(ctx, audit.Filter{})
	if err != nil {
		t.Fatalf("Query after Close: %v", err)
	}
	if len(entries) != 5 {
		t.Errorf("expected 5 entries after Close, got %d", len(entries))
	}
}

func TestBatchInsert(t *testing.T) {
	tdb := testutil.RequirePostgres(t)
	s := tdb.Store

	ctx := context.Background()
	batch := []types.AuditEntry{
		{AgentID: "a1", EventType: types.EventToolCall, Action: "read", Decision: "allowed", Details: "1"},
		{AgentID: "a1", EventType: types.EventToolCall, Action: "write", Decision: "denied", Details: "2"},
		{AgentID: "a2", EventType: types.EventPermission, Action: "exec", Decision: "allowed", Details: "3"},
	}

	if err := s.InsertAuditEntries(ctx, batch); err != nil {
		t.Fatalf("InsertAuditEntries: %v", err)
	}

	entries, err := s.ListAuditEntries(ctx, audit.Filter{})
	if err != nil {
		t.Fatalf("ListAuditEntries: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(entries))
	}

	// Verify empty batch is a no-op.
	if err := s.InsertAuditEntries(ctx, nil); err != nil {
		t.Fatalf("InsertAuditEntries(nil): %v", err)
	}
}

func TestSubscribeReceivesEntries(t *testing.T) {
	logger := newTestLogger(t)
	ctx := t.Context()

	ch, err := logger.Subscribe(ctx, audit.SubscriptionFilter{})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	_ = logger.Log(ctx, types.AuditEntry{
		AgentID: "a1", EventType: types.EventToolCall, Action: "read", Decision: "allowed",
	})

	select {
	case e := <-ch:
		if e.AgentID != "a1" {
			t.Errorf("AgentID = %q, want %q", e.AgentID, "a1")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for entry")
	}
}

func TestSubscribeFilterByAgent(t *testing.T) {
	logger := newTestLogger(t)
	ctx := t.Context()

	ch, err := logger.Subscribe(ctx, audit.SubscriptionFilter{AgentID: "a1"})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	_ = logger.Log(ctx, types.AuditEntry{AgentID: "a2", Action: "read"})
	_ = logger.Log(ctx, types.AuditEntry{AgentID: "a1", Action: "write"})

	select {
	case e := <-ch:
		if e.AgentID != "a1" {
			t.Errorf("AgentID = %q, want %q", e.AgentID, "a1")
		}
		if e.Action != "write" {
			t.Errorf("Action = %q, want %q", e.Action, "write")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out")
	}

	// No more entries should arrive.
	select {
	case e := <-ch:
		t.Errorf("unexpected entry: %+v", e)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestSubscribeFilterByAction(t *testing.T) {
	logger := newTestLogger(t)
	ctx := t.Context()

	ch, err := logger.Subscribe(ctx, audit.SubscriptionFilter{Actions: []string{"start", "stop"}})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	_ = logger.Log(ctx, types.AuditEntry{AgentID: "a1", Action: "read"})
	_ = logger.Log(ctx, types.AuditEntry{AgentID: "a1", Action: "start"})

	select {
	case e := <-ch:
		if e.Action != "start" {
			t.Errorf("Action = %q, want %q", e.Action, "start")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out")
	}
}

func TestSubscribeCleanupOnCancel(t *testing.T) {
	logger := newTestLogger(t)
	ctx, cancel := context.WithCancel(context.Background())

	ch, err := logger.Subscribe(ctx, audit.SubscriptionFilter{})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	cancel()

	// Channel should close.
	select {
	case _, ok := <-ch:
		if ok {
			// May get one entry, keep draining.
			for range ch {
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for channel to close")
	}
}

func TestSubscribeNonBlockingWhenFull(t *testing.T) {
	logger := newTestLogger(t)
	ctx := t.Context()

	ch, err := logger.Subscribe(ctx, audit.SubscriptionFilter{})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// Fill the subscriber channel (buffer=64) and then some.
	for i := range 100 {
		_ = logger.Log(ctx, types.AuditEntry{
			AgentID: "a1", Action: "read", Details: string(rune('A' + i%26)),
		})
	}

	// Should not hang — drain what we can.
	count := 0
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				t.Fatal("channel closed unexpectedly")
			}
			count++
		case <-time.After(100 * time.Millisecond):
			goto done
		}
	}
done:
	// Should have received up to 64 (buffer size), the rest dropped.
	if count == 0 {
		t.Error("expected to receive some entries")
	}
	if count > 64 {
		t.Errorf("received %d entries, expected at most 64", count)
	}
}
