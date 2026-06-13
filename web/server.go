// Package web provides the HTTP server and web dashboard for Kyvik.
package web

import (
	"embed"
	"encoding/base64"
	"encoding/json"
	"html/template"
	"io/fs"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/kkjorsvik/kyvik/internal/apikeys"
	"github.com/kkjorsvik/kyvik/internal/auth"
	"github.com/kkjorsvik/kyvik/internal/authprovider"
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
	"github.com/kkjorsvik/kyvik/internal/scheduler"
	"github.com/kkjorsvik/kyvik/internal/secrets"
	"github.com/kkjorsvik/kyvik/internal/store"
	tmplsvc "github.com/kkjorsvik/kyvik/internal/templates"
	"github.com/kkjorsvik/kyvik/internal/workflow"
	"github.com/kkjorsvik/kyvik/pkg/types"
	webapi "github.com/kkjorsvik/kyvik/web/api"
	"github.com/kkjorsvik/kyvik/web/handlers"
)

//go:embed templates/*.html templates/**/*.html templates/**/**/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// routeConfig holds optional dependencies passed via functional options.
type routeConfig struct {
	webui          *webui.Adapter
	secrets        secrets.SecretStore
	backupDeps     *backup.ExportDeps
	timezone       string // IANA timezone for display (e.g. "America/Chicago")
	authProvider   authprovider.AuthProvider
	apiKeys        *apikeys.Service
	templateSvc    *tmplsvc.Service
	auditStreamCfg *config.AuditStreamConfig
	webhooks       http.Handler
	outboundStore  store.Store
	integrationMgr *integrations.Manager
	providerMgr    *providers.Manager
	channelMgr       *channelmgr.Manager
	managedAPI       *webapi.ManagedAPI
	discordAuthStore store.Store
	workflowEngine   *workflow.Engine
	clusterMgr       cluster.Manager
	obsidianMgr      *obsidian.VaultManager
	trustedProxies   []string
}

// Option configures SetupRoutes.
type Option func(*routeConfig)

// WithWebUI sets the WebUI adapter for chat functionality.
func WithWebUI(a *webui.Adapter) Option {
	return func(c *routeConfig) { c.webui = a }
}

// WithSecretStore sets the secrets vault for secret management.
func WithSecretStore(s secrets.SecretStore) Option {
	return func(c *routeConfig) { c.secrets = s }
}

// WithBackupDeps sets the backup export/import dependencies.
func WithBackupDeps(deps backup.ExportDeps) Option {
	return func(c *routeConfig) { c.backupDeps = &deps }
}

// WithTimezone sets the IANA timezone used for displaying timestamps.
func WithTimezone(tz string) Option {
	return func(c *routeConfig) { c.timezone = tz }
}

// WithAuthProvider sets the authentication provider.
func WithAuthProvider(p authprovider.AuthProvider) Option {
	return func(c *routeConfig) { c.authProvider = p }
}

// WithAPIKeys enables the REST API with API key authentication.
func WithAPIKeys(svc *apikeys.Service) Option {
	return func(c *routeConfig) { c.apiKeys = svc }
}

// WithTemplateService sets the agent template management service.
func WithTemplateService(svc *tmplsvc.Service) Option {
	return func(c *routeConfig) { c.templateSvc = svc }
}

// WithAuditStreamConfig sets the audit SSE streaming configuration.
func WithAuditStreamConfig(ac config.AuditStreamConfig) Option {
	return func(c *routeConfig) { c.auditStreamCfg = &ac }
}

// WithWebhookHandler registers the inbound webhook handler on the public mux.
// The handler is mounted at POST /webhooks/{agent_id}/{webhook_secret} without auth.
func WithWebhookHandler(h http.Handler) Option {
	return func(c *routeConfig) { c.webhooks = h }
}

// WithOutboundWebhookStore sets the store for outbound webhook management.
func WithOutboundWebhookStore(s store.Store) Option {
	return func(c *routeConfig) { c.outboundStore = s }
}

// WithIntegrationManager sets the integration catalog manager.
func WithIntegrationManager(m *integrations.Manager) Option {
	return func(c *routeConfig) { c.integrationMgr = m }
}

// WithProviderManager sets the LLM provider manager.
func WithProviderManager(m *providers.Manager) Option {
	return func(c *routeConfig) { c.providerMgr = m }
}

// WithChannelManager sets the channel manager for channel settings.
func WithChannelManager(m *channelmgr.Manager) Option {
	return func(c *routeConfig) { c.channelMgr = m }
}

// WithDiscordAuthStore sets the store for Discord user authorization management.
func WithDiscordAuthStore(s store.Store) Option {
	return func(c *routeConfig) { c.discordAuthStore = s }
}

// WithManagedAPI mounts the Sett-managed sync API endpoints on the public mux.
// This should only be used when auth.type == "delegated".
func WithManagedAPI(store webapi.ManagedStore, sharedSecret, version string) Option {
	return func(c *routeConfig) {
		c.managedAPI = webapi.NewManagedAPI(store, sharedSecret, version)
	}
}

// WithWorkflowEngine sets the workflow engine for manual execution from the dashboard.
func WithWorkflowEngine(e *workflow.Engine) Option {
	return func(c *routeConfig) { c.workflowEngine = e }
}

// WithClusterManager sets the cluster manager for cluster management pages.
func WithClusterManager(m cluster.Manager) Option {
	return func(c *routeConfig) { c.clusterMgr = m }
}

// WithObsidianVaultManager sets the Obsidian vault manager for vault settings.
func WithObsidianVaultManager(m *obsidian.VaultManager) Option {
	return func(c *routeConfig) { c.obsidianMgr = m }
}

// WithTrustedProxies sets the upstream proxy IPs whose X-Forwarded-* headers are trusted.
func WithTrustedProxies(proxies []string) Option {
	return func(c *routeConfig) { c.trustedProxies = proxies }
}

// SetupRoutes creates the HTTP handler for the Kyvik web dashboard.
func SetupRoutes(kyvik *core.Kyvik, opts ...Option) http.Handler {
	var cfg routeConfig
	for _, o := range opts {
		o(&cfg)
	}

	// Load display timezone (falls back to UTC).
	loc := time.UTC
	if cfg.timezone != "" {
		if l, err := time.LoadLocation(cfg.timezone); err == nil {
			loc = l
		}
	}

	// Template helper functions
	funcMap := template.FuncMap{
		"localTime": func(t any, layout string) string {
			switch v := t.(type) {
			case time.Time:
				return v.In(loc).Format(layout)
			case *time.Time:
				if v == nil {
					return ""
				}
				return v.In(loc).Format(layout)
			default:
				return ""
			}
		},
		"pct": func(used, limit int64) int {
			if limit <= 0 {
				return 0
			}
			p := int(float64(used) / float64(limit) * 100)
			if p > 100 {
				return 100
			}
			return p
		},
		"pctf": func(used, limit float64) int {
			if limit <= 0 {
				return 0
			}
			p := int(used / limit * 100)
			if p > 100 {
				return 100
			}
			return p
		},
		"dict": func(pairs ...any) map[string]any {
			m := make(map[string]any, len(pairs)/2)
			for i := 0; i < len(pairs)-1; i += 2 {
				m[pairs[i].(string)] = pairs[i+1]
			}
			return m
		},
		"isImageMIME": types.IsImageMIME,
		"joinStrings": strings.Join,
		"cronPreview": scheduler.CronPreview,
		"truncate": func(s string, maxLen int) string {
			if len(s) <= maxLen {
				return s
			}
			return s[:maxLen] + "…"
		},
		"base64": func(data []byte) string {
			return base64.StdEncoding.EncodeToString(data)
		},
		"marshalJSON": func(v any) string {
			b, err := json.MarshalIndent(v, "", "  ")
			if err != nil {
				return "[]"
			}
			return string(b)
		},
		"can": func(user any, permission string) bool {
			if user == nil {
				return false
			}

			role := ""
			isAdmin := false
			switch u := user.(type) {
			case map[string]any:
				if v, ok := u["Role"].(string); ok {
					role = v
				}
				if v, ok := u["IsAdmin"].(bool); ok {
					isAdmin = v
				}
			default:
				rv := reflect.ValueOf(user)
				if rv.Kind() == reflect.Ptr {
					rv = rv.Elem()
				}
				if rv.IsValid() && rv.Kind() == reflect.Struct {
					fRole := rv.FieldByName("Role")
					if fRole.IsValid() && fRole.Kind() == reflect.String {
						role = fRole.String()
					}
					fAdmin := rv.FieldByName("IsAdmin")
					if fAdmin.IsValid() && fAdmin.Kind() == reflect.Bool {
						isAdmin = fAdmin.Bool()
					}
				}
			}

			if isAdmin {
				return auth.Can(auth.RoleAdmin, permission)
			}
			if strings.TrimSpace(role) == "" {
				return false
			}
			return auth.Can(role, permission)
		},
	}

	// Parse all templates
	tmpl := template.Must(template.New("").Funcs(funcMap).ParseFS(templateFS,
		"templates/*.html",
		"templates/agents/*.html",
		"templates/agents/sections/*.html",
		"templates/secrets/*.html",
		"templates/alerts/*.html",
		"templates/system/*.html",
		"templates/backup/*.html",
		"templates/skills/*.html",
		"templates/teams/*.html",
		"templates/paired/*.html",
		"templates/users/*.html",
		"templates/templates/*.html",
		"templates/audit/*.html",
		"templates/spending/*.html",
		"templates/permissions/*.html",
		"templates/webhooks/*.html",
		"templates/integrations/*.html",
		"templates/capabilities/*.html",
		"templates/providers/*.html",
		"templates/channels_settings/*.html",
		"templates/setup/*.html",
		"templates/discord_auth/*.html",
		"templates/settings/*.html",
		"templates/cluster/*.html",
	))

	// Build static file server from embedded FS
	staticSub, _ := fs.Sub(staticFS, "static")
	fileServer := http.FileServer(http.FS(staticSub))

	h := handlers.New(kyvik, tmpl)
	h.SetTimezone(loc)
	h.SetDefaultTimezone(loc.String())

	if cfg.webui != nil {
		h.SetWebUI(cfg.webui)
	}

	if cfg.secrets != nil {
		h.SetSecretStore(cfg.secrets)
	}

	if cfg.backupDeps != nil {
		h.SetBackupDeps(*cfg.backupDeps)
	}
	if cfg.authProvider != nil {
		h.SetAuthProvider(cfg.authProvider)
	}
	if cfg.templateSvc != nil {
		h.SetTemplateService(cfg.templateSvc)
	}
	if cfg.auditStreamCfg != nil {
		h.SetAuditStreamConfig(*cfg.auditStreamCfg)
	}
	if cfg.outboundStore != nil {
		h.SetOutboundWebhookStore(cfg.outboundStore)
	}
	if cfg.integrationMgr != nil {
		h.SetIntegrationManager(cfg.integrationMgr)
	}
	// Resolver is always created; it handles nil managers internally and still
	// serves native-tool capabilities without an integration manager.
	capabilityResolver := capabilities.New(kyvik.SkillManager(), cfg.integrationMgr)
	h.SetCapabilityResolver(capabilityResolver)
	if cfg.providerMgr != nil {
		h.SetProviderManager(cfg.providerMgr)
	}
	if cfg.channelMgr != nil {
		h.SetChannelManager(cfg.channelMgr)
	}
	if cfg.discordAuthStore != nil {
		h.SetDiscordAuthStore(cfg.discordAuthStore)
	}
	if cfg.workflowEngine != nil {
		h.SetWorkflowEngine(cfg.workflowEngine)
	}
	if cfg.clusterMgr != nil {
		h.SetClusterManager(cfg.clusterMgr)
	}
	if cfg.obsidianMgr != nil {
		h.SetObsidianVaultManager(cfg.obsidianMgr)
	}
	h.SetTrustedProxies(cfg.trustedProxies)

	// Top-level mux for public routes
	mux := http.NewServeMux()

	// Static assets — no auth
	mux.Handle("GET /static/", http.StripPrefix("/static/", fileServer))

	// Health check endpoint — no auth
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	// Auth routes — no auth
	loginLimiter := handlers.RateLimit(1, 5)
	mux.HandleFunc("GET /login", h.LoginPage)
	mux.Handle("POST /login", loginLimiter(http.HandlerFunc(h.LoginSubmit)))
	mux.HandleFunc("POST /logout", h.Logout)
	mux.HandleFunc("GET /auth/redirect", h.AuthRedirect)
	mux.HandleFunc("GET /auth/callback", h.AuthCallback)

	// Protected routes
	protected := http.NewServeMux()
	protected.HandleFunc("GET /password/change", h.PasswordChangePage)
	protected.HandleFunc("POST /password/change", h.PasswordChangeSubmit)
	protected.HandleFunc("GET /{$}", h.Dashboard)
	protected.Handle("GET /agents", h.RequirePermission(auth.PermAgentView, http.HandlerFunc(h.AgentList)))
	protected.Handle("GET /agents/new", h.RequirePermission(auth.PermAgentCreate, http.HandlerFunc(h.AgentWizardStep1)))
	protected.Handle("POST /agents/new/step1", h.RequirePermission(auth.PermAgentCreate, http.HandlerFunc(h.AgentWizardStep1Post)))
	protected.Handle("POST /agents/new/step2", h.RequirePermission(auth.PermAgentCreate, http.HandlerFunc(h.AgentWizardStep2)))
	protected.Handle("POST /agents/new/step3", h.RequirePermission(auth.PermAgentCreate, http.HandlerFunc(h.AgentWizardStep3)))
	protected.Handle("POST /agents/new/step4", h.RequirePermission(auth.PermAgentCreate, http.HandlerFunc(h.AgentWizardStep4)))
	protected.Handle("POST /agents/new/step5", h.RequirePermission(auth.PermAgentCreate, http.HandlerFunc(h.AgentWizardStep5)))
	protected.Handle("POST /agents/new/step6", h.RequirePermission(auth.PermAgentCreate, http.HandlerFunc(h.AgentWizardStep6)))
	protected.Handle("POST /agents/new/step7", h.RequirePermission(auth.PermAgentCreate, http.HandlerFunc(h.AgentWizardStep7)))
	protected.Handle("POST /agents/new/step8", h.RequirePermission(auth.PermAgentCreate, http.HandlerFunc(h.AgentWizardStep8)))
	protected.Handle("POST /agents/new/step9", h.RequirePermission(auth.PermAgentCreate, http.HandlerFunc(h.AgentWizardStep9)))
	protected.Handle("POST /agents/new/step10", h.RequirePermission(auth.PermAgentCreate, http.HandlerFunc(h.AgentWizardStep10)))
	protected.Handle("POST /agents/new/quick-review", h.RequirePermission(auth.PermAgentCreate, http.HandlerFunc(h.AgentWizardQuickReview)))
	protected.Handle("POST /agents/new/slot-row", h.RequirePermission(auth.PermAgentCreate, http.HandlerFunc(h.AgentSlotRow)))
	protected.Handle("POST /agents/new/cap-row", h.RequirePermission(auth.PermAgentCreate, http.HandlerFunc(h.AgentCapRow)))
	protected.Handle("GET /agents/souls", h.RequirePermission(auth.PermAgentCreate, http.HandlerFunc(h.AgentSoulsFragment)))
	protected.Handle("GET /agents/identities", h.RequirePermission(auth.PermAgentCreate, http.HandlerFunc(h.AgentIdentitiesFragment)))
	protected.Handle("POST /agents", h.RequirePermission(auth.PermAgentCreate, http.HandlerFunc(h.AgentCreate)))
	// Template-based agent creation (two-step flow)
	protected.Handle("GET /create-from-template", h.RequirePermission(auth.PermAgentCreate, http.HandlerFunc(h.AgentTemplateSelectPage)))
	protected.Handle("GET /create-from-template/{template_id}", h.RequirePermission(auth.PermAgentCreate, http.HandlerFunc(h.AgentTemplateConfigurePage)))
	protected.Handle("POST /create-from-template/{template_id}", h.RequirePermission(auth.PermAgentCreate, http.HandlerFunc(h.AgentTemplateCreate)))
	protected.Handle("GET /agents/{id}", h.RequireAgentPermission(auth.PermAgentView, http.HandlerFunc(h.AgentDetail)))
	protected.Handle("GET /agents/{id}/edit", h.RequireAgentPermission(auth.PermAgentEdit, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.Path+"/general", http.StatusSeeOther)
	})))
	protected.Handle("GET /agents/{id}/edit/{section}", h.RequireAgentPermission(auth.PermAgentEdit, http.HandlerFunc(h.AgentEditSectionGet)))
	protected.Handle("POST /agents/{id}/edit/{section}", h.RequireAgentPermission(auth.PermAgentEdit, http.HandlerFunc(h.AgentEditSectionPost)))
	protected.Handle("POST /agents/{id}/delete", h.RequireAgentPermission(auth.PermAgentDelete, http.HandlerFunc(h.AgentDelete)))
	protected.Handle("POST /agents/{id}/start", h.RequireAgentPermission(auth.PermAgentStart, http.HandlerFunc(h.AgentStart)))
	protected.Handle("POST /agents/{id}/stop", h.RequireAgentPermission(auth.PermAgentStop, http.HandlerFunc(h.AgentStop)))
	protected.Handle("POST /agents/{id}/quarantine", h.RequireAgentPermission(auth.PermAgentQuarantine, http.HandlerFunc(h.AgentQuarantine)))
	protected.Handle("POST /agents/{id}/kill", h.RequireAgentPermission(auth.PermAgentKill, http.HandlerFunc(h.AgentKill)))
	protected.Handle("POST /kill-all", h.RequirePermission(auth.PermAgentKill, http.HandlerFunc(h.KillAll)))
	protected.Handle("POST /emergency-stop/clear", h.RequirePermission(auth.PermAgentKill, http.HandlerFunc(h.ClearEmergencyStop)))
	protected.Handle("POST /vacation/activate", h.RequirePermission(auth.PermAgentStop, http.HandlerFunc(h.ActivateVacation)))
	protected.Handle("POST /vacation/deactivate", h.RequirePermission(auth.PermAgentStart, http.HandlerFunc(h.DeactivateVacation)))
	protected.Handle("GET /alerts", h.RequirePermission(auth.PermAuditView, http.HandlerFunc(h.AlertsPage)))
	protected.Handle("POST /alerts/ack", h.RequirePermission(auth.PermAuditView, http.HandlerFunc(h.AlertAcknowledge)))
	protected.Handle("GET /alerts/badge", h.RequirePermission(auth.PermAuditView, http.HandlerFunc(h.AlertBadge)))
	protected.Handle("GET /agents/{id}/status", h.RequireAgentPermission(auth.PermAgentView, http.HandlerFunc(h.AgentStatusFragment)))
	protected.Handle("GET /agents/{id}/history", h.RequireAgentPermission(auth.PermHistoryView, http.HandlerFunc(h.AgentHistory)))
	protected.Handle("GET /agents/{id}/history/entries", h.RequireAgentPermission(auth.PermHistoryView, http.HandlerFunc(h.AgentHistoryFragment)))
	protected.Handle("POST /agents/{id}/history/clear", h.RequireAgentPermission(auth.PermHistoryView, http.HandlerFunc(h.AgentHistoryClear)))
	protected.Handle("GET /agents/{id}/chat", h.RequireAgentPermission(auth.PermChatWithAgent, http.HandlerFunc(h.AgentChat)))
	protected.Handle("GET /agents/{id}/chat/v1", h.RequireAgentPermission(auth.PermChatWithAgent, http.HandlerFunc(h.AgentChatV1)))
	protected.Handle("GET /agents/{id}/chat2", h.RequireAgentPermission(auth.PermChatWithAgent, http.HandlerFunc(h.AgentChatV2)))
	protected.Handle("GET /agents/{id}/chat2/ws", h.RequireAgentPermission(auth.PermChatWithAgent, http.HandlerFunc(h.AgentChatV2WS)))
	protected.Handle("POST /agents/{id}/chat/send", h.RequireAgentPermission(auth.PermChatWithAgent, http.HandlerFunc(h.AgentChatSend)))
	protected.Handle("GET /agents/{id}/chat/stream", h.RequireAgentPermission(auth.PermChatWithAgent, http.HandlerFunc(h.AgentChatStream)))
	protected.Handle("GET /agents/{id}/chat/messages", h.RequireAgentPermission(auth.PermChatWithAgent, http.HandlerFunc(h.AgentChatConversationMessages)))
	protected.Handle("POST /agents/{id}/chat/new", h.RequireAgentPermission(auth.PermChatWithAgent, http.HandlerFunc(h.AgentChatConversationNew)))
	protected.Handle("POST /agents/{id}/chat/conversations/{convID}/rename", h.RequireAgentPermission(auth.PermChatWithAgent, http.HandlerFunc(h.AgentChatConversationRename)))
	protected.Handle("POST /agents/{id}/chat/conversations/{convID}/delete", h.RequireAgentPermission(auth.PermChatWithAgent, http.HandlerFunc(h.AgentChatConversationDelete)))
	protected.Handle("GET /agents/{id}/chat/sidebar", h.RequireAgentPermission(auth.PermChatWithAgent, http.HandlerFunc(h.AgentChatSidebar)))

	// Permission management routes
	protected.Handle("GET /agents/{id}/permissions", h.RequireAgentPermission(auth.PermPermissionsView, http.HandlerFunc(h.PermissionsPage)))
	protected.Handle("POST /agents/{id}/permissions/overrides", h.RequireAgentPermission(auth.PermPermissionsEdit, http.HandlerFunc(h.PermissionsSaveOverrides)))
	protected.Handle("POST /agents/{id}/permissions/tier", h.RequireAgentPermission(auth.PermPermissionsEdit, http.HandlerFunc(h.PermissionsChangeTier)))
	protected.Handle("POST /agents/{id}/permissions/paths", h.RequireAgentPermission(auth.PermPermissionsEdit, http.HandlerFunc(h.PermissionsSavePaths)))

	// Agent queue routes
	protected.Handle("GET /agents/{id}/queue/stats", h.RequireAgentPermission(auth.PermAgentView, http.HandlerFunc(h.AgentQueueStats)))
	protected.Handle("GET /agents/{id}/queue", h.RequireAgentPermission(auth.PermAgentView, http.HandlerFunc(h.AgentQueue)))
	protected.Handle("GET /agents/{id}/queue/replay", h.RequireAgentPermission(auth.PermAgentView, http.HandlerFunc(h.AgentQueueReplayPreview)))
	protected.Handle("POST /agents/{id}/queue/clear", h.RequireAgentPermission(auth.PermAgentEdit, http.HandlerFunc(h.AgentQueueClear)))
	protected.Handle("POST /agents/{id}/queue/{msgID}/retry", h.RequireAgentPermission(auth.PermAgentEdit, http.HandlerFunc(h.AgentQueueRetry)))
	protected.Handle("POST /agents/{id}/queue/{msgID}/delete", h.RequireAgentPermission(auth.PermAgentEdit, http.HandlerFunc(h.AgentQueueDelete)))
	protected.Handle("GET /agents/{id}/queue/message/{msgID}", h.RequireAgentPermission(auth.PermAgentView, http.HandlerFunc(h.AgentQueueMessageDetail)))

	// Memory routes
	protected.Handle("GET /agents/{id}/memories", h.RequireAgentPermission(auth.PermMemoryView, http.HandlerFunc(h.AgentMemories)))
	protected.Handle("GET /agents/{id}/memories/list", h.RequireAgentPermission(auth.PermMemoryView, http.HandlerFunc(h.AgentMemoriesFragment)))
	protected.Handle("GET /agents/{id}/memories/export", h.RequireAgentPermission(auth.PermMemoryView, http.HandlerFunc(h.AgentMemoriesExport)))
	protected.Handle("POST /agents/{id}/memories/import", h.RequireAgentPermission(auth.PermMemoryEdit, http.HandlerFunc(h.AgentMemoriesImport)))
	protected.Handle("POST /agents/{id}/memories/bulk", h.RequireAgentPermission(auth.PermMemoryEdit, http.HandlerFunc(h.AgentMemoriesBulk)))
	protected.Handle("POST /agents/{id}/memories", h.RequireAgentPermission(auth.PermMemoryEdit, http.HandlerFunc(h.AgentMemoryCreate)))
	protected.Handle("POST /agents/{id}/memories/{memID}/edit", h.RequireAgentPermission(auth.PermMemoryEdit, http.HandlerFunc(h.AgentMemoryUpdate)))
	protected.Handle("POST /agents/{id}/memories/{memID}/delete", h.RequireAgentPermission(auth.PermMemoryEdit, http.HandlerFunc(h.AgentMemoryDelete)))
	protected.Handle("POST /agents/{id}/memories/{memID}/pin", h.RequireAgentPermission(auth.PermMemoryEdit, http.HandlerFunc(h.AgentMemoryTogglePin)))
	protected.Handle("POST /agents/{id}/memories/{memID}/archive", h.RequireAgentPermission(auth.PermMemoryEdit, http.HandlerFunc(h.AgentMemoryArchive)))
	protected.Handle("POST /agents/{id}/memories/{memID}/unarchive", h.RequireAgentPermission(auth.PermMemoryEdit, http.HandlerFunc(h.AgentMemoryUnarchive)))
	protected.Handle("POST /agents/{id}/memories/clear", h.RequireAgentPermission(auth.PermMemoryBulkDelete, http.HandlerFunc(h.AgentMemoriesClear)))
	protected.Handle("POST /agents/{id}/memories/review/{memID}/approve", h.RequireAgentPermission(auth.PermMemoryEdit, http.HandlerFunc(h.AgentMemoryReviewApprove)))
	protected.Handle("POST /agents/{id}/memories/review/{memID}/edit-approve", h.RequireAgentPermission(auth.PermMemoryEdit, http.HandlerFunc(h.AgentMemoryReviewEditApprove)))
	protected.Handle("POST /agents/{id}/memories/review/{memID}/reject", h.RequireAgentPermission(auth.PermMemoryEdit, http.HandlerFunc(h.AgentMemoryReviewReject)))
	protected.Handle("POST /agents/{id}/memories/review/approve-all", h.RequireAgentPermission(auth.PermMemoryEdit, http.HandlerFunc(h.AgentMemoryReviewApproveAll)))

	// Schedule routes
	protected.Handle("GET /agents/{id}/schedules", h.RequireAgentPermission(auth.PermAgentView, http.HandlerFunc(h.ScheduleList)))
	protected.Handle("POST /agents/{id}/schedules", h.RequireAgentPermission(auth.PermAgentEdit, http.HandlerFunc(h.ScheduleCreate)))
	protected.Handle("POST /agents/{id}/schedules/{schedID}/update", h.RequireAgentPermission(auth.PermAgentEdit, http.HandlerFunc(h.ScheduleUpdate)))
	protected.Handle("POST /agents/{id}/schedules/{schedID}/delete", h.RequireAgentPermission(auth.PermAgentEdit, http.HandlerFunc(h.ScheduleDelete)))
	protected.Handle("POST /agents/{id}/schedules/{schedID}/toggle", h.RequireAgentPermission(auth.PermAgentEdit, http.HandlerFunc(h.ScheduleToggle)))
	protected.Handle("GET /schedules/preview", h.RequirePermission(auth.PermAgentView, http.HandlerFunc(h.ScheduleCronPreview)))
	protected.Handle("GET /agents/{id}/schedules/{schedID}/edit", h.RequireAgentPermission(auth.PermAgentEdit, http.HandlerFunc(h.ScheduleEditModal)))

	// Workflow routes
	protected.Handle("GET /agents/{id}/workflows", h.RequireAgentPermission(auth.PermAgentView, http.HandlerFunc(h.WorkflowList)))
	protected.Handle("GET /agents/{id}/workflows/{wid}", h.RequireAgentPermission(auth.PermAgentView, http.HandlerFunc(h.WorkflowDetail)))
	protected.Handle("GET /agents/{id}/workflows/{wid}/edit", h.RequireAgentPermission(auth.PermAgentEdit, http.HandlerFunc(h.WorkflowEditModal)))
	protected.Handle("POST /agents/{id}/workflows/{wid}/edit", h.RequireAgentPermission(auth.PermAgentEdit, http.HandlerFunc(h.WorkflowUpdate)))
	protected.Handle("POST /agents/{id}/workflows/{wid}/toggle", h.RequireAgentPermission(auth.PermAgentEdit, http.HandlerFunc(h.WorkflowToggle)))
	protected.Handle("POST /agents/{id}/workflows/{wid}/execute", h.RequireAgentPermission(auth.PermAgentEdit, http.HandlerFunc(h.WorkflowExecute)))
	protected.Handle("DELETE /agents/{id}/workflows/{wid}", h.RequireAgentPermission(auth.PermAgentEdit, http.HandlerFunc(h.WorkflowDelete)))

	// Heartbeat routes
	protected.Handle("GET /agents/{id}/heartbeat", h.RequireAgentPermission(auth.PermAgentView, http.HandlerFunc(h.HeartbeatSection)))
	protected.Handle("POST /agents/{id}/heartbeat", h.RequireAgentPermission(auth.PermAgentEdit, http.HandlerFunc(h.HeartbeatUpdate)))
	protected.Handle("POST /agents/{id}/heartbeat/toggle", h.RequireAgentPermission(auth.PermAgentEdit, http.HandlerFunc(h.HeartbeatToggle)))
	protected.Handle("POST /agents/{id}/heartbeat/pulse", h.RequireAgentPermission(auth.PermAgentEdit, http.HandlerFunc(h.HeartbeatPulseNow)))

	// Webhook management routes
	protected.Handle("POST /agents/{id}/webhook/enable", h.RequireAgentPermission(auth.PermWebhooksManage, http.HandlerFunc(h.AgentWebhookEnable)))
	protected.Handle("POST /agents/{id}/webhook/disable", h.RequireAgentPermission(auth.PermWebhooksManage, http.HandlerFunc(h.AgentWebhookDisable)))
	protected.Handle("POST /agents/{id}/webhook/template", h.RequireAgentPermission(auth.PermWebhooksManage, http.HandlerFunc(h.AgentWebhookSaveTemplate)))
	protected.Handle("GET /agents/{id}/webhook/secret", h.RequireAgentPermission(auth.PermWebhooksManage, http.HandlerFunc(h.AgentWebhookRevealSecret)))
	protected.Handle("GET /agents/{id}/webhook/deliveries", h.RequireAgentPermission(auth.PermAgentView, http.HandlerFunc(h.AgentWebhookDeliveries)))
	protected.Handle("POST /agents/{id}/webhook/test", h.RequireAgentPermission(auth.PermWebhooksManage, http.HandlerFunc(h.AgentWebhookTest)))

	// Secrets routes
	secretsLimiter := handlers.RateLimit(2, 10)
	protected.Handle("GET /secrets", h.RequirePermission(auth.PermSecretsManage, http.HandlerFunc(h.SecretsList)))
	protected.Handle("GET /secrets/table", h.RequirePermission(auth.PermSecretsManage, http.HandlerFunc(h.SecretsTableFragment)))
	protected.Handle("POST /secrets", h.RequirePermission(auth.PermSecretsManage, secretsLimiter(http.HandlerFunc(h.SecretsCreate))))
	protected.Handle("POST /secrets/delete", h.RequirePermission(auth.PermSecretsManage, secretsLimiter(http.HandlerFunc(h.SecretsDelete))))
	protected.Handle("GET /secrets/copy", h.RequirePermission(auth.PermSecretsManage, http.HandlerFunc(h.SecretsCopy)))

	// Outbound webhook management routes
	protected.Handle("GET /outbound-webhooks", h.RequirePermission(auth.PermWebhooksManage, http.HandlerFunc(h.OutboundWebhookList)))
	protected.Handle("GET /outbound-webhooks/table", h.RequirePermission(auth.PermWebhooksManage, http.HandlerFunc(h.OutboundWebhookTableFragment)))
	protected.Handle("POST /outbound-webhooks", h.RequirePermission(auth.PermWebhooksManage, http.HandlerFunc(h.OutboundWebhookCreate)))
	protected.Handle("POST /outbound-webhooks/{webhookID}/edit", h.RequirePermission(auth.PermWebhooksManage, http.HandlerFunc(h.OutboundWebhookEdit)))
	protected.Handle("POST /outbound-webhooks/delete", h.RequirePermission(auth.PermWebhooksManage, http.HandlerFunc(h.OutboundWebhookDelete)))
	protected.Handle("POST /outbound-webhooks/{webhookID}/toggle", h.RequirePermission(auth.PermWebhooksManage, http.HandlerFunc(h.OutboundWebhookToggle)))
	protected.Handle("POST /outbound-webhooks/{webhookID}/test", h.RequirePermission(auth.PermWebhooksManage, http.HandlerFunc(h.OutboundWebhookTest)))
	protected.Handle("GET /outbound-webhooks/{webhookID}/deliveries", h.RequirePermission(auth.PermWebhooksManage, http.HandlerFunc(h.OutboundWebhookDeliveries)))

	// REST API endpoint management routes (per-agent)
	protected.Handle("GET /agents/{id}/rest-api", h.RequireAgentPermission(auth.PermAgentEdit, http.HandlerFunc(h.RESTAPIEndpointList)))
	protected.Handle("POST /agents/{id}/rest-api", h.RequireAgentPermission(auth.PermAgentEdit, http.HandlerFunc(h.RESTAPIEndpointCreate)))
	protected.Handle("POST /agents/{id}/rest-api/{name}/edit", h.RequireAgentPermission(auth.PermAgentEdit, http.HandlerFunc(h.RESTAPIEndpointEdit)))
	protected.Handle("POST /agents/{id}/rest-api/{name}/delete", h.RequireAgentPermission(auth.PermAgentEdit, http.HandlerFunc(h.RESTAPIEndpointDelete)))
	protected.Handle("POST /agents/{id}/rest-api/{name}/test", h.RequireAgentPermission(auth.PermAgentEdit, http.HandlerFunc(h.RESTAPIEndpointTest)))

	// Settings routes (replaces /system)
	protected.Handle("GET /settings", h.RequirePermission(auth.PermSystemConfig, http.HandlerFunc(h.SettingsPage)))
	protected.Handle("POST /settings/circuit-breaker", h.RequirePermission(auth.PermSystemConfig, http.HandlerFunc(h.SettingsCircuitBreakerSave)))
	protected.Handle("POST /settings/system/prune", h.RequirePermission(auth.PermSystemConfig, http.HandlerFunc(h.SystemPruneNow)))
	// Obsidian vault settings routes
	protected.Handle("POST /settings/obsidian-vaults/create", h.RequirePermission(auth.PermSystemConfig, http.HandlerFunc(h.ObsidianVaultCreate)))
	protected.Handle("POST /settings/obsidian-vaults/{id}/update", h.RequirePermission(auth.PermSystemConfig, http.HandlerFunc(h.ObsidianVaultUpdate)))
	protected.Handle("POST /settings/obsidian-vaults/{id}/delete", h.RequirePermission(auth.PermSystemConfig, http.HandlerFunc(h.ObsidianVaultDelete)))
	// Keep old POST /system/prune for backwards compat
	protected.Handle("POST /system/prune", h.RequirePermission(auth.PermSystemConfig, http.HandlerFunc(h.SystemPruneNow)))
	// Redirect old /system URL to /settings?tab=system
	protected.Handle("GET /system", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/settings?tab=system", http.StatusMovedPermanently)
	}))

	// Backup routes
	protected.Handle("GET /backup", h.RequirePermission(auth.PermBackupManage, http.HandlerFunc(h.BackupPage)))
	protected.Handle("POST /backup/now", h.RequirePermission(auth.PermBackupManage, http.HandlerFunc(h.BackupNow)))
	protected.Handle("GET /backup/download/{filename}", h.RequirePermission(auth.PermBackupManage, http.HandlerFunc(h.BackupDownload)))
	protected.Handle("POST /backup/delete/{filename}", h.RequirePermission(auth.PermBackupManage, http.HandlerFunc(h.BackupDelete)))
	protected.Handle("POST /backup/restore", h.RequirePermission(auth.PermBackupManage, http.HandlerFunc(h.BackupRestore)))

	// Skills routes (legacy — list and detail redirect to /capabilities)
	protected.Handle("GET /skills", http.RedirectHandler("/capabilities", http.StatusFound))
	protected.Handle("GET /skills/new", h.RequirePermission(auth.PermSkillsManage, http.HandlerFunc(h.SkillCreateForm)))
	protected.Handle("POST /skills/new", h.RequirePermission(auth.PermSkillsManage, http.HandlerFunc(h.SkillCreatePost)))
	protected.Handle("POST /skills/refresh", h.RequirePermission(auth.PermSkillsManage, http.HandlerFunc(h.SkillRefresh)))
	protected.Handle("GET /skills/{name}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/capabilities/"+r.PathValue("name")+"/install", http.StatusFound)
	}))
	protected.Handle("POST /skills/{name}/grant", h.RequirePermission(auth.PermSkillsManage, http.HandlerFunc(h.SkillGrant)))
	protected.Handle("POST /skills/{name}/revoke", h.RequirePermission(auth.PermSkillsManage, http.HandlerFunc(h.SkillRevoke)))
	protected.Handle("POST /skills/{name}/delete", h.RequirePermission(auth.PermSkillsManage, http.HandlerFunc(h.SkillDeleteLocal)))

	// OAuth2 flow routes
	protected.Handle("GET /oauth/start", h.RequirePermission(auth.PermIntegrationsManage, http.HandlerFunc(h.OAuthStart)))
	protected.Handle("GET /oauth/callback", h.RequirePermission(auth.PermIntegrationsManage, http.HandlerFunc(h.OAuthCallback)))

	// Capabilities routes
	protected.Handle("GET /capabilities", h.RequirePermission(auth.PermCapabilitiesManage, http.HandlerFunc(h.CapabilitiesList)))
	protected.Handle("GET /capabilities/{name}/install", h.RequirePermission(auth.PermCapabilitiesManage, http.HandlerFunc(h.CapabilityInstallPage)))
	protected.Handle("POST /capabilities/{name}/install", h.RequirePermission(auth.PermCapabilitiesManage, http.HandlerFunc(h.CapabilityInstallPost)))
	protected.Handle("GET /agents/{id}/capabilities", h.RequireAgentPermission(auth.PermAgentView, http.HandlerFunc(h.AgentCapabilitiesTab)))

	// Integration routes (legacy — list and detail redirect to /capabilities)
	protected.Handle("GET /integrations", http.RedirectHandler("/capabilities", http.StatusFound))
	protected.Handle("GET /integrations/new", h.RequirePermission(auth.PermIntegrationsManage, http.HandlerFunc(h.IntegrationCreateForm)))
	protected.Handle("POST /integrations/new", h.RequirePermission(auth.PermIntegrationsManage, http.HandlerFunc(h.IntegrationCreatePost)))
	protected.Handle("POST /integrations/refresh", h.RequirePermission(auth.PermIntegrationsManage, http.HandlerFunc(h.IntegrationRefresh)))
	protected.Handle("POST /integrations/{name}/install", h.RequirePermission(auth.PermIntegrationsManage, http.HandlerFunc(h.IntegrationInstall)))
	protected.Handle("POST /integrations/{name}/uninstall", h.RequirePermission(auth.PermIntegrationsManage, http.HandlerFunc(h.IntegrationUninstall)))
	protected.Handle("GET /integrations/{name}/export", h.RequirePermission(auth.PermIntegrationsManage, http.HandlerFunc(h.IntegrationExportYAML)))
	protected.Handle("POST /integrations/{name}/delete", h.RequirePermission(auth.PermIntegrationsManage, http.HandlerFunc(h.IntegrationDeleteLocal)))
	// Native tool install/uninstall (POST only — GET redirect would conflict with {name}/export).
	protected.Handle("POST /integrations/native/{name}/install", h.RequirePermission(auth.PermIntegrationsManage, http.HandlerFunc(h.NativeToolInstall)))
	protected.Handle("POST /integrations/native/{name}/uninstall", h.RequirePermission(auth.PermIntegrationsManage, http.HandlerFunc(h.NativeToolUninstall)))
	// Integration detail redirect — GET /integrations/native/{name} omitted due to mux conflict.
	protected.Handle("GET /integrations/{name}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/capabilities/"+r.PathValue("name")+"/install", http.StatusFound)
	}))
	protected.Handle("GET /agents/{id}/integrations", h.RequireAgentPermission(auth.PermAgentView, http.HandlerFunc(h.AgentIntegrationsTab)))

	// Setup wizard routes
	protected.HandleFunc("GET /setup", h.SetupWizard)
	protected.HandleFunc("POST /setup/complete", h.SetupComplete)

	// Guide agent routes
	protected.HandleFunc("POST /guide/create", h.CreateGuideAgent)
	protected.HandleFunc("POST /guide/dismiss", h.DismissGuideBanner)

	// Provider management routes
	protected.Handle("GET /providers", h.RequirePermission(auth.PermProvidersManage, http.HandlerFunc(h.ProviderList)))
	protected.Handle("GET /providers/new", h.RequirePermission(auth.PermProvidersManage, http.HandlerFunc(h.ProviderNewForm)))
	protected.Handle("POST /providers", h.RequirePermission(auth.PermProvidersManage, http.HandlerFunc(h.ProviderCreate)))
	protected.Handle("GET /providers/{id}/edit", h.RequirePermission(auth.PermProvidersManage, http.HandlerFunc(h.ProviderEditForm)))
	protected.Handle("POST /providers/{id}/edit", h.RequirePermission(auth.PermProvidersManage, http.HandlerFunc(h.ProviderUpdate)))
	protected.Handle("POST /providers/{id}/delete", h.RequirePermission(auth.PermProvidersManage, http.HandlerFunc(h.ProviderDelete)))
	protected.Handle("POST /providers/{id}/toggle", h.RequirePermission(auth.PermProvidersManage, http.HandlerFunc(h.ProviderToggle)))
	protected.Handle("POST /providers/test", h.RequirePermission(auth.PermProvidersManage, http.HandlerFunc(h.ProviderTestConnection)))
	protected.Handle("GET /providers/{id}/models", h.RequirePermission(auth.PermProvidersManage, http.HandlerFunc(h.ProviderFetchModels)))
	protected.Handle("POST /providers/{id}/models", h.RequirePermission(auth.PermProvidersManage, http.HandlerFunc(h.ProviderSaveModels)))

	// Channel settings routes
	protected.Handle("GET /channels/settings", h.RequirePermission(auth.PermChannelsManage, http.HandlerFunc(h.ChannelSettingsList)))
	protected.Handle("GET /channels/settings/{type}/edit", h.RequirePermission(auth.PermChannelsManage, http.HandlerFunc(h.ChannelSettingsEdit)))
	protected.Handle("POST /channels/settings/{type}", h.RequirePermission(auth.PermChannelsManage, http.HandlerFunc(h.ChannelSettingsSave)))
	protected.Handle("POST /channels/settings/{type}/toggle", h.RequirePermission(auth.PermChannelsManage, http.HandlerFunc(h.ChannelSettingsToggle)))
	protected.Handle("POST /channels/settings/test", h.RequirePermission(auth.PermChannelsManage, http.HandlerFunc(h.ChannelSettingsTest)))

	// Discord auth routes
	protected.HandleFunc("POST /discord-auth/redeem", h.DiscordAuthRedeem)
	protected.Handle("GET /agents/{id}/discord-auth", h.RequireAgentPermission(auth.PermAgentEdit, http.HandlerFunc(h.DiscordAuthPage)))
	protected.Handle("GET /agents/{id}/discord-auth/table", h.RequireAgentPermission(auth.PermAgentEdit, http.HandlerFunc(h.DiscordAuthTableFragment)))
	protected.Handle("POST /agents/{id}/discord-auth/{user_id}/approve", h.RequireAgentPermission(auth.PermAgentEdit, http.HandlerFunc(h.DiscordAuthApprove)))
	protected.Handle("POST /agents/{id}/discord-auth/{user_id}/deny", h.RequireAgentPermission(auth.PermAgentEdit, http.HandlerFunc(h.DiscordAuthDeny)))
	protected.Handle("POST /agents/{id}/discord-auth/{user_id}/delete", h.RequireAgentPermission(auth.PermAgentEdit, http.HandlerFunc(h.DiscordAuthDelete)))
	protected.Handle("POST /agents/{id}/discord-auth/allowlist", h.RequireAgentPermission(auth.PermAgentEdit, http.HandlerFunc(h.DiscordAuthAllowlist)))

	// Cluster management routes
	protected.Handle("GET /cluster", h.RequirePermission(auth.PermSystemConfig, http.HandlerFunc(h.ClusterList)))
	protected.Handle("POST /cluster/{nodeID}/drain", h.RequirePermission(auth.PermSystemConfig, http.HandlerFunc(h.ClusterDrain)))
	protected.Handle("POST /agents/{id}/migrate", h.RequireAgentPermission(auth.PermAgentEdit, http.HandlerFunc(h.ClusterMigrateAgent)))

	// Teams routes
	protected.Handle("GET /teams", h.RequirePermission(auth.PermTeamsView, http.HandlerFunc(h.TeamsList)))
	protected.Handle("GET /teams/new", h.RequirePermission(auth.PermTeamsManage, http.HandlerFunc(h.TeamCreateForm)))
	protected.Handle("POST /teams", h.RequirePermission(auth.PermTeamsManage, http.HandlerFunc(h.TeamCreatePost)))
	protected.Handle("GET /teams/{id}", h.RequirePermission(auth.PermTeamsView, http.HandlerFunc(h.TeamDetail)))
	protected.Handle("GET /teams/{id}/edit", h.RequirePermission(auth.PermTeamsManage, http.HandlerFunc(h.TeamEdit)))
	protected.Handle("POST /teams/{id}/edit", h.RequirePermission(auth.PermTeamsManage, http.HandlerFunc(h.TeamEditPost)))
	protected.Handle("POST /teams/{id}/pause", h.RequirePermission(auth.PermTeamsManage, http.HandlerFunc(h.TeamPause)))
	protected.Handle("POST /teams/{id}/resume", h.RequirePermission(auth.PermTeamsManage, http.HandlerFunc(h.TeamResume)))
	protected.Handle("POST /teams/{id}/delete", h.RequirePermission(auth.PermTeamsManage, http.HandlerFunc(h.TeamDelete)))
	protected.Handle("POST /teams/{id}/context", h.RequirePermission(auth.PermTeamsManage, http.HandlerFunc(h.TeamSharedContext)))
	protected.Handle("GET /teams/{id}/messages", h.RequirePermission(auth.PermTeamsView, http.HandlerFunc(h.TeamMessages)))

	// Team queue visibility routes
	protected.Handle("GET /teams/{id}/queues", h.RequirePermission(auth.PermTeamsView, http.HandlerFunc(h.TeamQueues)))
	protected.Handle("GET /teams/{id}/queues/{agentID}", h.RequirePermission(auth.PermTeamsView, http.HandlerFunc(h.TeamAgentQueue)))
	protected.Handle("POST /teams/{id}/queues/{msgID}/retry", h.RequirePermission(auth.PermTeamsManage, http.HandlerFunc(h.TeamQueueRetry)))
	protected.Handle("POST /teams/{id}/queues/{msgID}/delete", h.RequirePermission(auth.PermTeamsManage, http.HandlerFunc(h.TeamQueueDelete)))
	protected.Handle("GET /teams/{id}/queues/message/{msgID}", h.RequirePermission(auth.PermTeamsView, http.HandlerFunc(h.TeamQueueMessageDetail)))

	// Paired conversation routes
	protected.Handle("GET /paired", h.RequirePermission(auth.PermTeamsView, http.HandlerFunc(h.PairedList)))
	protected.Handle("GET /paired/new", h.RequirePermission(auth.PermTeamsManage, http.HandlerFunc(h.PairedLaunchForm)))
	protected.Handle("POST /paired", h.RequirePermission(auth.PermTeamsManage, http.HandlerFunc(h.PairedLaunch)))
	protected.Handle("GET /paired/{id}", h.RequirePermission(auth.PermTeamsView, http.HandlerFunc(h.PairedView)))
	protected.Handle("POST /paired/{id}/pause", h.RequirePermission(auth.PermTeamsManage, http.HandlerFunc(h.PairedPause)))
	protected.Handle("POST /paired/{id}/resume", h.RequirePermission(auth.PermTeamsManage, http.HandlerFunc(h.PairedResume)))
	protected.Handle("POST /paired/{id}/stop", h.RequirePermission(auth.PermTeamsManage, http.HandlerFunc(h.PairedStop)))
	protected.Handle("POST /paired/{id}/inject", h.RequirePermission(auth.PermTeamsManage, http.HandlerFunc(h.PairedInject)))
	protected.Handle("POST /paired/{id}/delete", h.RequirePermission(auth.PermTeamsManage, http.HandlerFunc(h.PairedDelete)))
	protected.Handle("POST /paired/{id}/continue", h.RequirePermission(auth.PermTeamsManage, http.HandlerFunc(h.PairedContinue)))
	protected.Handle("POST /paired/{id}/agents", h.RequirePermission(auth.PermTeamsManage, http.HandlerFunc(h.PairedEditAgents)))
	protected.Handle("GET /paired/{id}/messages", h.RequirePermission(auth.PermTeamsView, http.HandlerFunc(h.PairedMessages)))
	protected.Handle("GET /paired/{id}/stream", h.RequirePermission(auth.PermTeamsView, http.HandlerFunc(h.PairedSSE)))

	// Spending dashboard routes
	protected.Handle("GET /spending", h.RequirePermission(auth.PermSpendingView, http.HandlerFunc(h.SpendingPage)))
	protected.Handle("GET /spending/charts", h.RequirePermission(auth.PermSpendingView, http.HandlerFunc(h.SpendingChartData)))
	protected.Handle("GET /spending/export", h.RequirePermission(auth.PermSpendingView, http.HandlerFunc(h.SpendingExportCSV)))
	protected.Handle("POST /spending/limits/{agent_id}", h.RequirePermission(auth.PermSpendingAdjust, http.HandlerFunc(h.SpendingUpdateLimit)))
	protected.Handle("GET /spending/providers/{provider}", h.RequirePermission(auth.PermSpendingView, http.HandlerFunc(h.SpendingProviderDrillDown)))

	// Agent clone + save-as-template
	protected.Handle("GET /agents/{id}/clone", h.RequireAgentPermission(auth.PermAgentCreate, http.HandlerFunc(h.AgentClone)))
	protected.Handle("POST /agents/{id}/save-template", h.RequirePermission(auth.PermTemplatesManage, http.HandlerFunc(h.AgentSaveAsTemplate)))

	// Agent export/import routes
	protected.Handle("POST /agents/{id}/export", h.RequireAgentPermission(auth.PermAgentView, http.HandlerFunc(h.AgentExport)))
	protected.Handle("GET /agents/{id}/workspace/download", h.RequirePermission(auth.PermAgentView, http.HandlerFunc(h.WorkspaceDownload)))
	protected.Handle("POST /agents/import", h.RequirePermission(auth.PermAgentCreate, http.HandlerFunc(h.AgentImport)))

	// Template management routes
	protected.Handle("GET /templates", h.RequirePermission(auth.PermTemplatesManage, http.HandlerFunc(h.TemplatesList)))
	protected.Handle("POST /templates", h.RequirePermission(auth.PermTemplatesManage, http.HandlerFunc(h.TemplateCreate)))
	protected.Handle("GET /templates/{id}/edit", h.RequirePermission(auth.PermTemplatesManage, http.HandlerFunc(h.TemplateEdit)))
	protected.Handle("POST /templates/{id}/edit", h.RequirePermission(auth.PermTemplatesManage, http.HandlerFunc(h.TemplateEditPost)))
	protected.Handle("POST /templates/{id}/delete", h.RequirePermission(auth.PermTemplatesManage, http.HandlerFunc(h.TemplateDelete)))
	protected.Handle("GET /templates/{id}/prefill", h.RequirePermission(auth.PermAgentCreate, http.HandlerFunc(h.TemplatePrefill)))

	// Audit log routes
	protected.Handle("GET /audit", h.RequirePermission(auth.PermAuditView, http.HandlerFunc(h.AuditPage)))
	protected.Handle("GET /audit/entries", h.RequirePermission(auth.PermAuditView, http.HandlerFunc(h.AuditEntriesFragment)))
	protected.Handle("GET /audit/stream", h.RequirePermission(auth.PermAuditView, http.HandlerFunc(h.AuditStreamSSE)))
	protected.Handle("GET /audit/{entryID}", h.RequirePermission(auth.PermAuditView, http.HandlerFunc(h.AuditEntryDetail)))

	// User/group management routes
	protected.Handle("GET /users", h.RequirePermission(auth.PermUsersManage, http.HandlerFunc(h.UsersPage)))
	protected.Handle("POST /users", h.RequirePermission(auth.PermUsersManage, http.HandlerFunc(h.UsersCreate)))
	protected.Handle("POST /users/{id}/edit", h.RequirePermission(auth.PermUsersManage, http.HandlerFunc(h.UsersUpdate)))
	protected.Handle("POST /users/{id}/reset-password", h.RequirePermission(auth.PermUsersManage, http.HandlerFunc(h.UsersResetPassword)))
	protected.Handle("POST /users/{id}/delete", h.RequirePermission(auth.PermUsersManage, http.HandlerFunc(h.UsersDelete)))
	protected.Handle("GET /groups", h.RequirePermission(auth.PermUsersManage, http.HandlerFunc(h.GroupsPage)))
	protected.Handle("POST /groups", h.RequirePermission(auth.PermUsersManage, http.HandlerFunc(h.GroupsCreate)))
	protected.Handle("POST /groups/{id}/edit", h.RequirePermission(auth.PermUsersManage, http.HandlerFunc(h.GroupsUpdate)))
	protected.Handle("POST /groups/{id}/delete", h.RequirePermission(auth.PermUsersManage, http.HandlerFunc(h.GroupsDelete)))
	protected.Handle("POST /groups/{id}/agents", h.RequirePermission(auth.PermUsersManage, http.HandlerFunc(h.GroupAddAgent)))
	protected.Handle("POST /groups/{id}/agents/{agentID}/delete", h.RequirePermission(auth.PermUsersManage, http.HandlerFunc(h.GroupRemoveAgent)))
	protected.Handle("POST /groups/{id}/users", h.RequirePermission(auth.PermUsersManage, http.HandlerFunc(h.GroupSetUserRole)))
	protected.Handle("POST /groups/{id}/users/{userID}/delete", h.RequirePermission(auth.PermUsersManage, http.HandlerFunc(h.GroupRemoveUserRole)))

	// Mount managed API (Sett sync) before the catch-all.
	if cfg.managedAPI != nil {
		mux.Handle("/api/managed/", http.StripPrefix("/api/managed", cfg.managedAPI.Routes()))
	}

	// Mount REST API before the catch-all.
	if cfg.apiKeys != nil {
		apiHandler := webapi.New(kyvik, cfg.apiKeys)
		mux.Handle("/api/v1/", http.StripPrefix("/api/v1", apiHandler.Routes()))
	}

	// Inbound webhooks — public, no auth (secret in URL path).
	if cfg.webhooks != nil {
		mux.Handle("POST /webhooks/{agent_id}/{webhook_secret}", cfg.webhooks)
	}

	// NOTE: RequireFormContentType middleware is NOT applied globally because
	// AgentMemoriesImport (POST /agents/{id}/memories/import) accepts JSON bodies
	// for bulk memory imports. If that endpoint is refactored to only accept form
	// data, the global middleware can be added by wrapping protected:
	// handlers.RequireFormContentType(protected)
	mux.Handle("/", h.RequireAuth(h.SetupCheck(protected)))

	return handlers.SecurityHeaders(handlers.RequestID(mux))
}
