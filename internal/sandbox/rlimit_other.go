//go:build !linux

package sandbox

import (
	"log/slog"
	"os/exec"
)

// applyRLimits is a no-op on non-Linux platforms.
func applyRLimits(_ SandboxConfig) error {
	slog.Debug("sandbox rlimits not supported on this platform")
	return nil
}

// configureSysProcAttr is a no-op on non-Linux platforms.
func configureSysProcAttr(_ *exec.Cmd) {
	slog.Debug("sandbox process group isolation not supported on this platform")
}

// killProcessGroup is a no-op on non-Linux platforms.
func killProcessGroup(_ int) {
	slog.Debug("sandbox process group kill not supported on this platform")
}
