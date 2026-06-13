// Package myspending implements a KTP tool that lets agents query their own spending.
package myspending

import (
	"context"
	"fmt"
	"time"

	"github.com/kkjorsvik/kyvik/internal/ktp"
	"github.com/kkjorsvik/kyvik/internal/spending"
)

// SpendingStore is the subset of store.Store needed by the my_spending tool.
type SpendingStore interface {
	AggregateUsage(ctx context.Context, agentID string, period string) (*spending.Summary, error)
}

// SpendingTool implements ktp.InlineTool for self-scoped spending queries.
type SpendingTool struct {
	store SpendingStore
}

// New creates a SpendingTool backed by the given store.
func New(store SpendingStore) *SpendingTool {
	return &SpendingTool{store: store}
}

// Declaration returns the my_spending tool's KTP declaration.
func (t *SpendingTool) Declaration() ktp.ToolDeclaration {
	return ktp.ToolDeclaration{
		Name:        "my_spending",
		Version:     "1.0.0",
		Description: "Query your own spending summary (tokens, cost, requests)",
		MinTier:     ktp.TierReader,
		DefaultTiers: []string{
			ktp.TierReader,
			ktp.TierWriter,
			ktp.TierOperator,
			ktp.TierAdmin,
		},
		Actions: []ktp.ActionSpec{
			{
				Name:        "summary",
				Description: "Get your spending summary for the given period",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"period": {
							Type:        "string",
							Description: "Time period: day or month",
							Enum:        []string{"day", "month"},
							Default:     "day",
						},
					},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"agent_id":     {Type: "string"},
						"period":       {Type: "string"},
						"total_tokens": {Type: "integer"},
						"total_cost":   {Type: "number"},
						"requests":     {Type: "integer"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "spending", Access: "read", Resource: "self"}},
			},
		},
	}
}

// Inline returns true — my_spending accesses local state only.
func (t *SpendingTool) Inline() bool { return true }

// Execute dispatches to the requested action.
func (t *SpendingTool) Execute(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	start := time.Now()

	switch req.Action {
	case "summary":
		return t.summary(ctx, req, start)
	default:
		return errResp(req.ID, fmt.Sprintf("unknown action: %s", req.Action)), nil
	}
}

func (t *SpendingTool) summary(ctx context.Context, req ktp.ToolRequest, start time.Time) (*ktp.ToolResponse, error) {
	if req.AgentID == "" {
		return errResp(req.ID, "agent_id is required"), nil
	}

	period := strDefault(req.Parameters, "period", "day")
	if period != "day" && period != "month" {
		period = "day"
	}

	summary, err := t.store.AggregateUsage(ctx, req.AgentID, period)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("aggregate usage: %s", err)), nil
	}

	var totalTokens int64
	var totalCost float64
	var requests int64
	if summary != nil {
		totalTokens = summary.TotalTokens
		totalCost = summary.TotalCost
		requests = summary.RequestCount
	}

	result := map[string]any{
		"agent_id":     req.AgentID,
		"period":       period,
		"total_tokens": totalTokens,
		"total_cost":   totalCost,
		"requests":     requests,
	}

	resp := ktp.NewToolResponse(req.ID, true, result, "", ms(start))
	return &resp, nil
}

// --- helpers ---

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

func errResp(reqID, msg string) *ktp.ToolResponse {
	resp := ktp.NewToolResponse(reqID, false, nil, msg, 0)
	return &resp
}

func ms(start time.Time) int64 {
	return time.Since(start).Milliseconds()
}
