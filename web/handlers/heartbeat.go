package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/kkjorsvik/kyvik/internal/identity"
	"github.com/kkjorsvik/kyvik/internal/timeutil"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// HeartbeatSection renders the heartbeat card fragment for an agent.
func (h *Handlers) HeartbeatSection(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	config, err := h.kyvik.GetAgent(ctx, id)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	data := h.buildAgentDetailData(ctx, config)
	h.renderFragment(w, r, "card-heartbeat", data)
}

// HeartbeatUpdate updates the heartbeat configuration for an agent.
func (h *Handlers) HeartbeatUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	config, err := h.kyvik.GetAgent(ctx, id)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	r.ParseForm()
	hbCfg := types.HeartbeatConfig{
		Enabled:    r.FormValue("heartbeat_enabled") == "true",
		Interval:   r.FormValue("heartbeat_interval"),
		Prompt:     r.FormValue("heartbeat_prompt"),
		QuietHours: r.FormValue("heartbeat_quiet_hours"),
	}

	// Apply preset if selected.
	if presetID := r.FormValue("heartbeat_preset"); presetID != "" && presetID != "custom" {
		if preset := identity.GetHeartbeatPreset(presetID); preset != nil {
			hbCfg.Prompt = preset.Content
		}
	}

	hbJSON, _ := json.Marshal(hbCfg)
	config.HeartbeatJSON = string(hbJSON)
	config.UpdatedAt = timeutil.NowUTC()

	if err := h.kyvik.UpdateAgent(ctx, *config); err != nil {
		http.Error(w, fmt.Sprintf("failed to update agent: %v", err), http.StatusInternalServerError)
		return
	}

	// Register/update the heartbeat schedule.
	if sched := h.kyvik.Lifecycle.Scheduler; sched != nil {
		if hbCfg.Enabled {
			if err := sched.RegisterHeartbeat(ctx, id, hbCfg, h.configuredTimezone()); err != nil {
				http.Error(w, fmt.Sprintf("failed to register heartbeat: %v", err), http.StatusInternalServerError)
				return
			}
		} else {
			_ = sched.UnregisterHeartbeat(ctx, id)
		}
	}

	h.HeartbeatSection(w, r)
}

// HeartbeatToggle enables or disables the heartbeat for an agent.
func (h *Handlers) HeartbeatToggle(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	config, err := h.kyvik.GetAgent(ctx, id)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	var hbCfg types.HeartbeatConfig
	if config.HeartbeatJSON != "" {
		json.Unmarshal([]byte(config.HeartbeatJSON), &hbCfg)
	}

	hbCfg.Enabled = !hbCfg.Enabled
	hbJSON, _ := json.Marshal(hbCfg)
	config.HeartbeatJSON = string(hbJSON)
	config.UpdatedAt = timeutil.NowUTC()

	if err := h.kyvik.UpdateAgent(ctx, *config); err != nil {
		http.Error(w, fmt.Sprintf("failed to update agent: %v", err), http.StatusInternalServerError)
		return
	}

	// Toggle the heartbeat schedule.
	if sched := h.kyvik.Lifecycle.Scheduler; sched != nil {
		if hbCfg.Enabled {
			_ = sched.EnableHeartbeat(ctx, id)
		} else {
			_ = sched.UnregisterHeartbeat(ctx, id)
		}
	}

	h.HeartbeatSection(w, r)
}

// HeartbeatPulseNow fires an immediate heartbeat for an agent.
func (h *Handlers) HeartbeatPulseNow(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	sched := h.kyvik.Lifecycle.Scheduler
	if sched == nil {
		http.Error(w, "scheduler not enabled", http.StatusBadRequest)
		return
	}

	if err := sched.PulseNow(ctx, id); err != nil {
		http.Error(w, fmt.Sprintf("failed to pulse: %v", err), http.StatusInternalServerError)
		return
	}

	h.HeartbeatSection(w, r)
}
