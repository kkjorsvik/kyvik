package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

// templateListItem wraps AgentTemplate with extracted metadata for display.
type templateListItem struct {
	types.AgentTemplate
	SetupTier  string
	SetupNotes string
	IsBuiltin  bool
}

// TemplatesList renders the template management page.
func (h *Handlers) TemplatesList(w http.ResponseWriter, r *http.Request) {
	if h.templateSvc == nil {
		http.Error(w, "template service not available", http.StatusServiceUnavailable)
		return
	}

	ctx := r.Context()
	rawTemplates, err := h.templateSvc.List(ctx)
	if err != nil {
		h.serverError(w, r, "loading templates", err)
		return
	}

	// Extract metadata from each template's config for display.
	items := make([]templateListItem, 0, len(rawTemplates))
	for _, tmpl := range rawTemplates {
		item := templateListItem{AgentTemplate: tmpl}
		var cfg struct {
			Metadata map[string]string `json:"metadata"`
		}
		if err := json.Unmarshal([]byte(tmpl.ConfigJSON), &cfg); err == nil && cfg.Metadata != nil {
			item.SetupTier = cfg.Metadata["setup_tier"]
			item.SetupNotes = cfg.Metadata["setup_notes"]
			item.IsBuiltin = cfg.Metadata["builtin"] == "true"
		}
		items = append(items, item)
	}

	// Load groups for display.
	var groups []types.AgentGroup
	if h.userSvc != nil {
		groups, _ = h.userSvc.ListGroups(ctx)
	}
	groupMap := make(map[string]string, len(groups))
	for _, g := range groups {
		groupMap[g.ID] = g.Name
	}

	data := map[string]any{
		"Nav":       "templates",
		"Title":     "Agent Templates",
		"Templates": items,
		"GroupMap":   groupMap,
		"Groups":    groups,
		"Providers": h.kyvik.ListProviders(),
		"Success":   r.URL.Query().Get("success"),
		"Error":     r.URL.Query().Get("error"),
	}
	h.renderPageWithRequest(w, r, "templates-list", data)
}

// TemplateCreate handles POST /templates to create a new template from scratch.
func (h *Handlers) TemplateCreate(w http.ResponseWriter, r *http.Request) {
	if h.templateSvc == nil {
		http.Error(w, "template service not available", http.StatusServiceUnavailable)
		return
	}

	ctx := r.Context()
	name := r.FormValue("name")
	desc := r.FormValue("description")
	groupID := r.FormValue("group_id")

	if name == "" {
		redirectWithNotice(w, r, "/templates", "error", "Template name is required.")
		return
	}

	// Build full AgentConfig from form fields.
	cfg := buildConfigFromRequest(r)

	// Parse locked fields and constrained fields from JSON.
	var locked []string
	if lf := r.FormValue("locked_fields"); lf != "" {
		json.Unmarshal([]byte(lf), &locked)
	}
	var constrained map[string]types.ConstraintRule
	if cf := r.FormValue("constrained_fields"); cf != "" {
		json.Unmarshal([]byte(cf), &constrained)
	}

	var createdBy string
	if u, ok := currentDashboardUser(ctx); ok {
		createdBy = u.ID
	}

	tmpl, err := h.templateSvc.Create(ctx, name, desc, groupID, createdBy, cfg, locked, constrained)
	if err != nil {
		redirectWithNotice(w, r, "/templates", "error", "Failed to create template")
		return
	}

	// Audit log.
	if al := h.kyvik.Audit(); al != nil {
		al.Log(ctx, types.AuditEntry{
			EventType: "template",
			Action:    "template.created",
			Details:   "Created template: " + tmpl.Name,
			Decision:  "allowed",
			RiskLevel: "low",
		})
	}

	redirectWithNotice(w, r, "/templates", "success", "Template '"+tmpl.Name+"' created.")
}

// TemplateEdit renders the edit form for a template.
func (h *Handlers) TemplateEdit(w http.ResponseWriter, r *http.Request) {
	if h.templateSvc == nil {
		http.Error(w, "template service not available", http.StatusServiceUnavailable)
		return
	}

	ctx := r.Context()
	id := r.PathValue("id")

	tmpl, err := h.templateSvc.Get(ctx, id)
	if err != nil {
		http.Error(w, "template not found", http.StatusNotFound)
		return
	}

	// Deserialize config for display.
	var cfg types.AgentConfig
	json.Unmarshal([]byte(tmpl.ConfigJSON), &cfg)

	var groups []types.AgentGroup
	if h.userSvc != nil {
		groups, _ = h.userSvc.ListGroups(ctx)
	}

	lockedJSON, _ := json.Marshal(tmpl.LockedFields)
	constrainedJSON, _ := json.Marshal(tmpl.ConstrainedFields)

	data := map[string]any{
		"Nav":              "templates",
		"Title":            "Edit Template — " + tmpl.Name,
		"Template":         tmpl,
		"Config":           cfg,
		"Groups":           groups,
		"Providers":        h.kyvik.ListProviders(),
		"LockedJSON":       string(lockedJSON),
		"ConstrainedJSON":  string(constrainedJSON),
	}
	h.renderPageWithRequest(w, r, "templates-edit", data)
}

// TemplateEditPost handles POST /templates/{id}/edit.
func (h *Handlers) TemplateEditPost(w http.ResponseWriter, r *http.Request) {
	if h.templateSvc == nil {
		http.Error(w, "template service not available", http.StatusServiceUnavailable)
		return
	}

	ctx := r.Context()
	id := r.PathValue("id")

	tmpl, err := h.templateSvc.Get(ctx, id)
	if err != nil {
		http.Error(w, "template not found", http.StatusNotFound)
		return
	}

	tmpl.Name = r.FormValue("name")
	tmpl.Description = r.FormValue("description")
	tmpl.GroupID = r.FormValue("group_id")

	// Rebuild config from form fields.
	cfg := buildConfigFromRequest(r)
	configJSON, _ := json.Marshal(cfg)
	tmpl.ConfigJSON = string(configJSON)

	if lf := r.FormValue("locked_fields"); lf != "" {
		json.Unmarshal([]byte(lf), &tmpl.LockedFields)
	}
	if cf := r.FormValue("constrained_fields"); cf != "" {
		json.Unmarshal([]byte(cf), &tmpl.ConstrainedFields)
	}

	if err := h.templateSvc.Update(ctx, *tmpl); err != nil {
		redirectWithNotice(w, r, "/templates/"+id+"/edit", "error", "Failed to update template")
		return
	}

	if al := h.kyvik.Audit(); al != nil {
		al.Log(ctx, types.AuditEntry{
			EventType: "template",
			Action:    "template.updated",
			Details:   "Updated template: " + tmpl.Name,
			Decision:  "allowed",
			RiskLevel: "low",
		})
	}

	redirectWithNotice(w, r, "/templates", "success", "Template '"+tmpl.Name+"' updated.")
}

// TemplateDelete handles POST /templates/{id}/delete.
func (h *Handlers) TemplateDelete(w http.ResponseWriter, r *http.Request) {
	if h.templateSvc == nil {
		http.Error(w, "template service not available", http.StatusServiceUnavailable)
		return
	}

	ctx := r.Context()
	id := r.PathValue("id")

	tmpl, err := h.templateSvc.Get(ctx, id)
	if err != nil {
		http.Error(w, "template not found", http.StatusNotFound)
		return
	}

	if err := h.templateSvc.Delete(ctx, id); err != nil {
		redirectWithNotice(w, r, "/templates", "error", "Failed to delete template")
		return
	}

	if al := h.kyvik.Audit(); al != nil {
		al.Log(ctx, types.AuditEntry{
			EventType: "template",
			Action:    "template.deleted",
			Details:   "Deleted template: " + tmpl.Name,
			Decision:  "allowed",
			RiskLevel: "medium",
		})
	}

	redirectWithNotice(w, r, "/templates", "success", "Template '"+tmpl.Name+"' deleted.")
}

// TemplatePrefill returns the template's config as wizard form hidden fields (HTMX fragment).
func (h *Handlers) TemplatePrefill(w http.ResponseWriter, r *http.Request) {
	if h.templateSvc == nil {
		http.Error(w, "template service not available", http.StatusServiceUnavailable)
		return
	}

	ctx := r.Context()
	id := r.PathValue("id")

	cfg, err := h.templateSvc.ConfigFromTemplate(ctx, id)
	if err != nil {
		http.Error(w, "template not found", http.StatusNotFound)
		return
	}

	tmpl, _ := h.templateSvc.Get(ctx, id)
	lockedJSON, _ := json.Marshal(tmpl.LockedFields)
	constrainedJSON, _ := json.Marshal(tmpl.ConstrainedFields)

	// Encode model slots if present.
	slotsJSON := cfg.ModelSlotsJSON
	routingJSON := cfg.RoutingConfigJSON

	// Encode tool grants as JSON for hidden fields.
	var toolGrantsJSON string
	if len(cfg.ToolGrants) > 0 {
		if b, err := json.Marshal(cfg.ToolGrants); err == nil {
			toolGrantsJSON = string(b)
		}
	}

	data := map[string]any{
		"Config":          cfg,
		"TemplateID":      id,
		"LockedFields":    tmpl.LockedFields,
		"LockedJSON":      string(lockedJSON),
		"ConstrainedJSON": string(constrainedJSON),
		"SlotsJSON":       slotsJSON,
		"RoutingJSON":     routingJSON,
		"ToolGrantsJSON":  toolGrantsJSON,
	}

	h.renderFragment(w, r, "template-prefill", data)
}

