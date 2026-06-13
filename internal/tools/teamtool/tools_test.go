package teamtool

import (
	"context"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/audit"
	"github.com/kkjorsvik/kyvik/internal/channels/busadapter"
	"github.com/kkjorsvik/kyvik/internal/ktp"
	"github.com/kkjorsvik/kyvik/internal/store/postgres"
	"github.com/kkjorsvik/kyvik/internal/teams"
	"github.com/kkjorsvik/kyvik/internal/testutil"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

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

func newTestDeps(t *testing.T) (*postgres.PostgresStore, *teams.Bus, *teams.Manager, AgentLookup) {
	t.Helper()
	tdb := testutil.RequirePostgres(t)
	s := tdb.Store

	bus := teams.NewBus(s.DB(), mockAuditLogger{})
	mgr := teams.NewManager(s, bus, mockAuditLogger{})
	lookup := func(ctx context.Context, id string) (*types.AgentConfig, error) {
		return s.GetAgent(ctx, id)
	}
	return s, bus, mgr, lookup
}

func createAgent(t *testing.T, s *postgres.PostgresStore, id, name string, state types.AgentStatus) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	err := s.CreateAgent(context.Background(), types.AgentConfig{
		ID:          id,
		Name:        name,
		Template:    "worker",
		ModelConfig: types.ModelConfig{Provider: "openrouter", Model: "test-model"},
		ActualState: state,
		CreatedAt:   now,
		UpdatedAt:   now,
	})
	if err != nil {
		t.Fatalf("CreateAgent(%s): %v", id, err)
	}
}

func createTeam(t *testing.T, mgr *teams.Manager, id, leaderID string, members []string, mode types.TeamCommunication) {
	t.Helper()
	err := mgr.CreateTeam(context.Background(), types.Team{
		ID:            id,
		Name:          id,
		LeaderID:      leaderID,
		MemberIDs:     members,
		Communication: mode,
	})
	if err != nil {
		t.Fatalf("CreateTeam(%s): %v", id, err)
	}
}

func TestDelegateLeaderToMember(t *testing.T) {
	s, bus, mgr, lookup := newTestDeps(t)
	createAgent(t, s, "leader-1", "Leader", types.AgentStatusRunning)
	createAgent(t, s, "member-1", "Member", types.AgentStatusRunning)
	createTeam(t, mgr, "team-1", "leader-1", []string{"leader-1", "member-1"}, types.TeamCommLeaderMediated)

	tool := NewDelegateTool(mgr, bus, lookup)
	resp, err := tool.Execute(context.Background(), ktp.NewToolRequest("leader-1", "team.delegate", "delegate", map[string]any{
		"to":   "member-1",
		"task": "Investigate alert #42",
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	msgs, err := bus.RecentMessages(context.Background(), "member-1", 10)
	if err != nil {
		t.Fatalf("RecentMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("message count = %d, want 1", len(msgs))
	}
	if msgs[0].Type != types.MessageTypeTask {
		t.Fatalf("message type = %q, want task", msgs[0].Type)
	}
	if msgs[0].Metadata["task_id"] == "" {
		t.Fatal("expected delegated task_id metadata")
	}
}

func TestDelegateToNonMemberFails(t *testing.T) {
	s, bus, mgr, lookup := newTestDeps(t)
	createAgent(t, s, "leader-1", "Leader", types.AgentStatusRunning)
	createAgent(t, s, "member-1", "Member", types.AgentStatusRunning)
	createAgent(t, s, "outsider", "Outsider", types.AgentStatusRunning)
	createTeam(t, mgr, "team-1", "leader-1", []string{"leader-1", "member-1"}, types.TeamCommLeaderMediated)

	tool := NewDelegateTool(mgr, bus, lookup)
	resp, err := tool.Execute(context.Background(), ktp.NewToolRequest("leader-1", "team.delegate", "delegate", map[string]any{
		"to":   "outsider",
		"task": "Should fail",
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if resp.Success {
		t.Fatal("expected delegation failure for non-member")
	}
}

func TestDelegateByNonLeaderFails(t *testing.T) {
	s, bus, mgr, lookup := newTestDeps(t)
	createAgent(t, s, "leader-1", "Leader", types.AgentStatusRunning)
	createAgent(t, s, "member-1", "Member", types.AgentStatusRunning)
	createTeam(t, mgr, "team-1", "leader-1", []string{"leader-1", "member-1"}, types.TeamCommLeaderMediated)

	tool := NewDelegateTool(mgr, bus, lookup)
	resp, err := tool.Execute(context.Background(), ktp.NewToolRequest("member-1", "team.delegate", "delegate", map[string]any{
		"to":   "leader-1",
		"task": "Do this",
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if resp.Success {
		t.Fatal("expected non-leader delegation to fail")
	}
}

func TestDelegateParallelMultipleMembers(t *testing.T) {
	s, bus, mgr, lookup := newTestDeps(t)
	createAgent(t, s, "leader-1", "Leader", types.AgentStatusRunning)
	createAgent(t, s, "member-1", "Member One", types.AgentStatusRunning)
	createAgent(t, s, "member-2", "Member Two", types.AgentStatusRunning)
	createTeam(t, mgr, "team-1", "leader-1", []string{"leader-1", "member-1", "member-2"}, types.TeamCommLeaderMediated)

	tool := NewDelegateTool(mgr, bus, lookup)
	resp, err := tool.Execute(context.Background(), ktp.NewToolRequest("leader-1", "team.delegate", "delegate", map[string]any{
		"to":       []any{"member-1", "member-2"},
		"task":     "Work in parallel",
		"parallel": true,
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got %s", resp.Error)
	}

	for _, id := range []string{"member-1", "member-2"} {
		msgs, err := bus.RecentMessages(context.Background(), id, 10)
		if err != nil {
			t.Fatalf("RecentMessages(%s): %v", id, err)
		}
		if len(msgs) != 1 {
			t.Fatalf("message count for %s = %d, want 1", id, len(msgs))
		}
		if msgs[0].Metadata["parallel"] != "true" {
			t.Fatalf("parallel metadata for %s = %q, want true", id, msgs[0].Metadata["parallel"])
		}
	}
}

func TestBroadcastAllMembersReceive(t *testing.T) {
	s, bus, mgr, _ := newTestDeps(t)
	createAgent(t, s, "leader-1", "Leader", types.AgentStatusRunning)
	createAgent(t, s, "member-1", "Member One", types.AgentStatusRunning)
	createAgent(t, s, "member-2", "Member Two", types.AgentStatusRunning)
	createTeam(t, mgr, "team-1", "leader-1", []string{"leader-1", "member-1", "member-2"}, types.TeamCommLeaderMediated)

	tool := NewBroadcastTool(mgr, bus)
	resp, err := tool.Execute(context.Background(), ktp.NewToolRequest("leader-1", "team.broadcast", "broadcast", map[string]any{
		"message":      "Daily standup in 5 minutes",
		"exclude_self": false,
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got %s", resp.Error)
	}

	for _, id := range []string{"leader-1", "member-1", "member-2"} {
		msgs, err := bus.RecentMessages(context.Background(), id, 10)
		if err != nil {
			t.Fatalf("RecentMessages(%s): %v", id, err)
		}
		if len(msgs) != 1 {
			t.Fatalf("message count for %s = %d, want 1", id, len(msgs))
		}
	}
}

func TestBroadcastExcludeSelf(t *testing.T) {
	s, bus, mgr, _ := newTestDeps(t)
	createAgent(t, s, "leader-1", "Leader", types.AgentStatusRunning)
	createAgent(t, s, "member-1", "Member", types.AgentStatusRunning)
	createTeam(t, mgr, "team-1", "leader-1", []string{"leader-1", "member-1"}, types.TeamCommLeaderMediated)

	tool := NewBroadcastTool(mgr, bus)
	resp, err := tool.Execute(context.Background(), ktp.NewToolRequest("leader-1", "team.broadcast", "broadcast", map[string]any{
		"message": "Only member should receive",
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got %s", resp.Error)
	}

	leaderMsgs, _ := bus.RecentMessages(context.Background(), "leader-1", 10)
	if len(leaderMsgs) != 0 {
		t.Fatalf("leader should have 0 messages, got %d", len(leaderMsgs))
	}
	memberMsgs, _ := bus.RecentMessages(context.Background(), "member-1", 10)
	if len(memberMsgs) != 1 {
		t.Fatalf("member should have 1 message, got %d", len(memberMsgs))
	}
}

func TestStatusReturnsAccurateMembers(t *testing.T) {
	s, bus, mgr, lookup := newTestDeps(t)
	createAgent(t, s, "leader-1", "Leader", types.AgentStatusRunning)
	createAgent(t, s, "member-1", "Member One", types.AgentStatusStopped)
	createTeam(t, mgr, "team-1", "leader-1", []string{"leader-1", "member-1"}, types.TeamCommLeaderMediated)

	_ = bus.Send(context.Background(), types.InternalMessage{From: "leader-1", To: "member-1", Content: "task"})

	tool := NewStatusTool(mgr, lookup)
	resp, err := tool.Execute(context.Background(), ktp.NewToolRequest("leader-1", "team.status", "status", map[string]any{}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got %s", resp.Error)
	}

	result := resp.Result.(map[string]any)
	members := result["members"].([]map[string]any)
	if len(members) != 2 {
		t.Fatalf("members count = %d, want 2", len(members))
	}

	depthByID := map[string]int{}
	for _, m := range members {
		depthByID[m["agent_id"].(string)] = m["queue_depth"].(int)
	}
	if depthByID["member-1"] != 1 {
		t.Fatalf("member-1 queue depth = %d, want 1", depthByID["member-1"])
	}
}

func TestRecallSendsUrgentStatusMessage(t *testing.T) {
	s, bus, mgr, lookup := newTestDeps(t)
	createAgent(t, s, "leader-1", "Leader", types.AgentStatusRunning)
	createAgent(t, s, "member-1", "Member", types.AgentStatusRunning)
	createTeam(t, mgr, "team-1", "leader-1", []string{"leader-1", "member-1"}, types.TeamCommLeaderMediated)

	tool := NewRecallTool(mgr, bus, lookup)
	resp, err := tool.Execute(context.Background(), ktp.NewToolRequest("leader-1", "team.recall", "recall", map[string]any{
		"member":  "member-1",
		"task_id": "task-123",
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got %s", resp.Error)
	}

	msgs, err := bus.RecentMessages(context.Background(), "member-1", 10)
	if err != nil {
		t.Fatalf("RecentMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("message count = %d, want 1", len(msgs))
	}
	if msgs[0].Type != types.MessageTypeStatus {
		t.Fatalf("message type = %q, want status", msgs[0].Type)
	}
	if msgs[0].Priority != types.MessagePriorityUrgent {
		t.Fatalf("priority = %q, want urgent", msgs[0].Priority)
	}
	if msgs[0].Metadata["recall"] != "true" {
		t.Fatalf("recall metadata = %q, want true", msgs[0].Metadata["recall"])
	}
}

func TestLeaderMediatedMemberToNonLeaderDenied(t *testing.T) {
	s, bus, mgr, _ := newTestDeps(t)
	createAgent(t, s, "leader-1", "Leader", types.AgentStatusRunning)
	createAgent(t, s, "member-1", "Member One", types.AgentStatusRunning)
	createAgent(t, s, "member-2", "Member Two", types.AgentStatusRunning)
	createTeam(t, mgr, "team-1", "leader-1", []string{"leader-1", "member-1", "member-2"}, types.TeamCommLeaderMediated)

	adapter := busadapter.New(bus)
	defer adapter.Close()
	adapter.SetConfigLookup(func(ctx context.Context, id string) (*types.AgentConfig, error) {
		return s.GetAgent(ctx, id)
	})
	adapter.SetTeamLookup(mgr.GetTeamForAgent)

	if err := adapter.ProvisionAgent(context.Background(), types.AgentConfig{ID: "member-2"}); err != nil {
		t.Fatalf("ProvisionAgent: %v", err)
	}
	incoming, _ := adapter.Receive(context.Background())

	if err := bus.Send(context.Background(), types.InternalMessage{
		From:    "member-1",
		To:      "member-2",
		Content: "should be denied",
		Type:    types.MessageTypeMessage,
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case msg := <-incoming:
		t.Fatalf("unexpected message delivered: %+v", msg)
	case <-time.After(500 * time.Millisecond):
	}
}

func TestOpenModeMemberToMemberAllowed(t *testing.T) {
	s, bus, mgr, _ := newTestDeps(t)
	createAgent(t, s, "leader-1", "Leader", types.AgentStatusRunning)
	createAgent(t, s, "member-1", "Member One", types.AgentStatusRunning)
	createAgent(t, s, "member-2", "Member Two", types.AgentStatusRunning)
	createTeam(t, mgr, "team-1", "leader-1", []string{"leader-1", "member-1", "member-2"}, types.TeamCommOpen)

	adapter := busadapter.New(bus)
	defer adapter.Close()
	adapter.SetConfigLookup(func(ctx context.Context, id string) (*types.AgentConfig, error) {
		return s.GetAgent(ctx, id)
	})
	adapter.SetTeamLookup(mgr.GetTeamForAgent)

	if err := adapter.ProvisionAgent(context.Background(), types.AgentConfig{ID: "member-2"}); err != nil {
		t.Fatalf("ProvisionAgent: %v", err)
	}
	incoming, _ := adapter.Receive(context.Background())

	if err := bus.Send(context.Background(), types.InternalMessage{
		From:    "member-1",
		To:      "member-2",
		Content: "should be allowed",
		Type:    types.MessageTypeMessage,
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case msg := <-incoming:
		if msg.Content != "should be allowed" {
			t.Fatalf("content = %q, want allowed message", msg.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for message")
	}
}
