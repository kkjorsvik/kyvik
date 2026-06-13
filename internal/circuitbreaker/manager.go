package circuitbreaker

import (
	"crypto/sha256"
	"fmt"
	"sync"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

// Manager manages per-agent circuit breakers and fires a callback on trip.
type Manager struct {
	mu             sync.RWMutex
	breakers       map[string]*Breaker
	onTrip         func(TripResult)
	systemDefaults types.CircuitBreakerConfig
}

// NewManager creates a Manager. The onTrip callback is invoked (in the caller's
// goroutine) whenever an agent's breaker trips.
func NewManager(onTrip func(TripResult)) *Manager {
	return &Manager{
		breakers: make(map[string]*Breaker),
		onTrip:   onTrip,
	}
}

// SetSystemDefaults updates the system-wide circuit breaker defaults.
func (m *Manager) SetSystemDefaults(cfg types.CircuitBreakerConfig) {
	m.mu.Lock()
	m.systemDefaults = cfg
	m.mu.Unlock()
}

// SystemDefaults returns the current system-wide circuit breaker defaults.
func (m *Manager) SystemDefaults() types.CircuitBreakerConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.systemDefaults
}

// GetOrCreate returns the breaker for an agent, creating one if it doesn't exist.
// If the agent's CircuitBreakerJSON has changed since the breaker was created,
// the old breaker is discarded and a new one is created with fresh counters.
func (m *Manager) GetOrCreate(config types.AgentConfig) *Breaker {
	hash := configHash(config.CircuitBreakerJSON)

	m.mu.RLock()
	b, ok := m.breakers[config.ID]
	m.mu.RUnlock()
	if ok && b.ConfigHash() == hash {
		return b
	}

	// Double-check under write lock.
	m.mu.Lock()
	defer m.mu.Unlock()
	if b, ok = m.breakers[config.ID]; ok && b.ConfigHash() == hash {
		return b
	}

	cbConfig := ResolveConfig(config, m.systemDefaults)
	b = NewBreaker(config.ID, cbConfig, config.Limits.MaxSpendPerDay, hash)
	m.breakers[config.ID] = b
	return b
}

// configHash returns a hex-encoded SHA-256 hash of the given JSON string.
func configHash(json string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(json)))
}

// RecordToolCall delegates to the agent's breaker and fires onTrip if tripped.
func (m *Manager) RecordToolCall(config types.AgentConfig, toolName, action string, success, destructive bool) *TripResult {
	b := m.GetOrCreate(config)
	trip := b.RecordToolCall(toolName, action, success, destructive)
	if trip != nil && m.onTrip != nil {
		m.onTrip(*trip)
	}
	return trip
}

// RecordSpending delegates to the agent's breaker and fires onTrip if tripped.
func (m *Manager) RecordSpending(config types.AgentConfig, cost float64) *TripResult {
	b := m.GetOrCreate(config)
	trip := b.RecordSpending(cost)
	if trip != nil && m.onTrip != nil {
		m.onTrip(*trip)
	}
	return trip
}

// RecordMessage delegates to the agent's breaker and fires onTrip if tripped.
func (m *Manager) RecordMessage(config types.AgentConfig, content string) *TripResult {
	b := m.GetOrCreate(config)
	trip := b.RecordMessage(content)
	if trip != nil && m.onTrip != nil {
		m.onTrip(*trip)
	}
	return trip
}

// Status returns the breaker status for an agent, or a zero-value status if not tracked.
func (m *Manager) Status(agentID string) BreakerStatus {
	m.mu.RLock()
	b, ok := m.breakers[agentID]
	m.mu.RUnlock()
	if !ok {
		return BreakerStatus{AgentID: agentID}
	}
	return b.Status()
}

// Remove deletes a breaker when an agent is stopped, freeing memory.
func (m *Manager) Remove(agentID string) {
	m.mu.Lock()
	delete(m.breakers, agentID)
	m.mu.Unlock()
}
