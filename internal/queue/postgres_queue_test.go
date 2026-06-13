package queue

import (
	"context"
	"testing"

	"github.com/kkjorsvik/kyvik/internal/testutil"
)

func TestPostgresQueue_EnqueueDequeue(t *testing.T) {
	tdb := testutil.RequirePostgres(t)
	ctx := context.Background()

	q, err := NewPostgresQueue(tdb.DB, "node-1", "")
	if err != nil {
		t.Fatalf("NewPostgresQueue: %v", err)
	}
	defer q.Stop()

	// Register the channel before enqueue so the message is pushed immediately.
	ch := q.Dequeue(ctx, "agent-1")

	msg := QueueMessage{
		AgentID:      "agent-1",
		Channel:      "test",
		Content:      "hello",
		TargetNodeID: "node-1",
	}

	if _, err := q.Enqueue(ctx, msg); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	select {
	case got := <-ch:
		if got.Content != "hello" {
			t.Errorf("expected 'hello', got %q", got.Content)
		}
		if got.TargetNodeID != "node-1" {
			t.Errorf("expected target_node_id 'node-1', got %q", got.TargetNodeID)
		}
	default:
		t.Fatal("expected message in channel")
	}
}

func TestPostgresQueue_ReplayAgent(t *testing.T) {
	tdb := testutil.RequirePostgres(t)
	ctx := context.Background()

	q, err := NewPostgresQueue(tdb.DB, "node-1", "")
	if err != nil {
		t.Fatalf("NewPostgresQueue: %v", err)
	}
	defer q.Stop()

	// Enqueue without a channel registered — message stays in DB only.
	msg := QueueMessage{
		AgentID:      "agent-1",
		Channel:      "test",
		Content:      "replay-me",
		TargetNodeID: "node-1",
	}

	if _, err := q.Enqueue(ctx, msg); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// Now register the channel and replay.
	ch := q.Dequeue(ctx, "agent-1")
	if err := q.ReplayAgent(ctx, "agent-1"); err != nil {
		t.Fatalf("ReplayAgent: %v", err)
	}

	select {
	case got := <-ch:
		if got.Content != "replay-me" {
			t.Errorf("expected 'replay-me', got %q", got.Content)
		}
	default:
		t.Fatal("expected replayed message in channel")
	}
}

func TestPostgresQueue_SkipOtherNode(t *testing.T) {
	tdb := testutil.RequirePostgres(t)
	ctx := context.Background()

	q, err := NewPostgresQueue(tdb.DB, "node-1", "")
	if err != nil {
		t.Fatalf("NewPostgresQueue: %v", err)
	}
	defer q.Stop()

	// Register channel first so Enqueue can try to push.
	_ = q.Dequeue(ctx, "agent-1")

	// Enqueue a message targeted at a different node.
	msg := QueueMessage{
		AgentID:      "agent-1",
		Channel:      "test",
		Content:      "for node-2",
		TargetNodeID: "node-2",
	}

	id, err := q.Enqueue(ctx, msg)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if id == 0 {
		t.Fatal("expected non-zero message ID")
	}

	// The message should NOT be pushed to the in-memory channel since it's for node-2.
	ch := q.Dequeue(ctx, "agent-1")
	select {
	case got := <-ch:
		t.Fatalf("should not have received message for other node, got: %+v", got)
	default:
		// Expected: channel is empty because the message is for another node.
	}
}

func TestPostgresQueue_CompleteAndStats(t *testing.T) {
	tdb := testutil.RequirePostgres(t)
	ctx := context.Background()

	q, err := NewPostgresQueue(tdb.DB, "node-1", "")
	if err != nil {
		t.Fatalf("NewPostgresQueue: %v", err)
	}
	defer q.Stop()

	msg := QueueMessage{
		AgentID:      "agent-1",
		Channel:      "test",
		Content:      "stats-test",
		TargetNodeID: "node-1",
	}

	id, err := q.Enqueue(ctx, msg)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	if err := q.MarkProcessing(ctx, id); err != nil {
		t.Fatalf("MarkProcessing: %v", err)
	}

	if err := q.Complete(ctx, id); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	stats, err := q.Stats(ctx, "agent-1")
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}

	if stats[StatusCompleted] != 1 {
		t.Errorf("expected 1 completed, got %d", stats[StatusCompleted])
	}
}
