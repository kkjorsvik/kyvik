package spending

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kkjorsvik/kyvik/internal/audit"
	"github.com/kkjorsvik/kyvik/internal/notifications"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// SpendingStore is the narrow persistence interface that StoreTracker needs.
// It breaks the circular import between spending and store packages.
// The store implementations satisfy this interface.
type SpendingStore interface {
	InsertUsageRecord(ctx context.Context, agentID string, tokensIn, tokensOut int64, cost float64,
		model, modelSlot, routedBy, provider, parentAgentID string) error
	AggregateUsage(ctx context.Context, agentID string, period string) (*Summary, error)
	AggregateSlotUsage(ctx context.Context, agentID, period string) ([]SlotUsageSummary, error)
	AggregateProviderUsage(ctx context.Context, agentID, period string) ([]ProviderUsageSummary, error)
	GetSpendingLimit(ctx context.Context, agentID string) (*types.SpendingLimits, error)
	SetSpendingLimit(ctx context.Context, agentID string, limit types.SpendingLimits) error
	AggregateDailyUsage(ctx context.Context, agentID string, days int) ([]DailyUsage, error)
}

// detailedUsageStore supports writing provenance metadata with usage records.
type detailedUsageStore interface {
	InsertUsageRecordDetailed(
		ctx context.Context,
		agentID string,
		tokensIn, tokensOut int64,
		cost float64,
		model, modelSlot, routedBy, provider, parentAgentID string,
		costSource, usageSource string,
		usageComplete bool,
		pricingVersion string,
		category string,
	) error
}

// pricingLookupStore supports catalog-based cost computation.
type pricingLookupStore interface {
	LookupModelPrice(ctx context.Context, provider, model string, at time.Time) (float64, float64, string, bool, error)
}

// Compile-time check that StoreTracker implements Tracker.
var _ Tracker = (*StoreTracker)(nil)

// StoreTracker implements Tracker by delegating persistence to a SpendingStore
// and audit-logging budget violations.
type StoreTracker struct {
	store     SpendingStore
	audit     audit.Logger
	model     string // default model name for usage records
	notifier  notifications.Notifier
	threshold int // percentage (0-100), 0 = disabled
}

// NewStoreTracker creates a StoreTracker.
func NewStoreTracker(store SpendingStore, auditLogger audit.Logger, defaultModel string) *StoreTracker {
	return &StoreTracker{
		store: store,
		audit: auditLogger,
		model: defaultModel,
	}
}

// SetNotifier configures a notifier for spending threshold alerts.
func (t *StoreTracker) SetNotifier(n notifications.Notifier, threshold int) {
	t.notifier = n
	t.threshold = threshold
}

// Record logs token usage and cost for an agent.
func (t *StoreTracker) Record(ctx context.Context, agentID string, tokensIn, tokensOut int64, cost float64, opts RecordOptions) error {
	model := t.model
	if opts.Model != "" {
		model = opts.Model
	}
	slot := opts.ModelSlot
	if slot == "" {
		slot = "default"
	}
	routedBy := opts.RoutedBy
	if routedBy == "" {
		routedBy = "default"
	}
	costSource := opts.CostSource
	if costSource == "" && cost > 0 {
		costSource = CostSourceProviderReported
	}
	usageSource := opts.UsageSource
	if usageSource == "" {
		if tokensIn > 0 || tokensOut > 0 {
			usageSource = UsageSourceProvider
		} else {
			usageSource = UsageSourceUnknown
		}
	}
	usageComplete := opts.UsageComplete
	if !usageComplete && (tokensIn > 0 || tokensOut > 0) {
		usageComplete = true
	}
	pricingVersion := opts.PricingVersion

	// Compute cost from pricing catalog when provider didn't return a cost.
	if cost <= 0 && opts.Provider != "" && model != "" && (tokensIn > 0 || tokensOut > 0) {
		if ps, ok := t.store.(pricingLookupStore); ok {
			inPerM, outPerM, version, found, err := ps.LookupModelPrice(ctx, opts.Provider, model, time.Now())
			if err != nil {
				return fmt.Errorf("lookup model price: %w", err)
			}
			if found {
				cost = (float64(tokensIn)*inPerM + float64(tokensOut)*outPerM) / 1_000_000
				costSource = CostSourceCatalogComputed
				if pricingVersion == "" {
					pricingVersion = version
				}
			}
		}
	}
	if costSource == "" {
		costSource = CostSourceUnknown
	}

	if ds, ok := t.store.(detailedUsageStore); ok {
		if err := ds.InsertUsageRecordDetailed(
			ctx, agentID, tokensIn, tokensOut, cost,
			model, slot, routedBy, opts.Provider, opts.ParentAgentID,
			costSource, usageSource, usageComplete, pricingVersion,
			opts.Category,
		); err != nil {
			return fmt.Errorf("record detailed usage: %w", err)
		}
	} else {
		if err := t.store.InsertUsageRecord(ctx, agentID, tokensIn, tokensOut, cost,
			model, slot, routedBy, opts.Provider, opts.ParentAgentID); err != nil {
			return fmt.Errorf("record usage: %w", err)
		}
	}
	_ = audit.LogSpending(ctx, t.audit, agentID, "record",
		"allowed", fmt.Sprintf("tokens_in=%d tokens_out=%d cost=%.4f cost_source=%s", tokensIn, tokensOut, cost, costSource))
	return nil
}

// GetSlotBreakdown returns per-slot usage for an agent in the given period.
func (t *StoreTracker) GetSlotBreakdown(ctx context.Context, agentID, period string) ([]SlotUsageSummary, error) {
	return t.store.AggregateSlotUsage(ctx, agentID, period)
}

// GetProviderBreakdown returns per-provider usage for an agent in the given period.
func (t *StoreTracker) GetProviderBreakdown(ctx context.Context, agentID, period string) ([]ProviderUsageSummary, error) {
	return t.store.AggregateProviderUsage(ctx, agentID, period)
}

// CheckBudget returns whether the agent is within its spending limits.
func (t *StoreTracker) CheckBudget(ctx context.Context, agentID string) (*BudgetStatus, error) {
	daySummary, err := t.store.AggregateUsage(ctx, agentID, "day")
	if err != nil {
		return nil, fmt.Errorf("aggregate daily usage: %w", err)
	}
	monthSummary, err := t.store.AggregateUsage(ctx, agentID, "month")
	if err != nil {
		return nil, fmt.Errorf("aggregate monthly usage: %w", err)
	}

	globalLimit, err := t.store.GetSpendingLimit(ctx, "__global__")
	if err != nil && !errors.Is(err, types.ErrNotFound) {
		return nil, fmt.Errorf("get global limit: %w", err)
	}
	if globalLimit == nil {
		globalLimit = &types.SpendingLimits{}
	}

	agentLimit, err := t.store.GetSpendingLimit(ctx, agentID)
	if err != nil && !errors.Is(err, types.ErrNotFound) {
		return nil, fmt.Errorf("get agent limit: %w", err)
	}
	if agentLimit == nil {
		agentLimit = &types.SpendingLimits{}
	}

	effectiveDailySpend := resolveLimit(globalLimit.MaxSpendPerDay, agentLimit.MaxSpendPerDay)
	effectiveMonthlySpend := resolveLimit(globalLimit.MaxSpendPerMonth, agentLimit.MaxSpendPerMonth)
	effectiveDailyTokens := resolveLimitInt(globalLimit.MaxTokensPerDay, agentLimit.MaxTokensPerDay)
	effectiveMonthlyTokens := resolveLimitInt(globalLimit.MaxTokensPerMonth, agentLimit.MaxTokensPerMonth)

	status := &BudgetStatus{
		AgentID:         agentID,
		WithinBudget:    true,
		DailyUsed:       daySummary.TotalCost,
		DailyLimit:      effectiveDailySpend,
		MonthlyUsed:     monthSummary.TotalCost,
		MonthlyLimit:    effectiveMonthlySpend,
		TokensToday:     daySummary.TotalTokens,
		TokensThisMonth: monthSummary.TotalTokens,
	}

	if effectiveDailySpend > 0 && daySummary.TotalCost >= effectiveDailySpend {
		status.WithinBudget = false
	}
	if effectiveMonthlySpend > 0 && monthSummary.TotalCost >= effectiveMonthlySpend {
		status.WithinBudget = false
	}
	if effectiveDailyTokens > 0 && daySummary.TotalTokens >= effectiveDailyTokens {
		status.WithinBudget = false
	}
	if effectiveMonthlyTokens > 0 && monthSummary.TotalTokens >= effectiveMonthlyTokens {
		status.WithinBudget = false
	}

	// Check spending threshold for notifications.
	if t.notifier != nil && t.threshold > 0 {
		thresholdRatio := float64(t.threshold) / 100.0
		var crossed []string

		if effectiveDailySpend > 0 {
			ratio := daySummary.TotalCost / effectiveDailySpend
			if ratio >= thresholdRatio {
				crossed = append(crossed, fmt.Sprintf("daily spend %.0f%%", ratio*100))
			}
		}
		if effectiveMonthlySpend > 0 {
			ratio := monthSummary.TotalCost / effectiveMonthlySpend
			if ratio >= thresholdRatio {
				crossed = append(crossed, fmt.Sprintf("monthly spend %.0f%%", ratio*100))
			}
		}
		if effectiveDailyTokens > 0 {
			ratio := float64(daySummary.TotalTokens) / float64(effectiveDailyTokens)
			if ratio >= thresholdRatio {
				crossed = append(crossed, fmt.Sprintf("daily tokens %.0f%%", ratio*100))
			}
		}
		if effectiveMonthlyTokens > 0 {
			ratio := float64(monthSummary.TotalTokens) / float64(effectiveMonthlyTokens)
			if ratio >= thresholdRatio {
				crossed = append(crossed, fmt.Sprintf("monthly tokens %.0f%%", ratio*100))
			}
		}

		if len(crossed) > 0 {
			_ = t.notifier.Send(ctx, notifications.Event{
				Type:      "spending_alert",
				Severity:  "warning",
				Agent:     agentID,
				Title:     fmt.Sprintf("Spending threshold crossed (%d%%)", t.threshold),
				Detail:    "Limits crossed: " + strings.Join(crossed, ", "),
				Timestamp: time.Now(),
			})
		}
	}

	if !status.WithinBudget {
		_ = audit.LogSpending(ctx, t.audit, agentID, "check_budget", "denied",
			fmt.Sprintf("daily_cost=%.4f/%g monthly_cost=%.4f/%g daily_tokens=%d/%d monthly_tokens=%d/%d",
				daySummary.TotalCost, effectiveDailySpend,
				monthSummary.TotalCost, effectiveMonthlySpend,
				daySummary.TotalTokens, effectiveDailyTokens,
				monthSummary.TotalTokens, effectiveMonthlyTokens))
	}

	return status, nil
}

// GetSummary returns usage data matching the given filter.
func (t *StoreTracker) GetSummary(ctx context.Context, filter Filter) (*Summary, error) {
	period := filter.Period
	if period == "" {
		period = "all"
	}
	return t.store.AggregateUsage(ctx, filter.AgentID, period)
}

// SetGlobalLimit updates the global spending limit.
func (t *StoreTracker) SetGlobalLimit(ctx context.Context, limit types.SpendingLimits) error {
	return t.store.SetSpendingLimit(ctx, "__global__", limit)
}

// SetAgentLimit updates spending limits for a specific agent.
func (t *StoreTracker) SetAgentLimit(ctx context.Context, agentID string, limit types.SpendingLimits) error {
	return t.store.SetSpendingLimit(ctx, agentID, limit)
}

// GetDailyTimeSeries returns per-day aggregated usage for the last N days.
func (t *StoreTracker) GetDailyTimeSeries(ctx context.Context, agentID string, days int) ([]DailyUsage, error) {
	return t.store.AggregateDailyUsage(ctx, agentID, days)
}

// resolveLimit returns the more restrictive (lower non-zero) of two limits.
// If both are 0, returns 0 (unlimited).
func resolveLimit(global, agent float64) float64 {
	if global == 0 {
		return agent
	}
	if agent == 0 {
		return global
	}
	if global < agent {
		return global
	}
	return agent
}

// resolveLimitInt returns the more restrictive (lower non-zero) of two limits.
// If both are 0, returns 0 (unlimited).
func resolveLimitInt(global, agent int64) int64 {
	if global == 0 {
		return agent
	}
	if agent == 0 {
		return global
	}
	if global < agent {
		return global
	}
	return agent
}
