package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/kkjorsvik/kyvik/internal/identity"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// AgentClone renders the wizard pre-populated with an existing agent's config (GET).
// Does NOT copy memories, history, secrets, or keys.
func (h *Handlers) AgentClone(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	config, err := h.kyvik.GetAgent(ctx, id)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	// Parse heartbeat config.
	var heartbeatConfig types.HeartbeatConfig
	if config.HeartbeatJSON != "" {
		json.Unmarshal([]byte(config.HeartbeatJSON), &heartbeatConfig)
	}

	// Serialize tool/capability grants.
	toolGrantsJSON := ""
	if len(config.ToolGrants) > 0 {
		tg, _ := json.Marshal(config.ToolGrants)
		toolGrantsJSON = string(tg)
	}
	capGrantsJSON := ""
	if len(config.CapabilityGrants) > 0 {
		cg, _ := json.Marshal(config.CapabilityGrants)
		capGrantsJSON = string(cg)
	}

	// Extract host paths.
	hostPathsRead := ""
	hostPathsWrite := ""
	hostPathsDeny := ""
	if config.HostPaths != nil {
		hostPathsRead = strings.Join(config.HostPaths.Read, "\n")
		hostPathsWrite = strings.Join(config.HostPaths.Write, "\n")
		hostPathsDeny = strings.Join(config.HostPaths.Deny, "\n")
	}

	// Build wizard data pre-populated from clone source.
	data := map[string]any{
		"Nav":                  "agents",
		"Title":                "Create Agent",
		"Name":                 config.Name + " (Copy)",
		"Description":          config.Description,
		"SystemPrompt":         config.SystemPrompt,
		"SoulContent":          config.SoulContent,
		"IdentityContent":      config.IdentityContent,
		"SoulTab":              "",
		"IdentityTab":          "",
		"Provider":             config.ModelConfig.Provider,
		"Model":                config.ModelConfig.Model,
		"ModelSlotsJSON":       config.ModelSlotsJSON,
		"RoutingConfigJSON":    config.RoutingConfigJSON,
		"SelectedTemplate":     config.Template,
		"MaxTokensPerDay":      fmtInt64(config.Limits.MaxTokensPerDay),
		"MaxTokensPerMonth":    fmtInt64(config.Limits.MaxTokensPerMonth),
		"MaxSpendPerDay":       fmtFloat64(config.Limits.MaxSpendPerDay),
		"MaxSpendPerMonth":     fmtFloat64(config.Limits.MaxSpendPerMonth),
		"HistoryLimit":         fmtInt(config.HistoryLimit),
		"MemoryLimit":          fmtInt(config.MemoryLimit),
		"AutoExtractMemories":  fmtBool(config.AutoExtractMemories),
		"TimestampMessages":    fmtBool(config.TimestampMessages),
		"MaxTotalTokens":       fmtInt(config.ContextBudget.MaxTotalTokens),
		"SoulIdentityPct":      fmtInt(config.ContextBudget.SoulIdentityPct),
		"SkillsPct":            fmtInt(config.ContextBudget.SkillsPct),
		"MemoriesPct":          fmtInt(config.ContextBudget.MemoriesPct),
		"HistoryPct":           fmtInt(config.ContextBudget.HistoryPct),
		"SlackMode":            config.SlackMode,
		"SlackChannel":         "", // Don't copy channel binding.
		"WebUIEnabled":         fmtBool(config.WebUIEnabled),
		"WorkersEnabled":       fmtBool(config.Workers.Enabled),
		"WorkersMaxConcurrent": fmtInt(config.Workers.MaxConcurrent),
		"WorkersTTLSeconds":    fmtInt(config.Workers.TTLSeconds),
		"WorkersModelSlot":     config.Workers.ModelSlot,
		"ToolGrantsJSON":       toolGrantsJSON,
		"CapabilityGrantsJSON": capGrantsJSON,
		"TierAcknowledged":     "",
		"TierConfirmName":      "",
		"HostPathsRead":        hostPathsRead,
		"HostPathsWrite":       hostPathsWrite,
		"HostPathsDeny":        hostPathsDeny,
		"HeartbeatEnabled":     fmtBool(heartbeatConfig.Enabled),
		"HeartbeatInterval":    heartbeatConfig.Interval,
		"HeartbeatPrompt":      heartbeatConfig.Prompt,
		"HeartbeatQuietHours":  heartbeatConfig.QuietHours,
		"HeartbeatPresets":     identity.GetHeartbeatPresets(),
		"Error":                "",
		"CloneSource":          config.Name,
	}

	h.renderPageWithRequest(w, r, "agents-new", data)
}

// AgentSaveAsTemplate saves the current agent's config as a new template (POST).
func (h *Handlers) AgentSaveAsTemplate(w http.ResponseWriter, r *http.Request) {
	if h.templateSvc == nil {
		http.Error(w, "template service not available", http.StatusServiceUnavailable)
		return
	}

	id := r.PathValue("id")
	ctx := r.Context()

	config, err := h.kyvik.GetAgent(ctx, id)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	name := r.FormValue("template_name")
	if name == "" {
		name = config.Name + " Template"
	}
	desc := r.FormValue("template_description")
	groupID := r.FormValue("template_group_id")

	var createdBy string
	if u, ok := currentDashboardUser(ctx); ok {
		createdBy = u.ID
	}

	tmpl, err := h.templateSvc.SaveFromAgent(ctx, *config, name, desc, groupID, createdBy)
	if err != nil {
		h.serverError(w, r, "saving template", err)
		return
	}

	if al := h.kyvik.Audit(); al != nil {
		al.Log(ctx, types.AuditEntry{
			AgentID:   id,
			EventType: "template",
			Action:    "template.saved_from_agent",
			Details:   "Saved agent '" + config.Name + "' as template '" + tmpl.Name + "'",
			Decision:  "allowed",
			RiskLevel: "low",
		})
	}

	w.Header().Set("HX-Redirect", "/templates")
	http.Redirect(w, r, "/templates?success=Template+saved.", http.StatusSeeOther)
}

func fmtInt(v int) string {
	if v == 0 {
		return ""
	}
	return fmt.Sprintf("%d", v)
}

func fmtInt64(v int64) string {
	if v == 0 {
		return ""
	}
	return fmt.Sprintf("%d", v)
}

func fmtFloat64(v float64) string {
	if v == 0 {
		return ""
	}
	return fmt.Sprintf("%g", v)
}

func fmtBool(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
