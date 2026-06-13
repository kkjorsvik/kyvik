package circuitbreaker

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

// TripResult describes why a circuit breaker tripped.
type TripResult struct {
	Trigger     string    // "error_rate", "spending_velocity", "action_rate", "destructive_limit", "loop_detection"
	Description string    // Human-readable explanation
	AgentID     string
	Timestamp   time.Time
}

// BreakerStatus is a read-only snapshot of a breaker's current metrics.
type BreakerStatus struct {
	AgentID          string
	ErrorCount       int
	ToolCallCount    int
	DestructiveCount int
	SpendingInWindow float64
	Tripped          bool
	TripResult       *TripResult
}

// spendingEntry tracks a single cost record with its timestamp.
type spendingEntry struct {
	cost      float64
	timestamp time.Time
}

// Breaker monitors a single agent's behavior against configured thresholds.
type Breaker struct {
	mu               sync.Mutex
	agentID          string
	config           types.CircuitBreakerConfig
	dailyBudget      float64
	configHash       string
	tripped          bool
	tripResult       *TripResult
	errorTimes       []time.Time
	toolCallTimes    []time.Time
	destructiveCount int
	spendingRecords  []spendingEntry
	messageHashes    []string
}

// NewBreaker creates a per-agent breaker with the given config and daily budget.
// The configHash identifies the configuration version so callers can detect changes.
func NewBreaker(agentID string, config types.CircuitBreakerConfig, dailyBudget float64, configHash string) *Breaker {
	return &Breaker{
		agentID:     agentID,
		config:      config,
		dailyBudget: dailyBudget,
		configHash:  configHash,
	}
}

// ConfigHash returns the hash of the configuration used to create this breaker.
func (b *Breaker) ConfigHash() string {
	return b.configHash
}

// RecordToolCall records a tool execution and checks error rate, action rate,
// and destructive limit thresholds. Returns a TripResult if any threshold is breached.
func (b *Breaker) RecordToolCall(toolName, action string, success, destructive bool) *TripResult {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.config.Enabled || b.tripped {
		return nil
	}

	now := time.Now()

	// Track tool call times for action rate.
	b.toolCallTimes = append(b.toolCallTimes, now)
	oneMinuteAgo := now.Add(-time.Minute)
	b.toolCallTimes = expireBefore(b.toolCallTimes, oneMinuteAgo)

	// Check action rate.
	if len(b.toolCallTimes) > b.config.ActionRatePerMinute {
		return b.trip("action_rate", fmt.Sprintf(
			"%d tool calls in last minute (threshold: %d/min)",
			len(b.toolCallTimes), b.config.ActionRatePerMinute,
		))
	}

	// Track errors.
	if !success {
		b.errorTimes = append(b.errorTimes, now)
		windowStart := now.Add(-time.Duration(b.config.ErrorWindowMinutes) * time.Minute)
		b.errorTimes = expireBefore(b.errorTimes, windowStart)

		// Check error rate.
		if len(b.errorTimes) >= b.config.ErrorThreshold {
			return b.trip("error_rate", fmt.Sprintf(
				"%d tool failures in %d minutes (threshold: %d in %dm)",
				len(b.errorTimes), b.config.ErrorWindowMinutes,
				b.config.ErrorThreshold, b.config.ErrorWindowMinutes,
			))
		}
	}

	// Track destructive calls.
	if destructive {
		b.destructiveCount++
		if b.destructiveCount > b.config.DestructiveLimit {
			return b.trip("destructive_limit", fmt.Sprintf(
				"%d destructive tool calls (threshold: %d per session)",
				b.destructiveCount, b.config.DestructiveLimit,
			))
		}
	}

	return nil
}

// RecordSpending records a cost and checks spending velocity against daily budget.
// Skipped when dailyBudget is 0 (no limit configured).
func (b *Breaker) RecordSpending(cost float64) *TripResult {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.config.Enabled || b.tripped || b.dailyBudget <= 0 {
		return nil
	}

	now := time.Now()
	b.spendingRecords = append(b.spendingRecords, spendingEntry{cost: cost, timestamp: now})

	// Expire records outside the spending window.
	windowStart := now.Add(-time.Duration(b.config.SpendingWindowMinutes) * time.Minute)
	filtered := b.spendingRecords[:0]
	for _, r := range b.spendingRecords {
		if !r.timestamp.Before(windowStart) {
			filtered = append(filtered, r)
		}
	}
	b.spendingRecords = filtered

	// Sum spending in window.
	var total float64
	for _, r := range b.spendingRecords {
		total += r.cost
	}

	// Check if spending exceeds threshold percentage of daily budget.
	threshold := b.dailyBudget * float64(b.config.SpendingVelocityPct) / 100.0
	if total > threshold {
		return b.trip("spending_velocity", fmt.Sprintf(
			"$%.4f spent in %d minutes (threshold: %d%% of $%.2f daily budget = $%.4f)",
			total, b.config.SpendingWindowMinutes,
			b.config.SpendingVelocityPct, b.dailyBudget, threshold,
		))
	}

	return nil
}

// RecordMessage records a model response and checks for repetitive loop patterns.
// The content is normalized (lowercased, whitespace collapsed) and SHA-256 hashed.
func (b *Breaker) RecordMessage(content string) *TripResult {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.config.Enabled || b.tripped {
		return nil
	}

	// Normalize and hash the content.
	normalized := strings.Join(strings.Fields(strings.ToLower(content)), " ")
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(normalized)))

	b.messageHashes = append(b.messageHashes, hash)

	// Keep only the last N hashes (ring buffer behavior).
	maxKeep := b.config.LoopIdenticalCount
	if len(b.messageHashes) > maxKeep {
		b.messageHashes = b.messageHashes[len(b.messageHashes)-maxKeep:]
	}

	// Check if all N hashes are identical.
	if len(b.messageHashes) >= maxKeep {
		allSame := true
		first := b.messageHashes[0]
		for _, h := range b.messageHashes[1:] {
			if h != first {
				allSame = false
				break
			}
		}
		if allSame {
			return b.trip("loop_detection", fmt.Sprintf(
				"last %d model responses are identical (possible infinite loop)",
				maxKeep,
			))
		}
	}

	return nil
}

// Status returns a read-only snapshot of the breaker's current metrics.
func (b *Breaker) Status() BreakerStatus {
	b.mu.Lock()
	defer b.mu.Unlock()

	var spendingTotal float64
	for _, r := range b.spendingRecords {
		spendingTotal += r.cost
	}

	return BreakerStatus{
		AgentID:          b.agentID,
		ErrorCount:       len(b.errorTimes),
		ToolCallCount:    len(b.toolCallTimes),
		DestructiveCount: b.destructiveCount,
		SpendingInWindow: spendingTotal,
		Tripped:          b.tripped,
		TripResult:       b.tripResult,
	}
}

// trip marks the breaker as tripped and returns the TripResult. Caller must hold mu.
func (b *Breaker) trip(trigger, description string) *TripResult {
	result := &TripResult{
		Trigger:     trigger,
		Description: description,
		AgentID:     b.agentID,
		Timestamp:   time.Now(),
	}
	b.tripped = true
	b.tripResult = result
	return result
}

// expireBefore removes entries older than cutoff from a time slice.
func expireBefore(times []time.Time, cutoff time.Time) []time.Time {
	filtered := times[:0]
	for _, t := range times {
		if !t.Before(cutoff) {
			filtered = append(filtered, t)
		}
	}
	return filtered
}
