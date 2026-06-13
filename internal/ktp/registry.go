package ktp

import (
	"fmt"
	"log/slog"
	"slices"
	"sync"
)

// Registry is a concurrent-safe store for KTP tools. Tools register at startup
// and the system looks them up by name, lists them filtered by tier/grants,
// and produces model-ready tool definitions.
type Registry struct {
	tools map[string]Tool
	mu    sync.RWMutex
}

// NewRegistry creates an empty Registry ready for tool registration.
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Register validates and stores a tool. It returns an error if the tool's
// declaration is invalid or if a tool with the same name is already registered.
func (r *Registry) Register(tool Tool) error {
	decl := tool.Declaration()
	if err := decl.Validate(); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.tools[decl.Name]; exists {
		return fmt.Errorf("tool %q already registered", decl.Name)
	}

	r.tools[decl.Name] = tool
	slog.Info("tool registered",
		"name", decl.Name,
		"version", decl.Version,
		"actions", len(decl.Actions),
		"min_tier", decl.MinTier,
	)
	return nil
}

// Get retrieves a registered tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// List returns the declarations of all registered tools.
func (r *Registry) List() []ToolDeclaration {
	r.mu.RLock()
	defer r.mu.RUnlock()

	decls := make([]ToolDeclaration, 0, len(r.tools))
	for _, t := range r.tools {
		decls = append(decls, t.Declaration())
	}
	return decls
}

// ListForTier returns declarations for tools whose MinTier is satisfied by the
// given tier. Tools with an empty MinTier are included unconditionally.
func (r *Registry) ListForTier(tier string) []ToolDeclaration {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var decls []ToolDeclaration
	for _, t := range r.tools {
		decl := t.Declaration()
		if decl.MinTier == "" || TierAtLeast(tier, decl.MinTier) {
			decls = append(decls, decl)
		}
	}
	return decls
}

// ListForAgent returns declarations filtered by both tier and an explicit
// grants list. A tool is included if:
//   - Tier check passes (MinTier is empty OR TierAtLeast(tier, MinTier))
//   - Grant check passes (grants is empty/nil OR tool name is in grants)
//
// agentID is accepted for future use (logging, audit) but not filtered on.
func (r *Registry) ListForAgent(agentID string, tier string, grants []string) []ToolDeclaration {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var decls []ToolDeclaration
	for _, t := range r.tools {
		decl := t.Declaration()
		if decl.MinTier != "" && !TierAtLeast(tier, decl.MinTier) {
			continue
		}
		if len(grants) > 0 && !slices.Contains(grants, decl.Name) {
			continue
		}
		decls = append(decls, decl)
	}
	slog.Debug("ktp: ListForAgent result", "agent_id", agentID, "tier", tier, "registered", len(r.tools), "visible", len(decls), "grants", len(grants))
	return decls
}

// DefaultToolsForTier returns tool names that are pre-selected defaults for the
// given tier. A tool is included if the tier appears in its DefaultTiers list
// and the tier satisfies the tool's MinTier requirement.
func (r *Registry) DefaultToolsForTier(tier string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var names []string
	for _, t := range r.tools {
		decl := t.Declaration()
		if decl.MinTier != "" && !TierAtLeast(tier, decl.MinTier) {
			continue
		}
		if slices.Contains(decl.DefaultTiers, tier) {
			names = append(names, decl.Name)
		}
	}
	slices.Sort(names)
	return names
}

// GetModelToolDefinitions returns model-ready function-calling definitions for
// tools visible to the given agent. Each action of each matching tool is
// converted via SchemaToModelFormat.
func (r *Registry) GetModelToolDefinitions(agentID string, tier string, grants []string) []map[string]any {
	decls := r.ListForAgent(agentID, tier, grants)

	var defs []map[string]any
	for _, decl := range decls {
		for _, action := range decl.Actions {
			defs = append(defs, SchemaToModelFormat(action))
		}
	}
	return defs
}

// ModelToolDefinition is a KTP-local type for tool definitions sent to models.
// It uses "tool__action" naming and avoids importing internal/models.
type ModelToolDefinition struct {
	Name        string         `json:"name"`        // "tool__action" format
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// GetToolDefinitionsForModel returns tool definitions using "tool__action"
// naming, suitable for populating model CompletionRequest.Tools. Each action
// of each matching tool produces one definition.
func (r *Registry) GetToolDefinitionsForModel(agentID, tier string, grants []string) []ModelToolDefinition {
	decls := r.ListForAgent(agentID, tier, grants)

	var defs []ModelToolDefinition
	for _, decl := range decls {
		for _, action := range decl.Actions {
			defs = append(defs, ModelToolDefinition{
				Name:        JoinToolAction(decl.Name, action.Name),
				Description: action.Description,
				Parameters:  schemaToMap(action.Parameters),
			})
		}
	}
	return defs
}
