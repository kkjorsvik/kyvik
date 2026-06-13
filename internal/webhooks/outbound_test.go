package webhooks_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/kkjorsvik/kyvik/internal/notifications"
	"github.com/kkjorsvik/kyvik/internal/store/postgres"
	"github.com/kkjorsvik/kyvik/internal/testutil"
	"github.com/kkjorsvik/kyvik/internal/webhooks"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

func newTestStore(t *testing.T) *postgres.PostgresStore {
	t.Helper()
	tdb := testutil.RequirePostgres(t)
	return tdb.Store
}

func TestDispatcher_EventTriggersDelivery(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	var received atomic.Int32
	var receivedBody []byte
	var mu sync.Mutex

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		receivedBody, _ = io.ReadAll(r.Body)
		mu.Unlock()
		received.Add(1)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	whID := uuid.New().String()
	wh := types.OutboundWebhook{
		ID:             whID,
		Name:           "test-hook",
		URL:            srv.URL,
		Events:         []string{"*"},
		MaxRetries:     3,
		BackoffSeconds: types.DefaultWebhookBackoff,
		CBThreshold:    10,
		CBCooldownSecs: 3600,
		Enabled:        true,
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	if err := store.CreateOutboundWebhook(ctx, wh); err != nil {
		t.Fatalf("CreateOutboundWebhook: %v", err)
	}

	dispatcher := webhooks.NewDispatcher(store, nil)
	if err := dispatcher.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer dispatcher.Stop()

	event := notifications.Event{
		Type:      "circuit_breaker",
		Severity:  "critical",
		Agent:     "agent-1",
		Title:     "Circuit breaker tripped",
		Detail:    "Too many errors",
		Timestamp: time.Now().UTC(),
	}

	if err := dispatcher.Send(ctx, event); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Wait for async delivery.
	deadline := time.Now().Add(5 * time.Second)
	for received.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}

	if received.Load() != 1 {
		t.Fatalf("expected 1 delivery, got %d", received.Load())
	}

	// Verify payload.
	mu.Lock()
	body := receivedBody
	mu.Unlock()
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload["event"] != "circuit_breaker.tripped" {
		t.Errorf("event = %q, want %q", payload["event"], "circuit_breaker.tripped")
	}
	if payload["title"] != "Circuit breaker tripped" {
		t.Errorf("title = %q, want %q", payload["title"], "Circuit breaker tripped")
	}

	// Verify delivery record in store.
	deliveries, err := store.ListWebhookDeliveries(ctx, whID, 10)
	if err != nil {
		t.Fatalf("ListWebhookDeliveries: %v", err)
	}
	if len(deliveries) != 1 {
		t.Fatalf("expected 1 delivery record, got %d", len(deliveries))
	}
	if deliveries[0].Status != types.DeliveryStatusSuccess {
		t.Errorf("status = %q, want %q", deliveries[0].Status, types.DeliveryStatusSuccess)
	}
	if deliveries[0].HTTPCode != 200 {
		t.Errorf("http_code = %d, want 200", deliveries[0].HTTPCode)
	}
}

func TestDispatcher_RetryOnFailure(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("server error"))
	}))
	defer srv.Close()

	whID := uuid.New().String()
	wh := types.OutboundWebhook{
		ID:             whID,
		Name:           "fail-hook",
		URL:            srv.URL,
		Events:         []string{"*"},
		MaxRetries:     3,
		BackoffSeconds: []int{1, 2, 5},
		CBThreshold:    100, // high so circuit doesn't open
		CBCooldownSecs: 3600,
		Enabled:        true,
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	if err := store.CreateOutboundWebhook(ctx, wh); err != nil {
		t.Fatalf("CreateOutboundWebhook: %v", err)
	}

	dispatcher := webhooks.NewDispatcher(store, nil)
	if err := dispatcher.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer dispatcher.Stop()

	if err := dispatcher.Send(ctx, notifications.Event{
		Type: "agent_error", Severity: "critical", Title: "test", Timestamp: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Wait for delivery.
	time.Sleep(500 * time.Millisecond)

	deliveries, err := store.ListWebhookDeliveries(ctx, whID, 10)
	if err != nil {
		t.Fatalf("ListWebhookDeliveries: %v", err)
	}
	if len(deliveries) == 0 {
		t.Fatal("expected at least 1 delivery record")
	}
	// The first delivery should be pending_retry (since retryCount 0 < maxRetries 3).
	found := false
	for _, d := range deliveries {
		if d.Status == types.DeliveryStatusPendingRetry {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected at least one delivery with status pending_retry, got: %v", deliveries[0].Status)
	}
}

func TestDispatcher_CircuitBreaker(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	whID := uuid.New().String()
	wh := types.OutboundWebhook{
		ID:             whID,
		Name:           "breaker-hook",
		URL:            srv.URL,
		Events:         []string{"*"},
		MaxRetries:     0, // no retries
		BackoffSeconds: []int{},
		CBThreshold:    3,    // trip after 3 failures
		CBCooldownSecs: 3600, // long cooldown
		Enabled:        true,
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	if err := store.CreateOutboundWebhook(ctx, wh); err != nil {
		t.Fatalf("CreateOutboundWebhook: %v", err)
	}

	dispatcher := webhooks.NewDispatcher(store, nil)
	if err := dispatcher.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer dispatcher.Stop()

	// Send 5 events.
	for i := 0; i < 5; i++ {
		_ = dispatcher.Send(ctx, notifications.Event{
			Type: "agent_error", Severity: "critical", Title: "test", Timestamp: time.Now().UTC(),
		})
		// Small delay to let async delivery complete before next send.
		time.Sleep(100 * time.Millisecond)
	}

	// Wait for all deliveries.
	time.Sleep(500 * time.Millisecond)

	// Circuit should have opened after 3 failures, so we expect 3 actual HTTP calls.
	count := callCount.Load()
	if count > 4 {
		t.Errorf("expected <= 4 HTTP calls (circuit should trip after 3), got %d", count)
	}
	if count < 3 {
		t.Errorf("expected at least 3 HTTP calls before circuit trips, got %d", count)
	}
}

func TestDispatcher_AgentScopeRouting(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	var globalCalls, agentCalls atomic.Int32

	globalSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		globalCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer globalSrv.Close()

	agentSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		agentCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer agentSrv.Close()

	// Global webhook (no agent_id).
	globalWH := types.OutboundWebhook{
		ID: uuid.New().String(), Name: "global", URL: globalSrv.URL,
		Events: []string{"*"}, MaxRetries: 0, BackoffSeconds: []int{},
		CBThreshold: 100, CBCooldownSecs: 3600, Enabled: true,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	// Agent-scoped webhook.
	agentWH := types.OutboundWebhook{
		ID: uuid.New().String(), Name: "agent-only", URL: agentSrv.URL,
		AgentID: "agent-1", Events: []string{"*"}, MaxRetries: 0, BackoffSeconds: []int{},
		CBThreshold: 100, CBCooldownSecs: 3600, Enabled: true,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}

	if err := store.CreateOutboundWebhook(ctx, globalWH); err != nil {
		t.Fatalf("CreateOutboundWebhook(global): %v", err)
	}
	if err := store.CreateOutboundWebhook(ctx, agentWH); err != nil {
		t.Fatalf("CreateOutboundWebhook(agent): %v", err)
	}

	dispatcher := webhooks.NewDispatcher(store, nil)
	if err := dispatcher.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer dispatcher.Stop()

	// Event from agent-1: both should fire.
	_ = dispatcher.Send(ctx, notifications.Event{
		Type: "agent_error", Severity: "critical", Agent: "agent-1",
		Title: "test", Timestamp: time.Now().UTC(),
	})
	time.Sleep(300 * time.Millisecond)

	// Event from agent-2: only global should fire.
	_ = dispatcher.Send(ctx, notifications.Event{
		Type: "agent_error", Severity: "critical", Agent: "agent-2",
		Title: "test", Timestamp: time.Now().UTC(),
	})
	time.Sleep(300 * time.Millisecond)

	if globalCalls.Load() != 2 {
		t.Errorf("global webhook calls = %d, want 2", globalCalls.Load())
	}
	if agentCalls.Load() != 1 {
		t.Errorf("agent webhook calls = %d, want 1 (only for agent-1)", agentCalls.Load())
	}
}

func TestMatchesEventPatterns(t *testing.T) {
	// Use exported MatchesEventPatterns if available, otherwise test via Send.
	// Since matchesEventPatterns is unexported, we test it indirectly through the dispatcher.
	// Instead, let's test it via table-driven approach using the dispatcher behavior.
	store := newTestStore(t)
	ctx := context.Background()

	tests := []struct {
		name      string
		patterns  []string
		eventType string
		wantFire  bool
	}{
		{"wildcard matches all", []string{"*"}, "agent_error", true},
		{"exact match", []string{"agent.error"}, "agent_error", true}, // agent_error maps to agent.error
		{"glob prefix", []string{"circuit_breaker.*"}, "circuit_breaker", true}, // maps to circuit_breaker.tripped
		{"no match", []string{"spending.*"}, "agent_error", false},
		{"multiple patterns first matches", []string{"agent.*", "spending.*"}, "agent_error", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var called atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			whID := uuid.New().String()
			wh := types.OutboundWebhook{
				ID: whID, Name: tt.name, URL: srv.URL,
				Events: tt.patterns, MaxRetries: 0, BackoffSeconds: []int{},
				CBThreshold: 100, CBCooldownSecs: 3600, Enabled: true,
				CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
			}
			if err := store.CreateOutboundWebhook(ctx, wh); err != nil {
				t.Fatalf("CreateOutboundWebhook: %v", err)
			}
			defer store.DeleteOutboundWebhook(ctx, whID)

			dispatcher := webhooks.NewDispatcher(store, nil)
			_ = dispatcher.Start()
			defer dispatcher.Stop()

			_ = dispatcher.Send(ctx, notifications.Event{
				Type: tt.eventType, Severity: "info", Title: "test", Timestamp: time.Now().UTC(),
			})
			time.Sleep(300 * time.Millisecond)

			fired := called.Load() > 0
			if fired != tt.wantFire {
				t.Errorf("fired = %v, want %v", fired, tt.wantFire)
			}
		})
	}
}

func TestDispatcher_TemplateRendering(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	var receivedBody []byte
	var mu sync.Mutex

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		receivedBody, _ = io.ReadAll(r.Body)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	whID := uuid.New().String()
	wh := types.OutboundWebhook{
		ID:              whID,
		Name:            "template-hook",
		URL:             srv.URL,
		Events:          []string{"*"},
		PayloadTemplate: `{"custom_event":"{{.Event}}","custom_title":"{{.Title}}"}`,
		MaxRetries:      0,
		BackoffSeconds:  []int{},
		CBThreshold:     100,
		CBCooldownSecs:  3600,
		Enabled:         true,
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
	}
	if err := store.CreateOutboundWebhook(ctx, wh); err != nil {
		t.Fatalf("CreateOutboundWebhook: %v", err)
	}

	dispatcher := webhooks.NewDispatcher(store, nil)
	_ = dispatcher.Start()
	defer dispatcher.Stop()

	_ = dispatcher.Send(ctx, notifications.Event{
		Type: "agent_error", Severity: "info", Title: "Custom Test", Timestamp: time.Now().UTC(),
	})
	time.Sleep(300 * time.Millisecond)

	mu.Lock()
	body := receivedBody
	mu.Unlock()

	if len(body) == 0 {
		t.Fatal("no body received")
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal: %v, body: %s", err, body)
	}
	if payload["custom_event"] != "agent.error" {
		t.Errorf("custom_event = %q, want %q", payload["custom_event"], "agent.error")
	}
	if payload["custom_title"] != "Custom Test" {
		t.Errorf("custom_title = %q, want %q", payload["custom_title"], "Custom Test")
	}
}
