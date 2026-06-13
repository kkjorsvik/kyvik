package obsidian

import (
	"context"
	"time"
)

// VaultConfig represents a configured Obsidian vault.
type VaultConfig struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Path         string    `json:"path"`
	SyncEmail    string    `json:"sync_email,omitempty"`
	SyncPassword string    `json:"sync_password,omitempty"`
	SyncVaultID  string    `json:"sync_vault_id,omitempty"`
	SyncEnabled  bool      `json:"sync_enabled"`
	SyncStatus   string    `json:"sync_status"`
	LastSyncAt   time.Time `json:"last_sync_at,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

const (
	SyncStatusDisabled = "disabled"
	SyncStatusSyncing  = "syncing"
	SyncStatusSynced   = "synced"
	SyncStatusError    = "error"
	SyncStatusStopped  = "stopped"
)

// VaultStore manages Obsidian vault configuration in the database.
type VaultStore interface {
	Create(ctx context.Context, vault VaultConfig) error
	Get(ctx context.Context, id string) (*VaultConfig, error)
	GetByName(ctx context.Context, name string) (*VaultConfig, error)
	List(ctx context.Context) ([]VaultConfig, error)
	Update(ctx context.Context, vault VaultConfig) error
	Delete(ctx context.Context, id string) error
	SetSyncStatus(ctx context.Context, id, status string) error
	UpdateLastSync(ctx context.Context, id string) error
}
