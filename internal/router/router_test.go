package router

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/kkjorsvik/kyvik/internal/models"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// makeMultiSlotConfig builds an AgentConfig with the given slots and routing config.
func makeMultiSlotConfig(slots []ModelSlot, rc RoutingConfig) types.AgentConfig {
	slotsJSON, _ := json.Marshal(slots)
	rcJSON, _ := json.Marshal(rc)
	return types.AgentConfig{
		ID:   "test-agent",
		Name: "Test Agent",
		ModelConfig: types.ModelConfig{
			Provider: slots[0].Provider,
			Model:    slots[0].Model,
		},
		ModelSlotsJSON:    string(slotsJSON),
		RoutingConfigJSON: string(rcJSON),
	}
}

func TestRoute_SingleSlot(t *testing.T) {
	mock := &classifierMockProvider{name: "openrouter"}
	registry := NewProviderRegistry(staticSource(map[string]models.Provider{"openrouter": mock}))
	r := NewRouter(registry)

	config := types.AgentConfig{
		ID:   "agent-single",
		Name: "Single Slot Agent",
		ModelConfig: types.ModelConfig{
			Provider: "openrouter",
			Model:    "gpt-4o-mini",
		},
	}

	d, err := r.Route(context.Background(), "agent-single",
		IncomingMessage{Content: "hello"}, config, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.RoutedBy != "default" {
		t.Errorf("RoutedBy = %q, want %q", d.RoutedBy, "default")
	}
	if d.Slot.Name != "default" {
		t.Errorf("Slot.Name = %q, want %q", d.Slot.Name, "default")
	}
}

func TestRoute_PrefixTrigger(t *testing.T) {
	mock := &classifierMockProvider{name: "openrouter"}
	registry := NewProviderRegistry(staticSource(map[string]models.Provider{"openrouter": mock}))
	r := NewRouter(registry)

	config := makeMultiSlotConfig(testSlots, RoutingConfig{
		DefaultSlot:   "fast",
		TriggerPrefix: true,
	})

	d, err := r.Route(context.Background(), "agent-prefix",
		IncomingMessage{Content: "reason: explain this"}, config, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.RoutedBy != "prefix" {
		t.Errorf("RoutedBy = %q, want %q", d.RoutedBy, "prefix")
	}
	if d.Slot.Name != "reason" {
		t.Errorf("Slot.Name = %q, want %q", d.Slot.Name, "reason")
	}
	if d.Message != "explain this" {
		t.Errorf("Message = %q, want %q", d.Message, "explain this")
	}
}

func TestRoute_VisionRoute(t *testing.T) {
	mock := &classifierMockProvider{name: "openrouter"}
	registry := NewProviderRegistry(staticSource(map[string]models.Provider{"openrouter": mock}))
	r := NewRouter(registry)

	slots := append(testSlots, ModelSlot{Name: "vision", Provider: "openrouter", Model: "gpt-4o"})
	config := makeMultiSlotConfig(slots, RoutingConfig{
		DefaultSlot: "fast",
	})

	d, err := r.Route(context.Background(), "agent-vision",
		IncomingMessage{
			Content:     "what is this?",
			Attachments: []types.Attachment{{ContentType: "image/png", Filename: "photo.png"}},
		}, config, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.RoutedBy != "vision" {
		t.Errorf("RoutedBy = %q, want %q", d.RoutedBy, "vision")
	}
	if d.Slot.Name != "vision" {
		t.Errorf("Slot.Name = %q, want %q", d.Slot.Name, "vision")
	}
}

func TestRoute_PrefixOverridesVision(t *testing.T) {
	mock := &classifierMockProvider{name: "openrouter"}
	registry := NewProviderRegistry(staticSource(map[string]models.Provider{"openrouter": mock}))
	r := NewRouter(registry)

	slots := append(testSlots, ModelSlot{Name: "vision", Provider: "openrouter", Model: "gpt-4o"})
	config := makeMultiSlotConfig(slots, RoutingConfig{
		DefaultSlot:   "fast",
		TriggerPrefix: true,
	})

	d, err := r.Route(context.Background(), "agent-prefix-vision",
		IncomingMessage{
			Content:     "reason: explain this image",
			Attachments: []types.Attachment{{ContentType: "image/png", Filename: "photo.png"}},
		}, config, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.RoutedBy != "prefix" {
		t.Errorf("RoutedBy = %q, want %q (prefix should override vision)", d.RoutedBy, "prefix")
	}
	if d.Slot.Name != "reason" {
		t.Errorf("Slot.Name = %q, want %q", d.Slot.Name, "reason")
	}
}

func TestRoute_Classifier(t *testing.T) {
	mock := &classifierMockProvider{
		name:     "openrouter",
		response: `{"slot":"reason","confidence":"high","reason":"deep thinking needed"}`,
	}
	registry := NewProviderRegistry(staticSource(map[string]models.Provider{"openrouter": mock}))
	r := NewRouter(registry)

	config := makeMultiSlotConfig(testSlots, RoutingConfig{
		DefaultSlot:    "fast",
		AutoRoute:      true,
		ClassifierSlot: "fast",
	})

	d, err := r.Route(context.Background(), "agent-classify",
		IncomingMessage{Content: "explain quantum entanglement"}, config, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.RoutedBy != "classifier" {
		t.Errorf("RoutedBy = %q, want %q", d.RoutedBy, "classifier")
	}
	if d.Slot.Name != "reason" {
		t.Errorf("Slot.Name = %q, want %q", d.Slot.Name, "reason")
	}
	if d.ClassifierCost.Cost <= 0 {
		t.Error("expected ClassifierCost.Cost > 0")
	}
}

func TestRoute_ClassifierMalformedJSON(t *testing.T) {
	mock := &classifierMockProvider{
		name:     "openrouter",
		response: "use reason slot",
	}
	registry := NewProviderRegistry(staticSource(map[string]models.Provider{"openrouter": mock}))
	r := NewRouter(registry)

	config := makeMultiSlotConfig(testSlots, RoutingConfig{
		DefaultSlot:    "fast",
		AutoRoute:      true,
		ClassifierSlot: "fast",
	})

	d, err := r.Route(context.Background(), "agent-malformed",
		IncomingMessage{Content: "hello"}, config, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.RoutedBy != "default" {
		t.Errorf("RoutedBy = %q, want %q (malformed JSON should fall back)", d.RoutedBy, "default")
	}
	if d.Slot.Name != "fast" {
		t.Errorf("Slot.Name = %q, want %q", d.Slot.Name, "fast")
	}
}

func TestRoute_ClassifierLowConfidence(t *testing.T) {
	mock := &classifierMockProvider{
		name:     "openrouter",
		response: `{"slot":"reason","confidence":"low","reason":"not sure"}`,
	}
	registry := NewProviderRegistry(staticSource(map[string]models.Provider{"openrouter": mock}))
	r := NewRouter(registry)

	config := makeMultiSlotConfig(testSlots, RoutingConfig{
		DefaultSlot:    "creative",
		AutoRoute:      true,
		ClassifierSlot: "fast",
		FallbackSlot:   "fast",
	})

	d, err := r.Route(context.Background(), "agent-lowconf",
		IncomingMessage{Content: "hello"}, config, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.RoutedBy != "classifier" {
		t.Errorf("RoutedBy = %q, want %q", d.RoutedBy, "classifier")
	}
	if d.Slot.Name != "fast" {
		t.Errorf("Slot.Name = %q, want %q (low confidence should use fallback)", d.Slot.Name, "fast")
	}
}

func TestRoute_NoRoutingEnabled(t *testing.T) {
	mock := &classifierMockProvider{name: "openrouter"}
	registry := NewProviderRegistry(staticSource(map[string]models.Provider{"openrouter": mock}))
	r := NewRouter(registry)

	config := makeMultiSlotConfig(testSlots, RoutingConfig{
		DefaultSlot:   "fast",
		AutoRoute:     false,
		TriggerPrefix: false,
	})

	d, err := r.Route(context.Background(), "agent-noroute",
		IncomingMessage{Content: "reason: this should not trigger"}, config, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.RoutedBy != "default" {
		t.Errorf("RoutedBy = %q, want %q", d.RoutedBy, "default")
	}
	if d.Slot.Name != "fast" {
		t.Errorf("Slot.Name = %q, want %q", d.Slot.Name, "fast")
	}
}

func TestRoute_ProviderUnavailable(t *testing.T) {
	// Only register "openrouter", not "other-provider"
	mock := &classifierMockProvider{name: "openrouter"}
	registry := NewProviderRegistry(staticSource(map[string]models.Provider{"openrouter": mock}))
	r := NewRouter(registry)

	slots := []ModelSlot{
		{Name: "default", Provider: "openrouter", Model: "gpt-4o-mini"},
		{Name: "heavy", Provider: "other-provider", Model: "o1-preview"},
	}
	config := makeMultiSlotConfig(slots, RoutingConfig{
		DefaultSlot:   "default",
		TriggerPrefix: true,
	})

	d, err := r.Route(context.Background(), "agent-noprov",
		IncomingMessage{Content: "heavy: do something"}, config, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should fall back to default slot because "other-provider" isn't registered
	if d.Slot.Name != "default" {
		t.Errorf("Slot.Name = %q, want %q (should fallback)", d.Slot.Name, "default")
	}
	if d.Details == "" {
		t.Error("expected Details to contain fallback explanation")
	}

	_ = fmt.Sprintf("details: %s", d.Details) // use fmt to avoid import error
}
