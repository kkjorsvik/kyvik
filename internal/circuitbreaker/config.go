// Package circuitbreaker provides automated agent quarantine when behavioral
// thresholds are breached (error rate, spending velocity, action rate, etc.).
package circuitbreaker

import (
	"encoding/json"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

// ResolveConfig parses CircuitBreakerJSON from an agent config and applies defaults.
// Resolution chain: per-agent JSON → system defaults → hardcoded defaults.
func ResolveConfig(config types.AgentConfig, systemDefaults types.CircuitBreakerConfig) types.CircuitBreakerConfig {
	cfg := types.CircuitBreakerConfig{}
	hasAgentJSON := config.CircuitBreakerJSON != "" && config.CircuitBreakerJSON != "{}"

	if hasAgentJSON {
		_ = json.Unmarshal([]byte(config.CircuitBreakerJSON), &cfg)
	}

	// Enabled is a bool (zero value = false), so we can't use pickNonZero.
	// Only apply defaults if the agent didn't provide explicit JSON.
	if !hasAgentJSON {
		if systemDefaults.Enabled {
			cfg.Enabled = true
		} else {
			cfg.Enabled = types.DefaultCircuitBreakerConfig().Enabled
		}
	}

	hardcoded := types.DefaultCircuitBreakerConfig()
	if cfg.ErrorThreshold <= 0 {
		cfg.ErrorThreshold = pickNonZero(systemDefaults.ErrorThreshold, hardcoded.ErrorThreshold)
	}
	if cfg.ErrorWindowMinutes <= 0 {
		cfg.ErrorWindowMinutes = pickNonZero(systemDefaults.ErrorWindowMinutes, hardcoded.ErrorWindowMinutes)
	}
	if cfg.SpendingVelocityPct <= 0 {
		cfg.SpendingVelocityPct = pickNonZero(systemDefaults.SpendingVelocityPct, hardcoded.SpendingVelocityPct)
	}
	if cfg.SpendingWindowMinutes <= 0 {
		cfg.SpendingWindowMinutes = pickNonZero(systemDefaults.SpendingWindowMinutes, hardcoded.SpendingWindowMinutes)
	}
	if cfg.ActionRatePerMinute <= 0 {
		cfg.ActionRatePerMinute = pickNonZero(systemDefaults.ActionRatePerMinute, hardcoded.ActionRatePerMinute)
	}
	if cfg.DestructiveLimit <= 0 {
		cfg.DestructiveLimit = pickNonZero(systemDefaults.DestructiveLimit, hardcoded.DestructiveLimit)
	}
	if cfg.LoopIdenticalCount <= 0 {
		cfg.LoopIdenticalCount = pickNonZero(systemDefaults.LoopIdenticalCount, hardcoded.LoopIdenticalCount)
	}

	return cfg
}

func pickNonZero(a, b int) int {
	if a > 0 {
		return a
	}
	return b
}
