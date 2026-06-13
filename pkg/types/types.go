// Package types defines the shared types used across Kyvik.
package types

import "time"

// AgentStatus represents the current state of an agent.
type AgentStatus string

const (
	AgentStatusStopped     AgentStatus = "stopped"
	AgentStatusStarting    AgentStatus = "starting"
	AgentStatusRunning     AgentStatus = "running"
	AgentStatusPaused      AgentStatus = "paused"
	AgentStatusError       AgentStatus = "error"
	AgentStatusQuarantined AgentStatus = "quarantined"
	AgentStatusKilled      AgentStatus = "killed"
)

// DesiredState represents the operator-intended state of an agent.
type DesiredState string

const (
	DesiredStateRunning     DesiredState = "running"
	DesiredStateStopped     DesiredState = "stopped"
	DesiredStateQuarantined DesiredState = "quarantined"
	DesiredStateKilled      DesiredState = "killed"
)

// AgentConfig defines everything needed to create and run an agent.
type AgentConfig struct {
	ID                   string                `json:"id"`
	Name                 string                `json:"name"`
	Description          string                `json:"description"`
	SystemPrompt         string                `json:"system_prompt"`
	SoulContent          string                `json:"soul_content"`
	IdentityContent      string                `json:"identity_content"`
	ModelConfig          ModelConfig           `json:"model_config"`
	ModelSlotsJSON       string                `json:"model_slots_json,omitempty"`
	RoutingConfigJSON    string                `json:"routing_config_json,omitempty"`
	Template             string                `json:"template"` // Permission template: reader, worker, admin
	Channels             []ChannelMapping      `json:"channels"`
	Limits               SpendingLimits        `json:"limits"`
	HistoryLimit         int                   `json:"history_limit"`         // Max messages to inject into context (default 50)
	MemoryLimit          int                   `json:"memory_limit"`          // Max memories to inject into context (default 10)
	AttachmentMaxSizeMB  int                   `json:"attachment_max_size_mb,omitempty" db:"attachment_max_size_mb"`
	AutoExtractMemories                bool                  `json:"auto_extract_memories"` // Auto-extract memories from conversations
	MaxMemories                        int                   `json:"max_memories,omitempty" db:"max_memories"`
	MemoryExtractionInterval           int                   `json:"memory_extraction_interval,omitempty" db:"memory_extraction_interval"`
	MemoryMaxExtractionsPerRun         int                   `json:"memory_max_extractions_per_run,omitempty" db:"memory_max_extractions_per_run"`
	MemoryDuplicateThreshold           float32               `json:"memory_duplicate_threshold,omitempty" db:"memory_duplicate_threshold"`
	MemorySimilarThreshold             float32               `json:"memory_similar_threshold,omitempty" db:"memory_similar_threshold"`
	TimestampMessages    bool                  `json:"timestamp_messages"`    // Prepend current time to user messages
	ContextBudget        ContextBudget         `json:"context_budget"`
	Workers              WorkerConfig          `json:"workers"`
	ToolGrants           []string              `json:"tool_grants,omitempty"`
	CapabilityGrants     []Capability          `json:"capability_grants,omitempty"`
	SlackMode            string                `json:"slack_mode"`            // "none", "primary", "dedicated"
	SlackChannel         string                `json:"slack_channel"`         // channel ID for primary mode
	DiscordMode          string                `json:"discord_mode"`          // "none", "primary", "dedicated"
	DiscordChannelID     string                `json:"discord_channel_id"`    // channel ID for primary mode
	DiscordAuthMode      string                `json:"discord_auth_mode"`     // "open" (default) or "restricted"
	WebUIEnabled         bool                  `json:"webui_enabled"`         // default true
	SecurityJSON         string                `json:"security_json,omitempty"`
	CircuitBreakerJSON   string                `json:"circuit_breaker_json,omitempty"`
	HeartbeatJSON        string                `json:"heartbeat_json,omitempty"`
	CompressionJSON      string                `json:"compression_json,omitempty"`
	FeedbackHooksJSON    string                `json:"feedback_hooks_json,omitempty"`
	CanMessage           []string              `json:"can_message,omitempty"`
	TeamID               string                `json:"team_id,omitempty"`
	ProviderIgnore       []string              `json:"provider_ignore,omitempty" db:"-"` // OpenRouter providers to skip (e.g., ["Google"])
	HostPaths            *HostPathConfig       `json:"host_paths,omitempty"`
	HostFilesystem       *HostFilesystemConfig `json:"host_filesystem,omitempty"`
	WebhookInbound       *InboundWebhookConfig `json:"webhook_inbound,omitempty"`
	RESTAPIEndpointsJSON string                `json:"rest_api_endpoints_json,omitempty"`
	HTTPAllowedHosts     []string              `json:"http_allowed_hosts,omitempty"`
	ShellAllowedCommands []string              `json:"shell_allowed_commands,omitempty"`
	IsGuide              bool                  `json:"is_guide"`
	Metadata             map[string]string     `json:"metadata"`
	ObsidianVaults       []string              `json:"obsidian_vaults,omitempty" db:"obsidian_vaults"`
	NodeAffinity         map[string]string     `json:"node_affinity,omitempty" db:"node_affinity"`
	NodePreference       map[string]string     `json:"node_preference,omitempty" db:"node_preference"`
	DesiredState         DesiredState          `json:"desired_state"`
	ActualState          AgentStatus           `json:"actual_state"`
	LastError            string                `json:"last_error,omitempty"`
	CreatedAt            time.Time             `json:"created_at"`
	UpdatedAt            time.Time             `json:"updated_at"`
}

// ModelConfig defines how an agent connects to its LLM.
type ModelConfig struct {
	Provider string `json:"provider"` // e.g., "openrouter", "ollama"
	Model    string `json:"model"`    // e.g., "deepseek/deepseek-chat", "llama3"
	// Future: escalation rules, routing config
}

// ChannelMapping connects an agent to a communication channel.
type ChannelMapping struct {
	ChannelType   string `json:"channel_type"` // e.g., "slack", "webui"
	ChannelID     string `json:"channel_id"`   // e.g., Slack channel ID
	AutoProvision bool   `json:"auto_provision"`
}

// SpendingLimits defines budget constraints for an agent.
type SpendingLimits struct {
	MaxTokensPerDay   int64   `json:"max_tokens_per_day"`
	MaxTokensPerMonth int64   `json:"max_tokens_per_month"`
	MaxSpendPerDay    float64 `json:"max_spend_per_day"`   // USD
	MaxSpendPerMonth  float64 `json:"max_spend_per_month"` // USD
}

// ContextBudget controls how prompt components are sized within a token budget.
// Zero values mean "use defaults" (8000 total, 15/10/25/50 split).
type ContextBudget struct {
	MaxTotalTokens  int `json:"max_total_tokens"`
	SoulIdentityPct int `json:"soul_identity_pct"`
	SkillsPct       int `json:"skills_pct"`
	MemoriesPct     int `json:"memories_pct"`
	HistoryPct      int `json:"history_pct"`
}

// WorkerConfig controls ephemeral worker spawning for task delegation.
type WorkerConfig struct {
	Enabled       bool   `json:"enabled"`
	MaxConcurrent int    `json:"max_concurrent"`
	TTLSeconds    int    `json:"ttl_seconds"`
	ModelSlot     string `json:"model_slot"`
}

// Slack mode constants.
const (
	SlackModeNone      = "none"
	SlackModePrimary   = "primary"
	SlackModeDedicated = "dedicated"
)

// Discord mode constants.
const (
	DiscordModeNone      = "none"
	DiscordModePrimary   = "primary"
	DiscordModeDedicated = "dedicated"
)

// Discord auth mode constants.
const (
	DiscordAuthModeOpen       = "open"       // No auth check (default, backward compatible)
	DiscordAuthModeRestricted = "restricted" // Requires pairing or allowlist
)

// Discord authorization status constants.
const (
	DiscordAuthStatusPending  = "pending"
	DiscordAuthStatusApproved = "approved"
	DiscordAuthStatusDenied   = "denied"
)

// Discord authorization added-by constants.
const (
	DiscordAuthAddedByPairing   = "pairing"
	DiscordAuthAddedByAllowlist = "allowlist"
)

// DiscordAuthorization represents a Discord user's authorization status for an agent.
type DiscordAuthorization struct {
	ID            string     `json:"id"`
	AgentID       string     `json:"agent_id"`
	DiscordUserID string     `json:"discord_user_id"`
	Status        string     `json:"status"`       // "pending", "approved", "denied"
	PairingCode   string     `json:"pairing_code"` // 6-char code for pairing flow
	AddedBy       string     `json:"added_by"`     // "pairing" or "allowlist"
	CodeExpiresAt *time.Time `json:"code_expires_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// Default context budget values.
const (
	DefaultContextMaxTotalTokens  = 32000
	DefaultContextSoulIdentityPct = 15
	DefaultContextSkillsPct       = 10
	DefaultContextMemoriesPct     = 25
	DefaultContextHistoryPct      = 50
)

// NormalizeContextBudget fills zero or negative fields with defaults.
func NormalizeContextBudget(b ContextBudget) ContextBudget {
	if b.MaxTotalTokens <= 0 {
		b.MaxTotalTokens = DefaultContextMaxTotalTokens
	}
	if b.SoulIdentityPct <= 0 {
		b.SoulIdentityPct = DefaultContextSoulIdentityPct
	}
	if b.SkillsPct <= 0 {
		b.SkillsPct = DefaultContextSkillsPct
	}
	if b.MemoriesPct <= 0 {
		b.MemoriesPct = DefaultContextMemoriesPct
	}
	if b.HistoryPct <= 0 {
		b.HistoryPct = DefaultContextHistoryPct
	}
	return b
}

// Default worker config values.
const (
	DefaultWorkerMaxConcurrent = 3
	DefaultWorkerTTLSeconds    = 300
	DefaultWorkerModelSlot     = "fast"
)

// NormalizeWorkerConfig fills zero fields with defaults when workers are enabled.
func NormalizeWorkerConfig(w WorkerConfig) WorkerConfig {
	if !w.Enabled {
		return w
	}
	if w.MaxConcurrent <= 0 {
		w.MaxConcurrent = DefaultWorkerMaxConcurrent
	}
	if w.TTLSeconds <= 0 {
		w.TTLSeconds = DefaultWorkerTTLSeconds
	}
	if w.ModelSlot == "" {
		w.ModelSlot = DefaultWorkerModelSlot
	}
	return w
}

// Attachment represents a file attached to a message.
type Attachment struct {
	Filename      string `json:"filename"`
	ContentType   string `json:"content_type"`
	Size          int64  `json:"size"`
	Data          []byte `json:"data,omitempty"`          // raw file bytes; omitted in history
	ExtractedText string `json:"extracted_text,omitempty"` // text content from server-side extraction
}

// Attachment size and count limits.
const (
	MaxAttachmentSize    = 25 << 20 // 25 MB per file
	MaxAttachmentsPerMsg = 5
)

// Message represents a message flowing through Kyvik.
type Message struct {
	ID             string       `json:"id"`
	AgentID        string       `json:"agent_id"`
	Channel        string       `json:"channel"`
	ConversationID string       `json:"conversation_id,omitempty"`
	Sender         string       `json:"sender,omitempty"` // original sender (agent ID for internal, user ID for external)
	Role           string       `json:"role"`             // "user", "assistant", "system"
	Content        string       `json:"content"`
	Attachments    []Attachment      `json:"attachments,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	Timestamp      time.Time         `json:"timestamp"`
}

// User represents a dashboard account.
type User struct {
	ID                  string     `json:"id"`
	Username            string     `json:"username"`
	PasswordHash        string     `json:"password_hash,omitempty"`
	DisplayName         string     `json:"display_name"`
	IsAdmin             bool       `json:"is_admin"`
	IsActive            bool       `json:"is_active"`
	CreatedAt           time.Time  `json:"created_at"`
	LastLoginAt         *time.Time `json:"last_login_at,omitempty"`
	ForcePasswordChange bool       `json:"force_password_change"`
}

// AgentGroup scopes users to sets of agents.
type AgentGroup struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}

// UserGroupRole maps a user to a role within a specific group.
type UserGroupRole struct {
	UserID  string `json:"user_id"`
	GroupID string `json:"group_id"`
	Role    string `json:"role"`
}

// UserSession represents an authenticated dashboard session.
type UserSession struct {
	ID         string     `json:"id"`
	UserID     string     `json:"user_id"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
	IPAddress  string     `json:"ip_address"`
	UserAgent  string     `json:"user_agent"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// ToolCall represents a request from an agent to execute a tool.
type ToolCall struct {
	ID         string                 `json:"id"`
	AgentID    string                 `json:"agent_id"`
	ToolName   string                 `json:"tool_name"`
	Action     string                 `json:"action"`
	Parameters map[string]interface{} `json:"parameters"`
	Timestamp  time.Time              `json:"timestamp"`
}

// ToolResult represents the outcome of a tool execution.
type ToolResult struct {
	CallID    string    `json:"call_id"`
	Success   bool      `json:"success"`
	Output    string    `json:"output"`
	Error     string    `json:"error,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// Capability represents a specific action an agent might perform.
// Used by the permission gate and tool declarations.
type Capability struct {
	Tool     string `json:"tool"`     // Tool name (e.g., "filesystem", "http", "database")
	Action   string `json:"action"`   // Action on the tool (e.g., "read", "write", "delete", "execute")
	Resource string `json:"resource"` // Resource scope (e.g., "/data/*", "https://api.example.com/*")
}

// EventType categorizes audit events.
type EventType string

const (
	EventToolCall        EventType = "tool_call"
	EventPermission      EventType = "permission_check"
	EventModelRequest    EventType = "model_request"
	EventAgentLifecycle  EventType = "agent_lifecycle"
	EventSpending        EventType = "spending"
	EventConfigChange    EventType = "config_change"
	EventAuth            EventType = "auth"
	EventSecret          EventType = "secret"
	EventInternalMessage EventType = "internal_message"
	EventWebhook         EventType = "webhook"
)

// AuditEntry records an action for the audit trail.
type AuditEntry struct {
	ID        string    `json:"id"`
	AgentID   string    `json:"agent_id"`
	EventType EventType `json:"event_type"`
	Action    string    `json:"action"`
	Resource  string    `json:"resource"`
	Decision  string    `json:"decision"`             // "allowed", "denied"
	RiskLevel string    `json:"risk_level,omitempty"` // "standard", "elevated", "critical"
	Details   string    `json:"details"`
	Timestamp time.Time `json:"timestamp"`
}

// SecurityConfig controls the prompt injection defense layers for an agent.
type SecurityConfig struct {
	SanitizeExternalContent     bool   `json:"sanitize_external_content"`
	ContentBoundaries           bool   `json:"content_boundaries"`
	IdentityReinforcement       bool   `json:"identity_reinforcement"`
	CanaryTokens                bool   `json:"canary_tokens"`
	OutputValidation            bool   `json:"output_validation"`
	AnomalyDetectionSensitivity string `json:"anomaly_detection_sensitivity"`
}

// DefaultSecurityConfig returns a SecurityConfig with all defenses enabled.
func DefaultSecurityConfig() SecurityConfig {
	return SecurityConfig{
		SanitizeExternalContent:     true,
		ContentBoundaries:           true,
		IdentityReinforcement:       true,
		CanaryTokens:                true,
		OutputValidation:            true,
		AnomalyDetectionSensitivity: "medium",
	}
}

// SecurityEvent records a security-relevant occurrence for an agent.
type SecurityEvent struct {
	ID        string    `json:"id"`
	AgentID   string    `json:"agent_id"`
	EventType string    `json:"event_type"` // "injection_detected", "canary_leaked", "destructive_pattern", "exfiltration_attempt"
	Severity  string    `json:"severity"`   // "info", "warning", "critical"
	Details   string    `json:"details"`
	CreatedAt time.Time `json:"created_at"`
}

// HostPathConfig defines allowed and denied host filesystem paths for power-tier agents.
type HostPathConfig struct {
	Read  []string `json:"read,omitempty"`  // Absolute paths allowed for reading
	Write []string `json:"write,omitempty"` // Absolute paths allowed for writing
	Deny  []string `json:"deny,omitempty"`  // Absolute paths always denied (takes precedence)
}

// HostFilesystemConfig defines allowlisted host filesystem paths for the HostFS tool.
type HostFilesystemConfig struct {
	Allowlist []HostFilesystemAllowlistEntry `json:"allowlist,omitempty"`
}

// HostFilesystemAllowlistEntry defines a single host path allowlist entry.
type HostFilesystemAllowlistEntry struct {
	Path   string `json:"path"`   // Absolute path or prefix
	Access string `json:"access"` // "read" or "write"
}

// InboundWebhookConfig configures per-agent inbound webhook behavior.
// The secret is stored in the secrets vault, not here.
type InboundWebhookConfig struct {
	Enabled           bool     `json:"enabled"`
	AllowedSources    []string `json:"allowed_sources,omitempty"` // IP allowlist; empty = allow all
	RateLimit         int      `json:"rate_limit"`                // max requests per minute; 0 = use default (60)
	TransformTemplate string   `json:"transform_template,omitempty"`
	SignatureHeader   string   `json:"signature_header,omitempty"`  // e.g. "X-Hub-Signature-256"; empty = skip HMAC
	MaxPayloadBytes   int64    `json:"max_payload_bytes,omitempty"` // 0 = use default (1 MiB)
}

// RESTAPIEndpoint defines a pre-configured REST API endpoint that agents can call.
type RESTAPIEndpoint struct {
	Name              string            `json:"name"`
	Description       string            `json:"description,omitempty"`
	Method            string            `json:"method"`                          // GET, POST, PUT, PATCH, DELETE
	URL               string            `json:"url"`                             // Go template
	Headers           map[string]string `json:"headers,omitempty"`               // Go templates in values
	QueryParams       map[string]string `json:"query_params,omitempty"`          // Go templates in values
	BodyTemplate      string            `json:"body_template,omitempty"`         // Go template for request body
	Auth              RESTAPIAuth       `json:"auth"`
	ResponseTemplate  string            `json:"response_template,omitempty"`     // Go template for response formatting
	CacheTTLSeconds   int               `json:"cache_ttl_seconds,omitempty"`     // 0 = no cache
	RateLimitRPM      int               `json:"rate_limit_rpm,omitempty"`        // 0 = no limit
	TimeoutSeconds    int               `json:"timeout_seconds,omitempty"`       // 0 = default 30s
	IntegrationSource string            `json:"integration_source,omitempty"`    // Name of integration template that installed this endpoint
	Parameters        []EndpointParam   `json:"parameters,omitempty"`            // Declared parameters for LLM discoverability
}

// EndpointParam declares a parameter that an endpoint accepts.
type EndpointParam struct {
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	Required    bool   `json:"required" yaml:"required"`
	Type        string `json:"type,omitempty" yaml:"type,omitempty"`       // default "string"
	Default     string `json:"default,omitempty" yaml:"default,omitempty"`
	Example     string `json:"example,omitempty" yaml:"example,omitempty"`
}

// RESTAPIAuth configures authentication for a REST API endpoint.
type RESTAPIAuth struct {
	Type            string `json:"type"`                        // none, basic, bearer, api_key, custom_header, oauth2
	SecretRef       string `json:"secret_ref,omitempty"`        // Vault key (cascading: agent → team → global)
	HeaderName      string `json:"header_name,omitempty"`       // For api_key/custom_header
	ParamName       string `json:"param_name,omitempty"`        // For api_key (query param placement)
	ClientIDRef     string `json:"client_id_ref,omitempty"`     // OAuth2: vault key for client ID
	ClientSecretRef string `json:"client_secret_ref,omitempty"` // OAuth2: vault key for client secret
	AuthURL         string `json:"auth_url,omitempty"`          // OAuth2: authorization endpoint
	TokenURL        string `json:"token_url,omitempty"`         // OAuth2: token endpoint
	Scopes          string `json:"scopes,omitempty"`            // OAuth2: space-separated scopes
	RefreshTokenRef string `json:"refresh_token_ref,omitempty"` // OAuth2: vault key for refresh token
	AccessTokenRef  string `json:"access_token_ref,omitempty"`  // OAuth2: vault key for cached access token
}

// DatabaseConnection defines a pre-configured database connection that agents can use.
type DatabaseConnection struct {
	Name           string `json:"name" yaml:"name"`
	Description    string `json:"description,omitempty" yaml:"description,omitempty"`
	Driver         string `json:"driver" yaml:"driver"`                                       // "postgres", "mysql", "sqlite", "sqlserver"
	Host           string `json:"host,omitempty" yaml:"host,omitempty"`
	Port           int    `json:"port,omitempty" yaml:"port,omitempty"`
	Database       string `json:"database" yaml:"database"`
	SSLMode        string `json:"ssl_mode,omitempty" yaml:"ssl_mode,omitempty"`
	UsernameRef    string `json:"username_ref,omitempty" yaml:"username_ref,omitempty"`        // vault key
	PasswordRef    string `json:"password_ref,omitempty" yaml:"password_ref,omitempty"`        // vault key
	MaxRows        int    `json:"max_rows,omitempty" yaml:"max_rows,omitempty"`                // per-connection row limit
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" yaml:"timeout_seconds,omitempty"`
	ReadOnly       bool   `json:"read_only,omitempty" yaml:"read_only,omitempty"`
}

// IntegrationCategory classifies an integration.
type IntegrationCategory string

const (
	IntCatCommunication     IntegrationCategory = "communication"
	IntCatProjectManagement IntegrationCategory = "project-management"
	IntCatDeveloperTools    IntegrationCategory = "developer-tools"
	IntCatKnowledge         IntegrationCategory = "knowledge"
	IntCatCalendar          IntegrationCategory = "calendar"
	IntCatCRM               IntegrationCategory = "crm"
	IntCatFinance           IntegrationCategory = "finance"
	IntCatPersonal          IntegrationCategory = "personal"
	IntCatData              IntegrationCategory = "data"
	IntCatSecurity          IntegrationCategory = "security"
	IntCatMonitoring        IntegrationCategory = "monitoring"
)

// IntegrationInstall records that an integration template was installed to an agent.
type IntegrationInstall struct {
	ID              string    `json:"id"`
	AgentID         string    `json:"agent_id"`
	IntegrationName string    `json:"integration_name"`
	Version         string    `json:"version"`
	Variables       string    `json:"variables,omitempty"` // JSON of user-supplied variables
	InstalledAt     time.Time `json:"installed_at"`
	InstalledBy     string    `json:"installed_by"`
}

// OutboundWebhook defines a webhook that fires HTTP POST notifications
// to an external endpoint when matching system events occur.
type OutboundWebhook struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	URL             string            `json:"url"`
	AgentID         string            `json:"agent_id,omitempty"` // empty = global
	Events          []string          `json:"events"`             // glob patterns: ["*"], ["circuit_breaker.*"]
	SecretRef       string            `json:"secret_ref,omitempty"`
	Headers         map[string]string `json:"headers,omitempty"`
	PayloadTemplate string            `json:"payload_template,omitempty"`
	MaxRetries      int               `json:"max_retries"`
	BackoffSeconds  []int             `json:"backoff_seconds"`
	CBThreshold     int               `json:"cb_threshold"`
	CBCooldownSecs  int               `json:"cb_cooldown_secs"`
	Enabled         bool              `json:"enabled"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
}

// WebhookDeliveryStatus represents the outcome of a webhook delivery attempt.
type WebhookDeliveryStatus string

const (
	DeliveryStatusSuccess      WebhookDeliveryStatus = "success"
	DeliveryStatusFailed       WebhookDeliveryStatus = "failed"
	DeliveryStatusPendingRetry WebhookDeliveryStatus = "pending_retry"
)

// WebhookDelivery records an individual delivery attempt for an outbound webhook.
type WebhookDelivery struct {
	ID            string                `json:"id"`
	WebhookID     string                `json:"webhook_id"`
	EventType     string                `json:"event_type"`
	Payload       string                `json:"payload,omitempty"`
	Status        WebhookDeliveryStatus `json:"status"`
	HTTPCode      int                   `json:"http_code"`
	ResponseBody  string                `json:"response_body,omitempty"`
	DurationMs    int                   `json:"duration_ms"`
	RetryCount    int                   `json:"retry_count"`
	NextRetryAt   *time.Time            `json:"next_retry_at,omitempty"`
	ErrorMessage  string                `json:"error_message,omitempty"`
	PayloadSha256 string                `json:"payload_sha256,omitempty"`
	CreatedAt     time.Time             `json:"created_at"`
}

// Default outbound webhook settings.
const (
	DefaultWebhookMaxRetries    = 3
	DefaultWebhookCBThreshold   = 10
	DefaultWebhookCBCooldownSec = 3600
)

// DefaultWebhookBackoff defines the default retry backoff intervals in seconds.
var DefaultWebhookBackoff = []int{5, 30, 120}

// CircuitBreakerConfig controls automated agent quarantine thresholds.
type CircuitBreakerConfig struct {
	Enabled               bool `json:"enabled"`
	ErrorThreshold        int  `json:"error_threshold"`         // Errors within window to trip (default 5)
	ErrorWindowMinutes    int  `json:"error_window_minutes"`    // Sliding window for errors (default 10)
	SpendingVelocityPct   int  `json:"spending_velocity_pct"`   // % of daily budget in window to trip (default 50)
	SpendingWindowMinutes int  `json:"spending_window_minutes"` // Sliding window for spending (default 5)
	ActionRatePerMinute   int  `json:"action_rate_per_minute"`  // Tool calls per minute to trip (default 30)
	DestructiveLimit      int  `json:"destructive_limit"`       // Destructive calls per session to trip (default 5)
	LoopIdenticalCount    int  `json:"loop_identical_count"`    // Identical messages in a row to trip (default 3)
}

// DefaultCircuitBreakerConfig returns a CircuitBreakerConfig with sensible defaults.
func DefaultCircuitBreakerConfig() CircuitBreakerConfig {
	return CircuitBreakerConfig{
		Enabled:               true,
		ErrorThreshold:        5,
		ErrorWindowMinutes:    10,
		SpendingVelocityPct:   50,
		SpendingWindowMinutes: 5,
		ActionRatePerMinute:   30,
		DestructiveLimit:      5,
		LoopIdenticalCount:    3,
	}
}

// Schedule represents a cron-triggered message injection for an agent.
type Schedule struct {
	ID        string     `json:"id"`
	AgentID   string     `json:"agent_id"`
	Name      string     `json:"name"`
	CronExpr  string     `json:"cron_expr"`
	Message   string     `json:"message"`
	Channel   string     `json:"channel"`
	Type      string     `json:"type"` // "task" or "heartbeat"
	Enabled   bool       `json:"enabled"`
	Timezone  string     `json:"timezone"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	LastRunAt *time.Time `json:"last_run_at,omitempty"`
	NextRunAt *time.Time `json:"next_run_at,omitempty"`
}

const (
	ScheduleTypeTask      = "task"
	ScheduleTypeHeartbeat = "heartbeat"
)

// HeartbeatConfig controls agent periodic self-check behavior.
type HeartbeatConfig struct {
	Enabled    bool   `json:"enabled"`
	Interval   string `json:"interval"`
	Prompt     string `json:"prompt"`
	QuietHours string `json:"quiet_hours"`
}

// CompressionConfig controls conversation compression behavior.
type CompressionConfig struct {
	Enabled            bool   `json:"enabled"`
	Model              string `json:"model,omitempty"`
	MessageThreshold   int    `json:"message_threshold"`
	TokenThresholdPct  int    `json:"token_threshold_pct"`
	KeepRecentMessages int    `json:"keep_recent_messages"`
}

// NormalizeCompressionConfig returns a copy with zero values replaced by defaults.
func NormalizeCompressionConfig(c CompressionConfig) CompressionConfig {
	if c.MessageThreshold == 0 {
		c.MessageThreshold = 20
	}
	if c.TokenThresholdPct == 0 {
		c.TokenThresholdPct = 70
	}
	if c.KeepRecentMessages == 0 {
		c.KeepRecentMessages = 10
	}
	return c
}

// CompressionRecord tracks a single compression event for observability.
type CompressionRecord struct {
	ID                string    `json:"id"`
	AgentID           string    `json:"agent_id"`
	Channel           string    `json:"channel"`
	ChannelID         string    `json:"channel_id"`
	MessagesInput     int       `json:"messages_input"`
	TokensInput       int       `json:"tokens_input"`
	TokensOutput      int       `json:"tokens_output"`
	TokensSummarize   int       `json:"tokens_summarize"`
	Model             string    `json:"model"`
	PreviousSummaryID int64     `json:"previous_summary_id,omitempty"`
	DurationMs        int64     `json:"duration_ms"`
	CreatedAt         time.Time `json:"created_at"`
}

// Workflow defines a reusable deterministic tool-chain pipeline.
type Workflow struct {
	ID          string         `json:"id"`
	AgentID     string         `json:"agent_id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Steps       []WorkflowStep `json:"steps"`
	Enabled     bool           `json:"enabled"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

// WorkflowStep defines a single step in a workflow.
type WorkflowStep struct {
	Name    string         `json:"name"`
	Tool    string         `json:"tool"`
	Action  string         `json:"action"`
	Params  map[string]any `json:"params"`
	SaveAs  string         `json:"save_as,omitempty"`
	OnError string         `json:"on_error,omitempty"` // "stop" (default) or "continue"
}

// WorkflowRun tracks a single execution of a workflow.
type WorkflowRun struct {
	ID            string     `json:"id"`
	WorkflowID    string     `json:"workflow_id"`
	AgentID       string     `json:"agent_id"`
	Status        string     `json:"status"`             // "running", "completed", "failed"
	StepsJSON     string     `json:"steps_json"`         // JSON array of WorkflowStepResult
	InputVarsJSON string     `json:"input_vars_json"`    // JSON object of input variables
	Error         string     `json:"error,omitempty"`
	DurationMs    int64      `json:"duration_ms"`
	StartedAt     time.Time  `json:"started_at"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
}

// WorkflowStepResult records the outcome of a single workflow step execution.
type WorkflowStepResult struct {
	Name       string `json:"name"`
	Tool       string `json:"tool"`
	Action     string `json:"action"`
	Success    bool   `json:"success"`
	Result     any    `json:"result,omitempty"`
	Error      string `json:"error,omitempty"`
	DurationMs int64  `json:"duration_ms"`
	Skipped    bool   `json:"skipped,omitempty"`
}

// FeedbackHook defines a single post-action feedback hook.
type FeedbackHook struct {
	After      string `json:"after"`                // "tool.action" pattern, e.g. "file.edit", "file.*", "*.*"
	Match      string `json:"match,omitempty"`       // glob on "path" parameter, e.g. "*.go", "*.yaml"
	Run        string `json:"run"`                  // shell command to execute
	TimeoutSec int    `json:"timeout_sec,omitempty"` // per-hook timeout, default 10
}

// FeedbackHooksConfig holds all feedback hooks for an agent.
type FeedbackHooksConfig struct {
	Enabled        bool           `json:"enabled"`
	Hooks          []FeedbackHook `json:"hooks"`
	MaxOutputBytes int            `json:"max_output_bytes,omitempty"` // per-hook output cap, default 4096
}

// VacationState holds the global vacation mode state.
type VacationState struct {
	Active         bool      `json:"active"`
	ActivatedBy    string    `json:"activated_by"`
	ActivatedAt    time.Time `json:"activated_at"`
	Message        string    `json:"message,omitempty"`
	PreviousAgents []string  `json:"previous_agents"`
}

// DashboardAlert represents a system alert surfaced in the dashboard.
type DashboardAlert struct {
	ID           string     `json:"id"`
	SourceType   string     `json:"source_type"`
	SourceID     string     `json:"source_id"`
	AgentID      string     `json:"agent_id"`
	AgentName    string     `json:"agent_name"`
	Severity     string     `json:"severity"`
	Title        string     `json:"title"`
	Description  string     `json:"description"`
	Timestamp    time.Time  `json:"timestamp"`
	Acknowledged bool       `json:"acknowledged"`
	AckedAt      *time.Time `json:"acked_at,omitempty"`
}

// DefaultDenyPaths returns paths that are always denied for power-tier host access.
// These are merged into the deny list automatically.
func DefaultDenyPaths() []string {
	return []string{
		"/etc/shadow",
		"/etc/passwd",
		"~/.ssh/",
		"~/.gnupg/",
		"**/id_rsa",
		"**/id_ed25519",
		"**/*.pem",
	}
}

// TrustTier indicates the trust level of a skill source.
type TrustTier string

const (
	TrustBuiltIn   TrustTier = "builtin"
	TrustVerified  TrustTier = "verified"
	TrustCommunity TrustTier = "community"
	TrustLocal     TrustTier = "local"
)

// SkillManifest is the parsed representation of a skill.yaml file.
type SkillManifest struct {
	Name                 string              `yaml:"name" json:"name"`
	Version              string              `yaml:"version" json:"version"`
	Description          string              `yaml:"description" json:"description"`
	Author               string              `yaml:"author" json:"author"`
	License              string              `yaml:"license" json:"license"`
	RequiredTools        []string            `yaml:"required_tools" json:"required_tools"`
	RequiredIntegrations []string            `yaml:"required_integrations" json:"required_integrations"`
	RequiredCapabilities []SkillCapability   `yaml:"required_capabilities" json:"required_capabilities"`
	Prompts              map[string]string   `yaml:"prompts" json:"prompts"`
	Sandbox              *SkillSandboxConfig `yaml:"sandbox,omitempty" json:"sandbox,omitempty"`
}

// SkillCapability declares a capability a skill needs granted.
type SkillCapability struct {
	Tool     string `yaml:"tool" json:"tool"`
	Action   string `yaml:"action" json:"action"`
	Resource string `yaml:"resource" json:"resource"`
}

// SkillSandboxConfig declares sandbox constraints for a skill.
// Constraints use intersection semantics: a skill can only restrict agent
// capabilities, never expand them. The zero value (AllowNetwork: false) is
// intentionally restrictive — skills must explicitly opt in to network access.
type SkillSandboxConfig struct {
	AllowNetwork bool     `yaml:"allow_network" json:"allow_network"`
	AllowedHosts []string `yaml:"allowed_hosts" json:"allowed_hosts"`
	ReadPaths    []string `yaml:"read_paths" json:"read_paths"`
	WritePaths   []string `yaml:"write_paths" json:"write_paths"`
}

// Skill is the runtime representation of a loaded skill.
type Skill struct {
	Name          string        `json:"name"`
	Version       string        `json:"version"`
	Description   string        `json:"description"`
	Author        string        `json:"author"`
	Trust         TrustTier     `json:"trust"`
	Manifest      SkillManifest `json:"manifest"`
	DocContent    string        `json:"-"`
	PromptContent string        `json:"-"`
	HasDocs       bool          `json:"has_docs"`
	HasPrompts    bool          `json:"has_prompts"`
	HasTools      bool          `json:"has_tools"`
	LoadedAt      time.Time     `json:"loaded_at"`
}

// SkillGrant records which skill is granted to which agent.
type SkillGrant struct {
	ID        string    `json:"id"`
	AgentID   string    `json:"agent_id"`
	SkillName string    `json:"skill_name"`
	GrantedAt time.Time `json:"granted_at"`
	GrantedBy string    `json:"granted_by"`
}

// MessageType categorizes internal messages between agents.
type MessageType string

const (
	MessageTypeMessage MessageType = "message"
	MessageTypeTask    MessageType = "task"
	MessageTypeResult  MessageType = "result"
	MessageTypeStatus  MessageType = "status"
)

// MessagePriority indicates the urgency of an internal message.
type MessagePriority string

const (
	MessagePriorityNormal MessagePriority = "normal"
	MessagePriorityUrgent MessagePriority = "urgent"
)

// InternalMessage represents a message sent between agents.
type InternalMessage struct {
	ID        string            `json:"id"`
	From      string            `json:"from"`
	To        string            `json:"to"`
	Content   string            `json:"content"`
	Type      MessageType       `json:"type"`
	Priority  MessagePriority   `json:"priority"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	Timestamp time.Time         `json:"timestamp"`
}

// TeamCommunication defines how agents within a team communicate.
type TeamCommunication string

const (
	TeamCommLeaderMediated TeamCommunication = "leader-mediated"
	TeamCommOpen           TeamCommunication = "open"
)

// Team represents a group of agents working together.
type Team struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	LeaderID      string            `json:"leader_id"`
	MemberIDs     []string          `json:"member_ids"`
	Communication TeamCommunication `json:"communication"`
	Active        bool              `json:"active"`
	SharedContext string            `json:"shared_context"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
}

// PairedStatus represents the state of a paired conversation.
type PairedStatus string

const (
	PairedStatusActive    PairedStatus = "active"
	PairedStatusPaused    PairedStatus = "paused"
	PairedStatusCompleted PairedStatus = "completed"
	PairedStatusStopped   PairedStatus = "stopped"
)

// APIKey represents a programmatic access key for the REST API.
type APIKey struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	KeyHash    string     `json:"-"`
	KeyPrefix  string     `json:"key_prefix"`
	Scope      string     `json:"scope"`
	AgentIDs   []string   `json:"agent_ids"`
	IsActive   bool       `json:"is_active"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

// AgentTemplate defines a reusable agent configuration blueprint.
type AgentTemplate struct {
	ID                string                    `json:"id"`
	Name              string                    `json:"name"`
	Description       string                    `json:"description"`
	GroupID           string                    `json:"group_id,omitempty"`
	ConfigJSON        string                    `json:"config_json"`
	LockedFields      []string                  `json:"locked_fields"`
	ConstrainedFields map[string]ConstraintRule `json:"constrained_fields"`
	CreatedBy         string                    `json:"created_by"`
	CreatedAt         time.Time                 `json:"created_at"`
	UpdatedAt         time.Time                 `json:"updated_at"`
}

// ConstraintRule defines validation bounds for a constrained template field.
type ConstraintRule struct {
	Min     *float64 `json:"min,omitempty"`
	Max     *float64 `json:"max,omitempty"`
	Options []string `json:"options,omitempty"`
}

// ProviderRecord represents a configured LLM provider stored in the database.
type ProviderRecord struct {
	ID            string   `json:"id"`
	ProviderType  string   `json:"provider_type"`  // "openrouter", "openai", "anthropic", "ollama", "gemini"
	DisplayName   string   `json:"display_name"`
	APIKeyEnc     string   `json:"-"`               // encrypted API key (never serialized)
	BaseURL       string   `json:"base_url"`
	DefaultModel  string   `json:"default_model"`
	AllowedModels []string `json:"allowed_models"`
	IsEnabled     bool     `json:"is_enabled"`
	Source        string   `json:"source"`          // "db" or "config"
	ConfigJSON    string   `json:"config_json"`     // extra provider-specific config
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Provider source constants.
const (
	ProviderSourceDB     = "db"
	ProviderSourceConfig = "config"
)

// Valid provider types.
var ValidProviderTypes = []string{
	"openrouter", "openai", "anthropic", "ollama", "gemini",
}

// PairedConversation represents a structured dialogue between two agents.
type PairedConversation struct {
	ID                 string       `json:"id"`
	AgentA             string       `json:"agent_a"`
	AgentB             string       `json:"agent_b"`
	Topic              string       `json:"topic"`
	MaxTurns           int          `json:"max_turns"`
	TurnDelayMs        int          `json:"turn_delay_ms"`
	AllowUserInjection bool         `json:"allow_user_injection"`
	AutoStopPhrases    []string     `json:"auto_stop_phrases"`
	Status             PairedStatus `json:"status"`
	CurrentTurn        int          `json:"current_turn"`
	TotalTokens        int64        `json:"total_tokens"`
	EstimatedCost      float64      `json:"estimated_cost"`
	CreatedAt          time.Time    `json:"created_at"`
	CompletedAt        *time.Time   `json:"completed_at,omitempty"`
}

// --- Cluster types ---

// NodeInfo represents a cluster node's current state.
type NodeInfo struct {
	NodeID        string            `json:"node_id"`
	NodeName      string            `json:"node_name"`
	Address       string            `json:"address"`
	Status        string            `json:"status"`
	IsLeader      bool              `json:"is_leader"`
	LastHeartbeat time.Time         `json:"last_heartbeat"`
	Capacity      NodeCapacity      `json:"capacity"`
	Labels        map[string]string `json:"labels"`
	Version       string            `json:"version"`
	CreatedAt     time.Time         `json:"created_at"`
}

// NodeCapacity reports a node's resource usage.
type NodeCapacity struct {
	MaxAgents   int     `json:"max_agents"`
	AgentCount  int     `json:"agent_count"`
	CPUPercent  float64 `json:"cpu_percent"`
	MemoryBytes int64   `json:"memory_bytes"`
	MemoryTotal int64   `json:"memory_total"`
}

// Assignment records which node runs an agent.
type Assignment struct {
	AgentID        string            `json:"agent_id"`
	NodeID         string            `json:"node_id"`
	AssignedAt     time.Time         `json:"assigned_at"`
	NodeAffinity   map[string]string `json:"node_affinity,omitempty"`
	NodePreference map[string]string `json:"node_preference,omitempty"`
}

// Node statuses.
const (
	NodeStatusJoining      = "joining"
	NodeStatusActive       = "active"
	NodeStatusDraining     = "draining"
	NodeStatusDrained      = "drained"
	NodeStatusDisconnected = "disconnected"
)
