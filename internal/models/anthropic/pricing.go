package anthropic

import "strings"

// modelPricing holds per-million-token costs for a model.
type modelPricing struct {
	InputPerM  float64 // USD per 1M input tokens
	OutputPerM float64 // USD per 1M output tokens
}

// pricingTable maps model IDs to their per-million-token costs.
var pricingTable = map[string]modelPricing{
	"claude-opus-4-5-20250527":    {InputPerM: 15.00, OutputPerM: 75.00},
	"claude-sonnet-4-20250514":    {InputPerM: 3.00, OutputPerM: 15.00},
	"claude-haiku-4-5-20251001":   {InputPerM: 0.80, OutputPerM: 4.00},
	"claude-3-5-sonnet-20241022":  {InputPerM: 3.00, OutputPerM: 15.00},
	"claude-3-5-haiku-20241022":   {InputPerM: 0.80, OutputPerM: 4.00},
	"claude-3-opus-20240229":      {InputPerM: 15.00, OutputPerM: 75.00},
}

// getPricing returns the per-million-token costs for a model.
// It tries exact match first, then prefix match (e.g. "claude-sonnet-4-20250514" matches "claude-sonnet-4").
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
