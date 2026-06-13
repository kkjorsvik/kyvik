package restapi

import (
	"testing"
)

func TestRateLimiter_AllowUnderLimit(t *testing.T) {
	r := NewRateLimiterSet()
	for i := 0; i < 5; i++ {
		if !r.Allow("agent1", "ep1", 60) {
			t.Fatalf("request %d should be allowed", i)
		}
	}
}

func TestRateLimiter_ExceedLimit(t *testing.T) {
	r := NewRateLimiterSet()
	// Use a very low RPM to trigger limiting quickly.
	rpm := 2
	allowed := 0
	for i := 0; i < 10; i++ {
		if r.Allow("agent1", "ep1", rpm) {
			allowed++
		}
	}
	if allowed >= 10 {
		t.Fatalf("should have rate-limited some requests, all %d allowed", allowed)
	}
	if allowed < 1 {
		t.Fatal("at least the first request should be allowed")
	}
}

func TestRateLimiter_ZeroRPM(t *testing.T) {
	r := NewRateLimiterSet()
	for i := 0; i < 100; i++ {
		if !r.Allow("agent1", "ep1", 0) {
			t.Fatalf("zero RPM should always allow, failed at request %d", i)
		}
	}
}

func TestRateLimiter_PerAgentIsolation(t *testing.T) {
	r := NewRateLimiterSet()
	// Exhaust agent1's bucket.
	rpm := 2
	for i := 0; i < 10; i++ {
		r.Allow("agent1", "ep1", rpm)
	}

	// agent2 should still have tokens.
	if !r.Allow("agent2", "ep1", rpm) {
		t.Fatal("agent2 should have its own bucket")
	}
}

func TestRateLimiter_PerEndpointIsolation(t *testing.T) {
	r := NewRateLimiterSet()
	rpm := 2
	for i := 0; i < 10; i++ {
		r.Allow("agent1", "ep1", rpm)
	}

	// Same agent, different endpoint should still have tokens.
	if !r.Allow("agent1", "ep2", rpm) {
		t.Fatal("different endpoint should have its own bucket")
	}
}
