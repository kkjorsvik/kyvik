package ktp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"

	"github.com/oklog/ulid/v2"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

// AuditLogger is the interface the gate uses to record permission decisions
// and tool executions. Implementations can write to the audit store, slog, etc.
type AuditLogger interface {
	LogToolPermission(ctx context.Context, result PermissionResult) error
	LogToolExecution(ctx context.Context, req ToolRequest, resp *ToolResponse) error
}

// PermissionResult captures the outcome of a permission check.
type PermissionResult struct {
	Allowed     bool   `json:"allowed"`
	Token       string `json:"token,omitempty"`
	Reason      string `json:"reason"`
	AgentID     string `json:"agent_id"`
	Tool        string `json:"tool"`
	Action      string `json:"action"`
	Destructive bool   `json:"destructive,omitempty"`
	Tier        string `json:"tier,omitempty"`
}

// AgentStore is the narrow persistence interface for the permission gate.
// The store implementations satisfy this interface.
type AgentStore interface {
	GetAgent(ctx context.Context, id string) (*types.AgentConfig, error)
}

// PermissionGate enforces KTP-level permissions before tool execution.
// It bridges the legacy template system (reader/worker/admin) to KTP tiers
// and checks tool grants + capability requirements.
type PermissionGate struct {
	store             AgentStore
	audit             AuditLogger
	allowUnrestricted bool
}

// NewPermissionGate creates a PermissionGate with the given store and audit logger.
func NewPermissionGate(store AgentStore, audit AuditLogger) *PermissionGate {
	return &PermissionGate{store: store, audit: audit}
}

// SetAllowUnrestricted controls whether agents with the unrestricted tier are
// permitted. When false (the default), all unrestricted-tier tool requests are denied.
func (g *PermissionGate) SetAllowUnrestricted(allow bool) {
	g.allowUnrestricted = allow
}

// AllowUnrestricted returns whether the unrestricted tier is enabled.
func (g *PermissionGate) AllowUnrestricted() bool {
	return g.allowUnrestricted
}

// Check verifies that the agent identified by agentID is permitted to invoke
// the given action on the tool described by toolDecl. On success it returns a
// PermissionResult with Allowed=true and a ULID token. On denial the result
// explains the reason. params is accepted for future parameter-level checks.
func (g *PermissionGate) Check(ctx context.Context, agentID string, toolDecl ToolDeclaration, actionName string, params map[string]any) (*PermissionResult, error) {
	agent, err := g.store.GetAgent(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("get agent %s: %w", agentID, err)
	}

	action, ok := toolDecl.GetAction(actionName)
	if !ok {
		result := &PermissionResult{
			Allowed: false,
			Reason:  fmt.Sprintf("action %q not found on tool %q", actionName, toolDecl.Name),
			AgentID: agentID,
			Tool:    toolDecl.Name,
			Action:  actionName,
		}
		g.logResult(ctx, result)
		return result, nil
	}

	// 1. Tier check: resolve agent template to KTP tier, then compare.
	agentTier := ResolveAgentTier(agent.Template)

	if !TierAtLeast(agentTier, toolDecl.MinTier) {
		result := &PermissionResult{
			Allowed: false,
			Reason:  fmt.Sprintf("agent tier %q does not meet minimum tier %q", agentTier, toolDecl.MinTier),
			AgentID: agentID,
			Tool:    toolDecl.Name,
			Action:  actionName,
			Tier:    agentTier,
		}
		g.logResult(ctx, result)
		return result, nil
	}

	// 2. Tool grant check: if the agent has an explicit grant list, the tool must be in it.
	if len(agent.ToolGrants) > 0 && !slices.Contains(agent.ToolGrants, toolDecl.Name) {
		result := &PermissionResult{
			Allowed: false,
			Reason:  fmt.Sprintf("tool %q not in agent's tool grants", toolDecl.Name),
			AgentID: agentID,
			Tool:    toolDecl.Name,
			Action:  actionName,
			Tier:    agentTier,
		}
		g.logResult(ctx, result)
		return result, nil
	}

	// 3. Capability check: each required capability must be satisfied.
	requiredCaps, err := expandRequiredCapabilities(toolDecl.Name, action.RequiredCapabilities, params)
	if err != nil {
		result := &PermissionResult{
			Allowed: false,
			Reason:  err.Error(),
			AgentID: agentID,
			Tool:    toolDecl.Name,
			Action:  actionName,
			Tier:    agentTier,
		}
		g.logResult(ctx, result)
		return result, nil
	}
	// Add conditional delete_recursive capability for hostfs recursive deletes.
	if toolDecl.Name == "hostfs" && actionName == "delete" && boolParam(params, "recursive") {
		extra, err := expandRequiredCapabilities(toolDecl.Name, []Capability{
			{Type: "host_filesystem", Access: "delete_recursive", Resource: "{path}"},
		}, params)
		if err != nil {
			result := &PermissionResult{
				Allowed: false,
				Reason:  err.Error(),
				AgentID: agentID,
				Tool:    toolDecl.Name,
				Action:  actionName,
				Tier:    agentTier,
			}
			g.logResult(ctx, result)
			return result, nil
		}
		requiredCaps = append(requiredCaps, extra...)
	}

	effectiveCaps := getEffectiveCapabilities(agent, agentTier)
	for _, required := range requiredCaps {
		if !hasMatchingCapability(effectiveCaps, required) {
			result := &PermissionResult{
				Allowed: false,
				Reason:  fmt.Sprintf("missing capability %s/%s/%s", required.Type, required.Access, required.Resource),
				AgentID: agentID,
				Tool:    toolDecl.Name,
				Action:  actionName,
				Tier:    agentTier,
			}
			g.logResult(ctx, result)
			return result, nil
		}
	}

	// All checks passed.
	result := &PermissionResult{
		Allowed:     true,
		Token:       ulid.Make().String(),
		Reason:      "all checks passed",
		AgentID:     agentID,
		Tool:        toolDecl.Name,
		Action:      actionName,
		Destructive: action.Destructive,
		Tier:        agentTier,
	}
	g.logResult(ctx, result)
	return result, nil
}

// logResult logs the permission result via the audit logger (best-effort).
func (g *PermissionGate) logResult(ctx context.Context, result *PermissionResult) {
	if g.audit == nil {
		return
	}
	if err := g.audit.LogToolPermission(ctx, *result); err != nil {
		slog.Warn("failed to audit log permission result",
			"agent_id", result.AgentID,
			"tool", result.Tool,
			"error", err,
		)
	}
}

// ResolveAgentTier maps legacy permission template names to KTP tier strings.
// Valid KTP tier names pass through unchanged. Unknown templates return ""
// which will fail TierAtLeast (deny-by-default).
func ResolveAgentTier(template string) string {
	// Legacy "worker" template maps to KTP "writer" tier.
	if template == "worker" {
		return TierWriter
	}
	// "guide" template maps to its own tier (same privilege level as reader).
	if template == "guide" {
		return TierGuide
	}
	// "reader", "writer", "operator", "admin" are valid KTP tiers.
	if TierLevel(template) >= 0 {
		return template
	}
	// Unknown → empty string, which fails TierAtLeast (deny-by-default).
	slog.Warn("ktp: unknown template mapped to empty tier (deny-by-default)", "template", template)
	return ""
}

// toKTPCapability converts a types.Capability to a KTP Capability.
func toKTPCapability(c types.Capability) Capability {
	return Capability{Type: c.Tool, Access: c.Action, Resource: c.Resource}
}

// getEffectiveCapabilities returns the KTP capabilities for an agent.
// If the agent has explicit CapabilityGrants, those are used (converted).
// Otherwise, the default capabilities for the agent's tier are returned.
func getEffectiveCapabilities(agent *types.AgentConfig, tier string) []Capability {
	base := defaultCapabilities(tier)
	if len(agent.CapabilityGrants) > 0 {
		caps := make([]Capability, len(agent.CapabilityGrants))
		for i, c := range agent.CapabilityGrants {
			caps[i] = toKTPCapability(c)
		}
		base = caps
	}
	hostfsCaps := hostFSCapabilities(agent)
	if len(hostfsCaps) == 0 {
		return base
	}
	return append(base, hostfsCaps...)
}

func hostFSCapabilities(agent *types.AgentConfig) []Capability {
	var caps []Capability
	var entries []types.HostFilesystemAllowlistEntry
	if agent.HostFilesystem != nil && len(agent.HostFilesystem.Allowlist) > 0 {
		entries = agent.HostFilesystem.Allowlist
	} else if agent.HostPaths != nil {
		for _, p := range agent.HostPaths.Read {
			entries = append(entries, types.HostFilesystemAllowlistEntry{Path: p, Access: "read"})
		}
		for _, p := range agent.HostPaths.Write {
			entries = append(entries, types.HostFilesystemAllowlistEntry{Path: p, Access: "write"})
		}
	}
	for _, entry := range entries {
		if entry.Path == "" {
			continue
		}
		resource := normalizeHostFSResource(entry.Path)
		switch strings.ToLower(strings.TrimSpace(entry.Access)) {
		case "read":
			caps = append(caps, Capability{Type: "host_filesystem", Access: "read", Resource: resource})
		case "write":
			caps = append(caps, Capability{Type: "host_filesystem", Access: "write", Resource: resource})
		}
	}
	return caps
}

func normalizeHostFSResource(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if strings.HasSuffix(trimmed, "/*") {
		base := strings.TrimSuffix(trimmed, "/*")
		return filepath.Clean(base) + "/*"
	}
	cleaned := filepath.Clean(trimmed)
	if strings.HasSuffix(trimmed, string(filepath.Separator)) {
		return cleaned + "/*"
	}
	return cleaned
}

func expandRequiredCapabilities(toolName string, required []Capability, params map[string]any) ([]Capability, error) {
	if len(required) == 0 {
		return nil, nil
	}
	out := make([]Capability, len(required))
	for i, cap := range required {
		resource, err := expandResource(cap.Resource, params)
		if err != nil {
			return nil, err
		}
		out[i] = Capability{Type: cap.Type, Access: cap.Access, Resource: resource}
	}
	return out, nil
}

func expandResource(resource string, params map[string]any) (string, error) {
	if !strings.Contains(resource, "{path}") {
		return resource, nil
	}
	raw, ok := params["path"]
	if !ok {
		return "", fmt.Errorf("missing parameter: path")
	}
	path, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("parameter path must be a string")
	}
	cleaned := filepath.Clean(path)
	if !filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("path must be absolute")
	}
	return strings.ReplaceAll(resource, "{path}", cleaned), nil
}

func boolParam(params map[string]any, key string) bool {
	raw, ok := params[key]
	if !ok {
		return false
	}
	switch v := raw.(type) {
	case bool:
		return v
	case json.Number:
		s := strings.ToLower(v.String())
		return s == "1" || s == "true"
	case string:
		s := strings.ToLower(strings.TrimSpace(v))
		return s == "1" || s == "true"
	default:
		return false
	}
}

// hasMatchingCapability returns true if any capability in the list satisfies
// the required capability using KTP's wildcard and path-prefix matching.
func hasMatchingCapability(caps []Capability, required Capability) bool {
	for _, c := range caps {
		if c.Matches(required) {
			return true
		}
	}
	return false
}

// defaultCapabilities returns the flattened (no runtime inheritance) set of
// default capabilities for a KTP tier. Each tier includes all lower-tier caps.
func defaultCapabilities(tier string) []Capability {
	switch tier {
	case TierReader:
		return readerCaps()
	case TierGuide:
		return guideCaps()
	case TierWriter:
		return append(readerCaps(), writerExtraCaps()...)
	case TierOperator:
		return append(append(readerCaps(), writerExtraCaps()...), operatorExtraCaps()...)
	case TierAdmin:
		return append(append(append(readerCaps(), writerExtraCaps()...), operatorExtraCaps()...), adminExtraCaps()...)
	default:
		return nil
	}
}

func readerCaps() []Capability {
	return []Capability{
		{Type: "filesystem", Access: "read", Resource: "{workspace}/*"},
		{Type: "memory", Access: "read", Resource: "*"},
		{Type: "memory", Access: "write", Resource: "*"},
		{Type: "network", Access: "read", Resource: "*"},
		{Type: "scheduler", Access: "read", Resource: "*"},
		{Type: "scheduler", Access: "write", Resource: "*"},
		{Type: "github", Access: "read", Resource: "api.github.com"},
		{Type: "git", Access: "read", Resource: "*"},
		{Type: "database", Access: "select", Resource: "*"},
		{Type: "obsidian", Access: "read", Resource: "*"},
	}
}

func guideCaps() []Capability {
	return []Capability{
		{Type: "system", Access: "read", Resource: "agents"},
		{Type: "system", Access: "read", Resource: "overview"},
		{Type: "system", Access: "read", Resource: "spending"},
		{Type: "system", Access: "read", Resource: "audit"},
		{Type: "system", Access: "read", Resource: "security"},
		{Type: "memory", Access: "read", Resource: "*"},
		{Type: "memory", Access: "write", Resource: "*"},
		{Type: "scheduler", Access: "read", Resource: "*"},
		{Type: "scheduler", Access: "write", Resource: "*"},
	}
}

func writerExtraCaps() []Capability {
	return []Capability{
		{Type: "filesystem", Access: "write", Resource: "{workspace}/*"},
		{Type: "network", Access: "write", Resource: "*"},
		{Type: "github", Access: "issues", Resource: "api.github.com"},
		{Type: "git", Access: "write", Resource: "*"},
		{Type: "database", Access: "insert", Resource: "*"},
		{Type: "database", Access: "update", Resource: "*"},
		{Type: "workflow", Access: "read", Resource: "*"},
		{Type: "workflow", Access: "write", Resource: "*"},
		{Type: "workflow", Access: "execute", Resource: "*"},
		{Type: "obsidian", Access: "write", Resource: "*"},
	}
}

func operatorExtraCaps() []Capability {
	return []Capability{
		{Type: "shell", Access: "execute", Resource: "*"},
		{Type: "process", Access: "execute", Resource: "*"},
		{Type: "code", Access: "execute", Resource: "*"},
		{Type: "browser", Access: "execute", Resource: "*"},
		{Type: "github", Access: "pull_requests", Resource: "api.github.com"},
		{Type: "git", Access: "push", Resource: "*"},
		{Type: "docker", Access: "read", Resource: "*"},
		{Type: "docker", Access: "manage", Resource: "*"},
		{Type: "database", Access: "delete", Resource: "*"},
		{Type: "obsidian", Access: "delete", Resource: "*"},
	}
}

func adminExtraCaps() []Capability {
	// Admin gets full system read access (system_status, agent_list, etc.).
	// These are in guideCaps but not in the operator stack, so we add them
	// explicitly here so admin agents can inspect system state.
	return []Capability{
		{Type: "system", Access: "read", Resource: "*"},
		{Type: "docker", Access: "registry", Resource: "*"},
	}
}

