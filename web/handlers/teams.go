package handlers

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/kkjorsvik/kyvik/internal/audit"
	"github.com/kkjorsvik/kyvik/internal/queue"
	"github.com/kkjorsvik/kyvik/internal/spending"
	"github.com/kkjorsvik/kyvik/internal/teams"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

type teamListItem struct {
	Team            types.Team
	LeaderName      string
	MemberCount     int
	AggregateStatus string
}

type teamMemberRow struct {
	ID         string
	Name       string
	IsLeader   bool
	Status     types.AgentStatus
	QueueDepth int
}

type teamMessageRow struct {
	FromID      string
	FromName    string
	ToID        string
	ToName      string
	Content     string
	Preview     string
	Timestamp   time.Time
	Priority    types.MessagePriority
	MessageType types.MessageType
}

type teamToolActivity struct {
	AgentID   string
	AgentName string
	Resource  string
	Decision  string
	Timestamp time.Time
}

type teamSpendingTotals struct {
	DayTokens   int64
	DayCost     float64
	MonthTokens int64
	MonthCost   float64
}

// TeamsList — GET /teams
func (h *Handlers) TeamsList(w http.ResponseWriter, r *http.Request) {
	tm := h.kyvik.TeamManager()
	if tm == nil {
		http.Error(w, "Team manager not configured", http.StatusServiceUnavailable)
		return
	}

	ctx := r.Context()
	all, err := tm.ListTeams(ctx)
	if err != nil {
		h.serverError(w, r, "listing teams", err)
		return
	}

	items := make([]teamListItem, 0, len(all))
	for _, t := range all {
		visibleMembers, verr := h.visibleAgentIDs(ctx, uniqueMemberIDs(t))
		if verr != nil {
			h.serverError(w, r, "resolving team scope", verr)
			return
		}
		if len(visibleMembers) == 0 {
			continue
		}
		leaderName := t.LeaderID
		if ok, _ := h.isAgentVisible(ctx, t.LeaderID); ok {
			if cfg, err := h.kyvik.GetAgent(ctx, t.LeaderID); err == nil {
				leaderName = cfg.Name
			}
		}
		aggregateStatus := "unknown"
		if status, err := tm.TeamStatus(ctx, t.ID); err == nil {
			if len(visibleMembers) > 0 {
				visibleSet := map[string]struct{}{}
				for _, id := range visibleMembers {
					visibleSet[id] = struct{}{}
				}
				filtered := make([]teams.TeamMemberStatus, 0, len(status))
				for _, st := range status {
					if _, ok := visibleSet[st.AgentID]; ok {
						filtered = append(filtered, st)
					}
				}
				status = filtered
			}
			aggregateStatus = summarizeTeamStatus(status)
		}
		items = append(items, teamListItem{
			Team:            t,
			LeaderName:      leaderName,
			MemberCount:     len(visibleMembers),
			AggregateStatus: aggregateStatus,
		})
	}

	data := map[string]any{
		"Nav":   "teams",
		"Title": "Teams",
		"Teams": items,
	}
	if isHTMX(r) {
		h.renderFragment(w, r, "teams-list", data)
		return
	}
	h.renderPageWithRequest(w, r, "teams-list", data)
}

// TeamDetail — GET /teams/{id}
func (h *Handlers) TeamDetail(w http.ResponseWriter, r *http.Request) {
	tm := h.kyvik.TeamManager()
	if tm == nil {
		http.Error(w, "Team manager not configured", http.StatusServiceUnavailable)
		return
	}

	ctx := r.Context()
	id := r.PathValue("id")
	team, err := tm.GetTeam(ctx, id)
	if err != nil {
		http.Error(w, "team not found", http.StatusNotFound)
		return
	}
	visibleMembers, err := h.visibleAgentIDs(ctx, uniqueMemberIDs(*team))
	if err != nil {
		h.serverError(w, r, "resolving team scope", err)
		return
	}
	if len(visibleMembers) == 0 {
		http.Error(w, "team not found", http.StatusNotFound)
		return
	}
	members, aggregateStatus, err := h.teamMembersForDetail(ctx, team, visibleMembers)
	if err != nil {
		h.serverError(w, r, "resolving team members", err)
		return
	}
	messages, _ := h.teamRecentMessages(ctx, *team, visibleMembers, 50)
	activities, _ := h.teamToolActivities(ctx, *team, visibleMembers, 25)
	spend, _ := h.teamSpending(ctx, *team, visibleMembers)

	data := map[string]any{
		"Nav":             "teams",
		"Title":           team.Name,
		"Team":            team,
		"Members":         members,
		"AggregateStatus": aggregateStatus,
		"Messages":        messages,
		"TeamActivities":  activities,
		"TeamSpending":    spend,
	}
	if isHTMX(r) {
		h.renderFragment(w, r, "team-detail", data)
		return
	}
	h.renderPageWithRequest(w, r, "team-detail", data)
}

// TeamCreateForm — GET /teams/new
func (h *Handlers) TeamCreateForm(w http.ResponseWriter, r *http.Request) {
	eligible, err := h.eligibleAgentsForTeam(r.Context(), "")
	if err != nil {
		h.serverError(w, r, "loading agents", err)
		return
	}

	data := map[string]any{
		"Nav":               "teams",
		"Title":             "Create Team",
		"EligibleAgents":    eligible,
		"CommunicationMode": string(types.TeamCommLeaderMediated),
	}
	if isHTMX(r) {
		h.renderFragment(w, r, "team-create", data)
		return
	}
	h.renderPageWithRequest(w, r, "team-create", data)
}

// TeamCreatePost — POST /teams
func (h *Handlers) TeamCreatePost(w http.ResponseWriter, r *http.Request) {
	tm := h.kyvik.TeamManager()
	if tm == nil {
		http.Error(w, "Team manager not configured", http.StatusServiceUnavailable)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	leaderID := strings.TrimSpace(r.FormValue("leader_id"))
	if name == "" || leaderID == "" {
		http.Error(w, "name and leader are required", http.StatusBadRequest)
		return
	}

	members := uniqueStrings(append(r.Form["member_ids"], leaderID))
	if err := h.ensureAgentsVisible(r.Context(), append([]string{leaderID}, members...)...); err != nil {
		http.Error(w, "invalid team members", http.StatusBadRequest)
		return
	}
	mode := parseTeamCommunication(r.FormValue("communication"))
	team := types.Team{
		ID:            uuid.New().String(),
		Name:          name,
		Description:   strings.TrimSpace(r.FormValue("description")),
		LeaderID:      leaderID,
		MemberIDs:     members,
		Communication: mode,
		Active:        true,
		SharedContext: strings.TrimSpace(r.FormValue("shared_context")),
	}

	if err := tm.CreateTeam(r.Context(), team); err != nil {
		http.Error(w, "failed to create team", http.StatusBadRequest)
		return
	}

	redirect := "/teams/" + team.ID
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", redirect)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, redirect, http.StatusSeeOther)
}

// TeamEdit — GET /teams/{id}/edit
func (h *Handlers) TeamEdit(w http.ResponseWriter, r *http.Request) {
	tm := h.kyvik.TeamManager()
	if tm == nil {
		http.Error(w, "Team manager not configured", http.StatusServiceUnavailable)
		return
	}

	ctx := r.Context()
	id := r.PathValue("id")
	team, err := tm.GetTeam(ctx, id)
	if err != nil {
		http.Error(w, "team not found", http.StatusNotFound)
		return
	}
	visibleMembers, err := h.visibleAgentIDs(ctx, uniqueMemberIDs(*team))
	if err != nil {
		h.serverError(w, r, "resolving team scope", err)
		return
	}
	if len(visibleMembers) == 0 {
		http.Error(w, "team not found", http.StatusNotFound)
		return
	}
	eligible, err := h.eligibleAgentsForTeam(ctx, team.ID)
	if err != nil {
		h.serverError(w, r, "loading agents", err)
		return
	}

	memberSet := make(map[string]bool)
	for _, m := range uniqueMemberIDs(*team) {
		memberSet[m] = true
	}

	data := map[string]any{
		"Nav":               "teams",
		"Title":             "Edit Team",
		"Team":              team,
		"EligibleAgents":    eligible,
		"MemberSet":         memberSet,
		"CommunicationMode": string(team.Communication),
	}
	if isHTMX(r) {
		h.renderFragment(w, r, "team-edit", data)
		return
	}
	h.renderPageWithRequest(w, r, "team-edit", data)
}

// TeamEditPost — POST /teams/{id}/edit
func (h *Handlers) TeamEditPost(w http.ResponseWriter, r *http.Request) {
	tm := h.kyvik.TeamManager()
	if tm == nil {
		http.Error(w, "Team manager not configured", http.StatusServiceUnavailable)
		return
	}

	ctx := r.Context()
	id := r.PathValue("id")
	team, err := tm.GetTeam(ctx, id)
	if err != nil {
		http.Error(w, "team not found", http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	if removeMember := strings.TrimSpace(r.FormValue("remove_member")); removeMember != "" {
		if removeMember == team.LeaderID {
			http.Error(w, "cannot remove the team leader", http.StatusBadRequest)
			return
		}
		newMembers := make([]string, 0, len(team.MemberIDs))
		for _, m := range uniqueMemberIDs(*team) {
			if m != removeMember {
				newMembers = append(newMembers, m)
			}
		}
		team.MemberIDs = newMembers
		if err := h.ensureAgentsVisible(ctx, append([]string{team.LeaderID}, team.MemberIDs...)...); err != nil {
			http.Error(w, "invalid team members", http.StatusBadRequest)
			return
		}
		if err := tm.UpdateTeam(ctx, *team); err != nil {
			http.Error(w, "failed to remove member", http.StatusBadRequest)
			return
		}
		redirect := "/teams/" + id
		if isHTMX(r) {
			w.Header().Set("HX-Redirect", redirect)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Redirect(w, r, redirect, http.StatusSeeOther)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	leaderID := strings.TrimSpace(r.FormValue("leader_id"))
	if name == "" || leaderID == "" {
		http.Error(w, "name and leader are required", http.StatusBadRequest)
		return
	}

	members := uniqueStrings(append(r.Form["member_ids"], leaderID))
	if err := h.ensureAgentsVisible(ctx, append([]string{leaderID}, members...)...); err != nil {
		http.Error(w, "invalid team members", http.StatusBadRequest)
		return
	}
	team.Name = name
	team.Description = strings.TrimSpace(r.FormValue("description"))
	team.LeaderID = leaderID
	team.MemberIDs = members
	team.Communication = parseTeamCommunication(r.FormValue("communication"))
	team.Active = r.FormValue("active") == "on"
	team.SharedContext = strings.TrimSpace(r.FormValue("shared_context"))

	if err := tm.UpdateTeam(ctx, *team); err != nil {
		http.Error(w, "failed to update team", http.StatusBadRequest)
		return
	}

	redirect := "/teams/" + id
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", redirect)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, redirect, http.StatusSeeOther)
}

// TeamDelete — POST /teams/{id}/delete
func (h *Handlers) TeamDelete(w http.ResponseWriter, r *http.Request) {
	tm := h.kyvik.TeamManager()
	if tm == nil {
		http.Error(w, "Team manager not configured", http.StatusServiceUnavailable)
		return
	}

	id := r.PathValue("id")
	team, err := tm.GetTeam(r.Context(), id)
	if err != nil {
		http.Error(w, "team not found", http.StatusNotFound)
		return
	}
	visibleMembers, err := h.visibleAgentIDs(r.Context(), uniqueMemberIDs(*team))
	if err != nil {
		h.serverError(w, r, "resolving team scope", err)
		return
	}
	if len(visibleMembers) == 0 {
		http.Error(w, "team not found", http.StatusNotFound)
		return
	}
	if err := tm.DeleteTeam(r.Context(), id); err != nil {
		http.Error(w, "failed to delete team", http.StatusBadRequest)
		return
	}

	redirect := "/teams"
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", redirect)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, redirect, http.StatusSeeOther)
}

// TeamPause pauses team communication without deleting the team.
func (h *Handlers) TeamPause(w http.ResponseWriter, r *http.Request) {
	tm := h.kyvik.TeamManager()
	if tm == nil {
		http.Error(w, "Team manager not configured", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	if err := tm.SetTeamActive(r.Context(), id, false); err != nil {
		http.Error(w, "failed to pause team", http.StatusBadRequest)
		return
	}
	redirect := "/teams/" + id
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", redirect)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, redirect, http.StatusSeeOther)
}

// TeamResume resumes team communication.
func (h *Handlers) TeamResume(w http.ResponseWriter, r *http.Request) {
	tm := h.kyvik.TeamManager()
	if tm == nil {
		http.Error(w, "Team manager not configured", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	if err := tm.SetTeamActive(r.Context(), id, true); err != nil {
		http.Error(w, "failed to resume team", http.StatusBadRequest)
		return
	}
	if q := h.kyvik.Storage.Queue; q != nil {
		if team, err := tm.GetTeam(r.Context(), id); err == nil && team != nil {
			for _, agentID := range uniqueMemberIDs(*team) {
				if err := q.ReplayAgent(r.Context(), agentID); err != nil {
					h.serverError(w, r, "resuming team queue", err)
					return
				}
			}
		}
	}
	redirect := "/teams/" + id
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", redirect)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, redirect, http.StatusSeeOther)
}

// TeamSharedContext — POST /teams/{id}/context
func (h *Handlers) TeamSharedContext(w http.ResponseWriter, r *http.Request) {
	tm := h.kyvik.TeamManager()
	if tm == nil {
		http.Error(w, "Team manager not configured", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	team, err := tm.GetTeam(r.Context(), id)
	if err != nil {
		http.Error(w, "team not found", http.StatusNotFound)
		return
	}
	visibleMembers, err := h.visibleAgentIDs(r.Context(), uniqueMemberIDs(*team))
	if err != nil {
		h.serverError(w, r, "resolving team scope", err)
		return
	}
	if len(visibleMembers) == 0 {
		http.Error(w, "team not found", http.StatusNotFound)
		return
	}
	content := r.FormValue("shared_context")
	if err := tm.UpdateSharedContext(r.Context(), id, content); err != nil {
		http.Error(w, "failed to update shared context", http.StatusBadRequest)
		return
	}
	team, err = tm.GetTeam(r.Context(), id)
	if err != nil {
		http.Error(w, "team not found", http.StatusNotFound)
		return
	}
	h.renderFragment(w, r, "team-shared-context", map[string]any{"Team": team})
}

// TeamMessages — GET /teams/{id}/messages
func (h *Handlers) TeamMessages(w http.ResponseWriter, r *http.Request) {
	tm := h.kyvik.TeamManager()
	if tm == nil {
		http.Error(w, "Team manager not configured", http.StatusServiceUnavailable)
		return
	}
	team, err := tm.GetTeam(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, "team not found", http.StatusNotFound)
		return
	}
	visibleMembers, err := h.visibleAgentIDs(r.Context(), uniqueMemberIDs(*team))
	if err != nil {
		h.serverError(w, r, "resolving team scope", err)
		return
	}
	if len(visibleMembers) == 0 {
		http.Error(w, "team not found", http.StatusNotFound)
		return
	}
	messages, err := h.teamRecentMessages(r.Context(), *team, visibleMembers, 50)
	if err != nil {
		http.Error(w, "failed to load team messages", http.StatusInternalServerError)
		return
	}
	h.renderFragment(w, r, "team-messages", map[string]any{
		"Team":     team,
		"Messages": messages,
	})
}

func (h *Handlers) eligibleAgentsForTeam(ctx context.Context, currentTeamID string) ([]types.AgentConfig, error) {
	agents, err := h.kyvik.ListAgents(ctx)
	if err != nil {
		return nil, err
	}
	agents, err = h.filterAgentsForUser(ctx, agents)
	if err != nil {
		return nil, err
	}
	out := make([]types.AgentConfig, 0, len(agents))
	for _, a := range agents {
		if a.TeamID == "" || a.TeamID == currentTeamID {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

func (h *Handlers) teamMembersForDetail(ctx context.Context, team *types.Team, visibleMembers []string) ([]teamMemberRow, string, error) {
	tm := h.kyvik.TeamManager()
	if tm == nil {
		return nil, "unknown", fmt.Errorf("team manager unavailable")
	}
	statuses, err := tm.TeamStatus(ctx, team.ID)
	if err != nil {
		return nil, "unknown", err
	}
	q := h.kyvik.Storage.Queue
	visibleSet := make(map[string]struct{}, len(visibleMembers))
	for _, id := range visibleMembers {
		visibleSet[id] = struct{}{}
	}
	rows := make([]teamMemberRow, 0, len(statuses))
	filteredStatus := make([]teams.TeamMemberStatus, 0, len(statuses))
	for _, st := range statuses {
		if _, ok := visibleSet[st.AgentID]; !ok {
			continue
		}
		filteredStatus = append(filteredStatus, st)
		pending := st.QueueDepth // fallback to bus count
		if q != nil {
			if counts, err := q.Stats(ctx, st.AgentID); err == nil {
				pending = counts["pending"]
			}
		}
		rows = append(rows, teamMemberRow{
			ID:         st.AgentID,
			Name:       st.AgentName,
			IsLeader:   st.IsLeader,
			Status:     st.Status,
			QueueDepth: pending,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].IsLeader != rows[j].IsLeader {
			return rows[i].IsLeader
		}
		return strings.ToLower(rows[i].Name) < strings.ToLower(rows[j].Name)
	})
	return rows, summarizeTeamStatus(filteredStatus), nil
}

func (h *Handlers) teamRecentMessages(ctx context.Context, team types.Team, visibleMembers []string, limit int) ([]teamMessageRow, error) {
	bus := h.kyvik.InternalBus()
	if bus == nil {
		return nil, nil
	}
	members := visibleMembers
	if len(members) < 2 {
		return nil, nil
	}

	nameByID := make(map[string]string, len(members))
	for _, id := range members {
		nameByID[id] = id
		if cfg, err := h.kyvik.GetAgent(ctx, id); err == nil {
			nameByID[id] = cfg.Name
		}
	}

	seen := map[string]bool{}
	var out []teamMessageRow
	for i := 0; i < len(members); i++ {
		for j := i + 1; j < len(members); j++ {
			pairMsgs, err := bus.MessagesBetween(ctx, members[i], members[j], limit)
			if err != nil {
				continue
			}
			for _, m := range pairMsgs {
				if seen[m.ID] {
					continue
				}
				seen[m.ID] = true
				out = append(out, teamMessageRow{
					FromID:      m.From,
					FromName:    nameByID[m.From],
					ToID:        m.To,
					ToName:      nameByID[m.To],
					Content:     m.Content,
					Preview:     previewText(m.Content, 140),
					Timestamp:   m.Timestamp,
					Priority:    m.Priority,
					MessageType: m.Type,
				})
			}
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Timestamp.After(out[j].Timestamp) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (h *Handlers) teamToolActivities(ctx context.Context, team types.Team, visibleMembers []string, limit int) ([]teamToolActivity, error) {
	al := h.kyvik.Audit()
	if al == nil {
		return nil, nil
	}
	members := visibleMembers
	activities := make([]teamToolActivity, 0, limit)
	for _, id := range members {
		entries, err := al.Query(ctx, audit.Filter{AgentID: id, EventType: types.EventToolCall, Limit: 30})
		if err != nil {
			continue
		}
		agentName := id
		if cfg, err := h.kyvik.GetAgent(ctx, id); err == nil {
			agentName = cfg.Name
		}
		for _, e := range entries {
			if !strings.HasPrefix(e.Resource, "team.") {
				continue
			}
			activities = append(activities, teamToolActivity{
				AgentID:   id,
				AgentName: agentName,
				Resource:  e.Resource,
				Decision:  e.Decision,
				Timestamp: e.Timestamp,
			})
		}
	}
	sort.Slice(activities, func(i, j int) bool { return activities[i].Timestamp.After(activities[j].Timestamp) })
	if len(activities) > limit {
		activities = activities[:limit]
	}
	return activities, nil
}

func (h *Handlers) teamSpending(ctx context.Context, team types.Team, visibleMembers []string) (*teamSpendingTotals, error) {
	sp := h.kyvik.Spending()
	if sp == nil {
		return nil, nil
	}
	totals := &teamSpendingTotals{}
	for _, id := range visibleMembers {
		if day, err := sp.GetSummary(ctx, spending.Filter{AgentID: id, Period: "day"}); err == nil && day != nil {
			totals.DayTokens += day.TotalTokens
			totals.DayCost += day.TotalCost
		}
		if month, err := sp.GetSummary(ctx, spending.Filter{AgentID: id, Period: "month"}); err == nil && month != nil {
			totals.MonthTokens += month.TotalTokens
			totals.MonthCost += month.TotalCost
		}
	}
	return totals, nil
}

func parseTeamCommunication(value string) types.TeamCommunication {
	if types.TeamCommunication(value) == types.TeamCommOpen {
		return types.TeamCommOpen
	}
	return types.TeamCommLeaderMediated
}

func summarizeTeamStatus(statuses []teams.TeamMemberStatus) string {
	if len(statuses) == 0 {
		return "unknown"
	}
	running := 0
	for _, st := range statuses {
		if st.Status == types.AgentStatusRunning {
			running++
		}
	}
	if running == len(statuses) {
		return "all running"
	}
	if running == 0 {
		return "all stopped"
	}
	return "some stopped"
}

func uniqueMemberIDs(team types.Team) []string {
	seen := make(map[string]bool, len(team.MemberIDs)+1)
	out := make([]string, 0, len(team.MemberIDs)+1)
	for _, id := range append(append([]string{}, team.MemberIDs...), team.LeaderID) {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func uniqueStrings(items []string) []string {
	seen := make(map[string]bool, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
}

func previewText(s string, max int) string {
	clean := strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(clean) <= max {
		return clean
	}
	return clean[:max] + "..."
}

// Queue visibility types.

type queueMessageRow struct {
	ID          int64
	DisplayID   string
	AgentID     string
	AgentName   string
	Channel     string
	Sender      string
	SenderName  string
	Content     string
	Preview     string
	MessageType string
	Status      string
	Attempts    int
	MaxAttempts int
	CreatedAt   time.Time
	StartedAt   *time.Time
	CompletedAt *time.Time
	WaitTime    string
	ShowActions bool
}

type queueStats struct {
	Pending    int
	Processing int
	Completed  int
	Failed     int
	Total      int
}

type teamQueueMemberRow struct {
	AgentID   string
	AgentName string
	IsLeader  bool
	Stats     queueStats
}

// TeamQueues — GET /teams/{id}/queues — renders queue overview for all team members.
func (h *Handlers) TeamQueues(w http.ResponseWriter, r *http.Request) {
	tm := h.kyvik.TeamManager()
	if tm == nil {
		http.Error(w, "Team manager not configured", http.StatusServiceUnavailable)
		return
	}
	q := h.kyvik.Storage.Queue
	if q == nil {
		http.Error(w, "Queue not configured", http.StatusServiceUnavailable)
		return
	}

	ctx := r.Context()
	id := r.PathValue("id")
	team, err := tm.GetTeam(ctx, id)
	if err != nil {
		http.Error(w, "team not found", http.StatusNotFound)
		return
	}
	members, err := h.visibleAgentIDs(ctx, uniqueMemberIDs(*team))
	if err != nil {
		h.serverError(w, r, "resolving team scope", err)
		return
	}
	if len(members) == 0 {
		http.Error(w, "team not found", http.StatusNotFound)
		return
	}

	rows := make([]teamQueueMemberRow, 0, len(members))
	for _, agentID := range members {
		agentName := agentID
		if cfg, err := h.kyvik.GetAgent(ctx, agentID); err == nil {
			agentName = cfg.Name
		}
		stats := queueStats{}
		if messages, err := q.ListMessages(ctx, agentID, "", 10000); err == nil {
			filtered := filterQueueByChannel(h.toQueueRows(ctx, agentName, messages), "internal")
			stats = computeQueueStats(filtered)
		}
		rows = append(rows, teamQueueMemberRow{
			AgentID:   agentID,
			AgentName: agentName,
			IsLeader:  agentID == team.LeaderID,
			Stats:     stats,
		})
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].IsLeader != rows[j].IsLeader {
			return rows[i].IsLeader
		}
		return strings.ToLower(rows[i].AgentName) < strings.ToLower(rows[j].AgentName)
	})

	h.renderFragment(w, r, "team-queues", map[string]any{
		"Team":    team,
		"Members": rows,
	})
}

// TeamAgentQueue — GET /teams/{id}/queues/{agentID} — renders per-agent queue list.
func (h *Handlers) TeamAgentQueue(w http.ResponseWriter, r *http.Request) {
	tm := h.kyvik.TeamManager()
	if tm == nil {
		http.Error(w, "Team manager not configured", http.StatusServiceUnavailable)
		return
	}
	q := h.kyvik.Storage.Queue
	if q == nil {
		http.Error(w, "Queue not configured", http.StatusServiceUnavailable)
		return
	}

	ctx := r.Context()
	id := r.PathValue("id")
	agentID := r.PathValue("agentID")
	statusFilter := r.URL.Query().Get("status")

	team, err := tm.GetTeam(ctx, id)
	if err != nil {
		http.Error(w, "team not found", http.StatusNotFound)
		return
	}
	visibleMembers, err := h.visibleAgentIDs(ctx, uniqueMemberIDs(*team))
	if err != nil {
		h.serverError(w, r, "resolving team scope", err)
		return
	}
	if len(visibleMembers) == 0 {
		http.Error(w, "team not found", http.StatusNotFound)
		return
	}
	memberSet := make(map[string]struct{}, len(visibleMembers))
	for _, id := range visibleMembers {
		memberSet[id] = struct{}{}
	}
	if _, ok := memberSet[agentID]; !ok {
		http.Error(w, "agent not found in team", http.StatusNotFound)
		return
	}

	agentName := agentID
	if cfg, err := h.kyvik.GetAgent(ctx, agentID); err == nil {
		agentName = cfg.Name
	}

	// Fetch all messages and filter to internal channel only for team view.
	allMessages, err := q.ListMessages(ctx, agentID, "", 10000)
	if err != nil {
		h.serverError(w, r, "listing queue messages", err)
		return
	}

	allRows := filterQueueByChannel(h.toQueueRows(ctx, agentName, allMessages), "internal")

	// Apply status filter after channel filtering.
	rows := allRows
	if statusFilter != "" {
		rows = make([]queueMessageRow, 0, len(allRows))
		for _, r := range allRows {
			if r.Status == statusFilter {
				rows = append(rows, r)
			}
		}
	}
	if len(rows) > 100 {
		rows = rows[:100]
	}

	stats := computeQueueStats(allRows)

	data := map[string]any{
		"Nav":          "teams",
		"Title":        agentName + " Queue",
		"Team":         team,
		"AgentID":      agentID,
		"AgentName":    agentName,
		"Messages":     rows,
		"Stats":        stats,
		"StatusFilter": statusFilter,
	}
	if isHTMX(r) {
		h.renderFragment(w, r, "team-agent-queue", data)
		return
	}
	h.renderPageWithRequest(w, r, "team-agent-queue", data)
}

// TeamQueueRetry — POST /teams/{id}/queues/{msgID}/retry — retry a failed message.
func (h *Handlers) TeamQueueRetry(w http.ResponseWriter, r *http.Request) {
	q := h.kyvik.Storage.Queue
	if q == nil {
		http.Error(w, "Queue not configured", http.StatusServiceUnavailable)
		return
	}
	if err := h.ensureTeamQueueAccess(r.Context(), r.PathValue("id")); err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	msgID, err := strconv.ParseInt(r.PathValue("msgID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid message ID", http.StatusBadRequest)
		return
	}
	if err := q.RetryMessage(r.Context(), msgID); err != nil {
		http.Error(w, "retry failed", http.StatusBadRequest)
		return
	}
	w.Header().Set("HX-Refresh", "true")
	w.WriteHeader(http.StatusOK)
}

// TeamQueueDelete — POST /teams/{id}/queues/{msgID}/delete — delete a message.
func (h *Handlers) TeamQueueDelete(w http.ResponseWriter, r *http.Request) {
	q := h.kyvik.Storage.Queue
	if q == nil {
		http.Error(w, "Queue not configured", http.StatusServiceUnavailable)
		return
	}
	if err := h.ensureTeamQueueAccess(r.Context(), r.PathValue("id")); err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	msgID, err := strconv.ParseInt(r.PathValue("msgID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid message ID", http.StatusBadRequest)
		return
	}
	if err := q.DeleteMessage(r.Context(), msgID); err != nil {
		http.Error(w, "delete failed", http.StatusBadRequest)
		return
	}
	w.Header().Set("HX-Refresh", "true")
	w.WriteHeader(http.StatusOK)
}

// TeamQueueMessageDetail — GET /teams/{id}/queues/message/{msgID} — full message content.
func (h *Handlers) TeamQueueMessageDetail(w http.ResponseWriter, r *http.Request) {
	q := h.kyvik.Storage.Queue
	if q == nil {
		http.Error(w, "Queue not configured", http.StatusServiceUnavailable)
		return
	}
	msgID, err := strconv.ParseInt(r.PathValue("msgID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid message ID", http.StatusBadRequest)
		return
	}
	teamID := r.PathValue("id")
	if err := h.ensureTeamQueueAccess(r.Context(), teamID); err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	// Use ListMessages with no filter; find the specific message.
	// For simplicity, query all messages and filter.
	messages, err := q.ListMessages(r.Context(), "", "", 10000)
	if err != nil {
		http.Error(w, "failed to query messages", http.StatusInternalServerError)
		return
	}
	var found *queueMessageRow
	for _, msg := range messages {
		if msg.ID == msgID {
			senderName := msg.Sender
			if msg.Sender != "" {
				if cfg, err := h.kyvik.GetAgent(r.Context(), msg.Sender); err == nil {
					senderName = cfg.Name
				}
			}
			found = &queueMessageRow{
				ID:          msg.ID,
				DisplayID:   strconv.FormatInt(msg.ID, 10),
				AgentID:     msg.AgentID,
				Channel:     msg.Channel,
				Sender:      msg.Sender,
				SenderName:  senderName,
				Content:     msg.Content,
				MessageType: msg.MessageType,
				Status:      msg.Status,
				Attempts:    msg.Attempts,
				MaxAttempts: msg.MaxAttempts,
				CreatedAt:   msg.CreatedAt,
				StartedAt:   msg.StartedAt,
				CompletedAt: msg.CompletedAt,
				ShowActions: true,
			}
			break
		}
	}
	if found == nil {
		http.Error(w, "message not found", http.StatusNotFound)
		return
	}

	h.renderFragment(w, r, "team-queue-message-detail", map[string]any{
		"Message": found,
		"TeamID":  teamID,
	})
}

func humanDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}

// toQueueRows converts raw queue messages into display rows, resolving sender names.
func (h *Handlers) toQueueRows(ctx context.Context, agentName string, messages []queue.QueueMessage) []queueMessageRow {
	rows := make([]queueMessageRow, 0, len(messages))
	for _, msg := range messages {
		senderName := msg.Sender
		if msg.Sender != "" {
			if cfg, err := h.kyvik.GetAgent(ctx, msg.Sender); err == nil {
				senderName = cfg.Name
			}
		}
		waitTime := ""
		if msg.Status == "pending" {
			waitTime = humanDuration(time.Since(msg.CreatedAt))
		} else if msg.StartedAt != nil && msg.Status == "processing" {
			waitTime = humanDuration(time.Since(*msg.StartedAt))
		}
		rows = append(rows, queueMessageRow{
			ID:          msg.ID,
			DisplayID:   strconv.FormatInt(msg.ID, 10),
			AgentID:     msg.AgentID,
			AgentName:   agentName,
			Channel:     msg.Channel,
			Sender:      msg.Sender,
			SenderName:  senderName,
			Content:     msg.Content,
			Preview:     previewText(msg.Content, 120),
			MessageType: msg.MessageType,
			Status:      msg.Status,
			Attempts:    msg.Attempts,
			MaxAttempts: msg.MaxAttempts,
			CreatedAt:   msg.CreatedAt,
			StartedAt:   msg.StartedAt,
			CompletedAt: msg.CompletedAt,
			WaitTime:    waitTime,
			ShowActions: true,
		})
	}
	return rows
}

// filterQueueByChannel returns only messages matching the given channel.
func filterQueueByChannel(messages []queueMessageRow, channel string) []queueMessageRow {
	out := make([]queueMessageRow, 0, len(messages))
	for _, m := range messages {
		if m.Channel == channel {
			out = append(out, m)
		}
	}
	return out
}

// computeQueueStats computes status counts from a slice of queue messages.
func computeQueueStats(messages []queueMessageRow) queueStats {
	var s queueStats
	for _, m := range messages {
		switch m.Status {
		case "pending":
			s.Pending++
		case "processing":
			s.Processing++
		case "completed":
			s.Completed++
		case "failed":
			s.Failed++
		}
	}
	s.Total = s.Pending + s.Processing + s.Completed + s.Failed
	return s
}

func (h *Handlers) ensureTeamQueueAccess(ctx context.Context, teamID string) error {
	tm := h.kyvik.TeamManager()
	if tm == nil {
		return fmt.Errorf("team manager not configured")
	}
	team, err := tm.GetTeam(ctx, teamID)
	if err != nil {
		return fmt.Errorf("team not found")
	}
	visibleMembers, err := h.visibleAgentIDs(ctx, uniqueMemberIDs(*team))
	if err != nil {
		return err
	}
	if len(visibleMembers) == 0 {
		return fmt.Errorf("team not found")
	}
	return nil
}
