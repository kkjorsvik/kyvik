package teams

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/store/postgres"
	"github.com/kkjorsvik/kyvik/internal/testutil"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

func newPairedTestDeps(t *testing.T) (*postgres.PostgresStore, *Bus, *PairedOrchestrator) {
	t.Helper()
	tdb := testutil.RequirePostgres(t)
	s := tdb.Store

	bus := NewBus(s.DB(), mockAuditLogger{})
	orch := NewPairedOrchestrator(bus, s, s.DB(), mockAuditLogger{})
	return s, bus, orch
}

func createPairedAgent(t *testing.T, s *postgres.PostgresStore, id, name string) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	err := s.CreateAgent(context.Background(), types.AgentConfig{
		ID:          id,
		Name:        name,
		Template:    "worker",
		ModelConfig: types.ModelConfig{Provider: "openrouter", Model: "test-model"},
		ActualState: types.AgentStatusRunning,
		CreatedAt:   now,
		UpdatedAt:   now,
	})
	if err != nil {
		t.Fatalf("CreateAgent(%s): %v", id, err)
	}
}

func runResponder(t *testing.T, bus *Bus, agentID string, fn func(string) string, delay time.Duration) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ch := bus.Subscribe(ctx, agentID)
	go func() {
		defer bus.Unsubscribe(agentID, ch)
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-ch:
				if !ok {
					return
				}
				if msg.Type == types.MessageTypeResult {
					continue
				}
				if delay > 0 {
					time.Sleep(delay)
				}
				_ = bus.Send(context.Background(), types.InternalMessage{
					From:     agentID,
					To:       msg.From,
					Content:  fn(msg.Content),
					Type:     types.MessageTypeResult,
					Metadata: msg.Metadata,
				})
			}
		}
	}()
	return cancel
}

func waitForConvStatus(t *testing.T, orch *PairedOrchestrator, convID string, status types.PairedStatus, timeout time.Duration) *types.PairedConversation {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conv, err := orch.GetConversation(context.Background(), convID)
		if err == nil && conv.Status == status {
			return conv
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("conversation %s did not reach status %s", convID, status)
	return nil
}

func waitForTurns(t *testing.T, orch *PairedOrchestrator, convID string, atLeast int, timeout time.Duration) *types.PairedConversation {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conv, err := orch.GetConversation(context.Background(), convID)
		if err == nil && conv.CurrentTurn >= atLeast {
			return conv
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("conversation %s did not reach %d turns", convID, atLeast)
	return nil
}

func TestPairedStartConversation(t *testing.T) {
	s, bus, orch := newPairedTestDeps(t)
	createPairedAgent(t, s, "a", "Agent A")
	createPairedAgent(t, s, "b", "Agent B")
	cancelA := runResponder(t, bus, "a", func(_ string) string { return "A: hello" }, 0)
	cancelB := runResponder(t, bus, "b", func(_ string) string { return "B: hi" }, 0)
	defer cancelA()
	defer cancelB()

	conv := types.PairedConversation{ID: "conv-start", AgentA: "a", AgentB: "b", Topic: "Discuss", MaxTurns: 2}
	if err := orch.Start(context.Background(), conv); err != nil {
		t.Fatalf("Start: %v", err)
	}

	got, err := orch.GetConversation(context.Background(), conv.ID)
	if err != nil {
		t.Fatalf("GetConversation: %v", err)
	}
	if got.ID != conv.ID || got.Status != types.PairedStatusActive {
		t.Fatalf("unexpected conversation state: %+v", got)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		msgs, err := bus.RecentMessages(context.Background(), "a", 5)
		if err != nil {
			t.Fatalf("RecentMessages(a): %v", err)
		}
		if len(msgs) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("expected topic to be delivered to agent A")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestPairedTurnExchange(t *testing.T) {
	s, bus, orch := newPairedTestDeps(t)
	createPairedAgent(t, s, "a", "Agent A")
	createPairedAgent(t, s, "b", "Agent B")
	cancelA := runResponder(t, bus, "a", func(in string) string { return "A got: " + in }, 0)
	cancelB := runResponder(t, bus, "b", func(in string) string { return "B got: " + in }, 0)
	defer cancelA()
	defer cancelB()

	conv := types.PairedConversation{ID: "conv-exchange", AgentA: "a", AgentB: "b", Topic: "Topic", MaxTurns: 2}
	if err := orch.Start(context.Background(), conv); err != nil {
		t.Fatalf("Start: %v", err)
	}

	_ = waitForTurns(t, orch, conv.ID, 2, 3*time.Second)
	all, err := orch.Messages(context.Background(), conv.ID)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(all) < 2 {
		t.Fatalf("message count = %d, want >=2", len(all))
	}
	if all[0].AgentID != "a" || all[1].AgentID != "b" {
		t.Fatalf("unexpected turn order: first=%s second=%s", all[0].AgentID, all[1].AgentID)
	}
}

func TestPairedMaxTurnsStopsConversation(t *testing.T) {
	s, bus, orch := newPairedTestDeps(t)
	createPairedAgent(t, s, "a", "Agent A")
	createPairedAgent(t, s, "b", "Agent B")
	defer runResponder(t, bus, "a", func(_ string) string { return "a" }, 0)()
	defer runResponder(t, bus, "b", func(_ string) string { return "b" }, 0)()

	conv := types.PairedConversation{ID: "conv-max", AgentA: "a", AgentB: "b", Topic: "Topic", MaxTurns: 1}
	if err := orch.Start(context.Background(), conv); err != nil {
		t.Fatalf("Start: %v", err)
	}

	got := waitForConvStatus(t, orch, conv.ID, types.PairedStatusCompleted, 3*time.Second)
	if got.CurrentTurn != 2 {
		t.Fatalf("current_turn = %d, want 2", got.CurrentTurn)
	}
}

func TestPairedAutoStopPhrase(t *testing.T) {
	s, bus, orch := newPairedTestDeps(t)
	createPairedAgent(t, s, "a", "Agent A")
	createPairedAgent(t, s, "b", "Agent B")
	defer runResponder(t, bus, "a", func(_ string) string { return "Please STOP NOW." }, 0)()
	defer runResponder(t, bus, "b", func(_ string) string { return "b" }, 0)()

	conv := types.PairedConversation{
		ID:              "conv-stop",
		AgentA:          "a",
		AgentB:          "b",
		Topic:           "Topic",
		MaxTurns:        5,
		AutoStopPhrases: []string{"stop now"},
	}
	if err := orch.Start(context.Background(), conv); err != nil {
		t.Fatalf("Start: %v", err)
	}

	got := waitForConvStatus(t, orch, conv.ID, types.PairedStatusCompleted, 3*time.Second)
	if got.CurrentTurn != 1 {
		t.Fatalf("current_turn = %d, want 1", got.CurrentTurn)
	}
}

func TestPairedPauseResume(t *testing.T) {
	s, bus, orch := newPairedTestDeps(t)
	createPairedAgent(t, s, "a", "Agent A")
	createPairedAgent(t, s, "b", "Agent B")
	defer runResponder(t, bus, "a", func(_ string) string { return "a" }, 20*time.Millisecond)()
	defer runResponder(t, bus, "b", func(_ string) string { return "b" }, 20*time.Millisecond)()

	conv := types.PairedConversation{ID: "conv-pause", AgentA: "a", AgentB: "b", Topic: "Topic", MaxTurns: 5, TurnDelayMs: 100}
	if err := orch.Start(context.Background(), conv); err != nil {
		t.Fatalf("Start: %v", err)
	}

	time.Sleep(50 * time.Millisecond)
	if err := orch.Pause(context.Background(), conv.ID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	paused := waitForConvStatus(t, orch, conv.ID, types.PairedStatusPaused, 3*time.Second)
	turnAtPause := paused.CurrentTurn
	time.Sleep(250 * time.Millisecond)
	stillPaused, _ := orch.GetConversation(context.Background(), conv.ID)
	if stillPaused.CurrentTurn != turnAtPause {
		t.Fatalf("turn advanced while paused: before=%d after=%d", turnAtPause, stillPaused.CurrentTurn)
	}

	if err := orch.Resume(context.Background(), conv.ID); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	resumed := waitForConvStatus(t, orch, conv.ID, types.PairedStatusActive, 3*time.Second)
	_ = waitForTurns(t, orch, resumed.ID, turnAtPause+1, 3*time.Second)
}

func TestPairedStopImmediately(t *testing.T) {
	s, bus, orch := newPairedTestDeps(t)
	createPairedAgent(t, s, "a", "Agent A")
	createPairedAgent(t, s, "b", "Agent B")
	defer runResponder(t, bus, "a", func(_ string) string { return "a" }, 50*time.Millisecond)()
	defer runResponder(t, bus, "b", func(_ string) string { return "b" }, 50*time.Millisecond)()

	conv := types.PairedConversation{ID: "conv-stop-now", AgentA: "a", AgentB: "b", Topic: "Topic", MaxTurns: 10}
	if err := orch.Start(context.Background(), conv); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(30 * time.Millisecond)
	if err := orch.Stop(context.Background(), conv.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	_ = waitForConvStatus(t, orch, conv.ID, types.PairedStatusStopped, 2*time.Second)
}

func TestPairedInjectMessage(t *testing.T) {
	s, bus, orch := newPairedTestDeps(t)
	createPairedAgent(t, s, "a", "Agent A")
	createPairedAgent(t, s, "b", "Agent B")
	defer runResponder(t, bus, "a", func(in string) string { return "a:" + in }, 10*time.Millisecond)()
	defer runResponder(t, bus, "b", func(in string) string { return "b:" + in }, 10*time.Millisecond)()

	conv := types.PairedConversation{ID: "conv-inject", AgentA: "a", AgentB: "b", Topic: "Topic", MaxTurns: 3, AllowUserInjection: true, TurnDelayMs: 80}
	if err := orch.Start(context.Background(), conv); err != nil {
		t.Fatalf("Start: %v", err)
	}
	_ = waitForTurns(t, orch, conv.ID, 1, 3*time.Second)
	if err := orch.Inject(context.Background(), conv.ID, "Please focus on risks"); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	_ = waitForTurns(t, orch, conv.ID, 2, 3*time.Second)
	msgs, err := orch.Messages(context.Background(), conv.ID)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	found := false
	for _, m := range msgs {
		if m.InjectedBy == "user" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected at least one injected message marker")
	}
}

func TestPairedTokenTrackingTotalsUpdated(t *testing.T) {
	s, bus, orch := newPairedTestDeps(t)
	createPairedAgent(t, s, "a", "Agent A")
	createPairedAgent(t, s, "b", "Agent B")
	defer runResponder(t, bus, "a", func(_ string) string { return strings.Repeat("alpha ", 20) }, 0)()
	defer runResponder(t, bus, "b", func(_ string) string { return strings.Repeat("beta ", 20) }, 0)()

	conv := types.PairedConversation{ID: "conv-tokens", AgentA: "a", AgentB: "b", Topic: "Topic", MaxTurns: 1}
	if err := orch.Start(context.Background(), conv); err != nil {
		t.Fatalf("Start: %v", err)
	}
	got := waitForConvStatus(t, orch, conv.ID, types.PairedStatusCompleted, 3*time.Second)
	if got.TotalTokens <= 0 {
		t.Fatalf("total_tokens = %d, want > 0", got.TotalTokens)
	}
}

func TestPairedTurnDelayApplied(t *testing.T) {
	s, bus, orch := newPairedTestDeps(t)
	createPairedAgent(t, s, "a", "Agent A")
	createPairedAgent(t, s, "b", "Agent B")
	defer runResponder(t, bus, "a", func(_ string) string { return "a" }, 0)()
	defer runResponder(t, bus, "b", func(_ string) string { return "b" }, 0)()

	conv := types.PairedConversation{ID: "conv-delay", AgentA: "a", AgentB: "b", Topic: "Topic", MaxTurns: 2, TurnDelayMs: 120}
	if err := orch.Start(context.Background(), conv); err != nil {
		t.Fatalf("Start: %v", err)
	}
	_ = waitForTurns(t, orch, conv.ID, 2, 4*time.Second)
	msgs, err := orch.Messages(context.Background(), conv.ID)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(msgs) < 2 {
		t.Fatalf("expected at least 2 messages, got %d", len(msgs))
	}
	delta := msgs[1].CreatedAt.Sub(msgs[0].CreatedAt)
	if delta < 100*time.Millisecond {
		t.Fatalf("turn delay too short: %v", delta)
	}
}
