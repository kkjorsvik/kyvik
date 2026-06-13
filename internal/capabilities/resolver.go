package capabilities

import (
	"context"
	"fmt"
	"strings"

	"github.com/kkjorsvik/kyvik/internal/integrations"
	"github.com/kkjorsvik/kyvik/internal/skills"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// CapabilityType identifies which registry a capability comes from.
type CapabilityType string

const (
	TypeSkill       CapabilityType = "skill"
	TypeIntegration CapabilityType = "integration"
	TypeNativeTool  CapabilityType = "native_tool"
)

// CapabilityInfo describes a single installable capability with its resolved dependencies.
type CapabilityInfo struct {
	Type        CapabilityType
	Name        string
	DisplayName string
	Description string
	Category    string
	MinTier     string // minimum agent template required ("", "reader", "worker", "operator", "admin")

	// Populated for skills.
	Skill *types.Skill

	// Populated for REST integrations.
	Integration *integrations.Template

	// Populated for native tools.
	NativeTool *integrations.NativeToolManifest

	// Resolved dependencies (native tools + integrations this capability needs).
	RequiredNativeTools  []*integrations.NativeToolManifest
	RequiredIntegrations []*integrations.Template
}

// AgentStatus describes the install status of a capability for a specific agent.
type AgentStatus struct {
	Installed         bool
	TierOK            bool   // agent's template meets MinTier
	AgentTier         string // agent's current template
	RequiredTier      string // minimum tier needed
	UnmetNativeTools  []*integrations.NativeToolManifest
	UnmetIntegrations []*integrations.Template
}

// InstallRequest is the input to Resolver.Install.
type InstallRequest struct {
	AgentID  string
	Name     string
	// Credentials keyed by native tool name or integration name.
	Credentials map[string]string
	// Variables keyed by template variable name (e.g. "service_url").
	// Baked into endpoint URLs at install time.
	Variables map[string]string
	// Whether the user has consented to upgrade the agent tier.
	UpgradeTier bool
	InstalledBy string
}

// Resolver provides unified capability lookup and installation across all three registries.
type Resolver struct {
	skills       *skills.Manager
	integrations *integrations.Manager
}

// New creates a Resolver.
func New(sm *skills.Manager, im *integrations.Manager) *Resolver {
	return &Resolver{skills: sm, integrations: im}
}

// Resolve looks up a capability by name across all three registries (skills → integrations → native tools).
// Returns an error if not found.
func (r *Resolver) Resolve(name string) (*CapabilityInfo, error) {
	// Check skills first.
	if r.skills != nil {
		sk, err := r.skills.Get(name)
		if err == nil {
			info := &CapabilityInfo{
				Type:        TypeSkill,
				Name:        sk.Name,
				DisplayName: sk.Name,
				Description: sk.Description,
				Skill:       sk,
			}
			// Resolve native tool dependencies.
			for _, toolName := range sk.Manifest.RequiredTools {
				m := integrations.GetNative(toolName)
				if m != nil {
					info.RequiredNativeTools = append(info.RequiredNativeTools, m)
					if tierGT(m.MinTier, info.MinTier) {
						info.MinTier = m.MinTier
					}
				}
			}
			// Resolve integration dependencies.
			for _, intName := range sk.Manifest.RequiredIntegrations {
				tmpl, err := r.integrations.Get(intName)
				if err == nil {
					info.RequiredIntegrations = append(info.RequiredIntegrations, tmpl)
				}
			}
			return info, nil
		}
	}

	// Check REST integrations.
	if r.integrations != nil {
		tmpl, err := r.integrations.Get(name)
		if err == nil {
			return &CapabilityInfo{
				Type:        TypeIntegration,
				Name:        tmpl.Name,
				DisplayName: tmpl.DisplayName,
				Description: tmpl.Description,
				Category:    string(tmpl.Category),
				Integration: tmpl,
			}, nil
		}
	}

	// Check native tools.
	m := integrations.GetNative(name)
	if m != nil {
		return &CapabilityInfo{
			Type:        TypeNativeTool,
			Name:        m.Name,
			DisplayName: m.DisplayName,
			Description: m.Description,
			Category:    string(m.Category),
			MinTier:     m.MinTier,
			NativeTool:  m,
		}, nil
	}

	return nil, fmt.Errorf("capability %q not found", name)
}

// All returns all capabilities from all three registries.
func (r *Resolver) All() []*CapabilityInfo {
	var out []*CapabilityInfo

	if r.skills != nil {
		for _, sk := range r.skills.All() {
			skCopy := sk
			info := &CapabilityInfo{
				Type:        TypeSkill,
				Name:        sk.Name,
				DisplayName: sk.Name,
				Description: sk.Description,
				Skill:       &skCopy,
			}
			for _, toolName := range sk.Manifest.RequiredTools {
				if m := integrations.GetNative(toolName); m != nil {
					info.RequiredNativeTools = append(info.RequiredNativeTools, m)
					if tierGT(m.MinTier, info.MinTier) {
						info.MinTier = m.MinTier
					}
				}
			}
			out = append(out, info)
		}
	}

	if r.integrations != nil {
		for _, tmpl := range r.integrations.Available() {
			tmplCopy := tmpl
			out = append(out, &CapabilityInfo{
				Type:        TypeIntegration,
				Name:        tmpl.Name,
				DisplayName: tmpl.DisplayName,
				Description: tmpl.Description,
				Category:    string(tmpl.Category),
				Integration: &tmplCopy,
			})
		}
	}

	for _, m := range integrations.AvailableNative() {
		out = append(out, &CapabilityInfo{
			Type:        TypeNativeTool,
			Name:        m.Name,
			DisplayName: m.DisplayName,
			Description: m.Description,
			Category:    string(m.Category),
			MinTier:     m.MinTier,
			NativeTool:  m,
		})
	}

	return out
}

// AgentCheck computes the install status of a capability for a specific agent.
func (r *Resolver) AgentCheck(ctx context.Context, info *CapabilityInfo, agent *types.AgentConfig) AgentStatus {
	status := AgentStatus{
		AgentTier:    agent.Template,
		RequiredTier: info.MinTier,
		TierOK:       !tierGT(info.MinTier, agent.Template),
	}

	// Build native tool grant set (used for both dep check and Installed check).
	grantSet := make(map[string]bool, len(agent.ToolGrants))
	for _, g := range agent.ToolGrants {
		grantSet[g] = true
	}
	for _, m := range info.RequiredNativeTools {
		if !grantSet[m.Name] {
			status.UnmetNativeTools = append(status.UnmetNativeTools, m)
		}
	}

	// Build installed integration set (used for both dep check and Installed check).
	var installedIntegrationSet map[string]bool
	if r.integrations != nil {
		installed, err := r.integrations.InstalledFor(ctx, agent.ID)
		if err == nil {
			installedIntegrationSet = make(map[string]bool, len(installed))
			for _, ii := range installed {
				installedIntegrationSet[ii.Name] = true
			}
			for _, tmpl := range info.RequiredIntegrations {
				if !installedIntegrationSet[tmpl.Name] {
					status.UnmetIntegrations = append(status.UnmetIntegrations, tmpl)
				}
			}
		}
	}

	// Determine whether the main capability itself is installed.
	switch info.Type {
	case TypeNativeTool:
		status.Installed = grantSet[info.Name]
	case TypeIntegration:
		status.Installed = installedIntegrationSet[info.Name]
	case TypeSkill:
		if r.skills != nil {
			grants, err := r.skills.ListGrants(ctx, agent.ID)
			if err == nil {
				for _, g := range grants {
					if g.Grant.SkillName == info.Name {
						status.Installed = true
						break
					}
				}
			}
		}
	}

	return status
}

// Install orchestrates installation of a capability and all its unmet dependencies.
// Order: tier upgrade → native tools → REST integrations → main capability.
func (r *Resolver) Install(ctx context.Context, req InstallRequest) error {
	info, err := r.Resolve(req.Name)
	if err != nil {
		return err
	}

	agent, err := r.integrations.GetAgent(ctx, req.AgentID)
	if err != nil {
		return fmt.Errorf("get agent: %w", err)
	}

	// Tier upgrade if requested.
	if req.UpgradeTier && tierGT(info.MinTier, agent.Template) {
		agent.Template = info.MinTier
		if err := r.integrations.UpdateAgent(ctx, *agent); err != nil {
			return fmt.Errorf("upgrade tier: %w", err)
		}
		// Reload after update.
		agent, err = r.integrations.GetAgent(ctx, req.AgentID)
		if err != nil {
			return fmt.Errorf("reload agent after tier upgrade: %w", err)
		}
	}

	// Install unmet native tools.
	grantSet := make(map[string]bool, len(agent.ToolGrants))
	for _, g := range agent.ToolGrants {
		grantSet[g] = true
	}
	for _, m := range info.RequiredNativeTools {
		if grantSet[m.Name] {
			continue
		}
		cred := req.Credentials[m.Name]
		if err := r.integrations.InstallNative(ctx, integrations.NativeInstallRequest{
			AgentID:     req.AgentID,
			ToolName:    m.Name,
			AuthSecret:  cred,
			InstalledBy: req.InstalledBy,
		}); err != nil {
			return fmt.Errorf("install native tool %q: %w", m.Name, err)
		}
	}

	// Install unmet REST integrations.
	for _, tmpl := range info.RequiredIntegrations {
		cred := req.Credentials[tmpl.Name]
		if err := r.integrations.Install(ctx, integrations.InstallRequest{
			AgentID:         req.AgentID,
			IntegrationName: tmpl.Name,
			AuthSecret:      cred,
			InstalledBy:     req.InstalledBy,
		}); err != nil && !strings.Contains(err.Error(), "already installed") {
			return fmt.Errorf("install required integration %q: %w", tmpl.Name, err)
		}
	}

	// Install the main capability itself.
	switch info.Type {
	case TypeSkill:
		// Reload agent to pick up new ToolGrants.
		agent, err = r.integrations.GetAgent(ctx, req.AgentID)
		if err != nil {
			return fmt.Errorf("reload agent before skill grant: %w", err)
		}
		return r.skills.Grant(ctx, req.AgentID, req.Name, req.InstalledBy, *agent)
	case TypeIntegration:
		cred := req.Credentials[req.Name]
		return r.integrations.Install(ctx, integrations.InstallRequest{
			AgentID:         req.AgentID,
			IntegrationName: req.Name,
			AuthSecret:      cred,
			Variables:       req.Variables,
			InstalledBy:     req.InstalledBy,
		})
	case TypeNativeTool:
		cred := req.Credentials[req.Name]
		return r.integrations.InstallNative(ctx, integrations.NativeInstallRequest{
			AgentID:     req.AgentID,
			ToolName:    req.Name,
			AuthSecret:  cred,
			InstalledBy: req.InstalledBy,
		})
	}
	return nil
}

// tierOrder maps template name to numeric rank for comparison.
var tierOrder = map[string]int{
	"":         0,
	"reader":   1,
	"worker":   2,
	"operator": 3,
	"admin":    4,
	"power":    5,
}

// tierGT returns true if a > b in the tier ordering.
func tierGT(a, b string) bool {
	return tierOrder[a] > tierOrder[b]
}
