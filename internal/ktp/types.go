// Package ktp defines the Kyvik Tool Protocol — a security-first native tool
// system where permissions, audit, and sandboxing are built into the protocol.
package ktp

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kkjorsvik/kyvik/pkg/types"
	"github.com/oklog/ulid/v2"
)

// Permission tiers control the minimum privilege level required to invoke a tool.
// User-facing templates (reader, worker, operator, admin) map to these KTP tiers.
// "worker" template maps to TierWriter via ResolveAgentTier.
const (
	TierReader   = "reader"
	TierWriter   = "writer"
	TierOperator = "operator"
	TierAdmin    = "admin"
	TierGuide    = "guide"
)

// ErrInvalidDeclaration is returned when a ToolDeclaration fails validation.
var ErrInvalidDeclaration = errors.New("invalid tool declaration")

// ToolDeclaration describes a tool's identity, required capabilities, and actions.
type ToolDeclaration struct {
	Name            string       `json:"name"`
	Version         string       `json:"version"`
	Description     string       `json:"description"`
	Capabilities    []Capability `json:"capabilities"`
	Actions         []ActionSpec `json:"actions"`
	MinTier         string       `json:"min_tier"`
	RequiredSecrets []string     `json:"required_secrets,omitempty"`
	DefaultTiers    []string     `json:"default_tiers,omitempty"`
}

// Capability represents a KTP-specific permission triplet (Type/Access/Resource).
type Capability struct {
	Type     string `json:"type"`
	Access   string `json:"access"`
	Resource string `json:"resource"`
}

// ActionSpec defines a single operation a tool exposes.
type ActionSpec struct {
	Name                 string       `json:"name"`
	Description          string       `json:"description"`
	Parameters           JSONSchema   `json:"parameters"`
	Returns              JSONSchema   `json:"returns"`
	RequiredCapabilities []Capability `json:"required_capabilities,omitempty"`
	Destructive          bool         `json:"destructive"`
}

// JSONSchema is a typed representation of a JSON Schema subset used for
// parameter and return value validation.
type JSONSchema struct {
	Type        string                `json:"type"`
	Properties  map[string]JSONSchema `json:"properties,omitempty"`
	Required    []string              `json:"required,omitempty"`
	Items       *JSONSchema           `json:"items,omitempty"`
	Description string                `json:"description,omitempty"`
	Enum        []string              `json:"enum,omitempty"`
	Default     any                   `json:"default,omitempty"`
	MinLength   *int                  `json:"minLength,omitempty"`
	MaxLength   *int                  `json:"maxLength,omitempty"`
	Minimum     *float64              `json:"minimum,omitempty"`
	Maximum     *float64              `json:"maximum,omitempty"`
	Pattern     string                `json:"pattern,omitempty"`
}

// ToolRequest is a ULID-tracked request to execute a tool action.
type ToolRequest struct {
	ID                 string                    `json:"id"`
	AgentID            string                    `json:"agent_id"`
	TeamID             string                    `json:"team_id,omitempty"`
	Tool               string                    `json:"tool"`
	Action             string                    `json:"action"`
	Parameters         map[string]any            `json:"parameters"`
	PermissionToken    string                    `json:"permission_token,omitempty"`
	Tier               string                    `json:"tier,omitempty"`
	SkillSandboxConfig *types.SkillSandboxConfig `json:"skill_sandbox_config,omitempty"`
	Timestamp          time.Time                 `json:"timestamp"`
}

// ToolResponse is the result of executing a tool request.
type ToolResponse struct {
	RequestID   string    `json:"request_id"`
	Success     bool      `json:"success"`
	Result      any       `json:"result,omitempty"`
	Error       string    `json:"error,omitempty"`
	ExecutionMs int64     `json:"execution_ms"`
	SandboxID   string    `json:"sandbox_id,omitempty"`
	Timestamp   time.Time `json:"timestamp"`
}

// tierLevels maps tier names to their numeric privilege level.
var tierLevels = map[string]int{
	TierReader:   0,
	TierGuide:    0, // same privilege level as reader
	TierWriter:   1,
	TierOperator: 2,
	TierAdmin:    3,
}

// TierLevel returns the numeric privilege level for a tier string.
// Unknown or empty tiers return -1.
func TierLevel(tier string) int {
	if level, ok := tierLevels[tier]; ok {
		return level
	}
	return -1
}

// TierAtLeast returns true if agentTier is at least as privileged as requiredTier.
// Unknown tiers on either side return false (deny-by-default).
// Guide tier is a named role: only guide and admin agents satisfy a guide requirement.
func TierAtLeast(agentTier, requiredTier string) bool {
	if requiredTier == TierGuide {
		return agentTier == TierGuide || agentTier == TierAdmin
	}
	agent := TierLevel(agentTier)
	required := TierLevel(requiredTier)
	if agent < 0 || required < 0 {
		return false
	}
	return agent >= required
}

// NewToolRequest creates a ToolRequest with a fresh ULID and current timestamp.
func NewToolRequest(agentID, tool, action string, params map[string]any) ToolRequest {
	return ToolRequest{
		ID:         ulid.Make().String(),
		AgentID:    agentID,
		Tool:       tool,
		Action:     action,
		Parameters: params,
		Timestamp:  time.Now(),
	}
}

// NewToolResponse creates a ToolResponse with the current timestamp.
func NewToolResponse(requestID string, success bool, result any, errMsg string, executionMs int64) ToolResponse {
	return ToolResponse{
		RequestID:   requestID,
		Success:     success,
		Result:      result,
		Error:       errMsg,
		ExecutionMs: executionMs,
		Timestamp:   time.Now(),
	}
}

// Validate checks that a ToolDeclaration has all required fields.
func (d ToolDeclaration) Validate() error {
	if d.Name == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidDeclaration)
	}
	if d.Version == "" {
		return fmt.Errorf("%w: version is required", ErrInvalidDeclaration)
	}
	if len(d.Actions) == 0 {
		return fmt.Errorf("%w: at least one action is required", ErrInvalidDeclaration)
	}
	for _, a := range d.Actions {
		if a.Name == "" {
			return fmt.Errorf("%w: action name is required", ErrInvalidDeclaration)
		}
		if a.Parameters.Type == "" {
			return fmt.Errorf("%w: action %q parameters must have a schema type", ErrInvalidDeclaration, a.Name)
		}
	}
	return nil
}

// GetAction returns the ActionSpec with the given name, or nil and false if not found.
func (d ToolDeclaration) GetAction(name string) (*ActionSpec, bool) {
	for i := range d.Actions {
		if d.Actions[i].Name == name {
			return &d.Actions[i], true
		}
	}
	return nil, false
}

// Matches reports whether c satisfies the required capability.
// Supports wildcard ("*") on each field and path-prefix matching on Resource.
func (c Capability) Matches(required Capability) bool {
	return matchField(c.Type, required.Type) &&
		matchField(c.Access, required.Access) &&
		matchResource(c.Resource, required.Resource)
}

// matchField matches a pattern against a value. "*" matches anything; otherwise exact.
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
		return false
	}
	if prefix, ok := strings.CutSuffix(pattern, "/*"); ok {
		return resource == prefix || strings.HasPrefix(resource, prefix+"/")
	}
	return pattern == resource
}
