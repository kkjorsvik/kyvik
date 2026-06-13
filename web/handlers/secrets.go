package handlers

import (
	"context"
	"net/http"
)

// validSecretScope returns true if scope is "global" or matches a known agent ID.
func (h *Handlers) validSecretScope(ctx context.Context, scope string) bool {
	if scope == "global" {
		return true
	}
	if h.kyvik == nil {
		return false
	}
	agents, err := h.kyvik.ListAgents(ctx)
	if err != nil {
		return false
	}
	for _, a := range agents {
		if a.ID == scope {
			return true
		}
	}
	return false
}

// SecretsList renders the secrets management page.
func (h *Handlers) SecretsList(w http.ResponseWriter, r *http.Request) {
	if h.secrets == nil {
		http.Error(w, "Secrets vault not configured", http.StatusServiceUnavailable)
		return
	}

	ctx := r.Context()

	// Determine active scope (default to global)
	activeScope := r.URL.Query().Get("scope")
	if activeScope == "" {
		activeScope = "global"
	}

	// List agents for scope tabs
	agents, err := h.kyvik.ListAgents(ctx)
	if err != nil {
		h.serverError(w, r, "failed to list agents", err)
		return
	}

	// List secrets for the active scope
	secrets, err := h.secrets.List(ctx, activeScope)
	if err != nil {
		h.serverError(w, r, "failed to list secrets", err)
		return
	}

	data := map[string]any{
		"Nav":         "secrets",
		"Title":       "Secrets",
		"ActiveScope": activeScope,
		"Agents":      agents,
		"Secrets":     secrets,
	}

	if isHTMX(r) {
		h.renderFragment(w, r, "secrets-list", data)
		return
	}

	h.renderPageWithRequest(w, r, "secrets-list", data)
}

// SecretsTableFragment renders just the secrets table for HTMX tab switching.
func (h *Handlers) SecretsTableFragment(w http.ResponseWriter, r *http.Request) {
	if h.secrets == nil {
		http.Error(w, "Secrets vault not configured", http.StatusServiceUnavailable)
		return
	}

	scope := r.URL.Query().Get("scope")
	if scope == "" {
		scope = "global"
	}

	secrets, err := h.secrets.List(r.Context(), scope)
	if err != nil {
		h.serverError(w, r, "failed to list secrets", err)
		return
	}

	data := map[string]any{
		"ActiveScope": scope,
		"Secrets":     secrets,
	}

	h.renderFragment(w, r, "secrets-table", data)
}

// SecretsCreate handles creating or updating a secret from the form.
func (h *Handlers) SecretsCreate(w http.ResponseWriter, r *http.Request) {
	if h.secrets == nil {
		http.Error(w, "Secrets vault not configured", http.StatusServiceUnavailable)
		return
	}

	scope := r.FormValue("scope")
	key := r.FormValue("key")
	value := r.FormValue("value")
	description := r.FormValue("description")

	if scope == "" || key == "" || value == "" {
		http.Error(w, "scope, key, and value are required", http.StatusBadRequest)
		return
	}

	if !h.validSecretScope(r.Context(), scope) {
		http.Error(w, "invalid scope", http.StatusBadRequest)
		return
	}

	if err := validateFormValue(key, 256); err != nil {
		http.Error(w, "invalid key: "+err.Error(), http.StatusBadRequest)
		return
	}

	if err := h.secrets.Set(r.Context(), scope, key, value, description); err != nil {
		h.serverError(w, r, "failed to save secret", err)
		return
	}

	// Return the updated table fragment
	secrets, err := h.secrets.List(r.Context(), scope)
	if err != nil {
		h.serverError(w, r, "failed to list secrets", err)
		return
	}

	data := map[string]any{
		"ActiveScope": scope,
		"Secrets":     secrets,
	}

	h.renderFragment(w, r, "secrets-table", data)
}

// SecretsDelete handles deleting a secret.
func (h *Handlers) SecretsDelete(w http.ResponseWriter, r *http.Request) {
	if h.secrets == nil {
		http.Error(w, "Secrets vault not configured", http.StatusServiceUnavailable)
		return
	}

	scope := r.FormValue("scope")
	key := r.FormValue("key")

	if scope == "" || key == "" {
		http.Error(w, "scope and key are required", http.StatusBadRequest)
		return
	}

	if !h.validSecretScope(r.Context(), scope) {
		http.Error(w, "invalid scope", http.StatusBadRequest)
		return
	}

	if err := h.secrets.Delete(r.Context(), scope, key); err != nil {
		h.serverError(w, r, "failed to delete secret", err)
		return
	}

	// Return the updated table fragment
	secrets, err := h.secrets.List(r.Context(), scope)
	if err != nil {
		h.serverError(w, r, "failed to list secrets", err)
		return
	}

	data := map[string]any{
		"ActiveScope": scope,
		"Secrets":     secrets,
	}

	h.renderFragment(w, r, "secrets-table", data)
}

// SecretsCopy returns the decrypted secret value as text/plain for clipboard copy.
func (h *Handlers) SecretsCopy(w http.ResponseWriter, r *http.Request) {
	if h.secrets == nil {
		http.Error(w, "Secrets vault not configured", http.StatusServiceUnavailable)
		return
	}

	scope := r.URL.Query().Get("scope")
	key := r.URL.Query().Get("key")

	if scope == "" || key == "" {
		http.Error(w, "scope and key are required", http.StatusBadRequest)
		return
	}

	value, err := h.secrets.Get(r.Context(), scope, key)
	if err != nil {
		h.serverError(w, r, "failed to get secret", err)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(value))
}
