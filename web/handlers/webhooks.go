package handlers

import (
	"encoding/json"
	"net/http"
	"text/template"
	"time"

	"github.com/kkjorsvik/kyvik/internal/audit"
	"github.com/kkjorsvik/kyvik/internal/timeutil"
	"github.com/kkjorsvik/kyvik/internal/webhooks"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// AgentWebhookEnable enables inbound webhooks for an agent, generating a secret if needed.
func (h *Handlers) AgentWebhookEnable(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	ctx := r.Context()

	config, err := h.kyvik.GetAgent(ctx, agentID)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}
	if config.WebhookInbound == nil {
		config.WebhookInbound = &types.InboundWebhookConfig{RateLimit: 60}
	}
	config.WebhookInbound.Enabled = true

	// Generate secret if not already in vault.
	if h.secrets != nil {
		if existing, _ := webhooks.GetSecret(ctx, h.secrets, agentID); existing == "" {
			if _, err := webhooks.GenerateSecret(ctx, h.secrets, agentID); err != nil {
				http.Error(w, "failed to generate secret", http.StatusInternalServerError)
				return
			}
		}
	}

	config.UpdatedAt = timeutil.NowUTC()
	if err := h.kyvik.UpdateAgent(ctx, *config); err != nil {
		http.Error(w, "failed to save config", http.StatusInternalServerError)
		return
	}
	h.renderWebhookCard(w, r, config)
}

// AgentWebhookDisable disables inbound webhooks for an agent (secret preserved in vault).
func (h *Handlers) AgentWebhookDisable(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	ctx := r.Context()

	config, err := h.kyvik.GetAgent(ctx, agentID)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}
	if config.WebhookInbound != nil {
		config.WebhookInbound.Enabled = false
	}
	config.UpdatedAt = timeutil.NowUTC()
	if err := h.kyvik.UpdateAgent(ctx, *config); err != nil {
		http.Error(w, "failed to save config", http.StatusInternalServerError)
		return
	}
	h.renderWebhookCard(w, r, config)
}

// AgentWebhookSaveTemplate saves the transform template for an agent's webhook.
func (h *Handlers) AgentWebhookSaveTemplate(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	ctx := r.Context()

	config, err := h.kyvik.GetAgent(ctx, agentID)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}
	if config.WebhookInbound == nil {
		config.WebhookInbound = &types.InboundWebhookConfig{}
	}
	tmpl := r.FormValue("transform_template")
	if tmpl != "" {
		if _, err := template.New("validate").Parse(tmpl); err != nil {
			http.Error(w, "invalid transform template", http.StatusBadRequest)
			return
		}
	}
	config.WebhookInbound.TransformTemplate = tmpl
	config.UpdatedAt = timeutil.NowUTC()

	if err := h.kyvik.UpdateAgent(ctx, *config); err != nil {
		http.Error(w, "failed to save config", http.StatusInternalServerError)
		return
	}
	h.renderWebhookCard(w, r, config)
}

// AgentWebhookRevealSecret returns the plaintext webhook secret for display.
func (h *Handlers) AgentWebhookRevealSecret(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if h.secrets == nil {
		http.Error(w, "secrets vault not configured", http.StatusServiceUnavailable)
		return
	}
	secret, err := webhooks.GetSecret(r.Context(), h.secrets, agentID)
	if err != nil || secret == "" {
		http.Error(w, "secret not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte(secret))
}

// AgentWebhookDeliveries returns the last 50 webhook delivery audit entries as JSON.
func (h *Handlers) AgentWebhookDeliveries(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	al := h.kyvik.Audit()
	if al == nil {
		http.Error(w, "audit not configured", http.StatusServiceUnavailable)
		return
	}
	entries, err := al.Query(r.Context(), audit.Filter{
		AgentID:   agentID,
		EventType: types.EventWebhook,
		Limit:     50,
	})
	if err != nil {
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(entries)
}

// AgentWebhookTest sends a test payload to the agent via the webhook channel.
func (h *Handlers) AgentWebhookTest(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	msg := types.Message{
		AgentID: agentID,
		Channel: "webhook",
		Role:    "user",
		Content: `{"event":"test","source":"dashboard","timestamp":"` + time.Now().UTC().Format(time.RFC3339) + `"}`,
	}
	if err := h.kyvik.SendMessage(r.Context(), agentID, msg); err != nil {
		http.Error(w, "failed to queue test message", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"sent"}`))
}

func (h *Handlers) renderWebhookCard(w http.ResponseWriter, r *http.Request, config *types.AgentConfig) {
	secret := ""
	if h.secrets != nil {
		secret, _ = webhooks.GetSecret(r.Context(), h.secrets, config.ID)
	}
	data := map[string]any{
		"Agent":  config,
		"Secret": secret,
	}
	h.injectTemplateUser(r.Context(), data)
	h.renderFragment(w, r, "card-webhooks", data)
}
