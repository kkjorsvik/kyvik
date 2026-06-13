package router

import (
	"maps"
	"sync"
)

// RoutingStats tracks per-agent routing counters. All methods are safe for
// concurrent use.
type RoutingStats struct {
	TotalRouted    int64            `json:"total_routed"`
	ByMethod       map[string]int64 `json:"by_method"`
	BySlot         map[string]int64 `json:"by_slot"`
	ClassifierCost float64          `json:"classifier_cost"`
	mu             sync.Mutex
}

// NewRoutingStats creates a RoutingStats with initialised maps.
func NewRoutingStats() *RoutingStats {
	return &RoutingStats{
		ByMethod: make(map[string]int64),
		BySlot:   make(map[string]int64),
	}
}

// Record increments counters from a routing decision.
func (s *RoutingStats) Record(d RouteDecision) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.TotalRouted++
	s.ByMethod[d.RoutedBy]++
	s.BySlot[d.Slot.Name]++
	s.ClassifierCost += d.ClassifierCost.Cost
}

// Snapshot returns a deep copy safe for reading without holding the lock.
func (s *RoutingStats) Snapshot() RoutingStats {
	s.mu.Lock()
	defer s.mu.Unlock()

	byMethod := make(map[string]int64, len(s.ByMethod))
	maps.Copy(byMethod, s.ByMethod)
	bySlot := make(map[string]int64, len(s.BySlot))
	maps.Copy(bySlot, s.BySlot)

	return RoutingStats{
		TotalRouted:    s.TotalRouted,
		ByMethod:       byMethod,
		BySlot:         bySlot,
		ClassifierCost: s.ClassifierCost,
	}
}

// Reset zeroes all counters and reinitialises maps.
func (s *RoutingStats) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.TotalRouted = 0
	s.ByMethod = make(map[string]int64)
	s.BySlot = make(map[string]int64)
	s.ClassifierCost = 0
}
