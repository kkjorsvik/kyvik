package memory

import (
	"testing"
	"time"
)

func TestEvictionScore(t *testing.T) {
	now := time.Now()
	high := Memory{
		Category:    CategoryInstruction,
		CreatedAt:   now.Add(-1 * time.Hour),
		AccessedAt:  now.Add(-1 * time.Hour),
		AccessCount: 15,
	}
	low := Memory{
		Category:    CategoryContext,
		CreatedAt:   now.Add(-60 * 24 * time.Hour),
		AccessedAt:  now.Add(-60 * 24 * time.Hour),
		AccessCount: 0,
	}
	highScore := evictionScore(high, now)
	lowScore := evictionScore(low, now)
	if highScore <= lowScore {
		t.Errorf("high-value memory scored %f <= low-value %f", highScore, lowScore)
	}
}

func TestFindEvictionTarget(t *testing.T) {
	now := time.Now()
	memories := []Memory{
		{ID: 1, Category: CategoryFact, CreatedAt: now.Add(-1 * time.Hour), AccessedAt: now.Add(-1 * time.Hour), AccessCount: 10, Pinned: false},
		{ID: 2, Category: CategoryContext, CreatedAt: now.Add(-30 * 24 * time.Hour), AccessedAt: now.Add(-30 * 24 * time.Hour), AccessCount: 0, Pinned: false},
		{ID: 3, Category: CategoryInstruction, CreatedAt: now.Add(-2 * time.Hour), AccessedAt: now.Add(-2 * time.Hour), AccessCount: 5, Pinned: true},
	}
	target := findEvictionTarget(memories, now)
	if target != 2 {
		t.Errorf("expected memory ID 2 (oldest, lowest access), got %d", target)
	}
}

func TestFindEvictionTarget_AllPinned(t *testing.T) {
	now := time.Now()
	memories := []Memory{
		{ID: 1, Pinned: true},
		{ID: 2, Pinned: true},
	}
	target := findEvictionTarget(memories, now)
	if target != -1 {
		t.Errorf("expected -1 (no eviction target), got %d", target)
	}
}
