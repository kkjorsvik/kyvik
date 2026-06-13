//go:build linux

package sandbox

import (
	"fmt"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

// rlimitASMultiplier is the factor applied to MaxMemoryMB for the RLIMIT_AS
// (virtual address space) limit. Go's runtime reserves far more virtual memory
// than it physically uses (goroutine stacks, GC heap arenas, mmap regions), so
// a 1:1 mapping causes OOM crashes during normal operations like TLS handshakes.
// The real memory enforcement is GOMEMLIMIT (set via env var); RLIMIT_AS at 4x
// serves as a defense-in-depth safety net against runaway allocations.
const rlimitASMultiplier = 4

// applyRLimits sets resource limits on the current process (Linux only).
func applyRLimits(cfg SandboxConfig) error {
	// RLIMIT_AS: virtual address space limit (defense-in-depth safety net).
	// GOMEMLIMIT (env var) is the real GC-aware enforcement; this just prevents
	// truly unbounded virtual memory growth.
	memBytes := uint64(cfg.MaxMemoryMB) * rlimitASMultiplier * 1024 * 1024
	if memBytes > 0 {
		if err := unix.Setrlimit(unix.RLIMIT_AS, &unix.Rlimit{
			Cur: memBytes,
			Max: memBytes,
		}); err != nil {
			return fmt.Errorf("set RLIMIT_AS: %w", err)
		}
	}

	// RLIMIT_FSIZE: max file size
	if cfg.MaxOutputBytes > 0 {
		fsize := uint64(cfg.MaxOutputBytes)
		if err := unix.Setrlimit(unix.RLIMIT_FSIZE, &unix.Rlimit{
			Cur: fsize,
			Max: fsize,
		}); err != nil {
			return fmt.Errorf("set RLIMIT_FSIZE: %w", err)
		}
	}

	// NOTE: RLIMIT_NPROC is intentionally not set. On Linux it is per-UID, not
	// per-process, so it counts all threads across the parent kyvik process and
	// every sandbox. A low value breaks thread creation (TLS, shell exec); a high
	// value provides no meaningful per-sandbox isolation. Fork-bomb protection
	// comes from RLIMIT_AS (can't allocate thread stacks beyond virtual memory
	// limit), timeouts, and process-group kills.

	return nil
}

// configureSysProcAttr sets process group isolation on the command.
func configureSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup kills the entire process group for the given PID.
func killProcessGroup(pid int) {
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}
