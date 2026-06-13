package teams

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/store/postgres"
	"github.com/kkjorsvik/kyvik/internal/testutil"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

func newTeamTestStore(t *testing.T) *postgres.PostgresStore {
	t.Helper()
	tdb := testutil.RequirePostgres(t)
	return tdb.Store
}

func createTestAgent(t *testing.T, s *postgres.PostgresStore, id, name string, status types.AgentStatus) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	if err := s.CreateAgent(context.Background(), types.AgentConfig{
		ID:          id,
		Name:        name,
		ModelConfig: types.ModelConfig{Provider: "openrouter", Model: "test-model"},
		Template:    "worker",
		ActualState: status,
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("create agent %s: %v", id, err)
	}
}

func TestManagerCreateTeamSetsTeamIDOnMembers(t *testing.T) {
	s := newTeamTestStore(t)
	createTestAgent(t, s, "leader-1", "Leader", types.AgentStatusRunning)
	createTestAgent(t, s, "member-1", "Member One", types.AgentStatusStopped)
	createTestAgent(t, s, "member-2", "Member Two", types.AgentStatusStopped)

	mgr := NewManager(s, NewBus(s.DB(), mockAuditLogger{}), mockAuditLogger{})
	team := types.Team{
		ID:            "team-1",
		Name:          "Ops",
		LeaderID:      "leader-1",
		MemberIDs:     []string{"leader-1", "member-1", "member-2"},
		Communication: types.TeamCommLeaderMediated,
	}

	if err := mgr.CreateTeam(context.Background(), team); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}

	gotTeam, err := s.GetTeam(context.Background(), "team-1")
	if err != nil {
		t.Fatalf("GetTeam: %v", err)
	}
	if len(gotTeam.MemberIDs) != 3 {
		t.Fatalf("member count = %d, want 3", len(gotTeam.MemberIDs))
	}

	for _, id := range []string{"leader-1", "member-1", "member-2"} {
		agent, err := s.GetAgent(context.Background(), id)
		if err != nil {
			t.Fatalf("GetAgent(%s): %v", id, err)
		}
		if agent.TeamID != "team-1" {
			t.Errorf("agent %s team_id = %q, want team-1", id, agent.TeamID)
		}
	}
}

func TestManagerCreateTeamWithNonexistentLeader(t *testing.T) {
	s := newTeamTestStore(t)
	createTestAgent(t, s, "member-1", "Member One", types.AgentStatusStopped)

	mgr := NewManager(s, NewBus(s.DB(), mockAuditLogger{}), mockAuditLogger{})
	err := mgr.CreateTeam(context.Background(), types.Team{
		ID:            "team-1",
		Name:          "Ops",
		LeaderID:      "missing-leader",
		MemberIDs:     []string{"member-1"},
		Communication: types.TeamCommLeaderMediated,
	})
	if err == nil {
		t.Fatal("expected error for nonexistent leader")
	}
}

func TestManagerCreateTeamWithAgentAlreadyInAnotherTeam(t *testing.T) {
	s := newTeamTestStore(t)
	createTestAgent(t, s, "leader-1", "Leader One", types.AgentStatusRunning)
	createTestAgent(t, s, "leader-2", "Leader Two", types.AgentStatusRunning)
	createTestAgent(t, s, "member-1", "Member One", types.AgentStatusStopped)

	mgr := NewManager(s, NewBus(s.DB(), mockAuditLogger{}), mockAuditLogger{})
	if err := mgr.CreateTeam(context.Background(), types.Team{
		ID:            "team-1",
		Name:          "Alpha",
		LeaderID:      "leader-1",
		MemberIDs:     []string{"leader-1", "member-1"},
		Communication: types.TeamCommLeaderMediated,
	}); err != nil {
		t.Fatalf("CreateTeam first: %v", err)
	}

	err := mgr.CreateTeam(context.Background(), types.Team{
		ID:            "team-2",
		Name:          "Beta",
		LeaderID:      "leader-2",
		MemberIDs:     []string{"leader-2", "member-1"},
		Communication: types.TeamCommLeaderMediated,
	})
	if err == nil {
		t.Fatal("expected error for member already in another team")
	}
}

func TestManagerUpdateTeamAddMemberSetsTeamID(t *testing.T) {
	s := newTeamTestStore(t)
	createTestAgent(t, s, "leader-1", "Leader", types.AgentStatusRunning)
	createTestAgent(t, s, "member-1", "Member One", types.AgentStatusStopped)
	createTestAgent(t, s, "member-2", "Member Two", types.AgentStatusStopped)

	mgr := NewManager(s, NewBus(s.DB(), mockAuditLogger{}), mockAuditLogger{})
	if err := mgr.CreateTeam(context.Background(), types.Team{
		ID:            "team-1",
		Name:          "Ops",
		LeaderID:      "leader-1",
		MemberIDs:     []string{"leader-1", "member-1"},
		Communication: types.TeamCommLeaderMediated,
	}); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}

	if err := mgr.UpdateTeam(context.Background(), types.Team{
		ID:            "team-1",
		Name:          "Ops",
		LeaderID:      "leader-1",
		MemberIDs:     []string{"leader-1", "member-1", "member-2"},
		Communication: types.TeamCommLeaderMediated,
	}); err != nil {
		t.Fatalf("UpdateTeam: %v", err)
	}

	agent, err := s.GetAgent(context.Background(), "member-2")
	if err != nil {
		t.Fatalf("GetAgent(member-2): %v", err)
	}
	if agent.TeamID != "team-1" {
		t.Errorf("member-2 team_id = %q, want team-1", agent.TeamID)
	}
}

func TestManagerUpdateTeamRemoveMemberClearsTeamID(t *testing.T) {
	s := newTeamTestStore(t)
	createTestAgent(t, s, "leader-1", "Leader", types.AgentStatusRunning)
	createTestAgent(t, s, "member-1", "Member One", types.AgentStatusStopped)
	createTestAgent(t, s, "member-2", "Member Two", types.AgentStatusStopped)

	mgr := NewManager(s, NewBus(s.DB(), mockAuditLogger{}), mockAuditLogger{})
	if err := mgr.CreateTeam(context.Background(), types.Team{
		ID:            "team-1",
		Name:          "Ops",
		LeaderID:      "leader-1",
		MemberIDs:     []string{"leader-1", "member-1", "member-2"},
		Communication: types.TeamCommLeaderMediated,
	}); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}

	if err := mgr.UpdateTeam(context.Background(), types.Team{
		ID:            "team-1",
		Name:          "Ops",
		LeaderID:      "leader-1",
		MemberIDs:     []string{"leader-1", "member-1"},
		Communication: types.TeamCommLeaderMediated,
	}); err != nil {
		t.Fatalf("UpdateTeam: %v", err)
	}

	agent, err := s.GetAgent(context.Background(), "member-2")
	if err != nil {
		t.Fatalf("GetAgent(member-2): %v", err)
	}
	if agent.TeamID != "" {
		t.Errorf("member-2 team_id = %q, want empty", agent.TeamID)
	}
}

func TestManagerDeleteTeamClearsMembers(t *testing.T) {
	s := newTeamTestStore(t)
	createTestAgent(t, s, "leader-1", "Leader", types.AgentStatusRunning)
	createTestAgent(t, s, "member-1", "Member One", types.AgentStatusStopped)

	mgr := NewManager(s, NewBus(s.DB(), mockAuditLogger{}), mockAuditLogger{})
	if err := mgr.CreateTeam(context.Background(), types.Team{
		ID:            "team-1",
		Name:          "Ops",
		LeaderID:      "leader-1",
		MemberIDs:     []string{"leader-1", "member-1"},
		Communication: types.TeamCommLeaderMediated,
	}); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}

	if err := mgr.DeleteTeam(context.Background(), "team-1"); err != nil {
		t.Fatalf("DeleteTeam: %v", err)
	}

	_, err := s.GetTeam(context.Background(), "team-1")
	if !errors.Is(err, types.ErrTeamNotFound) {
		t.Fatalf("GetTeam after delete error = %v, want ErrTeamNotFound", err)
	}

	for _, id := range []string{"leader-1", "member-1"} {
		agent, err := s.GetAgent(context.Background(), id)
		if err != nil {
			t.Fatalf("GetAgent(%s): %v", id, err)
		}
		if agent.TeamID != "" {
			t.Errorf("agent %s team_id = %q, want empty", id, agent.TeamID)
		}
	}
}

func TestManagerGetTeamForAgent(t *testing.T) {
	s := newTeamTestStore(t)
	createTestAgent(t, s, "leader-1", "Leader", types.AgentStatusRunning)
	createTestAgent(t, s, "member-1", "Member One", types.AgentStatusStopped)

	mgr := NewManager(s, NewBus(s.DB(), mockAuditLogger{}), mockAuditLogger{})
	if err := mgr.CreateTeam(context.Background(), types.Team{
		ID:            "team-1",
		Name:          "Ops",
		LeaderID:      "leader-1",
		MemberIDs:     []string{"leader-1", "member-1"},
		Communication: types.TeamCommLeaderMediated,
	}); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}

	team, err := mgr.GetTeamForAgent(context.Background(), "member-1")
	if err != nil {
		t.Fatalf("GetTeamForAgent: %v", err)
	}
	if team == nil || team.ID != "team-1" {
		t.Fatalf("team = %+v, want team-1", team)
	}
}

func TestManagerGetTeamForAgentUnaffiliated(t *testing.T) {
	s := newTeamTestStore(t)
	createTestAgent(t, s, "solo", "Solo", types.AgentStatusStopped)

	mgr := NewManager(s, NewBus(s.DB(), mockAuditLogger{}), mockAuditLogger{})
	team, err := mgr.GetTeamForAgent(context.Background(), "solo")
	if err != nil {
		t.Fatalf("GetTeamForAgent: %v", err)
	}
	if team != nil {
		t.Fatalf("team = %+v, want nil", team)
	}
}

func TestManagerTeamStatus(t *testing.T) {
	s := newTeamTestStore(t)
	bus := NewBus(s.DB(), mockAuditLogger{})
	createTestAgent(t, s, "leader-1", "Leader", types.AgentStatusRunning)
	createTestAgent(t, s, "member-1", "Member One", types.AgentStatusStopped)

	mgr := NewManager(s, bus, mockAuditLogger{})
	if err := mgr.CreateTeam(context.Background(), types.Team{
		ID:            "team-1",
		Name:          "Ops",
		LeaderID:      "leader-1",
		MemberIDs:     []string{"leader-1", "member-1"},
		Communication: types.TeamCommLeaderMediated,
	}); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}

	_ = bus.Send(context.Background(), types.InternalMessage{From: "leader-1", To: "member-1", Content: "one"})
	_ = bus.Send(context.Background(), types.InternalMessage{From: "leader-1", To: "member-1", Content: "two"})
	_ = bus.Send(context.Background(), types.InternalMessage{From: "member-1", To: "leader-1", Content: "ack"})

	statuses, err := mgr.TeamStatus(context.Background(), "team-1")
	if err != nil {
		t.Fatalf("TeamStatus: %v", err)
	}
	if len(statuses) != 2 {
		t.Fatalf("status count = %d, want 2", len(statuses))
	}

	depthByAgent := map[string]int{}
	for _, st := range statuses {
		depthByAgent[st.AgentID] = st.QueueDepth
	}
	if depthByAgent["member-1"] != 2 {
		t.Errorf("member-1 queue depth = %d, want 2", depthByAgent["member-1"])
	}
	if depthByAgent["leader-1"] != 1 {
		t.Errorf("leader-1 queue depth = %d, want 1", depthByAgent["leader-1"])
	}
}

func TestManagerUpdateSharedContext(t *testing.T) {
	s := newTeamTestStore(t)
	createTestAgent(t, s, "leader-1", "Leader", types.AgentStatusRunning)
	createTestAgent(t, s, "member-1", "Member One", types.AgentStatusStopped)

	mgr := NewManager(s, NewBus(s.DB(), mockAuditLogger{}), mockAuditLogger{})
	if err := mgr.CreateTeam(context.Background(), types.Team{
		ID:            "team-1",
		Name:          "Ops",
		LeaderID:      "leader-1",
		MemberIDs:     []string{"leader-1", "member-1"},
		Communication: types.TeamCommLeaderMediated,
	}); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}

	want := "Shared runbook: escalate P1 in 5 minutes."
	if err := mgr.UpdateSharedContext(context.Background(), "team-1", want); err != nil {
		t.Fatalf("UpdateSharedContext: %v", err)
	}

	team, err := mgr.GetTeam(context.Background(), "team-1")
	if err != nil {
		t.Fatalf("GetTeam: %v", err)
	}
	if team.SharedContext != want {
		t.Errorf("shared_context = %q, want %q", team.SharedContext, want)
	}
}
