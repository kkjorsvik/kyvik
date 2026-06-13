package api

import (
	"net/http"
	"time"

	"github.com/kkjorsvik/kyvik/internal/audit"
	"github.com/kkjorsvik/kyvik/internal/memory"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// HandleListMemories handles GET /agents/{id}/memories.
func (a *API) HandleListMemories(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	p := parsePagination(r)

	memStore := a.kyvik.Storage.Memory
	if memStore == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Memory store not configured")
		return
	}

	mems, err := memStore.List(r.Context(), agentID, memory.ListOptions{
		Limit:  p.Limit,
		Offset: p.Offset,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Failed to list memories")
		return
	}
	if mems == nil {
		mems = []memory.Memory{}
	}

	writeJSON(w, http.StatusOK, ListResponse[memory.Memory]{
		Data:    mems,
		HasMore: len(mems) == p.Limit,
	})
}

// HandleListHistory handles GET /agents/{id}/history.
func (a *API) HandleListHistory(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	p := parsePagination(r)
	channel := r.URL.Query().Get("channel")
	if channel == "" {
		channel = "webui"
	}

	histStore := a.kyvik.Storage.History
	if histStore == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "History store not configured")
		return
	}

	entries, err := histStore.Recent(r.Context(), agentID, channel, "", p.Limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Failed to list history")
		return
	}

	writeJSON(w, http.StatusOK, entries)
}

// HandleListTeams handles GET /teams.
func (a *API) HandleListTeams(w http.ResponseWriter, r *http.Request) {
	tm := a.kyvik.TeamManager()
	if tm == nil {
		writeJSON(w, http.StatusOK, []types.Team{})
		return
	}
	teams, err := tm.ListTeams(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Failed to list teams")
		return
	}
	writeJSON(w, http.StatusOK, teams)
}

// HandleGetTeam handles GET /teams/{id}.
func (a *API) HandleGetTeam(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tm := a.kyvik.TeamManager()
	if tm == nil {
		writeError(w, http.StatusNotFound, "not_found", "Team not found")
		return
	}
	team, err := tm.GetTeam(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Team not found")
		return
	}
	writeJSON(w, http.StatusOK, team)
}

// HandleGetSpending handles GET /spending.
func (a *API) HandleGetSpending(w http.ResponseWriter, r *http.Request) {
	spendTracker := a.kyvik.Spending()
	if spendTracker == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Spending tracker not configured")
		return
	}

	agents, err := a.kyvik.ListAgents(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Failed to list agents")
		return
	}

	type agentBudget struct {
		AgentID string `json:"agent_id"`
		AgentName string `json:"agent_name"`
		Budget  any    `json:"budget"`
	}

	budgets := make([]agentBudget, 0, len(agents))
	for _, ag := range agents {
		budget, err := spendTracker.CheckBudget(r.Context(), ag.ID)
		if err != nil {
			continue
		}
		budgets = append(budgets, agentBudget{
			AgentID: ag.ID,
			AgentName: ag.Name,
			Budget:  budget,
		})
	}

	writeJSON(w, http.StatusOK, budgets)
}

// HandleGetAgentSpending handles GET /spending/{agent_id}.
func (a *API) HandleGetAgentSpending(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent_id")
	spendTracker := a.kyvik.Spending()
	if spendTracker == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Spending tracker not configured")
		return
	}

	budget, err := spendTracker.CheckBudget(r.Context(), agentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Failed to check budget")
		return
	}

	writeJSON(w, http.StatusOK, budget)
}

// HandleListAudit handles GET /audit.
func (a *API) HandleListAudit(w http.ResponseWriter, r *http.Request) {
	auditLogger := a.kyvik.Audit()
	if auditLogger == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Audit logger not configured")
		return
	}

	p := parsePagination(r)
	q := r.URL.Query()

	filter := audit.Filter{
		AgentID:   q.Get("agent_id"),
		EventType: types.EventType(q.Get("event_type")),
		Decision:  q.Get("decision"),
		Limit:     p.Limit,
		Offset:    p.Offset,
	}

	if v := q.Get("start_time"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.StartTime = &t
		}
	}
	if v := q.Get("end_time"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.EndTime = &t
		}
	}

	entries, err := auditLogger.Query(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Failed to query audit log")
		return
	}
	if entries == nil {
		entries = []types.AuditEntry{}
	}

	writeJSON(w, http.StatusOK, ListResponse[types.AuditEntry]{
		Data:    entries,
		HasMore: len(entries) == p.Limit,
	})
}

// HandleRunBackup handles POST /backup.
func (a *API) HandleRunBackup(w http.ResponseWriter, r *http.Request) {
	bm := a.kyvik.BackupManager()
	if bm == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Backup manager not configured")
		return
	}

	result := bm.RunNow(r.Context())
	writeJSON(w, http.StatusOK, result)
}

// statusResponse is the response for GET /status.
type statusResponse struct {
	Version     string   `json:"version"`
	Uptime      string   `json:"uptime"`
	AgentCount  int      `json:"agent_count"`
	RunningCount int     `json:"running_count"`
	Providers   []string `json:"providers"`
}

// HandleStatus handles GET /status.
func (a *API) HandleStatus(w http.ResponseWriter, r *http.Request) {
	var agents []types.AgentConfig
	var providers []string

	if a.kyvik != nil {
		agents, _ = a.kyvik.ListAgents(r.Context())
		for _, p := range a.kyvik.ListProviders() {
			providers = append(providers, p.Name)
		}
	}

	running := 0
	for _, ag := range agents {
		if ag.ActualState == types.AgentStatusRunning {
			running++
		}
	}
	if providers == nil {
		providers = []string{}
	}

	writeJSON(w, http.StatusOK, statusResponse{
		Version:      "0.1.0-dev",
		Uptime:       time.Since(a.startAt).Truncate(time.Second).String(),
		AgentCount:   len(agents),
		RunningCount: running,
		Providers:    providers,
	})
}
