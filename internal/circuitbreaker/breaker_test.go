package circuitbreaker

import (
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

func defaultConfig() types.CircuitBreakerConfig {
	return types.DefaultCircuitBreakerConfig()
}

func TestErrorRateTrips(t *testing.T) {
	cfg := defaultConfig()
	cfg.ErrorThreshold = 3
	cfg.ErrorWindowMinutes = 10
	b := NewBreaker("agent-1", cfg, 0, "")

	// First two failures — no trip.
	if trip := b.RecordToolCall("tool", "act", false, false); trip != nil {
		t.Fatalf("unexpected trip after 1 error: %v", trip)
	}
	if trip := b.RecordToolCall("tool", "act", false, false); trip != nil {
		t.Fatalf("unexpected trip after 2 errors: %v", trip)
	}
	// Third failure — should trip.
	trip := b.RecordToolCall("tool", "act", false, false)
	if trip == nil {
		t.Fatal("expected trip after 3 errors, got nil")
	}
	if trip.Trigger != "error_rate" {
		t.Fatalf("expected trigger 'error_rate', got %q", trip.Trigger)
	}
}

func TestErrorsExpireOutsideWindow(t *testing.T) {
	cfg := defaultConfig()
	cfg.ErrorThreshold = 3
	cfg.ErrorWindowMinutes = 1
	b := NewBreaker("agent-1", cfg, 0, "")

	// Record 2 errors.
	b.RecordToolCall("tool", "act", false, false)
	b.RecordToolCall("tool", "act", false, false)

	// Manually expire old errors by backdating them.
	b.mu.Lock()
	past := time.Now().Add(-2 * time.Minute)
	b.errorTimes = []time.Time{past, past}
	b.mu.Unlock()

	// Third error should NOT trip because the first 2 are expired.
	trip := b.RecordToolCall("tool", "act", false, false)
	if trip != nil {
		t.Fatalf("unexpected trip — old errors should have expired: %v", trip)
	}
}

func TestSuccessDoesNotCountAsError(t *testing.T) {
	cfg := defaultConfig()
	cfg.ErrorThreshold = 2
	b := NewBreaker("agent-1", cfg, 0, "")

	// Successful calls should not contribute to error count.
	for i := 0; i < 10; i++ {
		if trip := b.RecordToolCall("tool", "act", true, false); trip != nil {
			t.Fatalf("unexpected trip on success: %v", trip)
		}
	}
}

func TestSpendingVelocityTrips(t *testing.T) {
	cfg := defaultConfig()
	cfg.SpendingVelocityPct = 50
	cfg.SpendingWindowMinutes = 5
	b := NewBreaker("agent-1", cfg, 10.0, "") // $10 daily budget

	// Threshold is 50% of $10 = $5 in the window.
	if trip := b.RecordSpending(4.0); trip != nil {
		t.Fatalf("unexpected trip at $4: %v", trip)
	}
	// This pushes total to $5.50 > $5.00 threshold.
	trip := b.RecordSpending(1.50)
	if trip == nil {
		t.Fatal("expected spending velocity trip, got nil")
	}
	if trip.Trigger != "spending_velocity" {
		t.Fatalf("expected trigger 'spending_velocity', got %q", trip.Trigger)
	}
}

func TestSpendingVelocitySkippedWithZeroBudget(t *testing.T) {
	cfg := defaultConfig()
	cfg.SpendingVelocityPct = 50
	b := NewBreaker("agent-1", cfg, 0, "") // No daily budget.

	// Even very high spending should not trip.
	for i := 0; i < 100; i++ {
		if trip := b.RecordSpending(100.0); trip != nil {
			t.Fatalf("unexpected trip with zero budget: %v", trip)
		}
	}
}

func TestActionRateTrips(t *testing.T) {
	cfg := defaultConfig()
	cfg.ActionRatePerMinute = 5
	b := NewBreaker("agent-1", cfg, 0, "")

	// 5 calls within a minute — no trip.
	for i := 0; i < 5; i++ {
		if trip := b.RecordToolCall("tool", "act", true, false); trip != nil {
			t.Fatalf("unexpected trip at call %d: %v", i+1, trip)
		}
	}
	// 6th call should trip.
	trip := b.RecordToolCall("tool", "act", true, false)
	if trip == nil {
		t.Fatal("expected action rate trip, got nil")
	}
	if trip.Trigger != "action_rate" {
		t.Fatalf("expected trigger 'action_rate', got %q", trip.Trigger)
	}
}

func TestDestructiveLimitTrips(t *testing.T) {
	cfg := defaultConfig()
	cfg.DestructiveLimit = 3
	// Set high action rate so it doesn't trip first.
	cfg.ActionRatePerMinute = 100
	b := NewBreaker("agent-1", cfg, 0, "")

	// 3 destructive calls — no trip (threshold is > 3).
	for i := 0; i < 3; i++ {
		if trip := b.RecordToolCall("tool", "delete", true, true); trip != nil {
			t.Fatalf("unexpected trip at destructive call %d: %v", i+1, trip)
		}
	}
	// 4th destructive call should trip.
	trip := b.RecordToolCall("tool", "delete", true, true)
	if trip == nil {
		t.Fatal("expected destructive limit trip, got nil")
	}
	if trip.Trigger != "destructive_limit" {
		t.Fatalf("expected trigger 'destructive_limit', got %q", trip.Trigger)
	}
}

func TestLoopDetectionIdentical(t *testing.T) {
	cfg := defaultConfig()
	cfg.LoopIdenticalCount = 3
	b := NewBreaker("agent-1", cfg, 0, "")

	msg := "I'm going to try this tool again"

	// First 2 identical messages — no trip.
	if trip := b.RecordMessage(msg); trip != nil {
		t.Fatalf("unexpected trip after 1 message: %v", trip)
	}
	if trip := b.RecordMessage(msg); trip != nil {
		t.Fatalf("unexpected trip after 2 messages: %v", trip)
	}
	// Third identical message — should trip.
	trip := b.RecordMessage(msg)
	if trip == nil {
		t.Fatal("expected loop detection trip, got nil")
	}
	if trip.Trigger != "loop_detection" {
		t.Fatalf("expected trigger 'loop_detection', got %q", trip.Trigger)
	}
}

func TestLoopDetectionNormalization(t *testing.T) {
	cfg := defaultConfig()
	cfg.LoopIdenticalCount = 3
	b := NewBreaker("agent-1", cfg, 0, "")

	// Different whitespace/casing should normalize to same hash.
	b.RecordMessage("Hello   World")
	b.RecordMessage("hello world")
	trip := b.RecordMessage("  HELLO   WORLD  ")
	if trip == nil {
		t.Fatal("expected loop detection trip after normalized-identical messages, got nil")
	}
}

func TestLoopDetectionVariedMessages(t *testing.T) {
	cfg := defaultConfig()
	cfg.LoopIdenticalCount = 3
	b := NewBreaker("agent-1", cfg, 0, "")

	// Varied messages should never trip.
	messages := []string{
		"I'm analyzing the data",
		"The results show an increase",
		"Let me check the trends",
		"Here is the final summary",
		"The data is inconclusive",
	}
	for _, msg := range messages {
		if trip := b.RecordMessage(msg); trip != nil {
			t.Fatalf("unexpected trip on varied messages: %v", trip)
		}
	}
}

func TestDefaultConfigValues(t *testing.T) {
	cfg := types.DefaultCircuitBreakerConfig()

	if !cfg.Enabled {
		t.Error("expected default Enabled=true")
	}
	if cfg.ErrorThreshold != 5 {
		t.Errorf("expected ErrorThreshold=5, got %d", cfg.ErrorThreshold)
	}
	if cfg.ErrorWindowMinutes != 10 {
		t.Errorf("expected ErrorWindowMinutes=10, got %d", cfg.ErrorWindowMinutes)
	}
	if cfg.SpendingVelocityPct != 50 {
		t.Errorf("expected SpendingVelocityPct=50, got %d", cfg.SpendingVelocityPct)
	}
	if cfg.SpendingWindowMinutes != 5 {
		t.Errorf("expected SpendingWindowMinutes=5, got %d", cfg.SpendingWindowMinutes)
	}
	if cfg.ActionRatePerMinute != 30 {
		t.Errorf("expected ActionRatePerMinute=30, got %d", cfg.ActionRatePerMinute)
	}
	if cfg.DestructiveLimit != 5 {
		t.Errorf("expected DestructiveLimit=5, got %d", cfg.DestructiveLimit)
	}
	if cfg.LoopIdenticalCount != 3 {
		t.Errorf("expected LoopIdenticalCount=3, got %d", cfg.LoopIdenticalCount)
	}
}

func TestDisabledBreakerNeverTrips(t *testing.T) {
	cfg := defaultConfig()
	cfg.Enabled = false
	cfg.ErrorThreshold = 1
	cfg.ActionRatePerMinute = 1
	cfg.DestructiveLimit = 1
	cfg.LoopIdenticalCount = 1
	b := NewBreaker("agent-1", cfg, 10.0, "")

	// None of these should trip when disabled.
	if trip := b.RecordToolCall("tool", "act", false, true); trip != nil {
		t.Fatalf("disabled breaker tripped on RecordToolCall: %v", trip)
	}
	if trip := b.RecordSpending(100.0); trip != nil {
		t.Fatalf("disabled breaker tripped on RecordSpending: %v", trip)
	}
	if trip := b.RecordMessage("same"); trip != nil {
		t.Fatalf("disabled breaker tripped on RecordMessage: %v", trip)
	}
}

func TestBreakerStatusReflectsState(t *testing.T) {
	cfg := defaultConfig()
	cfg.ErrorThreshold = 2
	b := NewBreaker("agent-1", cfg, 0, "")

	status := b.Status()
	if status.Tripped {
		t.Error("expected not tripped initially")
	}
	if status.ErrorCount != 0 {
		t.Errorf("expected ErrorCount=0, got %d", status.ErrorCount)
	}

	b.RecordToolCall("tool", "act", false, false)
	status = b.Status()
	if status.ErrorCount != 1 {
		t.Errorf("expected ErrorCount=1, got %d", status.ErrorCount)
	}

	b.RecordToolCall("tool", "act", false, false)
	status = b.Status()
	if !status.Tripped {
		t.Error("expected tripped after threshold")
	}
	if status.TripResult == nil {
		t.Error("expected TripResult to be set")
	}
}

func TestTrippedBreakerIgnoresSubsequentRecords(t *testing.T) {
	cfg := defaultConfig()
	cfg.ErrorThreshold = 1
	b := NewBreaker("agent-1", cfg, 10.0, "")

	// Trip the breaker.
	trip := b.RecordToolCall("tool", "act", false, false)
	if trip == nil {
		t.Fatal("expected trip")
	}

	// Subsequent records should return nil (already tripped).
	if trip := b.RecordToolCall("tool", "act", false, false); trip != nil {
		t.Fatalf("tripped breaker returned non-nil on RecordToolCall: %v", trip)
	}
	if trip := b.RecordSpending(100.0); trip != nil {
		t.Fatalf("tripped breaker returned non-nil on RecordSpending: %v", trip)
	}
	if trip := b.RecordMessage("same"); trip != nil {
		t.Fatalf("tripped breaker returned non-nil on RecordMessage: %v", trip)
	}
}

func TestResolveConfigDefaults(t *testing.T) {
	config := types.AgentConfig{ID: "test"}
	resolved := ResolveConfig(config, types.CircuitBreakerConfig{})

	if !resolved.Enabled {
		t.Error("expected Enabled=true from defaults")
	}
	if resolved.ErrorThreshold != 5 {
		t.Errorf("expected ErrorThreshold=5, got %d", resolved.ErrorThreshold)
	}
}

func TestResolveConfigOverride(t *testing.T) {
	config := types.AgentConfig{
		ID:                 "test",
		CircuitBreakerJSON: `{"enabled":false,"error_threshold":10}`,
	}
	resolved := ResolveConfig(config, types.CircuitBreakerConfig{})

	if resolved.Enabled {
		t.Error("expected Enabled=false from override")
	}
	if resolved.ErrorThreshold != 10 {
		t.Errorf("expected ErrorThreshold=10, got %d", resolved.ErrorThreshold)
	}
	// Other fields should still be defaults.
	if resolved.ActionRatePerMinute != 30 {
		t.Errorf("expected ActionRatePerMinute=30, got %d", resolved.ActionRatePerMinute)
	}
}

func TestManagerOnTripCallback(t *testing.T) {
	var tripped []TripResult
	mgr := NewManager(func(tr TripResult) {
		tripped = append(tripped, tr)
	})

	config := types.AgentConfig{
		ID:                 "agent-1",
		CircuitBreakerJSON: `{"enabled":true,"error_threshold":1}`,
	}

	trip := mgr.RecordToolCall(config, "tool", "act", false, false)
	if trip == nil {
		t.Fatal("expected trip")
	}
	if len(tripped) != 1 {
		t.Fatalf("expected 1 onTrip callback, got %d", len(tripped))
	}
	if tripped[0].AgentID != "agent-1" {
		t.Errorf("expected agentID 'agent-1', got %q", tripped[0].AgentID)
	}
}

func TestManagerRemove(t *testing.T) {
	mgr := NewManager(nil)
	config := types.AgentConfig{ID: "agent-1"}
	mgr.GetOrCreate(config)

	status := mgr.Status("agent-1")
	if status.AgentID != "agent-1" {
		t.Errorf("expected agent-1 status, got %q", status.AgentID)
	}

	mgr.Remove("agent-1")
	status = mgr.Status("agent-1")
	// After removal, should return zero-value status.
	if status.Tripped {
		t.Error("expected not tripped after removal")
	}
}

func TestManagerRecreatesBreakerOnConfigChange(t *testing.T) {
	mgr := NewManager(nil)

	config := types.AgentConfig{
		ID:                 "agent-1",
		CircuitBreakerJSON: `{"enabled":true,"error_threshold":2}`,
	}

	// Create initial breaker and record 1 error.
	mgr.RecordToolCall(config, "tool", "act", false, false)
	status := mgr.Status("agent-1")
	if status.ErrorCount != 1 {
		t.Fatalf("expected ErrorCount=1, got %d", status.ErrorCount)
	}

	// Change config — threshold raised to 10.
	config.CircuitBreakerJSON = `{"enabled":true,"error_threshold":10}`

	// Next call should detect config change, recreate breaker (counters reset).
	mgr.RecordToolCall(config, "tool", "act", false, false)
	status = mgr.Status("agent-1")
	if status.ErrorCount != 1 {
		t.Fatalf("expected ErrorCount=1 after recreate, got %d", status.ErrorCount)
	}
}

func TestResolveConfigWithSystemDefaults(t *testing.T) {
	systemDefaults := types.CircuitBreakerConfig{
		Enabled:             true,
		ErrorThreshold:      20,
		ErrorWindowMinutes:  30,
		ActionRatePerMinute: 60,
		DestructiveLimit:    15,
	}

	config := types.AgentConfig{ID: "test"}
	resolved := ResolveConfig(config, systemDefaults)

	if resolved.ErrorThreshold != 20 {
		t.Errorf("expected ErrorThreshold=20 from system defaults, got %d", resolved.ErrorThreshold)
	}
	if resolved.ActionRatePerMinute != 60 {
		t.Errorf("expected ActionRatePerMinute=60 from system defaults, got %d", resolved.ActionRatePerMinute)
	}
	if resolved.SpendingVelocityPct != 50 {
		t.Errorf("expected SpendingVelocityPct=50 from hardcoded defaults, got %d", resolved.SpendingVelocityPct)
	}
}

func TestResolveConfigAgentOverridesSystemDefaults(t *testing.T) {
	systemDefaults := types.CircuitBreakerConfig{
		Enabled:          true,
		ErrorThreshold:   20,
		DestructiveLimit: 15,
	}

	config := types.AgentConfig{
		ID:                 "test",
		CircuitBreakerJSON: `{"enabled":true,"error_threshold":50}`,
	}
	resolved := ResolveConfig(config, systemDefaults)

	if resolved.ErrorThreshold != 50 {
		t.Errorf("expected ErrorThreshold=50 from agent override, got %d", resolved.ErrorThreshold)
	}
	if resolved.DestructiveLimit != 15 {
		t.Errorf("expected DestructiveLimit=15 from system defaults, got %d", resolved.DestructiveLimit)
	}
}

func TestManagerSystemDefaults(t *testing.T) {
	mgr := NewManager(nil)

	sysDefaults := types.CircuitBreakerConfig{
		Enabled:               true,
		ErrorThreshold:        20,
		ErrorWindowMinutes:    10,
		SpendingVelocityPct:   50,
		SpendingWindowMinutes: 5,
		ActionRatePerMinute:   100,
		DestructiveLimit:      50,
		LoopIdenticalCount:    3,
	}
	mgr.SetSystemDefaults(sysDefaults)

	config := types.AgentConfig{
		ID:                 "agent-1",
		CircuitBreakerJSON: `{"enabled":true}`,
	}

	// 30 calls should NOT trip (system default is 100/min).
	for i := 0; i < 30; i++ {
		if trip := mgr.RecordToolCall(config, "tool", "act", true, false); trip != nil {
			t.Fatalf("unexpected trip at call %d with system default 100/min: %v", i+1, trip)
		}
	}
}
