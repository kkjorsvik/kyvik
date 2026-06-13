package handlers

import (
	"net/http"
	"strings"

	"github.com/kkjorsvik/kyvik/internal/skills"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// skillTierGroup groups skills by trust tier for display.
type skillTierGroup struct {
	Tier       types.TrustTier
	TierLabel  string
	BadgeClass string
	Skills     []types.Skill
}

// SkillsList renders the skills catalog page grouped by trust tier.
func (h *Handlers) SkillsList(w http.ResponseWriter, r *http.Request) {
	sm := h.kyvik.SkillManager()
	if sm == nil {
		http.Error(w, "Skill manager not configured", http.StatusServiceUnavailable)
		return
	}

	all := sm.Available()

	// Group by trust tier in display order.
	tierOrder := []struct {
		tier       types.TrustTier
		label      string
		badgeClass string
	}{
		{types.TrustBuiltIn, "Built-in", "badge-running"},
		{types.TrustVerified, "Verified", "badge-info"},
		{types.TrustCommunity, "Community", "badge-warning"},
		{types.TrustLocal, "Local", "badge-stopped"},
	}

	byTier := make(map[types.TrustTier][]types.Skill)
	for _, sk := range all {
		byTier[sk.Trust] = append(byTier[sk.Trust], sk)
	}

	var groups []skillTierGroup
	for _, t := range tierOrder {
		if skList, ok := byTier[t.tier]; ok && len(skList) > 0 {
			groups = append(groups, skillTierGroup{
				Tier:       t.tier,
				TierLabel:  t.label,
				BadgeClass: t.badgeClass,
				Skills:     skList,
			})
		}
	}

	data := map[string]any{
		"Nav":        "skills",
		"Title":      "Skills",
		"TierGroups": groups,
	}

	if isHTMX(r) {
		h.renderFragment(w, r, "skills-list", data)
		return
	}
	h.renderPageWithRequest(w, r, "skills-list", data)
}

// SkillDetail renders the detail page for a single skill.
func (h *Handlers) SkillDetail(w http.ResponseWriter, r *http.Request) {
	sm := h.kyvik.SkillManager()
	if sm == nil {
		http.Error(w, "Skill manager not configured", http.StatusServiceUnavailable)
		return
	}

	name := r.PathValue("name")
	ctx := r.Context()

	skill, err := sm.GetSkill(name)
	if err != nil {
		http.Error(w, "Skill not found", http.StatusNotFound)
		return
	}

	// List all agents to partition into granted vs available.
	agents, err := h.kyvik.ListAgents(ctx)
	if err != nil {
		h.serverError(w, r, "listing agents", err)
		return
	}

	// Get all grants for each agent to find who has this skill.
	type agentGrant struct {
		Agent types.AgentConfig
		Grant types.SkillGrant
	}
	var grantedAgents []agentGrant
	grantedSet := make(map[string]bool)

	for _, agent := range agents {
		grants, err := sm.ListGrants(ctx, agent.ID)
		if err != nil {
			continue
		}
		for _, gs := range grants {
			if gs.Grant.SkillName == name {
				grantedAgents = append(grantedAgents, agentGrant{
					Agent: agent,
					Grant: gs.Grant,
				})
				grantedSet[agent.ID] = true
				break
			}
		}
	}

	// Filter available agents: not already granted, and compatible.
	compatible := sm.AvailableForAgent
	var availableAgents []types.AgentConfig
	for _, agent := range agents {
		if grantedSet[agent.ID] {
			continue
		}
		// Check if agent meets skill requirements.
		compatSkills := compatible(agent)
		for _, cs := range compatSkills {
			if cs.Name == name {
				availableAgents = append(availableAgents, agent)
				break
			}
		}
	}

	data := map[string]any{
		"Nav":              "skills",
		"Title":            skill.Name,
		"Skill":            skill,
		"GrantedAgents":    grantedAgents,
		"AvailableAgents":  availableAgents,
		"TrustWarning":     skills.TrustWarning(skill.Trust),
		"RequiresApproval": skills.RequiresApproval(skill.Trust),
	}

	if isHTMX(r) {
		h.renderFragment(w, r, "skills-detail", data)
		return
	}
	h.renderPageWithRequest(w, r, "skills-detail", data)
}

// SkillGrant handles granting a skill to an agent.
func (h *Handlers) SkillGrant(w http.ResponseWriter, r *http.Request) {
	sm := h.kyvik.SkillManager()
	if sm == nil {
		http.Error(w, "Skill manager not configured", http.StatusServiceUnavailable)
		return
	}

	name := r.PathValue("name")
	agentID := r.FormValue("agent_id")
	ctx := r.Context()

	if agentID == "" {
		http.Error(w, "agent_id is required", http.StatusBadRequest)
		return
	}

	agentConfig, err := h.kyvik.GetAgent(ctx, agentID)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	if err := sm.Grant(ctx, agentID, name, "dashboard", *agentConfig); err != nil {
		http.Error(w, "failed to grant skill", http.StatusBadRequest)
		return
	}

	redirect := "/skills/" + name
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", redirect)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, redirect, http.StatusSeeOther)
}

// SkillRevoke handles revoking a skill from an agent.
func (h *Handlers) SkillRevoke(w http.ResponseWriter, r *http.Request) {
	sm := h.kyvik.SkillManager()
	if sm == nil {
		http.Error(w, "Skill manager not configured", http.StatusServiceUnavailable)
		return
	}

	name := r.PathValue("name")
	agentID := r.FormValue("agent_id")
	ctx := r.Context()

	if agentID == "" {
		http.Error(w, "agent_id is required", http.StatusBadRequest)
		return
	}

	if err := sm.Revoke(ctx, agentID, name); err != nil {
		h.serverError(w, r, "revoking skill", err)
		return
	}

	redirect := "/skills/" + name
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", redirect)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, redirect, http.StatusSeeOther)
}

// SkillRefresh re-scans the skill directories and refreshes the catalog.
func (h *Handlers) SkillRefresh(w http.ResponseWriter, r *http.Request) {
	sm := h.kyvik.SkillManager()
	if sm == nil {
		http.Error(w, "Skill manager not configured", http.StatusServiceUnavailable)
		return
	}

	if err := sm.Refresh(); err != nil {
		h.serverError(w, r, "refreshing skills", err)
		return
	}

	redirect := "/skills"
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", redirect)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, redirect, http.StatusSeeOther)
}

// SkillCreateForm renders the custom skill builder form.
func (h *Handlers) SkillCreateForm(w http.ResponseWriter, r *http.Request) {
	sm := h.kyvik.SkillManager()
	if sm == nil {
		http.Error(w, "Skill manager not configured", http.StatusServiceUnavailable)
		return
	}

	// List available KTP tools for the required_tools checkboxes.
	ktpReg := h.kyvik.KTPRegistry()
	var toolNames []string
	if ktpReg != nil {
		for _, decl := range ktpReg.List() {
			toolNames = append(toolNames, decl.Name)
		}
	}

	data := map[string]any{
		"Nav":       "skills",
		"Title":     "Create Custom Skill",
		"ToolNames": toolNames,
	}

	if isHTMX(r) {
		h.renderFragment(w, r, "skills-new", data)
		return
	}
	h.renderPageWithRequest(w, r, "skills-new", data)
}

// SkillCreatePost handles creating a custom skill from the form.
func (h *Handlers) SkillCreatePost(w http.ResponseWriter, r *http.Request) {
	sm := h.kyvik.SkillManager()
	if sm == nil {
		http.Error(w, "Skill manager not configured", http.StatusServiceUnavailable)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	manifest := types.SkillManifest{
		Name:        name,
		Version:     r.FormValue("version"),
		Description: r.FormValue("description"),
		Author:      r.FormValue("author"),
	}
	if manifest.Version == "" {
		manifest.Version = "1.0.0"
	}

	// Collect required tools from form checkboxes.
	_ = r.ParseForm()
	manifest.RequiredTools = r.Form["required_tools"]

	// Sandbox config.
	if r.FormValue("allow_network") == "on" {
		manifest.Sandbox = &types.SkillSandboxConfig{
			AllowNetwork: true,
		}
		if hosts := strings.TrimSpace(r.FormValue("allowed_hosts")); hosts != "" {
			manifest.Sandbox.AllowedHosts = strings.Split(hosts, "\n")
			for i := range manifest.Sandbox.AllowedHosts {
				manifest.Sandbox.AllowedHosts[i] = strings.TrimSpace(manifest.Sandbox.AllowedHosts[i])
			}
		}
	}

	systemPrompt := r.FormValue("system_prompt")

	if err := sm.SaveLocal(manifest, systemPrompt); err != nil {
		http.Error(w, "save failed", http.StatusBadRequest)
		return
	}

	redirect := "/skills/" + name
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", redirect)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, redirect, http.StatusSeeOther)
}

// SkillDeleteLocal deletes a local skill.
func (h *Handlers) SkillDeleteLocal(w http.ResponseWriter, r *http.Request) {
	sm := h.kyvik.SkillManager()
	if sm == nil {
		http.Error(w, "Skill manager not configured", http.StatusServiceUnavailable)
		return
	}

	name := r.PathValue("name")
	if err := sm.DeleteLocal(name); err != nil {
		http.Error(w, "delete failed", http.StatusBadRequest)
		return
	}

	redirect := "/skills"
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", redirect)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, redirect, http.StatusSeeOther)
}
