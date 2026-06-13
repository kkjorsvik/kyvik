package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kkjorsvik/kyvik/pkg/types"
	"gopkg.in/yaml.v3"
)

// AgentStore is the narrow interface the manager needs for agent CRUD.
type AgentStore interface {
	GetAgent(ctx context.Context, id string) (*types.AgentConfig, error)
	UpdateAgent(ctx context.Context, agent types.AgentConfig) error
}

// SecretStore is the narrow interface the manager needs for storing credentials.
type SecretStore interface {
	Set(ctx context.Context, scope, key, plaintext, description string) error
}

// Manager is the business logic layer for integration templates.
type Manager struct {
	loader  *Loader
	store   AgentStore
	secrets SecretStore
	catalog map[string]*Template // name → Template
	mu      sync.RWMutex
}

// NewManager creates a Manager, loading the initial catalog from the Loader.
func NewManager(loader *Loader, store AgentStore, secrets SecretStore) (*Manager, error) {
	m := &Manager{
		loader:  loader,
		store:   store,
		secrets: secrets,
		catalog: make(map[string]*Template),
	}
	if err := m.buildCatalog(); err != nil {
		return nil, fmt.Errorf("build integration catalog: %w", err)
	}
	return m, nil
}

// buildCatalog calls loader.LoadAll and populates the catalog map.
func (m *Manager) buildCatalog() error {
	templates, err := m.loader.LoadAll()
	if err != nil {
		return err
	}
	cat := make(map[string]*Template, len(templates))
	for i := range templates {
		cat[templates[i].Name] = &templates[i]
	}
	m.catalog = cat
	return nil
}

// Available returns all catalog entries sorted by display name.
func (m *Manager) Available() []Template {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Template, 0, len(m.catalog))
	for _, t := range m.catalog {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].DisplayName < out[j].DisplayName
	})
	return out
}

// AvailableByCategory returns templates filtered by category.
func (m *Manager) AvailableByCategory(category types.IntegrationCategory) []Template {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Template
	for _, t := range m.catalog {
		if t.Category == category {
			out = append(out, *t)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].DisplayName < out[j].DisplayName
	})
	return out
}

// Get returns a single template by name.
func (m *Manager) Get(name string) (*Template, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.catalog[name]
	if !ok {
		return nil, fmt.Errorf("integration %q not found", name)
	}
	copy := *t
	return &copy, nil
}

// Categories returns all unique categories in the catalog.
func (m *Manager) Categories() []types.IntegrationCategory {
	m.mu.RLock()
	defer m.mu.RUnlock()
	seen := make(map[types.IntegrationCategory]bool)
	for _, t := range m.catalog {
		seen[t.Category] = true
	}
	out := make([]types.IntegrationCategory, 0, len(seen))
	for cat := range seen {
		out = append(out, cat)
	}
	sort.Slice(out, func(i, j int) bool {
		return string(out[i]) < string(out[j])
	})
	return out
}

// Install installs an integration template to an agent.
// It stores auth credentials in the vault, renders endpoints from the template,
// and appends them to the agent's REST API endpoints config.
func (m *Manager) Install(ctx context.Context, req InstallRequest) error {
	tmpl, err := m.Get(req.IntegrationName)
	if err != nil {
		return err
	}

	agent, err := m.store.GetAgent(ctx, req.AgentID)
	if err != nil {
		return fmt.Errorf("get agent: %w", err)
	}

	// Check for duplicate installation.
	existing := parseEndpoints(agent.RESTAPIEndpointsJSON)
	for _, ep := range existing {
		if ep.IntegrationSource == tmpl.Name {
			return fmt.Errorf("integration %q is already installed on agent %s", tmpl.Name, req.AgentID)
		}
	}

	// Store auth credential in vault (scoped to agent + integration).
	if req.AuthSecret != "" && tmpl.Auth.SecretRef != "" {
		vaultKey := fmt.Sprintf("integrations/%s/%s", tmpl.Name, tmpl.Auth.SecretRef)
		desc := fmt.Sprintf("%s API credential for %s", tmpl.DisplayName, agent.Name)
		if err := m.secrets.Set(ctx, req.AgentID, vaultKey, req.AuthSecret, desc); err != nil {
			return fmt.Errorf("store auth credential: %w", err)
		}
	}

	// Build endpoint auth config with vault keys scoped to integration.
	auth := tmpl.Auth
	if auth.SecretRef != "" {
		auth.SecretRef = fmt.Sprintf("integrations/%s/%s", tmpl.Name, auth.SecretRef)
	}
	if auth.ClientIDRef != "" {
		auth.ClientIDRef = fmt.Sprintf("integrations/%s/%s", tmpl.Name, auth.ClientIDRef)
	}
	if auth.ClientSecretRef != "" {
		auth.ClientSecretRef = fmt.Sprintf("integrations/%s/%s", tmpl.Name, auth.ClientSecretRef)
	}

	// Render each template endpoint into a REST API endpoint.
	for _, tep := range tmpl.Endpoints {
		ep := tep.ToRESTAPIEndpoint(auth, tmpl.Name)

		// For OAuth2 integrations, set the token refs to match the keys
		// used by the OAuth callback handler to store tokens in the vault.
		if auth.Type == "oauth2" {
			ep.Auth.RefreshTokenRef = fmt.Sprintf("integrations/%s/refresh_token", tmpl.Name)
			ep.Auth.AccessTokenRef = fmt.Sprintf("integrations/%s/access_token", tmpl.Name)
		}

		// Substitute install-time variables into endpoint templates.
		// Variables (e.g. base URL) are baked in so agents don't need to know them.
		if len(req.Variables) > 0 {
			substituteVariables(&ep, req.Variables)
		}

		existing = append(existing, ep)
	}

	// Auto-add endpoint hosts to HTTPAllowedHosts so the REST API tool
	// can reach them even if they resolve to private IPs.
	hostSet := make(map[string]bool)
	for _, h := range agent.HTTPAllowedHosts {
		hostSet[h] = true
	}
	for _, ep := range existing {
		if ep.IntegrationSource != tmpl.Name {
			continue // Only process endpoints from this integration.
		}
		parsed, err := url.Parse(ep.URL)
		if err != nil || parsed.Hostname() == "" {
			continue
		}
		host := parsed.Hostname()
		if !hostSet[host] {
			agent.HTTPAllowedHosts = append(agent.HTTPAllowedHosts, host)
			hostSet[host] = true
		}
	}

	// Save updated endpoints to agent config.
	data, err := json.Marshal(existing)
	if err != nil {
		return fmt.Errorf("marshal endpoints: %w", err)
	}
	agent.RESTAPIEndpointsJSON = string(data)
	agent.UpdatedAt = time.Now().UTC()

	return m.store.UpdateAgent(ctx, *agent)
}

// Uninstall removes all endpoints from an integration and cleans up.
func (m *Manager) Uninstall(ctx context.Context, agentID, integrationName string) error {
	agent, err := m.store.GetAgent(ctx, agentID)
	if err != nil {
		return fmt.Errorf("get agent: %w", err)
	}

	existing := parseEndpoints(agent.RESTAPIEndpointsJSON)
	filtered := make([]types.RESTAPIEndpoint, 0, len(existing))
	for _, ep := range existing {
		if ep.IntegrationSource != integrationName {
			filtered = append(filtered, ep)
		}
	}

	if len(filtered) == len(existing) {
		return fmt.Errorf("integration %q is not installed on agent %s", integrationName, agentID)
	}

	data, err := json.Marshal(filtered)
	if err != nil {
		return fmt.Errorf("marshal endpoints: %w", err)
	}
	agent.RESTAPIEndpointsJSON = string(data)
	agent.UpdatedAt = time.Now().UTC()

	return m.store.UpdateAgent(ctx, *agent)
}

// InstalledFor returns the list of installed integration names for an agent.
func (m *Manager) InstalledFor(ctx context.Context, agentID string) ([]InstalledIntegration, error) {
	agent, err := m.store.GetAgent(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("get agent: %w", err)
	}

	endpoints := parseEndpoints(agent.RESTAPIEndpointsJSON)
	seen := make(map[string]int) // integration name → endpoint count
	for _, ep := range endpoints {
		if ep.IntegrationSource != "" {
			seen[ep.IntegrationSource]++
		}
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	var out []InstalledIntegration
	for name, count := range seen {
		inst := InstalledIntegration{
			Name:          name,
			EndpointCount: count,
		}
		if t, ok := m.catalog[name]; ok {
			inst.DisplayName = t.DisplayName
			inst.Category = t.Category
			inst.Icon = t.Icon
			inst.Description = t.Description
		} else {
			inst.DisplayName = name
		}
		out = append(out, inst)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].DisplayName < out[j].DisplayName
	})
	return out, nil
}

// PromptContentForAgent returns prompt guidance for all integrations installed on an agent.
//
// The content teaches the model how to use integration endpoints through rest_api
// without requesting credentials from users. Template-level prompt guidance is
// included when present.
func (m *Manager) PromptContentForAgent(ctx context.Context, agentID string) (string, error) {
	agent, err := m.store.GetAgent(ctx, agentID)
	if err != nil {
		return "", fmt.Errorf("get agent: %w", err)
	}

	endpoints := parseEndpoints(agent.RESTAPIEndpointsJSON)
	if len(endpoints) == 0 {
		return "", nil
	}

	perIntegration := make(map[string][]string)
	for _, ep := range endpoints {
		if ep.IntegrationSource == "" {
			continue
		}
		perIntegration[ep.IntegrationSource] = append(perIntegration[ep.IntegrationSource], ep.Name)
	}
	if len(perIntegration) == 0 {
		return "", nil
	}

	names := make([]string, 0, len(perIntegration))
	for name := range perIntegration {
		names = append(names, name)
	}
	sort.Strings(names)

	m.mu.RLock()
	defer m.mu.RUnlock()

	var blocks []string
	for _, name := range names {
		endpointNames := perIntegration[name]
		sort.Strings(endpointNames)

		display := name
		var extraPrompt string
		if tmpl, ok := m.catalog[name]; ok {
			if strings.TrimSpace(tmpl.DisplayName) != "" {
				display = tmpl.DisplayName
			}
			if tmpl.Prompts != nil {
				extraPrompt = strings.TrimSpace(tmpl.Prompts.System)
			}
		}

		block := "### " + display + "\n" +
			"- Use `rest_api__call` for this integration.\n" +
			"- Discover endpoint shapes with `rest_api__list_endpoints` when needed.\n" +
			"- Available endpoint names: " + strings.Join(endpointNames, ", ") + "\n" +
			"- Do not ask the user for OAuth tokens/API keys when this integration is installed; auth is injected by Kyvik."
		if extraPrompt != "" {
			block += "\n- Integration-specific guidance:\n" + extraPrompt
		}
		blocks = append(blocks, block)
	}

	return strings.Join(blocks, "\n\n"), nil
}

// Refresh re-scans disk and replaces the catalog.
func (m *Manager) Refresh() error {
	templates, err := m.loader.LoadAll()
	if err != nil {
		return fmt.Errorf("refresh integration catalog: %w", err)
	}
	cat := make(map[string]*Template, len(templates))
	for i := range templates {
		cat[templates[i].Name] = &templates[i]
	}
	m.mu.Lock()
	m.catalog = cat
	m.mu.Unlock()
	return nil
}

// SaveLocal writes a template to the local integrations directory.
func (m *Manager) SaveLocal(tmpl Template) error {
	tmpl.Source = "local"
	data, err := yaml.Marshal(tmpl)
	if err != nil {
		return fmt.Errorf("marshal template: %w", err)
	}

	filename := tmpl.Name + ".yaml"
	path := filepath.Join(m.loader.LocalDir(), filename)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write template: %w", err)
	}

	// Reload catalog.
	return m.Refresh()
}

// DeleteLocal removes a local integration template file.
func (m *Manager) DeleteLocal(name string) error {
	m.mu.RLock()
	tmpl, ok := m.catalog[name]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("integration %q not found", name)
	}
	if tmpl.Source != "local" {
		return fmt.Errorf("cannot delete built-in integration %q", name)
	}

	if err := os.Remove(tmpl.FilePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete template file: %w", err)
	}

	return m.Refresh()
}

// InstallRequest contains the parameters for installing an integration.
type InstallRequest struct {
	AgentID         string            `json:"agent_id"`
	IntegrationName string            `json:"integration_name"`
	AuthSecret      string            `json:"auth_secret,omitempty"`
	Variables       map[string]string `json:"variables,omitempty"`
	InstalledBy     string            `json:"installed_by"`
}

// InstalledIntegration summarizes an installed integration on an agent.
type InstalledIntegration struct {
	Name          string                    `json:"name"`
	DisplayName   string                    `json:"display_name"`
	Description   string                    `json:"description"`
	Category      types.IntegrationCategory `json:"category"`
	Icon          string                    `json:"icon"`
	EndpointCount int                       `json:"endpoint_count"`
}

// GetAgent returns the agent config for the given ID.
func (m *Manager) GetAgent(ctx context.Context, agentID string) (*types.AgentConfig, error) {
	return m.store.GetAgent(ctx, agentID)
}

// UpdateAgent saves the agent config.
func (m *Manager) UpdateAgent(ctx context.Context, agent types.AgentConfig) error {
	return m.store.UpdateAgent(ctx, agent)
}

// InstallNative grants a native tool to an agent by appending it to ToolGrants.
func (m *Manager) InstallNative(ctx context.Context, req NativeInstallRequest) error {
	manifest := GetNative(req.ToolName)
	if manifest == nil {
		return fmt.Errorf("native tool %q not found", req.ToolName)
	}

	agent, err := m.store.GetAgent(ctx, req.AgentID)
	if err != nil {
		return fmt.Errorf("get agent: %w", err)
	}

	// Check for duplicate grant.
	for _, g := range agent.ToolGrants {
		if g == req.ToolName {
			return fmt.Errorf("native tool %q is already granted to agent %s", req.ToolName, req.AgentID)
		}
	}

	// Store auth credential in vault if provided.
	// Scope must be "agent:<id>" to match vault.Resolve() cascading lookup.
	if req.AuthSecret != "" && manifest.Auth.SecretRef != "" {
		vaultKey := manifest.Auth.SecretRef
		scope := "agent:" + req.AgentID
		desc := manifest.DisplayName + " credential for " + req.AgentID
		if err := m.secrets.Set(ctx, scope, vaultKey, req.AuthSecret, desc); err != nil {
			return fmt.Errorf("store native tool credential: %w", err)
		}
	}

	agent.ToolGrants = append(agent.ToolGrants, req.ToolName)
	agent.UpdatedAt = time.Now().UTC()

	return m.store.UpdateAgent(ctx, *agent)
}

// UninstallNative removes a native tool grant from an agent.
// Vault secrets are left in place (consistent with REST API integration behavior).
func (m *Manager) UninstallNative(ctx context.Context, agentID, toolName string) error {
	agent, err := m.store.GetAgent(ctx, agentID)
	if err != nil {
		return fmt.Errorf("get agent: %w", err)
	}
	var updated []string
	found := false
	for _, g := range agent.ToolGrants {
		if g == toolName {
			found = true
			continue
		}
		updated = append(updated, g)
	}
	if !found {
		return fmt.Errorf("native tool %q is not installed on agent %s", toolName, agentID)
	}
	agent.ToolGrants = updated
	agent.UpdatedAt = time.Now().UTC()
	return m.store.UpdateAgent(ctx, *agent)
}

// substituteVariables replaces Go template placeholders for install-time variables
// (e.g. {{.service_url}}) with their values in all template strings of an endpoint.
// Runtime parameters (passed by the agent at call time) are left as templates.
func substituteVariables(ep *types.RESTAPIEndpoint, vars map[string]string) {
	for name, val := range vars {
		if val == "" {
			continue
		}
		placeholder := "{{." + name + "}}"
		ep.URL = strings.ReplaceAll(ep.URL, placeholder, val)
		ep.BodyTemplate = strings.ReplaceAll(ep.BodyTemplate, placeholder, val)
		for k, v := range ep.Headers {
			ep.Headers[k] = strings.ReplaceAll(v, placeholder, val)
		}
		for k, v := range ep.QueryParams {
			ep.QueryParams[k] = strings.ReplaceAll(v, placeholder, val)
		}
	}
}

func parseEndpoints(jsonStr string) []types.RESTAPIEndpoint {
	if jsonStr == "" {
		return nil
	}
	var endpoints []types.RESTAPIEndpoint
	if err := json.Unmarshal([]byte(jsonStr), &endpoints); err != nil {
		log.Printf("[integrations] warning: failed to parse endpoints JSON: %v", err)
		return nil
	}
	return endpoints
}
