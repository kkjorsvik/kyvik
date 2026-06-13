package queue_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/queue"
	"github.com/kkjorsvik/kyvik/internal/testutil"
)

func mustNewPQ(t *testing.T, db *sql.DB, cfg queue.Config) *queue.PostgresQueue {
	t.Helper()
	q, err := queue.NewPostgresQueue(db, "", "", cfg)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func newTestQueue(t *testing.T, cfg queue.Config) *queue.PostgresQueue {
	t.Helper()
	tdb := testutil.RequirePostgres(t)

	q := mustNewPQ(t, tdb.DB, cfg)
	t.Cleanup(func() { q.Stop() })
	return q
}

func TestEnqueueDequeue(t *testing.T) {
	q := newTestQueue(t, queue.DefaultConfig())
	ctx := context.Background()

	// Create dequeue channel first so Enqueue can push to it.
	ch := q.Dequeue(ctx, "agent-1")

	id, err := q.Enqueue(ctx, queue.QueueMessage{
		AgentID: "agent-1",
		Channel: "webui",
		Sender:  "user-1",
		Content: "hello world",
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if id <= 0 {
		t.Fatalf("expected positive ID, got %d", id)
	}

	select {
	case msg := <-ch:
		if msg.ID != id {
			t.Errorf("ID: got %d, want %d", msg.ID, id)
		}
		if msg.AgentID != "agent-1" {
			t.Errorf("AgentID: got %q, want %q", msg.AgentID, "agent-1")
		}
		if msg.Content != "hello world" {
			t.Errorf("Content: got %q, want %q", msg.Content, "hello world")
		}
		if msg.Channel != "webui" {
			t.Errorf("Channel: got %q, want %q", msg.Channel, "webui")
		}
		if msg.Sender != "user-1" {
			t.Errorf("Sender: got %q, want %q", msg.Sender, "user-1")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for message")
	}
}

func TestComplete(t *testing.T) {
	q := newTestQueue(t, queue.DefaultConfig())
	ctx := context.Background()
	q.Dequeue(ctx, "agent-1")

	id, err := q.Enqueue(ctx, queue.QueueMessage{
		AgentID: "agent-1",
		Content: "test",
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	if err := q.MarkProcessing(ctx, id); err != nil {
		t.Fatalf("MarkProcessing: %v", err)
	}

	if err := q.Complete(ctx, id); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// Verify depth is 0 (completed messages don't count).
	depth, err := q.Depth(ctx, "agent-1")
	if err != nil {
		t.Fatalf("Depth: %v", err)
	}
	if depth != 0 {
		t.Errorf("Depth: got %d, want 0", depth)
	}
}

func TestFailRetry(t *testing.T) {
	q := newTestQueue(t, queue.DefaultConfig())
	ctx := context.Background()
	ch := q.Dequeue(ctx, "agent-1")

	id, err := q.Enqueue(ctx, queue.QueueMessage{
		AgentID:     "agent-1",
		Content:     "retry-me",
		MaxAttempts: 3,
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	// Drain the initial push.
	<-ch

	if err := q.MarkProcessing(ctx, id); err != nil {
		t.Fatalf("MarkProcessing: %v", err)
	}

	// First failure — should retry (attempts goes from 0 -> 1, max is 3).
	if err := q.Fail(ctx, id); err != nil {
		t.Fatalf("Fail: %v", err)
	}

	// Message should be re-pushed to channel.
	select {
	case msg := <-ch:
		if msg.ID != id {
			t.Errorf("retried message ID: got %d, want %d", msg.ID, id)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for retried message")
	}

	// Depth should be 1 (pending again).
	depth, err := q.Depth(ctx, "agent-1")
	if err != nil {
		t.Fatalf("Depth: %v", err)
	}
	if depth != 1 {
		t.Errorf("Depth after retry: got %d, want 1", depth)
	}
}

func TestFailTerminal(t *testing.T) {
	q := newTestQueue(t, queue.DefaultConfig())
	ctx := context.Background()
	ch := q.Dequeue(ctx, "agent-1")

	id, err := q.Enqueue(ctx, queue.QueueMessage{
		AgentID:     "agent-1",
		Content:     "fail-me",
		MaxAttempts: 1, // Will fail permanently on first failure.
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	<-ch

	if err := q.MarkProcessing(ctx, id); err != nil {
		t.Fatalf("MarkProcessing: %v", err)
	}

	if err := q.Fail(ctx, id); err != nil {
		t.Fatalf("Fail: %v", err)
	}

	// Depth should be 0 — message is failed, not pending/processing.
	depth, err := q.Depth(ctx, "agent-1")
	if err != nil {
		t.Fatalf("Depth: %v", err)
	}
	if depth != 0 {
		t.Errorf("Depth after terminal fail: got %d, want 0", depth)
	}

	// No message should appear on channel.
	select {
	case msg := <-ch:
		t.Fatalf("unexpected message on channel: %+v", msg)
	case <-time.After(100 * time.Millisecond):
		// Expected — no retry.
	}
}

func TestReplay(t *testing.T) {
	tdb := testutil.RequirePostgres(t)
	db := tdb.DB
	ctx := context.Background()

	// Insert a pending message directly into DB (simulating pre-crash state).
	_, err := db.ExecContext(ctx,
		`INSERT INTO message_queue (agent_id, channel, sender, content, status, attempts, max_attempts)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		"agent-1", "webui", "user-1", "pending-msg", "pending", 0, 3,
	)
	if err != nil {
		t.Fatalf("insert pending: %v", err)
	}

	// Insert a stale processing message (started > stale threshold ago).
	staleTime := time.Now().Add(-10 * time.Minute).UTC().Format("2006-01-02 15:04:05")
	_, err = db.ExecContext(ctx,
		`INSERT INTO message_queue (agent_id, channel, sender, content, status, attempts, max_attempts, started_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		"agent-1", "webui", "user-2", "stale-msg", "processing", 0, 3, staleTime,
	)
	if err != nil {
		t.Fatalf("insert stale: %v", err)
	}

	cfg := queue.DefaultConfig()
	cfg.StaleTimeoutSeconds = 60 // Stale after 60s; our message is 10min old.
	q := mustNewPQ(t, db, cfg)
	t.Cleanup(func() { q.Stop() })

	// Create channel before replay.
	ch := q.Dequeue(ctx, "agent-1")

	if err := q.Replay(ctx); err != nil {
		t.Fatalf("Replay: %v", err)
	}

	// Should receive 2 messages (the original pending + the reset stale one).
	var received int
	for i := range 2 {
		select {
		case <-ch:
			received++
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for message %d", i+1)
		}
	}
	if received != 2 {
		t.Errorf("received %d messages, want 2", received)
	}
}

func TestReplayAgent(t *testing.T) {
	tdb := testutil.RequirePostgres(t)
	db := tdb.DB
	ctx := context.Background()

	// Insert pending messages for two agents.
	for _, agentID := range []string{"agent-1", "agent-2"} {
		_, err := db.ExecContext(ctx,
			`INSERT INTO message_queue (agent_id, channel, sender, content, status, attempts, max_attempts)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			agentID, "webui", "user-1", "msg-for-"+agentID, "pending", 0, 3,
		)
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	q := mustNewPQ(t, db, queue.DefaultConfig())
	t.Cleanup(func() { q.Stop() })

	ch1 := q.Dequeue(ctx, "agent-1")
	q.Dequeue(ctx, "agent-2")

	// Replay only agent-1.
	if err := q.ReplayAgent(ctx, "agent-1"); err != nil {
		t.Fatalf("ReplayAgent: %v", err)
	}

	select {
	case msg := <-ch1:
		if msg.AgentID != "agent-1" {
			t.Errorf("AgentID: got %q, want agent-1", msg.AgentID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for agent-1 message")
	}
}

func TestCleanup(t *testing.T) {
	tdb := testutil.RequirePostgres(t)
	db := tdb.DB
	ctx := context.Background()

	// Insert a completed message from 48 hours ago.
	oldTime := time.Now().Add(-48 * time.Hour).UTC().Format("2006-01-02 15:04:05")
	_, err := db.ExecContext(ctx,
		`INSERT INTO message_queue (agent_id, content, status, completed_at, attempts, max_attempts)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		"agent-1", "old-msg", "completed", oldTime, 1, 3,
	)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Insert a recent completed message.
	_, err = db.ExecContext(ctx,
		`INSERT INTO message_queue (agent_id, content, status, completed_at, attempts, max_attempts)
		 VALUES ($1, $2, $3, CURRENT_TIMESTAMP, $4, $5)`,
		"agent-1", "new-msg", "completed", 1, 3,
	)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	q := mustNewPQ(t, db, queue.DefaultConfig())
	t.Cleanup(func() { q.Stop() })

	deleted, err := q.Cleanup(ctx, 24)
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted: got %d, want 1", deleted)
	}
}

func TestDepth(t *testing.T) {
	q := newTestQueue(t, queue.DefaultConfig())
	ctx := context.Background()
	q.Dequeue(ctx, "agent-1")

	// Enqueue 3 messages.
	for i := range 3 {
		if _, err := q.Enqueue(ctx, queue.QueueMessage{
			AgentID: "agent-1",
			Content: "msg",
		}); err != nil {
			t.Fatalf("Enqueue %d: %v", i, err)
		}
	}

	depth, err := q.Depth(ctx, "agent-1")
	if err != nil {
		t.Fatalf("Depth: %v", err)
	}
	if depth != 3 {
		t.Errorf("Depth: got %d, want 3", depth)
	}

	// Complete one — depth should be 2 after marking processing then completing.
	// First drain the channel to get the ID.
	msg := <-q.Dequeue(ctx, "agent-1")
	if err := q.MarkProcessing(ctx, msg.ID); err != nil {
		t.Fatalf("MarkProcessing: %v", err)
	}
	if err := q.Complete(ctx, msg.ID); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	depth, err = q.Depth(ctx, "agent-1")
	if err != nil {
		t.Fatalf("Depth: %v", err)
	}
	if depth != 2 {
		t.Errorf("Depth after complete: got %d, want 2", depth)
	}
}

func TestBackpressureAcknowledge(t *testing.T) {
	cfg := queue.DefaultConfig()
	cfg.Depth = 2
	q := newTestQueue(t, cfg)
	ctx := context.Background()
	ch := q.Dequeue(ctx, "agent-1")

	// Enqueue 2 messages (at limit).
	for i := range 2 {
		if _, err := q.Enqueue(ctx, queue.QueueMessage{
			AgentID: "agent-1",
			Content: "msg",
		}); err != nil {
			t.Fatalf("Enqueue %d: %v", i, err)
		}
	}

	// Third message should be persisted but NOT pushed to channel.
	id3, err := q.Enqueue(ctx, queue.QueueMessage{
		AgentID: "agent-1",
		Content: "over-limit",
	})
	if err != nil {
		t.Fatalf("Enqueue 3: %v", err)
	}
	if id3 <= 0 {
		t.Fatalf("third message should still get a valid ID, got %d", id3)
	}

	// Drain the channel — should only get 2 messages.
	var count int
	for {
		select {
		case <-ch:
			count++
		case <-time.After(200 * time.Millisecond):
			goto done
		}
	}
done:
	if count != 2 {
		t.Errorf("channel messages: got %d, want 2", count)
	}
}

func TestPriorityBypassesDepth(t *testing.T) {
	cfg := queue.DefaultConfig()
	cfg.Depth = 1
	q := newTestQueue(t, cfg)
	ctx := context.Background()
	ch := q.Dequeue(ctx, "agent-1")

	// Fill to depth limit.
	if _, err := q.Enqueue(ctx, queue.QueueMessage{
		AgentID: "agent-1",
		Content: "normal",
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// Operator priority (2) should bypass depth limit.
	id, err := q.Enqueue(ctx, queue.QueueMessage{
		AgentID:  "agent-1",
		Content:  "operator-msg",
		Priority: 2,
	})
	if err != nil {
		t.Fatalf("Enqueue priority: %v", err)
	}
	if id <= 0 {
		t.Fatalf("expected valid ID, got %d", id)
	}

	// Both messages should be in the channel.
	var count int
	for {
		select {
		case <-ch:
			count++
		case <-time.After(200 * time.Millisecond):
			goto done
		}
	}
done:
	if count != 2 {
		t.Errorf("channel messages: got %d, want 2", count)
	}
}

func TestMarkProcessing(t *testing.T) {
	tdb := testutil.RequirePostgres(t)
	db := tdb.DB
	ctx := context.Background()

	q := mustNewPQ(t, db, queue.DefaultConfig())
	t.Cleanup(func() { q.Stop() })
	q.Dequeue(ctx, "agent-1")

	id, err := q.Enqueue(ctx, queue.QueueMessage{
		AgentID: "agent-1",
		Content: "test",
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	if err := q.MarkProcessing(ctx, id); err != nil {
		t.Fatalf("MarkProcessing: %v", err)
	}

	// Verify started_at is set in DB.
	var status string
	var startedAt *string
	err = db.QueryRowContext(ctx,
		`SELECT status, started_at FROM message_queue WHERE id = $1`, id,
	).Scan(&status, &startedAt)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if status != "processing" {
		t.Errorf("status: got %q, want processing", status)
	}
	if startedAt == nil {
		t.Error("started_at should be set")
	}
}

func TestListMessages_ParsesNaiveTimestampAsUTC(t *testing.T) {
	tdb := testutil.RequirePostgres(t)
	db := tdb.DB
	ctx := context.Background()

	_, err := db.ExecContext(ctx,
		`INSERT INTO message_queue (agent_id, channel, sender, content, status, attempts, max_attempts, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		"agent-1", "webui", "user-1", "hello", "pending", 0, 3, "2026-02-19 02:42:37",
	)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	q := mustNewPQ(t, db, queue.DefaultConfig())
	t.Cleanup(func() { q.Stop() })

	msgs, err := q.ListMessages(ctx, "agent-1", "", 10)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if got, want := msgs[0].CreatedAt.Location(), time.UTC; got != want {
		t.Fatalf("CreatedAt location = %v, want %v", got, want)
	}
	if got, want := msgs[0].CreatedAt.Format(time.RFC3339), "2026-02-19T02:42:37Z"; got != want {
		t.Fatalf("CreatedAt = %q, want %q", got, want)
	}
}

func TestStopClosesChannels(t *testing.T) {
	tdb := testutil.RequirePostgres(t)

	q := mustNewPQ(t, tdb.DB, queue.DefaultConfig())
	ctx := context.Background()
	ch := q.Dequeue(ctx, "agent-1")

	q.Stop()

	// Channel should be closed.
	_, ok := <-ch
	if ok {
		t.Error("channel should be closed after Stop()")
	}
}

func TestEnqueueNoChannel(t *testing.T) {
	q := newTestQueue(t, queue.DefaultConfig())
	ctx := context.Background()

	// Enqueue without calling Dequeue first — no channel exists.
	id, err := q.Enqueue(ctx, queue.QueueMessage{
		AgentID: "agent-no-channel",
		Content: "persisted only",
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if id <= 0 {
		t.Fatalf("expected positive ID, got %d", id)
	}

	// Message should be in DB.
	depth, err := q.Depth(ctx, "agent-no-channel")
	if err != nil {
		t.Fatalf("Depth: %v", err)
	}
	if depth != 1 {
		t.Errorf("Depth: got %d, want 1", depth)
	}
}

func TestReplayAgent_DoesNotDuplicateInFlightPending(t *testing.T) {
	q := newTestQueue(t, queue.DefaultConfig())
	ctx := context.Background()
	ch := q.Dequeue(ctx, "agent-1")

	_, err := q.Enqueue(ctx, queue.QueueMessage{
		AgentID: "agent-1",
		Content: "once-only",
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// First delivery should arrive from Enqueue push.
	var first queue.QueueMessage
	select {
	case first = <-ch:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial message")
	}

	// Replay should skip this still-pending in-flight message.
	if err := q.ReplayAgent(ctx, "agent-1"); err != nil {
		t.Fatalf("ReplayAgent: %v", err)
	}

	select {
	case dup := <-ch:
		t.Fatalf("unexpected duplicate delivery: %+v (first=%+v)", dup, first)
	case <-time.After(150 * time.Millisecond):
		// expected
	}
}
