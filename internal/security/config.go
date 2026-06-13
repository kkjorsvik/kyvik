package security

import (
	"encoding/json"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

// ResolveConfig parses SecurityJSON from an agent config and applies defaults.
// Operator and admin templates default to "high" sensitivity.
func ResolveConfig(config types.AgentConfig) types.SecurityConfig {
	cfg := types.DefaultSecurityConfig()

	if config.SecurityJSON != "" && config.SecurityJSON != "{}" {
		_ = json.Unmarshal([]byte(config.SecurityJSON), &cfg)
	}

	// Ensure sensitivity has a valid value.
	switch cfg.AnomalyDetectionSensitivity {
	case "low", "medium", "high":
		// valid
	default:
		cfg.AnomalyDetectionSensitivity = "medium"
	}

	// Operator and admin templates default to high sensitivity.
	if config.Template == "operator" || config.Template == "admin" {
		if config.SecurityJSON == "" || config.SecurityJSON == "{}" {
			cfg.AnomalyDetectionSensitivity = "high"
		}
	}

	return cfg
}
