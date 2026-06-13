package integration

import (
	"context"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/audit"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// =============================================================================
// Scenario: Team Delegation
// Tests team creation, bus messaging, broadcast, and audit of internal comms.
// =============================================================================

func TestScenario_Teams_CreateAndVerify(t *testing.T) {
	// t.Parallel() — disabled: shared PostgreSQL database
	h := newFullHarness(t)

	h.seedAgent(t, "leader", "Leader", "admin")
	h.seedAgent(t, "member-1", "Member 1", "worker")
	h.seedAgent(t, "member-2", "Member 2", "worker")

	h.createTeam(t, "team-1", "Alpha Team", "leader", []string{"member-1", "member-2"})

	// Verify team was created.
	team, err := h.teamMgr.GetTeam(context.Background(), "team-1")
	if err != nil {
		t.Fatalf("GetTeam: %v", err)
	}
	if team.LeaderID != "leader" {
		t.Fatalf("expected leader, got %s", team.LeaderID)
	}
	// CreateTeam auto-adds the leader to MemberIDs if not present.
	if len(team.MemberIDs) != 3 {
		t.Fatalf("expected 3 members (leader + 2), got %d", len(team.MemberIDs))
	}

	// Verify agents have TeamID set.
	agent, err := h.store.GetAgent(context.Background(), "member-1")
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if agent.TeamID != "team-1" {
		t.Fatalf("expected TeamID=team-1, got %q", agent.TeamID)
	}
}

func TestScenario_Teams_BusMessageDelivery(t *testing.T) {
	// t.Parallel() — disabled: shared PostgreSQL database
	h := newFullHarness(t)

	h.seedAgent(t, "bus-leader", "Bus Leader", "admin")
	h.seedAgent(t, "bus-member", "Bus Member", "worker")

	h.createTeam(t, "bus-team", "Bus Team", "bus-leader", []string{"bus-member"})

	// Subscribe member to bus.
	ch := h.teamBus.Subscribe(context.Background(), "bus-member")
	defer h.teamBus.Unsubscribe("bus-member", ch)

	// Send from leader to member.
	err := h.teamBus.Send(context.Background(), types.InternalMessage{
		From:    "bus-leader",
		To:      "bus-member",
		Content: "task assignment",
		Type:    types.MessageTypeTask,
	})
	if err != nil {
		t.Fatalf("bus.Send: %v", err)
	}

	// Member should receive the message.
	select {
	case msg := <-ch:
		if msg.Content != "task assignment" {
			t.Fatalf("expected 'task assignment', got %q", msg.Content)
		}
		if msg.From != "bus-leader" {
			t.Fatalf("expected from bus-leader, got %s", msg.From)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for bus message")
	}

	// Verify message appears in recent messages.
	recent, err := h.teamBus.RecentMessages(context.Background(), "bus-member", 10)
	if err != nil {
		t.Fatalf("RecentMessages: %v", err)
	}
	found := false
	for _, m := range recent {
		if m.Content == "task assignment" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("message not found in RecentMessages")
	}
}

func TestScenario_Teams_BroadcastToAll(t *testing.T) {
	// t.Parallel() — disabled: shared PostgreSQL database
	h := newFullHarness(t)

	h.seedAgent(t, "bc-leader", "BC Leader", "admin")
	h.seedAgent(t, "bc-m1", "BC Member 1", "worker")
	h.seedAgent(t, "bc-m2", "BC Member 2", "worker")

	h.createTeam(t, "bc-team", "Broadcast Team", "bc-leader", []string{"bc-m1", "bc-m2"})

	ch1 := h.teamBus.Subscribe(context.Background(), "bc-m1")
	defer h.teamBus.Unsubscribe("bc-m1", ch1)
	ch2 := h.teamBus.Subscribe(context.Background(), "bc-m2")
	defer h.teamBus.Unsubscribe("bc-m2", ch2)

	// Broadcast from leader.
	err := h.teamBus.Broadcast(context.Background(), "bc-leader", "bc-team", "team update", h.store)
	if err != nil {
		t.Fatalf("Broadcast: %v", err)
	}

	// Both members should receive.
	for _, ch := range []<-chan types.InternalMessage{ch1, ch2} {
		select {
		case msg := <-ch:
			if msg.Content != "team update" {
				t.Fatalf("expected 'team update', got %q", msg.Content)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for broadcast")
		}
	}
}

func TestScenario_Teams_NonMemberIsolation(t *testing.T) {
	// t.Parallel() — disabled: shared PostgreSQL database
	h := newFullHarness(t)

	h.seedAgent(t, "iso-leader", "Iso Leader", "admin")
	h.seedAgent(t, "iso-member", "Iso Member", "worker")
	h.seedAgent(t, "iso-outsider", "Outsider", "worker")

	h.createTeam(t, "iso-team", "Iso Team", "iso-leader", []string{"iso-member"})

	// Outsider should not have TeamID set.
	outsider, err := h.store.GetAgent(context.Background(), "iso-outsider")
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if outsider.TeamID != "" {
		t.Fatalf("outsider should have no TeamID, got %q", outsider.TeamID)
	}
}

func TestScenario_Teams_DelegationViaBus(t *testing.T) {
	// t.Parallel() — disabled: shared PostgreSQL database
	h := newFullHarness(t)

	h.seedAgent(t, "del-leader", "Del Leader", "admin")
	h.seedAgent(t, "del-worker", "Del Worker", "worker")

	h.createTeam(t, "del-team", "Delegation Team", "del-leader", []string{"del-worker"})

	// Subscribe worker.
	ch := h.teamBus.Subscribe(context.Background(), "del-worker")
	defer h.teamBus.Unsubscribe("del-worker", ch)

	// Leader sends delegation.
	err := h.teamBus.Send(context.Background(), types.InternalMessage{
		From:    "del-leader",
		To:      "del-worker",
		Content: "please handle task X",
		Type:    types.MessageTypeTask,
	})
	if err != nil {
		t.Fatalf("bus.Send: %v", err)
	}

	// Worker receives.
	select {
	case msg := <-ch:
		if msg.Content != "please handle task X" {
			t.Fatalf("wrong content: %q", msg.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out")
	}

	// Verify MessagesBetween or RecentMessages shows the exchange.
	recent, err := h.teamBus.RecentMessages(context.Background(), "del-worker", 10)
	if err != nil {
		t.Fatalf("RecentMessages: %v", err)
	}
	if len(recent) == 0 {
		t.Fatal("expected at least one message in recent")
	}
}

func TestScenario_Teams_AuditLogsInternalMessages(t *testing.T) {
	// t.Parallel() — disabled: shared PostgreSQL database
	h := newFullHarness(t)

	h.seedAgent(t, "aud-leader", "Aud Leader", "admin")
	h.seedAgent(t, "aud-member", "Aud Member", "worker")

	h.createTeam(t, "aud-team", "Audit Team", "aud-leader", []string{"aud-member"})

	// Send bus message.
	err := h.teamBus.Send(context.Background(), types.InternalMessage{
		From:    "aud-leader",
		To:      "aud-member",
		Content: "audit me",
		Type:    types.MessageTypeTask,
	})
	if err != nil {
		t.Fatalf("bus.Send: %v", err)
	}

	// Wait for audit flush.
	time.Sleep(300 * time.Millisecond)

	// Check audit for internal message entries.
	entries, err := h.audit.Query(context.Background(), audit.Filter{Limit: 100})
	if err != nil {
		t.Fatalf("audit query: %v", err)
	}

	found := false
	for _, e := range entries {
		if e.EventType == "internal_message" || e.Action == "bus_send" || e.Action == "internal_message" {
			found = true
			break
		}
	}
	if !found {
		// List what we do have for debugging.
		actions := make([]string, 0, len(entries))
		for _, e := range entries {
			actions = append(actions, string(e.EventType)+":"+e.Action)
		}
		t.Fatalf("expected audit entry for internal message, found %d entries: %v", len(entries), actions)
	}
}
