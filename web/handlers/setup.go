package handlers

import (
	"net/http"
	"strings"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

// SetupCheck is middleware that redirects to /setup when no providers are
// configured and the setup wizard hasn't been completed yet.
func (h *Handlers) SetupCheck(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip non-page paths.
		path := r.URL.Path
		if strings.HasPrefix(path, "/setup") ||
			strings.HasPrefix(path, "/login") ||
			strings.HasPrefix(path, "/logout") ||
			strings.HasPrefix(path, "/static") ||
			strings.HasPrefix(path, "/api") ||
			strings.HasPrefix(path, "/auth/") ||
			strings.HasPrefix(path, "/password") ||
			path == "/providers/test" {
			next.ServeHTTP(w, r)
			return
		}

		// If setup is already complete, pass through.
		if done, _ := h.kyvik.GetSystemState(r.Context(), "setup_complete"); done == "true" {
			next.ServeHTTP(w, r)
			return
		}

		// If providers exist, setup is implicitly complete.
		if h.providerMgr != nil {
			if providers, err := h.providerMgr.ListProviders(r.Context()); err == nil && len(providers) > 0 {
				_ = h.kyvik.SetSystemState(r.Context(), "setup_complete", "true")
				next.ServeHTTP(w, r)
				return
			}
		}

		http.Redirect(w, r, "/setup", http.StatusTemporaryRedirect)
	})
}

// SetupWizard renders the setup wizard page.
func (h *Handlers) SetupWizard(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"Nav":        "",
		"Title":      "Setup",
		"ValidTypes": types.ValidProviderTypes,
	}
	h.renderPageWithRequest(w, r, "setup-wizard", data)
}

// SetupComplete handles the setup wizard form submission.
// It creates the provider and marks setup as complete.
func (h *Handlers) SetupComplete(w http.ResponseWriter, r *http.Request) {
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
		DefaultModel:  r.FormValue("default_model"),
		BaseURL:       r.FormValue("base_url"),
		AllowedModels: splitModels(r.FormValue("allowed_models")),
		IsEnabled:     true,
	}
	apiKey := strings.TrimSpace(r.FormValue("api_key"))

	if p.ProviderType == "" || apiKey == "" {
		http.Error(w, "provider type and API key are required", http.StatusBadRequest)
		return
	}

	if p.DisplayName == "" {
		p.DisplayName = p.ProviderType
	}

	if err := h.providerMgr.AddProvider(r.Context(), p, apiKey); err != nil {
		h.serverError(w, r, "creating provider during setup", err)
		return
	}

	_ = h.kyvik.SetSystemState(r.Context(), "setup_complete", "true")

	http.Redirect(w, r, "/", http.StatusSeeOther)
}
