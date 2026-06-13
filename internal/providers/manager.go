package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/kkjorsvik/kyvik/internal/config"
	"github.com/kkjorsvik/kyvik/internal/models"
	"github.com/kkjorsvik/kyvik/internal/models/anthropic"
	"github.com/kkjorsvik/kyvik/internal/models/gemini"
	"github.com/kkjorsvik/kyvik/internal/models/ollama"
	"github.com/kkjorsvik/kyvik/internal/models/openai"
	"github.com/kkjorsvik/kyvik/internal/models/openrouter"
	"github.com/kkjorsvik/kyvik/internal/store"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// Manager manages the lifecycle of LLM providers: CRUD operations,
// encryption of API keys, adapter construction, and hybrid sync
// between config-file and database providers.
type Manager struct {
	store     store.Store
	enc       *Encryptor
	core      CoreRegistrar
	mu        sync.RWMutex
	adapters  map[string]models.Provider // instanceID → live adapter
	records   map[string]types.ProviderRecord
}

// NewManager creates a new provider manager.
// core may be nil during testing (adapter registration is skipped).
func NewManager(s store.Store, enc *Encryptor, core CoreRegistrar) *Manager {
	return &Manager{
		store:    s,
		enc:      enc,
		core:     core,
		adapters: make(map[string]models.Provider),
		records:  make(map[string]types.ProviderRecord),
	}
}

// AddProvider encrypts the API key, persists the record, builds an adapter,
// and registers it with the core runtime.
func (m *Manager) AddProvider(ctx context.Context, p types.ProviderRecord, apiKeyPlain string) error {
	if apiKeyPlain != "" {
		enc, err := m.enc.Encrypt(apiKeyPlain)
		if err != nil {
			return fmt.Errorf("encrypt api key: %w", err)
		}
		p.APIKeyEnc = enc
	}

	now := time.Now().UTC()
	if p.ID == "" {
		p.ID = ulid.Make().String()
	}
	if p.Source == "" {
		p.Source = types.ProviderSourceDB
	}
	if p.AllowedModels == nil {
		p.AllowedModels = []string{}
	}
	if p.ConfigJSON == "" {
		p.ConfigJSON = "{}"
	}
	p.CreatedAt = now
	p.UpdatedAt = now

	if err := m.store.CreateProvider(ctx, p); err != nil {
		return fmt.Errorf("store provider: %w", err)
	}

	if p.IsEnabled {
		if err := m.activateProvider(p, apiKeyPlain); err != nil {
			slog.Warn("provider saved but adapter failed to start", "id", p.ID, "err", err)
		}
	}

	return nil
}

// UpdateProvider updates an existing provider. If apiKeyPlain is non-empty,
// the API key is re-encrypted; otherwise the existing encrypted key is kept.
func (m *Manager) UpdateProvider(ctx context.Context, p types.ProviderRecord, apiKeyPlain string) error {
	if apiKeyPlain != "" {
		enc, err := m.enc.Encrypt(apiKeyPlain)
		if err != nil {
			return fmt.Errorf("encrypt api key: %w", err)
		}
		p.APIKeyEnc = enc
	}
	p.UpdatedAt = time.Now().UTC()

	if err := m.store.UpdateProvider(ctx, p); err != nil {
		return err
	}

	// Deactivate the old adapter and re-activate if enabled.
	m.deactivateProvider(p.ID)
	if p.IsEnabled {
		key := apiKeyPlain
		if key == "" {
			key, _ = m.decryptKey(p)
		}
		if err := m.activateProvider(p, key); err != nil {
			slog.Warn("provider updated but adapter failed to start", "id", p.ID, "err", err)
		}
	}
	return nil
}

// RemoveProvider deletes a provider and unregisters its adapter.
func (m *Manager) RemoveProvider(ctx context.Context, id string) error {
	m.deactivateProvider(id)
	return m.store.DeleteProvider(ctx, id)
}

// GetProvider returns a single provider record by ID.
func (m *Manager) GetProvider(ctx context.Context, id string) (*types.ProviderRecord, error) {
	return m.store.GetProvider(ctx, id)
}

// ListProviders returns all provider records (both DB and config-sourced).
func (m *Manager) ListProviders(ctx context.Context) ([]types.ProviderRecord, error) {
	return m.store.ListProviders(ctx)
}

// TestConnection builds an ephemeral adapter and calls ListModels to
// validate the API key and connectivity.
func (m *Manager) TestConnection(ctx context.Context, providerType, apiKey, baseURL string) ([]models.ModelInfo, error) {
	adapter, err := buildAdapter(providerType, apiKey, baseURL)
	if err != nil {
		return nil, err
	}
	modelList, err := adapter.ListModels(ctx)
	if err != nil {
		return nil, fmt.Errorf("connection test failed: %w", err)
	}
	return modelList, nil
}

// FetchModels returns available models for a registered provider.
func (m *Manager) FetchModels(ctx context.Context, id string) ([]models.ModelInfo, error) {
	m.mu.RLock()
	adapter, ok := m.adapters[id]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("provider %q not active", id)
	}
	return adapter.ListModels(ctx)
}

// UpdateAllowedModels updates the allowed models list for a provider.
func (m *Manager) UpdateAllowedModels(ctx context.Context, id string, models []string) error {
	p, err := m.store.GetProvider(ctx, id)
	if err != nil {
		return err
	}
	p.AllowedModels = models
	p.UpdatedAt = time.Now().UTC()
	return m.store.UpdateProvider(ctx, *p)
}

// ToggleProvider enables or disables a provider.
func (m *Manager) ToggleProvider(ctx context.Context, id string, enabled bool) error {
	p, err := m.store.GetProvider(ctx, id)
	if err != nil {
		return err
	}
	p.IsEnabled = enabled
	p.UpdatedAt = time.Now().UTC()
	if err := m.store.UpdateProvider(ctx, *p); err != nil {
		return err
	}
	if enabled {
		key, _ := m.decryptKey(*p)
		if err := m.activateProvider(*p, key); err != nil {
			slog.Warn("toggle-on failed to activate adapter", "id", id, "err", err)
		}
	} else {
		m.deactivateProvider(id)
	}
	return nil
}

// SyncProviders merges config-file providers with DB providers, builds
// adapters, and registers them with the core runtime. Config-file providers
// are upserted as read-only ("config" source) records.
func (m *Manager) SyncProviders(ctx context.Context, cfgModels config.ModelsConfig) ([]string, error) {
	var names []string

	// Sync config-file providers into the database as "config" source.
	configProviders := m.configToRecords(cfgModels)
	for _, cp := range configProviders {
		existing, err := m.store.GetProvider(ctx, cp.ID)
		if err != nil {
			// Not found → create.
			if err := m.store.CreateProvider(ctx, cp); err != nil {
				slog.Warn("failed to persist config provider", "id", cp.ID, "err", err)
				continue
			}
		} else {
			// Update display name and config but keep enabled state.
			cp.IsEnabled = existing.IsEnabled
			cp.UpdatedAt = time.Now().UTC()
			_ = m.store.UpdateProvider(ctx, cp)
		}
	}

	// Load all providers (config + DB) and activate enabled ones.
	all, err := m.store.ListProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("list providers: %w", err)
	}

	for _, p := range all {
		if !p.IsEnabled {
			continue
		}
		key, decErr := m.decryptKey(p)
		if decErr != nil {
			slog.Warn("failed to decrypt provider API key — provider will activate with no key",
				"id", p.ID, "type", p.ProviderType, "err", decErr)
		}
		if err := m.activateProvider(p, key); err != nil {
			slog.Warn("failed to activate provider", "id", p.ID, "type", p.ProviderType, "err", err)
			continue
		}
		names = append(names, p.DisplayName)
	}

	return names, nil
}

// RegisteredProviderNames returns display names of all active providers.
func (m *Manager) RegisteredProviderNames() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	names := make([]string, 0, len(m.records))
	for _, r := range m.records {
		names = append(names, r.DisplayName)
	}
	return names
}

// ActiveAdapters returns a copy of the active adapter map.
func (m *Manager) ActiveAdapters() map[string]models.Provider {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]models.Provider, len(m.adapters))
	for k, v := range m.adapters {
		out[k] = v
	}
	return out
}

// ── internal helpers ───────────────────────────────────────────────────

func (m *Manager) activateProvider(p types.ProviderRecord, apiKeyPlain string) error {
	adapter, err := buildAdapter(p.ProviderType, apiKeyPlain, p.BaseURL)
	if err != nil {
		return err
	}

	m.mu.Lock()
	m.adapters[p.ID] = adapter
	m.records[p.ID] = p
	m.mu.Unlock()

	if m.core != nil {
		// Register under instance ID.
		m.core.RegisterModelAs(p.ID, adapter)

		// Also register under bare provider type for backward compat,
		// but only if no other instance of the same type is already registered
		// under the bare name.
		existing := m.core.Models()
		if _, ok := existing[p.ProviderType]; !ok {
			m.core.RegisterModelAs(p.ProviderType, adapter)
		}
	}
	return nil
}

func (m *Manager) deactivateProvider(id string) {
	m.mu.Lock()
	rec, hadRecord := m.records[id]
	delete(m.adapters, id)
	delete(m.records, id)
	m.mu.Unlock()

	if m.core != nil {
		m.core.UnregisterModel(id)
		// If we were the one registered under the bare type name, remove it too.
		if hadRecord {
			if existing := m.core.Models(); existing != nil {
				if a, ok := existing[rec.ProviderType]; ok {
					if myAdapter, ok2 := m.adapters[id]; ok2 && a == myAdapter {
						m.core.UnregisterModel(rec.ProviderType)
					}
				}
			}
		}
	}
}

func (m *Manager) decryptKey(p types.ProviderRecord) (string, error) {
	if p.APIKeyEnc == "" {
		// Config-file providers may have their key in ConfigJSON.
		var cfg map[string]string
		if err := json.Unmarshal([]byte(p.ConfigJSON), &cfg); err == nil {
			if k := cfg["api_key"]; k != "" {
				return k, nil
			}
		}
		return "", nil
	}
	return m.enc.Decrypt(p.APIKeyEnc)
}

// configToRecords converts config-file model settings into ProviderRecord
// entries with source="config". API keys are stored in ConfigJSON as plaintext
// (not encrypted) since config-file providers are read-only.
func (m *Manager) configToRecords(cfg config.ModelsConfig) []types.ProviderRecord {
	now := time.Now().UTC()
	var records []types.ProviderRecord

	if cfg.OpenRouter.APIKey != "" {
		cfgJSON, _ := json.Marshal(map[string]string{
			"api_key":          cfg.OpenRouter.APIKey,
			"provisioning_key": cfg.OpenRouter.ProvisioningKey,
		})
		records = append(records, types.ProviderRecord{
			ID:            "openrouter-config",
			ProviderType:  "openrouter",
			DisplayName:   "OpenRouter (config)",
			DefaultModel:  cfg.OpenRouter.DefaultModel,
			AllowedModels: []string{},
			IsEnabled:     true,
			Source:        types.ProviderSourceConfig,
			ConfigJSON:    string(cfgJSON),
			CreatedAt:     now,
			UpdatedAt:     now,
		})
	}
	if cfg.OpenAI.APIKey != "" {
		cfgJSON, _ := json.Marshal(map[string]string{
			"api_key":  cfg.OpenAI.APIKey,
			"base_url": cfg.OpenAI.BaseURL,
		})
		records = append(records, types.ProviderRecord{
			ID:            "openai-config",
			ProviderType:  "openai",
			DisplayName:   "OpenAI (config)",
			DefaultModel:  cfg.OpenAI.DefaultModel,
			AllowedModels: []string{},
			IsEnabled:     true,
			Source:        types.ProviderSourceConfig,
			ConfigJSON:    string(cfgJSON),
			CreatedAt:     now,
			UpdatedAt:     now,
		})
	}
	if cfg.Anthropic.APIKey != "" {
		cfgJSON, _ := json.Marshal(map[string]string{
			"api_key":  cfg.Anthropic.APIKey,
			"base_url": cfg.Anthropic.BaseURL,
		})
		records = append(records, types.ProviderRecord{
			ID:            "anthropic-config",
			ProviderType:  "anthropic",
			DisplayName:   "Anthropic (config)",
			DefaultModel:  cfg.Anthropic.DefaultModel,
			AllowedModels: []string{},
			IsEnabled:     true,
			Source:        types.ProviderSourceConfig,
			ConfigJSON:    string(cfgJSON),
			CreatedAt:     now,
			UpdatedAt:     now,
		})
	}
	if cfg.Ollama.Enabled {
		cfgJSON, _ := json.Marshal(map[string]string{
			"base_url":        cfg.Ollama.BaseURL,
			"embedding_model": cfg.Ollama.EmbeddingModel,
		})
		records = append(records, types.ProviderRecord{
			ID:            "ollama-config",
			ProviderType:  "ollama",
			DisplayName:   "Ollama (config)",
			DefaultModel:  cfg.Ollama.DefaultModel,
			AllowedModels: []string{},
			IsEnabled:     true,
			Source:        types.ProviderSourceConfig,
			ConfigJSON:    string(cfgJSON),
			CreatedAt:     now,
			UpdatedAt:     now,
		})
	}
	if cfg.Gemini.APIKey != "" {
		cfgJSON, _ := json.Marshal(map[string]string{
			"api_key":  cfg.Gemini.APIKey,
			"base_url": cfg.Gemini.BaseURL,
		})
		records = append(records, types.ProviderRecord{
			ID:            "gemini-config",
			ProviderType:  "gemini",
			DisplayName:   "Gemini (config)",
			DefaultModel:  cfg.Gemini.DefaultModel,
			AllowedModels: []string{},
			IsEnabled:     true,
			Source:        types.ProviderSourceConfig,
			ConfigJSON:    string(cfgJSON),
			CreatedAt:     now,
			UpdatedAt:     now,
		})
	}

	return records
}

// buildAdapter constructs a models.Provider for the given type.
func buildAdapter(providerType, apiKey, baseURL string) (models.Provider, error) {
	switch providerType {
	case "openrouter":
		var opts []openrouter.Option
		if baseURL != "" {
			opts = append(opts, openrouter.WithBaseURL(baseURL))
		}
		return openrouter.New(apiKey, opts...), nil

	case "openai":
		var opts []openai.Option
		if baseURL != "" {
			opts = append(opts, openai.WithBaseURL(baseURL))
		}
		return openai.New(apiKey, opts...), nil

	case "anthropic":
		var opts []anthropic.Option
		if baseURL != "" {
			opts = append(opts, anthropic.WithBaseURL(baseURL))
		}
		return anthropic.New(apiKey, opts...), nil

	case "ollama":
		var opts []ollama.Option
		if baseURL != "" {
			opts = append(opts, ollama.WithBaseURL(baseURL))
		}
		return ollama.New(opts...), nil

	case "gemini":
		var opts []gemini.Option
		if baseURL != "" {
			opts = append(opts, gemini.WithBaseURL(baseURL))
		}
		return gemini.New(apiKey, opts...), nil

	default:
		return nil, fmt.Errorf("unsupported provider type: %q", providerType)
	}
}
