package teams

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/audit"
	"github.com/kkjorsvik/kyvik/internal/testutil"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// mockAuditLogger is a no-op audit logger for tests.
type mockAuditLogger struct{}

func (mockAuditLogger) Log(_ context.Context, _ types.AuditEntry) error { return nil }
func (mockAuditLogger) Query(_ context.Context, _ audit.Filter) ([]types.AuditEntry, error) {
	return nil, nil
}
func (mockAuditLogger) Stream(_ context.Context, _ string) (<-chan types.AuditEntry, error) {
	return nil, nil
}
func (mockAuditLogger) Subscribe(_ context.Context, _ audit.SubscriptionFilter) (<-chan types.AuditEntry, error) {
	ch := make(chan types.AuditEntry)
	close(ch)
	return ch, nil
}
func (mockAuditLogger) Close() error { return nil }

// mockTeamStore returns a pre-configured team.
type mockTeamStore struct {
	team *types.Team
}

func (m mockTeamStore) GetTeam(_ context.Context, id string) (*types.Team, error) {
	if m.team != nil && m.team.ID == id {
		return m.team, nil
	}
	return nil, types.ErrTeamNotFound
}

func newTestBus(t *testing.T) *Bus {
	t.Helper()
	tdb := testutil.RequirePostgres(t)
	return NewBus(tdb.DB, mockAuditLogger{})
}

func TestSendAndSubscribe(t *testing.T) {
	bus := newTestBus(t)
	ctx := context.Background()

	ch := bus.Subscribe(ctx, "agent-b")

	err := bus.Send(ctx, types.InternalMessage{
		From:    "agent-a",
		To:      "agent-b",
		Content: "hello",
		Type:    types.MessageTypeTask,
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	select {
	case msg := <-ch:
		if msg.From != "agent-a" {
			t.Errorf("from = %q, want agent-a", msg.From)
		}
		if msg.To != "agent-b" {
			t.Errorf("to = %q, want agent-b", msg.To)
		}
		if msg.Content != "hello" {
			t.Errorf("content = %q, want hello", msg.Content)
		}
		if msg.Type != types.MessageTypeTask {
			t.Errorf("type = %q, want task", msg.Type)
		}
		if msg.ID == "" {
			t.Error("expected non-empty ID")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for message")
	}
}

func TestSendPersistence(t *testing.T) {
	bus := newTestBus(t)
	ctx := context.Background()

	// Send without any subscribers.
	err := bus.Send(ctx, types.InternalMessage{
		From:    "agent-a",
		To:      "agent-b",
		Content: "persisted",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	msgs, err := bus.RecentMessages(ctx, "agent-b", 10)
	if err != nil {
		t.Fatalf("recent messages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	if msgs[0].Content != "persisted" {
		t.Errorf("content = %q, want persisted", msgs[0].Content)
	}
	if msgs[0].Type != types.MessageTypeMessage {
		t.Errorf("type = %q, want message (default)", msgs[0].Type)
	}
	if msgs[0].Priority != types.MessagePriorityNormal {
		t.Errorf("priority = %q, want normal (default)", msgs[0].Priority)
	}
}

func TestUnsubscribe(t *testing.T) {
	bus := newTestBus(t)
	ctx := context.Background()

	ch := bus.Subscribe(ctx, "agent-x")
	bus.Unsubscribe("agent-x", ch)

	// Channel should be closed.
	_, ok := <-ch
	if ok {
		t.Error("expected channel to be closed")
	}
}

func TestBroadcast(t *testing.T) {
	bus := newTestBus(t)
	ctx := context.Background()

	team := &types.Team{
		ID:        "team-1",
		LeaderID:  "leader",
		MemberIDs: []string{"leader", "member-1", "member-2"},
		Active:    true,
	}
	ts := mockTeamStore{team: team}

	ch1 := bus.Subscribe(ctx, "member-1")
	ch2 := bus.Subscribe(ctx, "member-2")

	err := bus.Broadcast(ctx, "leader", "team-1", "team update", ts)
	if err != nil {
		t.Fatalf("broadcast: %v", err)
	}

	for _, ch := range []<-chan types.InternalMessage{ch1, ch2} {
		select {
		case msg := <-ch:
			if msg.From != "leader" {
				t.Errorf("from = %q, want leader", msg.From)
			}
			if msg.Content != "team update" {
				t.Errorf("content = %q, want 'team update'", msg.Content)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for broadcast message")
		}
	}

	// Leader should NOT receive the message.
	msgs, err := bus.RecentMessages(ctx, "leader", 10)
	if err != nil {
		t.Fatalf("recent messages: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("leader received %d messages, want 0", len(msgs))
	}
}

func TestRecentMessages(t *testing.T) {
	bus := newTestBus(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		err := bus.Send(ctx, types.InternalMessage{
			From:    "sender",
			To:      "receiver",
			Content: fmt.Sprintf("msg-%d", i),
		})
		if err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}

	msgs, err := bus.RecentMessages(ctx, "receiver", 3)
	if err != nil {
		t.Fatalf("recent messages: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("got %d messages, want 3", len(msgs))
	}

	// Most recent first (DESC order).
	if msgs[0].Content != "msg-4" {
		t.Errorf("first message = %q, want msg-4", msgs[0].Content)
	}
	if msgs[2].Content != "msg-2" {
		t.Errorf("last message = %q, want msg-2", msgs[2].Content)
	}
}

func TestMessagesBetween(t *testing.T) {
	bus := newTestBus(t)
	ctx := context.Background()

	err := bus.Send(ctx, types.InternalMessage{
		From: "alice", To: "bob", Content: "hi bob",
	})
	if err != nil {
		t.Fatalf("send a->b: %v", err)
	}
	err = bus.Send(ctx, types.InternalMessage{
		From: "bob", To: "alice", Content: "hi alice",
	})
	if err != nil {
		t.Fatalf("send b->a: %v", err)
	}
	// Unrelated message should not appear.
	err = bus.Send(ctx, types.InternalMessage{
		From: "charlie", To: "dave", Content: "irrelevant",
	})
	if err != nil {
		t.Fatalf("send c->d: %v", err)
	}

	msgs, err := bus.MessagesBetween(ctx, "alice", "bob", 10)
	if err != nil {
		t.Fatalf("messages between: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2", len(msgs))
	}
}

func TestConcurrentSend(t *testing.T) {
	bus := newTestBus(t)
	ctx := context.Background()

	const goroutines = 10
	const perGoroutine = 10

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				err := bus.Send(ctx, types.InternalMessage{
					From:    fmt.Sprintf("sender-%d", g),
					To:      "target",
					Content: fmt.Sprintf("g%d-m%d", g, i),
				})
				if err != nil {
					t.Errorf("send g%d m%d: %v", g, i, err)
				}
			}
		}(g)
	}
	wg.Wait()

	msgs, err := bus.RecentMessages(ctx, "target", 200)
	if err != nil {
		t.Fatalf("recent messages: %v", err)
	}
	if len(msgs) != goroutines*perGoroutine {
		t.Errorf("got %d messages, want %d", len(msgs), goroutines*perGoroutine)
	}
}
