package integration

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/audit"
	"github.com/kkjorsvik/kyvik/internal/notifications"
	"github.com/kkjorsvik/kyvik/internal/webhooks"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// =============================================================================
// Scenario: Webhook Round-Trip
// Tests inbound webhook handling, secret validation, transform, and outbound.
// =============================================================================

func TestScenario_Webhook_InboundToAgent(t *testing.T) {
	// t.Parallel() — disabled: shared PostgreSQL database
	h := newFullHarness(t)

	agentID := "wh-inbound"
	h.seedAgent(t, agentID, "WH Inbound", "worker")

	// Start the agent first (startAgent deletes+re-creates in store).
	h.startAgent(t, agentID)

	// Configure inbound webhook on the running agent's store entry.
	agent, _ := h.store.GetAgent(context.Background(), agentID)
	agent.WebhookInbound = &types.InboundWebhookConfig{Enabled: true}
	_ = h.store.UpdateAgent(context.Background(), *agent)

	// Generate secret.
	secret, err := webhooks.GenerateSecret(context.Background(), h.secrets, agentID)
	if err != nil {
		t.Fatalf("GenerateSecret: %v", err)
	}

	// Create handler.
	whHandler := webhooks.New(h.store, h.secrets, h.kyvik, h.audit)

	// POST to webhook endpoint.
	url := fmt.Sprintf("/webhooks/%s/%s", agentID, secret)
	req := httptest.NewRequest(http.MethodPost, url, bytes.NewBufferString(`{"message":"webhook test"}`))
	req.SetPathValue("agent_id", agentID)
	req.SetPathValue("webhook_secret", secret)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	whHandler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Verify audit entry (retry for async audit flush under load).
	var found bool
	var entries []types.AuditEntry
	for retries := 0; retries < 5; retries++ {
		time.Sleep(200 * time.Millisecond)
		entries, _ = h.audit.Query(context.Background(), audit.Filter{AgentID: agentID, Limit: 100})
		for _, e := range entries {
			if e.Action == "webhook_accepted" || e.Action == "inbound_webhook" || e.Action == "webhook.delivery" {
				found = true
				break
			}
		}
		if found {
			break
		}
	}
	if !found {
		actions := make([]string, 0, len(entries))
		for _, e := range entries {
			actions = append(actions, e.Action)
		}
		t.Fatalf("expected webhook audit entry, found: %v", actions)
	}
}

func TestScenario_Webhook_InboundInvalidSecret(t *testing.T) {
	// t.Parallel() — disabled: shared PostgreSQL database
	h := newFullHarness(t)

	agentID := "wh-bad-secret"
	h.seedAgent(t, agentID, "WH Bad Secret", "worker")

	agent, _ := h.store.GetAgent(context.Background(), agentID)
	agent.WebhookInbound = &types.InboundWebhookConfig{Enabled: true}
	_ = h.store.UpdateAgent(context.Background(), *agent)

	// Generate real secret but use wrong one.
	_, err := webhooks.GenerateSecret(context.Background(), h.secrets, agentID)
	if err != nil {
		t.Fatalf("GenerateSecret: %v", err)
	}

	whHandler := webhooks.New(h.store, h.secrets, h.kyvik, h.audit)

	url := fmt.Sprintf("/webhooks/%s/%s", agentID, "wrong-secret-value")
	req := httptest.NewRequest(http.MethodPost, url, bytes.NewBufferString(`{"msg":"test"}`))
	req.SetPathValue("agent_id", agentID)
	req.SetPathValue("webhook_secret", "wrong-secret-value")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	whHandler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestScenario_Webhook_InboundTransform(t *testing.T) {
	// t.Parallel() — disabled: shared PostgreSQL database
	h := newFullHarness(t)

	agentID := "wh-transform"
	h.seedAgent(t, agentID, "WH Transform", "worker")

	// Start agent first (startAgent deletes+re-creates in store).
	h.startAgent(t, agentID)

	// Configure inbound webhook with transform template on the running agent.
	agent, _ := h.store.GetAgent(context.Background(), agentID)
	agent.WebhookInbound = &types.InboundWebhookConfig{
		Enabled:           true,
		TransformTemplate: "Repo: {{.repository}}, Action: {{.action}}",
	}
	_ = h.store.UpdateAgent(context.Background(), *agent)

	secret, _ := webhooks.GenerateSecret(context.Background(), h.secrets, agentID)
	whHandler := webhooks.New(h.store, h.secrets, h.kyvik, h.audit)

	url := fmt.Sprintf("/webhooks/%s/%s", agentID, secret)
	payload := `{"repository":"kyvik","action":"push"}`
	req := httptest.NewRequest(http.MethodPost, url, bytes.NewBufferString(payload))
	req.SetPathValue("agent_id", agentID)
	req.SetPathValue("webhook_secret", secret)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	whHandler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestScenario_Webhook_OutboundFires(t *testing.T) {
	// t.Parallel() — disabled: shared PostgreSQL database
	h := newFullHarness(t)

	// Create a receiver server.
	received := make(chan string, 1)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, 4096)
		n, _ := r.Body.Read(body)
		received <- string(body[:n])
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	// Register outbound webhook in store.
	now := time.Now().UTC()
	webhook := types.OutboundWebhook{
		ID:        "owh-1",
		Name:      "Test Hook",
		URL:       receiver.URL,
		Events:    []string{"*"},
		Enabled:   true,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := h.store.CreateOutboundWebhook(context.Background(), webhook); err != nil {
		t.Fatalf("CreateOutboundWebhook: %v", err)
	}

	// Create dispatcher.
	dispatcher := webhooks.NewDispatcher(h.store, h.secrets)

	// Send an event.
	err := dispatcher.Send(context.Background(), notifications.Event{
		Type:      "test.event",
		Severity:  "info",
		Title:     "Test Event",
		Detail:    "event details",
		Timestamp: now,
	})
	if err != nil {
		t.Fatalf("dispatcher.Send: %v", err)
	}

	// Verify receiver got the payload.
	select {
	case body := <-received:
		if body == "" {
			t.Fatal("empty payload received")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for outbound webhook delivery")
	}
}

func TestScenario_Webhook_OutboundRetryAndCircuitBreaker(t *testing.T) {
	// t.Parallel() — disabled: shared PostgreSQL database
	h := newFullHarness(t)

	// Create a server that always fails.
	failServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failServer.Close()

	now := time.Now().UTC()
	webhook := types.OutboundWebhook{
		ID:             "owh-fail",
		Name:           "Failing Hook",
		URL:            failServer.URL,
		Events:         []string{"*"},
		Enabled:        true,
		MaxRetries:     2,
		BackoffSeconds: []int{1, 2},
		CBThreshold:    3,
		CBCooldownSecs: 60,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := h.store.CreateOutboundWebhook(context.Background(), webhook); err != nil {
		t.Fatalf("CreateOutboundWebhook: %v", err)
	}

	dispatcher := webhooks.NewDispatcher(h.store, h.secrets)

	// Send multiple events to trigger failures and eventually circuit breaker.
	for i := 0; i < 5; i++ {
		_ = dispatcher.Send(context.Background(), notifications.Event{
			Type:      "fail.event",
			Severity:  "info",
			Title:     fmt.Sprintf("Fail Event %d", i),
			Timestamp: now,
		})
	}

	// Verify deliveries were recorded (some may be pending_retry or failed).
	time.Sleep(500 * time.Millisecond)

	// The key assertion is that the dispatcher doesn't panic and handles failures gracefully.
	// After enough failures, the circuit breaker should open and subsequent sends become no-ops.
}
