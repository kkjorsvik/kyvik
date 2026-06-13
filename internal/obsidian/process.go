package obsidian

import (
	"context"
	"log/slog"
	"os/exec"
	"sync"
	"time"
)

const (
	restartBackoffBase = time.Second
	restartBackoffMax  = 5 * time.Minute
	stopTimeout        = 10 * time.Second
)

// vaultProcess wraps an obsidian-headless child process for a single vault.
// It automatically restarts the process with exponential backoff on crash.
type vaultProcess struct {
	vault  VaultConfig
	cmd    *exec.Cmd
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	mu     sync.Mutex
}

// newVaultProcess creates a vaultProcess for the given vault. Call start to
// actually spawn the child process.
func newVaultProcess(vault VaultConfig) *vaultProcess {
	return &vaultProcess{
		vault: vault,
		done:  make(chan struct{}),
	}
}

// start spawns the obsidian-headless daemon and begins monitoring it.
// The provided ctx is used as the parent; cancelling it will stop the process.
func (p *vaultProcess) start(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	childCtx, cancel := context.WithCancel(ctx)
	p.ctx = childCtx
	p.cancel = cancel

	if err := p.spawn(); err != nil {
		cancel()
		return err
	}

	go p.monitor()
	return nil
}

// stop signals the process to shut down and waits for it to exit.
// If the process does not exit within 10 seconds it is killed with SIGKILL.
func (p *vaultProcess) stop() {
	p.mu.Lock()
	cancel := p.cancel
	p.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	select {
	case <-p.done:
	case <-time.After(stopTimeout):
		p.mu.Lock()
		cmd := p.cmd
		p.mu.Unlock()
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-p.done
	}
}

// spawn builds and starts the obsidian-headless command.
// Must be called with p.mu held.
func (p *vaultProcess) spawn() error {
	args := []string{
		"--vault", p.vault.Path,
	}
	if p.vault.SyncEnabled && p.vault.SyncEmail != "" {
		args = append(args,
			"--sync-email", p.vault.SyncEmail,
			"--sync-password", p.vault.SyncPassword,
		)
		if p.vault.SyncVaultID != "" {
			args = append(args, "--sync-vault-id", p.vault.SyncVaultID)
		}
	}

	cmd := exec.CommandContext(p.ctx, "obsidian-headless", args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	p.cmd = cmd
	return nil
}

// monitor waits for the child process to exit and restarts it with exponential
// backoff until the context is cancelled.
func (p *vaultProcess) monitor() {
	defer close(p.done)

	backoff := restartBackoffBase

	for {
		// Wait for the current process to exit.
		p.mu.Lock()
		cmd := p.cmd
		p.mu.Unlock()

		if cmd != nil {
			_ = cmd.Wait()
		}

		// Check whether the context is done before restarting.
		select {
		case <-p.ctx.Done():
			return
		default:
		}

		slog.Warn("obsidian: vault process exited unexpectedly, restarting",
			"vault", p.vault.Name,
			"backoff", backoff,
		)

		// Wait for the backoff duration or until stopped.
		select {
		case <-p.ctx.Done():
			return
		case <-time.After(backoff):
		}

		// Attempt to restart.
		p.mu.Lock()
		err := p.spawn()
		p.mu.Unlock()

		if err != nil {
			slog.Error("obsidian: failed to restart vault process",
				"vault", p.vault.Name,
				"err", err,
			)
			// Advance backoff on spawn failure too.
			backoff = nextBackoff(backoff)
			continue
		}

		slog.Info("obsidian: vault process restarted", "vault", p.vault.Name)
		// Reset backoff after a successful start.
		backoff = restartBackoffBase
	}
}

// nextBackoff doubles the duration up to restartBackoffMax.
func nextBackoff(d time.Duration) time.Duration {
	d *= 2
	if d > restartBackoffMax {
		return restartBackoffMax
	}
	return d
}
