package permissions

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/kkjorsvik/kyvik/internal/audit"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// PermissionStore is the narrow persistence interface that StoreGate needs.
// It breaks the circular import between permissions and store packages.
// The store implementations satisfy this interface.
type PermissionStore interface {
	GetAgent(ctx context.Context, id string) (*types.AgentConfig, error)
	ListOverrides(ctx context.Context, agentID string) ([]Override, error)
	// GetAgentWithOverrides returns both the agent config and its permission
	// overrides fetched inside a single read-only transaction, eliminating
	// the TOCTOU window that exists when calling GetAgent and ListOverrides
	// separately.
	GetAgentWithOverrides(ctx context.Context, agentID string) (*types.AgentConfig, []Override, error)
	AddOverride(ctx context.Context, override Override) error
	RemoveOverride(ctx context.Context, agentID string, cap types.Capability) error
	RemoveAllOverrides(ctx context.Context, agentID string) error
}

// Compile-time check that StoreGate implements Gate.
var _ Gate = (*StoreGate)(nil)

// StoreGate implements Gate using a PermissionStore for persistence,
// an audit.Logger for logging every decision, and in-memory templates.
type StoreGate struct {
	store     PermissionStore
	audit     audit.Logger
	templates map[string]Template
}

// NewStoreGate creates a StoreGate. Built-in Go templates (reader, worker,
// operator, admin, guide) are registered first. If templateDir is non-empty,
// YAML files in that directory are loaded and can override built-ins by name.
func NewStoreGate(store PermissionStore, auditLogger audit.Logger, templateDir string) *StoreGate {
	g := &StoreGate{
		store: store,
		audit: auditLogger,
		templates: map[string]Template{
			ReaderTemplate.Name:     ReaderTemplate,
			WorkerTemplate.Name:     WorkerTemplate,
			OperatorTemplate.Name:   OperatorTemplate,
			AdminTemplate.Name:      AdminTemplate,
			GuideBasicTemplate.Name: GuideBasicTemplate,
		},
	}

	if templateDir != "" {
		maps.Copy(g.templates, loadTemplatesFromDir(templateDir))
	}

	return g
}

// SetGuideMode switches the guide template between basic and full scope.
func (g *StoreGate) SetGuideMode(mode string) {
	switch mode {
	case "full":
		g.templates["guide"] = GuideFullTemplate
	default:
		g.templates["guide"] = GuideBasicTemplate
	}
}

// Check evaluates whether an agent may perform a tool call.
// Priority: deny overrides > grant overrides > template > default deny.
// Every decision is audit-logged (best-effort).
//
// The agent config and overrides are fetched in a single transactional call
// to avoid a TOCTOU race between the two reads.
func (g *StoreGate) Check(ctx context.Context, agentID string, call types.ToolCall) (*Decision, error) {
	resource := extractResource(call)

	agent, overrides, err := g.store.GetAgentWithOverrides(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("get agent with overrides: %w", err)
	}

	// 1. Deny overrides first.
	for _, o := range overrides {
		if !o.Grant && matchCapability(o.Capability, call.ToolName, call.Action, resource) {
			d := &Decision{Allowed: false, Reason: "denied by override", Rule: "deny_override"}
			g.logDecision(ctx, agentID, call.ToolName+"."+call.Action, resource, d)
			return d, nil
		}
	}

	// 2. Grant overrides second.
	for _, o := range overrides {
		if o.Grant && matchCapability(o.Capability, call.ToolName, call.Action, resource) {
			d := &Decision{Allowed: true, Reason: "granted by override", Rule: "grant_override"}
			g.logDecision(ctx, agentID, call.ToolName+"."+call.Action, resource, d)
			return d, nil
		}
	}

	// 3. Template check.
	if tmpl, ok := g.templates[agent.Template]; ok {
		for _, cap := range tmpl.Capabilities {
			if matchCapability(cap, call.ToolName, call.Action, resource) {
				d := &Decision{Allowed: true, Reason: "granted by template", Rule: "template:" + tmpl.Name}
				g.logDecision(ctx, agentID, call.ToolName+"."+call.Action, resource, d)
				return d, nil
			}
		}
	}

	// 4. Default deny.
	d := &Decision{Allowed: false, Reason: "no matching permission", Rule: "default_deny"}
	g.logDecision(ctx, agentID, call.ToolName+"."+call.Action, resource, d)
	return d, nil
}

// GetAgentCapabilities returns the effective permissions for an agent:
// template capabilities + grant overrides, minus deny overrides.
//
// The agent config and overrides are fetched in a single transactional call
// to avoid a TOCTOU race between the two reads.
func (g *StoreGate) GetAgentCapabilities(ctx context.Context, agentID string) ([]types.Capability, error) {
	agent, overrides, err := g.store.GetAgentWithOverrides(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("get agent with overrides: %w", err)
	}

	// Start with template capabilities.
	var caps []types.Capability
	if tmpl, ok := g.templates[agent.Template]; ok {
		caps = append(caps, tmpl.Capabilities...)
	}

	// Add grant overrides.
	for _, o := range overrides {
		if o.Grant {
			caps = append(caps, o.Capability)
		}
	}

	// Filter out deny overrides.
	var result []types.Capability
	for _, cap := range caps {
		denied := false
		for _, o := range overrides {
			if !o.Grant && matchCapability(o.Capability, cap.Tool, cap.Action, cap.Resource) {
				denied = true
				break
			}
		}
		if !denied {
			result = append(result, cap)
		}
	}

	return result, nil
}

// LoadTemplate retrieves a permission template by name.
func (g *StoreGate) LoadTemplate(_ context.Context, name string) (*Template, error) {
	tmpl, ok := g.templates[name]
	if !ok {
		return nil, fmt.Errorf("template %q: %w", name, types.ErrNotFound)
	}
	return &tmpl, nil
}

// ListTemplates returns all user-assignable permission templates, sorted by
// tier level (reader < worker < operator < admin). The internal "guide"
// template is excluded because it is not user-assignable.
func (g *StoreGate) ListTemplates(_ context.Context) ([]Template, error) {
	var result []Template
	for _, tmpl := range g.templates {
		if tmpl.Name == "guide" {
			continue // internal-only, not user-assignable
		}
		result = append(result, tmpl)
	}
	sort.Slice(result, func(i, j int) bool {
		return tierLevel[result[i].Name] < tierLevel[result[j].Name]
	})
	return result, nil
}

// tierLevel maps template names to escalation levels.
var tierLevel = map[string]int{
	"guide":    0,
	"reader":   1,
	"worker":   2,
	"operator": 3,
	"admin":    4,
}

// maxGrantLevel defines the highest capability tier each template can receive via grant overrides.
var maxGrantLevel = map[string]int{
	"guide":    0, // guide cannot have grant overrides
	"reader":   2, // can grant up to worker-level
	"worker":   3, // can grant up to operator-level
	"operator": 4, // can grant up to admin-level (specific caps, not wildcard)
	"admin":    4, // already at max
}

// capabilityTierLevel returns the minimum tier level required for a capability.
func capabilityTierLevel(cap types.Capability) int {
	// Wildcard tool or action = admin-level
	if cap.Tool == "*" || cap.Action == "*" {
		return 4
	}
	// Execute or delete = operator-level
	if cap.Action == "execute" || cap.Action == "delete" {
		return 3
	}
	// Write/post/insert/update = worker-level
	switch cap.Action {
	case "write", "post", "insert", "update":
		return 2
	}
	// Read/get/select and everything else = reader-level
	return 1
}

// tierNameForLevel returns the template name for a given tier level.
func tierNameForLevel(level int) string {
	for name, l := range tierLevel {
		if l == level {
			return name
		}
	}
	return "unknown"
}

// AddOverride adds a granular permission override for an agent.
// For grant overrides, escalation limits are enforced: an agent cannot be
// granted capabilities beyond one tier above its template level.
// Deny overrides are always allowed (restricting is always safe).
func (g *StoreGate) AddOverride(ctx context.Context, override Override) error {
	// Deny overrides are always allowed — restricting is always safe.
	if !override.Grant {
		return g.store.AddOverride(ctx, override)
	}

	// For grant overrides, check escalation limits.
	agent, _, err := g.store.GetAgentWithOverrides(ctx, override.AgentID)
	if err != nil {
		return fmt.Errorf("get agent for escalation check: %w", err)
	}

	maxLevel, ok := maxGrantLevel[agent.Template]
	if !ok {
		maxLevel = 1 // unknown templates default to reader-level grants
	}

	requiredLevel := capabilityTierLevel(override.Capability)
	if requiredLevel > maxLevel {
		return fmt.Errorf("escalation denied: %s-tier agent cannot be granted %s.%s (requires %s-tier template or higher)",
			agent.Template, override.Capability.Tool, override.Capability.Action, tierNameForLevel(requiredLevel))
	}

	return g.store.AddOverride(ctx, override)
}

// RemoveOverride removes a specific override.
func (g *StoreGate) RemoveOverride(ctx context.Context, agentID string, capability types.Capability) error {
	return g.store.RemoveOverride(ctx, agentID, capability)
}

// ListOverrides returns all overrides for an agent.
func (g *StoreGate) ListOverrides(ctx context.Context, agentID string) ([]Override, error) {
	return g.store.ListOverrides(ctx, agentID)
}

// RemoveAllOverrides removes all overrides for an agent.
func (g *StoreGate) RemoveAllOverrides(ctx context.Context, agentID string) error {
	return g.store.RemoveAllOverrides(ctx, agentID)
}

// logDecision audit-logs a permission check (best-effort, errors ignored).
func (g *StoreGate) logDecision(ctx context.Context, agentID, action, resource string, d *Decision) {
	decision := "denied"
	if d.Allowed {
		decision = "allowed"
	}
	details := fmt.Sprintf("rule=%s reason=%s", d.Rule, d.Reason)
	_ = audit.LogPermissionCheck(ctx, g.audit, agentID, action, resource, decision, details)
}

// extractResource pulls a resource identifier from tool call parameters.
// Checks keys in priority order: resource, path, url, target. Defaults to "*".
func extractResource(call types.ToolCall) string {
	for _, key := range []string{"resource", "path", "url", "target"} {
		if v, ok := call.Parameters[key]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return "*"
}

// matchCapability checks if a capability pattern matches a concrete tool/action/resource.
func matchCapability(cap types.Capability, tool, action, resource string) bool {
	return matchField(cap.Tool, tool) &&
		matchField(cap.Action, action) &&
		matchResource(cap.Resource, resource)
}

// matchField matches a pattern against a value. "*" matches anything; otherwise exact match.
func matchField(pattern, value string) bool {
	if pattern == "*" {
		return true
	}
	return pattern == value
}

// matchResource matches a resource pattern against a requested resource.
// "*" pattern matches everything. "/prefix/*" matches the prefix and its children.
// A request for "*" is only matched by pattern "*". Otherwise, exact match.
func matchResource(pattern, resource string) bool {
	if pattern == "*" {
		return true
	}
	if resource == "*" {
		return false // only pattern "*" matches request "*"
	}
	if prefix, ok := strings.CutSuffix(pattern, "/*"); ok {
		return resource == prefix || strings.HasPrefix(resource, prefix+"/")
	}
	return pattern == resource
}

// --- YAML template loading ---

// templateYAML is the intermediate struct for parsing YAML template files.
type templateYAML struct {
	Name         string           `yaml:"name"`
	Description  string           `yaml:"description"`
	Capabilities []capabilityYAML `yaml:"capabilities"`
}

type capabilityYAML struct {
	Tool     string `yaml:"tool"`
	Action   string `yaml:"action"`
	Resource string `yaml:"resource"`
}

// loadTemplatesFromDir reads *.yaml files from dir and returns parsed templates.
// Unparseable files are silently skipped.
func loadTemplatesFromDir(dir string) map[string]Template {
	templates := make(map[string]Template)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return templates
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}

		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			slog.Warn("permission template: read failed", "file", entry.Name(), "error", err)
			continue
		}

		var raw templateYAML
		if err := yaml.Unmarshal(data, &raw); err != nil {
			slog.Warn("permission template: parse failed", "file", entry.Name(), "error", err)
			continue
		}

		if raw.Name == "" {
			slog.Warn("permission template: missing name", "file", entry.Name())
			continue
		}

		tmpl := Template{
			Name:        raw.Name,
			Description: raw.Description,
		}
		for _, c := range raw.Capabilities {
			tmpl.Capabilities = append(tmpl.Capabilities, types.Capability{
				Tool:     c.Tool,
				Action:   c.Action,
				Resource: c.Resource,
			})
		}
		templates[tmpl.Name] = tmpl
	}

	return templates
}
