package handlers

import (
	"context"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/kkjorsvik/kyvik/internal/secrets"
)

// mockSecretStore is a minimal in-memory SecretStore for tests.
type mockSecretStore struct {
	data map[string]map[string]string // scope -> key -> value
}

func newMockSecretStore() *mockSecretStore {
	return &mockSecretStore{data: make(map[string]map[string]string)}
}

func (m *mockSecretStore) Set(_ context.Context, scope, key, plaintext, _ string) error {
	if m.data[scope] == nil {
		m.data[scope] = make(map[string]string)
	}
	m.data[scope][key] = plaintext
	return nil
}

func (m *mockSecretStore) Get(_ context.Context, scope, key string) (string, error) {
	if s, ok := m.data[scope]; ok {
		if v, ok := s[key]; ok {
			return v, nil
		}
	}
	return "", nil
}

func (m *mockSecretStore) Resolve(_ context.Context, _, _, _ string) (string, error) {
	return "", nil
}

func (m *mockSecretStore) Delete(_ context.Context, scope, key string) error {
	if s, ok := m.data[scope]; ok {
		delete(s, key)
	}
	return nil
}

func (m *mockSecretStore) List(_ context.Context, _ string) ([]secrets.SecretMeta, error) {
	return nil, nil
}

func (m *mockSecretStore) Exists(_ context.Context, scope, key string) (bool, error) {
	if s, ok := m.data[scope]; ok {
		_, ok := s[key]
		return ok, nil
	}
	return false, nil
}

// newSecretsHandlers builds a minimal Handlers instance wired for secrets tests.
// It includes a trivial template for secrets-table so renderFragment doesn't panic.
func newSecretsHandlers(t *testing.T, store secrets.SecretStore) *Handlers {
	t.Helper()

	tmpl := template.Must(template.New("secrets-table").Parse(`{{range .Secrets}}{{.Key}}{{end}}`))

	h := &Handlers{
		sessionKey: generateKey(),
		secrets:    store,
		templates:  tmpl,
	}
	return h
}

func TestSecretsCreateValidInput(t *testing.T) {
	store := newMockSecretStore()
	h := newSecretsHandlers(t, store)

	form := url.Values{}
	form.Set("scope", "global")
	form.Set("key", "MY_SECRET")
	form.Set("value", "s3cr3t")
	form.Set("description", "a test secret")

	req := httptest.NewRequest(http.MethodPost, "/secrets", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	h.SecretsCreate(rec, req)

	if rec.Code == http.StatusBadRequest {
		t.Errorf("expected non-400, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Code == http.StatusServiceUnavailable {
		t.Fatalf("secrets vault not configured")
	}

	// Verify the value was stored.
	got, _ := store.Get(req.Context(), "global", "MY_SECRET")
	if got != "s3cr3t" {
		t.Errorf("stored value = %q, want %q", got, "s3cr3t")
	}
}

func TestSecretsCreateMissingFields(t *testing.T) {
	store := newMockSecretStore()
	h := newSecretsHandlers(t, store)

	// Empty key should trigger 400.
	form := url.Values{}
	form.Set("scope", "global")
	form.Set("key", "")
	form.Set("value", "s3cr3t")

	req := httptest.NewRequest(http.MethodPost, "/secrets", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	h.SecretsCreate(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty key, got %d", rec.Code)
	}
}

func TestSecretsCreateInvalidScope(t *testing.T) {
	store := newMockSecretStore()
	h := newSecretsHandlers(t, store)
	// h.kyvik is nil, so validSecretScope returns false for non-"global" scopes.

	form := url.Values{}
	form.Set("scope", "unknown-agent-id")
	form.Set("key", "MY_SECRET")
	form.Set("value", "s3cr3t")

	req := httptest.NewRequest(http.MethodPost, "/secrets", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	h.SecretsCreate(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid scope, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid scope") {
		t.Errorf("expected 'invalid scope' in body, got %q", rec.Body.String())
	}
}

func TestSecretsDeleteMissingKey(t *testing.T) {
	store := newMockSecretStore()
	h := newSecretsHandlers(t, store)

	// Empty key (and scope) should trigger 400.
	form := url.Values{}
	form.Set("scope", "")
	form.Set("key", "")

	req := httptest.NewRequest(http.MethodPost, "/secrets/delete", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	h.SecretsDelete(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing key/scope, got %d", rec.Code)
	}
}

func TestSecretsCopyContentType(t *testing.T) {
	store := newMockSecretStore()
	// Pre-populate a secret.
	_ = store.Set(context.Background(), "global", "API_KEY", "topsecret", "")

	h := newSecretsHandlers(t, store)

	req := httptest.NewRequest(http.MethodGet, "/secrets/copy?scope=global&key=API_KEY", nil)
	rec := httptest.NewRecorder()

	h.SecretsCopy(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}

	body := rec.Body.String()
	if body != "topsecret" {
		t.Errorf("body = %q, want %q", body, "topsecret")
	}
}

// Ensure the mock satisfies the interface at compile time.
var _ secrets.SecretStore = (*mockSecretStore)(nil)
