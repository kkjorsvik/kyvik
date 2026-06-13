//go:build !linux

package executil

import "os/exec"

// ConfigureProcGroup is a no-op on non-Linux platforms.
func ConfigureProcGroup(_ *exec.Cmd) {}

// KillProcGroup is a no-op on non-Linux platforms.
func KillProcGroup(_ int) {}
