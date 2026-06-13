package core

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/kkjorsvik/kyvik/internal/audit"
	"github.com/kkjorsvik/kyvik/internal/spending"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// AlertFilter controls which alerts are returned.
type AlertFilter struct {
	SourceType string
	AgentID    string
	Limit      int
}

// ListAlerts aggregates alerts from multiple sources into a unified list.
func (p *Kyvik) ListAlerts(ctx context.Context, filter AlertFilter) ([]types.DashboardAlert, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}

	// Build agent name lookup.
	agents, err := p.store.ListAgents(ctx)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	agentNames := make(map[string]string, len(agents))
	for _, a := range agents {
		agentNames[a.ID] = a.Name
	}

	var alerts []types.DashboardAlert

	// 1. Circuit breaker trips from audit log.
	if filter.SourceType == "" || filter.SourceType == "circuit_breaker" {
		cbAlerts, err := p.circuitBreakerAlerts(ctx, filter.AgentID, agentNames)
		if err == nil {
			alerts = append(alerts, cbAlerts...)
		}
	}

	// 2. Security events (critical + warning).
	if filter.SourceType == "" || filter.SourceType == "security" {
		secAlerts, err := p.securityAlerts(ctx, filter.AgentID, agentNames)
		if err == nil {
			alerts = append(alerts, secAlerts...)
		}
	}

	// 3. Agents in error state.
	if filter.SourceType == "" || filter.SourceType == "agent_error" {
		for _, a := range agents {
			if filter.AgentID != "" && a.ID != filter.AgentID {
				continue
			}
			if a.ActualState == types.AgentStatusError {
				alerts = append(alerts, types.DashboardAlert{
					ID:          "agent_error:" + a.ID,
					SourceType:  "agent_error",
					SourceID:    a.ID,
					AgentID:     a.ID,
					AgentName:   a.Name,
					Severity:    "warning",
					Title:       "Agent in error state",
					Description: a.LastError,
					Timestamp:   a.UpdatedAt,
				})
			}
		}
	}

	// 4. Spending alerts (agents at 90%+ of daily budget).
	if filter.SourceType == "" || filter.SourceType == "spending" {
		spendAlerts := p.spendingAlerts(ctx, filter.AgentID, agents)
		alerts = append(alerts, spendAlerts...)
	}

	// Sort by timestamp descending.
	sort.Slice(alerts, func(i, j int) bool {
		return alerts[i].Timestamp.After(alerts[j].Timestamp)
	})

	// Join with acknowledgments.
	acked, err := p.store.ListAcknowledgedAlerts(ctx)
	if err == nil {
		for i := range alerts {
			key := alerts[i].SourceType + ":" + alerts[i].SourceID
			if t, ok := acked[key]; ok {
				alerts[i].Acknowledged = true
				alerts[i].AckedAt = &t
			}
		}
	}

	// Apply limit.
	if len(alerts) > limit {
		alerts = alerts[:limit]
	}

	return alerts, nil
}

// UnacknowledgedAlertCount returns the number of alerts that haven't been acknowledged.
func (p *Kyvik) UnacknowledgedAlertCount(ctx context.Context) int {
	alerts, err := p.ListAlerts(ctx, AlertFilter{Limit: 200})
	if err != nil {
		return 0
	}
	count := 0
	for _, a := range alerts {
		if !a.Acknowledged {
			count++
		}
	}
	return count
}

// AcknowledgeAlert marks an alert as acknowledged.
func (p *Kyvik) AcknowledgeAlert(ctx context.Context, sourceType, sourceID string) error {
	return p.store.AcknowledgeAlert(ctx, sourceType, sourceID)
}

// circuitBreakerAlerts queries the audit log for circuit breaker trip events.
func (p *Kyvik) circuitBreakerAlerts(ctx context.Context, agentID string, agentNames map[string]string) ([]types.DashboardAlert, error) {
	f := audit.Filter{
		EventType: types.EventAgentLifecycle,
		Limit:     50,
	}
	if agentID != "" {
		f.AgentID = agentID
	}

	entries, err := p.store.ListAuditEntries(ctx, f)
	if err != nil {
		return nil, err
	}

	var alerts []types.DashboardAlert
	for _, e := range entries {
		if e.Action != "quarantine" && e.Action != "circuit_breaker_trip" && e.Action != "agent_quarantined" {
			continue
		}
		alerts = append(alerts, types.DashboardAlert{
			ID:          "circuit_breaker:" + e.ID,
			SourceType:  "circuit_breaker",
			SourceID:    e.ID,
			AgentID:     e.AgentID,
			AgentName:   agentNames[e.AgentID],
			Severity:    "critical",
			Title:       "Circuit breaker tripped",
			Description: e.Details,
			Timestamp:   e.Timestamp,
		})
	}
	return alerts, nil
}

// securityAlerts queries security events for warning/critical severity.
func (p *Kyvik) securityAlerts(ctx context.Context, agentID string, agentNames map[string]string) ([]types.DashboardAlert, error) {
	var allEvents []types.SecurityEvent
	for _, sev := range []string{"critical", "warning"} {
		events, err := p.store.QueryAllSecurityEvents(ctx, sev, 25)
		if err != nil {
			continue
		}
		allEvents = append(allEvents, events...)
	}

	var alerts []types.DashboardAlert
	for _, e := range allEvents {
		if agentID != "" && e.AgentID != agentID {
			continue
		}
		alerts = append(alerts, types.DashboardAlert{
			ID:          "security:" + e.ID,
			SourceType:  "security",
			SourceID:    e.ID,
			AgentID:     e.AgentID,
			AgentName:   agentNames[e.AgentID],
			Severity:    e.Severity,
			Title:       "Security: " + e.EventType,
			Description: e.Details,
			Timestamp:   e.CreatedAt,
		})
	}
	return alerts, nil
}

// spendingAlerts checks each agent's spending against their daily budget.
func (p *Kyvik) spendingAlerts(ctx context.Context, filterAgentID string, agents []types.AgentConfig) []types.DashboardAlert {
	var alerts []types.DashboardAlert
	today := time.Now().Format("2006-01-02")

	for _, a := range agents {
		if filterAgentID != "" && a.ID != filterAgentID {
			continue
		}
		if a.Limits.MaxSpendPerDay <= 0 {
			continue
		}

		budget, err := p.spending.CheckBudget(ctx, a.ID)
		if err != nil || budget == nil {
			continue
		}

		// Get daily spending to calculate percentage.
		summary, err := p.spending.GetSummary(ctx, spendingFilterForAgent(a.ID))
		if err != nil || summary == nil {
			continue
		}

		pct := summary.TotalCost / a.Limits.MaxSpendPerDay * 100
		if pct >= 90 {
			severity := "warning"
			if pct >= 100 {
				severity = "critical"
			}
			alerts = append(alerts, types.DashboardAlert{
				ID:          fmt.Sprintf("spending:%s:%s", a.ID, today),
				SourceType:  "spending",
				SourceID:    fmt.Sprintf("%s:%s", a.ID, today),
				AgentID:     a.ID,
				AgentName:   a.Name,
				Severity:    severity,
				Title:       fmt.Sprintf("Spending at %.0f%% of daily budget", pct),
				Description: fmt.Sprintf("$%.4f / $%.4f daily limit", summary.TotalCost, a.Limits.MaxSpendPerDay),
				Timestamp:   time.Now(),
			})
		}
	}
	return alerts
}

// spendingFilterForAgent creates a spending.Filter for daily usage.
func spendingFilterForAgent(agentID string) spending.Filter {
	return spending.Filter{
		AgentID: agentID,
		Period:  "day",
	}
}
