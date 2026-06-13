package obsidian

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// DBVaultStore implements VaultStore backed by a *sql.DB (PostgreSQL).
type DBVaultStore struct {
	db *sql.DB
}

// NewDBVaultStore creates a new DBVaultStore using the given database.
func NewDBVaultStore(db *sql.DB) *DBVaultStore {
	return &DBVaultStore{db: db}
}

// Create inserts a new vault. The vault's ID is generated if empty.
func (s *DBVaultStore) Create(ctx context.Context, vault VaultConfig) error {
	if vault.ID == "" {
		vault.ID = uuid.New().String()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO obsidian_vaults
			(id, name, path, sync_email, sync_password, sync_vault_id, sync_enabled, sync_status)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		vault.ID, vault.Name, vault.Path,
		vault.SyncEmail, vault.SyncPassword, vault.SyncVaultID,
		boolToInt(vault.SyncEnabled), vault.SyncStatus,
	)
	if err != nil {
		return fmt.Errorf("obsidian: create vault: %w", err)
	}
	return nil
}

// Get returns the vault with the given ID, or nil if not found.
func (s *DBVaultStore) Get(ctx context.Context, id string) (*VaultConfig, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, path, sync_email, sync_password, sync_vault_id,
		        sync_enabled, sync_status, last_sync_at, created_at
		 FROM obsidian_vaults WHERE id = $1`, id)
	v, err := scanVault(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("obsidian: get vault: %w", err)
	}
	return v, nil
}

// GetByName returns the vault with the given name, or nil if not found.
func (s *DBVaultStore) GetByName(ctx context.Context, name string) (*VaultConfig, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, path, sync_email, sync_password, sync_vault_id,
		        sync_enabled, sync_status, last_sync_at, created_at
		 FROM obsidian_vaults WHERE name = $1`, name)
	v, err := scanVault(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("obsidian: get vault by name: %w", err)
	}
	return v, nil
}

// List returns all vaults ordered by created_at ascending.
func (s *DBVaultStore) List(ctx context.Context) ([]VaultConfig, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, path, sync_email, sync_password, sync_vault_id,
		        sync_enabled, sync_status, last_sync_at, created_at
		 FROM obsidian_vaults ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("obsidian: list vaults: %w", err)
	}
	defer rows.Close()

	var vaults []VaultConfig
	for rows.Next() {
		v, err := scanVaultRow(rows)
		if err != nil {
			return nil, fmt.Errorf("obsidian: list vaults scan: %w", err)
		}
		vaults = append(vaults, *v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("obsidian: list vaults rows: %w", err)
	}
	return vaults, nil
}

// Update replaces all mutable fields of an existing vault by ID.
func (s *DBVaultStore) Update(ctx context.Context, vault VaultConfig) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE obsidian_vaults
		 SET name = $1, path = $2, sync_email = $3, sync_password = $4,
		     sync_vault_id = $5, sync_enabled = $6, sync_status = $7
		 WHERE id = $8`,
		vault.Name, vault.Path,
		vault.SyncEmail, vault.SyncPassword, vault.SyncVaultID,
		boolToInt(vault.SyncEnabled), vault.SyncStatus,
		vault.ID,
	)
	if err != nil {
		return fmt.Errorf("obsidian: update vault: %w", err)
	}
	return nil
}

// Delete removes the vault with the given ID.
func (s *DBVaultStore) Delete(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM obsidian_vaults WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("obsidian: delete vault: %w", err)
	}
	return nil
}

// SetSyncStatus updates only the sync_status field.
func (s *DBVaultStore) SetSyncStatus(ctx context.Context, id, status string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE obsidian_vaults SET sync_status = $1 WHERE id = $2`, status, id)
	if err != nil {
		return fmt.Errorf("obsidian: set sync status: %w", err)
	}
	return nil
}

// UpdateLastSync sets last_sync_at to CURRENT_TIMESTAMP for the given vault.
func (s *DBVaultStore) UpdateLastSync(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE obsidian_vaults SET last_sync_at = CURRENT_TIMESTAMP WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("obsidian: update last sync: %w", err)
	}
	return nil
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanVault(r *sql.Row) (*VaultConfig, error) {
	return scanVaultRow(r)
}

func scanVaultRow(r rowScanner) (*VaultConfig, error) {
	var v VaultConfig
	var syncEnabled int
	var lastSyncAt sql.NullTime

	err := r.Scan(
		&v.ID, &v.Name, &v.Path,
		&v.SyncEmail, &v.SyncPassword, &v.SyncVaultID,
		&syncEnabled, &v.SyncStatus,
		&lastSyncAt, &v.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	v.SyncEnabled = syncEnabled != 0
	if lastSyncAt.Valid {
		v.LastSyncAt = lastSyncAt.Time
	}
	return &v, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
