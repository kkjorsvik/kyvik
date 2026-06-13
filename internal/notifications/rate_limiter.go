package notifications

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type rateLimitKey struct {
	eventType string
	agent     string
}

type rateLimitEntry struct {
	firstSeen  time.Time
	suppressed int
}

// RateLimiter prevents notification flooding by allowing at most one
// notification per (eventType, agent) pair within a configurable window.
type RateLimiter struct {
	mu       sync.Mutex
	entries  map[rateLimitKey]*rateLimitEntry
	window   time.Duration
	notifier Notifier // for sending suppression summaries
	stopCh   chan struct{}
	done     chan struct{}
}

// NewRateLimiter creates a RateLimiter that allows one event per key per window.
// The notifier is used to send suppression summary messages.
func NewRateLimiter(window time.Duration, notifier Notifier) *RateLimiter {
	rl := &RateLimiter{
		entries:  make(map[rateLimitKey]*rateLimitEntry),
		window:   window,
		notifier: notifier,
		stopCh:   make(chan struct{}),
		done:     make(chan struct{}),
	}
	go rl.run()
	return rl
}

// Allow returns true if the event should be sent, false if suppressed.
// Suppressed events increment a counter for later summary reporting.
func (rl *RateLimiter) Allow(eventType, agent string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	key := rateLimitKey{eventType: eventType, agent: agent}
	entry, exists := rl.entries[key]

	now := time.Now()
	if !exists || now.Sub(entry.firstSeen) >= rl.window {
		// Window expired or first occurrence — allow and start new window.
		rl.entries[key] = &rateLimitEntry{firstSeen: now}
		return true
	}

	// Within window — suppress.
	entry.suppressed++
	return false
}

// DrainSummaries sends summary notifications for any expired windows that
// had suppressed events, then removes those entries.
func (rl *RateLimiter) DrainSummaries() {
	rl.mu.Lock()
	now := time.Now()
	var summaries []Event
	for key, entry := range rl.entries {
		if now.Sub(entry.firstSeen) >= rl.window && entry.suppressed > 0 {
			summaries = append(summaries, Event{
				Type:      key.eventType,
				Severity:  "info",
				Agent:     key.agent,
				Title:     fmt.Sprintf("%d additional %s events suppressed", entry.suppressed, key.eventType),
				Detail:    fmt.Sprintf("Rate limiter suppressed %d events in the last %s", entry.suppressed, rl.window),
				Timestamp: now,
			})
			delete(rl.entries, key)
		} else if now.Sub(entry.firstSeen) >= rl.window {
			// Expired with no suppressed events — just clean up.
			delete(rl.entries, key)
		}
	}
	rl.mu.Unlock()

	for _, s := range summaries {
		_ = rl.notifier.Send(context.Background(), s)
	}
}

// Stop shuts down the background goroutine and performs a final drain.
func (rl *RateLimiter) Stop() {
	close(rl.stopCh)
	<-rl.done
	rl.DrainSummaries()
}

func (rl *RateLimiter) run() {
	defer close(rl.done)
	ticker := time.NewTicker(rl.window / 2)
	defer ticker.Stop()

	for {
		select {
		case <-rl.stopCh:
			return
		case <-ticker.C:
			rl.DrainSummaries()
		}
	}
}
