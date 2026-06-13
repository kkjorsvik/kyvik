// Package builtins provides built-in agent template definitions that are
// auto-seeded on first startup.
package builtins

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/kkjorsvik/kyvik/internal/templates"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

//go:embed definitions/*.yaml
var definitionsFS embed.FS

// BuiltinTemplate is the YAML shape for a built-in template definition.
type BuiltinTemplate struct {
	Name              string                          `yaml:"name"`
	Description       string                          `yaml:"description"`
	Category          string                          `yaml:"category"`          // "ready" or "setup_required"
	SetupNotes        string                          `yaml:"setup_notes"`
	Config            builtinConfig                   `yaml:"config"`
	LockedFields      []string                        `yaml:"locked_fields"`
	ConstrainedFields map[string]types.ConstraintRule `yaml:"constrained_fields"`
}

// builtinConfig mirrors types.AgentConfig but uses YAML tags.
type builtinConfig struct {
	Name                string            `yaml:"name"`
	Description         string            `yaml:"description"`
	SoulContent         string            `yaml:"soul_content"`
	IdentityContent     string            `yaml:"identity_content"`
	Template            string            `yaml:"template"`
	ToolGrants          []string          `yaml:"tool_grants"`
	WebUIEnabled        bool              `yaml:"webui_enabled"`
	AutoExtractMemories bool              `yaml:"auto_extract_memories"`
	HistoryLimit        int               `yaml:"history_limit"`
	MemoryLimit         int               `yaml:"memory_limit"`
	Limits              builtinLimits     `yaml:"limits"`
	Metadata            map[string]string `yaml:"metadata"`
}

type builtinLimits struct {
	MaxTokensPerDay   int64   `yaml:"max_tokens_per_day"`
	MaxTokensPerMonth int64   `yaml:"max_tokens_per_month"`
	MaxSpendPerDay    float64 `yaml:"max_spend_per_day"`
	MaxSpendPerMonth  float64 `yaml:"max_spend_per_month"`
}

// StateStore is the minimal interface for checking/setting system state.
type StateStore interface {
	GetSystemState(ctx context.Context, key string) (string, error)
	SetSystemState(ctx context.Context, key, value string) error
}

const systemStateKey = "builtin_templates_seeded"

// EnsureBuiltinTemplates seeds built-in templates on first startup.
// Returns the number of templates seeded (0 if already done).
func EnsureBuiltinTemplates(ctx context.Context, svc *templates.Service, store StateStore) (int, error) {
	// Check if already seeded.
	val, err := store.GetSystemState(ctx, systemStateKey)
	if err != nil {
		return 0, fmt.Errorf("check system state: %w", err)
	}
	if val != "" {
		return 0, nil
	}

	// Parse all definition YAMLs.
	defs, err := loadDefinitions()
	if err != nil {
		return 0, fmt.Errorf("load definitions: %w", err)
	}

	count := 0
	for _, def := range defs {
		cfg := toAgentConfig(def)

		// Use empty createdBy so nullIfEmpty stores NULL (avoids FK violation on users table).
		_, err := svc.Create(ctx, def.Name, def.Description, "", "",
			cfg, def.LockedFields, def.ConstrainedFields)
		if err != nil {
			slog.Warn("failed to seed built-in template", "name", def.Name, "error", err)
			continue
		}
		count++
	}

	// Mark as seeded.
	if err := store.SetSystemState(ctx, systemStateKey, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return count, fmt.Errorf("set system state: %w", err)
	}

	return count, nil
}

// loadDefinitions reads and parses all YAML files from the embedded FS.
func loadDefinitions() ([]BuiltinTemplate, error) {
	var defs []BuiltinTemplate

	err := fs.WalkDir(definitionsFS, "definitions", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		data, err := definitionsFS.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}

		var def BuiltinTemplate
		if err := yaml.Unmarshal(data, &def); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		defs = append(defs, def)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return defs, nil
}

// toAgentConfig converts a builtin definition to a types.AgentConfig.
func toAgentConfig(def BuiltinTemplate) types.AgentConfig {
	return types.AgentConfig{
		Name:                def.Config.Name,
		Description:         def.Config.Description,
		SoulContent:         def.Config.SoulContent,
		IdentityContent:     def.Config.IdentityContent,
		Template:            def.Config.Template,
		ToolGrants:          def.Config.ToolGrants,
		WebUIEnabled:        def.Config.WebUIEnabled,
		AutoExtractMemories: def.Config.AutoExtractMemories,
		HistoryLimit:        def.Config.HistoryLimit,
		MemoryLimit:         def.Config.MemoryLimit,
		Limits: types.SpendingLimits{
			MaxTokensPerDay:   def.Config.Limits.MaxTokensPerDay,
			MaxTokensPerMonth: def.Config.Limits.MaxTokensPerMonth,
			MaxSpendPerDay:    def.Config.Limits.MaxSpendPerDay,
			MaxSpendPerMonth:  def.Config.Limits.MaxSpendPerMonth,
		},
		Metadata: def.Config.Metadata,
	}
}

// LoadDefinitions is exported for testing — returns all parsed definitions.
func LoadDefinitions() ([]BuiltinTemplate, error) {
	return loadDefinitions()
}
