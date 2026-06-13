// Package templates manages reusable agent configuration blueprints.
package templates

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

// Store is the minimal persistence contract required by Service.
type Store interface {
	CreateTemplate(ctx context.Context, tmpl types.AgentTemplate) error
	GetTemplate(ctx context.Context, id string) (*types.AgentTemplate, error)
	ListTemplates(ctx context.Context) ([]types.AgentTemplate, error)
	ListTemplatesByGroup(ctx context.Context, groupID string) ([]types.AgentTemplate, error)
	UpdateTemplate(ctx context.Context, tmpl types.AgentTemplate) error
	DeleteTemplate(ctx context.Context, id string) error
}

// Service manages agent template CRUD and validation.
type Service struct {
	store Store
}

// AuditOverride records a single field override for audit logging.
type AuditOverride struct {
	Field         string `json:"field"`
	TemplateValue string `json:"template_value"`
	OverrideValue string `json:"override_value"`
}

// New creates a template service.
func New(store Store) *Service {
	return &Service{store: store}
}

// Create creates a new agent template.
func (s *Service) Create(ctx context.Context, name, desc, groupID, createdBy string,
	config types.AgentConfig, locked []string, constrained map[string]types.ConstraintRule) (*types.AgentTemplate, error) {

	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("template name is required")
	}

	// Strip runtime-only fields before serializing.
	config.ID = ""
	config.DesiredState = ""
	config.ActualState = ""
	config.LastError = ""
	config.CreatedAt = time.Time{}
	config.UpdatedAt = time.Time{}

	configJSON, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("marshal config: %w", err)
	}

	if locked == nil {
		locked = []string{}
	}
	if constrained == nil {
		constrained = make(map[string]types.ConstraintRule)
	}

	tmpl := types.AgentTemplate{
		ID:                ulid.Make().String(),
		Name:              name,
		Description:       strings.TrimSpace(desc),
		GroupID:           groupID,
		ConfigJSON:        string(configJSON),
		LockedFields:      locked,
		ConstrainedFields: constrained,
		CreatedBy:         createdBy,
		CreatedAt:         time.Now().UTC(),
		UpdatedAt:         time.Now().UTC(),
	}

	if err := s.store.CreateTemplate(ctx, tmpl); err != nil {
		return nil, fmt.Errorf("create template: %w", err)
	}
	return &tmpl, nil
}

// Get retrieves a template by ID.
func (s *Service) Get(ctx context.Context, id string) (*types.AgentTemplate, error) {
	return s.store.GetTemplate(ctx, id)
}

// List returns all templates.
func (s *Service) List(ctx context.Context) ([]types.AgentTemplate, error) {
	return s.store.ListTemplates(ctx)
}

// ListForGroups returns templates visible to any of the given group IDs.
func (s *Service) ListForGroups(ctx context.Context, groupIDs []string) ([]types.AgentTemplate, error) {
	seen := make(map[string]struct{})
	var out []types.AgentTemplate
	for _, gid := range groupIDs {
		templates, err := s.store.ListTemplatesByGroup(ctx, gid)
		if err != nil {
			return nil, fmt.Errorf("list templates by group %s: %w", gid, err)
		}
		for _, t := range templates {
			if _, ok := seen[t.ID]; !ok {
				seen[t.ID] = struct{}{}
				out = append(out, t)
			}
		}
	}
	return out, nil
}

// Update updates a template's metadata and constraints.
func (s *Service) Update(ctx context.Context, tmpl types.AgentTemplate) error {
	tmpl.Name = strings.TrimSpace(tmpl.Name)
	if tmpl.Name == "" {
		return fmt.Errorf("template name is required")
	}
	if tmpl.LockedFields == nil {
		tmpl.LockedFields = []string{}
	}
	if tmpl.ConstrainedFields == nil {
		tmpl.ConstrainedFields = make(map[string]types.ConstraintRule)
	}
	return s.store.UpdateTemplate(ctx, tmpl)
}

// Delete removes a template.
func (s *Service) Delete(ctx context.Context, id string) error {
	return s.store.DeleteTemplate(ctx, id)
}

// UpdateConfig is a convenience method that strips runtime fields, marshals
// the config to JSON, and persists it on the template.
func (s *Service) UpdateConfig(ctx context.Context, id string, config types.AgentConfig) error {
	tmpl, err := s.store.GetTemplate(ctx, id)
	if err != nil {
		return err
	}

	// Strip runtime-only fields before serializing.
	config.ID = ""
	config.DesiredState = ""
	config.ActualState = ""
	config.LastError = ""
	config.CreatedAt = time.Time{}
	config.UpdatedAt = time.Time{}

	configJSON, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	tmpl.ConfigJSON = string(configJSON)
	return s.store.UpdateTemplate(ctx, *tmpl)
}

// ConfigFromTemplate deserializes the config_json back to AgentConfig.
func (s *Service) ConfigFromTemplate(ctx context.Context, id string) (*types.AgentConfig, error) {
	tmpl, err := s.store.GetTemplate(ctx, id)
	if err != nil {
		return nil, err
	}
	var cfg types.AgentConfig
	if err := json.Unmarshal([]byte(tmpl.ConfigJSON), &cfg); err != nil {
		return nil, fmt.Errorf("unmarshal template config: %w", err)
	}
	return &cfg, nil
}

// ValidateOverrides checks that overrides do not violate locked/constrained rules.
func ValidateOverrides(tmpl *types.AgentTemplate, overrides map[string]any) error {
	// Check locked fields: must not appear in overrides.
	lockedSet := make(map[string]struct{}, len(tmpl.LockedFields))
	for _, f := range tmpl.LockedFields {
		lockedSet[f] = struct{}{}
	}
	for field := range overrides {
		if _, locked := lockedSet[field]; locked {
			return fmt.Errorf("field %q is locked and cannot be overridden", field)
		}
	}

	// Check constrained fields: values must be within bounds.
	for field, rule := range tmpl.ConstrainedFields {
		val, ok := overrides[field]
		if !ok {
			continue
		}

		// Numeric constraints (min/max).
		if rule.Min != nil || rule.Max != nil {
			numVal, err := toFloat64(val)
			if err != nil {
				return fmt.Errorf("field %q: expected numeric value: %w", field, err)
			}
			if rule.Min != nil && numVal < *rule.Min {
				return fmt.Errorf("field %q: value %v is below minimum %v", field, numVal, *rule.Min)
			}
			if rule.Max != nil && numVal > *rule.Max {
				return fmt.Errorf("field %q: value %v exceeds maximum %v", field, numVal, *rule.Max)
			}
		}

		// Options constraint: value must be one of the allowed options.
		if len(rule.Options) > 0 {
			strVal := fmt.Sprintf("%v", val)
			found := false
			for _, opt := range rule.Options {
				if opt == strVal {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("field %q: value %q not in allowed options %v", field, strVal, rule.Options)
			}
		}
	}

	return nil
}

// BuildConfigWithOverrides applies overrides to a template config and returns the result.
func (s *Service) BuildConfigWithOverrides(tmpl *types.AgentTemplate, overrides map[string]any) (*types.AgentConfig, []AuditOverride, error) {
	if err := ValidateOverrides(tmpl, overrides); err != nil {
		return nil, nil, err
	}

	var cfg types.AgentConfig
	if err := json.Unmarshal([]byte(tmpl.ConfigJSON), &cfg); err != nil {
		return nil, nil, fmt.Errorf("unmarshal template config: %w", err)
	}

	var audits []AuditOverride

	// Apply overrides to config fields.
	for field, val := range overrides {
		var templateVal string
		switch field {
		case "name":
			templateVal = cfg.Name
			cfg.Name = fmt.Sprintf("%v", val)
		case "description":
			templateVal = cfg.Description
			cfg.Description = fmt.Sprintf("%v", val)
		case "system_prompt":
			templateVal = cfg.SystemPrompt
			cfg.SystemPrompt = fmt.Sprintf("%v", val)
		case "history_limit":
			templateVal = fmt.Sprintf("%d", cfg.HistoryLimit)
			if n, err := toFloat64(val); err == nil {
				cfg.HistoryLimit = int(n)
			}
		case "memory_limit":
			templateVal = fmt.Sprintf("%d", cfg.MemoryLimit)
			if n, err := toFloat64(val); err == nil {
				cfg.MemoryLimit = int(n)
			}
		case "soul_content":
			templateVal = cfg.SoulContent
			cfg.SoulContent = fmt.Sprintf("%v", val)
		case "identity_content":
			templateVal = cfg.IdentityContent
			cfg.IdentityContent = fmt.Sprintf("%v", val)
		case "provider":
			templateVal = cfg.ModelConfig.Provider
			cfg.ModelConfig.Provider = fmt.Sprintf("%v", val)
		case "model":
			templateVal = cfg.ModelConfig.Model
			cfg.ModelConfig.Model = fmt.Sprintf("%v", val)
		case "template":
			templateVal = cfg.Template
			cfg.Template = fmt.Sprintf("%v", val)
		case "max_tokens_per_day":
			templateVal = fmt.Sprintf("%d", cfg.Limits.MaxTokensPerDay)
			if n, err := toFloat64(val); err == nil {
				cfg.Limits.MaxTokensPerDay = int64(n)
			}
		case "max_tokens_per_month":
			templateVal = fmt.Sprintf("%d", cfg.Limits.MaxTokensPerMonth)
			if n, err := toFloat64(val); err == nil {
				cfg.Limits.MaxTokensPerMonth = int64(n)
			}
		case "max_spend_per_day":
			templateVal = fmt.Sprintf("%.2f", cfg.Limits.MaxSpendPerDay)
			if n, err := toFloat64(val); err == nil {
				cfg.Limits.MaxSpendPerDay = n
			}
		case "max_spend_per_month":
			templateVal = fmt.Sprintf("%.2f", cfg.Limits.MaxSpendPerMonth)
			if n, err := toFloat64(val); err == nil {
				cfg.Limits.MaxSpendPerMonth = n
			}
		default:
			continue
		}
		audits = append(audits, AuditOverride{
			Field:         field,
			TemplateValue: templateVal,
			OverrideValue: fmt.Sprintf("%v", val),
		})
	}

	return &cfg, audits, nil
}

// SaveFromAgent creates a template from an existing agent's config.
func (s *Service) SaveFromAgent(ctx context.Context, agentConfig types.AgentConfig, name, desc, groupID, createdBy string) (*types.AgentTemplate, error) {
	return s.Create(ctx, name, desc, groupID, createdBy, agentConfig, nil, nil)
}

// toFloat64 converts a value to float64 for constraint checking.
func toFloat64(v any) (float64, error) {
	switch n := v.(type) {
	case float64:
		return n, nil
	case float32:
		return float64(n), nil
	case int:
		return float64(n), nil
	case int64:
		return float64(n), nil
	case json.Number:
		return n.Float64()
	case string:
		var f float64
		_, err := fmt.Sscanf(n, "%f", &f)
		return f, err
	default:
		return 0, fmt.Errorf("cannot convert %T to float64", v)
	}
}
