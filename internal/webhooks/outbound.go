package webhooks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"sync"
	ttmpl "text/template"
	"time"

	"github.com/google/uuid"
	"github.com/kkjorsvik/kyvik/internal/notifications"
	"github.com/kkjorsvik/kyvik/internal/secrets"
	"github.com/kkjorsvik/kyvik/internal/timeutil"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// OutboundWebhookStore is the narrow interface satisfied by store.Store.
type OutboundWebhookStore interface {
	ListAllEnabledOutboundWebhooks(ctx context.Context) ([]types.OutboundWebhook, error)
	GetOutboundWebhook(ctx context.Context, id string) (*types.OutboundWebhook, error)
	InsertWebhookDelivery(ctx context.Context, d types.WebhookDelivery) error
	ListPendingRetries(ctx context.Context) ([]types.WebhookDelivery, error)
	UpdateDeliveryStatus(ctx context.Context, id string, status types.WebhookDeliveryStatus, httpCode int, responseBody, errMsg string) error
}

// Dispatcher fires HTTP POST notifications to external endpoints when events occur.
// It implements notifications.Notifier.
type Dispatcher struct {
	store    OutboundWebhookStore
	vault    secrets.SecretStore // may be nil
	client   *http.Client
	mu       sync.RWMutex
	breakers map[string]*circuitState
	stopCh   chan struct{}
	wg       sync.WaitGroup
}

type circuitState struct {
	consecutiveFailures int
	openUntil           time.Time
}

// eventTypeMap maps internal notification types to dotted webhook event names.
var eventTypeMap = map[string]string{
	"circuit_breaker":  "circuit_breaker.tripped",
	"agent_error":      "agent.error",
	"agent_killed":     "agent.killed",
	"spending_alert":   "spending.threshold",
	"security_alert":   "security.alert",
	"backup_status":    "backup.complete",
	"key_failure":      "security.key_failure",
	"agent_quarantine": "agent.quarantine",
}

// NewDispatcher creates a new outbound webhook dispatcher.
func NewDispatcher(store OutboundWebhookStore, vault secrets.SecretStore) *Dispatcher {
	return &Dispatcher{
		store:    store,
		vault:    vault,
		client:   &http.Client{Timeout: 30 * time.Second},
		breakers: make(map[string]*circuitState),
		stopCh:   make(chan struct{}),
	}
}

// Send dispatches an event to all matching enabled webhooks.
func (d *Dispatcher) Send(ctx context.Context, event notifications.Event) error {
	webhooks, err := d.store.ListAllEnabledOutboundWebhooks(ctx)
	if err != nil {
		slog.Warn("outbound-webhooks: failed to list webhooks", "error", err)
		return nil // don't block callers
	}

	eventType := mapEventType(event.Type)

	for _, wh := range webhooks {
		// Agent scope check: per-agent webhook only fires for matching agent.
		if wh.AgentID != "" && wh.AgentID != event.Agent {
			continue
		}
		// Event pattern check.
		if !matchesEventPatterns(eventType, wh.Events) {
			continue
		}
		// Circuit breaker check.
		if d.isCircuitOpen(wh.ID, wh.CBThreshold, wh.CBCooldownSecs) {
			continue
		}

		payload, err := buildPayload(wh, event, eventType)
		if err != nil {
			slog.Warn("outbound-webhooks: payload build error", "webhook", wh.ID, "error", err)
			continue
		}
		headers := d.resolveHeaders(ctx, wh)

		// Fire delivery in background.
		whCopy := wh
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			d.deliver(context.Background(), whCopy, eventType, payload, headers, 0)
		}()
	}

	return nil
}

// Start begins the background retry loop.
func (d *Dispatcher) Start() error {
	d.wg.Add(1)
	go d.retryLoop()
	return nil
}

// Stop signals the retry loop to stop and waits for in-flight deliveries.
func (d *Dispatcher) Stop() {
	close(d.stopCh)
	d.wg.Wait()
}

// deliver performs an HTTP POST to the webhook URL and records the result.
func (d *Dispatcher) deliver(ctx context.Context, wh types.OutboundWebhook, eventType string, payload []byte, headers map[string]string, retryCount int) {
	start := timeutil.NowUTC()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, wh.URL, bytes.NewReader(payload))
	if err != nil {
		d.recordDelivery(ctx, wh, eventType, payload, 0, "", 0, retryCount, err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Kyvik-Webhooks/1.0")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := d.client.Do(req)
	durationMs := int(time.Since(start).Milliseconds())

	if err != nil {
		d.handleFailure(ctx, wh, eventType, payload, 0, "", durationMs, retryCount, err.Error())
		return
	}
	defer resp.Body.Close()

	// Read response body (first 1 KiB).
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	respStr := string(respBody)

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		// Success — reset circuit breaker.
		d.resetBreaker(wh.ID)
		d.recordDelivery(ctx, wh, eventType, payload, resp.StatusCode, respStr, durationMs, retryCount, "")
	} else {
		d.handleFailure(ctx, wh, eventType, payload, resp.StatusCode, respStr, durationMs, retryCount,
			fmt.Sprintf("HTTP %d", resp.StatusCode))
	}
}

func (d *Dispatcher) handleFailure(ctx context.Context, wh types.OutboundWebhook, eventType string, payload []byte,
	httpCode int, respBody string, durationMs, retryCount int, errMsg string) {

	d.incrementBreaker(wh.ID, wh.CBCooldownSecs)

	status := types.DeliveryStatusFailed
	var nextRetry *time.Time

	if retryCount < wh.MaxRetries && retryCount < len(wh.BackoffSeconds) {
		status = types.DeliveryStatusPendingRetry
		t := timeutil.NowUTC().Add(time.Duration(wh.BackoffSeconds[retryCount]) * time.Second)
		nextRetry = &t
	}

	h := sha256.Sum256(payload)
	delivery := types.WebhookDelivery{
		ID:            uuid.New().String(),
		WebhookID:     wh.ID,
		EventType:     eventType,
		Payload:       string(payload),
		Status:        status,
		HTTPCode:      httpCode,
		ResponseBody:  respBody,
		DurationMs:    durationMs,
		RetryCount:    retryCount,
		NextRetryAt:   nextRetry,
		ErrorMessage:  errMsg,
		PayloadSha256: hex.EncodeToString(h[:]),
		CreatedAt:     timeutil.NowUTC(),
	}

	if err := d.store.InsertWebhookDelivery(ctx, delivery); err != nil {
		slog.Warn("outbound-webhooks: failed to insert delivery", "error", err)
	}
}

func (d *Dispatcher) recordDelivery(ctx context.Context, wh types.OutboundWebhook, eventType string, payload []byte,
	httpCode int, respBody string, durationMs, retryCount int, errMsg string) {

	status := types.DeliveryStatusSuccess
	if errMsg != "" {
		status = types.DeliveryStatusFailed
	}

	h := sha256.Sum256(payload)
	delivery := types.WebhookDelivery{
		ID:            uuid.New().String(),
		WebhookID:     wh.ID,
		EventType:     eventType,
		Payload:       string(payload),
		Status:        status,
		HTTPCode:      httpCode,
		ResponseBody:  respBody,
		DurationMs:    durationMs,
		RetryCount:    retryCount,
		ErrorMessage:  errMsg,
		PayloadSha256: hex.EncodeToString(h[:]),
		CreatedAt:     timeutil.NowUTC(),
	}

	if err := d.store.InsertWebhookDelivery(ctx, delivery); err != nil {
		slog.Warn("outbound-webhooks: failed to insert delivery", "error", err)
	}
}

// retryLoop polls for pending retries every 10 seconds.
func (d *Dispatcher) retryLoop() {
	defer d.wg.Done()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-d.stopCh:
			return
		case <-ticker.C:
			d.processRetries()
		}
	}
}

func (d *Dispatcher) processRetries() {
	ctx := context.Background()
	retries, err := d.store.ListPendingRetries(ctx)
	if err != nil {
		slog.Warn("outbound-webhooks: failed to list pending retries", "error", err)
		return
	}

	for _, delivery := range retries {
		wh, err := d.store.GetOutboundWebhook(ctx, delivery.WebhookID)
		if err != nil {
			slog.Warn("outbound-webhooks: webhook not found for retry", "webhook_id", delivery.WebhookID, "error", err)
			// Mark as failed since webhook is gone.
			_ = d.store.UpdateDeliveryStatus(ctx, delivery.ID, types.DeliveryStatusFailed, 0, "", "webhook deleted")
			continue
		}
		if !wh.Enabled {
			continue
		}
		if d.isCircuitOpen(wh.ID, wh.CBThreshold, wh.CBCooldownSecs) {
			continue
		}

		// Update old delivery to failed (we create a new one for the retry).
		_ = d.store.UpdateDeliveryStatus(ctx, delivery.ID, types.DeliveryStatusFailed, delivery.HTTPCode, delivery.ResponseBody, delivery.ErrorMessage)

		headers := d.resolveHeaders(ctx, *wh)
		d.deliver(ctx, *wh, delivery.EventType, []byte(delivery.Payload), headers, delivery.RetryCount+1)
	}
}

// ─── Circuit breaker ────────────────────────────────────────────────────────

func (d *Dispatcher) isCircuitOpen(webhookID string, threshold, _ int) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	cs, ok := d.breakers[webhookID]
	if !ok {
		return false
	}
	if cs.consecutiveFailures >= threshold {
		if timeutil.NowUTC().Before(cs.openUntil) {
			return true
		}
		// Half-open: allow one attempt (the deliver call will reset or increment).
	}
	return false
}

func (d *Dispatcher) incrementBreaker(webhookID string, cooldownSecs int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	cs, ok := d.breakers[webhookID]
	if !ok {
		cs = &circuitState{}
		d.breakers[webhookID] = cs
	}
	cs.consecutiveFailures++
	cs.openUntil = timeutil.NowUTC().Add(time.Duration(cooldownSecs) * time.Second)
}

func (d *Dispatcher) resetBreaker(webhookID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.breakers, webhookID)
}

// ─── Event mapping and pattern matching ─────────────────────────────────────

func mapEventType(raw string) string {
	if mapped, ok := eventTypeMap[raw]; ok {
		return mapped
	}
	return raw
}

// matchesEventPatterns checks if eventType matches any of the patterns.
// Supports: "*" (match all), exact match, and glob via path.Match.
func matchesEventPatterns(eventType string, patterns []string) bool {
	for _, p := range patterns {
		if p == "*" {
			return true
		}
		if p == eventType {
			return true
		}
		if matched, _ := path.Match(p, eventType); matched {
			return true
		}
	}
	return false
}

// ─── Payload building ───────────────────────────────────────────────────────

type payloadData struct {
	Event     string         `json:"event"`
	AgentID   string         `json:"agent_id,omitempty"`
	Title     string         `json:"title"`
	Detail    string         `json:"detail"`
	Severity  string         `json:"severity"`
	Timestamp string         `json:"timestamp"`
	Instance  string         `json:"instance,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

func buildPayload(wh types.OutboundWebhook, event notifications.Event, eventType string) ([]byte, error) {
	if wh.PayloadTemplate != "" {
		return buildTemplatedPayload(wh.PayloadTemplate, event, eventType)
	}
	return buildDefaultPayload(event, eventType)
}

func buildDefaultPayload(event notifications.Event, eventType string) ([]byte, error) {
	pd := payloadData{
		Event:     eventType,
		AgentID:   event.Agent,
		Title:     event.Title,
		Detail:    event.Detail,
		Severity:  event.Severity,
		Timestamp: event.Timestamp.UTC().Format(time.RFC3339),
		Metadata:  event.Metadata,
	}
	return json.Marshal(pd)
}

func buildTemplatedPayload(tmplStr string, event notifications.Event, eventType string) ([]byte, error) {
	t, err := ttmpl.New("payload").Parse(tmplStr)
	if err != nil {
		return nil, fmt.Errorf("parse payload template: %w", err)
	}
	data := map[string]any{
		"Event":     eventType,
		"AgentID":   event.Agent,
		"Title":     event.Title,
		"Detail":    event.Detail,
		"Severity":  event.Severity,
		"Metadata":  event.Metadata,
		"Timestamp": event.Timestamp.UTC().Format(time.RFC3339),
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("execute payload template: %w", err)
	}
	return buf.Bytes(), nil
}

// ─── Header resolution ──────────────────────────────────────────────────────

func (d *Dispatcher) resolveHeaders(ctx context.Context, wh types.OutboundWebhook) map[string]string {
	headers := make(map[string]string)
	for k, v := range wh.Headers {
		if strings.Contains(v, "{secret}") && d.vault != nil && wh.SecretRef != "" {
			secret, err := d.vault.Get(ctx, "global", wh.SecretRef)
			if err == nil {
				v = strings.ReplaceAll(v, "{secret}", secret)
			}
		}
		headers[k] = v
	}
	return headers
}
