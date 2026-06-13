package restapi

import (
	"sync"
	"time"
)

type bucket struct {
	tokens    float64
	lastCheck time.Time
}

// RateLimiterSet manages per-agent-per-endpoint token bucket rate limiters.
type RateLimiterSet struct {
	mu      sync.Mutex
	buckets map[string]*bucket
}

// NewRateLimiterSet creates a new rate limiter set.
func NewRateLimiterSet() *RateLimiterSet {
	return &RateLimiterSet{
		buckets: make(map[string]*bucket),
	}
}

// Allow checks whether a request is permitted under the given rate limit.
// rpm=0 means no limit (always allowed).
// Returns true if the request is allowed, false if rate-limited.
func (r *RateLimiterSet) Allow(agentID, endpointName string, rpm int) bool {
	if rpm <= 0 {
		return true
	}

	key := agentID + ":" + endpointName
	r.mu.Lock()
	defer r.mu.Unlock()

	b, ok := r.buckets[key]
	if !ok {
		b = &bucket{
			tokens:    float64(rpm) - 1, // consume one token immediately
			lastCheck: time.Now(),
		}
		r.buckets[key] = b
		return true
	}

	// Refill tokens based on elapsed time.
	now := time.Now()
	elapsed := now.Sub(b.lastCheck).Seconds()
	b.lastCheck = now

	tokensPerSecond := float64(rpm) / 60.0
	b.tokens += elapsed * tokensPerSecond
	if b.tokens > float64(rpm) {
		b.tokens = float64(rpm)
	}

	if b.tokens < 1 {
		return false
	}

	b.tokens--
	return true
}
