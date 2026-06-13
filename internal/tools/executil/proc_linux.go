//go:build linux

package executil

import (
	"os/exec"
	"syscall"
)

// ConfigureProcGroup sets up process group isolation so child processes
// can be killed as a group on timeout.
func ConfigureProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// KillProcGroup kills the entire process group for the given PID.
func KillProcGroup(pid int) {
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}
