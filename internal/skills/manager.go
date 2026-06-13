package skills

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/kkjorsvik/kyvik/internal/permissions"
	"github.com/kkjorsvik/kyvik/internal/security"
	"github.com/kkjorsvik/kyvik/internal/store"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// GrantedSkill pairs a grant record with its catalog entry.
type GrantedSkill struct {
	Grant types.SkillGrant `json:"grant"`
	Skill *types.Skill     `json:"skill"` // nil if skill was uninstalled after granting
}

// Manager is the business logic layer for skill catalog and agent grants.
type Manager struct {
	loader  *Loader
	store   store.Store
	catalog map[string]*types.Skill // name → Skill
	mu      sync.RWMutex
}

// NewEmptyManager creates a Manager with an empty catalog.
// Used as a fallback when the initial catalog build fails,
// so the dashboard still shows the skills page.
func NewEmptyManager(loader *Loader, s store.Store) *Manager {
	return &Manager{
		loader:  loader,
		store:   s,
		catalog: make(map[string]*types.Skill),
	}
}

// NewManager creates a Manager, loading the initial catalog from the Loader.
func NewManager(loader *Loader, s store.Store) (*Manager, error) {
	m := &Manager{
		loader:  loader,
		store:   s,
		catalog: make(map[string]*types.Skill),
	}
	if err := m.buildCatalog(); err != nil {
		return nil, fmt.Errorf("build skill catalog: %w", err)
	}
	return m, nil
}

// buildCatalog calls loader.LoadAll and populates the catalog map.
func (m *Manager) buildCatalog() error {
	skills, err := m.loader.LoadAll()
	if err != nil {
		return err
	}
	cat := make(map[string]*types.Skill, len(skills))
	for i := range skills {
		cat[skills[i].Name] = &skills[i]
	}
	m.catalog = cat
	return nil
}

// Available returns a snapshot of all catalog entries.
func (m *Manager) Available() []types.Skill {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]types.Skill, 0, len(m.catalog))
	for _, sk := range m.catalog {
		out = append(out, *sk)
	}
	return out
}

// GetSkill looks up a skill by name. Returns ErrSkillNotFound if missing.
func (m *Manager) GetSkill(name string) (*types.Skill, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	sk, ok := m.catalog[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", types.ErrSkillNotFound, name)
	}
	copy := *sk
	return &copy, nil
}

// Get looks up a skill by name. Returns ErrSkillNotFound if missing.
// This is an alias for GetSkill for compatibility with the capabilities resolver.
func (m *Manager) Get(name string) (*types.Skill, error) {
	return m.GetSkill(name)
}

// All returns all skills in the catalog.
func (m *Manager) All() []types.Skill {
	return m.Available()
}

// AvailableForAgent filters the catalog to skills whose requirements are
// satisfied by the agent's permission template.
func (m *Manager) AvailableForAgent(agent types.AgentConfig) []types.Skill {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []types.Skill
	for _, sk := range m.catalog {
		if validateRequirements(sk, agent) == nil {
			out = append(out, *sk)
		}
	}
	return out
}

// Refresh re-scans disk and replaces the catalog.
func (m *Manager) Refresh() error {
	skills, err := m.loader.LoadAll()
	if err != nil {
		return fmt.Errorf("refresh skill catalog: %w", err)
	}
	cat := make(map[string]*types.Skill, len(skills))
	for i := range skills {
		cat[skills[i].Name] = &skills[i]
	}
	m.mu.Lock()
	m.catalog = cat
	m.mu.Unlock()
	return nil
}

// Grant grants a skill to an agent after validating requirements.
func (m *Manager) Grant(ctx context.Context, agentID, skillName, grantedBy string, agentConfig types.AgentConfig) error {
	m.mu.RLock()
	sk, ok := m.catalog[skillName]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("%w: %s", types.ErrSkillNotFound, skillName)
	}

	if err := validateRequirements(sk, agentConfig); err != nil {
		return err
	}

	grant := types.SkillGrant{
		AgentID:   agentID,
		SkillName: skillName,
		GrantedAt: time.Now(),
		GrantedBy: grantedBy,
	}
	return m.store.GrantSkill(ctx, grant)
}

// Revoke removes a skill grant for an agent.
func (m *Manager) Revoke(ctx context.Context, agentID, skillName string) error {
	return m.store.RevokeSkill(ctx, agentID, skillName)
}

// ListGrants returns all grants for an agent, enriched with catalog data.
func (m *Manager) ListGrants(ctx context.Context, agentID string) ([]GrantedSkill, error) {
	grants, err := m.store.ListSkillGrants(ctx, agentID)
	if err != nil {
		return nil, err
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]GrantedSkill, len(grants))
	for i, g := range grants {
		gs := GrantedSkill{Grant: g}
		if sk, ok := m.catalog[g.SkillName]; ok {
			copy := *sk
			gs.Skill = &copy
		}
		out[i] = gs
	}
	return out, nil
}

// PromptContentForAgent collects prompt content from all granted skills.
func (m *Manager) PromptContentForAgent(ctx context.Context, agentID string) (string, error) {
	grants, err := m.store.ListSkillGrants(ctx, agentID)
	if err != nil {
		return "", err
	}
	if len(grants) == 0 {
		return "", nil
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	var parts []string
	for _, g := range grants {
		if sk, ok := m.catalog[g.SkillName]; ok && sk.PromptContent != "" {
			content := sk.PromptContent
			// Wrap untrusted skill content in security boundaries to prevent
			// prompt injection from community or local skill sources.
			if sk.Trust == types.TrustCommunity || sk.Trust == types.TrustLocal {
				content = security.WrapExternalContent("skill:"+sk.Name, content)
			}
			parts = append(parts, "### "+sk.Name+"\n"+content)
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

// SaveLocal creates a local skill on disk (under local/{name}/) and refreshes the catalog.
func (m *Manager) SaveLocal(manifest types.SkillManifest, systemPrompt string) error {
	if err := ValidateManifest(&manifest); err != nil {
		return err
	}

	skillDir := filepath.Join(m.loader.BaseDir(), dirLocal, manifest.Name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		return fmt.Errorf("create skill directory: %w", err)
	}

	data, err := yaml.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "skill.yaml"), data, 0o644); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}

	if systemPrompt != "" {
		promptsDir := filepath.Join(skillDir, "prompts")
		if err := os.MkdirAll(promptsDir, 0o755); err != nil {
			return fmt.Errorf("create prompts directory: %w", err)
		}
		if err := os.WriteFile(filepath.Join(promptsDir, "system.md"), []byte(systemPrompt), 0o644); err != nil {
			return fmt.Errorf("write system prompt: %w", err)
		}
	}

	return m.Refresh()
}

// DeleteLocal removes a local skill from disk and refreshes the catalog.
func (m *Manager) DeleteLocal(name string) error {
	skillDir := filepath.Join(m.loader.BaseDir(), dirLocal, name)
	if _, err := os.Stat(skillDir); os.IsNotExist(err) {
		return fmt.Errorf("local skill %q not found", name)
	}
	if err := os.RemoveAll(skillDir); err != nil {
		return fmt.Errorf("delete skill directory: %w", err)
	}
	return m.Refresh()
}

// CleanupAgent removes all skill grants for an agent.
func (m *Manager) CleanupAgent(ctx context.Context, agentID string) error {
	return m.store.DeleteSkillGrantsByAgent(ctx, agentID)
}

// validateRequirements checks if the agent's template covers the skill's required capabilities.
func validateRequirements(skill *types.Skill, agent types.AgentConfig) error {
	reqs := skill.Manifest.RequiredCapabilities
	if len(reqs) == 0 {
		return nil // prompt-only skill, always allowed
	}

	tmpl := templateByName(agent.Template)
	if tmpl == nil {
		// Unknown template — no capabilities, deny all requirements.
		return fmt.Errorf("%w: agent template %q is unknown", types.ErrSkillRequirements, agent.Template)
	}

	for _, req := range reqs {
		if !anyCapsCovers(tmpl.Capabilities, req.Tool, req.Action) {
			return fmt.Errorf("%w: agent template %q does not allow %s/%s",
				types.ErrSkillRequirements, agent.Template, req.Tool, req.Action)
		}
	}
	return nil
}

// templateByName returns the matching built-in template, or nil.
func templateByName(name string) *permissions.Template {
	switch name {
	case "reader":
		return &permissions.ReaderTemplate
	case "worker":
		return &permissions.WorkerTemplate
	case "operator":
		return &permissions.OperatorTemplate
	case "admin":
		return &permissions.AdminTemplate
	case "guide":
		return &permissions.GuideBasicTemplate
	default:
		return nil
	}
}

// anyCapsCovers returns true if any capability in the slice covers the given tool/action.
func anyCapsCovers(caps []types.Capability, tool, action string) bool {
	for _, cap := range caps {
		if capabilityCovers(cap, tool, action) {
			return true
		}
	}
	return false
}

// capabilityCovers checks if a capability pattern matches a tool/action pair.
// Replicates matchField logic from permissions/store_gate.go (unexported).
func capabilityCovers(cap types.Capability, tool, action string) bool {
	return matchField(cap.Tool, tool) && matchField(cap.Action, action)
}

// matchField matches a pattern against a value. "*" matches anything; otherwise exact.
func matchField(pattern, value string) bool {
	if pattern == "*" {
		return true
	}
	return pattern == value
}
