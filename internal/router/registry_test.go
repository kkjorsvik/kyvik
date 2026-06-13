package router

import (
	"context"
	"testing"

	"github.com/kkjorsvik/kyvik/internal/models"
)

// mockProvider implements models.Provider for testing.
type mockProvider struct {
	name string
}

func (m *mockProvider) Complete(_ context.Context, _ models.CompletionRequest) (*models.CompletionResponse, error) {
	return &models.CompletionResponse{}, nil
}

func (m *mockProvider) Stream(_ context.Context, _ models.CompletionRequest) (<-chan models.StreamChunk, error) {
	return nil, nil
}

func (m *mockProvider) ListModels(_ context.Context) ([]models.ModelInfo, error) {
	return nil, nil
}

func (m *mockProvider) Name() string { return m.name }

func staticSource(m map[string]models.Provider) ProviderSource {
	return func() map[string]models.Provider { return m }
}

func TestGetProvider_Found(t *testing.T) {
	p := &mockProvider{name: "openrouter"}
	reg := NewProviderRegistry(staticSource(map[string]models.Provider{"openrouter": p}))

	got, ok := reg.GetProvider("openrouter")
	if !ok {
		t.Fatal("expected provider to be found")
	}
	if got.Name() != "openrouter" {
		t.Errorf("Name() = %q, want %q", got.Name(), "openrouter")
	}
}

func TestGetProvider_NotFound(t *testing.T) {
	reg := NewProviderRegistry(staticSource(map[string]models.Provider{}))

	_, ok := reg.GetProvider("nonexistent")
	if ok {
		t.Fatal("expected provider not to be found")
	}
}

func TestGetProviderForSlot(t *testing.T) {
	p := &mockProvider{name: "ollama"}
	reg := NewProviderRegistry(staticSource(map[string]models.Provider{"ollama": p}))

	slot := ModelSlot{Name: "fast", Provider: "ollama", Model: "llama3"}
	got, ok := reg.GetProviderForSlot(slot)
	if !ok {
		t.Fatal("expected provider to be found for slot")
	}
	if got.Name() != "ollama" {
		t.Errorf("Name() = %q, want %q", got.Name(), "ollama")
	}
}

func TestGetProviderForSlot_NotFound(t *testing.T) {
	reg := NewProviderRegistry(staticSource(map[string]models.Provider{}))

	slot := ModelSlot{Name: "fast", Provider: "missing", Model: "llama3"}
	_, ok := reg.GetProviderForSlot(slot)
	if ok {
		t.Fatal("expected provider not to be found for slot")
	}
}

func TestGetProvider_LiveSource(t *testing.T) {
	providers := map[string]models.Provider{}
	reg := NewProviderRegistry(func() map[string]models.Provider { return providers })

	// Initially empty — provider not found
	if _, ok := reg.GetProvider("openrouter"); ok {
		t.Fatal("expected provider not to be found before registration")
	}

	// Add provider dynamically
	providers["openrouter"] = &mockProvider{name: "openrouter"}

	// Now it should be found
	got, ok := reg.GetProvider("openrouter")
	if !ok {
		t.Fatal("expected provider to be found after dynamic registration")
	}
	if got.Name() != "openrouter" {
		t.Errorf("Name() = %q, want %q", got.Name(), "openrouter")
	}
}
