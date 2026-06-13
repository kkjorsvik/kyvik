package handlers

import (
	"net/http"

	"github.com/kkjorsvik/kyvik/internal/guide"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// CreateGuideAgent handles creating the guide agent on demand from the
// dashboard banner. The guide is created in stopped state.
func (h *Handlers) CreateGuideAgent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if h.providerMgr == nil {
		http.Error(w, "Provider manager not configured", http.StatusServiceUnavailable)
		return
	}

	// Find the first enabled provider with a default model.
	var model types.ModelConfig
	if providers, err := h.providerMgr.ListProviders(ctx); err == nil {
		for _, p := range providers {
			if p.IsEnabled && p.DefaultModel != "" {
				model = types.ModelConfig{Provider: p.ProviderType, Model: p.DefaultModel}
				break
			}
		}
	}

	if model.Provider == "" {
		http.Error(w, "No provider with a default model is available", http.StatusBadRequest)
		return
	}

	_, err := guide.EnsureGuideAgent(ctx, guide.ProvisionDeps{
		Store:        h.kyvik,
		SkillManager: h.kyvik.SkillManager(),
		DefaultModel: model,
	})
	if err != nil {
		h.serverError(w, r, "creating guide agent", err)
		return
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// DismissGuideBanner dismisses the guide creation banner on the dashboard.
func (h *Handlers) DismissGuideBanner(w http.ResponseWriter, r *http.Request) {
	_ = h.kyvik.SetSystemState(r.Context(), "guide_banner_dismissed", "true")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
