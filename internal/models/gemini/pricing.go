package gemini

import "strings"

// modelPricing holds per-million-token costs for a model.
type modelPricing struct {
	InputPerM  float64 // USD per 1M input tokens
	OutputPerM float64 // USD per 1M output tokens
}

// pricingTable maps model IDs to their per-million-token costs.
// Prices from Google AI Studio (as of early 2026).
var pricingTable = map[string]modelPricing{
	"gemini-2.5-pro":   {InputPerM: 1.25, OutputPerM: 10.00},
	"gemini-2.5-flash": {InputPerM: 0.15, OutputPerM: 0.60},
	"gemini-2.0-flash": {InputPerM: 0.10, OutputPerM: 0.40},
	"gemini-1.5-pro":   {InputPerM: 1.25, OutputPerM: 5.00},
	"gemini-1.5-flash": {InputPerM: 0.075, OutputPerM: 0.30},
}

// getPricing returns the per-million-token costs for a model.
// Tries exact match first, then prefix match.
func getPricing(model string) (inputPerM, outputPerM float64) {
	if p, ok := pricingTable[model]; ok {
		return p.InputPerM, p.OutputPerM
	}

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
