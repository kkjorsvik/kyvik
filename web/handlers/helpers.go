// Package handlers implements the HTTP handlers for the Kyvik web dashboard.
package handlers

import (
	"bytes"
	"context"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kkjorsvik/kyvik/internal/authprovider"
	"github.com/kkjorsvik/kyvik/internal/authprovider/local"
	"github.com/kkjorsvik/kyvik/internal/backup"
	"github.com/kkjorsvik/kyvik/internal/capabilities"
	"github.com/kkjorsvik/kyvik/internal/channelmgr"
	"github.com/kkjorsvik/kyvik/internal/channels/webui"
	"github.com/kkjorsvik/kyvik/internal/cluster"
	"github.com/kkjorsvik/kyvik/internal/config"
	"github.com/kkjorsvik/kyvik/internal/core"
	"github.com/kkjorsvik/kyvik/internal/integrations"
	"github.com/kkjorsvik/kyvik/internal/obsidian"
	"github.com/kkjorsvik/kyvik/internal/providers"
	"github.com/kkjorsvik/kyvik/internal/secrets"
	tmplsvc "github.com/kkjorsvik/kyvik/internal/templates"
	"github.com/kkjorsvik/kyvik/internal/users"
	"github.com/kkjorsvik/kyvik/internal/workflow"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// outboundWebhookStore is the narrow store interface for outbound webhook handlers.
type outboundWebhookStore interface {
	CreateOutboundWebhook(ctx context.Context, wh types.OutboundWebhook) error
	GetOutboundWebhook(ctx context.Context, id string) (*types.OutboundWebhook, error)
	UpdateOutboundWebhook(ctx context.Context, wh types.OutboundWebhook) error
	DeleteOutboundWebhook(ctx context.Context, id string) error
	ListOutboundWebhooks(ctx context.Context, agentID string) ([]types.OutboundWebhook, error)
	ListWebhookDeliveries(ctx context.Context, webhookID string, limit int) ([]types.WebhookDelivery, error)
}

// Handlers holds shared dependencies for all HTTP handlers.
type Handlers struct {
	kyvik          *core.Kyvik
	templates      *template.Template
	sessionKey     []byte
	webui          *webui.Adapter
	secrets        secrets.SecretStore
	backupDeps     backup.ExportDeps
	tz             *time.Location // display timezone (nil = UTC)
	defaultTZ      string
	templateSvc    *tmplsvc.Service
	auditStreamCfg config.AuditStreamConfig
	outboundStore  outboundWebhookStore
	integrationMgr     *integrations.Manager
	providerMgr        *providers.Manager
	channelMgr         *channelmgr.Manager
	capabilityResolver *capabilities.Resolver
	auth               authprovider.AuthProvider
	userSvc            *users.Service
	discordAuthStore   discordAuthStore
	workflowEngine     *workflow.Engine
	clusterMgr         cluster.Manager
	obsidianMgr        *obsidian.VaultManager
	trustedProxies     map[string]bool
}

// SetSecretStore sets the secrets vault on the handlers, enabling secrets management.
func (h *Handlers) SetSecretStore(s secrets.SecretStore) {
	h.secrets = s
}

// SetBackupDeps sets the backup export/import dependencies.
func (h *Handlers) SetBackupDeps(deps backup.ExportDeps) {
	h.backupDeps = deps
}

// SetTimezone sets the display timezone for formatting timestamps.
func (h *Handlers) SetTimezone(loc *time.Location) { h.tz = loc }

// SetDefaultTimezone sets the configured IANA timezone string.
func (h *Handlers) SetDefaultTimezone(tz string) { h.defaultTZ = tz }

// SetAuthProvider sets the authentication provider and extracts *users.Service
// from local providers for user CRUD operations.
func (h *Handlers) SetAuthProvider(p authprovider.AuthProvider) {
	h.auth = p
	if lp, ok := p.(*local.Provider); ok {
		h.userSvc = lp.UserService()
	}
}

// SetTemplateService sets the agent template service.
func (h *Handlers) SetTemplateService(svc *tmplsvc.Service) { h.templateSvc = svc }

// SetAuditStreamConfig sets the audit SSE streaming config.
func (h *Handlers) SetAuditStreamConfig(cfg config.AuditStreamConfig) { h.auditStreamCfg = cfg }

// SetOutboundWebhookStore sets the store for outbound webhook management.
func (h *Handlers) SetOutboundWebhookStore(s outboundWebhookStore) { h.outboundStore = s }

// SetProviderManager sets the provider manager.
func (h *Handlers) SetProviderManager(m *providers.Manager) { h.providerMgr = m }

// SetChannelManager sets the channel manager.
func (h *Handlers) SetChannelManager(m *channelmgr.Manager) { h.channelMgr = m }

// SetCapabilityResolver sets the unified capability resolver.
func (h *Handlers) SetCapabilityResolver(r *capabilities.Resolver) { h.capabilityResolver = r }

// SetTrustedProxies configures the set of upstream proxy IPs allowed to supply
// X-Forwarded-Proto and X-Forwarded-Host headers.
func (h *Handlers) SetTrustedProxies(proxies []string) {
	h.trustedProxies = make(map[string]bool, len(proxies))
	for _, p := range proxies {
		h.trustedProxies[p] = true
	}
}

// localTime converts a UTC time to the display timezone.
func (h *Handlers) localTime(t time.Time) time.Time {
	if h.tz != nil {
		return t.In(h.tz)
	}
	return t
}

func (h *Handlers) configuredTimezone() string {
	if h.defaultTZ == "" {
		return "UTC"
	}
	return h.defaultTZ
}

// New creates a new Handlers instance.
func New(kyvik *core.Kyvik, tmpl *template.Template) *Handlers {
	return &Handlers{
		kyvik:      kyvik,
		templates:  tmpl,
		sessionKey: generateKey(),
	}
}

// serverError logs the full error with request context and returns a generic
// 500 response to the client to prevent information leakage.
func (h *Handlers) serverError(w http.ResponseWriter, r *http.Request, msg string, err error) {
	slog.Error(msg, "error", err, "request_id", requestIDFromContext(r.Context()), "method", r.Method, "path", r.URL.Path)
	http.Error(w, "Internal server error", http.StatusInternalServerError)
}

// renderPageWithRequest renders a content template with request-scoped user context.
func (h *Handlers) renderPageWithRequest(w http.ResponseWriter, r *http.Request, templateName string, data map[string]any) {
	h.renderPageForContext(w, r.Context(), templateName, data)
}

func (h *Handlers) renderPageForContext(w http.ResponseWriter, ctx context.Context, templateName string, data map[string]any) {
	// Inject global state into every page.
	data["Nonce"] = cspNonceFromContext(ctx)
	data["VacationMode"] = h.kyvik.VacationModeActive()
	data["EmergencyStop"] = h.kyvik.EmergencyStopActive()
	h.injectTemplateUser(ctx, data)
	if h.clusterMgr != nil {
		data["ClusterEnabled"] = true
		if nodes, err := h.clusterMgr.ListNodes(); err == nil {
			data["ClusterNodeCount"] = len(nodes)
		}
	}
	if h.auth != nil {
		data["AuthCaps"] = h.auth.Capabilities()
	}
	if h.channelMgr != nil {
		var failedChannels []channelmgr.ChannelStatus
		for _, ch := range h.channelMgr.ListChannels() {
			if ch.Enabled && !ch.Connected && ch.Error != "" {
				failedChannels = append(failedChannels, ch)
			}
		}
		if len(failedChannels) > 0 {
			data["FailedChannels"] = failedChannels
		}
	}

	var buf bytes.Buffer
	if err := h.templates.ExecuteTemplate(&buf, templateName, data); err != nil {
		slog.Error("template render error", "template", templateName, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	data["Content"] = template.HTML(buf.String())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.templates.ExecuteTemplate(w, "layout", data); err != nil {
		slog.Error("layout render error", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// renderFragment renders a template fragment without the layout wrapper.
// The request is used to inject the per-request CSP nonce into map[string]any
// data so that fragment templates can emit nonce="{{.Nonce}}" on script tags.
func (h *Handlers) renderFragment(w http.ResponseWriter, r *http.Request, templateName string, data any) {
	if m, ok := data.(map[string]any); ok {
		m["Nonce"] = cspNonceFromContext(r.Context())
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.templates.ExecuteTemplate(w, templateName, data); err != nil {
		slog.Error("fragment render error", "template", templateName, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// isHTMX returns true if the request was made by HTMX.
func isHTMX(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}

func (h *Handlers) injectTemplateUser(ctx context.Context, data map[string]any) {
	if user := h.templateUser(ctx); user != nil {
		data["User"] = user
	}
}

func (h *Handlers) templateUser(ctx context.Context) map[string]any {
	if u, ok := currentDashboardUser(ctx); ok {
		return map[string]any{
			"ID":       u.ID,
			"Username": u.Username,
			"Role":     u.Role,
			"IsAdmin":  u.IsAdmin,
		}
	}
	return nil
}

// validateFormValue checks a user-provided form value for common attack patterns.
func validateFormValue(val string, maxLen int) error {
	if len(val) > maxLen {
		return errors.New("value too long")
	}
	if strings.Contains(val, "\x00") {
		return errors.New("value contains null byte")
	}
	if strings.Contains(val, "../") || strings.Contains(val, "..\\") {
		return errors.New("value contains path traversal")
	}
	if len(val) > 0 && val[0] == '/' {
		return errors.New("value contains absolute path")
	}
	return nil
}
