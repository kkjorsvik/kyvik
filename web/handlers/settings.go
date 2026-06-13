package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/kkjorsvik/kyvik/internal/store"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

const cbDefaultsKey = "circuit_breaker_defaults"

// SettingsPage handles GET /settings — renders the settings page with tabs.
func (h *Handlers) SettingsPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tab := r.URL.Query().Get("tab")
	if tab == "" {
		tab = "circuit-breaker"
	}

	data := map[string]any{
		"Nav":       "settings",
		"Title":     "Settings",
		"ActiveTab": tab,
	}

	switch tab {
	case "circuit-breaker":
		h.loadCircuitBreakerTab(ctx, data)
	case "system":
		h.loadSystemTab(ctx, data)
	case "obsidian-vaults":
		h.loadObsidianVaultsTab(ctx, data)
	}

	if isHTMX(r) {
		h.renderFragment(w, r, "settings-tab-"+tab, data)
		return
	}
	h.renderPageWithRequest(w, r, "settings", data)
}

func (h *Handlers) loadCircuitBreakerTab(_ context.Context, data map[string]any) {
	bm := h.kyvik.Lifecycle.Breaker
	if bm != nil {
		data["SystemDefaults"] = bm.SystemDefaults()
	} else {
		data["SystemDefaults"] = types.DefaultCircuitBreakerConfig()
	}
	data["HardcodedDefaults"] = types.DefaultCircuitBreakerConfig()
}

func (h *Handlers) loadSystemTab(ctx context.Context, data map[string]any) {
	pruner := h.kyvik.Lifecycle.Pruner
	if pruner != nil {
		stats, err := pruner.Stats(ctx)
		if err == nil {
			data["Stats"] = stats
		}
		cfg := pruner.Config()
		data["RetentionConfig"] = cfg
		data["RetentionEnabled"] = cfg.Enabled != nil && *cfg.Enabled

		result := pruner.LastResult()
		if result == nil {
			result, _ = pruner.LoadLastResult(ctx)
		}
		data["LastResult"] = result
	}
}

// settingsParseInt parses a form value as int, falling back to a default.
func settingsParseInt(raw string, fallback int) int {
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || v <= 0 {
		return fallback
	}
	return v
}

// SettingsCircuitBreakerSave handles POST /settings/circuit-breaker.
func (h *Handlers) SettingsCircuitBreakerSave(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	hardcoded := types.DefaultCircuitBreakerConfig()
	cfg := types.CircuitBreakerConfig{
		Enabled:               r.FormValue("circuit_breaker_enabled") == "true",
		ErrorThreshold:        settingsParseInt(r.FormValue("circuit_breaker_error_threshold"), hardcoded.ErrorThreshold),
		ErrorWindowMinutes:    settingsParseInt(r.FormValue("circuit_breaker_error_window_minutes"), hardcoded.ErrorWindowMinutes),
		SpendingVelocityPct:   settingsParseInt(r.FormValue("circuit_breaker_spending_velocity_pct"), hardcoded.SpendingVelocityPct),
		SpendingWindowMinutes: settingsParseInt(r.FormValue("circuit_breaker_spending_window_minutes"), hardcoded.SpendingWindowMinutes),
		ActionRatePerMinute:   settingsParseInt(r.FormValue("circuit_breaker_action_rate_per_minute"), hardcoded.ActionRatePerMinute),
		DestructiveLimit:      settingsParseInt(r.FormValue("circuit_breaker_destructive_limit"), hardcoded.DestructiveLimit),
		LoopIdenticalCount:    settingsParseInt(r.FormValue("circuit_breaker_loop_identical_count"), hardcoded.LoopIdenticalCount),
	}

	// Persist to system_state.
	b, _ := json.Marshal(cfg)
	s, ok := h.kyvik.Store().(store.Store)
	if !ok || s == nil {
		http.Error(w, "store not available", http.StatusInternalServerError)
		return
	}
	if err := s.SetSystemState(ctx, cbDefaultsKey, string(b)); err != nil {
		h.serverError(w, r, "saving circuit breaker settings", err)
		return
	}

	// Update in-memory manager defaults.
	if bm := h.kyvik.Lifecycle.Breaker; bm != nil {
		bm.SetSystemDefaults(cfg)
	}

	// Re-render the circuit breaker tab.
	data := map[string]any{}
	h.loadCircuitBreakerTab(ctx, data)
	h.renderFragment(w, r, "settings-tab-circuit-breaker", data)
}
