package router

import "github.com/kkjorsvik/kyvik/internal/models"

// ProviderSource returns the current set of registered providers.
// Using a callback instead of a static map ensures the registry always
// reflects dynamically added/removed providers.
type ProviderSource func() map[string]models.Provider

// ProviderRegistry provides slot-aware provider lookup, delegating to a
// live ProviderSource so that providers registered after startup are visible.
type ProviderRegistry struct {
	source ProviderSource
}

// NewProviderRegistry creates a registry backed by a live provider source.
func NewProviderRegistry(source ProviderSource) *ProviderRegistry {
	return &ProviderRegistry{source: source}
}

// GetProvider returns the provider registered under the given name.
func (r *ProviderRegistry) GetProvider(name string) (models.Provider, bool) {
	p, ok := r.source()[name]
	return p, ok
}

// GetProviderForSlot returns the provider for the given model slot.
func (r *ProviderRegistry) GetProviderForSlot(slot ModelSlot) (models.Provider, bool) {
	return r.GetProvider(slot.Provider)
}
