package handlers

import (
	"net/http"
)

// SystemPage handles GET /system — renders database stats, retention config,
// and last prune result.
func (h *Handlers) SystemPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	data := map[string]any{
		"Nav":   "system",
		"Title": "System",
	}

	pruner := h.kyvik.Lifecycle.Pruner
	if pruner != nil {
		stats, err := pruner.Stats(ctx)
		if err == nil {
			data["Stats"] = stats
		}

		cfg := pruner.Config()
		data["RetentionConfig"] = cfg
		data["RetentionEnabled"] = cfg.Enabled != nil && *cfg.Enabled

		// Load last result from DB if not cached
		result := pruner.LastResult()
		if result == nil {
			result, _ = pruner.LoadLastResult(ctx)
		}
		data["LastResult"] = result
	}

	h.renderPageWithRequest(w, r, "system", data)
}

// SystemPruneNow handles POST /system/prune — triggers an immediate prune run.
func (h *Handlers) SystemPruneNow(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	pruner := h.kyvik.Lifecycle.Pruner
	if pruner == nil {
		http.Error(w, "retention pruner not configured", http.StatusServiceUnavailable)
		return
	}

	result := pruner.RunNow(ctx)

	// Re-fetch stats after prune
	stats, _ := pruner.Stats(ctx)

	data := map[string]any{
		"Stats":      stats,
		"LastResult": &result,
	}

	h.renderFragment(w, r, "system-result", data)
}
