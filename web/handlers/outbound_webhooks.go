package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"text/template"

	"github.com/google/uuid"
	"github.com/kkjorsvik/kyvik/internal/timeutil"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// OutboundWebhookList renders the outbound webhooks management page.
func (h *Handlers) OutboundWebhookList(w http.ResponseWriter, r *http.Request) {
	if h.outboundStore == nil {
		http.Error(w, "outbound webhooks not configured", http.StatusServiceUnavailable)
		return
	}
	ctx := r.Context()

	agentID := r.URL.Query().Get("agent_id")

	agents, err := h.kyvik.ListAgents(ctx)
	if err != nil {
		h.serverError(w, r, "listing agents", err)
		return
	}

	webhooks, err := h.outboundStore.ListOutboundWebhooks(ctx, agentID)
	if err != nil {
		h.serverError(w, r, "listing webhooks", err)
		return
	}

	data := map[string]any{
		"Nav":      "outbound-webhooks",
		"Title":    "Outbound Webhooks",
		"AgentID":  agentID,
		"Agents":   agents,
		"Webhooks": webhooks,
	}

	if isHTMX(r) {
		h.renderFragment(w, r, "outbound-webhooks-list", data)
		return
	}
	h.renderPageWithRequest(w, r, "outbound-webhooks-list", data)
}

// OutboundWebhookTableFragment renders the webhook table for HTMX tab switching.
func (h *Handlers) OutboundWebhookTableFragment(w http.ResponseWriter, r *http.Request) {
	if h.outboundStore == nil {
		http.Error(w, "outbound webhooks not configured", http.StatusServiceUnavailable)
		return
	}

	agentID := r.URL.Query().Get("agent_id")
	webhooks, err := h.outboundStore.ListOutboundWebhooks(r.Context(), agentID)
	if err != nil {
		h.serverError(w, r, "listing webhooks", err)
		return
	}

	h.renderFragment(w, r, "outbound-webhooks-table", map[string]any{
		"AgentID":  agentID,
		"Webhooks": webhooks,
	})
}

// OutboundWebhookCreate handles creating a new outbound webhook from a form.
func (h *Handlers) OutboundWebhookCreate(w http.ResponseWriter, r *http.Request) {
	if h.outboundStore == nil {
		http.Error(w, "outbound webhooks not configured", http.StatusServiceUnavailable)
		return
	}

	name := r.FormValue("name")
	url := r.FormValue("url")
	if name == "" || url == "" {
		http.Error(w, "name and url are required", http.StatusBadRequest)
		return
	}

	agentID := r.FormValue("agent_id")
	events := parseEventPatterns(r.FormValue("events"))
	maxRetries, _ := strconv.Atoi(r.FormValue("max_retries"))
	if maxRetries <= 0 {
		maxRetries = types.DefaultWebhookMaxRetries
	}
	cbThreshold, _ := strconv.Atoi(r.FormValue("cb_threshold"))
	if cbThreshold <= 0 {
		cbThreshold = types.DefaultWebhookCBThreshold
	}
	cbCooldown, _ := strconv.Atoi(r.FormValue("cb_cooldown_secs"))
	if cbCooldown <= 0 {
		cbCooldown = types.DefaultWebhookCBCooldownSec
	}

	payloadTmpl := r.FormValue("payload_template")
	if payloadTmpl != "" {
		if _, err := template.New("validate").Parse(payloadTmpl); err != nil {
			http.Error(w, "invalid payload template", http.StatusBadRequest)
			return
		}
	}

	wh := types.OutboundWebhook{
		ID:              uuid.New().String(),
		Name:            name,
		URL:             url,
		AgentID:         agentID,
		Events:          events,
		SecretRef:       r.FormValue("secret_ref"),
		Headers:         parseHeadersFromForm(r),
		PayloadTemplate: payloadTmpl,
		MaxRetries:      maxRetries,
		BackoffSeconds:  types.DefaultWebhookBackoff,
		CBThreshold:     cbThreshold,
		CBCooldownSecs:  cbCooldown,
		Enabled:         true,
		CreatedAt:       timeutil.NowUTC(),
		UpdatedAt:       timeutil.NowUTC(),
	}

	if err := h.outboundStore.CreateOutboundWebhook(r.Context(), wh); err != nil {
		h.serverError(w, r, "creating webhook", err)
		return
	}

	// Return updated table.
	webhooks, _ := h.outboundStore.ListOutboundWebhooks(r.Context(), agentID)
	h.renderFragment(w, r, "outbound-webhooks-table", map[string]any{
		"AgentID":  agentID,
		"Webhooks": webhooks,
	})
}

// OutboundWebhookEdit handles updating an outbound webhook from a form.
func (h *Handlers) OutboundWebhookEdit(w http.ResponseWriter, r *http.Request) {
	if h.outboundStore == nil {
		http.Error(w, "outbound webhooks not configured", http.StatusServiceUnavailable)
		return
	}
	ctx := r.Context()
	webhookID := r.PathValue("webhookID")

	wh, err := h.outboundStore.GetOutboundWebhook(ctx, webhookID)
	if err != nil {
		http.Error(w, "webhook not found", http.StatusNotFound)
		return
	}

	editPayloadTmpl := r.FormValue("payload_template")
	if editPayloadTmpl != "" {
		if _, err := template.New("validate").Parse(editPayloadTmpl); err != nil {
			http.Error(w, "invalid payload template", http.StatusBadRequest)
			return
		}
	}

	wh.Name = r.FormValue("name")
	wh.URL = r.FormValue("url")
	wh.Events = parseEventPatterns(r.FormValue("events"))
	wh.SecretRef = r.FormValue("secret_ref")
	wh.Headers = parseHeadersFromForm(r)
	wh.PayloadTemplate = editPayloadTmpl
	if v, err := strconv.Atoi(r.FormValue("max_retries")); err == nil && v > 0 {
		wh.MaxRetries = v
	}
	if v, err := strconv.Atoi(r.FormValue("cb_threshold")); err == nil && v > 0 {
		wh.CBThreshold = v
	}
	if v, err := strconv.Atoi(r.FormValue("cb_cooldown_secs")); err == nil && v > 0 {
		wh.CBCooldownSecs = v
	}
	wh.UpdatedAt = timeutil.NowUTC()

	if err := h.outboundStore.UpdateOutboundWebhook(ctx, *wh); err != nil {
		h.serverError(w, r, "updating outbound webhook", err)
		return
	}

	webhooks, _ := h.outboundStore.ListOutboundWebhooks(ctx, wh.AgentID)
	h.renderFragment(w, r, "outbound-webhooks-table", map[string]any{
		"AgentID":  wh.AgentID,
		"Webhooks": webhooks,
	})
}

// OutboundWebhookDelete deletes an outbound webhook.
func (h *Handlers) OutboundWebhookDelete(w http.ResponseWriter, r *http.Request) {
	if h.outboundStore == nil {
		http.Error(w, "outbound webhooks not configured", http.StatusServiceUnavailable)
		return
	}

	webhookID := r.FormValue("webhook_id")
	agentID := r.FormValue("agent_id")

	if err := h.outboundStore.DeleteOutboundWebhook(r.Context(), webhookID); err != nil {
		h.serverError(w, r, "deleting webhook", err)
		return
	}

	webhooks, _ := h.outboundStore.ListOutboundWebhooks(r.Context(), agentID)
	h.renderFragment(w, r, "outbound-webhooks-table", map[string]any{
		"AgentID":  agentID,
		"Webhooks": webhooks,
	})
}

// OutboundWebhookToggle enables or disables an outbound webhook.
func (h *Handlers) OutboundWebhookToggle(w http.ResponseWriter, r *http.Request) {
	if h.outboundStore == nil {
		http.Error(w, "outbound webhooks not configured", http.StatusServiceUnavailable)
		return
	}
	ctx := r.Context()
	webhookID := r.PathValue("webhookID")

	wh, err := h.outboundStore.GetOutboundWebhook(ctx, webhookID)
	if err != nil {
		http.Error(w, "webhook not found", http.StatusNotFound)
		return
	}

	wh.Enabled = !wh.Enabled
	wh.UpdatedAt = timeutil.NowUTC()
	if err := h.outboundStore.UpdateOutboundWebhook(ctx, *wh); err != nil {
		h.serverError(w, r, "toggling webhook", err)
		return
	}

	webhooks, _ := h.outboundStore.ListOutboundWebhooks(ctx, wh.AgentID)
	h.renderFragment(w, r, "outbound-webhooks-table", map[string]any{
		"AgentID":  wh.AgentID,
		"Webhooks": webhooks,
	})
}

// OutboundWebhookTest sends a test event payload directly to the webhook.
func (h *Handlers) OutboundWebhookTest(w http.ResponseWriter, r *http.Request) {
	if h.outboundStore == nil {
		http.Error(w, "outbound webhooks not configured", http.StatusServiceUnavailable)
		return
	}
	webhookID := r.PathValue("webhookID")

	wh, err := h.outboundStore.GetOutboundWebhook(r.Context(), webhookID)
	if err != nil {
		http.Error(w, "webhook not found", http.StatusNotFound)
		return
	}

	testPayload := map[string]any{
		"event":     "test",
		"title":     "Test webhook delivery",
		"detail":    "This is a test event sent from the Kyvik dashboard.",
		"severity":  "info",
		"timestamp": timeutil.NowUTC().Format("2006-01-02T15:04:05Z07:00"),
	}
	body, _ := json.Marshal(testPayload)

	client := &http.Client{Timeout: 10 * 1e9} // 10 seconds
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, wh.URL, strings.NewReader(string(body)))
	if err != nil {
		http.Error(w, "invalid webhook URL", http.StatusBadRequest)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Kyvik-Webhooks/1.0")

	resp, err := client.Do(req)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"status": "error", "error": "webhook delivery failed"})
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"status": "sent", "http_code": resp.StatusCode})
}

// OutboundWebhookDeliveries returns the delivery log for a specific webhook.
func (h *Handlers) OutboundWebhookDeliveries(w http.ResponseWriter, r *http.Request) {
	if h.outboundStore == nil {
		http.Error(w, "outbound webhooks not configured", http.StatusServiceUnavailable)
		return
	}
	webhookID := r.PathValue("webhookID")

	deliveries, err := h.outboundStore.ListWebhookDeliveries(r.Context(), webhookID, 50)
	if err != nil {
		h.serverError(w, r, "listing webhook deliveries", err)
		return
	}

	h.renderFragment(w, r, "outbound-webhook-deliveries", map[string]any{
		"Deliveries": deliveries,
	})
}

// parseEventPatterns handles both JSON array and comma-separated input.
func parseEventPatterns(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return []string{"*"}
	}
	// Try JSON first.
	if strings.HasPrefix(s, "[") {
		var patterns []string
		if err := json.Unmarshal([]byte(s), &patterns); err == nil && len(patterns) > 0 {
			return patterns
		}
	}
	// Fall back to comma-separated.
	parts := strings.Split(s, ",")
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return []string{"*"}
	}
	return out
}

// parseHeadersFromForm reads parallel header_key[]/header_value[] arrays from the form.
func parseHeadersFromForm(r *http.Request) map[string]string {
	_ = r.ParseForm()
	keys := r.Form["header_key[]"]
	values := r.Form["header_value[]"]
	headers := make(map[string]string)
	for i, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		v := ""
		if i < len(values) {
			v = strings.TrimSpace(values[i])
		}
		headers[k] = v
	}
	return headers
}
