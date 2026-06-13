package handlers

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/kkjorsvik/kyvik/internal/integrations"
	"github.com/kkjorsvik/kyvik/pkg/types"
	"gopkg.in/yaml.v3"
)

// SetIntegrationManager sets the integration manager on the handlers.
func (h *Handlers) SetIntegrationManager(m *integrations.Manager) {
	h.integrationMgr = m
}

// IntegrationList renders the integration catalog page.
func (h *Handlers) IntegrationList(w http.ResponseWriter, r *http.Request) {
	mgr := h.integrationMgr
	if mgr == nil {
		http.Error(w, "Integration manager not configured", http.StatusServiceUnavailable)
		return
	}

	category := types.IntegrationCategory(r.URL.Query().Get("category"))
	var templates []integrations.Template
	if category != "" {
		templates = mgr.AvailableByCategory(category)
	} else {
		templates = mgr.Available()
	}

	data := map[string]any{
		"Nav":            "integrations",
		"Title":          "Integrations",
		"Templates":      templates,
		"Categories":     mgr.Categories(),
		"ActiveCategory": string(category),
	}

	if isHTMX(r) {
		h.renderFragment(w, r, "integrations-list", data)
		return
	}
	h.renderPageWithRequest(w, r, "integrations-list", data)
}

// IntegrationDetail renders the detail/install page for an integration.
func (h *Handlers) IntegrationDetail(w http.ResponseWriter, r *http.Request) {
	mgr := h.integrationMgr
	if mgr == nil {
		http.Error(w, "Integration manager not configured", http.StatusServiceUnavailable)
		return
	}

	name := r.PathValue("name")
	tmpl, err := mgr.Get(name)
	if err != nil {
		http.Error(w, "Integration not found", http.StatusNotFound)
		return
	}

	// List all agents for the install form.
	agents, err := h.kyvik.ListAgents(r.Context())
	if err != nil {
		http.Error(w, "failed to list agents", http.StatusInternalServerError)
		return
	}

	data := map[string]any{
		"Nav":         "integrations",
		"Title":       tmpl.DisplayName,
		"Integration": tmpl,
		"Agents":      agents,
	}

	if r.URL.Query().Get("oauth") == "success" {
		data["OAuthSuccess"] = true
	}
	if r.URL.Query().Get("installed") == "true" {
		data["Installed"] = true
		data["InstalledAgentID"] = r.URL.Query().Get("agent_id")
	}

	if isHTMX(r) {
		h.renderFragment(w, r, "integrations-detail", data)
		return
	}
	h.renderPageWithRequest(w, r, "integrations-detail", data)
}

// IntegrationInstall handles installing an integration to an agent.
func (h *Handlers) IntegrationInstall(w http.ResponseWriter, r *http.Request) {
	mgr := h.integrationMgr
	if mgr == nil {
		http.Error(w, "Integration manager not configured", http.StatusServiceUnavailable)
		return
	}

	name := r.PathValue("name")
	agentID := r.FormValue("agent_id")
	authSecret := r.FormValue("auth_secret")

	if agentID == "" {
		http.Error(w, "agent_id is required", http.StatusBadRequest)
		return
	}

	// For OAuth2 integrations, store client_id and client_secret in vault before install.
	tmpl, err := mgr.Get(name)
	if err != nil {
		http.Error(w, "Integration not found", http.StatusNotFound)
		return
	}
	if tmpl.Auth.Type == "oauth2" && h.secrets != nil {
		clientID := r.FormValue("client_id")
		clientSecret := r.FormValue("client_secret")
		if clientID == "" || clientSecret == "" {
			http.Error(w, "client_id and client_secret are required for OAuth2 integrations", http.StatusBadRequest)
			return
		}
		clientIDKey := fmt.Sprintf("integrations/%s/%s", name, tmpl.Auth.ClientIDRef)
		clientSecretKey := fmt.Sprintf("integrations/%s/%s", name, tmpl.Auth.ClientSecretRef)
		if err := h.secrets.Set(r.Context(), agentID, clientIDKey, clientID, tmpl.DisplayName+" OAuth2 client ID"); err != nil {
			h.serverError(w, r, "storing OAuth2 client ID", err)
			return
		}
		if err := h.secrets.Set(r.Context(), agentID, clientSecretKey, clientSecret, tmpl.DisplayName+" OAuth2 client secret"); err != nil {
			h.serverError(w, r, "storing OAuth2 client secret", err)
			return
		}
	}

	// Collect variables from form.
	variables := make(map[string]string)
	_ = r.ParseForm()
	for key, values := range r.Form {
		if strings.HasPrefix(key, "var_") && len(values) > 0 {
			varName := strings.TrimPrefix(key, "var_")
			variables[varName] = values[0]
		}
	}

	err = mgr.Install(r.Context(), integrations.InstallRequest{
		AgentID:         agentID,
		IntegrationName: name,
		AuthSecret:      authSecret,
		Variables:        variables,
		InstalledBy:     "dashboard",
	})
	if err != nil {
		http.Error(w, "install failed", http.StatusBadRequest)
		return
	}

	redirect := "/integrations/" + name + "?installed=true&agent_id=" + url.QueryEscape(agentID)
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", redirect)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, redirect, http.StatusSeeOther)
}

// IntegrationUninstall removes an integration from an agent.
func (h *Handlers) IntegrationUninstall(w http.ResponseWriter, r *http.Request) {
	mgr := h.integrationMgr
	if mgr == nil {
		http.Error(w, "Integration manager not configured", http.StatusServiceUnavailable)
		return
	}

	name := r.PathValue("name")
	agentID := r.FormValue("agent_id")

	if agentID == "" {
		http.Error(w, "agent_id is required", http.StatusBadRequest)
		return
	}

	if err := mgr.Uninstall(r.Context(), agentID, name); err != nil {
		http.Error(w, "uninstall failed", http.StatusBadRequest)
		return
	}

	redirect := "/integrations/" + name
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", redirect)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, redirect, http.StatusSeeOther)
}

// IntegrationRefresh re-scans the integration templates and refreshes the catalog.
func (h *Handlers) IntegrationRefresh(w http.ResponseWriter, r *http.Request) {
	mgr := h.integrationMgr
	if mgr == nil {
		http.Error(w, "Integration manager not configured", http.StatusServiceUnavailable)
		return
	}

	if err := mgr.Refresh(); err != nil {
		h.serverError(w, r, "refreshing integrations catalog", err)
		return
	}

	redirect := "/integrations"
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", redirect)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, redirect, http.StatusSeeOther)
}

// IntegrationCreateForm renders the custom integration builder form.
func (h *Handlers) IntegrationCreateForm(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"Nav":   "integrations",
		"Title": "Create Custom Integration",
	}
	if isHTMX(r) {
		h.renderFragment(w, r, "integrations-new", data)
		return
	}
	h.renderPageWithRequest(w, r, "integrations-new", data)
}

// IntegrationCreatePost handles creating a custom integration template.
func (h *Handlers) IntegrationCreatePost(w http.ResponseWriter, r *http.Request) {
	mgr := h.integrationMgr
	if mgr == nil {
		http.Error(w, "Integration manager not configured", http.StatusServiceUnavailable)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	tmpl := integrations.Template{
		Name:        name,
		DisplayName: r.FormValue("display_name"),
		Description: r.FormValue("description"),
		Version:     "1.0.0",
		Category:    types.IntegrationCategory(r.FormValue("category")),
		Auth: integrations.TemplateAuth{
			Type:      r.FormValue("auth_type"),
			SecretRef: r.FormValue("auth_secret_ref"),
			HeaderName: r.FormValue("auth_header_name"),
			ParamName:  r.FormValue("auth_param_name"),
		},
	}

	if tmpl.DisplayName == "" {
		tmpl.DisplayName = name
	}
	if tmpl.Auth.Type == "" {
		tmpl.Auth.Type = "none"
	}

	// Parse endpoints from form.
	_ = r.ParseForm()
	epNames := r.Form["ep_name[]"]
	epMethods := r.Form["ep_method[]"]
	epURLs := r.Form["ep_url[]"]
	epDescs := r.Form["ep_description[]"]

	for i := range epNames {
		epName := strings.TrimSpace(epNames[i])
		if epName == "" {
			continue
		}
		ep := integrations.TemplateEndpoint{
			Name:        epName,
			Description: safeIndex(epDescs, i),
			Method:      safeIndex(epMethods, i),
			URL:         safeIndex(epURLs, i),
		}
		if ep.Method == "" {
			ep.Method = "GET"
		}
		tmpl.Endpoints = append(tmpl.Endpoints, ep)
	}

	if len(tmpl.Endpoints) == 0 {
		http.Error(w, "at least one endpoint is required", http.StatusBadRequest)
		return
	}

	// Optional system prompt.
	if prompt := r.FormValue("system_prompt"); prompt != "" {
		tmpl.Prompts = &integrations.TemplatePrompts{System: prompt}
	}

	if err := mgr.SaveLocal(tmpl); err != nil {
		h.serverError(w, r, "saving integration template", err)
		return
	}

	redirect := "/integrations/" + name
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", redirect)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, redirect, http.StatusSeeOther)
}

// IntegrationExportYAML exports an integration template as downloadable YAML.
func (h *Handlers) IntegrationExportYAML(w http.ResponseWriter, r *http.Request) {
	mgr := h.integrationMgr
	if mgr == nil {
		http.Error(w, "Integration manager not configured", http.StatusServiceUnavailable)
		return
	}

	name := r.PathValue("name")
	tmpl, err := mgr.Get(name)
	if err != nil {
		http.Error(w, "Integration not found", http.StatusNotFound)
		return
	}

	data, err := yaml.Marshal(tmpl)
	if err != nil {
		http.Error(w, "Export failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/x-yaml")
	w.Header().Set("Content-Disposition", "attachment; filename="+name+".yaml")
	w.Write(data)
}

// IntegrationDeleteLocal deletes a local integration template.
func (h *Handlers) IntegrationDeleteLocal(w http.ResponseWriter, r *http.Request) {
	mgr := h.integrationMgr
	if mgr == nil {
		http.Error(w, "Integration manager not configured", http.StatusServiceUnavailable)
		return
	}

	name := r.PathValue("name")
	if err := mgr.DeleteLocal(name); err != nil {
		http.Error(w, "delete failed", http.StatusBadRequest)
		return
	}

	redirect := "/integrations"
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", redirect)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, redirect, http.StatusSeeOther)
}

// AgentIntegrationsTab renders the integrations tab on the agent edit page.
func (h *Handlers) AgentIntegrationsTab(w http.ResponseWriter, r *http.Request) {
	mgr := h.integrationMgr
	if mgr == nil {
		http.Error(w, "Integration manager not configured", http.StatusServiceUnavailable)
		return
	}

	agentID := r.PathValue("id")
	installed, err := mgr.InstalledFor(r.Context(), agentID)
	if err != nil {
		h.serverError(w, r, "listing integrations", err)
		return
	}

	agent, err := h.kyvik.GetAgent(r.Context(), agentID)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	data := map[string]any{
		"Agent":     agent,
		"Installed": installed,
	}

	h.renderFragment(w, r, "agent-integrations-tab", data)
}

// NativeToolInstall handles installing a native tool to an agent.
func (h *Handlers) NativeToolInstall(w http.ResponseWriter, r *http.Request) {
	mgr := h.integrationMgr
	if mgr == nil {
		http.Error(w, "Integration manager not configured", http.StatusServiceUnavailable)
		return
	}
	name := r.PathValue("name")
	agentID := r.FormValue("agent_id")
	authSecret := r.FormValue("auth_secret")
	if agentID == "" {
		http.Error(w, "agent_id is required", http.StatusBadRequest)
		return
	}
	err := mgr.InstallNative(r.Context(), integrations.NativeInstallRequest{
		AgentID:     agentID,
		ToolName:    name,
		AuthSecret:  authSecret,
		InstalledBy: "dashboard",
	})
	if err != nil {
		http.Error(w, "install failed", http.StatusBadRequest)
		return
	}
	redirect := "/capabilities/" + name + "/install?installed=true&agent_id=" + url.QueryEscape(agentID)
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", redirect)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, redirect, http.StatusSeeOther)
}

// NativeToolUninstall handles removing a native tool grant from an agent.
func (h *Handlers) NativeToolUninstall(w http.ResponseWriter, r *http.Request) {
	mgr := h.integrationMgr
	if mgr == nil {
		http.Error(w, "Integration manager not configured", http.StatusServiceUnavailable)
		return
	}
	name := r.PathValue("name")
	agentID := r.FormValue("agent_id")
	if agentID == "" {
		http.Error(w, "agent_id is required", http.StatusBadRequest)
		return
	}
	if err := mgr.UninstallNative(r.Context(), agentID, name); err != nil {
		http.Error(w, "uninstall failed", http.StatusBadRequest)
		return
	}
	redirect := "/capabilities/" + name + "/install?agent_id=" + url.QueryEscape(agentID)
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", redirect)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, redirect, http.StatusSeeOther)
}

func safeIndex(s []string, i int) string {
	if i < len(s) {
		return strings.TrimSpace(s[i])
	}
	return ""
}
