package sandbox

// SandboxConfigForTier returns a SandboxConfig tuned for the given KTP tier.
// Unknown or empty tiers get the most restrictive defaults (defense-in-depth).
// The provided defaults are used as a base for tiers that don't override a field.
func SandboxConfigForTier(tier string, defaults SandboxConfig) SandboxConfig {
	switch tier {
	case "reader":
		return SandboxConfig{
			MaxMemoryMB:    256,
			MaxCPUPercent:  10,
			TimeoutSeconds: 10,
			AllowNetwork:   true,
			MaxOutputBytes: defaults.MaxOutputBytes,
		}
	case "writer":
		return SandboxConfig{
			MaxMemoryMB:    512,
			MaxCPUPercent:  25,
			TimeoutSeconds: 30,
			AllowNetwork:   true,
			MaxOutputBytes: defaults.MaxOutputBytes,
		}
	case "operator":
		return SandboxConfig{
			MaxMemoryMB:    defaults.MaxMemoryMB,
			MaxCPUPercent:  defaults.MaxCPUPercent,
			TimeoutSeconds: defaults.TimeoutSeconds,
			AllowNetwork:   true,
			MaxOutputBytes: defaults.MaxOutputBytes,
		}
	case "admin":
		return SandboxConfig{
			MaxMemoryMB:    1024,
			MaxCPUPercent:  50,
			TimeoutSeconds: 120,
			AllowNetwork:   true,
			MaxOutputBytes: defaults.MaxOutputBytes,
		}
	default:
		// Unknown → strictest defaults (deny-by-default).
		return SandboxConfig{
			MaxMemoryMB:    256,
			MaxCPUPercent:  10,
			TimeoutSeconds: 10,
			AllowNetwork:   false,
			MaxOutputBytes: defaults.MaxOutputBytes,
		}
	}
}
