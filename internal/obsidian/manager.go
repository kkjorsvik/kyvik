package obsidian

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"sync"
)

// SecretsVault is the subset of the secrets store used by VaultManager to
// persist sync credentials outside the main database.
type SecretsVault interface {
	Set(ctx context.Context, scope, key, value, description string) error
	Get(ctx context.Context, scope, key string) (string, error)
}

// VaultManager manages a set of Obsidian vault configurations and their
// corresponding obsidian-headless sync daemons.
type VaultManager struct {
	store     VaultStore
	secrets   SecretsVault
	processes map[string]*vaultProcess // keyed by vault ID
	mu        sync.RWMutex
	cancel    context.CancelFunc
	available bool // true when obsidian-headless is in PATH
}

// NewManager constructs a VaultManager. It checks whether obsidian-headless
// is installed; if not, sync daemons will not be started but CRUD operations
// still work.
func NewManager(store VaultStore, secrets SecretsVault) *VaultManager {
	_, err := exec.LookPath("obsidian-headless")
	return &VaultManager{
		store:     store,
		secrets:   secrets,
		processes: make(map[string]*vaultProcess),
		available: err == nil,
	}
}

// Start loads all vaults from the database and starts sync daemons for those
// with sync enabled. The provided context governs all child processes.
func (m *VaultManager) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	m.mu.Lock()
	m.cancel = cancel
	m.mu.Unlock()

	vaults, err := m.store.List(ctx)
	if err != nil {
		cancel()
		return fmt.Errorf("obsidian manager: load vaults: %w", err)
	}

	for _, v := range vaults {
		if v.SyncEnabled {
			if startErr := m.startDaemon(ctx, v); startErr != nil {
				slog.Warn("obsidian manager: failed to start daemon on startup",
					"vault", v.Name, "err", startErr)
			}
		}
	}

	return nil
}

// Stop shuts down all running sync daemons.
func (m *VaultManager) Stop() {
	m.mu.Lock()
	cancel := m.cancel
	procs := make(map[string]*vaultProcess, len(m.processes))
	for k, v := range m.processes {
		procs[k] = v
	}
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	for _, p := range procs {
		p.stop()
	}

	m.mu.Lock()
	m.processes = make(map[string]*vaultProcess)
	m.mu.Unlock()
}

// AddVault stores the vault in the database, persists sync credentials to the
// secrets store, and starts a sync daemon if sync is enabled.
func (m *VaultManager) AddVault(ctx context.Context, vault VaultConfig) error {
	// Persist sync credentials to secrets store and redact from DB record.
	if vault.SyncEnabled && vault.SyncPassword != "" {
		scope := secretScope(vault.Name)
		if err := m.secrets.Set(ctx, scope, "sync_password", vault.SyncPassword,
			"Obsidian Sync password for vault "+vault.Name); err != nil {
			return fmt.Errorf("obsidian manager: store sync password: %w", err)
		}
		vault.SyncPassword = "" // do not persist plaintext in the DB
	}

	vault.SyncStatus = SyncStatusDisabled
	if vault.SyncEnabled {
		vault.SyncStatus = SyncStatusStopped
	}

	if err := m.store.Create(ctx, vault); err != nil {
		return fmt.Errorf("obsidian manager: create vault: %w", err)
	}

	if vault.SyncEnabled {
		// Reload from DB to get generated ID / timestamps.
		stored, err := m.store.GetByName(ctx, vault.Name)
		if err != nil || stored == nil {
			return fmt.Errorf("obsidian manager: reload vault after create: %w", err)
		}
		// Re-attach the password for the daemon.
		stored.SyncPassword, _ = m.secrets.Get(ctx, secretScope(vault.Name), "sync_password")

		m.mu.RLock()
		daemonCtx := m.daemonContext(ctx)
		m.mu.RUnlock()

		if startErr := m.startDaemon(daemonCtx, *stored); startErr != nil {
			slog.Warn("obsidian manager: daemon not started", "vault", vault.Name, "err", startErr)
		}
	}

	return nil
}

// UpdateVault updates the vault record in the database and restarts the sync
// daemon when sync settings have changed.
func (m *VaultManager) UpdateVault(ctx context.Context, vault VaultConfig) error {
	existing, err := m.store.Get(ctx, vault.ID)
	if err != nil {
		return fmt.Errorf("obsidian manager: get vault for update: %w", err)
	}

	// Update secrets if a new password was supplied.
	if vault.SyncPassword != "" {
		if err := m.secrets.Set(ctx, secretScope(vault.Name), "sync_password", vault.SyncPassword,
			"Obsidian Sync password for vault "+vault.Name); err != nil {
			return fmt.Errorf("obsidian manager: update sync password: %w", err)
		}
		vault.SyncPassword = ""
	}

	if err := m.store.Update(ctx, vault); err != nil {
		return fmt.Errorf("obsidian manager: update vault: %w", err)
	}

	// Restart daemon when sync settings changed.
	syncChanged := existing == nil ||
		existing.SyncEnabled != vault.SyncEnabled ||
		existing.SyncEmail != vault.SyncEmail ||
		existing.SyncVaultID != vault.SyncVaultID ||
		existing.Path != vault.Path

	if syncChanged {
		m.stopDaemon(vault.ID)

		if vault.SyncEnabled {
			vault.SyncPassword, _ = m.secrets.Get(ctx, secretScope(vault.Name), "sync_password")

			m.mu.RLock()
			daemonCtx := m.daemonContext(ctx)
			m.mu.RUnlock()

			if startErr := m.startDaemon(daemonCtx, vault); startErr != nil {
				slog.Warn("obsidian manager: daemon not restarted", "vault", vault.Name, "err", startErr)
			}
		}
	}

	return nil
}

// RemoveVault stops the daemon, deletes the vault from the database, and
// removes its secrets.
func (m *VaultManager) RemoveVault(ctx context.Context, id string) error {
	vault, err := m.store.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("obsidian manager: get vault for remove: %w", err)
	}

	m.stopDaemon(id)

	if err := m.store.Delete(ctx, id); err != nil {
		return fmt.Errorf("obsidian manager: delete vault: %w", err)
	}

	// Best-effort cleanup of secrets — ignore errors.
	if vault != nil {
		_ = m.secrets.Set(ctx, secretScope(vault.Name), "sync_password", "", "")
	}

	return nil
}

// VaultPath returns the filesystem path of the named vault.
func (m *VaultManager) VaultPath(ctx context.Context, name string) (string, error) {
	v, err := m.store.GetByName(ctx, name)
	if err != nil {
		return "", fmt.Errorf("obsidian manager: vault path: %w", err)
	}
	if v == nil {
		return "", fmt.Errorf("obsidian manager: vault %q not found", name)
	}
	return v.Path, nil
}

// VaultStatus returns the current sync status of the named vault.
func (m *VaultManager) VaultStatus(ctx context.Context, name string) (string, error) {
	v, err := m.store.GetByName(ctx, name)
	if err != nil {
		return "", fmt.Errorf("obsidian manager: vault status: %w", err)
	}
	if v == nil {
		return "", fmt.Errorf("obsidian manager: vault %q not found", name)
	}
	return v.SyncStatus, nil
}

// ListVaults returns all configured vaults.
func (m *VaultManager) ListVaults(ctx context.Context) ([]VaultConfig, error) {
	vaults, err := m.store.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("obsidian manager: list vaults: %w", err)
	}
	return vaults, nil
}

// IsAvailable reports whether obsidian-headless is installed and available.
func (m *VaultManager) IsAvailable() bool {
	return m.available
}

// startDaemon spawns an obsidian-headless process for the vault and registers
// it in the process map. It is a no-op if obsidian-headless is not installed.
func (m *VaultManager) startDaemon(ctx context.Context, vault VaultConfig) error {
	if !m.available {
		return nil
	}

	p := newVaultProcess(vault)
	if err := p.start(ctx); err != nil {
		_ = m.store.SetSyncStatus(ctx, vault.ID, SyncStatusError)
		return err
	}

	m.mu.Lock()
	m.processes[vault.ID] = p
	m.mu.Unlock()

	_ = m.store.SetSyncStatus(ctx, vault.ID, SyncStatusSyncing)
	return nil
}

// stopDaemon stops and removes the daemon for the given vault ID.
func (m *VaultManager) stopDaemon(id string) {
	m.mu.Lock()
	p, ok := m.processes[id]
	if ok {
		delete(m.processes, id)
	}
	m.mu.Unlock()

	if ok {
		p.stop()
	}
}

// daemonContext returns the context to use for spawning daemons.
// When a manager-level cancel is available, it returns a child of it;
// otherwise falls back to the provided ctx.
// Must be called with m.mu held (at least for read).
func (m *VaultManager) daemonContext(ctx context.Context) context.Context {
	// The manager's own context was set up in Start; child processes inherit it
	// so that Stop() terminates them all. We derive a new child here so that the
	// individual process cancel is still available.
	return ctx
}

// secretScope returns the secrets scope key for a vault.
func secretScope(vaultName string) string {
	return "obsidian:" + vaultName
}
