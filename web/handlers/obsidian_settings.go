package handlers

import (
	"context"
	"net/http"
	"strings"

	"github.com/kkjorsvik/kyvik/internal/obsidian"
)

// SetObsidianVaultManager sets the Obsidian vault manager on the handlers.
func (h *Handlers) SetObsidianVaultManager(m *obsidian.VaultManager) {
	h.obsidianMgr = m
}

// loadObsidianVaultsTab populates data for the obsidian-vaults settings tab.
func (h *Handlers) loadObsidianVaultsTab(ctx context.Context, data map[string]any) {
	if h.obsidianMgr == nil {
		data["ObsidianAvailable"] = false
		data["ObsidianVaults"] = []obsidian.VaultConfig{}
		return
	}

	data["ObsidianAvailable"] = h.obsidianMgr.IsAvailable()

	vaults, err := h.obsidianMgr.ListVaults(ctx)
	if err != nil {
		data["ObsidianVaults"] = []obsidian.VaultConfig{}
		data["ObsidianError"] = "Failed to load vaults"
		return
	}
	data["ObsidianVaults"] = vaults
}

// ObsidianVaultCreate handles POST /settings/obsidian-vaults/create.
func (h *Handlers) ObsidianVaultCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if h.obsidianMgr == nil {
		http.Error(w, "obsidian vault manager not available", http.StatusServiceUnavailable)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "failed to parse form", http.StatusBadRequest)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	path := strings.TrimSpace(r.FormValue("path"))
	if name == "" || path == "" {
		http.Error(w, "name and path are required", http.StatusBadRequest)
		return
	}

	vault := obsidian.VaultConfig{
		Name:        name,
		Path:        path,
		SyncEmail:   strings.TrimSpace(r.FormValue("sync_email")),
		SyncPassword: r.FormValue("sync_password"),
		SyncVaultID: strings.TrimSpace(r.FormValue("sync_vault_id")),
		SyncEnabled: r.FormValue("sync_enabled") == "true",
	}

	if err := h.obsidianMgr.AddVault(ctx, vault); err != nil {
		h.serverError(w, r, "creating vault", err)
		return
	}

	data := map[string]any{}
	h.loadObsidianVaultsTab(ctx, data)
	h.renderFragment(w, r, "settings-tab-obsidian-vaults", data)
}

// ObsidianVaultUpdate handles POST /settings/obsidian-vaults/{id}/update.
func (h *Handlers) ObsidianVaultUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")

	if h.obsidianMgr == nil {
		http.Error(w, "obsidian vault manager not available", http.StatusServiceUnavailable)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "failed to parse form", http.StatusBadRequest)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	path := strings.TrimSpace(r.FormValue("path"))
	if name == "" || path == "" {
		http.Error(w, "name and path are required", http.StatusBadRequest)
		return
	}

	vault := obsidian.VaultConfig{
		ID:          id,
		Name:        name,
		Path:        path,
		SyncEmail:   strings.TrimSpace(r.FormValue("sync_email")),
		SyncPassword: r.FormValue("sync_password"),
		SyncVaultID: strings.TrimSpace(r.FormValue("sync_vault_id")),
		SyncEnabled: r.FormValue("sync_enabled") == "true",
	}

	if err := h.obsidianMgr.UpdateVault(ctx, vault); err != nil {
		h.serverError(w, r, "updating vault", err)
		return
	}

	data := map[string]any{}
	h.loadObsidianVaultsTab(ctx, data)
	h.renderFragment(w, r, "settings-tab-obsidian-vaults", data)
}

// ObsidianVaultDelete handles POST /settings/obsidian-vaults/{id}/delete.
func (h *Handlers) ObsidianVaultDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")

	if h.obsidianMgr == nil {
		http.Error(w, "obsidian vault manager not available", http.StatusServiceUnavailable)
		return
	}

	if err := h.obsidianMgr.RemoveVault(ctx, id); err != nil {
		h.serverError(w, r, "deleting vault", err)
		return
	}

	data := map[string]any{}
	h.loadObsidianVaultsTab(ctx, data)
	h.renderFragment(w, r, "settings-tab-obsidian-vaults", data)
}
