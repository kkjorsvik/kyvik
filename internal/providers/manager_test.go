package providers_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"testing"

	"github.com/kkjorsvik/kyvik/internal/models"
	"github.com/kkjorsvik/kyvik/internal/providers"
	"github.com/kkjorsvik/kyvik/internal/store/postgres"
	"github.com/kkjorsvik/kyvik/internal/testutil"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

func newTestStore(t *testing.T) *postgres.PostgresStore {
	t.Helper()
	tdb := testutil.RequirePostgres(t)
	return tdb.Store
}

func testKey() []byte {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

// fakeCore implements providers.CoreRegistrar for testing.
type fakeCore struct {
	registered map[string]models.Provider
}

func newFakeCore() *fakeCore {
	return &fakeCore{registered: make(map[string]models.Provider)}
}

func (c *fakeCore) RegisterModelAs(id string, p models.Provider) {
	c.registered[id] = p
}

func (c *fakeCore) UnregisterModel(id string) {
	delete(c.registered, id)
}

func (c *fakeCore) Models() map[string]models.Provider {
	out := make(map[string]models.Provider, len(c.registered))
	for k, v := range c.registered {
		out[k] = v
	}
	return out
}

func TestAddAndGetProvider(t *testing.T) {
	s := newTestStore(t)
	enc := providers.NewEncryptor(testKey())
	core := newFakeCore()
	mgr := providers.NewManager(s, enc, core)
	ctx := context.Background()

	p := types.ProviderRecord{
		ProviderType:  "openai",
		DisplayName:   "Test OpenAI",
		BaseURL:       "https://api.openai.com/v1",
		DefaultModel:  "gpt-4o",
		AllowedModels: []string{"gpt-4o"},
		IsEnabled:     true,
	}

	err := mgr.AddProvider(ctx, p, "sk-test-key-123")
	if err != nil {
		t.Fatalf("AddProvider: %v", err)
	}

	// Should be in the list.
	list, err := mgr.ListProviders(ctx)
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d providers, want 1", len(list))
	}
	if list[0].DisplayName != "Test OpenAI" {
		t.Errorf("DisplayName = %q, want %q", list[0].DisplayName, "Test OpenAI")
	}
	// API key should be encrypted (not plaintext).
	if list[0].APIKeyEnc == "sk-test-key-123" {
		t.Error("API key stored as plaintext")
	}
	if list[0].APIKeyEnc == "" {
		t.Error("API key not stored")
	}

	// Core should have the adapter registered.
	if len(core.registered) == 0 {
		t.Error("no adapters registered with core")
	}
}

func TestEncryptionRoundTrip(t *testing.T) {
	key := testKey()
	enc := providers.NewEncryptor(key)

	plain := "sk-secret-key-12345"
	ct, err := enc.Encrypt(plain)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if ct == plain {
		t.Error("ciphertext equals plaintext")
	}

	got, err := enc.Decrypt(ct)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if got != plain {
		t.Errorf("Decrypt = %q, want %q", got, plain)
	}
}

func TestEncryptionWithoutKey(t *testing.T) {
	enc := providers.NewEncryptor(nil)
	_, err := enc.Encrypt("test")
	if err == nil {
		t.Error("expected error from Encrypt with nil key")
	}
	_, err = enc.Decrypt(base64.StdEncoding.EncodeToString([]byte("test")))
	if err == nil {
		t.Error("expected error from Decrypt with nil key")
	}
}

func TestRemoveProvider(t *testing.T) {
	s := newTestStore(t)
	enc := providers.NewEncryptor(testKey())
	core := newFakeCore()
	mgr := providers.NewManager(s, enc, core)
	ctx := context.Background()

	p := types.ProviderRecord{
		ProviderType:  "openai",
		DisplayName:   "To Delete",
		AllowedModels: []string{},
		IsEnabled:     true,
	}
	if err := mgr.AddProvider(ctx, p, "sk-key"); err != nil {
		t.Fatalf("AddProvider: %v", err)
	}

	list, _ := mgr.ListProviders(ctx)
	if len(list) != 1 {
		t.Fatalf("got %d providers, want 1", len(list))
	}

	if err := mgr.RemoveProvider(ctx, list[0].ID); err != nil {
		t.Fatalf("RemoveProvider: %v", err)
	}

	list, _ = mgr.ListProviders(ctx)
	if len(list) != 0 {
		t.Errorf("got %d providers after delete, want 0", len(list))
	}
}

func TestToggleProvider(t *testing.T) {
	s := newTestStore(t)
	enc := providers.NewEncryptor(testKey())
	core := newFakeCore()
	mgr := providers.NewManager(s, enc, core)
	ctx := context.Background()

	p := types.ProviderRecord{
		ProviderType:  "openai",
		DisplayName:   "Toggle Test",
		AllowedModels: []string{},
		IsEnabled:     true,
	}
	if err := mgr.AddProvider(ctx, p, "sk-key"); err != nil {
		t.Fatalf("AddProvider: %v", err)
	}

	list, _ := mgr.ListProviders(ctx)
	id := list[0].ID

	// Disable.
	if err := mgr.ToggleProvider(ctx, id, false); err != nil {
		t.Fatalf("ToggleProvider(false): %v", err)
	}
	got, _ := mgr.GetProvider(ctx, id)
	if got.IsEnabled {
		t.Error("provider still enabled after toggle off")
	}

	// Re-enable.
	if err := mgr.ToggleProvider(ctx, id, true); err != nil {
		t.Fatalf("ToggleProvider(true): %v", err)
	}
	got, _ = mgr.GetProvider(ctx, id)
	if !got.IsEnabled {
		t.Error("provider not enabled after toggle on")
	}
}

func TestBuildAdapterUnsupportedType(t *testing.T) {
	s := newTestStore(t)
	enc := providers.NewEncryptor(testKey())
	mgr := providers.NewManager(s, enc, nil)
	ctx := context.Background()

	p := types.ProviderRecord{
		ProviderType:  "grok",
		DisplayName:   "Grok (banned)",
		AllowedModels: []string{},
		IsEnabled:     true,
	}
	// Should fail because "grok" is not a supported provider type.
	// It will be stored but the adapter won't activate (logged as warning).
	err := mgr.AddProvider(ctx, p, "key")
	if err != nil {
		t.Fatalf("AddProvider should succeed (store the record): %v", err)
	}
	// No adapter should be active.
	active := mgr.ActiveAdapters()
	if len(active) != 0 {
		t.Errorf("got %d active adapters for unsupported type, want 0", len(active))
	}
}
