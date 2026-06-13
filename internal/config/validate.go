package config

import (
	"log/slog"
	"os"
)

// HostAccessDiagnostic captures the results of probing the runtime environment.
type HostAccessDiagnostic struct {
	Mode             string   // "sandbox" or "host"
	HomeAccessible   bool     // true if /home is accessible (no ProtectHome)
	Warnings         []string // non-fatal warnings
	Errors           []string // fatal errors (caller should exit)
	InaccessiblePaths []string // extra_paths that couldn't be accessed
}

// ValidateHostAccess probes the runtime to verify that the configured host_access
// mode is actually possible given systemd restrictions. Returns diagnostics.
func ValidateHostAccess(cfg *Config) HostAccessDiagnostic {
	diag := HostAccessDiagnostic{
		Mode: cfg.Sandbox.HostAccess,
	}

	// Probe /home to detect ProtectHome=true.
	if _, err := os.Stat("/home"); err == nil {
		diag.HomeAccessible = true
	}

	if cfg.Sandbox.HostAccess == "host" && !diag.HomeAccessible {
		diag.Warnings = append(diag.Warnings,
			"host_access=\"host\" but /home is not accessible (systemd ProtectHome=true?); "+
				"run 'make install-service' to regenerate the service file")
	}

	// Probe each extra_paths entry.
	for _, p := range cfg.Sandbox.ExtraPaths.ReadWrite {
		if _, err := os.Stat(p); err != nil {
			diag.InaccessiblePaths = append(diag.InaccessiblePaths, p)
			diag.Warnings = append(diag.Warnings,
				"extra_paths.read_write path inaccessible: "+p)
		}
	}
	for _, p := range cfg.Sandbox.ExtraPaths.ReadOnly {
		if _, err := os.Stat(p); err != nil {
			diag.InaccessiblePaths = append(diag.InaccessiblePaths, p)
			diag.Warnings = append(diag.Warnings,
				"extra_paths.read_only path inaccessible: "+p)
		}
	}

	return diag
}

// LogDiagnostic logs the host access diagnostic results.
func (d HostAccessDiagnostic) LogDiagnostic() {
	for _, w := range d.Warnings {
		slog.Warn("host access: " + w)
	}
	for _, e := range d.Errors {
		slog.Error("host access: " + e)
	}
}
