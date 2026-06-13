package router

import (
	"encoding/json"
	"testing"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

func TestResolveSlots_LegacyFallback(t *testing.T) {
	config := types.AgentConfig{
		ModelConfig: types.ModelConfig{
			Provider: "openrouter",
			Model:    "deepseek/deepseek-chat",
		},
	}

	resolved, err := ResolveSlots(config)
	if err != nil {
		t.Fatalf("ResolveSlots: %v", err)
	}

	if resolved.DefaultSlot.Name != "default" {
		t.Errorf("DefaultSlot.Name = %q, want %q", resolved.DefaultSlot.Name, "default")
	}
	if resolved.DefaultSlot.Provider != "openrouter" {
		t.Errorf("DefaultSlot.Provider = %q, want %q", resolved.DefaultSlot.Provider, "openrouter")
	}
	if resolved.DefaultSlot.Model != "deepseek/deepseek-chat" {
		t.Errorf("DefaultSlot.Model = %q, want %q", resolved.DefaultSlot.Model, "deepseek/deepseek-chat")
	}
	if len(resolved.Config.Slots) != 1 {
		t.Errorf("len(Slots) = %d, want 1", len(resolved.Config.Slots))
	}
}

func TestResolveSlots_MultiSlot(t *testing.T) {
	slots := []ModelSlot{
		{Name: "default", Provider: "openrouter", Model: "deepseek/deepseek-chat"},
		{Name: "reasoning", Provider: "openrouter", Model: "anthropic/claude-3.5-sonnet"},
		{Name: "fast", Provider: "ollama", Model: "llama3"},
	}
	slotsJSON, _ := json.Marshal(slots)

	config := types.AgentConfig{
		ModelSlotsJSON: string(slotsJSON),
	}

	resolved, err := ResolveSlots(config)
	if err != nil {
		t.Fatalf("ResolveSlots: %v", err)
	}

	if len(resolved.Config.Slots) != 3 {
		t.Errorf("len(Slots) = %d, want 3", len(resolved.Config.Slots))
	}
	if resolved.DefaultSlot.Provider != "openrouter" {
		t.Errorf("DefaultSlot.Provider = %q, want %q", resolved.DefaultSlot.Provider, "openrouter")
	}
	if resolved.DefaultSlot.Model != "deepseek/deepseek-chat" {
		t.Errorf("DefaultSlot.Model = %q, want %q", resolved.DefaultSlot.Model, "deepseek/deepseek-chat")
	}
}

func TestResolveSlots_MissingDefaultSlot(t *testing.T) {
	slots := []ModelSlot{
		{Name: "reasoning", Provider: "openrouter", Model: "claude-3.5"},
	}
	slotsJSON, _ := json.Marshal(slots)

	config := types.AgentConfig{
		ModelSlotsJSON: string(slotsJSON),
		// DefaultSlot defaults to "default" which doesn't exist
	}

	_, err := ResolveSlots(config)
	if err == nil {
		t.Fatal("expected error for missing default slot, got nil")
	}
}

func TestResolveSlots_EmptySlotsArray(t *testing.T) {
	config := types.AgentConfig{
		ModelSlotsJSON: "[]",
	}

	_, err := ResolveSlots(config)
	if err == nil {
		t.Fatal("expected error for empty slots array, got nil")
	}
}

func TestResolveSlots_InvalidJSON(t *testing.T) {
	config := types.AgentConfig{
		ModelSlotsJSON: "{not valid json",
	}

	_, err := ResolveSlots(config)
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestResolveSlots_SlotMissingProvider(t *testing.T) {
	slots := []ModelSlot{
		{Name: "default", Provider: "", Model: "some-model"},
	}
	slotsJSON, _ := json.Marshal(slots)

	config := types.AgentConfig{
		ModelSlotsJSON: string(slotsJSON),
	}

	_, err := ResolveSlots(config)
	if err == nil {
		t.Fatal("expected error for slot with empty provider, got nil")
	}
}

func TestResolveSlots_SlotMissingModel(t *testing.T) {
	slots := []ModelSlot{
		{Name: "default", Provider: "openrouter", Model: ""},
	}
	slotsJSON, _ := json.Marshal(slots)

	config := types.AgentConfig{
		ModelSlotsJSON: string(slotsJSON),
	}

	_, err := ResolveSlots(config)
	if err == nil {
		t.Fatal("expected error for slot with empty model, got nil")
	}
}

func TestResolveSlots_CustomDefaultSlot(t *testing.T) {
	slots := []ModelSlot{
		{Name: "main", Provider: "openrouter", Model: "gpt-4"},
		{Name: "fast", Provider: "ollama", Model: "llama3"},
	}
	slotsJSON, _ := json.Marshal(slots)
	routingJSON, _ := json.Marshal(RoutingConfig{DefaultSlot: "main"})

	config := types.AgentConfig{
		ModelSlotsJSON:    string(slotsJSON),
		RoutingConfigJSON: string(routingJSON),
	}

	resolved, err := ResolveSlots(config)
	if err != nil {
		t.Fatalf("ResolveSlots: %v", err)
	}

	if resolved.DefaultSlot.Name != "main" {
		t.Errorf("DefaultSlot.Name = %q, want %q", resolved.DefaultSlot.Name, "main")
	}
	if resolved.DefaultSlot.Provider != "openrouter" {
		t.Errorf("DefaultSlot.Provider = %q, want %q", resolved.DefaultSlot.Provider, "openrouter")
	}
}

func TestResolveSlots_SlotMissingName(t *testing.T) {
	slots := []ModelSlot{
		{Name: "", Provider: "openrouter", Model: "gpt-4"},
	}
	slotsJSON, _ := json.Marshal(slots)

	config := types.AgentConfig{
		ModelSlotsJSON: string(slotsJSON),
	}

	_, err := ResolveSlots(config)
	if err == nil {
		t.Fatal("expected error for slot with empty name, got nil")
	}
}
