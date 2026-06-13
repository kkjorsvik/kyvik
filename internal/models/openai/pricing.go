package openai

import "strings"

// modelPricing holds per-million-token costs for a model.
type modelPricing struct {
	InputPerM  float64 // USD per 1M input tokens
	OutputPerM float64 // USD per 1M output tokens
}

// pricingTable maps model IDs to their per-million-token costs.
var pricingTable = map[string]modelPricing{
	"gpt-4o":                   {InputPerM: 2.50, OutputPerM: 10.00},
	"gpt-4o-mini":              {InputPerM: 0.15, OutputPerM: 0.60},
	"gpt-4-turbo":              {InputPerM: 10.00, OutputPerM: 30.00},
	"o1":                       {InputPerM: 15.00, OutputPerM: 60.00},
	"o1-mini":                  {InputPerM: 1.10, OutputPerM: 4.40},
	"o3-mini":                  {InputPerM: 1.10, OutputPerM: 4.40},
	"text-embedding-3-small":   {InputPerM: 0.02, OutputPerM: 0},
	"text-embedding-3-large":   {InputPerM: 0.13, OutputPerM: 0},
}

// getPricing returns the per-million-token costs for a model.
// It tries exact match first, then prefix match (e.g. "gpt-4o-2024-08-06" matches "gpt-4o").
// Returns (0, 0) if no match is found.
func getPricing(model string) (inputPerM, outputPerM float64) {
	// Exact match.
	if p, ok := pricingTable[model]; ok {
		return p.InputPerM, p.OutputPerM
	}

	// Prefix match: find the longest matching prefix.
	var bestKey string
	for key := range pricingTable {
		if strings.HasPrefix(model, key) && len(key) > len(bestKey) {
			bestKey = key
		}
	}
	if bestKey != "" {
		p := pricingTable[bestKey]
		return p.InputPerM, p.OutputPerM
	}

	return 0, 0
}

// calculateCost returns the estimated cost in USD for the given token counts.
func calculateCost(model string, tokensIn, tokensOut int64) float64 {
	inputPerM, outputPerM := getPricing(model)
	return (float64(tokensIn)*inputPerM + float64(tokensOut)*outputPerM) / 1_000_000
}
