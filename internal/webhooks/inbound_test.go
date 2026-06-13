package webhooks_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kkjorsvik/kyvik/internal/audit"
	"github.com/kkjorsvik/kyvik/internal/secrets"
	"github.com/kkjorsvik/kyvik/internal/webhooks"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// --- fakes ---

type fakeStore struct{ agent *types.AgentConfig }

func (f *fakeStore) GetAgent(_ context.Context, id string) (*types.AgentConfig, error) {
	if f.agent == nil || f.agent.ID != id {
		return nil, types.ErrNotFound
	}
	return f.agent, nil
}

type fakeVault struct{ secrets map[string]string }

func (v *fakeVault) Get(_ context.Context, scope, key string) (string, error) {
	s, ok := v.secrets[scope+":"+key]
	if !ok {
		return "", types.ErrNotFound
	}
	return s, nil
}
func (v *fakeVault) Set(_ context.Context, scope, key, val, _ string) error {
	v.secrets[scope+":"+key] = val
	return nil
}
func (v *fakeVault) Delete(_ context.Context, scope, key string) error {
	delete(v.secrets, scope+":"+key)
	return nil
}
func (v *fakeVault) List(_ context.Context, _ string) ([]secrets.SecretMeta, error) { return nil, nil }
func (v *fakeVault) Exists(_ context.Context, scope, key string) (bool, error) {
	_, ok := v.secrets[scope+":"+key]
	return ok, nil
}
func (v *fakeVault) Resolve(_ context.Context, agentID, _, key string) (string, error) {
	return v.Get(context.Background(), "agent:"+agentID, key)
}

type fakeKyvik struct{ queued []types.Message }

func (k *fakeKyvik) SendMessage(_ context.Context, _ string, msg types.Message) error {
	k.queued = append(k.queued, msg)
	return nil
}

type fakeAudit struct{ entries []types.AuditEntry }

func (a *fakeAudit) Log(_ context.Context, e types.AuditEntry) error {
	a.entries = append(a.entries, e)
	return nil
}
func (a *fakeAudit) Query(_ context.Context, _ audit.Filter) ([]types.AuditEntry, error) {
	return nil, nil
}
func (a *fakeAudit) Stream(_ context.Context, _ string) (<-chan types.AuditEntry, error) {
	return nil, nil
}
func (a *fakeAudit) Subscribe(_ context.Context, _ audit.SubscriptionFilter) (<-chan types.AuditEntry, error) {
	return nil, nil
}
func (a *fakeAudit) Close() error { return nil }

// newTestEnv builds a handler with in-memory fakes.
func newTestEnv(agentID, secret string, wh *types.InboundWebhookConfig) (*fakeStore, *fakeVault, *fakeKyvik, *fakeAudit, http.Handler) {
	st := &fakeStore{agent: &types.AgentConfig{
		ID:             agentID,
		WebhookInbound: wh,
	}}
	vault := &fakeVault{secrets: map[string]string{
		"agent:" + agentID + ":" + webhooks.SecretVaultKey: secret,
	}}
	kyv := &fakeKyvik{}
	al := &fakeAudit{}
	h := webhooks.New(st, vault, kyv, al)
	return st, vault, kyv, al, h
}

func post(h http.Handler, agentID, secret, body, ct string) *httptest.ResponseRecorder {
	url := fmt.Sprintf("/webhooks/%s/%s", agentID, secret)
	req := httptest.NewRequest(http.MethodPost, url, bytes.NewBufferString(body))
	req.SetPathValue("agent_id", agentID)
	req.SetPathValue("webhook_secret", secret)
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestWebhook_ValidSecretQueuesMessage(t *testing.T) {
	_, _, kyv, _, h := newTestEnv("agent-1", "abc123", &types.InboundWebhookConfig{
		Enabled: true,
	})
	rr := post(h, "agent-1", "abc123", `{"event":"push"}`, "application/json")
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if len(kyv.queued) != 1 {
		t.Fatalf("expected 1 queued message, got %d", len(kyv.queued))
	}
	if kyv.queued[0].Channel != "webhook" {
		t.Errorf("expected channel=webhook, got %q", kyv.queued[0].Channel)
	}
}

func TestWebhook_InvalidSecretReturns401(t *testing.T) {
	_, _, kyv, _, h := newTestEnv("agent-2", "correct", &types.InboundWebhookConfig{
		Enabled: true,
	})
	rr := post(h, "agent-2", "wrong", `{}`, "application/json")
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rr.Code)
	}
	if len(kyv.queued) != 0 {
		t.Error("should not have queued message on bad secret")
	}
}

func TestWebhook_RateLimitingReturns429(t *testing.T) {
	wh := &types.InboundWebhookConfig{Enabled: true, RateLimit: 2}
	_, _, _, _, h := newTestEnv("agent-3", "s3cr3t", wh)
	for i := 0; i < 2; i++ {
		rr := post(h, "agent-3", "s3cr3t", `{}`, "application/json")
		if rr.Code != http.StatusOK {
			t.Fatalf("request %d: want 200, got %d", i, rr.Code)
		}
	}
	rr := post(h, "agent-3", "s3cr3t", `{}`, "application/json")
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("want 429, got %d", rr.Code)
	}
}

func TestWebhook_TransformTemplate(t *testing.T) {
	wh := &types.InboundWebhookConfig{
		Enabled:           true,
		TransformTemplate: "Repo: {{.repository}}",
	}
	_, _, kyv, _, h := newTestEnv("agent-4", "tok", wh)
	rr := post(h, "agent-4", "tok", `{"repository":"kyvik"}`, "application/json")
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if len(kyv.queued) == 0 {
		t.Fatal("no message queued")
	}
	if kyv.queued[0].Content != "Repo: kyvik" {
		t.Errorf("unexpected content: %q", kyv.queued[0].Content)
	}
}

func TestWebhook_IPAllowlistEnforced(t *testing.T) {
	wh := &types.InboundWebhookConfig{
		Enabled:        true,
		AllowedSources: []string{"10.0.0.1"},
	}
	_, _, kyv, _, h := newTestEnv("agent-5", "tok", wh)
	// httptest uses 192.0.2.1 as RemoteAddr; not in allowlist.
	rr := post(h, "agent-5", "tok", `{}`, "application/json")
	if rr.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rr.Code)
	}
	if len(kyv.queued) != 0 {
		t.Error("should not queue when IP not allowed")
	}
}

func TestWebhook_HMACSignatureVerification(t *testing.T) {
	wh := &types.InboundWebhookConfig{
		Enabled:         true,
		SignatureHeader: "X-Hub-Signature-256",
	}
	_, vault, kyv, _, h := newTestEnv("agent-6", "tok", wh)
	vault.secrets["agent:agent-6:"+webhooks.HMACVaultKey] = "hmac-secret"

	body := `{"event":"push"}`

	t.Run("bad signature returns 401", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/webhooks/agent-6/tok", bytes.NewBufferString(body))
		req.SetPathValue("agent_id", "agent-6")
		req.SetPathValue("webhook_secret", "tok")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Hub-Signature-256", "sha256=badhex")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("want 401 for bad HMAC, got %d", rr.Code)
		}
	})

	t.Run("correct signature is accepted", func(t *testing.T) {
		sig := webhooks.ComputeHMAC([]byte(body), "hmac-secret")
		req := httptest.NewRequest(http.MethodPost, "/webhooks/agent-6/tok", bytes.NewBufferString(body))
		req.SetPathValue("agent_id", "agent-6")
		req.SetPathValue("webhook_secret", "tok")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Hub-Signature-256", sig)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("want 200 for valid HMAC, got %d: %s", rr.Code, rr.Body.String())
		}
		if len(kyv.queued) == 0 {
			t.Error("expected message to be queued")
		}
	})
}

func TestWebhook_RateLimitWindowBlocks(t *testing.T) {
	wh := &types.InboundWebhookConfig{Enabled: true, RateLimit: 1}
	_, _, _, _, h := newTestEnv("agent-7", "tok", wh)
	post(h, "agent-7", "tok", `{}`, "application/json") // consume the slot
	rr := post(h, "agent-7", "tok", `{}`, "application/json")
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("want 429 on second request, got %d", rr.Code)
	}
}
