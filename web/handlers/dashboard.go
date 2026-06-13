package handlers

import (
	"net/http"
	"sort"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

// AgentCard combines agent config with runtime status for display.
type AgentCard struct {
	types.AgentConfig
	Status           types.AgentStatus
	AssignedNodeName string // cluster node name (empty in single-node mode)
	AssignedNodeID   string // cluster node ID (empty in single-node mode)
}

// Dashboard renders the main dashboard page with agent cards.
func (h *Handlers) Dashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// First-run: redirect to guide agent chat.
	if firstRun, _ := h.kyvik.GetSystemState(ctx, "guide_first_run"); firstRun == "pending" {
		_ = h.kyvik.SetSystemState(ctx, "guide_first_run", "complete")
		http.Redirect(w, r, "/agents/kyvik-guide/chat", http.StatusTemporaryRedirect)
		return
	}

	agents, err := h.kyvik.ListAgents(ctx)
	if err != nil {
		h.serverError(w, r, "listing agents", err)
		return
	}
	agents, err = h.filterAgentsForUser(ctx, agents)
	if err != nil {
		h.serverError(w, r, "applying agent scope", err)
		return
	}

	// Enrich with runtime status
	cards := make([]AgentCard, 0, len(agents))
	var runningCount, stoppedCount, errorCount, quarantinedCount, killedCount int
	for _, a := range agents {
		status, _ := h.kyvik.GetAgentStatus(ctx, a.ID)
		cards = append(cards, AgentCard{AgentConfig: a, Status: status})
		switch status {
		case types.AgentStatusRunning:
			runningCount++
		case types.AgentStatusStopped:
			stoppedCount++
		case types.AgentStatusError:
			errorCount++
		case types.AgentStatusQuarantined:
			quarantinedCount++
		case types.AgentStatusKilled:
			killedCount++
		}
	}

	// Sort guide agent first.
	sort.SliceStable(cards, func(i, j int) bool {
		if cards[i].IsGuide != cards[j].IsGuide {
			return cards[i].IsGuide
		}
		return false
	})

	slackPrimary, slackDedicated := h.kyvik.SlackStatus()

	data := map[string]any{
		"Nav":              "dashboard",
		"Title":            "Dashboard",
		"Agents":           cards,
		"TotalAgents":      len(agents),
		"RunningCount":     runningCount,
		"StoppedCount":     stoppedCount,
		"ErrorCount":       errorCount,
		"QuarantinedCount": quarantinedCount,
		"KilledCount":      killedCount,
		"SlackPrimary":     slackPrimary,
		"SlackDedicated":   slackDedicated,
		"EmergencyStop":    h.kyvik.EmergencyStopActive(),
	}

	if h.kyvik.VacationModeActive() {
		if vs, err := h.kyvik.GetVacationState(ctx); err == nil {
			data["VacationState"] = vs
		}
	}

	// Show guide agent creation banner if providers exist but guide doesn't.
	if h.providerMgr != nil {
		dismissed, _ := h.kyvik.GetSystemState(ctx, "guide_banner_dismissed")
		if dismissed != "true" {
			if providers, err := h.providerMgr.ListProviders(ctx); err == nil && len(providers) > 0 {
				if _, err := h.kyvik.GetAgent(ctx, "kyvik-guide"); err != nil {
					data["ShowGuideBanner"] = true
				}
			}
		}
	}

	if isHTMX(r) {
		h.injectTemplateUser(ctx, data)
		h.renderFragment(w, r, "agent-cards", data)
		return
	}

	h.renderPageWithRequest(w, r, "dashboard", data)
}
