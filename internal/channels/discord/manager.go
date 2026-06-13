package discord

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/kkjorsvik/kyvik/internal/attachments"
	"github.com/kkjorsvik/kyvik/internal/channels"
	"github.com/kkjorsvik/kyvik/internal/config"
	"github.com/kkjorsvik/kyvik/internal/secrets"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// Compile-time interface check.
var _ channels.Adapter = (*DiscordManager)(nil)

// DiscordManager wraps a primary DiscordAdapter (the shared Kyvik Discord bot)
// plus N dedicated per-agent DiscordAdapters. It implements channels.Adapter
// so the core sees a single "discord" adapter.
type DiscordManager struct {
	primary       *DiscordAdapter
	vault         secrets.SecretStore
	discordConfig config.DiscordConfig
	authorizer    DiscordAuthorizer // nil if auth not configured
	attachmentSvc *attachments.Service

	mu        sync.RWMutex
	dedicated map[string]*DiscordAdapter // agentID → dedicated adapter
	incoming  chan channels.IncomingMessage
	routerCtx context.Context
	cancel    context.CancelFunc
	closed    bool

	// agentMode tracks which mode each agent was provisioned with
	agentMode map[string]string // agentID → "primary" or "dedicated"
}

// ManagerOption configures a DiscordManager.
type ManagerOption func(*DiscordManager)

// WithManagerAuthorizer sets the authorizer on the manager, which passes it to all adapters.
func WithManagerAuthorizer(auth DiscordAuthorizer) ManagerOption {
	return func(m *DiscordManager) { m.authorizer = auth }
}

// NewManager creates a DiscordManager. If primary tokens are available, a primary
// adapter is created. The vault is stored for creating dedicated adapters later.
func NewManager(cfg config.DiscordConfig, vault secrets.SecretStore, opts ...Option) (*DiscordManager, error) {
	return NewManagerWithOptions(cfg, vault, nil, opts...)
}

// NewManagerWithOptions creates a DiscordManager with manager-level options.
func NewManagerWithOptions(cfg config.DiscordConfig, vault secrets.SecretStore, managerOpts []ManagerOption, opts ...Option) (*DiscordManager, error) {
	ctx, cancel := context.WithCancel(context.Background())
	m := &DiscordManager{
		vault:         vault,
		discordConfig: cfg,
		dedicated:     make(map[string]*DiscordAdapter),
		incoming:      make(chan channels.IncomingMessage, 128),
		routerCtx:     ctx,
		cancel:        cancel,
		agentMode:     make(map[string]string),
	}

	for _, mo := range managerOpts {
		mo(m)
	}

	// Inject authorizer into adapter options if configured.
	if m.authorizer != nil {
		opts = append(opts, WithAuthorizer(m.authorizer))
	}

	// Try to create the primary adapter if token exists.
	primary, err := New(cfg, opts...)
	if err != nil {
		slog.Warn("discord manager: primary adapter unavailable, dedicated-only mode", "error", err)
	} else {
		m.primary = primary
	}

	return m, nil
}

// SetAttachmentService sets the shared attachment processing service.
func (m *DiscordManager) SetAttachmentService(svc *attachments.Service) {
	m.attachmentSvc = svc
	// Also inject into primary adapter if it exists.
	if m.primary != nil {
		m.primary.attachmentSvc = svc
	}
}

// Name returns the channel identifier.
func (m *DiscordManager) Name() string { return "discord" }

// ProvisionAgent provisions a Discord channel for an agent based on its DiscordMode.
func (m *DiscordManager) ProvisionAgent(ctx context.Context, cfg types.AgentConfig) error {
	log := slog.With("channel", "discord", "agent_id", cfg.ID, "discord_mode", cfg.DiscordMode)

	switch cfg.DiscordMode {
	case types.DiscordModePrimary:
		if m.primary == nil {
			return fmt.Errorf("discord: primary adapter not available")
		}

		if err := m.primary.ProvisionAgent(ctx, cfg); err != nil {
			return err
		}

		m.mu.Lock()
		m.agentMode[cfg.ID] = types.DiscordModePrimary
		m.mu.Unlock()

		log.Info("agent provisioned on primary adapter")
		return nil

	case types.DiscordModeDedicated:
		if m.vault == nil {
			return fmt.Errorf("discord: vault required for dedicated mode")
		}

		scope := "agent:" + cfg.ID
		botToken, err := m.vault.Get(ctx, scope, "discord:bot_token")
		if err != nil {
			return fmt.Errorf("discord: dedicated bot token not found for agent %s: %w", cfg.ID, err)
		}

		// Create a dedicated adapter with the agent's own credentials.
		dedCfg := config.DiscordConfig{
			Enabled:  true,
			BotToken: botToken,
			GuildID:  m.discordConfig.GuildID,
		}
		dedOpts := []Option{WithAgentID(cfg.ID)}
		if m.authorizer != nil {
			dedOpts = append(dedOpts, WithAuthorizer(m.authorizer))
		}
		if m.attachmentSvc != nil {
			dedOpts = append(dedOpts, WithAttachmentService(m.attachmentSvc))
		}
		adapter, err := New(dedCfg, dedOpts...)
		if err != nil {
			return fmt.Errorf("discord: create dedicated adapter for agent %s: %w", cfg.ID, err)
		}

		// Start receiving events from the dedicated adapter and merge into m.incoming.
		dedEvents, err := adapter.Receive(m.routerCtx)
		if err != nil {
			_ = adapter.Close()
			return fmt.Errorf("discord: start dedicated receive for agent %s: %w", cfg.ID, err)
		}

		m.mu.Lock()
		m.dedicated[cfg.ID] = adapter
		m.agentMode[cfg.ID] = types.DiscordModeDedicated
		m.mu.Unlock()

		// Merge dedicated events into the shared incoming channel.
		go func() {
			for msg := range dedEvents {
				select {
				case m.incoming <- msg:
				case <-m.routerCtx.Done():
					return
				}
			}
		}()

		log.Info("agent provisioned with dedicated adapter")
		return nil

	case types.DiscordModeNone, "":
		log.Debug("discord mode none, skipping provisioning")
		return nil

	default:
		return fmt.Errorf("discord: unknown mode %q for agent %s", cfg.DiscordMode, cfg.ID)
	}
}

// DeprovisionAgent removes an agent's Discord resources.
func (m *DiscordManager) DeprovisionAgent(ctx context.Context, agentID string) error {
	m.mu.RLock()
	mode := m.agentMode[agentID]
	m.mu.RUnlock()

	switch mode {
	case types.DiscordModePrimary:
		if m.primary != nil {
			if err := m.primary.DeprovisionAgent(ctx, agentID); err != nil {
				return err
			}
		}

	case types.DiscordModeDedicated:
		m.mu.Lock()
		adapter, ok := m.dedicated[agentID]
		if ok {
			delete(m.dedicated, agentID)
		}
		m.mu.Unlock()

		if ok {
			if err := adapter.Close(); err != nil {
				return fmt.Errorf("discord: close dedicated adapter for %s: %w", agentID, err)
			}
		}

	default:
		return types.ErrNotProvisioned
	}

	m.mu.Lock()
	delete(m.agentMode, agentID)
	m.mu.Unlock()

	return nil
}

// Send delivers a message from an agent to its Discord channel.
// Checks dedicated adapters first, then falls back to primary.
func (m *DiscordManager) Send(ctx context.Context, agentID string, msg types.Message) error {
	m.mu.RLock()
	adapter, ok := m.dedicated[agentID]
	mode := m.agentMode[agentID]
	m.mu.RUnlock()

	if ok {
		return adapter.Send(ctx, agentID, msg)
	}

	if mode == types.DiscordModePrimary && m.primary != nil {
		return m.primary.Send(ctx, agentID, msg)
	}

	return types.ErrNotProvisioned
}

// Receive returns a merged channel of incoming messages from primary + all dedicated adapters.
func (m *DiscordManager) Receive(ctx context.Context) (<-chan channels.IncomingMessage, error) {
	// Start primary adapter's receive and merge into m.incoming.
	if m.primary != nil {
		primaryEvents, err := m.primary.Receive(ctx)
		if err != nil {
			return nil, fmt.Errorf("discord: start primary receive: %w", err)
		}

		go func() {
			for msg := range primaryEvents {
				select {
				case m.incoming <- msg:
				case <-m.routerCtx.Done():
					return
				}
			}
		}()
	}

	return m.incoming, nil
}

// Close shuts down the primary adapter and all dedicated adapters.
func (m *DiscordManager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	m.cancel()

	// Snapshot dedicated adapters for closing.
	adapters := make([]*DiscordAdapter, 0, len(m.dedicated))
	for _, a := range m.dedicated {
		adapters = append(adapters, a)
	}
	m.dedicated = make(map[string]*DiscordAdapter)
	m.mu.Unlock()

	var firstErr error
	if m.primary != nil {
		firstErr = m.primary.Close()
	}
	for _, a := range adapters {
		if err := a.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// UpdateAuthMode updates the in-memory auth mode for an agent on whichever
// adapter (primary or dedicated) currently hosts it.
func (m *DiscordManager) UpdateAuthMode(agentID, mode string) {
	m.mu.RLock()
	agentModeVal := m.agentMode[agentID]
	adapter := m.dedicated[agentID]
	m.mu.RUnlock()

	switch agentModeVal {
	case types.DiscordModePrimary:
		if m.primary != nil {
			m.primary.UpdateAuthMode(agentID, mode)
		}
	case types.DiscordModeDedicated:
		if adapter != nil {
			adapter.UpdateAuthMode(agentID, mode)
		}
	}
}

// PrimaryConnected returns true if the primary adapter is initialized.
func (m *DiscordManager) PrimaryConnected() bool {
	return m.primary != nil
}

// DedicatedCount returns the number of active dedicated adapters.
func (m *DiscordManager) DedicatedCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.dedicated)
}
