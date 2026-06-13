package memory

import (
	"math"
	"time"
)

func evictionScore(mem Memory, now time.Time) float64 {
	ageHours := now.Sub(mem.AccessedAt).Hours()
	recency := math.Exp(-ageHours / 720)
	accessCount := float64(mem.AccessCount)
	if accessCount > 20 {
		accessCount = 20
	}
	frequency := accessCount / 20.0
	catWeight := evictionCategoryWeight(mem.Category)
	return 0.45*recency + 0.35*frequency + 0.20*catWeight
}

// evictionCategoryWeight is named differently from retrieval.go's categoryWeight
// to avoid redeclaration in the same package.
func evictionCategoryWeight(category string) float64 {
	switch category {
	case CategoryInstruction:
		return 1.0
	case CategoryFact:
		return 0.8
	case CategoryDecision:
		return 0.6
	case CategoryContext:
		return 0.4
	default:
		return 0.5
	}
}

func findEvictionTarget(memories []Memory, now time.Time) int64 {
	var lowestID int64 = -1
	lowestScore := math.MaxFloat64
	for _, m := range memories {
		if m.Pinned {
			continue
		}
		score := evictionScore(m, now)
		if score < lowestScore {
			lowestScore = score
			lowestID = m.ID
		}
	}
	return lowestID
}
