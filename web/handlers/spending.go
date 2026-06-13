package handlers

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/kkjorsvik/kyvik/internal/audit"
	"github.com/kkjorsvik/kyvik/internal/auth"
	"github.com/kkjorsvik/kyvik/internal/spending"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// spendingAgentRow holds per-agent spending data for the dashboard table.
type spendingAgentRow struct {
	AgentID    string
	AgentName  string
	Budget     *spending.BudgetStatus
	AlertLevel string // "green", "yellow", "red"
	CanAdjust  bool   // whether the current user can edit limits
}

// spendingChartResponse is the JSON payload for the chart data endpoint.
type spendingChartResponse struct {
	Daily     []spending.DailyUsage        `json:"daily"`
	Agents    []agentCostEntry             `json:"agents"`
	Providers []spending.ProviderUsageSummary `json:"providers"`
}

type agentCostEntry struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Cost   float64 `json:"cost"`
	Tokens int64   `json:"tokens"`
}

// parseRange converts a range query param into a day count and period string.
// Returns (days, period) where period is used for GetSummary calls.
func parseRange(r *http.Request) (int, string) {
	switch r.URL.Query().Get("range") {
	case "today":
		return 1, "day"
	case "30d":
		return 30, "month"
	case "month":
		return 30, "month"
	default: // "7d" is the default
		return 7, "day"
	}
}

// SpendingPage renders the main spending dashboard.
func (h *Handlers) SpendingPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	sp := h.kyvik.Spending()
	if sp == nil {
		http.Error(w, "spending tracker not configured", http.StatusServiceUnavailable)
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

	days, _ := parseRange(r)

	// Determine if the current user can adjust spending limits.
	canAdjust := false
	if u, ok := currentDashboardUser(ctx); ok {
		role := u.Role
		if u.IsAdmin {
			role = auth.RoleAdmin
		}
		canAdjust = auth.Can(role, auth.PermSpendingAdjust)
	}

	// Build per-agent rows with real budget data.
	var rows []spendingAgentRow
	var totalCostDay, totalCostMonth float64
	var totalTokensDay int64
	activeCount := 0

	for _, a := range agents {
		budget, berr := sp.CheckBudget(ctx, a.ID)
		if berr != nil {
			budget = &spending.BudgetStatus{AgentID: a.ID, WithinBudget: true}
		}

		alert := budgetAlertLevel(budget)
		rows = append(rows, spendingAgentRow{
			AgentID:    a.ID,
			AgentName:  a.Name,
			Budget:     budget,
			AlertLevel: alert,
			CanAdjust:  canAdjust,
		})

		totalCostDay += budget.DailyUsed
		totalCostMonth += budget.MonthlyUsed
		totalTokensDay += budget.TokensToday

		status, _ := h.kyvik.GetAgentStatus(ctx, a.ID)
		if status == types.AgentStatusRunning {
			activeCount++
		}
	}

	rangeParam := r.URL.Query().Get("range")
	if rangeParam == "" {
		rangeParam = "7d"
	}

	// Aggregate provider breakdown across all visible agents for the selected period.
	_, period := parseRange(r)
	var providers []spending.ProviderUsageSummary
	providerMap := make(map[string]*spending.ProviderUsageSummary)
	for _, a := range agents {
		agentProviders, perr := sp.GetProviderBreakdown(ctx, a.ID, period)
		if perr != nil {
			continue
		}
		for _, p := range agentProviders {
			if existing, ok := providerMap[p.Provider]; ok {
				existing.TotalTokens += p.TotalTokens
				existing.TotalCost += p.TotalCost
				existing.RequestCount += p.RequestCount
			} else {
				entry := p
				providerMap[p.Provider] = &entry
			}
		}
	}
	for _, p := range providerMap {
		providers = append(providers, *p)
	}

	data := map[string]any{
		"Nav":            "spending",
		"Title":          "Spending Dashboard",
		"Agents":         rows,
		"Providers":      providers,
		"Range":          rangeParam,
		"Days":           days,
		"TotalCostDay":   totalCostDay,
		"TotalCostMonth": totalCostMonth,
		"TotalTokensDay": totalTokensDay,
		"ActiveAgents":   activeCount,
	}

	h.renderPageWithRequest(w, r, "spending-index", data)
}

// SpendingChartData returns JSON chart data for the spending dashboard.
func (h *Handlers) SpendingChartData(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	sp := h.kyvik.Spending()
	if sp == nil {
		http.Error(w, "spending tracker not configured", http.StatusServiceUnavailable)
		return
	}

	days, period := parseRange(r)

	// Global daily time series from actual database records.
	daily, err := sp.GetDailyTimeSeries(ctx, "", days)
	if err != nil {
		h.serverError(w, r, "getting daily time series", err)
		return
	}
	if daily == nil {
		daily = []spending.DailyUsage{}
	}

	// Per-agent cost breakdown from actual usage records.
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

	var agentEntries []agentCostEntry
	providerMap := make(map[string]*spending.ProviderUsageSummary)

	for _, a := range agents {
		summary, serr := sp.GetSummary(ctx, spending.Filter{AgentID: a.ID, Period: period})
		if serr != nil || summary == nil {
			continue
		}
		if summary.TotalCost > 0 || summary.TotalTokens > 0 {
			agentEntries = append(agentEntries, agentCostEntry{
				ID:     a.ID,
				Name:   a.Name,
				Cost:   summary.TotalCost,
				Tokens: summary.TotalTokens,
			})
		}

		// Aggregate provider data from actual records.
		agentProviders, perr := sp.GetProviderBreakdown(ctx, a.ID, period)
		if perr != nil {
			continue
		}
		for _, p := range agentProviders {
			if existing, ok := providerMap[p.Provider]; ok {
				existing.TotalTokens += p.TotalTokens
				existing.TotalCost += p.TotalCost
				existing.RequestCount += p.RequestCount
			} else {
				entry := p
				providerMap[p.Provider] = &entry
			}
		}
	}

	var providers []spending.ProviderUsageSummary
	for _, p := range providerMap {
		providers = append(providers, *p)
	}
	if agentEntries == nil {
		agentEntries = []agentCostEntry{}
	}
	if providers == nil {
		providers = []spending.ProviderUsageSummary{}
	}

	resp := spendingChartResponse{
		Daily:     daily,
		Agents:    agentEntries,
		Providers: providers,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		h.serverError(w, r, "encoding spending chart response", err)
	}
}

// SpendingProviderDrillDown returns an HTMX fragment with per-agent breakdown for a provider.
func (h *Handlers) SpendingProviderDrillDown(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	provider := r.PathValue("provider")

	sp := h.kyvik.Spending()
	if sp == nil {
		http.Error(w, "spending tracker not configured", http.StatusServiceUnavailable)
		return
	}

	_, period := parseRange(r)

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

	type providerAgentRow struct {
		AgentID      string
		AgentName    string
		TotalTokens  int64
		TotalCost    float64
		RequestCount int64
	}

	var rows []providerAgentRow
	for _, a := range agents {
		breakdown, berr := sp.GetProviderBreakdown(ctx, a.ID, period)
		if berr != nil {
			continue
		}
		for _, entry := range breakdown {
			if entry.Provider == provider {
				rows = append(rows, providerAgentRow{
					AgentID:      a.ID,
					AgentName:    a.Name,
					TotalTokens:  entry.TotalTokens,
					TotalCost:    entry.TotalCost,
					RequestCount: entry.RequestCount,
				})
			}
		}
	}

	h.renderFragment(w, r, "spending-provider-detail", map[string]any{
		"Provider": provider,
		"Rows":     rows,
	})
}

// SpendingUpdateLimit handles inline limit editing via HTMX.
func (h *Handlers) SpendingUpdateLimit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	agentID := r.PathValue("agent_id")

	sp := h.kyvik.Spending()
	if sp == nil {
		http.Error(w, "spending tracker not configured", http.StatusServiceUnavailable)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form data", http.StatusBadRequest)
		return
	}

	limit := types.SpendingLimits{
		MaxSpendPerDay:    parseFormFloat(r, "max_spend_per_day"),
		MaxSpendPerMonth:  parseFormFloat(r, "max_spend_per_month"),
		MaxTokensPerDay:   parseFormInt64(r, "max_tokens_per_day"),
		MaxTokensPerMonth: parseFormInt64(r, "max_tokens_per_month"),
	}

	if err := sp.SetAgentLimit(ctx, agentID, limit); err != nil {
		h.serverError(w, r, "updating spending limit", err)
		return
	}

	// Audit log the change.
	al := h.kyvik.Audit()
	if al != nil {
		_ = audit.LogSpending(ctx, al, agentID, "limit_update", "allowed",
			fmt.Sprintf("day=$%.2f month=$%.2f tokens_day=%d tokens_month=%d",
				limit.MaxSpendPerDay, limit.MaxSpendPerMonth,
				limit.MaxTokensPerDay, limit.MaxTokensPerMonth))
	}

	// Return updated row fragment.
	budget, _ := sp.CheckBudget(ctx, agentID)
	if budget == nil {
		budget = &spending.BudgetStatus{AgentID: agentID, WithinBudget: true}
	}

	agent, _ := h.kyvik.GetAgent(ctx, agentID)
	name := agentID
	if agent != nil {
		name = agent.Name
	}

	h.renderFragment(w, r, "spending-agent-row", spendingAgentRow{
		AgentID:    agentID,
		AgentName:  name,
		Budget:     budget,
		AlertLevel: budgetAlertLevel(budget),
		CanAdjust:  true, // only users with PermSpendingAdjust reach this handler
	})
}

// SpendingExportCSV exports spending data as a CSV file.
func (h *Handlers) SpendingExportCSV(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	sp := h.kyvik.Spending()
	if sp == nil {
		http.Error(w, "spending tracker not configured", http.StatusServiceUnavailable)
		return
	}

	_, period := parseRange(r)
	rangeParam := r.URL.Query().Get("range")
	if rangeParam == "" {
		rangeParam = "7d"
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

	filename := fmt.Sprintf("spending-%s-%s.csv", rangeParam, time.Now().Format("2006-01-02"))
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", "attachment; filename="+filename)

	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"agent_id", "agent_name", "total_tokens", "total_cost", "request_count", "period"})

	for _, a := range agents {
		summary, serr := sp.GetSummary(ctx, spending.Filter{AgentID: a.ID, Period: period})
		if serr != nil || summary == nil {
			continue
		}
		_ = cw.Write([]string{
			a.ID,
			a.Name,
			strconv.FormatInt(summary.TotalTokens, 10),
			fmt.Sprintf("%.6f", summary.TotalCost),
			strconv.FormatInt(summary.RequestCount, 10),
			period,
		})
	}

	cw.Flush()
}

// budgetAlertLevel returns "green", "yellow", or "red" based on budget usage.
func budgetAlertLevel(b *spending.BudgetStatus) string {
	if b == nil {
		return "green"
	}
	if !b.WithinBudget {
		return "red"
	}
	// Check if any limit is >=80% used.
	if b.DailyLimit > 0 && b.DailyUsed/b.DailyLimit >= 0.8 {
		return "yellow"
	}
	if b.MonthlyLimit > 0 && b.MonthlyUsed/b.MonthlyLimit >= 0.8 {
		return "yellow"
	}
	return "green"
}

func parseFormFloat(r *http.Request, key string) float64 {
	v, _ := strconv.ParseFloat(r.FormValue(key), 64)
	return v
}

func parseFormInt64(r *http.Request, key string) int64 {
	v, _ := strconv.ParseInt(r.FormValue(key), 10, 64)
	return v
}
