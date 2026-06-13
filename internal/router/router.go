package router

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/kkjorsvik/kyvik/internal/history"
	"github.com/kkjorsvik/kyvik/internal/models"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// IncomingMessage is the input to the routing pipeline.
type IncomingMessage struct {
	Content     string
	Attachments []types.Attachment
}

// ClassifierCostInfo records the cost of a classifier call so the caller
// can attribute it to spending.
type ClassifierCostInfo struct {
	TokensIn  int64
	TokensOut int64
	Cost      float64
	Model     string
	SlotName  string
	Provider  string
}

// RouteDecision is the output of the routing pipeline.
type RouteDecision struct {
	Slot           ModelSlot
	Provider       models.Provider
	RoutedBy       string // "prefix", "classifier", "vision", "default"
	Message        string // content, with prefix stripped if applicable
	Details        string // human-readable explanation
	ClassifierCost ClassifierCostInfo
}

// Router consolidates prefix, vision, and classifier routing behind a
// single Route() call. It owns the Classifier and per-agent stats.
type Router struct {
	registry   *ProviderRegistry
	classifier *Classifier
	stats      map[string]*RoutingStats
	statsMu    sync.Mutex
}

// NewRouter creates a Router that resolves providers via registry.
func NewRouter(registry *ProviderRegistry) *Router {
	return &Router{
		registry:   registry,
		classifier: NewClassifier(),
		stats:      make(map[string]*RoutingStats),
	}
}

// Route determines which model slot should handle a message for the given agent.
//
// Pipeline order:
//  1. ResolveSlots — parse agent config
//  2. Single-slot fast path — return default immediately
//  3. Prefix trigger
//  4. Vision routing
//  5. Priority: prefix > vision > classifier > default
//  6. Provider resolution (with fallback)
//  7. Record stats
func (r *Router) Route(
	ctx context.Context,
	agentID string,
	msg IncomingMessage,
	config types.AgentConfig,
	recentHistory []history.HistoryEntry,
) (RouteDecision, error) {
	resolved, err := ResolveSlots(config)
	if err != nil {
		return RouteDecision{}, fmt.Errorf("resolve slots: %w", err)
	}

	slots := resolved.Config.Slots

	// Single-slot agent — skip all routing logic
	if len(slots) <= 1 {
		provider, ok := r.registry.GetProviderForSlot(resolved.DefaultSlot)
		if !ok {
			return RouteDecision{}, fmt.Errorf("provider %q not found for default slot", resolved.DefaultSlot.Provider)
		}
		d := RouteDecision{
			Slot:     resolved.DefaultSlot,
			Provider: provider,
			RoutedBy: "default",
			Message:  msg.Content,
			Details:  "single-slot agent",
		}
		r.getOrCreateStats(agentID).Record(d)
		return d, nil
	}

	// Build slot lookup
	slotMap := make(map[string]ModelSlot, len(slots))
	for _, s := range slots {
		slotMap[s.Name] = s
	}

	// --- Check routing methods ---
	var prefixResult PrefixResult
	if resolved.Config.TriggerPrefix {
		prefixResult = ParsePrefix(msg.Content, slots)
	}

	visionResult := CheckVisionRoute(msg.Attachments, slots)

	// --- Priority resolution ---
	var targetSlot ModelSlot
	var routedBy string
	var details string
	var classifierCost ClassifierCostInfo
	strippedMessage := msg.Content

	switch {
	case prefixResult.Matched:
		// Explicit intent always wins
		targetSlot = slotMap[prefixResult.SlotName]
		routedBy = "prefix"
		strippedMessage = prefixResult.Message
		details = fmt.Sprintf("prefix trigger: %s", prefixResult.SlotName)

	case visionResult.ShouldRoute:
		targetSlot = slotMap[visionResult.SlotName]
		routedBy = "vision"
		details = fmt.Sprintf("vision auto-route: %s", visionResult.Reason)

	case resolved.Config.AutoRoute && resolved.Config.ClassifierSlot != "":
		classifierSlot, classifierFound := slotMap[resolved.Config.ClassifierSlot]
		if classifierFound {
			classifierProvider, provOK := r.registry.GetProviderForSlot(classifierSlot)
			if provOK {
				classResult, classErr := r.classifier.Classify(
					ctx, agentID, msg.Content, recentHistory,
					slots, classifierProvider, classifierSlot.Model,
				)
				if classErr != nil {
					slog.Warn("classifier failed, using default slot", "error", classErr)
					targetSlot = resolved.DefaultSlot
					routedBy = "default"
					details = fmt.Sprintf("classifier error, fallback to default: %v", classErr)
				} else {
					classifierCost = ClassifierCostInfo{
						TokensIn:  classResult.TokensIn,
						TokensOut: classResult.TokensOut,
						Cost:      classResult.Cost,
						Model:     classifierSlot.Model,
						SlotName:  resolved.Config.ClassifierSlot,
						Provider:  classifierProvider.Name(),
					}

					targetName := classResult.SlotName
					if classResult.Confidence == "low" {
						if resolved.Config.FallbackSlot != "" {
							targetName = resolved.Config.FallbackSlot
						} else {
							targetName = resolved.Config.DefaultSlot
						}
					}

					if s, ok := slotMap[targetName]; ok {
						targetSlot = s
					} else {
						targetSlot = resolved.DefaultSlot
					}
					routedBy = "classifier"
					details = fmt.Sprintf("auto-classified: %s (confidence: %s, reason: %s)",
						classResult.SlotName, classResult.Confidence, classResult.Reason)
				}
			} else {
				targetSlot = resolved.DefaultSlot
				routedBy = "default"
				details = "classifier provider unavailable, using default"
			}
		} else {
			targetSlot = resolved.DefaultSlot
			routedBy = "default"
			details = "classifier slot not found, using default"
		}

	default:
		targetSlot = resolved.DefaultSlot
		routedBy = "default"
		details = "no routing method matched"
	}

	// --- Provider resolution ---
	provider, ok := r.registry.GetProviderForSlot(targetSlot)
	if !ok {
		slog.Warn("provider unavailable for routed slot, falling back to default",
			"slot", targetSlot.Name, "provider", targetSlot.Provider)
		targetSlot = resolved.DefaultSlot
		details = fmt.Sprintf("provider %q unavailable for slot %q, fallback to default",
			targetSlot.Provider, targetSlot.Name)

		provider, ok = r.registry.GetProviderForSlot(resolved.DefaultSlot)
		if !ok {
			return RouteDecision{}, fmt.Errorf("provider %q not found for default slot", resolved.DefaultSlot.Provider)
		}
	}

	d := RouteDecision{
		Slot:           targetSlot,
		Provider:       provider,
		RoutedBy:       routedBy,
		Message:        strippedMessage,
		Details:        details,
		ClassifierCost: classifierCost,
	}

	r.getOrCreateStats(agentID).Record(d)
	return d, nil
}

// Stats returns a snapshot of routing stats for the given agent.
func (r *Router) Stats(agentID string) RoutingStats {
	return r.getOrCreateStats(agentID).Snapshot()
}

// ResetStats zeroes all per-agent routing counters.
func (r *Router) ResetStats() {
	r.statsMu.Lock()
	defer r.statsMu.Unlock()
	for _, s := range r.stats {
		s.Reset()
	}
}

func (r *Router) getOrCreateStats(agentID string) *RoutingStats {
	r.statsMu.Lock()
	defer r.statsMu.Unlock()
	s, ok := r.stats[agentID]
	if !ok {
		s = NewRoutingStats()
		r.stats[agentID] = s
	}
	return s
}
