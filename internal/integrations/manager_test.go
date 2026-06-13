package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

// mockStore is a minimal in-memory agent store for testing.
type mockStore struct {
	mu     sync.Mutex
	agents map[string]*types.AgentConfig
}

func newMockStore() *mockStore {
	return &mockStore{agents: make(map[string]*types.AgentConfig)}
}

func (s *mockStore) GetAgent(_ context.Context, id string) (*types.AgentConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.agents[id]
	if !ok {
		return nil, fmt.Errorf("agent %q not found", id)
	}
	copy := *a
	return &copy, nil
}

func (s *mockStore) UpdateAgent(_ context.Context, agent types.AgentConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.agents[agent.ID] = &agent
	return nil
}

// mockSecrets is a minimal in-memory secret store for testing.
type mockSecrets struct {
	mu      sync.Mutex
	secrets map[string]string
}

func newMockSecrets() *mockSecrets {
	return &mockSecrets{secrets: make(map[string]string)}
}

func (s *mockSecrets) Set(_ context.Context, scope, key, plaintext, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secrets[scope+":"+key] = plaintext
	return nil
}

func TestLoaderBuiltinTemplates(t *testing.T) {
	tmpDir := t.TempDir()
	loader, err := NewLoader(BuiltinTemplates, tmpDir)
	if err != nil {
		t.Fatalf("NewLoader: %v", err)
	}

	templates, err := loader.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}

	if len(templates) == 0 {
		t.Fatal("expected at least one built-in template, got 0")
	}

	// Check that all templates have required fields.
	for _, tmpl := range templates {
		if tmpl.Name == "" {
			t.Error("template has empty name")
		}
		if tmpl.DisplayName == "" {
			t.Errorf("template %q has empty display_name", tmpl.Name)
		}
		if len(tmpl.Endpoints) == 0 {
			t.Errorf("template %q has no endpoints", tmpl.Name)
		}
		if tmpl.Source != "builtin" {
			t.Errorf("template %q has source %q, want builtin", tmpl.Name, tmpl.Source)
		}
	}

	t.Logf("Loaded %d built-in templates", len(templates))
}

func TestLoaderLocalTemplates(t *testing.T) {
	tmpDir := t.TempDir()
	loader, err := NewLoader(BuiltinTemplates, tmpDir)
	if err != nil {
		t.Fatalf("NewLoader: %v", err)
	}

	// Write a local template.
	localYAML := `
name: test-local
display_name: Test Local Integration
description: A test integration
version: 1.0.0
category: developer-tools
auth:
  type: bearer
  secret_ref: test_token
endpoints:
  - name: test_action
    description: A test action
    method: GET
    url: https://api.example.com/test
`
	if err := os.WriteFile(filepath.Join(tmpDir, "test-local.yaml"), []byte(localYAML), 0o644); err != nil {
		t.Fatalf("write local template: %v", err)
	}

	templates, err := loader.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}

	var found bool
	for _, tmpl := range templates {
		if tmpl.Name == "test-local" {
			found = true
			if tmpl.Source != "local" {
				t.Errorf("local template has source %q, want local", tmpl.Source)
			}
		}
	}
	if !found {
		t.Error("local template 'test-local' not found in catalog")
	}
}

func TestManagerInstallUninstall(t *testing.T) {
	tmpDir := t.TempDir()
	loader, err := NewLoader(BuiltinTemplates, tmpDir)
	if err != nil {
		t.Fatalf("NewLoader: %v", err)
	}

	store := newMockStore()
	secrets := newMockSecrets()

	// Create test agent.
	store.agents["agent-1"] = &types.AgentConfig{
		ID:   "agent-1",
		Name: "Test Agent",
	}

	mgr, err := NewManager(loader, store, secrets)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// List available.
	available := mgr.Available()
	if len(available) == 0 {
		t.Fatal("expected at least one available integration")
	}

	// Pick the first integration.
	tmpl := available[0]
	t.Logf("Installing %q to agent-1", tmpl.Name)

	// Install.
	err = mgr.Install(context.Background(), InstallRequest{
		AgentID:         "agent-1",
		IntegrationName: tmpl.Name,
		AuthSecret:      "test-secret-value",
		InstalledBy:     "test-user",
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	// Verify endpoints were added.
	agent, _ := store.GetAgent(context.Background(), "agent-1")
	var endpoints []types.RESTAPIEndpoint
	if err := json.Unmarshal([]byte(agent.RESTAPIEndpointsJSON), &endpoints); err != nil {
		t.Fatalf("unmarshal endpoints: %v", err)
	}
	if len(endpoints) != len(tmpl.Endpoints) {
		t.Errorf("got %d endpoints, want %d", len(endpoints), len(tmpl.Endpoints))
	}
	for _, ep := range endpoints {
		if ep.IntegrationSource != tmpl.Name {
			t.Errorf("endpoint %q has source %q, want %q", ep.Name, ep.IntegrationSource, tmpl.Name)
		}
	}

	// Verify secret was stored.
	if tmpl.Auth.SecretRef != "" {
		secretKey := fmt.Sprintf("integrations/%s/%s", tmpl.Name, tmpl.Auth.SecretRef)
		secrets.mu.Lock()
		v, ok := secrets.secrets["agent-1:"+secretKey]
		secrets.mu.Unlock()
		if !ok {
			t.Error("auth secret was not stored in vault")
		} else if v != "test-secret-value" {
			t.Errorf("secret value = %q, want %q", v, "test-secret-value")
		}
	}

	// Verify installed list.
	installed, err := mgr.InstalledFor(context.Background(), "agent-1")
	if err != nil {
		t.Fatalf("InstalledFor: %v", err)
	}
	if len(installed) != 1 {
		t.Fatalf("got %d installed, want 1", len(installed))
	}
	if installed[0].Name != tmpl.Name {
		t.Errorf("installed name = %q, want %q", installed[0].Name, tmpl.Name)
	}

	// Duplicate install should fail.
	err = mgr.Install(context.Background(), InstallRequest{
		AgentID:         "agent-1",
		IntegrationName: tmpl.Name,
		AuthSecret:      "x",
	})
	if err == nil {
		t.Error("expected error on duplicate install")
	}

	// Uninstall.
	err = mgr.Uninstall(context.Background(), "agent-1", tmpl.Name)
	if err != nil {
		t.Fatalf("Uninstall: %v", err)
	}

	// Verify endpoints were removed.
	agent, _ = store.GetAgent(context.Background(), "agent-1")
	endpoints = nil
	if agent.RESTAPIEndpointsJSON != "" {
		_ = json.Unmarshal([]byte(agent.RESTAPIEndpointsJSON), &endpoints)
	}
	for _, ep := range endpoints {
		if ep.IntegrationSource == tmpl.Name {
			t.Errorf("endpoint %q still has source %q after uninstall", ep.Name, tmpl.Name)
		}
	}
}

func TestManagerSaveLocal(t *testing.T) {
	tmpDir := t.TempDir()
	loader, err := NewLoader(BuiltinTemplates, tmpDir)
	if err != nil {
		t.Fatalf("NewLoader: %v", err)
	}

	store := newMockStore()
	secrets := newMockSecrets()

	mgr, err := NewManager(loader, store, secrets)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// Save a custom template.
	custom := Template{
		Name:        "my-custom",
		DisplayName: "My Custom Integration",
		Description: "Custom integration for testing",
		Version:     "1.0.0",
		Category:    types.IntCatDeveloperTools,
		Auth: TemplateAuth{
			Type:      "bearer",
			SecretRef: "my_token",
		},
		Endpoints: []TemplateEndpoint{
			{
				Name:        "custom_action",
				Description: "Do something custom",
				Method:      "GET",
				URL:         "https://api.example.com/v1/test",
			},
		},
	}

	if err := mgr.SaveLocal(custom); err != nil {
		t.Fatalf("SaveLocal: %v", err)
	}

	// Verify file exists.
	path := filepath.Join(tmpDir, "my-custom.yaml")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("local template file not found: %v", err)
	}

	// Verify it's in the catalog.
	got, err := mgr.Get("my-custom")
	if err != nil {
		t.Fatalf("Get after SaveLocal: %v", err)
	}
	if got.DisplayName != "My Custom Integration" {
		t.Errorf("DisplayName = %q, want %q", got.DisplayName, "My Custom Integration")
	}

	// Delete it.
	if err := mgr.DeleteLocal("my-custom"); err != nil {
		t.Fatalf("DeleteLocal: %v", err)
	}

	_, err = mgr.Get("my-custom")
	if err == nil {
		t.Error("expected error after DeleteLocal")
	}
}

func TestTemplateEndpointConversion(t *testing.T) {
	tep := TemplateEndpoint{
		Name:           "test",
		Description:    "test endpoint",
		Method:         "POST",
		URL:            "https://api.example.com/v1/{{.action}}",
		RateLimitRPM:   30,
		TimeoutSeconds: 0, // Should default to 30
	}

	auth := TemplateAuth{
		Type:      "bearer",
		SecretRef: "my_token",
	}

	ep := tep.ToRESTAPIEndpoint(auth, "test-integration")

	if ep.IntegrationSource != "test-integration" {
		t.Errorf("IntegrationSource = %q, want %q", ep.IntegrationSource, "test-integration")
	}
	if ep.TimeoutSeconds != 30 {
		t.Errorf("TimeoutSeconds = %d, want 30", ep.TimeoutSeconds)
	}
	if ep.Auth.Type != "bearer" {
		t.Errorf("Auth.Type = %q, want bearer", ep.Auth.Type)
	}
}

func TestInstallWithVariables(t *testing.T) {
	tmpDir := t.TempDir()
	loader, err := NewLoader(BuiltinTemplates, tmpDir)
	if err != nil {
		t.Fatalf("NewLoader: %v", err)
	}

	store := newMockStore()
	secrets := newMockSecrets()
	store.agents["agent-1"] = &types.AgentConfig{ID: "agent-1", Name: "Test Agent"}

	mgr, err := NewManager(loader, store, secrets)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// Install an integration that bakes a base URL variable into its endpoints.
	err = mgr.Install(context.Background(), InstallRequest{
		AgentID:         "agent-1",
		IntegrationName: "home-assistant",
		AuthSecret:      "test-api-key",
		Variables:       map[string]string{"ha_url": "https://my-nos.local/api/v1"},
		InstalledBy:     "test",
	})
	if err != nil {
		t.Fatalf("Install home-assistant: %v", err)
	}

	// Verify all endpoint URLs have the variable baked in.
	agent, _ := store.GetAgent(context.Background(), "agent-1")
	var endpoints []types.RESTAPIEndpoint
	if err := json.Unmarshal([]byte(agent.RESTAPIEndpointsJSON), &endpoints); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(endpoints) == 0 {
		t.Fatal("expected endpoints after install")
	}

	for _, ep := range endpoints {
		if strings.Contains(ep.URL, "{{.ha_url}}") {
			t.Errorf("endpoint %q URL still contains template variable: %s", ep.Name, ep.URL)
		}
		if !strings.HasPrefix(ep.URL, "https://my-nos.local/api/v1/") {
			t.Errorf("endpoint %q URL = %q, want prefix https://my-nos.local/api/v1/", ep.Name, ep.URL)
		}
	}
}

func TestManagerCategories(t *testing.T) {
	tmpDir := t.TempDir()
	loader, err := NewLoader(BuiltinTemplates, tmpDir)
	if err != nil {
		t.Fatalf("NewLoader: %v", err)
	}

	mgr, err := NewManager(loader, newMockStore(), newMockSecrets())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	cats := mgr.Categories()
	if len(cats) == 0 {
		t.Error("expected at least one category")
	}

	_ = time.Now() // use time to avoid import error
}

func TestManagerPromptContentForAgent(t *testing.T) {
	tmpDir := t.TempDir()
	loader, err := NewLoader(BuiltinTemplates, tmpDir)
	if err != nil {
		t.Fatalf("NewLoader: %v", err)
	}

	store := newMockStore()
	secrets := newMockSecrets()
	store.agents["agent-1"] = &types.AgentConfig{ID: "agent-1", Name: "Prompt Agent", Template: "admin"}

	mgr, err := NewManager(loader, store, secrets)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	custom := Template{
		Name:        "prompted-int",
		DisplayName: "Prompted Integration",
		Description: "Has prompt guidance",
		Version:     "1.0.0",
		Category:    types.IntCatDeveloperTools,
		Auth: TemplateAuth{
			Type:      "bearer",
			SecretRef: "token",
		},
		Endpoints: []TemplateEndpoint{
			{Name: "pi_list", Description: "list", Method: "GET", URL: "https://api.example.com/list"},
			{Name: "pi_create", Description: "create", Method: "POST", URL: "https://api.example.com/create"},
		},
		Prompts: &TemplatePrompts{System: "Always validate required fields before calling create."},
	}
	if err := mgr.SaveLocal(custom); err != nil {
		t.Fatalf("SaveLocal: %v", err)
	}

	if err := mgr.Install(context.Background(), InstallRequest{AgentID: "agent-1", IntegrationName: custom.Name, AuthSecret: "abc", InstalledBy: "test"}); err != nil {
		t.Fatalf("Install: %v", err)
	}

	content, err := mgr.PromptContentForAgent(context.Background(), "agent-1")
	if err != nil {
		t.Fatalf("PromptContentForAgent: %v", err)
	}
	if content == "" {
		t.Fatal("expected non-empty prompt content")
	}
	if !containsAll(content,
		"Prompted Integration",
		"rest_api__call",
		"rest_api__list_endpoints",
		"pi_create",
		"pi_list",
		"auth is injected by Kyvik",
		"Always validate required fields before calling create.",
	) {
		t.Fatalf("unexpected prompt content:\n%s", content)
	}
}

func TestManagerPromptContentForAgent_NoIntegrations(t *testing.T) {
	tmpDir := t.TempDir()
	loader, err := NewLoader(BuiltinTemplates, tmpDir)
	if err != nil {
		t.Fatalf("NewLoader: %v", err)
	}

	store := newMockStore()
	store.agents["agent-1"] = &types.AgentConfig{ID: "agent-1", Name: "No Integrations"}

	mgr, err := NewManager(loader, store, newMockSecrets())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	content, err := mgr.PromptContentForAgent(context.Background(), "agent-1")
	if err != nil {
		t.Fatalf("PromptContentForAgent: %v", err)
	}
	if content != "" {
		t.Fatalf("expected empty prompt content, got: %q", content)
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !strings.Contains(s, p) {
			return false
		}
	}
	return true
}
