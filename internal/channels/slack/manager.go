package slack

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
var _ channels.Adapter = (*SlackManager)(nil)

// SlackManager wraps a primary SlackAdapter (the shared Kyvik Slack app)
// plus N dedicated per-agent SlackAdapters. It implements channels.Adapter
// so the core sees a single "slack" adapter.
type SlackManager struct {
	primary       *SlackAdapter
	vault         secrets.SecretStore
	slackConfig   config.SlackConfig
	attachmentSvc *attachments.Service

	mu        sync.RWMutex
	dedicated map[string]*SlackAdapter // agentID → dedicated adapter
	incoming  chan channels.IncomingMessage
	routerCtx context.Context
	cancel    context.CancelFunc
	closed    bool

	// agentMode tracks which mode each agent was provisioned with
	agentMode map[string]string // agentID → "primary" or "dedicated"
}

// NewManager creates a SlackManager. If primary tokens are available, a primary
// adapter is created. The vault is stored for creating dedicated adapters later.
func NewManager(cfg config.SlackConfig, vault secrets.SecretStore, opts ...Option) (*SlackManager, error) {
	ctx, cancel := context.WithCancel(context.Background())
	m := &SlackManager{
		vault:       vault,
		slackConfig: cfg,
		dedicated:   make(map[string]*SlackAdapter),
		incoming:    make(chan channels.IncomingMessage, 128),
		routerCtx:   ctx,
		cancel:      cancel,
		agentMode:   make(map[string]string),
	}

	// Try to create the primary adapter if tokens exist.
	primary, err := New(cfg, opts...)
	if err != nil {
		slog.Warn("slack manager: primary adapter unavailable, dedicated-only mode", "error", err)
	} else {
		m.primary = primary
	}

	return m, nil
}

// SetAttachmentService sets the shared attachment processing service.
func (m *SlackManager) SetAttachmentService(svc *attachments.Service) {
	m.attachmentSvc = svc
	// Also inject into primary adapter if it exists.
	if m.primary != nil {
		m.primary.attachmentSvc = svc
	}
}

// Name returns the channel identifier.
func (m *SlackManager) Name() string { return "slack" }

// ProvisionAgent provisions a Slack channel for an agent based on its SlackMode.
func (m *SlackManager) ProvisionAgent(ctx context.Context, cfg types.AgentConfig) error {
	log := slog.With("channel", "slack", "agent_id", cfg.ID, "slack_mode", cfg.SlackMode)

	switch cfg.SlackMode {
	case types.SlackModePrimary:
		if m.primary == nil {
			return fmt.Errorf("slack: primary adapter not available")
		}

		// Build a ChannelMapping so the primary adapter can map the agent.
		provCfg := cfg
		found := false
		for _, ch := range provCfg.Channels {
			if ch.ChannelType == "slack" {
				found = true
				break
			}
		}
		if !found {
			provCfg.Channels = append(provCfg.Channels, types.ChannelMapping{
				ChannelType: "slack",
				ChannelID:   cfg.SlackChannel,
			})
		}

		if err := m.primary.ProvisionAgent(ctx, provCfg); err != nil {
			return err
		}

		m.mu.Lock()
		m.agentMode[cfg.ID] = types.SlackModePrimary
		m.mu.Unlock()

		log.Info("agent provisioned on primary adapter")
		return nil

	case types.SlackModeDedicated:
		if m.vault == nil {
			return fmt.Errorf("slack: vault required for dedicated mode")
		}

		scope := "agent:" + cfg.ID
		botToken, err := m.vault.Get(ctx, scope, "slack:bot_token")
		if err != nil {
			return fmt.Errorf("slack: dedicated bot token not found for agent %s: %w", cfg.ID, err)
		}
		appToken, err := m.vault.Get(ctx, scope, "slack:app_token")
		if err != nil {
			return fmt.Errorf("slack: dedicated app token not found for agent %s: %w", cfg.ID, err)
		}

		// Create a dedicated adapter with the agent's own credentials.
		dedCfg := config.SlackConfig{
			Enabled:  true,
			BotToken: botToken,
			AppToken: appToken,
		}
		dedOpts := []Option{WithAgentID(cfg.ID)}
		if m.attachmentSvc != nil {
			dedOpts = append(dedOpts, WithAttachmentService(m.attachmentSvc))
		}
		adapter, err := New(dedCfg, dedOpts...)
		if err != nil {
			return fmt.Errorf("slack: create dedicated adapter for agent %s: %w", cfg.ID, err)
		}

		// Start receiving events from the dedicated adapter and merge into m.incoming.
		dedEvents, err := adapter.Receive(m.routerCtx)
		if err != nil {
			_ = adapter.Close()
			return fmt.Errorf("slack: start dedicated receive for agent %s: %w", cfg.ID, err)
		}

		m.mu.Lock()
		m.dedicated[cfg.ID] = adapter
		m.agentMode[cfg.ID] = types.SlackModeDedicated
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

	case types.SlackModeNone, "":
		log.Debug("slack mode none, skipping provisioning")
		return nil

	default:
		return fmt.Errorf("slack: unknown mode %q for agent %s", cfg.SlackMode, cfg.ID)
	}
}

// DeprovisionAgent removes an agent's Slack resources.
func (m *SlackManager) DeprovisionAgent(ctx context.Context, agentID string) error {
	m.mu.RLock()
	mode := m.agentMode[agentID]
	m.mu.RUnlock()

	switch mode {
	case types.SlackModePrimary:
		if m.primary != nil {
			if err := m.primary.DeprovisionAgent(ctx, agentID); err != nil {
				return err
			}
		}

	case types.SlackModeDedicated:
		m.mu.Lock()
		adapter, ok := m.dedicated[agentID]
		if ok {
			delete(m.dedicated, agentID)
		}
		m.mu.Unlock()

		if ok {
			if err := adapter.Close(); err != nil {
				return fmt.Errorf("slack: close dedicated adapter for %s: %w", agentID, err)
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

// Send delivers a message from an agent to its Slack channel.
// Checks dedicated adapters first, then falls back to primary.
func (m *SlackManager) Send(ctx context.Context, agentID string, msg types.Message) error {
	m.mu.RLock()
	adapter, ok := m.dedicated[agentID]
	mode := m.agentMode[agentID]
	m.mu.RUnlock()

	if ok {
		return adapter.Send(ctx, agentID, msg)
	}

	if mode == types.SlackModePrimary && m.primary != nil {
		return m.primary.Send(ctx, agentID, msg)
	}

	return types.ErrNotProvisioned
}

// Receive returns a merged channel of incoming messages from primary + all dedicated adapters.
func (m *SlackManager) Receive(ctx context.Context) (<-chan channels.IncomingMessage, error) {
	// Start primary adapter's receive and merge into m.incoming.
	if m.primary != nil {
		primaryEvents, err := m.primary.Receive(ctx)
		if err != nil {
			return nil, fmt.Errorf("slack: start primary receive: %w", err)
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
func (m *SlackManager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	m.cancel()

	// Snapshot dedicated adapters for closing.
	adapters := make([]*SlackAdapter, 0, len(m.dedicated))
	for _, a := range m.dedicated {
		adapters = append(adapters, a)
	}
	m.dedicated = make(map[string]*SlackAdapter)
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

// PrimaryConnected returns true if the primary adapter is initialized.
func (m *SlackManager) PrimaryConnected() bool {
	return m.primary != nil
}

// DedicatedCount returns the number of active dedicated adapters.
func (m *SlackManager) DedicatedCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.dedicated)
}
