package teams

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/kkjorsvik/kyvik/internal/audit"
	"github.com/kkjorsvik/kyvik/internal/store"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// TeamMemberStatus combines agent config with current operational state.
type TeamMemberStatus struct {
	AgentID    string            `json:"agent_id"`
	AgentName  string            `json:"agent_name"`
	IsLeader   bool              `json:"is_leader"`
	Status     types.AgentStatus `json:"status"`
	QueueDepth int               `json:"queue_depth"`
}

// Manager handles team lifecycle and coordination.
type Manager struct {
	store store.Store
	bus   *Bus
	audit audit.Logger
}

// NewManager creates a team manager.
func NewManager(store store.Store, bus *Bus, auditLogger audit.Logger) *Manager {
	return &Manager{store: store, bus: bus, audit: auditLogger}
}

// CreateTeam creates a new team and assigns team membership to all members.
func (m *Manager) CreateTeam(ctx context.Context, team types.Team) error {
	members := uniqueStrings(team.MemberIDs)
	if !slices.Contains(members, team.LeaderID) {
		members = append(members, team.LeaderID)
	}

	for _, agentID := range members {
		if _, err := m.store.GetAgent(ctx, agentID); err != nil {
			return fmt.Errorf("validate team member %s: %w", agentID, err)
		}
		existing, err := m.store.GetTeamByAgent(ctx, agentID)
		if err == nil && existing != nil {
			return fmt.Errorf("agent %s already belongs to team %s", agentID, existing.ID)
		}
		if err != nil && !errors.Is(err, types.ErrTeamNotFound) {
			return fmt.Errorf("check existing team for %s: %w", agentID, err)
		}
	}

	now := time.Now().UTC()
	if team.CreatedAt.IsZero() {
		team.CreatedAt = now
	}
	team.UpdatedAt = now
	if !team.Active {
		team.Active = true
	}
	team.MemberIDs = members

	if err := m.store.CreateTeam(ctx, team); err != nil {
		return err
	}
	for _, agentID := range members {
		if err := m.setAgentTeamID(ctx, agentID, team.ID); err != nil {
			return fmt.Errorf("assign team %s to %s: %w", team.ID, agentID, err)
		}
	}
	m.logAudit(ctx, team.LeaderID, "team_create", team.ID, "allowed", fmt.Sprintf("members=%d", len(members)))
	return nil
}

// GetTeam returns a team by ID.
func (m *Manager) GetTeam(ctx context.Context, id string) (*types.Team, error) {
	return m.store.GetTeam(ctx, id)
}

// UpdateTeam updates a team and applies membership changes to agent configs.
func (m *Manager) UpdateTeam(ctx context.Context, team types.Team) error {
	current, err := m.store.GetTeam(ctx, team.ID)
	if err != nil {
		return err
	}

	newMembers := uniqueStrings(team.MemberIDs)
	if !slices.Contains(newMembers, team.LeaderID) {
		return fmt.Errorf("%w: leader must be included in team members", types.ErrAgentNotInTeam)
	}

	for _, agentID := range newMembers {
		if _, err := m.store.GetAgent(ctx, agentID); err != nil {
			return fmt.Errorf("validate team member %s: %w", agentID, err)
		}
		existing, err := m.store.GetTeamByAgent(ctx, agentID)
		if err == nil && existing != nil && existing.ID != team.ID {
			return fmt.Errorf("agent %s already belongs to team %s", agentID, existing.ID)
		}
		if err != nil && !errors.Is(err, types.ErrTeamNotFound) {
			return fmt.Errorf("check existing team for %s: %w", agentID, err)
		}
	}

	oldMembers := uniqueStrings(current.MemberIDs)
	if !slices.Contains(oldMembers, current.LeaderID) {
		oldMembers = append(oldMembers, current.LeaderID)
	}
	added := diffStrings(newMembers, oldMembers)
	removed := diffStrings(oldMembers, newMembers)

	team.MemberIDs = newMembers
	team.CreatedAt = current.CreatedAt
	team.UpdatedAt = time.Now().UTC()
	if err := m.store.UpdateTeam(ctx, team); err != nil {
		return err
	}

	for _, agentID := range added {
		if err := m.setAgentTeamID(ctx, agentID, team.ID); err != nil {
			return fmt.Errorf("assign team %s to %s: %w", team.ID, agentID, err)
		}
	}
	for _, agentID := range removed {
		if err := m.setAgentTeamID(ctx, agentID, ""); err != nil {
			return fmt.Errorf("clear team for %s: %w", agentID, err)
		}
	}
	m.logAudit(ctx, team.LeaderID, "team_update", team.ID, "allowed", fmt.Sprintf("added=%d removed=%d", len(added), len(removed)))
	return nil
}

// DeleteTeam removes a team and clears team_id for all members.
func (m *Manager) DeleteTeam(ctx context.Context, id string) error {
	team, err := m.store.GetTeam(ctx, id)
	if err != nil {
		return err
	}
	members := uniqueStrings(team.MemberIDs)
	if !slices.Contains(members, team.LeaderID) {
		members = append(members, team.LeaderID)
	}

	for _, agentID := range members {
		if err := m.setAgentTeamID(ctx, agentID, ""); err != nil {
			return fmt.Errorf("clear team for %s: %w", agentID, err)
		}
	}
	if err := m.store.DeleteTeam(ctx, id); err != nil {
		return err
	}
	m.logAudit(ctx, team.LeaderID, "team_delete", id, "allowed", fmt.Sprintf("members=%d", len(members)))
	return nil
}

// SetTeamActive pauses or resumes team communication without deleting the team.
func (m *Manager) SetTeamActive(ctx context.Context, teamID string, active bool) error {
	team, err := m.store.GetTeam(ctx, teamID)
	if err != nil {
		return err
	}
	if team.Active == active {
		return nil
	}
	team.Active = active
	team.UpdatedAt = time.Now().UTC()
	if err := m.store.UpdateTeam(ctx, *team); err != nil {
		return err
	}
	action := "team_resume"
	if !active {
		action = "team_pause"
	}
	m.logAudit(ctx, team.LeaderID, action, teamID, "allowed", fmt.Sprintf("active=%v", active))
	return nil
}

// ListTeams returns all teams.
func (m *Manager) ListTeams(ctx context.Context) ([]types.Team, error) {
	return m.store.ListTeams(ctx)
}

// GetTeamForAgent returns the team an agent belongs to, or nil if none.
func (m *Manager) GetTeamForAgent(ctx context.Context, agentID string) (*types.Team, error) {
	team, err := m.store.GetTeamByAgent(ctx, agentID)
	if errors.Is(err, types.ErrTeamNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return team, nil
}

// TeamStatus returns member status for all team members.
func (m *Manager) TeamStatus(ctx context.Context, teamID string) ([]TeamMemberStatus, error) {
	team, err := m.store.GetTeam(ctx, teamID)
	if err != nil {
		return nil, err
	}
	members := uniqueStrings(team.MemberIDs)
	if !slices.Contains(members, team.LeaderID) {
		members = append(members, team.LeaderID)
	}

	status := make([]TeamMemberStatus, 0, len(members))
	for _, agentID := range members {
		agent, err := m.store.GetAgent(ctx, agentID)
		if err != nil {
			return nil, fmt.Errorf("get team member %s: %w", agentID, err)
		}
		queueDepth := 0
		if m.bus != nil {
			queueDepth, err = m.bus.QueueDepth(ctx, agentID)
			if err != nil {
				return nil, fmt.Errorf("queue depth for %s: %w", agentID, err)
			}
		}
		memberStatus := TeamMemberStatus{
			AgentID:    agent.ID,
			AgentName:  agent.Name,
			IsLeader:   agent.ID == team.LeaderID,
			Status:     agent.ActualState,
			QueueDepth: queueDepth,
		}
		if memberStatus.Status == "" {
			memberStatus.Status = types.AgentStatusStopped
		}
		status = append(status, memberStatus)
	}
	return status, nil
}

// UpdateSharedContext updates the team's shared context document.
func (m *Manager) UpdateSharedContext(ctx context.Context, teamID, content string) error {
	team, err := m.store.GetTeam(ctx, teamID)
	if err != nil {
		return err
	}
	team.SharedContext = content
	team.UpdatedAt = time.Now().UTC()
	if err := m.store.UpdateTeam(ctx, *team); err != nil {
		return err
	}
	m.logAudit(ctx, team.LeaderID, "team_shared_context_update", teamID, "allowed", "shared context updated")
	return nil
}

// SharedContextForAgent returns the shared context of the agent's team.
func (m *Manager) SharedContextForAgent(ctx context.Context, agentID string) (string, error) {
	team, err := m.GetTeamForAgent(ctx, agentID)
	if err != nil {
		return "", err
	}
	if team == nil {
		return "", nil
	}
	return team.SharedContext, nil
}

func (m *Manager) setAgentTeamID(ctx context.Context, agentID, teamID string) error {
	agent, err := m.store.GetAgent(ctx, agentID)
	if err != nil {
		return err
	}
	agent.TeamID = teamID
	agent.UpdatedAt = time.Now().UTC()
	return m.store.UpdateAgent(ctx, *agent)
}

func (m *Manager) logAudit(ctx context.Context, agentID, action, resource, decision, details string) {
	if m.audit == nil {
		return
	}
	_ = m.audit.Log(ctx, types.AuditEntry{
		AgentID:   agentID,
		EventType: types.EventConfigChange,
		Action:    action,
		Resource:  resource,
		Decision:  decision,
		Details:   details,
		Timestamp: time.Now().UTC(),
	})
}

func uniqueStrings(input []string) []string {
	seen := make(map[string]struct{}, len(input))
	result := make([]string, 0, len(input))
	for _, item := range input {
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		result = append(result, item)
	}
	return result
}

func diffStrings(left, right []string) []string {
	rightSet := make(map[string]struct{}, len(right))
	for _, item := range right {
		rightSet[item] = struct{}{}
	}
	var result []string
	for _, item := range left {
		if _, ok := rightSet[item]; !ok {
			result = append(result, item)
		}
	}
	return result
}
