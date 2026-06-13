// Package sysinfo implements a KTP tool for system status queries.
package sysinfo

import (
	"context"
	"fmt"
	"time"

	"github.com/kkjorsvik/kyvik/internal/audit"
	"github.com/kkjorsvik/kyvik/internal/ktp"
	"github.com/kkjorsvik/kyvik/internal/spending"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// StatusStore is the subset of store.Store needed by the system status tool.
type StatusStore interface {
	ListAgents(ctx context.Context) ([]types.AgentConfig, error)
	GetAgent(ctx context.Context, id string) (*types.AgentConfig, error)
	AggregateUsage(ctx context.Context, agentID string, period string) (*spending.Summary, error)
	AggregateProviderUsage(ctx context.Context, agentID, period string) ([]spending.ProviderUsageSummary, error)
	ListAuditEntries(ctx context.Context, filter audit.Filter) ([]types.AuditEntry, error)
	QueryAllSecurityEvents(ctx context.Context, severity string, limit int) ([]types.SecurityEvent, error)
}

// StatusTool implements ktp.InlineTool for system status queries.
type StatusTool struct {
	store StatusStore
}

// New creates a StatusTool backed by the given store.
func New(store StatusStore) *StatusTool {
	return &StatusTool{store: store}
}

// Declaration returns the system_status tool's KTP declaration.
func (t *StatusTool) Declaration() ktp.ToolDeclaration {
	return ktp.ToolDeclaration{
		Name:        "system_status",
		Version:     "1.0.0",
		Description: "Query system status, agent health, spending, and recent events",
		MinTier:      ktp.TierGuide,
		DefaultTiers: []string{ktp.TierGuide, ktp.TierAdmin},
		Actions: []ktp.ActionSpec{
			{
				Name:        "agent_list",
				Description: "List all agents with their current status",
				Parameters:  ktp.JSONSchema{Type: "object"},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"agents": {Type: "array", Items: &ktp.JSONSchema{Type: "object"}},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "system", Access: "read", Resource: "agents"}},
			},
			{
				Name:        "agent_status",
				Description: "Get detailed status for a specific agent",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"agent_id": {Type: "string", Description: "Agent ID to query"},
					},
					Required: []string{"agent_id"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"agent": {Type: "object"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "system", Access: "read", Resource: "agents"}},
			},
			{
				Name:        "system_overview",
				Description: "Get high-level system overview: agent counts, spending, errors",
				Parameters:  ktp.JSONSchema{Type: "object"},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"total_agents":     {Type: "integer"},
						"running":          {Type: "integer"},
						"spend_today":      {Type: "number"},
						"error_count_24h":  {Type: "integer"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "system", Access: "read", Resource: "overview"}},
			},
			{
				Name:        "spending_summary",
				Description: "Get spending breakdown by agent and provider",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"period": {Type: "string", Description: "Time period: day or month", Enum: []string{"day", "month"}, Default: "day"},
					},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"agents":    {Type: "array", Items: &ktp.JSONSchema{Type: "object"}},
						"providers": {Type: "array", Items: &ktp.JSONSchema{Type: "object"}},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "system", Access: "read", Resource: "spending"}},
			},
			{
				Name:        "recent_errors",
				Description: "Get recent denied audit entries",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"limit": {Type: "integer", Description: "Max entries to return", Default: 10},
					},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"errors": {Type: "array", Items: &ktp.JSONSchema{Type: "object"}},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "system", Access: "read", Resource: "audit"}},
			},
			{
				Name:        "recent_alerts",
				Description: "Get recent security events",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"limit": {Type: "integer", Description: "Max entries to return", Default: 10},
					},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"alerts": {Type: "array", Items: &ktp.JSONSchema{Type: "object"}},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "system", Access: "read", Resource: "security"}},
			},
		},
	}
}

// Inline returns true — system status accesses local state only.
func (t *StatusTool) Inline() bool { return true }

// Execute dispatches to the requested action.
func (t *StatusTool) Execute(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	start := time.Now()

	switch req.Action {
	case "agent_list":
		return t.agentList(ctx, req, start)
	case "agent_status":
		return t.agentStatus(ctx, req, start)
	case "system_overview":
		return t.systemOverview(ctx, req, start)
	case "spending_summary":
		return t.spendingSummary(ctx, req, start)
	case "recent_errors":
		return t.recentErrors(ctx, req, start)
	case "recent_alerts":
		return t.recentAlerts(ctx, req, start)
	default:
		return errResp(req.ID, fmt.Sprintf("unknown action: %s", req.Action)), nil
	}
}

func (t *StatusTool) agentList(ctx context.Context, req ktp.ToolRequest, start time.Time) (*ktp.ToolResponse, error) {
	agents, err := t.store.ListAgents(ctx)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("list agents: %s", err)), nil
	}

	result := make([]map[string]any, 0, len(agents))
	for _, a := range agents {
		result = append(result, map[string]any{
			"id":       a.ID,
			"name":     a.Name,
			"status":   string(a.ActualState),
			"template": a.Template,
			"is_guide": a.IsGuide,
		})
	}

	resp := ktp.NewToolResponse(req.ID, true, map[string]any{"agents": result}, "", ms(start))
	return &resp, nil
}

func (t *StatusTool) agentStatus(ctx context.Context, req ktp.ToolRequest, start time.Time) (*ktp.ToolResponse, error) {
	agentID, err := strParam(req.Parameters, "agent_id")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	agent, err := t.store.GetAgent(ctx, agentID)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("get agent: %s", err)), nil
	}

	var spendToday float64
	if summary, err := t.store.AggregateUsage(ctx, agentID, "day"); err == nil && summary != nil {
		spendToday = summary.TotalCost
	}

	result := map[string]any{
		"id":             agent.ID,
		"name":           agent.Name,
		"status":         string(agent.ActualState),
		"desired_state":  string(agent.DesiredState),
		"template":       agent.Template,
		"model":          agent.ModelConfig.Provider + "/" + agent.ModelConfig.Model,
		"spending_today": spendToday,
		"last_error":     agent.LastError,
		"is_guide":       agent.IsGuide,
	}

	resp := ktp.NewToolResponse(req.ID, true, map[string]any{"agent": result}, "", ms(start))
	return &resp, nil
}

func (t *StatusTool) systemOverview(ctx context.Context, req ktp.ToolRequest, start time.Time) (*ktp.ToolResponse, error) {
	agents, err := t.store.ListAgents(ctx)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("list agents: %s", err)), nil
	}

	running := 0
	for _, a := range agents {
		if a.ActualState == types.AgentStatusRunning {
			running++
		}
	}

	var spendToday float64
	if summary, err := t.store.AggregateUsage(ctx, "", "day"); err == nil && summary != nil {
		spendToday = summary.TotalCost
	}

	// Count denied entries in the last 24h.
	now := time.Now().UTC()
	dayAgo := now.Add(-24 * time.Hour)
	errorCount := 0
	entries, err := t.store.ListAuditEntries(ctx, audit.Filter{
		Decision:  "denied",
		StartTime: &dayAgo,
		Limit:     1000,
	})
	if err == nil {
		errorCount = len(entries)
	}

	result := map[string]any{
		"total_agents":    len(agents),
		"running":         running,
		"spend_today":     spendToday,
		"error_count_24h": errorCount,
	}

	resp := ktp.NewToolResponse(req.ID, true, result, "", ms(start))
	return &resp, nil
}

func (t *StatusTool) spendingSummary(ctx context.Context, req ktp.ToolRequest, start time.Time) (*ktp.ToolResponse, error) {
	period := strDefault(req.Parameters, "period", "day")
	if period != "day" && period != "month" {
		period = "day"
	}

	agents, err := t.store.ListAgents(ctx)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("list agents: %s", err)), nil
	}

	agentSpending := make([]map[string]any, 0, len(agents))
	for _, a := range agents {
		summary, err := t.store.AggregateUsage(ctx, a.ID, period)
		if err != nil || summary == nil {
			continue
		}
		if summary.TotalCost == 0 && summary.TotalTokens == 0 {
			continue
		}
		agentSpending = append(agentSpending, map[string]any{
			"agent_id":     a.ID,
			"agent_name":   a.Name,
			"total_tokens": summary.TotalTokens,
			"total_cost":   summary.TotalCost,
			"requests":     summary.RequestCount,
		})
	}

	providerUsage, _ := t.store.AggregateProviderUsage(ctx, "", period)
	providers := make([]map[string]any, 0, len(providerUsage))
	for _, p := range providerUsage {
		providers = append(providers, map[string]any{
			"provider":     p.Provider,
			"total_tokens": p.TotalTokens,
			"total_cost":   p.TotalCost,
			"requests":     p.RequestCount,
		})
	}

	result := map[string]any{
		"period":    period,
		"agents":    agentSpending,
		"providers": providers,
	}

	resp := ktp.NewToolResponse(req.ID, true, result, "", ms(start))
	return &resp, nil
}

func (t *StatusTool) recentErrors(ctx context.Context, req ktp.ToolRequest, start time.Time) (*ktp.ToolResponse, error) {
	limit := intDefault(req.Parameters, "limit", 10)

	entries, err := t.store.ListAuditEntries(ctx, audit.Filter{
		Decision: "denied",
		Limit:    limit,
	})
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("list audit entries: %s", err)), nil
	}

	result := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		result = append(result, map[string]any{
			"agent_id":   e.AgentID,
			"event_type": string(e.EventType),
			"action":     e.Action,
			"resource":   e.Resource,
			"details":    e.Details,
			"timestamp":  e.Timestamp.UTC().Format(time.RFC3339),
		})
	}

	resp := ktp.NewToolResponse(req.ID, true, map[string]any{"errors": result}, "", ms(start))
	return &resp, nil
}

func (t *StatusTool) recentAlerts(ctx context.Context, req ktp.ToolRequest, start time.Time) (*ktp.ToolResponse, error) {
	limit := intDefault(req.Parameters, "limit", 10)

	events, err := t.store.QueryAllSecurityEvents(ctx, "", limit)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("query security events: %s", err)), nil
	}

	result := make([]map[string]any, 0, len(events))
	for _, e := range events {
		result = append(result, map[string]any{
			"id":         e.ID,
			"agent_id":   e.AgentID,
			"event_type": e.EventType,
			"severity":   e.Severity,
			"details":    e.Details,
			"created_at": e.CreatedAt.UTC().Format(time.RFC3339),
		})
	}

	resp := ktp.NewToolResponse(req.ID, true, map[string]any{"alerts": result}, "", ms(start))
	return &resp, nil
}

// --- parameter helpers ---

func strParam(params map[string]any, key string) (string, error) {
	raw, ok := params[key]
	if !ok {
		return "", fmt.Errorf("missing required parameter: %s", key)
	}
	s, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("parameter %s must be a string", key)
	}
	return s, nil
}

func strDefault(params map[string]any, key, def string) string {
	raw, ok := params[key]
	if !ok {
		return def
	}
	s, ok := raw.(string)
	if !ok {
		return def
	}
	return s
}

func intDefault(params map[string]any, key string, def int) int {
	raw, ok := params[key]
	if !ok {
		return def
	}
	switch v := raw.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	default:
		return def
	}
}

func errResp(reqID, msg string) *ktp.ToolResponse {
	resp := ktp.NewToolResponse(reqID, false, nil, msg, 0)
	return &resp
}

func ms(start time.Time) int64 {
	return time.Since(start).Milliseconds()
}
