// Package router implements model slot resolution and provider registry
// for multi-model agent routing.
package router

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

// ModelSlot defines a named model endpoint an agent can use.
type ModelSlot struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// RoutingConfig controls how an agent selects between its model slots.
type RoutingConfig struct {
	Slots          []ModelSlot `json:"slots"`
	DefaultSlot    string      `json:"default_slot"`
	ClassifierSlot string      `json:"classifier_slot,omitempty"`
	AutoRoute      bool        `json:"auto_route,omitempty"`
	TriggerPrefix  bool        `json:"trigger_prefix,omitempty"`
	FallbackSlot   string      `json:"fallback_slot,omitempty"`
}

// ResolvedRouting is the validated result of slot resolution, ready for use
// by the agent loop.
type ResolvedRouting struct {
	Config      RoutingConfig
	DefaultSlot ModelSlot
}

// ResolveSlots resolves model slots from an agent's configuration.
// If ModelSlotsJSON is empty, a single "default" slot is synthesized from
// the legacy ModelConfig fields, ensuring backward compatibility.
func ResolveSlots(config types.AgentConfig) (*ResolvedRouting, error) {
	// Legacy path: no slots configured, use ModelConfig directly
	if config.ModelSlotsJSON == "" {
		slot := ModelSlot{
			Name:     "default",
			Provider: config.ModelConfig.Provider,
			Model:    config.ModelConfig.Model,
		}
		return &ResolvedRouting{
			Config: RoutingConfig{
				Slots:       []ModelSlot{slot},
				DefaultSlot: "default",
			},
			DefaultSlot: slot,
		}, nil
	}

	// Parse routing config or build one from slots array
	var rc RoutingConfig

	// Try parsing as RoutingConfig first (if routing_config_json is populated)
	if config.RoutingConfigJSON != "" {
		if err := json.Unmarshal([]byte(config.RoutingConfigJSON), &rc); err != nil {
			return nil, fmt.Errorf("invalid routing config JSON: %w", err)
		}
	}

	// Parse slots from ModelSlotsJSON
	var slots []ModelSlot
	if err := json.Unmarshal([]byte(config.ModelSlotsJSON), &slots); err != nil {
		return nil, fmt.Errorf("invalid model slots JSON: %w", err)
	}

	if len(slots) == 0 {
		return nil, fmt.Errorf("model slots array is empty")
	}

	// Validate each slot
	for i, slot := range slots {
		if slot.Name == "" {
			return nil, fmt.Errorf("slot %d has empty name", i)
		}
		if slot.Provider == "" {
			return nil, fmt.Errorf("slot %q has empty provider", slot.Name)
		}
		if slot.Model == "" {
			return nil, fmt.Errorf("slot %q has empty model", slot.Name)
		}
	}

	// Use slots from ModelSlotsJSON (overrides any in RoutingConfig)
	rc.Slots = slots

	// Default the default slot name
	if rc.DefaultSlot == "" {
		rc.DefaultSlot = "default"
	}

	// Build lookup map
	slotMap := make(map[string]ModelSlot, len(rc.Slots))
	for _, s := range rc.Slots {
		slotMap[s.Name] = s
	}

	// Validate default slot exists
	defaultSlot, ok := slotMap[rc.DefaultSlot]
	if !ok {
		return nil, fmt.Errorf("default slot %q not found in slots", rc.DefaultSlot)
	}

	// Warn on bad classifier/fallback slots (non-fatal)
	if rc.ClassifierSlot != "" {
		if _, ok := slotMap[rc.ClassifierSlot]; !ok {
			slog.Warn("classifier slot not found in slots", "slot", rc.ClassifierSlot)
		}
	}
	if rc.FallbackSlot != "" {
		if _, ok := slotMap[rc.FallbackSlot]; !ok {
			slog.Warn("fallback slot not found in slots", "slot", rc.FallbackSlot)
		}
	}

	return &ResolvedRouting{
		Config:      rc,
		DefaultSlot: defaultSlot,
	}, nil
}
