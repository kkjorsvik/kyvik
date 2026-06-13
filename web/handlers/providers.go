package handlers

import (
	"net/http"
	"strings"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

// ProviderList renders the providers management page.
func (h *Handlers) ProviderList(w http.ResponseWriter, r *http.Request) {
	if h.providerMgr == nil {
		http.Error(w, "Provider manager not configured", http.StatusServiceUnavailable)
		return
	}

	providers, err := h.providerMgr.ListProviders(r.Context())
	if err != nil {
		h.serverError(w, r, "listing providers", err)
		return
	}

	data := map[string]any{
		"Nav":             "providers",
		"Title":           "Providers",
		"Providers":       providers,
		"ValidTypes":      types.ValidProviderTypes,
	}

	if isHTMX(r) {
		h.renderFragment(w, r, "providers-list", data)
		return
	}
	h.renderPageWithRequest(w, r, "providers-list", data)
}

// ProviderNewForm renders the add-provider form.
func (h *Handlers) ProviderNewForm(w http.ResponseWriter, r *http.Request) {
	if h.providerMgr == nil {
		http.Error(w, "Provider manager not configured", http.StatusServiceUnavailable)
		return
	}

	data := map[string]any{
		"Nav":        "providers",
		"Title":      "Add Provider",
		"ValidTypes": types.ValidProviderTypes,
		"IsNew":      true,
	}

	if isHTMX(r) {
		h.renderFragment(w, r, "providers-form", data)
		return
	}
	h.renderPageWithRequest(w, r, "providers-form", data)
}

// ProviderCreate handles creating a new provider.
func (h *Handlers) ProviderCreate(w http.ResponseWriter, r *http.Request) {
	if h.providerMgr == nil {
		http.Error(w, "Provider manager not configured", http.StatusServiceUnavailable)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form data", http.StatusBadRequest)
		return
	}

	p := types.ProviderRecord{
		ProviderType:  r.FormValue("provider_type"),
		DisplayName:   r.FormValue("display_name"),
		BaseURL:       r.FormValue("base_url"),
		DefaultModel:  r.FormValue("default_model"),
		AllowedModels: splitModels(r.FormValue("allowed_models")),
		IsEnabled:     r.FormValue("is_enabled") == "on",
	}
	apiKey := strings.TrimSpace(r.FormValue("api_key"))

	if err := h.providerMgr.AddProvider(r.Context(), p, apiKey); err != nil {
		h.serverError(w, r, "creating provider", err)
		return
	}

	http.Redirect(w, r, "/providers", http.StatusSeeOther)
}

// ProviderEditForm renders the edit form for an existing provider.
func (h *Handlers) ProviderEditForm(w http.ResponseWriter, r *http.Request) {
	if h.providerMgr == nil {
		http.Error(w, "Provider manager not configured", http.StatusServiceUnavailable)
		return
	}

	id := r.PathValue("id")
	p, err := h.providerMgr.GetProvider(r.Context(), id)
	if err != nil {
		http.Error(w, "Provider not found", http.StatusNotFound)
		return
	}

	data := map[string]any{
		"Nav":        "providers",
		"Title":      "Edit Provider",
		"Provider":   p,
		"ValidTypes": types.ValidProviderTypes,
		"IsNew":      false,
	}

	if isHTMX(r) {
		h.renderFragment(w, r, "providers-form", data)
		return
	}
	h.renderPageWithRequest(w, r, "providers-form", data)
}

// ProviderUpdate handles updating an existing provider.
func (h *Handlers) ProviderUpdate(w http.ResponseWriter, r *http.Request) {
	if h.providerMgr == nil {
		http.Error(w, "Provider manager not configured", http.StatusServiceUnavailable)
		return
	}

	id := r.PathValue("id")
	existing, err := h.providerMgr.GetProvider(r.Context(), id)
	if err != nil {
		http.Error(w, "Provider not found", http.StatusNotFound)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form data", http.StatusBadRequest)
		return
	}

	existing.ProviderType = r.FormValue("provider_type")
	existing.DisplayName = r.FormValue("display_name")
	existing.BaseURL = r.FormValue("base_url")
	existing.DefaultModel = r.FormValue("default_model")
	existing.AllowedModels = splitModels(r.FormValue("allowed_models"))
	existing.IsEnabled = r.FormValue("is_enabled") == "on"

	apiKey := strings.TrimSpace(r.FormValue("api_key")) // empty = keep existing

	if err := h.providerMgr.UpdateProvider(r.Context(), *existing, apiKey); err != nil {
		h.serverError(w, r, "updating provider", err)
		return
	}

	http.Redirect(w, r, "/providers", http.StatusSeeOther)
}

// ProviderDelete handles deleting a provider.
func (h *Handlers) ProviderDelete(w http.ResponseWriter, r *http.Request) {
	if h.providerMgr == nil {
		http.Error(w, "Provider manager not configured", http.StatusServiceUnavailable)
		return
	}

	id := r.PathValue("id")
	if err := h.providerMgr.RemoveProvider(r.Context(), id); err != nil {
		h.serverError(w, r, "deleting provider", err)
		return
	}

	http.Redirect(w, r, "/providers", http.StatusSeeOther)
}

// ProviderToggle handles enabling/disabling a provider (HTMX fragment).
func (h *Handlers) ProviderToggle(w http.ResponseWriter, r *http.Request) {
	if h.providerMgr == nil {
		http.Error(w, "Provider manager not configured", http.StatusServiceUnavailable)
		return
	}

	id := r.PathValue("id")
	p, err := h.providerMgr.GetProvider(r.Context(), id)
	if err != nil {
		http.Error(w, "Provider not found", http.StatusNotFound)
		return
	}

	if err := h.providerMgr.ToggleProvider(r.Context(), id, !p.IsEnabled); err != nil {
		h.serverError(w, r, "toggling provider", err)
		return
	}

	// Re-render the provider list.
	h.ProviderList(w, r)
}

// ProviderTestConnection tests connectivity for a provider (HTMX fragment).
func (h *Handlers) ProviderTestConnection(w http.ResponseWriter, r *http.Request) {
	if h.providerMgr == nil {
		http.Error(w, "Provider manager not configured", http.StatusServiceUnavailable)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form data", http.StatusBadRequest)
		return
	}

	providerType := r.FormValue("provider_type")
	apiKey := strings.TrimSpace(r.FormValue("api_key"))
	baseURL := r.FormValue("base_url")

	models, err := h.providerMgr.TestConnection(r.Context(), providerType, apiKey, baseURL)
	if err != nil {
		h.renderFragment(w, r, "providers-test-result", map[string]any{
			"Success": false,
			"Error":   "Connection test failed",
		})
		return
	}

	h.renderFragment(w, r, "providers-test-result", map[string]any{
		"Success":    true,
		"ModelCount": len(models),
	})
}

// ProviderFetchModels returns available models for a provider (HTMX fragment).
func (h *Handlers) ProviderFetchModels(w http.ResponseWriter, r *http.Request) {
	if h.providerMgr == nil {
		http.Error(w, "Provider manager not configured", http.StatusServiceUnavailable)
		return
	}

	id := r.PathValue("id")
	models, err := h.providerMgr.FetchModels(r.Context(), id)
	if err != nil {
		h.renderFragment(w, r, "providers-models-list", map[string]any{
			"Error": "Failed to fetch models",
		})
		return
	}

	// Get current allowed models for checkbox state.
	p, _ := h.providerMgr.GetProvider(r.Context(), id)
	var allowed []string
	if p != nil {
		allowed = p.AllowedModels
	}

	h.renderFragment(w, r, "providers-models-list", map[string]any{
		"Models":        models,
		"AllowedModels": allowed,
		"ProviderID":    id,
	})
}

// ProviderSaveModels saves the allowed models list for a provider.
func (h *Handlers) ProviderSaveModels(w http.ResponseWriter, r *http.Request) {
	if h.providerMgr == nil {
		http.Error(w, "Provider manager not configured", http.StatusServiceUnavailable)
		return
	}

	id := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form data", http.StatusBadRequest)
		return
	}

	models := r.Form["models"]
	if err := h.providerMgr.UpdateAllowedModels(r.Context(), id, models); err != nil {
		h.serverError(w, r, "saving allowed models", err)
		return
	}

	http.Redirect(w, r, "/providers", http.StatusSeeOther)
}

func splitModels(s string) []string {
	if s == "" {
		return []string{}
	}
	var out []string
	for _, m := range strings.Split(s, ",") {
		m = strings.TrimSpace(m)
		if m != "" {
			out = append(out, m)
		}
	}
	if out == nil {
		return []string{}
	}
	return out
}
