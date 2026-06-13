package obsidian

import (
	"context"
	"database/sql"
	"testing"

	"github.com/kkjorsvik/kyvik/internal/testutil"
	"github.com/kkjorsvik/kyvik/migrations"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	tdb := testutil.RequirePostgres(t)
	// Ensure obsidian_vaults table exists (migration 048 may not yet be in
	// the shared schema if ensureSchema hasn't run it).
	tdb.DB.Exec(migrations.ObsidianVaultsSchema)
	return tdb.DB
}

func TestCreate(t *testing.T) {
	ctx := context.Background()
	store := NewDBVaultStore(testDB(t))

	vault := VaultConfig{
		Name:       "my-vault",
		Path:       "/home/user/vault",
		SyncStatus: SyncStatusDisabled,
	}
	if err := store.Create(ctx, vault); err != nil {
		t.Fatalf("Create: %v", err)
	}
}

func TestCreateGeneratesID(t *testing.T) {
	ctx := context.Background()
	store := NewDBVaultStore(testDB(t))

	vault := VaultConfig{
		Name:       "auto-id-vault",
		Path:       "/tmp/vault",
		SyncStatus: SyncStatusDisabled,
	}
	if err := store.Create(ctx, vault); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := store.GetByName(ctx, "auto-id-vault")
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}
	if got == nil {
		t.Fatal("expected vault, got nil")
	}
	if got.ID == "" {
		t.Error("expected auto-generated ID, got empty string")
	}
}

func TestGet(t *testing.T) {
	ctx := context.Background()
	store := NewDBVaultStore(testDB(t))

	vault := VaultConfig{
		ID:         "test-id-1",
		Name:       "get-vault",
		Path:       "/tmp/get",
		SyncStatus: SyncStatusDisabled,
	}
	if err := store.Create(ctx, vault); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := store.Get(ctx, "test-id-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("expected vault, got nil")
	}
	if got.ID != "test-id-1" {
		t.Errorf("ID: got %q, want %q", got.ID, "test-id-1")
	}
	if got.Name != "get-vault" {
		t.Errorf("Name: got %q, want %q", got.Name, "get-vault")
	}
}

func TestGetMissing(t *testing.T) {
	ctx := context.Background()
	store := NewDBVaultStore(testDB(t))

	got, err := store.Get(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("Get missing: unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("Get missing: expected nil, got %+v", got)
	}
}

func TestGetByName(t *testing.T) {
	ctx := context.Background()
	store := NewDBVaultStore(testDB(t))

	vault := VaultConfig{
		ID:         "test-id-2",
		Name:       "named-vault",
		Path:       "/tmp/named",
		SyncStatus: SyncStatusDisabled,
	}
	if err := store.Create(ctx, vault); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := store.GetByName(ctx, "named-vault")
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}
	if got == nil {
		t.Fatal("expected vault, got nil")
	}
	if got.ID != "test-id-2" {
		t.Errorf("ID: got %q, want %q", got.ID, "test-id-2")
	}
}

func TestGetByNameMissing(t *testing.T) {
	ctx := context.Background()
	store := NewDBVaultStore(testDB(t))

	got, err := store.GetByName(ctx, "no-such-name")
	if err != nil {
		t.Fatalf("GetByName missing: unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("GetByName missing: expected nil, got %+v", got)
	}
}

func TestList(t *testing.T) {
	ctx := context.Background()
	store := NewDBVaultStore(testDB(t))

	vaults := []VaultConfig{
		{ID: "list-1", Name: "vault-a", Path: "/a", SyncStatus: SyncStatusDisabled},
		{ID: "list-2", Name: "vault-b", Path: "/b", SyncStatus: SyncStatusDisabled},
		{ID: "list-3", Name: "vault-c", Path: "/c", SyncStatus: SyncStatusDisabled},
	}
	for _, v := range vaults {
		if err := store.Create(ctx, v); err != nil {
			t.Fatalf("Create %s: %v", v.Name, err)
		}
	}

	got, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("List: got %d items, want 3", len(got))
	}
}

func TestListEmpty(t *testing.T) {
	ctx := context.Background()
	store := NewDBVaultStore(testDB(t))

	got, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List empty: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("List empty: expected 0, got %d", len(got))
	}
}

func TestUpdate(t *testing.T) {
	ctx := context.Background()
	store := NewDBVaultStore(testDB(t))

	vault := VaultConfig{
		ID:         "upd-1",
		Name:       "update-vault",
		Path:       "/old/path",
		SyncStatus: SyncStatusDisabled,
	}
	if err := store.Create(ctx, vault); err != nil {
		t.Fatalf("Create: %v", err)
	}

	vault.Path = "/new/path"
	vault.SyncEnabled = true
	vault.SyncStatus = SyncStatusSynced
	if err := store.Update(ctx, vault); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := store.Get(ctx, "upd-1")
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if got.Path != "/new/path" {
		t.Errorf("Path: got %q, want %q", got.Path, "/new/path")
	}
	if !got.SyncEnabled {
		t.Error("SyncEnabled: expected true")
	}
	if got.SyncStatus != SyncStatusSynced {
		t.Errorf("SyncStatus: got %q, want %q", got.SyncStatus, SyncStatusSynced)
	}
}

func TestDelete(t *testing.T) {
	ctx := context.Background()
	store := NewDBVaultStore(testDB(t))

	vault := VaultConfig{
		ID:         "del-1",
		Name:       "delete-vault",
		Path:       "/del",
		SyncStatus: SyncStatusDisabled,
	}
	if err := store.Create(ctx, vault); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := store.Delete(ctx, "del-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	got, err := store.Get(ctx, "del-1")
	if err != nil {
		t.Fatalf("Get after delete: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil after delete, got %+v", got)
	}
}

func TestSetSyncStatus(t *testing.T) {
	ctx := context.Background()
	store := NewDBVaultStore(testDB(t))

	vault := VaultConfig{
		ID:         "sync-1",
		Name:       "sync-vault",
		Path:       "/sync",
		SyncStatus: SyncStatusDisabled,
	}
	if err := store.Create(ctx, vault); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := store.SetSyncStatus(ctx, "sync-1", SyncStatusSyncing); err != nil {
		t.Fatalf("SetSyncStatus: %v", err)
	}

	got, err := store.Get(ctx, "sync-1")
	if err != nil {
		t.Fatalf("Get after SetSyncStatus: %v", err)
	}
	if got.SyncStatus != SyncStatusSyncing {
		t.Errorf("SyncStatus: got %q, want %q", got.SyncStatus, SyncStatusSyncing)
	}
}

func TestUpdateLastSync(t *testing.T) {
	ctx := context.Background()
	store := NewDBVaultStore(testDB(t))

	vault := VaultConfig{
		ID:         "lastsync-1",
		Name:       "lastsync-vault",
		Path:       "/lastsync",
		SyncStatus: SyncStatusDisabled,
	}
	if err := store.Create(ctx, vault); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Before update, LastSyncAt should be zero.
	before, err := store.Get(ctx, "lastsync-1")
	if err != nil {
		t.Fatalf("Get before UpdateLastSync: %v", err)
	}
	if !before.LastSyncAt.IsZero() {
		t.Error("expected LastSyncAt to be zero before update")
	}

	if err := store.UpdateLastSync(ctx, "lastsync-1"); err != nil {
		t.Fatalf("UpdateLastSync: %v", err)
	}

	after, err := store.Get(ctx, "lastsync-1")
	if err != nil {
		t.Fatalf("Get after UpdateLastSync: %v", err)
	}
	if after.LastSyncAt.IsZero() {
		t.Error("expected LastSyncAt to be set after UpdateLastSync")
	}
}
