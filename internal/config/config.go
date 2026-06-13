// Package config handles loading and validating Kyvik configuration.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

// Default values for per-agent memory configuration.
const (
	DefaultMaxMemories                        = 100
	DefaultMemoryExtractionInterval           = 15
	DefaultMemoryMaxExtractionsPerRun         = 2
	DefaultMemoryDuplicateThreshold   float32 = 0.85
	DefaultMemorySimilarThreshold     float32 = 0.75
)

// Config holds the full Kyvik configuration, mapping to kyvik.yaml.
type Config struct {
	Server         ServerConfig             `yaml:"server"`
	Auth           AuthConfig               `yaml:"auth"`
	Storage        StorageConfig            `yaml:"storage"`
	Spending       types.SpendingLimits     `yaml:"spending"`
	Models         ModelsConfig             `yaml:"models"`
	Channels       ChannelsConfig           `yaml:"channels"`
	Logging        LoggingConfig            `yaml:"logging"`
	Queue          QueueConfig              `yaml:"queue"`
	Notifications  NotificationsConfig      `yaml:"notifications"`
	Memory         MemoryConfig             `yaml:"memory"`
	CircuitBreaker CircuitBreakerYAMLConfig `yaml:"circuit_breaker"`
	Sandbox        SandboxYAMLConfig        `yaml:"sandbox"`
	Scheduler      SchedulerConfig          `yaml:"scheduler"`
	Retention      RetentionConfig          `yaml:"retention"`
	Skills         SkillsConfig             `yaml:"skills"`
	Backup         BackupConfig             `yaml:"backup"`
	API            APIConfig                `yaml:"api"`
	AuditStream    AuditStreamConfig        `yaml:"audit_stream"`
	Browser        BrowserConfig            `yaml:"browser"`
	HostFilesystem HostFilesystemConfig     `yaml:"host_filesystem"`
	Guide          GuideConfig              `yaml:"guide"`
	Compression    types.CompressionConfig  `yaml:"compression"`
	NetProxy       NetProxyConfig           `yaml:"net_proxy"`
	Cluster        ClusterConfig            `yaml:"cluster"`
}

// GuideConfig defines built-in guide agent settings.
type GuideConfig struct {
	Enabled        *bool                `yaml:"enabled"`
	Mode           string               `yaml:"mode"` // "basic" (default) or "full"
	SpendingLimits types.SpendingLimits `yaml:"spending_limits"`
}

// AuditStreamConfig controls SSE audit event streaming.
type AuditStreamConfig struct {
	MaxConnections int `yaml:"max_connections"`
	HeartbeatSec   int `yaml:"heartbeat_seconds"`
}

// BrowserConfig defines headless browser tool settings.
type BrowserConfig struct {
	TimeoutSeconds      int `yaml:"timeout_seconds"`       // default: 30
	SettleMillis        int `yaml:"settle_millis"`         // default: 2000
	MaxTextBytes        int `yaml:"max_text_bytes"`        // default: 1 MB
	ViewportWidth       int `yaml:"viewport_width"`        // default: 1920
	ViewportHeight      int `yaml:"viewport_height"`       // default: 1080
	MaxViewportWidth    int `yaml:"max_viewport_width"`    // default: 3840
	MaxViewportHeight   int `yaml:"max_viewport_height"`   // default: 2160
	MaxScreenshotWidth  int `yaml:"max_screenshot_width"`  // default: 3840
	MaxScreenshotHeight int `yaml:"max_screenshot_height"` // default: 2160
	MaxResults          int `yaml:"max_results"`           // default: 5
}

// HostFilesystemConfig defines host filesystem tool settings.
type HostFilesystemConfig struct {
	MaxReadBytes   int64 `yaml:"max_read_bytes"`   // default: 10 MB
	MaxWriteBytes  int64 `yaml:"max_write_bytes"`  // default: 10 MB
	MaxListDepth   int   `yaml:"max_list_depth"`   // default: 1
	MaxListEntries int   `yaml:"max_list_entries"` // default: 5000
}

// NetProxyConfig configures the sandbox network proxy.
type NetProxyConfig struct {
	Enabled    bool   `yaml:"enabled"`
	ListenAddr string `yaml:"listen_addr"` // default "127.0.0.1:0"
}

// ClusterConfig holds multi-node clustering configuration.
// Uses integer seconds for durations to match the existing codebase convention
// (e.g., TimeoutSeconds, StaleTimeoutSeconds in other config structs).
type ClusterConfig struct {
	Enabled                  *bool             `yaml:"enabled"`
	NodeName                 string            `yaml:"node_name"`
	AdvertiseAddr            string            `yaml:"advertise_addr"`
	Labels                   map[string]string `yaml:"labels"`
	HeartbeatIntervalSeconds int               `yaml:"heartbeat_interval_seconds"`
	HeartbeatTimeoutSeconds  int               `yaml:"heartbeat_timeout_seconds"`
	MaxAgents                int               `yaml:"max_agents"`
}

// HeartbeatInterval returns the heartbeat interval as a time.Duration.
func (c ClusterConfig) HeartbeatInterval() time.Duration {
	return time.Duration(c.HeartbeatIntervalSeconds) * time.Second
}

// HeartbeatTimeout returns the heartbeat timeout as a time.Duration.
func (c ClusterConfig) HeartbeatTimeout() time.Duration {
	return time.Duration(c.HeartbeatTimeoutSeconds) * time.Second
}

// APIConfig defines REST API settings.
type APIConfig struct {
	Enabled           *bool `yaml:"enabled"`             // default: true
	ViewerRateLimit   int   `yaml:"viewer_rate_limit"`   // requests/min; default: 60
	OperatorRateLimit int   `yaml:"operator_rate_limit"` // requests/min; default: 120
	ManagerRateLimit  int   `yaml:"manager_rate_limit"`  // requests/min; default: 120
	AdminRateLimit    int   `yaml:"admin_rate_limit"`    // requests/min; default: 300
}

// SandboxYAMLConfig defines sandbox execution settings.
type SandboxYAMLConfig struct {
	WorkspaceRoot      string           `yaml:"workspace_root"`
	RunnerPath         string           `yaml:"runner_path"`
	MaxMemoryMB        int              `yaml:"max_memory_mb"`
	MaxCPUPercent      int              `yaml:"max_cpu_percent"`
	TimeoutSeconds     int              `yaml:"timeout_seconds"`
	ToolTimeoutSeconds int              `yaml:"tool_timeout_seconds"` // KTP executor timeout (default 300s / 5 min)
	MaxOutputBytes     int              `yaml:"max_output_bytes"`
	HostAccess         string           `yaml:"host_access"`        // "sandbox" (default) or "host"
	AllowUnrestricted  *bool            `yaml:"allow_unrestricted"` // default: false
	ExtraPaths         ExtraPathsConfig `yaml:"extra_paths"`        // additional paths for systemd + app layer
}

// ExtraPathsConfig defines additional filesystem paths available to standard-tier agents.
type ExtraPathsConfig struct {
	ReadWrite []string `yaml:"read_write"`
	ReadOnly  []string `yaml:"read_only"`
}

// ServerConfig defines HTTP server settings.
type ServerConfig struct {
	ListenAddr      string   `yaml:"listen_addr"`
	DataDir         string   `yaml:"data_dir"`
	TrustedProxies  []string `yaml:"trusted_proxies"`
}

// AuthConfig defines authentication settings.
type AuthConfig struct {
	Type      string              `yaml:"type"` // "local" (default), "oidc", "delegated"
	Username  string              `yaml:"username"`
	Password  string              `yaml:"password"`
	OIDC      OIDCAuthConfig      `yaml:"oidc"`
	Delegated DelegatedAuthConfig `yaml:"delegated"`
}

// OIDCAuthConfig defines OpenID Connect provider settings.
type OIDCAuthConfig struct {
	IssuerURL    string `yaml:"issuer_url"`
	ClientID     string `yaml:"client_id"`
	ClientSecret string `yaml:"client_secret"`
	RedirectURL  string `yaml:"redirect_url"`
	DefaultRole  string `yaml:"default_role"`
}

// DelegatedAuthConfig defines delegated auth (e.g. Sett) settings.
type DelegatedAuthConfig struct {
	SettURL      string `yaml:"sett_url"`
	InstanceID   string `yaml:"instance_id"`
	SharedSecret string `yaml:"shared_secret"`
	CallbackURL  string `yaml:"callback_url"` // e.g. "https://my-instance.example.com/auth/callback"
}

// StorageConfig defines database settings.
type StorageConfig struct {
	Driver          string         `yaml:"driver"` // "postgres"
	Postgres        PostgresConfig `yaml:"postgres"`
	SeparateAuditDB bool           `yaml:"separate_audit_db"` // default: false (ignored for postgres)
	WriteBatchMS    int            `yaml:"write_batch_ms"`    // default: 100
	MaxConnections  int            `yaml:"max_connections"`   // default: 15
}

// PostgresConfig defines PostgreSQL-specific settings.
type PostgresConfig struct {
	DSN string `yaml:"dsn"`
}

// ModelsConfig defines LLM provider settings.
type ModelsConfig struct {
	OpenRouter OpenRouterConfig `yaml:"openrouter"`
	OpenAI     OpenAIConfig     `yaml:"openai"`
	Anthropic  AnthropicConfig  `yaml:"anthropic"`
	Ollama     OllamaConfig     `yaml:"ollama"`
	Gemini     GeminiConfig     `yaml:"gemini"`
}

// OpenRouterConfig defines OpenRouter provider settings.
type OpenRouterConfig struct {
	APIKey          string `yaml:"api_key"`
	ProvisioningKey string `yaml:"provisioning_key"`
	DefaultModel    string `yaml:"default_model"`
}

// OpenAIConfig defines OpenAI provider settings.
type OpenAIConfig struct {
	APIKey       string `yaml:"api_key"`
	BaseURL      string `yaml:"base_url"`
	DefaultModel string `yaml:"default_model"`
}

// AnthropicConfig defines Anthropic provider settings.
type AnthropicConfig struct {
	APIKey       string `yaml:"api_key"`
	BaseURL      string `yaml:"base_url"`
	DefaultModel string `yaml:"default_model"`
}

// OllamaConfig defines Ollama local model provider settings.
type OllamaConfig struct {
	Enabled        bool   `yaml:"enabled"`
	BaseURL        string `yaml:"base_url"`
	DefaultModel   string `yaml:"default_model"`
	EmbeddingModel string `yaml:"embedding_model"`
}

// GeminiConfig defines Google Gemini provider settings.
type GeminiConfig struct {
	APIKey       string `yaml:"api_key"`
	BaseURL      string `yaml:"base_url"`
	DefaultModel string `yaml:"default_model"`
}

// ChannelsConfig defines communication channel settings.
type ChannelsConfig struct {
	Slack   SlackConfig   `yaml:"slack"`
	WebUI   WebUIConfig   `yaml:"webui"`
	Discord DiscordConfig `yaml:"discord"`
}

// SlackConfig defines Slack adapter settings.
type SlackConfig struct {
	Enabled       bool   `yaml:"enabled"`
	BotToken      string `yaml:"bot_token"`
	AppToken      string `yaml:"app_token"`
	AutoProvision bool   `yaml:"auto_provision"`
}

// WebUIConfig defines built-in web UI settings.
type WebUIConfig struct {
	Enabled bool `yaml:"enabled"`
}

// DiscordConfig defines Discord adapter settings.
type DiscordConfig struct {
	Enabled       bool   `yaml:"enabled"`
	BotToken      string `yaml:"bot_token"`
	GuildID       string `yaml:"guild_id"`
	AutoProvision bool   `yaml:"auto_provision"`
}

// QueueConfig defines persistent message queue settings.
type QueueConfig struct {
	Depth               int      `yaml:"depth"`
	FullBehavior        string   `yaml:"full_behavior"`
	PriorityUsers       []string `yaml:"priority_users"`
	StaleTimeoutSeconds int      `yaml:"stale_timeout_seconds"`
	RetentionHours      int      `yaml:"retention_hours"`
}

// LoggingConfig defines logging settings.
type LoggingConfig struct {
	Level string      `yaml:"level"` // "debug", "info", "warn", "error"
	Audit AuditConfig `yaml:"audit"`
}

// AuditConfig defines audit-specific logging settings.
type AuditConfig struct {
	Enabled       bool `yaml:"enabled"`
	RetentionDays int  `yaml:"retention_days"`
}

// NotificationsConfig defines operator notification settings.
type NotificationsConfig struct {
	SlackChannel string                   `yaml:"slack_channel"`
	Events       NotificationEventsConfig `yaml:"events"`
}

// MemoryConfig defines memory decay and archival settings.
type MemoryConfig struct {
	DecayDays    int    `yaml:"decay_days"`    // Days before archival (default 90)
	ArchivalTime string `yaml:"archival_time"` // Daily run time HH:MM (default "03:00")
}

// CircuitBreakerYAMLConfig defines global circuit breaker defaults.
// Zero values mean "use compiled defaults" from types.DefaultCircuitBreakerConfig().
type CircuitBreakerYAMLConfig struct {
	ErrorThreshold        int `yaml:"error_threshold"`
	ErrorWindowMinutes    int `yaml:"error_window_minutes"`
	SpendingVelocityPct   int `yaml:"spending_velocity_pct"`
	SpendingWindowMinutes int `yaml:"spending_window_minutes"`
	ActionRatePerMinute   int `yaml:"action_rate_per_minute"`
	DestructiveLimit      int `yaml:"destructive_limit"`
	LoopIdenticalCount    int `yaml:"loop_identical_count"`
}

// SchedulerConfig defines cron scheduler settings.
type SchedulerConfig struct {
	Enabled         *bool  `yaml:"enabled"`
	DefaultTimezone string `yaml:"default_timezone"`
}

// RetentionConfig defines data retention and automated pruning settings.
type RetentionConfig struct {
	Enabled                 *bool  `yaml:"enabled"`                   // default: true
	AuditLogsDays           int    `yaml:"audit_logs_days"`           // default: 90
	ConversationHistoryDays int    `yaml:"conversation_history_days"` // default: 180
	CompletedQueueHours     int    `yaml:"completed_queue_hours"`     // default: 24
	SecurityEventsDays      int    `yaml:"security_events_days"`      // default: 90
	ArchivedMemoriesDays    int    `yaml:"archived_memories_days"`    // default: 365
	WebConversationsDays    int    `yaml:"web_conversations_days"`    // default: 365
	Schedule                string `yaml:"schedule"`                  // default: "0 4 * * *"
	WorkspaceGraceDays      int    `yaml:"workspace_grace_days"`
	WorkspaceRoot           string `yaml:"-"` // Set programmatically, not from YAML
}

// SkillsConfig defines skill system settings.
type SkillsConfig struct {
	Enabled   *bool  `yaml:"enabled"`    // default: true
	SkillsDir string `yaml:"skills_dir"` // default: "data/skills"
}

// BackupConfig defines automated backup settings.
type BackupConfig struct {
	Enabled   *bool  `yaml:"enabled"`   // default: true
	Schedule  string `yaml:"schedule"`  // default: "0 3 * * *"
	Retention int    `yaml:"retention"` // default: 7 (keep last N backups)
	Path      string `yaml:"path"`      // default: "{data_dir}/backups"
}

// NotificationEventsConfig controls which event types trigger notifications.
type NotificationEventsConfig struct {
	CircuitBreaker    bool `yaml:"circuit_breaker"`
	AgentError        bool `yaml:"agent_error"`
	SpendingThreshold int  `yaml:"spending_threshold"`
	BackupStatus      bool `yaml:"backup_status"`
	SecurityAlerts    bool `yaml:"security_alerts"`
	KeyFailure        bool `yaml:"key_failure"`
	ChannelFailure    bool `yaml:"channel_failure"`
}

// Load reads and parses the configuration from the given YAML file path.
// After unmarshalling, it applies environment variable overrides for secrets
// and fills in sensible defaults for missing values.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	// Environment variable overrides for secrets.
	if v := os.Getenv("KYVIK_AUTH_USER"); v != "" {
		cfg.Auth.Username = v
	}
	if v := os.Getenv("KYVIK_AUTH_PASS"); v != "" {
		cfg.Auth.Password = v
	}
	if v := os.Getenv("KYVIK_OPENROUTER_API_KEY"); v != "" {
		cfg.Models.OpenRouter.APIKey = v
	}
	if v := os.Getenv("KYVIK_OPENROUTER_PROVISIONING_KEY"); v != "" {
		cfg.Models.OpenRouter.ProvisioningKey = v
	}
	if v := os.Getenv("KYVIK_OPENAI_API_KEY"); v != "" {
		cfg.Models.OpenAI.APIKey = v
	}
	if v := os.Getenv("KYVIK_ANTHROPIC_API_KEY"); v != "" {
		cfg.Models.Anthropic.APIKey = v
	}
	if v := os.Getenv("KYVIK_SLACK_BOT_TOKEN"); v != "" {
		cfg.Channels.Slack.BotToken = v
	}
	if v := os.Getenv("KYVIK_SLACK_APP_TOKEN"); v != "" {
		cfg.Channels.Slack.AppToken = v
	}
	if v := os.Getenv("KYVIK_DISCORD_BOT_TOKEN"); v != "" {
		cfg.Channels.Discord.BotToken = v
	}
	if v := os.Getenv("KYVIK_OIDC_CLIENT_SECRET"); v != "" {
		cfg.Auth.OIDC.ClientSecret = v
	}
	if v := os.Getenv("KYVIK_DELEGATED_SHARED_SECRET"); v != "" {
		cfg.Auth.Delegated.SharedSecret = v
	}
	if v := os.Getenv("KYVIK_DB_DSN"); v != "" {
		cfg.Storage.Postgres.DSN = v
		if cfg.Storage.Driver == "" {
			cfg.Storage.Driver = "postgres"
		}
	}

	// Sensible defaults.
	if cfg.Server.ListenAddr == "" {
		cfg.Server.ListenAddr = ":8080"
	}
	if cfg.Server.DataDir == "" {
		cfg.Server.DataDir = "./data"
	}
	if cfg.Storage.Driver == "" {
		cfg.Storage.Driver = "postgres"
	}
	if cfg.Storage.MaxConnections <= 0 {
		cfg.Storage.MaxConnections = 15
	}
	if cfg.Storage.WriteBatchMS <= 0 {
		cfg.Storage.WriteBatchMS = 100
	}
	if cfg.Storage.Driver != "postgres" {
		return nil, fmt.Errorf("unsupported storage.driver %q: only postgres is supported", cfg.Storage.Driver)
	}
	if cfg.Storage.Postgres.DSN == "" {
		return nil, fmt.Errorf("PostgreSQL is required but no DSN is configured; set KYVIK_DB_DSN env var or storage.postgres.dsn in kyvik.yaml")
	}
	// Normalize auth type: "" and "basic" → "local" for backward compatibility.
	switch cfg.Auth.Type {
	case "", "basic":
		cfg.Auth.Type = "local"
	}
	if cfg.Auth.Username == "" {
		cfg.Auth.Username = "admin"
	}
	if cfg.Auth.Password == "" {
		cfg.Auth.Password = "changeme"
	}
	if cfg.Logging.Level == "" {
		cfg.Logging.Level = "info"
	}
	if cfg.Queue.Depth <= 0 {
		cfg.Queue.Depth = 50
	}
	if cfg.Queue.FullBehavior == "" {
		cfg.Queue.FullBehavior = "acknowledge"
	}
	if cfg.Queue.StaleTimeoutSeconds <= 0 {
		cfg.Queue.StaleTimeoutSeconds = 300
	}
	if cfg.Queue.RetentionHours <= 0 {
		cfg.Queue.RetentionHours = 24
	}

	// Memory defaults.
	if cfg.Memory.DecayDays <= 0 {
		cfg.Memory.DecayDays = 90
	}
	if cfg.Memory.ArchivalTime == "" {
		cfg.Memory.ArchivalTime = "03:00"
	}

	// Sandbox defaults.
	if cfg.Sandbox.WorkspaceRoot == "" {
		cfg.Sandbox.WorkspaceRoot = filepath.Join(cfg.Server.DataDir, "workspaces")
	}
	if cfg.Sandbox.MaxMemoryMB <= 0 {
		cfg.Sandbox.MaxMemoryMB = 1024
	}
	if cfg.Sandbox.MaxCPUPercent <= 0 {
		cfg.Sandbox.MaxCPUPercent = 50
	}
	if cfg.Sandbox.TimeoutSeconds <= 0 {
		cfg.Sandbox.TimeoutSeconds = 60
	}
	if cfg.Sandbox.ToolTimeoutSeconds <= 0 {
		cfg.Sandbox.ToolTimeoutSeconds = 300 // 5 minutes
	}
	if cfg.Sandbox.MaxOutputBytes <= 0 {
		cfg.Sandbox.MaxOutputBytes = 1 << 20 // 1 MB
	}
	if cfg.Sandbox.HostAccess == "" {
		cfg.Sandbox.HostAccess = "sandbox"
	}
	if cfg.Sandbox.AllowUnrestricted == nil {
		disallow := false
		cfg.Sandbox.AllowUnrestricted = &disallow
	}

	// Validate sandbox host access settings.
	if err := validateSandboxConfig(&cfg); err != nil {
		return nil, err
	}

	// Net proxy defaults.
	if cfg.NetProxy.ListenAddr == "" {
		cfg.NetProxy.ListenAddr = "127.0.0.1:0"
	}

	// Scheduler defaults.
	if cfg.Scheduler.Enabled == nil {
		enabled := true
		cfg.Scheduler.Enabled = &enabled
	}
	if cfg.Scheduler.DefaultTimezone == "" {
		cfg.Scheduler.DefaultTimezone = "America/Chicago"
	}

	// Retention defaults.
	if cfg.Retention.Enabled == nil {
		enabled := true
		cfg.Retention.Enabled = &enabled
	}
	if cfg.Retention.AuditLogsDays <= 0 {
		cfg.Retention.AuditLogsDays = 90
	}
	if cfg.Retention.ConversationHistoryDays <= 0 {
		cfg.Retention.ConversationHistoryDays = 180
	}
	if cfg.Retention.CompletedQueueHours <= 0 {
		cfg.Retention.CompletedQueueHours = 24
	}
	if cfg.Retention.SecurityEventsDays <= 0 {
		cfg.Retention.SecurityEventsDays = 90
	}
	if cfg.Retention.ArchivedMemoriesDays <= 0 {
		cfg.Retention.ArchivedMemoriesDays = 365
	}
	if cfg.Retention.WebConversationsDays <= 0 {
		cfg.Retention.WebConversationsDays = 365
	}
	if cfg.Retention.Schedule == "" {
		cfg.Retention.Schedule = "0 4 * * *"
	}
	if cfg.Retention.WorkspaceGraceDays == 0 {
		cfg.Retention.WorkspaceGraceDays = 7
	}

	// Skills defaults.
	if cfg.Skills.Enabled == nil {
		enabled := true
		cfg.Skills.Enabled = &enabled
	}
	if cfg.Skills.SkillsDir == "" {
		cfg.Skills.SkillsDir = filepath.Join(cfg.Server.DataDir, "skills")
	}

	// Backup defaults.
	if cfg.Backup.Enabled == nil {
		enabled := true
		cfg.Backup.Enabled = &enabled
	}
	if cfg.Backup.Schedule == "" {
		cfg.Backup.Schedule = "0 3 * * *"
	}
	if cfg.Backup.Retention <= 0 {
		cfg.Backup.Retention = 7
	}
	if cfg.Backup.Path == "" {
		cfg.Backup.Path = filepath.Join(cfg.Server.DataDir, "backups")
	}

	// API defaults.
	if cfg.API.Enabled == nil {
		enabled := true
		cfg.API.Enabled = &enabled
	}
	if cfg.API.ViewerRateLimit <= 0 {
		cfg.API.ViewerRateLimit = 60
	}
	if cfg.API.OperatorRateLimit <= 0 {
		cfg.API.OperatorRateLimit = 120
	}
	if cfg.API.ManagerRateLimit <= 0 {
		cfg.API.ManagerRateLimit = 120
	}
	if cfg.API.AdminRateLimit <= 0 {
		cfg.API.AdminRateLimit = 300
	}

	// Audit stream defaults.
	if cfg.AuditStream.MaxConnections <= 0 {
		cfg.AuditStream.MaxConnections = 20
	}
	if cfg.AuditStream.HeartbeatSec <= 0 {
		cfg.AuditStream.HeartbeatSec = 30
	}

	// Browser defaults.
	if cfg.Browser.TimeoutSeconds <= 0 {
		cfg.Browser.TimeoutSeconds = 30
	}
	if cfg.Browser.SettleMillis < 0 {
		cfg.Browser.SettleMillis = 2000
	} else if cfg.Browser.SettleMillis == 0 {
		cfg.Browser.SettleMillis = 2000
	}
	if cfg.Browser.MaxTextBytes <= 0 {
		cfg.Browser.MaxTextBytes = 1 << 20
	}
	if cfg.Browser.ViewportWidth <= 0 {
		cfg.Browser.ViewportWidth = 1920
	}
	if cfg.Browser.ViewportHeight <= 0 {
		cfg.Browser.ViewportHeight = 1080
	}
	if cfg.Browser.MaxViewportWidth <= 0 {
		cfg.Browser.MaxViewportWidth = 3840
	}
	if cfg.Browser.MaxViewportHeight <= 0 {
		cfg.Browser.MaxViewportHeight = 2160
	}
	if cfg.Browser.MaxScreenshotWidth <= 0 {
		cfg.Browser.MaxScreenshotWidth = 3840
	}
	if cfg.Browser.MaxScreenshotHeight <= 0 {
		cfg.Browser.MaxScreenshotHeight = 2160
	}
	if cfg.Browser.MaxResults <= 0 {
		cfg.Browser.MaxResults = 5
	}

	// Host filesystem defaults.
	if cfg.HostFilesystem.MaxReadBytes <= 0 {
		cfg.HostFilesystem.MaxReadBytes = 10 << 20
	}
	if cfg.HostFilesystem.MaxWriteBytes <= 0 {
		cfg.HostFilesystem.MaxWriteBytes = 10 << 20
	}
	if cfg.HostFilesystem.MaxListDepth <= 0 {
		cfg.HostFilesystem.MaxListDepth = 1
	}
	if cfg.HostFilesystem.MaxListEntries <= 0 {
		cfg.HostFilesystem.MaxListEntries = 5000
	}

	// Guide defaults.
	if cfg.Guide.Enabled == nil {
		enabled := true
		cfg.Guide.Enabled = &enabled
	}
	if cfg.Guide.Mode == "" {
		cfg.Guide.Mode = "basic"
	}
	if cfg.Guide.SpendingLimits.MaxSpendPerDay <= 0 {
		cfg.Guide.SpendingLimits.MaxSpendPerDay = 1.00
	}
	if cfg.Guide.SpendingLimits.MaxSpendPerMonth <= 0 {
		cfg.Guide.SpendingLimits.MaxSpendPerMonth = 30.00
	}

	// Compression defaults.
	cfg.Compression = types.NormalizeCompressionConfig(cfg.Compression)

	// Notification defaults.
	if cfg.Notifications.SlackChannel == "" {
		cfg.Notifications.SlackChannel = "#kyvik-alerts"
	}
	// If the entire events block is zero-valued (not specified in YAML),
	// enable all event types with sensible defaults.
	if cfg.Notifications.Events == (NotificationEventsConfig{}) {
		cfg.Notifications.Events = NotificationEventsConfig{
			CircuitBreaker:    true,
			AgentError:        true,
			SpendingThreshold: 90,
			BackupStatus:      true,
			SecurityAlerts:    true,
			KeyFailure:        true,
			ChannelFailure:    true,
		}
	}

	// Cluster defaults.
	if cfg.Cluster.Enabled == nil {
		enabled := false
		cfg.Cluster.Enabled = &enabled
	}
	if cfg.Cluster.HeartbeatIntervalSeconds == 0 {
		cfg.Cluster.HeartbeatIntervalSeconds = 5
	}
	if cfg.Cluster.HeartbeatTimeoutSeconds == 0 {
		cfg.Cluster.HeartbeatTimeoutSeconds = 15
	}

	return &cfg, nil
}

// validateSandboxConfig checks sandbox host access configuration for invalid combinations.
func validateSandboxConfig(cfg *Config) error {
	if cfg.Sandbox.HostAccess != "sandbox" && cfg.Sandbox.HostAccess != "host" {
		return fmt.Errorf("sandbox.host_access must be \"sandbox\" or \"host\", got %q", cfg.Sandbox.HostAccess)
	}
	if cfg.Sandbox.AllowUnrestricted != nil && *cfg.Sandbox.AllowUnrestricted && cfg.Sandbox.HostAccess != "host" {
		return fmt.Errorf("sandbox.allow_unrestricted=true requires sandbox.host_access=\"host\" (currently %q)", cfg.Sandbox.HostAccess)
	}
	for _, p := range cfg.Sandbox.ExtraPaths.ReadWrite {
		if !filepath.IsAbs(p) {
			return fmt.Errorf("sandbox.extra_paths.read_write: path must be absolute: %q", p)
		}
	}
	for _, p := range cfg.Sandbox.ExtraPaths.ReadOnly {
		if !filepath.IsAbs(p) {
			return fmt.Errorf("sandbox.extra_paths.read_only: path must be absolute: %q", p)
		}
	}
	return nil
}
