package keymanager_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/kkjorsvik/kyvik/internal/audit"
	"github.com/kkjorsvik/kyvik/internal/keymanager"
	"github.com/kkjorsvik/kyvik/internal/models/openrouter"
	"github.com/kkjorsvik/kyvik/internal/secrets"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// --- Mock Secret Store ---

type mockVault struct {
	mu      sync.Mutex
	data    map[string]string // "scope:key" -> value
	failSet bool
}

func newMockVault() *mockVault {
	return &mockVault{data: make(map[string]string)}
}

func (m *mockVault) scopeKey(scope, key string) string { return scope + ":" + key }

func (m *mockVault) Set(_ context.Context, scope, key, plaintext, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failSet {
		return fmt.Errorf("vault write failed")
	}
	m.data[m.scopeKey(scope, key)] = plaintext
	return nil
}

func (m *mockVault) Get(_ context.Context, scope, key string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[m.scopeKey(scope, key)]
	if !ok {
		return "", fmt.Errorf("not found")
	}
	return v, nil
}

func (m *mockVault) Resolve(_ context.Context, _, _, _ string) (string, error) {
	return "", fmt.Errorf("not implemented")
}

func (m *mockVault) Delete(_ context.Context, scope, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, m.scopeKey(scope, key))
	return nil
}

func (m *mockVault) List(_ context.Context, _ string) ([]secrets.SecretMeta, error) {
	return nil, nil
}

func (m *mockVault) Exists(_ context.Context, scope, key string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.data[m.scopeKey(scope, key)]
	return ok, nil
}

// Compile-time check.
var _ secrets.SecretStore = (*mockVault)(nil)

// --- Mock Audit Logger ---

type mockAudit struct {
	mu      sync.Mutex
	entries []types.AuditEntry
}

func (m *mockAudit) Log(_ context.Context, entry types.AuditEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, entry)
	return nil
}

func (m *mockAudit) Query(_ context.Context, _ audit.Filter) ([]types.AuditEntry, error) {
	return nil, nil
}

func (m *mockAudit) Stream(_ context.Context, _ string) (<-chan types.AuditEntry, error) {
	return nil, nil
}

func (m *mockAudit) Subscribe(_ context.Context, _ audit.SubscriptionFilter) (<-chan types.AuditEntry, error) {
	ch := make(chan types.AuditEntry)
	close(ch)
	return ch, nil
}

func (m *mockAudit) Close() error { return nil }

func (m *mockAudit) hasAction(action string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.entries {
		if e.Action == action {
			return true
		}
	}
	return false
}

// Compile-time check.
var _ audit.Logger = (*mockAudit)(nil)

// --- Helpers ---

func setupMgmtServer(t *testing.T, handler http.HandlerFunc) (*openrouter.ManagementClient, *httptest.Server) {
	t.Helper()
	srv := newHTTPServer(t, handler)
	mc := openrouter.NewManagementClient("prov-key", openrouter.WithManagementBaseURL(srv.URL))
	return mc, srv
}

func newHTTPServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("listen not permitted: %v", err)
	}
	server := &httptest.Server{
		Listener: listener,
		Config:   &http.Server{Handler: handler},
	}
	server.Start()
	return server
}

// --- ProvisionKey Tests ---

func TestProvisionKey_Success(t *testing.T) {
	mc, srv := setupMgmtServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"key":  "sk-agent-key",
				"hash": "h1",
				"name": "kyvik-agent-a1",
			},
		})
	})
	defer srv.Close()

	vault := newMockVault()
	al := &mockAudit{}
	km := keymanager.New(mc, vault, al)

	err := km.ProvisionKey(context.Background(), "a1", "Agent One", 10.0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify key is in vault.
	key, err := vault.Get(context.Background(), "agent:a1", "openrouter:api_key")
	if err != nil {
		t.Fatalf("key not found in vault: %v", err)
	}
	if key != "sk-agent-key" {
		t.Errorf("key = %q, want %q", key, "sk-agent-key")
	}

	// Verify hash is in vault.
	hash, err := vault.Get(context.Background(), "agent:a1", "openrouter:key_hash")
	if err != nil {
		t.Fatalf("hash not found in vault: %v", err)
	}
	if hash != "h1" {
		t.Errorf("hash = %q, want %q", hash, "h1")
	}

	// Verify audit.
	if !al.hasAction("key.provisioned") {
		t.Error("expected key.provisioned audit entry")
	}
}

func TestProvisionKey_AlreadyExists(t *testing.T) {
	apiCalled := false
	mc, srv := setupMgmtServer(t, func(w http.ResponseWriter, r *http.Request) {
		apiCalled = true
		w.WriteHeader(http.StatusOK)
	})
	defer srv.Close()

	vault := newMockVault()
	vault.Set(context.Background(), "agent:a1", "openrouter:api_key", "existing-key", "")

	km := keymanager.New(mc, vault, &mockAudit{})

	err := km.ProvisionKey(context.Background(), "a1", "Agent One", 10.0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if apiCalled {
		t.Error("management API should not have been called when key already exists")
	}
}

func TestProvisionKey_APIFailure(t *testing.T) {
	mc, srv := setupMgmtServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"server error"}`))
	})
	defer srv.Close()

	al := &mockAudit{}
	km := keymanager.New(mc, newMockVault(), al)

	err := km.ProvisionKey(context.Background(), "a1", "Agent One", 10.0)
	if err == nil {
		t.Fatal("expected error")
	}
	if !al.hasAction("key.provision_failed") {
		t.Error("expected key.provision_failed audit entry")
	}
}

func TestProvisionKey_VaultFailure_CleansUpRemote(t *testing.T) {
	var deleteCalled bool
	mc, srv := setupMgmtServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleteCalled = true
			w.WriteHeader(http.StatusOK)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"key":  "sk-agent-key",
				"hash": "h1",
				"name": "kyvik-agent-a1",
			},
		})
	})
	defer srv.Close()

	vault := newMockVault()
	vault.failSet = true

	al := &mockAudit{}
	km := keymanager.New(mc, vault, al)

	err := km.ProvisionKey(context.Background(), "a1", "Agent One", 10.0)
	if err == nil {
		t.Fatal("expected error")
	}
	if !deleteCalled {
		t.Error("expected remote key cleanup via DeleteKey")
	}
	if !al.hasAction("key.provision_failed") {
		t.Error("expected key.provision_failed audit entry")
	}
}

// --- RevokeKey Tests ---

func TestRevokeKey_Success(t *testing.T) {
	var deletePath string
	mc, srv := setupMgmtServer(t, func(w http.ResponseWriter, r *http.Request) {
		deletePath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	})
	defer srv.Close()

	vault := newMockVault()
	vault.Set(context.Background(), "agent:a1", "openrouter:api_key", "sk-key", "")
	vault.Set(context.Background(), "agent:a1", "openrouter:key_hash", "h1", "")

	al := &mockAudit{}
	km := keymanager.New(mc, vault, al)

	err := km.RevokeKey(context.Background(), "a1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if deletePath != "/api/v1/keys/h1" {
		t.Errorf("delete path = %q, want /api/v1/keys/h1", deletePath)
	}

	// Verify vault entries are removed.
	_, err = vault.Get(context.Background(), "agent:a1", "openrouter:api_key")
	if err == nil {
		t.Error("api_key should have been deleted from vault")
	}
	_, err = vault.Get(context.Background(), "agent:a1", "openrouter:key_hash")
	if err == nil {
		t.Error("key_hash should have been deleted from vault")
	}

	if !al.hasAction("key.revoked") {
		t.Error("expected key.revoked audit entry")
	}
}

func TestRevokeKey_NoKey_NoOp(t *testing.T) {
	apiCalled := false
	mc, srv := setupMgmtServer(t, func(w http.ResponseWriter, r *http.Request) {
		apiCalled = true
	})
	defer srv.Close()

	km := keymanager.New(mc, newMockVault(), &mockAudit{})

	err := km.RevokeKey(context.Background(), "no-such-agent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if apiCalled {
		t.Error("management API should not have been called for non-existent key")
	}
}

func TestRevokeKey_RemoteFailure_StillCleansVault(t *testing.T) {
	mc, srv := setupMgmtServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer srv.Close()

	vault := newMockVault()
	vault.Set(context.Background(), "agent:a1", "openrouter:api_key", "sk-key", "")
	vault.Set(context.Background(), "agent:a1", "openrouter:key_hash", "h1", "")

	al := &mockAudit{}
	km := keymanager.New(mc, vault, al)

	err := km.RevokeKey(context.Background(), "a1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Vault should still be cleaned.
	_, err = vault.Get(context.Background(), "agent:a1", "openrouter:api_key")
	if err == nil {
		t.Error("api_key should have been deleted from vault despite remote failure")
	}

	if !al.hasAction("key.revoke_remote_failed") {
		t.Error("expected key.revoke_remote_failed audit entry")
	}
}

// --- GetKeyForAgent Tests ---

func TestGetKeyForAgent_Hit(t *testing.T) {
	mc, srv := setupMgmtServer(t, nil)
	defer srv.Close()

	vault := newMockVault()
	vault.Set(context.Background(), "agent:a1", "openrouter:api_key", "sk-agent-key", "")

	km := keymanager.New(mc, vault, &mockAudit{})

	key, err := km.GetKeyForAgent(context.Background(), "a1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "sk-agent-key" {
		t.Errorf("key = %q, want %q", key, "sk-agent-key")
	}
}

func TestGetKeyForAgent_Miss(t *testing.T) {
	mc, srv := setupMgmtServer(t, nil)
	defer srv.Close()

	km := keymanager.New(mc, newMockVault(), &mockAudit{})

	_, err := km.GetKeyForAgent(context.Background(), "no-such-agent")
	if err == nil {
		t.Fatal("expected error for missing key")
	}
}
