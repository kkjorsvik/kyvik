package handlers

import (
	"net/http"
	"net/url"

	"github.com/kkjorsvik/kyvik/internal/capabilities"
)

// CapabilitiesList renders the /capabilities page with all three capability types.
func (h *Handlers) CapabilitiesList(w http.ResponseWriter, r *http.Request) {
	resolver := h.capabilityResolver
	if resolver == nil {
		http.Error(w, "Capability resolver not configured", http.StatusServiceUnavailable)
		return
	}

	agentID := r.URL.Query().Get("agent_id")

	all := resolver.All()

	// Build per-agent status map if an agent is selected.
	var statusMap map[string]capabilities.AgentStatus
	var selectedAgent any
	if agentID != "" {
		ag, err := h.kyvik.GetAgent(r.Context(), agentID)
		if err == nil {
			selectedAgent = ag
			statusMap = make(map[string]capabilities.AgentStatus, len(all))
			for _, cap := range all {
				statusMap[cap.Name] = resolver.AgentCheck(r.Context(), cap, ag)
			}
		}
	}

	agents, _ := h.kyvik.ListAgents(r.Context())

	data := map[string]any{
		"Nav":          "capabilities",
		"Title":        "Capabilities",
		"Capabilities": all,
		"Agents":       agents,
		"AgentID":      agentID,
		"Agent":        selectedAgent,
		"StatusMap":    statusMap,
	}

	if isHTMX(r) {
		h.renderFragment(w, r, "capabilities-list", data)
		return
	}
	h.renderPageWithRequest(w, r, "capabilities-list", data)
}

// CapabilityInstallPage renders the install wizard for a single capability.
func (h *Handlers) CapabilityInstallPage(w http.ResponseWriter, r *http.Request) {
	resolver := h.capabilityResolver
	if resolver == nil {
		http.Error(w, "Capability resolver not configured", http.StatusServiceUnavailable)
		return
	}

	name := r.PathValue("name")
	agentID := r.URL.Query().Get("agent_id")

	info, err := resolver.Resolve(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	agents, _ := h.kyvik.ListAgents(r.Context())

	var agentStatus *capabilities.AgentStatus
	var selectedAgent any
	if agentID != "" {
		ag, err := h.kyvik.GetAgent(r.Context(), agentID)
		if err == nil {
			selectedAgent = ag
			st := resolver.AgentCheck(r.Context(), info, ag)
			agentStatus = &st
		}
	}

	data := map[string]any{
		"Nav":         "capabilities",
		"Title":       info.DisplayName,
		"Capability":  info,
		"Agents":      agents,
		"AgentID":     agentID,
		"Agent":       selectedAgent,
		"AgentStatus": agentStatus,
		"Installed":   r.URL.Query().Get("installed") == "true",
	}

	if isHTMX(r) {
		h.renderFragment(w, r, "capabilities-install", data)
		return
	}
	h.renderPageWithRequest(w, r, "capabilities-install", data)
}

// CapabilityInstallPost handles form submission for the install wizard.
func (h *Handlers) CapabilityInstallPost(w http.ResponseWriter, r *http.Request) {
	resolver := h.capabilityResolver
	if resolver == nil {
		http.Error(w, "Capability resolver not configured", http.StatusServiceUnavailable)
		return
	}

	name := r.PathValue("name")

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	agentID := r.FormValue("agent_id")
	if agentID == "" {
		http.Error(w, "agent_id is required", http.StatusBadRequest)
		return
	}

	// Collect credentials: form fields are named "cred_{credentialName}".
	creds := make(map[string]string)
	// Collect variables: form fields are named "var_{variableName}".
	vars := make(map[string]string)
	for key, vals := range r.Form {
		if len(vals) == 0 || vals[0] == "" {
			continue
		}
		if len(key) > 5 && key[:5] == "cred_" {
			creds[key[5:]] = vals[0]
		}
		if len(key) > 4 && key[:4] == "var_" {
			vars[key[4:]] = vals[0]
		}
	}

	upgradeTier := r.FormValue("upgrade_tier") == "true"

	err := resolver.Install(r.Context(), capabilities.InstallRequest{
		AgentID:     agentID,
		Name:        name,
		Credentials: creds,
		Variables:   vars,
		UpgradeTier: upgradeTier,
		InstalledBy: "dashboard",
	})
	if err != nil {
		http.Error(w, "install failed", http.StatusBadRequest)
		return
	}

	redirect := "/capabilities/" + url.PathEscape(name) + "/install?agent_id=" + url.QueryEscape(agentID) + "&installed=true"
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", redirect)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, redirect, http.StatusSeeOther)
}

// AgentCapabilitiesTab renders the capabilities tab on the agent edit page.
func (h *Handlers) AgentCapabilitiesTab(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if agentID == "" {
		agentID = r.URL.Query().Get("agent_id")
	}

	agent, err := h.kyvik.GetAgent(r.Context(), agentID)
	if err != nil {
		http.Error(w, "Agent not found", http.StatusNotFound)
		return
	}

	resolver := h.capabilityResolver

	// Collect installed capabilities per type.
	var installedSkills []capabilities.CapabilityInfo
	var installedIntegrations []capabilities.CapabilityInfo
	var installedNativeTools []capabilities.CapabilityInfo

	if resolver != nil {
		for _, cap := range resolver.All() {
			st := resolver.AgentCheck(r.Context(), cap, agent)
			if st.Installed {
				switch cap.Type {
				case capabilities.TypeSkill:
					installedSkills = append(installedSkills, *cap)
				case capabilities.TypeIntegration:
					installedIntegrations = append(installedIntegrations, *cap)
				case capabilities.TypeNativeTool:
					installedNativeTools = append(installedNativeTools, *cap)
				}
			}
		}
	}

	data := map[string]any{
		"Agent":                 agent,
		"InstalledSkills":       installedSkills,
		"InstalledIntegrations": installedIntegrations,
		"InstalledNativeTools":  installedNativeTools,
	}

	h.renderFragment(w, r, "agent-capabilities-tab", data)
}
