package sandbox

import "time"

// SandboxConfig defines resource constraints for a sandboxed execution.
type SandboxConfig struct {
	MaxMemoryMB    int  // Default 1024
	MaxCPUPercent  int  // Default 50
	TimeoutSeconds int  // Default 60
	AllowNetwork   bool // Default false
	MaxOutputBytes int  // Default 1MB (1048576)
}

// DefaultSandboxConfig returns a SandboxConfig with sensible defaults.
func DefaultSandboxConfig() SandboxConfig {
	return SandboxConfig{
		MaxMemoryMB:    1024,
		MaxCPUPercent:  50,
		TimeoutSeconds: 60,
		AllowNetwork:   false,
		MaxOutputBytes: 1 << 20, // 1 MB
	}
}

// Timeout returns the sandbox timeout as a time.Duration.
// Falls back to 60 seconds if TimeoutSeconds is zero.
func (c SandboxConfig) Timeout() time.Duration {
	if c.TimeoutSeconds <= 0 {
		return 60 * time.Second
	}
	return time.Duration(c.TimeoutSeconds) * time.Second
}

// ManagerConfig configures the sandbox Manager.
type ManagerConfig struct {
	WorkspaceRoot string        // Base path for agent workspaces
	RunnerPath    string        // Path to kyvik-sandbox binary (auto-detect if empty)
	Defaults      SandboxConfig // Default sandbox constraints
	ProxyAddr     string        // address of the network proxy (host:port)
}

// ApplyRLimits applies resource limits to the current process.
// This is a public wrapper around the build-tagged applyRLimits function,
// intended for use by the kyvik-sandbox binary (defense-in-depth).
func ApplyRLimits(cfg SandboxConfig) error {
	return applyRLimits(cfg)
}
