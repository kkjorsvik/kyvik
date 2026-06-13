package restapi

import (
	"testing"
	"time"
)

func TestCacheSetGet(t *testing.T) {
	c := NewResponseCache()
	defer c.Stop()

	result := map[string]any{"status": 200, "body": "ok"}
	c.Set("key1", result, 5*time.Second)

	got := c.Get("key1")
	if got == nil {
		t.Fatal("expected cached result, got nil")
	}
	if got["status"] != 200 {
		t.Fatalf("status = %v", got["status"])
	}
}

func TestCacheMiss(t *testing.T) {
	c := NewResponseCache()
	defer c.Stop()

	got := c.Get("nonexistent")
	if got != nil {
		t.Fatalf("expected nil for cache miss, got %v", got)
	}
}

func TestCacheTTLExpiry(t *testing.T) {
	c := NewResponseCache()
	defer c.Stop()

	result := map[string]any{"status": 200}
	c.Set("key1", result, 1*time.Millisecond)

	time.Sleep(5 * time.Millisecond)

	got := c.Get("key1")
	if got != nil {
		t.Fatalf("expected nil after TTL expiry, got %v", got)
	}
}

func TestCacheKeyDeterministic(t *testing.T) {
	k1 := CacheKey("agent1", "weather", "https://api.example.com/weather?city=oslo")
	k2 := CacheKey("agent1", "weather", "https://api.example.com/weather?city=oslo")
	k3 := CacheKey("agent2", "weather", "https://api.example.com/weather?city=oslo")

	if k1 != k2 {
		t.Fatal("same inputs should produce same key")
	}
	if k1 == k3 {
		t.Fatal("different agent IDs should produce different keys")
	}
}

func TestCacheGetReturnsCopy(t *testing.T) {
	c := NewResponseCache()
	defer c.Stop()

	result := map[string]any{"status": 200}
	c.Set("key1", result, 5*time.Second)

	got := c.Get("key1")
	got["mutated"] = true

	got2 := c.Get("key1")
	if _, ok := got2["mutated"]; ok {
		t.Fatal("cache should return copies, not references")
	}
}
