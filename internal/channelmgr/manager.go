// Package channelmgr manages the lifecycle of communication channel adapters
// (Slack, Discord). It stores credentials in the encrypted vault, creates
// adapters at runtime, and registers them with the core runtime.
package channelmgr

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/kkjorsvik/kyvik/internal/attachments"
	"github.com/kkjorsvik/kyvik/internal/channels"
	"github.com/kkjorsvik/kyvik/internal/channels/discord"
	"github.com/kkjorsvik/kyvik/internal/channels/slack"
	"github.com/kkjorsvik/kyvik/internal/config"
	"github.com/kkjorsvik/kyvik/internal/notifications"
	"github.com/kkjorsvik/kyvik/internal/secrets"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// CoreRegistrar is the narrow interface to the Kyvik core runtime
// needed by the channel manager.
type CoreRegistrar interface {
	RegisterChannel(adapter channels.Adapter)
	DeregisterChannel(name string) error
	ChannelAdapter(name string) channels.Adapter
	ListChannelNames() []string
	StartAdapterRouter(adapter channels.Adapter) error
}

// AgentLister is the narrow store interface for listing agents
// to re-provision when a channel is enabled.
type AgentLister interface {
	ListAgents(ctx context.Context) ([]types.AgentConfig, error)
}

// ChannelStatus represents the status of a channel type for the web UI.
type ChannelStatus struct {
	Type             string // "slack" or "discord"
	DisplayName      string
	Description      string
	Enabled          bool
	Connected        bool
	Error            string
	HasTokens        bool
	AutoProvision    bool
	PrimaryConnected bool
	DedicatedCount   int
}

// Manager manages the lifecycle of channel adapters: storing/retrieving
// config from the vault, creating/destroying adapters at runtime.
type Manager struct {
	vault            secrets.SecretStore
	core             CoreRegistrar
	agents           AgentLister // for listing agents to re-provision
	slkCfg           config.SlackConfig
	discCfg          config.DiscordConfig
	discordAuthorizer discord.DiscordAuthorizer // nil if not configured
	attachmentSvc    *attachments.Service
	notifier         notifications.Notifier
	lastNotified     map[string]time.Time // debounce per channel type
	mu               sync.RWMutex
	statuses         map[string]*ChannelStatus
}

// NewManager creates a new channel manager.
func NewManager(vault secrets.SecretStore, core CoreRegistrar, agentLister AgentLister, slkCfg config.SlackConfig, discCfg config.DiscordConfig) *Manager {
	return &Manager{
		vault:    vault,
		core:     core,
		agents:   agentLister,
		slkCfg:   slkCfg,
		discCfg:  discCfg,
		statuses: make(map[string]*ChannelStatus),
	}
}

// SetAttachmentService sets the shared attachment processing service
// which will be passed to Slack and Discord adapters.
func (m *Manager) SetAttachmentService(svc *attachments.Service) {
	m.attachmentSvc = svc
}

// SetNotifier sets the operator notifier used to fire channel_failure events.
func (m *Manager) SetNotifier(n notifications.Notifier) {
	m.notifier = n
}

// SetDiscordAuthorizer sets the authorizer for Discord user pairing/allowlists.
func (m *Manager) SetDiscordAuthorizer(auth discord.DiscordAuthorizer) {
	m.discordAuthorizer = auth
}

// UpdateDiscordAuthMode updates the in-memory auth mode for an agent on the
// active Discord adapter without requiring a restart.
func (m *Manager) UpdateDiscordAuthMode(agentID, mode string) {
	adapter := m.core.ChannelAdapter("discord")
	if adapter == nil {
		return
	}
	if dm, ok := adapter.(*discord.DiscordManager); ok {
		dm.UpdateAuthMode(agentID, mode)
	}
}

// LoadChannels reads vault for each channel type, falling back to config-file
// tokens and migrating them into vault on first run. Creates and registers
// adapters for enabled channels.
func (m *Manager) LoadChannels(ctx context.Context) error {
	// Initialize statuses for known channel types.
	m.mu.Lock()
	m.statuses["slack"] = &ChannelStatus{
		Type:        "slack",
		DisplayName: "Slack",
		Description: "Connect agents to Slack workspaces via Bot + App tokens.",
	}
	m.statuses["discord"] = &ChannelStatus{
		Type:        "discord",
		DisplayName: "Discord",
		Description: "Connect agents to Discord servers via a bot token.",
	}
	m.mu.Unlock()

	// Load Slack.
	if err := m.loadSlack(ctx); err != nil {
		slog.Warn("channel manager: slack load failed", "error", err)
	}

	// Load Discord.
	if err := m.loadDiscord(ctx); err != nil {
		slog.Warn("channel manager: discord load failed", "error", err)
	}

	return nil
}

func (m *Manager) loadSlack(ctx context.Context) error {
	enabled, _ := m.vault.Get(ctx, "global", "channel:slack:enabled")
	botToken, _ := m.vault.Get(ctx, "global", "channel:slack:bot_token")
	appToken, _ := m.vault.Get(ctx, "global", "channel:slack:app_token")
	autoStr, _ := m.vault.Get(ctx, "global", "channel:slack:auto_provision")

	// Migrate config-file values into vault on first run.
	if botToken == "" && m.slkCfg.BotToken != "" {
		botToken = m.slkCfg.BotToken
		appToken = m.slkCfg.AppToken
		_ = m.vault.Set(ctx, "global", "channel:slack:bot_token", botToken, "Slack bot token (migrated from config)")
		_ = m.vault.Set(ctx, "global", "channel:slack:app_token", appToken, "Slack app token (migrated from config)")
		if m.slkCfg.Enabled {
			enabled = "true"
			_ = m.vault.Set(ctx, "global", "channel:slack:enabled", "true", "")
		}
		autoStr = fmt.Sprintf("%t", m.slkCfg.AutoProvision)
		_ = m.vault.Set(ctx, "global", "channel:slack:auto_provision", autoStr, "")
		slog.Info("channel manager: migrated Slack tokens from config to vault")
	}

	m.mu.Lock()
	st := m.statuses["slack"]
	st.HasTokens = botToken != ""
	st.AutoProvision = autoStr == "true"
	m.mu.Unlock()

	if enabled == "true" && botToken != "" {
		return m.activateSlack(ctx, botToken, appToken, autoStr == "true")
	}
	return nil
}

func (m *Manager) loadDiscord(ctx context.Context) error {
	enabled, _ := m.vault.Get(ctx, "global", "channel:discord:enabled")
	botToken, _ := m.vault.Get(ctx, "global", "channel:discord:bot_token")
	guildID, _ := m.vault.Get(ctx, "global", "channel:discord:guild_id")
	autoStr, _ := m.vault.Get(ctx, "global", "channel:discord:auto_provision")

	// Migrate config-file values into vault on first run.
	if botToken == "" && m.discCfg.BotToken != "" {
		botToken = m.discCfg.BotToken
		guildID = m.discCfg.GuildID
		_ = m.vault.Set(ctx, "global", "channel:discord:bot_token", botToken, "Discord bot token (migrated from config)")
		_ = m.vault.Set(ctx, "global", "channel:discord:guild_id", guildID, "Discord guild ID (migrated from config)")
		if m.discCfg.Enabled {
			enabled = "true"
			_ = m.vault.Set(ctx, "global", "channel:discord:enabled", "true", "")
		}
		autoStr = fmt.Sprintf("%t", m.discCfg.AutoProvision)
		_ = m.vault.Set(ctx, "global", "channel:discord:auto_provision", autoStr, "")
		slog.Info("channel manager: migrated Discord tokens from config to vault")
	}

	m.mu.Lock()
	st := m.statuses["discord"]
	st.HasTokens = botToken != ""
	st.AutoProvision = autoStr == "true"
	m.mu.Unlock()

	if enabled == "true" && botToken != "" {
		return m.activateDiscord(ctx, botToken, guildID, autoStr == "true")
	}
	return nil
}

// activateSlack creates a SlackManager, registers it, and starts its router.
func (m *Manager) activateSlack(ctx context.Context, botToken, appToken string, autoProv bool) error {
	cfg := config.SlackConfig{
		Enabled:       true,
		BotToken:      botToken,
		AppToken:      appToken,
		AutoProvision: autoProv,
	}
	mgr, err := slack.NewManager(cfg, m.vault)
	if err != nil {
		m.setStatus("slack", false, false, err.Error())
		return fmt.Errorf("slack manager init: %w", err)
	}
	if m.attachmentSvc != nil {
		mgr.SetAttachmentService(m.attachmentSvc)
	}

	m.core.RegisterChannel(mgr)
	if err := m.core.StartAdapterRouter(mgr); err != nil {
		_ = m.core.DeregisterChannel("slack")
		m.setStatus("slack", false, false, err.Error())
		return fmt.Errorf("slack router start: %w", err)
	}

	m.setStatus("slack", true, mgr.PrimaryConnected(), "")

	// Re-provision running agents that use this channel.
	m.reprovisionAgents(ctx, "slack")

	return nil
}

// activateDiscord creates a DiscordManager, registers it, and starts its router.
func (m *Manager) activateDiscord(ctx context.Context, botToken, guildID string, autoProv bool) error {
	cfg := config.DiscordConfig{
		Enabled:       true,
		BotToken:      botToken,
		GuildID:       guildID,
		AutoProvision: autoProv,
	}
	var managerOpts []discord.ManagerOption
	if m.discordAuthorizer != nil {
		managerOpts = append(managerOpts, discord.WithManagerAuthorizer(m.discordAuthorizer))
	}
	mgr, err := discord.NewManagerWithOptions(cfg, m.vault, managerOpts)
	if err != nil {
		m.setStatus("discord", false, false, err.Error())
		return fmt.Errorf("discord manager init: %w", err)
	}
	if m.attachmentSvc != nil {
		mgr.SetAttachmentService(m.attachmentSvc)
	}

	m.core.RegisterChannel(mgr)
	if err := m.core.StartAdapterRouter(mgr); err != nil {
		_ = m.core.DeregisterChannel("discord")
		m.setStatus("discord", false, false, err.Error())
		return fmt.Errorf("discord router start: %w", err)
	}

	m.setStatus("discord", true, mgr.PrimaryConnected(), "")

	// Re-provision running agents that use this channel.
	m.reprovisionAgents(ctx, "discord")

	return nil
}

// reprovisionAgents finds running agents that are configured for a given channel
// and provisions them on the newly enabled adapter.
func (m *Manager) reprovisionAgents(ctx context.Context, channelType string) {
	if m.agents == nil {
		return
	}
	adapter := m.core.ChannelAdapter(channelType)
	if adapter == nil {
		return
	}

	agents, err := m.agents.ListAgents(ctx)
	if err != nil {
		slog.Warn("channel manager: failed to list agents for reprovisioning", "error", err)
		return
	}

	for _, ag := range agents {
		if ag.ActualState != types.AgentStatusRunning {
			continue
		}
		needsProv := false
		switch channelType {
		case "slack":
			needsProv = ag.SlackMode == types.SlackModePrimary
		case "discord":
			needsProv = ag.DiscordMode == types.DiscordModePrimary
		}
		if !needsProv {
			continue
		}
		if err := adapter.ProvisionAgent(ctx, ag); err != nil {
			slog.Warn("channel manager: reprovision agent failed",
				"agent_id", ag.ID, "channel", channelType, "error", err)
		} else {
			slog.Info("channel manager: reprovisioned agent on new channel",
				"agent_id", ag.ID, "channel", channelType)
		}
	}
}

// EnableChannel stores tokens in vault, creates an adapter, and registers it.
func (m *Manager) EnableChannel(ctx context.Context, channelType string, tokens map[string]string) error {
	switch channelType {
	case "slack":
		botToken := tokens["bot_token"]
		appToken := tokens["app_token"]
		autoProv := tokens["auto_provision"] == "true"
		if botToken == "" {
			return fmt.Errorf("slack bot token is required")
		}

		_ = m.vault.Set(ctx, "global", "channel:slack:bot_token", botToken, "Slack bot token")
		_ = m.vault.Set(ctx, "global", "channel:slack:app_token", appToken, "Slack app token")
		_ = m.vault.Set(ctx, "global", "channel:slack:auto_provision", fmt.Sprintf("%t", autoProv), "")
		_ = m.vault.Set(ctx, "global", "channel:slack:enabled", "true", "")

		// Deregister existing adapter if present.
		if existing := m.core.ChannelAdapter("slack"); existing != nil {
			_ = m.core.DeregisterChannel("slack")
		}

		return m.activateSlack(ctx, botToken, appToken, autoProv)

	case "discord":
		botToken := tokens["bot_token"]
		guildID := tokens["guild_id"]
		autoProv := tokens["auto_provision"] == "true"
		if botToken == "" {
			return fmt.Errorf("discord bot token is required")
		}

		_ = m.vault.Set(ctx, "global", "channel:discord:bot_token", botToken, "Discord bot token")
		_ = m.vault.Set(ctx, "global", "channel:discord:guild_id", guildID, "Discord guild ID")
		_ = m.vault.Set(ctx, "global", "channel:discord:auto_provision", fmt.Sprintf("%t", autoProv), "")
		_ = m.vault.Set(ctx, "global", "channel:discord:enabled", "true", "")

		// Deregister existing adapter if present.
		if existing := m.core.ChannelAdapter("discord"); existing != nil {
			_ = m.core.DeregisterChannel("discord")
		}

		return m.activateDiscord(ctx, botToken, guildID, autoProv)

	default:
		return fmt.Errorf("unsupported channel type: %q", channelType)
	}
}

// DisableChannel deregisters the adapter and marks the channel as disabled.
func (m *Manager) DisableChannel(ctx context.Context, channelType string) error {
	if err := m.core.DeregisterChannel(channelType); err != nil {
		return err
	}
	_ = m.vault.Set(ctx, "global", "channel:"+channelType+":enabled", "false", "")
	m.setStatus(channelType, false, false, "")
	return nil
}

// ReenableChannel re-enables a channel using tokens already stored in vault.
func (m *Manager) ReenableChannel(ctx context.Context, channelType string) error {
	switch channelType {
	case "slack":
		botToken, _ := m.vault.Get(ctx, "global", "channel:slack:bot_token")
		appToken, _ := m.vault.Get(ctx, "global", "channel:slack:app_token")
		autoStr, _ := m.vault.Get(ctx, "global", "channel:slack:auto_provision")
		if botToken == "" {
			return fmt.Errorf("slack: no stored bot token — configure first")
		}
		_ = m.vault.Set(ctx, "global", "channel:slack:enabled", "true", "")
		return m.activateSlack(ctx, botToken, appToken, autoStr == "true")
	case "discord":
		botToken, _ := m.vault.Get(ctx, "global", "channel:discord:bot_token")
		guildID, _ := m.vault.Get(ctx, "global", "channel:discord:guild_id")
		autoStr, _ := m.vault.Get(ctx, "global", "channel:discord:auto_provision")
		if botToken == "" {
			return fmt.Errorf("discord: no stored bot token — configure first")
		}
		_ = m.vault.Set(ctx, "global", "channel:discord:enabled", "true", "")
		return m.activateDiscord(ctx, botToken, guildID, autoStr == "true")
	default:
		return fmt.Errorf("unsupported channel type: %q", channelType)
	}
}

// UpdateTokens disables then re-enables a channel with new tokens.
func (m *Manager) UpdateTokens(ctx context.Context, channelType string, tokens map[string]string) error {
	if err := m.DisableChannel(ctx, channelType); err != nil {
		return err
	}
	return m.EnableChannel(ctx, channelType, tokens)
}

// TestConnection creates an ephemeral adapter to validate tokens, then closes it.
func (m *Manager) TestConnection(ctx context.Context, channelType string, tokens map[string]string) error {
	switch channelType {
	case "slack":
		cfg := config.SlackConfig{
			Enabled:  true,
			BotToken: tokens["bot_token"],
			AppToken: tokens["app_token"],
		}
		mgr, err := slack.NewManager(cfg, nil)
		if err != nil {
			return fmt.Errorf("slack connection test failed: %w", err)
		}
		defer mgr.Close()
		if !mgr.PrimaryConnected() {
			return fmt.Errorf("slack: could not connect with provided tokens")
		}
		return nil

	case "discord":
		cfg := config.DiscordConfig{
			Enabled:  true,
			BotToken: tokens["bot_token"],
			GuildID:  tokens["guild_id"],
		}
		mgr, err := discord.NewManager(cfg, nil)
		if err != nil {
			return fmt.Errorf("discord connection test failed: %w", err)
		}
		defer mgr.Close()
		if !mgr.PrimaryConnected() {
			return fmt.Errorf("discord: could not connect with provided tokens")
		}
		return nil

	default:
		return fmt.Errorf("unsupported channel type: %q", channelType)
	}
}

// ListChannels returns the status of all configurable channel types.
func (m *Manager) ListChannels() []ChannelStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]ChannelStatus, 0, len(m.statuses))
	for _, st := range m.statuses {
		cp := *st
		// Update dedicated count from live adapter.
		cp.DedicatedCount = m.dedicatedCount(st.Type)
		out = append(out, cp)
	}
	return out
}

// GetChannel returns the status of a specific channel type.
func (m *Manager) GetChannel(channelType string) *ChannelStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	st, ok := m.statuses[channelType]
	if !ok {
		return nil
	}
	cp := *st
	cp.DedicatedCount = m.dedicatedCount(channelType)
	return &cp
}

// GetTokens returns masked tokens for display (whether tokens are set,
// not the actual values). Returns the auto_provision flag in full.
func (m *Manager) GetTokens(ctx context.Context, channelType string) map[string]string {
	result := make(map[string]string)
	switch channelType {
	case "slack":
		if v, _ := m.vault.Get(ctx, "global", "channel:slack:bot_token"); v != "" {
			result["bot_token_set"] = "true"
		}
		if v, _ := m.vault.Get(ctx, "global", "channel:slack:app_token"); v != "" {
			result["app_token_set"] = "true"
		}
		v, _ := m.vault.Get(ctx, "global", "channel:slack:auto_provision")
		result["auto_provision"] = v
	case "discord":
		if v, _ := m.vault.Get(ctx, "global", "channel:discord:bot_token"); v != "" {
			result["bot_token_set"] = "true"
		}
		v, _ := m.vault.Get(ctx, "global", "channel:discord:guild_id")
		result["guild_id"] = v
		v, _ = m.vault.Get(ctx, "global", "channel:discord:auto_provision")
		result["auto_provision"] = v
	}
	return result
}

func (m *Manager) setStatus(channelType string, enabled, connected bool, errMsg string) {
	var displayName string
	var shouldNotify bool

	m.mu.Lock()
	if st, ok := m.statuses[channelType]; ok {
		st.Enabled = enabled
		st.Connected = connected
		st.Error = errMsg
		st.PrimaryConnected = connected
		displayName = st.DisplayName
	}
	// Debounce: suppress duplicate notifications within 5 minutes.
	if errMsg != "" && m.notifier != nil {
		now := time.Now()
		if m.lastNotified == nil {
			m.lastNotified = make(map[string]time.Time)
		}
		if last, ok := m.lastNotified[channelType]; !ok || now.Sub(last) >= 5*time.Minute {
			m.lastNotified[channelType] = now
			shouldNotify = true
		}
	}
	m.mu.Unlock()

	if shouldNotify {
		_ = m.notifier.Send(context.Background(), notifications.Event{
			Type:      "channel_failure",
			Severity:  "warning",
			Title:     fmt.Sprintf("%s channel failed to connect", displayName),
			Detail:    errMsg,
			Timestamp: time.Now(),
		})
	}
}

func (m *Manager) dedicatedCount(channelType string) int {
	adapter := m.core.ChannelAdapter(channelType)
	if adapter == nil {
		return 0
	}
	type counter interface{ DedicatedCount() int }
	if c, ok := adapter.(counter); ok {
		return c.DedicatedCount()
	}
	return 0
}
