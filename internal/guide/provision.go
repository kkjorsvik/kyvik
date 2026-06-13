package guide

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/kkjorsvik/kyvik/internal/config"
	"github.com/kkjorsvik/kyvik/internal/skills"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// ProvisionStore is the subset of store.Store needed for guide provisioning.
type ProvisionStore interface {
	GetAgent(ctx context.Context, id string) (*types.AgentConfig, error)
	CreateAgent(ctx context.Context, config types.AgentConfig) error
	UpdateAgent(ctx context.Context, config types.AgentConfig) error
	SetSystemState(ctx context.Context, key, value string) error
}

// ProvisionDeps holds all dependencies for guide agent provisioning.
type ProvisionDeps struct {
	Store        ProvisionStore
	SkillManager *skills.Manager
	DefaultModel types.ModelConfig
	GuideConfig  config.GuideConfig
}

// guideToolGrants is the canonical set of tool grants for the guide agent.
// These must match real KTP tool names (not phantom/placeholder names).
var guideToolGrants = []string{"system_status", "memory", "my_spending"}

// needsToolGrantUpdate returns true if the guide's tool grants don't match the
// canonical set. Catches stale phantom names, nil/empty grants (from UI form
// bug), or any other drift from the expected configuration.
func needsToolGrantUpdate(agent *types.AgentConfig) bool {
	if len(agent.ToolGrants) != len(guideToolGrants) {
		return true
	}
	for _, g := range guideToolGrants {
		if !slices.Contains(agent.ToolGrants, g) {
			return true
		}
	}
	return false
}

// PatchExistingGuide patches tool grants on an existing guide agent without
// creating a new one. This is the default behavior at startup — the guide is
// only created via the UI or setup wizard, never auto-created.
func PatchExistingGuide(ctx context.Context, deps ProvisionDeps) {
	existing, err := deps.Store.GetAgent(ctx, GuideAgentID)
	if err != nil {
		// Guide doesn't exist — nothing to patch.
		return
	}
	if needsToolGrantUpdate(existing) {
		slog.Info("guide: patching tool grants",
			"old_grants", existing.ToolGrants,
			"new_grants", guideToolGrants)
		existing.ToolGrants = guideToolGrants
		existing.UpdatedAt = time.Now().UTC()
		if updateErr := deps.Store.UpdateAgent(ctx, *existing); updateErr != nil {
			slog.Warn("guide: could not patch tool grants", "error", updateErr)
		}
	} else {
		slog.Debug("guide: tool grants are correct", "grants", existing.ToolGrants)
	}
}

// EnsureGuideAgent creates the guide agent if it doesn't already exist.
// If it already exists, it patches stale tool grants to match real KTP tool names.
// The guide is created in stopped state — the user must start it manually.
// Returns (true, nil) if the agent was created, (false, nil) if it already existed.
func EnsureGuideAgent(ctx context.Context, deps ProvisionDeps) (bool, error) {
	existing, err := deps.Store.GetAgent(ctx, GuideAgentID)
	if err == nil {
		// Guide already exists — patch tool grants if they don't match the canonical set.
		if needsToolGrantUpdate(existing) {
			slog.Info("guide: patching tool grants",
				"old_grants", existing.ToolGrants,
				"new_grants", guideToolGrants)
			existing.ToolGrants = guideToolGrants
			existing.UpdatedAt = time.Now().UTC()
			if updateErr := deps.Store.UpdateAgent(ctx, *existing); updateErr != nil {
				slog.Warn("guide: could not patch tool grants", "error", updateErr)
			}
		} else {
			slog.Debug("guide: tool grants are correct", "grants", existing.ToolGrants)
		}
		return false, nil
	}
	if !errors.Is(err, types.ErrNotFound) {
		return false, fmt.Errorf("check guide agent: %w", err)
	}

	toolGrants := guideToolGrants

	now := time.Now().UTC()
	agentConfig := types.AgentConfig{
		ID:                  GuideAgentID,
		Name:                GuideAgentName,
		Description:         "Built-in guide agent for the Kyvik framework",
		SoulContent:         SoulContent,
		IdentityContent:     IdentityContent,
		ModelConfig:         deps.DefaultModel,
		Template:            "guide",
		ToolGrants:          toolGrants,
		WebUIEnabled:        true,
		IsGuide:             true,
		Metadata:            map[string]string{"is_guide": "true"},
		Limits:              deps.GuideConfig.SpendingLimits,
		DesiredState:        types.DesiredStateStopped,
		AutoExtractMemories: true,
		CreatedAt:           now,
		UpdatedAt:           now,
	}

	if err := deps.Store.CreateAgent(ctx, agentConfig); err != nil {
		return false, fmt.Errorf("create guide agent: %w", err)
	}

	// Grant the system-docs skill if the skill manager is available.
	if deps.SkillManager != nil {
		if grantErr := deps.SkillManager.Grant(ctx, GuideAgentID, "system-docs", "system", agentConfig); grantErr != nil {
			// Non-fatal: guide works without the skill.
			fmt.Printf("Warning: could not grant system-docs skill to guide: %v\n", grantErr)
		}
	}

	if err := deps.Store.SetSystemState(ctx, "guide_first_run", "pending"); err != nil {
		// Non-fatal: first-run redirect just won't happen.
		fmt.Printf("Warning: could not set guide_first_run state: %v\n", err)
	}

	return true, nil
}
