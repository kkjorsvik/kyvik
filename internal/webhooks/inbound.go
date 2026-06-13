// Package webhooks implements the inbound webhook HTTP handler.
// Each agent gets a unique URL: POST /webhooks/{agent_id}/{secret}
// The secret is stored in the secrets vault (agent scope, key "webhook_inbound_secret").
package webhooks

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/kkjorsvik/kyvik/internal/audit"
	"github.com/kkjorsvik/kyvik/internal/secrets"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

const (
	defaultRateLimit    = 60      // requests per minute
	defaultMaxPayload   = 1 << 20 // 1 MiB
	SecretVaultKey      = "webhook_inbound_secret"
	HMACVaultKey        = "webhook_inbound_hmac_secret"
	rateLimitWindowSecs = 60
)

// MessageSender is the subset of core.Kyvik needed by the webhook handler.
type MessageSender interface {
	SendMessage(ctx context.Context, agentID string, msg types.Message) error
}

// AgentGetter is the subset of store.Store needed by the webhook handler.
type AgentGetter interface {
	GetAgent(ctx context.Context, id string) (*types.AgentConfig, error)
}

// Handler is the HTTP handler for all inbound webhook endpoints.
type Handler struct {
	agents AgentGetter
	vault  secrets.SecretStore
	kyvik  MessageSender
	audit  audit.Logger

	mu       sync.RWMutex
	limiters map[string]*agentLimiter
}

// New creates a new inbound webhook Handler.
func New(s AgentGetter, v secrets.SecretStore, k MessageSender, al audit.Logger) *Handler {
	return &Handler{
		agents:   s,
		vault:    v,
		kyvik:    k,
		audit:    al,
		limiters: make(map[string]*agentLimiter),
	}
}

// GenerateSecret creates a new 32-byte random hex secret and stores it in the vault.
func GenerateSecret(ctx context.Context, vault secrets.SecretStore, agentID string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate secret: %w", err)
	}
	secret := hex.EncodeToString(b)
	if err := vault.Set(ctx, "agent:"+agentID, SecretVaultKey, secret, "inbound webhook URL secret"); err != nil {
		return "", fmt.Errorf("store secret: %w", err)
	}
	return secret, nil
}

// GetSecret retrieves the webhook secret from the vault. Returns ("", nil) if not set.
func GetSecret(ctx context.Context, vault secrets.SecretStore, agentID string) (string, error) {
	secret, err := vault.Get(ctx, "agent:"+agentID, SecretVaultKey)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return "", nil
		}
		return "", err
	}
	return secret, nil
}

// ComputeHMAC returns the sha256= prefixed HMAC for the given body and secret.
func ComputeHMAC(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// ServeHTTP handles POST /webhooks/{agent_id}/{webhook_secret}.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	agentID := r.PathValue("agent_id")
	urlSecret := r.PathValue("webhook_secret")
	sourceIP := extractIP(r)

	// 1. Load agent config.
	config, err := h.agents.GetAgent(r.Context(), agentID)
	if err != nil || config.WebhookInbound == nil || !config.WebhookInbound.Enabled {
		// Return 401 (not 404) to avoid leaking agent IDs.
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	wh := config.WebhookInbound

	// 2. Validate secret (constant-time compare).
	vaultSecret, err := h.vault.Get(r.Context(), "agent:"+agentID, SecretVaultKey)
	if err != nil || !hmac.Equal([]byte(urlSecret), []byte(vaultSecret)) {
		h.logDelivery(r.Context(), agentID, sourceIP, 0, "rejected", "invalid secret", "")
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	// 3. IP allowlist.
	if !checkIPAllowlist(sourceIP, wh.AllowedSources) {
		h.logDelivery(r.Context(), agentID, sourceIP, 0, "rejected", "ip not allowed", "")
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}

	// 4. Rate limit.
	limit := wh.RateLimit
	if limit <= 0 {
		limit = defaultRateLimit
	}
	if !h.allowRequest(agentID, limit) {
		h.logDelivery(r.Context(), agentID, sourceIP, 0, "rate_limited", "rate limit exceeded", "")
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "rate limit exceeded"})
		return
	}

	// 5. Read body.
	maxBytes := wh.MaxPayloadBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxPayload
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBytes+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "read body failed"})
		return
	}
	if int64(len(body)) > maxBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "payload too large"})
		return
	}
	payloadSize := len(body)

	// 6. HMAC signature verification (optional).
	if wh.SignatureHeader != "" {
		hmacSecret, herr := h.vault.Get(r.Context(), "agent:"+agentID, HMACVaultKey)
		if herr == nil && hmacSecret != "" {
			sig := r.Header.Get(wh.SignatureHeader)
			if !verifyHMAC(body, hmacSecret, sig) {
				h.logDelivery(r.Context(), agentID, sourceIP, payloadSize, "rejected", "invalid signature", "")
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "signature verification failed"})
				return
			}
		}
	}

	// 7. Parse payload.
	content, parseErr := buildMessageContent(r, body, wh.TransformTemplate)
	if parseErr != nil {
		h.logDelivery(r.Context(), agentID, sourceIP, payloadSize, "rejected", "parse error: "+parseErr.Error(), "")
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unparseable payload: " + parseErr.Error()})
		return
	}

	// 8. Inject into agent queue.
	msg := types.Message{
		AgentID: agentID,
		Channel: "webhook",
		Role:    "user",
		Content: content,
	}
	if err := h.kyvik.SendMessage(r.Context(), agentID, msg); err != nil {
		slog.Error("webhook: failed to queue message", "agent_id", agentID, "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "agent unavailable"})
		return
	}

	// 9. Log and respond.
	msgID := fmt.Sprintf("wh-%d", time.Now().UnixNano())
	h.logDelivery(r.Context(), agentID, sourceIP, payloadSize, "accepted", "", msgID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "accepted", "message_id": msgID})
}

// buildMessageContent parses the request body and applies an optional Go template.
func buildMessageContent(r *http.Request, body []byte, tmplStr string) (string, error) {
	ct := r.Header.Get("Content-Type")

	var data map[string]any

	switch {
	case strings.HasPrefix(ct, "application/json"):
		if err := json.Unmarshal(body, &data); err != nil {
			return "", err
		}
	case strings.HasPrefix(ct, "application/x-www-form-urlencoded"),
		strings.HasPrefix(ct, "multipart/form-data"):
		if err := r.ParseForm(); err != nil {
			return "", err
		}
		data = make(map[string]any, len(r.Form))
		for k, v := range r.Form {
			if len(v) == 1 {
				data[k] = v[0]
			} else {
				data[k] = v
			}
		}
	default:
		// Try JSON; fall back to raw text.
		if err := json.Unmarshal(body, &data); err != nil {
			return string(body), nil
		}
	}

	if tmplStr == "" {
		pretty, _ := json.MarshalIndent(data, "", "  ")
		return string(pretty), nil
	}

	tmpl, err := template.New("webhook").Parse(tmplStr)
	if err != nil {
		return "", fmt.Errorf("invalid template: %w", err)
	}
	var sb strings.Builder
	if err := tmpl.Execute(&sb, data); err != nil {
		return "", fmt.Errorf("template execute: %w", err)
	}
	return sb.String(), nil
}

// verifyHMAC checks GitHub-style "sha256=<hex>" signatures.
func verifyHMAC(body []byte, secret, sig string) bool {
	hexSig := strings.TrimPrefix(sig, "sha256=")
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(hexSig), []byte(expected))
}

// checkIPAllowlist returns true if sourceIP is in the allowlist (or allowlist is empty).
func checkIPAllowlist(sourceIP string, allowlist []string) bool {
	if len(allowlist) == 0 {
		return true
	}
	ip := net.ParseIP(sourceIP)
	for _, entry := range allowlist {
		if strings.Contains(entry, "/") {
			_, cidr, err := net.ParseCIDR(entry)
			if err == nil && ip != nil && cidr.Contains(ip) {
				return true
			}
		} else if entry == sourceIP {
			return true
		}
	}
	return false
}

// extractIP returns the real client IP, respecting X-Forwarded-For.
func extractIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		parts := strings.SplitN(fwd, ",", 2)
		return strings.TrimSpace(parts[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// logDelivery writes a webhook delivery audit entry.
func (h *Handler) logDelivery(ctx context.Context, agentID, sourceIP string, payloadSize int, decision, reason, msgID string) {
	if h.audit == nil {
		return
	}
	details, _ := json.Marshal(map[string]any{
		"source_ip":    sourceIP,
		"payload_size": payloadSize,
		"message_id":   msgID,
		"reason":       reason,
	})
	_ = h.audit.Log(ctx, types.AuditEntry{
		AgentID:   agentID,
		EventType: types.EventWebhook,
		Action:    "webhook.delivery",
		Resource:  sourceIP,
		Decision:  decision,
		Details:   string(details),
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// --- Rate limiter ---

type agentLimiter struct {
	mu      sync.Mutex
	window  []time.Time
	maxReqs int
}

func (h *Handler) allowRequest(agentID string, limit int) bool {
	h.mu.RLock()
	lim, ok := h.limiters[agentID]
	h.mu.RUnlock()
	if !ok {
		h.mu.Lock()
		// Re-check after acquiring write lock.
		if lim, ok = h.limiters[agentID]; !ok {
			lim = &agentLimiter{maxReqs: limit}
			h.limiters[agentID] = lim
		}
		h.mu.Unlock()
	}

	lim.mu.Lock()
	defer lim.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-rateLimitWindowSecs * time.Second)
	// Evict expired entries.
	i := 0
	for i < len(lim.window) && lim.window[i].Before(cutoff) {
		i++
	}
	lim.window = lim.window[i:]

	if len(lim.window) >= lim.maxReqs {
		return false
	}
	lim.window = append(lim.window, now)
	return true
}
