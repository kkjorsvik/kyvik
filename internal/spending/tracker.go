// Package spending tracks token usage and costs per agent.
// Limits are layered: global → per-agent → real-time adjustable.
package spending

import (
	"context"
	"time"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

// RecordOptions carries per-request routing context for spending records.
type RecordOptions struct {
	Model          string // actual model used (overrides StoreTracker.model if non-empty)
	ModelSlot      string // "default", "heavy", "vision", etc.
	RoutedBy       string // "default", "prefix", "vision", "classifier", "system:classifier"
	Provider       string // "openrouter", "ollama"
	ParentAgentID  string // for future ephemeral worker attribution
	CostSource     string // "provider_reported", "catalog_computed", "unknown"
	UsageSource    string // "provider_usage", "estimated", "unknown"
	UsageComplete  bool   // false when provider usage is unavailable/incomplete
	PricingVersion string // pricing catalog version used for computed costs
	Category       string // e.g. "compression", "memory_extraction", "" (default agent work)
}

const (
	CostSourceProviderReported = "provider_reported"
	CostSourceCatalogComputed  = "catalog_computed"
	CostSourceUnknown          = "unknown"

	UsageSourceProvider  = "provider_usage"
	UsageSourceEstimated = "estimated"
	UsageSourceUnknown   = "unknown"
)

// DailyUsage holds aggregated usage for a single calendar day.
type DailyUsage struct {
	Date         string  `json:"date"` // "2026-02-18"
	TotalTokens  int64   `json:"total_tokens"`
	TotalCost    float64 `json:"total_cost"`
	RequestCount int64   `json:"request_count"`
}

// SlotUsageSummary aggregates usage per model slot.
type SlotUsageSummary struct {
	SlotName     string  `json:"slot_name"`
	Provider     string  `json:"provider"`
	Model        string  `json:"model"`
	TotalTokens  int64   `json:"total_tokens"`
	TotalCost    float64 `json:"total_cost"`
	RequestCount int64   `json:"request_count"`
}

// ProviderUsageSummary aggregates usage per provider.
type ProviderUsageSummary struct {
	Provider     string  `json:"provider"`
	TotalTokens  int64   `json:"total_tokens"`
	TotalCost    float64 `json:"total_cost"`
	RequestCount int64   `json:"request_count"`
}

// Tracker monitors and enforces spending limits.
type Tracker interface {
	// Record logs token usage and cost for an agent.
	Record(ctx context.Context, agentID string, tokensIn, tokensOut int64, cost float64, opts RecordOptions) error

	// CheckBudget returns whether the agent is within its spending limits.
	CheckBudget(ctx context.Context, agentID string) (*BudgetStatus, error)

	// GetSummary returns usage data matching the given filter.
	GetSummary(ctx context.Context, filter Filter) (*Summary, error)

	// GetSlotBreakdown returns per-slot usage for an agent in the given period.
	GetSlotBreakdown(ctx context.Context, agentID, period string) ([]SlotUsageSummary, error)

	// GetProviderBreakdown returns per-provider usage for an agent in the given period.
	GetProviderBreakdown(ctx context.Context, agentID, period string) ([]ProviderUsageSummary, error)

	// SetGlobalLimit updates the global spending limit.
	SetGlobalLimit(ctx context.Context, limit types.SpendingLimits) error

	// SetAgentLimit updates spending limits for a specific agent.
	SetAgentLimit(ctx context.Context, agentID string, limit types.SpendingLimits) error

	// GetDailyTimeSeries returns per-day aggregated usage for the last N days.
	// If agentID is empty, returns global totals across all agents.
	GetDailyTimeSeries(ctx context.Context, agentID string, days int) ([]DailyUsage, error)
}

// BudgetStatus indicates whether an agent can continue operating.
type BudgetStatus struct {
	AgentID         string  `json:"agent_id"`
	WithinBudget    bool    `json:"within_budget"`
	DailyUsed       float64 `json:"daily_used"`
	DailyLimit      float64 `json:"daily_limit"`
	MonthlyUsed     float64 `json:"monthly_used"`
	MonthlyLimit    float64 `json:"monthly_limit"`
	TokensToday     int64   `json:"tokens_today"`
	TokensThisMonth int64   `json:"tokens_this_month"`
}

// Summary contains aggregated usage data.
type Summary struct {
	AgentID      string  `json:"agent_id"`
	Period       string  `json:"period"`
	TotalTokens  int64   `json:"total_tokens"`
	TotalCost    float64 `json:"total_cost"`
	RequestCount int64   `json:"request_count"`
}

// Filter defines query parameters for usage data retrieval.
type Filter struct {
	AgentID   string     `json:"agent_id,omitempty"`
	Period    string     `json:"period,omitempty"` // "day", "month", "all"
	StartTime *time.Time `json:"start_time,omitempty"`
	EndTime   *time.Time `json:"end_time,omitempty"`
}
