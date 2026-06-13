package restapi

import (
	"crypto/sha256"
	"fmt"
	"sync"
	"time"
)

type cacheEntry struct {
	result   map[string]any
	deadline time.Time
}

// ResponseCache is a concurrent-safe in-memory cache with TTL-based expiry.
type ResponseCache struct {
	mu      sync.RWMutex
	entries map[string]cacheEntry
	stop    chan struct{}
}

// NewResponseCache creates a new cache and starts a background eviction goroutine.
func NewResponseCache() *ResponseCache {
	c := &ResponseCache{
		entries: make(map[string]cacheEntry),
		stop:    make(chan struct{}),
	}
	go c.evictLoop()
	return c
}

// CacheKey produces a deterministic key from agent ID, endpoint name, and rendered URL.
func CacheKey(agentID, endpointName, renderedURL string) string {
	h := sha256.Sum256([]byte(agentID + ":" + endpointName + ":" + renderedURL))
	return fmt.Sprintf("%x", h)
}

// Get retrieves a cached result. Returns nil if not found or expired.
func (c *ResponseCache) Get(key string) map[string]any {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.entries[key]
	if !ok {
		return nil
	}
	if time.Now().After(entry.deadline) {
		return nil
	}
	// Return a copy to avoid concurrent mutation.
	result := make(map[string]any, len(entry.result))
	for k, v := range entry.result {
		result[k] = v
	}
	return result
}

// Set stores a result with the given TTL.
func (c *ResponseCache) Set(key string, result map[string]any, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = cacheEntry{
		result:   result,
		deadline: time.Now().Add(ttl),
	}
}

// Stop terminates the background eviction goroutine.
func (c *ResponseCache) Stop() {
	select {
	case <-c.stop:
	default:
		close(c.stop)
	}
}

func (c *ResponseCache) evictLoop() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-ticker.C:
			c.evict()
		}
	}
}

func (c *ResponseCache) evict() {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, entry := range c.entries {
		if now.After(entry.deadline) {
			delete(c.entries, k)
		}
	}
}
