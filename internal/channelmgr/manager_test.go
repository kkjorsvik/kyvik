package channelmgr

import (
	"context"
	"sync"
	"testing"

	"github.com/kkjorsvik/kyvik/internal/channels"
	"github.com/kkjorsvik/kyvik/internal/config"
	"github.com/kkjorsvik/kyvik/internal/secrets"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// mockVault implements secrets.SecretStore for testing.
type mockVault struct {
	mu   sync.Mutex
	data map[string]string // "scope:key" → value
}

func newMockVault() *mockVault {
	return &mockVault{data: make(map[string]string)}
}

func (v *mockVault) Set(_ context.Context, scope, key, plaintext, _ string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.data[scope+":"+key] = plaintext
	return nil
}

func (v *mockVault) Get(_ context.Context, scope, key string) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.data[scope+":"+key], nil
}

func (v *mockVault) Resolve(_ context.Context, _, _, _ string) (string, error) { return "", nil }
func (v *mockVault) Delete(_ context.Context, _, _ string) error               { return nil }
func (v *mockVault) List(_ context.Context, _ string) ([]secrets.SecretMeta, error) {
	return nil, nil
}
func (v *mockVault) Exists(_ context.Context, _, _ string) (bool, error) { return false, nil }

// mockCore implements CoreRegistrar for testing.
type mockCore struct {
	mu       sync.Mutex
	adapters map[string]channels.Adapter
	started  []string
}

func newMockCore() *mockCore {
	return &mockCore{adapters: make(map[string]channels.Adapter)}
}

func (c *mockCore) RegisterChannel(a channels.Adapter) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.adapters[a.Name()] = a
}

func (c *mockCore) DeregisterChannel(name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if a, ok := c.adapters[name]; ok {
		delete(c.adapters, name)
		return a.Close()
	}
	return nil
}

func (c *mockCore) ChannelAdapter(name string) channels.Adapter {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.adapters[name]
}

func (c *mockCore) ListChannelNames() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.adapters))
	for k := range c.adapters {
		out = append(out, k)
	}
	return out
}

func (c *mockCore) StartAdapterRouter(a channels.Adapter) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.started = append(c.started, a.Name())
	return nil
}

// mockAgentLister implements AgentLister for testing.
type mockAgentLister struct {
	agents []types.AgentConfig
}

func (s *mockAgentLister) ListAgents(_ context.Context) ([]types.AgentConfig, error) {
	return s.agents, nil
}

func TestNewManager(t *testing.T) {
	vault := newMockVault()
	core := newMockCore()
	m := NewManager(vault, core, nil, config.SlackConfig{}, config.DiscordConfig{})
	if m == nil {
		t.Fatal("NewManager returned nil")
	}
}

func TestLoadChannels_Empty(t *testing.T) {
	vault := newMockVault()
	core := newMockCore()
	m := NewManager(vault, core, nil, config.SlackConfig{}, config.DiscordConfig{})

	if err := m.LoadChannels(context.Background()); err != nil {
		t.Fatalf("LoadChannels: %v", err)
	}

	chs := m.ListChannels()
	if len(chs) != 2 {
		t.Fatalf("expected 2 channel statuses, got %d", len(chs))
	}
	for _, ch := range chs {
		if ch.Enabled {
			t.Errorf("channel %q should be disabled", ch.Type)
		}
	}
}

func TestLoadChannels_MigratesConfig(t *testing.T) {
	vault := newMockVault()
	core := newMockCore()
	slkCfg := config.SlackConfig{
		Enabled:       true,
		BotToken:      "xoxb-test",
		AppToken:      "xapp-test",
		AutoProvision: true,
	}
	m := NewManager(vault, core, nil, slkCfg, config.DiscordConfig{})

	if err := m.LoadChannels(context.Background()); err != nil {
		t.Fatalf("LoadChannels: %v", err)
	}

	// Verify tokens were migrated to vault.
	ctx := context.Background()
	bot, _ := vault.Get(ctx, "global", "channel:slack:bot_token")
	if bot != "xoxb-test" {
		t.Errorf("expected bot token migrated, got %q", bot)
	}
	app, _ := vault.Get(ctx, "global", "channel:slack:app_token")
	if app != "xapp-test" {
		t.Errorf("expected app token migrated, got %q", app)
	}
	enabled, _ := vault.Get(ctx, "global", "channel:slack:enabled")
	if enabled != "true" {
		t.Errorf("expected enabled=true, got %q", enabled)
	}
}

func TestEnableDisableChannel(t *testing.T) {
	vault := newMockVault()
	core := newMockCore()
	m := NewManager(vault, core, nil, config.SlackConfig{}, config.DiscordConfig{})
	_ = m.LoadChannels(context.Background())

	// Verify Slack starts disabled.
	st := m.GetChannel("slack")
	if st == nil {
		t.Fatal("GetChannel(slack) returned nil")
	}
	if st.Enabled {
		t.Error("slack should start disabled")
	}

	// Enable Slack — this will fail because we don't have a real Slack
	// connection, but tokens should be stored in vault.
	tokens := map[string]string{
		"bot_token":      "xoxb-test-enable",
		"app_token":      "xapp-test-enable",
		"auto_provision": "true",
	}
	// EnableChannel will try to create a real Slack connection which may fail
	// in test. We just verify the vault side.
	_ = m.EnableChannel(context.Background(), "slack", tokens)

	ctx := context.Background()
	bot, _ := vault.Get(ctx, "global", "channel:slack:bot_token")
	if bot != "xoxb-test-enable" {
		t.Errorf("expected stored bot token, got %q", bot)
	}

	// Disable.
	if err := m.DisableChannel(context.Background(), "slack"); err != nil {
		t.Fatalf("DisableChannel: %v", err)
	}
	st = m.GetChannel("slack")
	if st.Enabled {
		t.Error("slack should be disabled after DisableChannel")
	}
}

func TestGetTokens(t *testing.T) {
	vault := newMockVault()
	core := newMockCore()
	m := NewManager(vault, core, nil, config.SlackConfig{}, config.DiscordConfig{})
	_ = m.LoadChannels(context.Background())

	ctx := context.Background()
	_ = vault.Set(ctx, "global", "channel:slack:bot_token", "xoxb-secret", "")
	_ = vault.Set(ctx, "global", "channel:slack:auto_provision", "true", "")

	tokens := m.GetTokens(ctx, "slack")
	if tokens["bot_token_set"] != "true" {
		t.Error("expected bot_token_set=true")
	}
	if tokens["auto_provision"] != "true" {
		t.Error("expected auto_provision=true")
	}
}

func TestGetChannel_Unknown(t *testing.T) {
	vault := newMockVault()
	core := newMockCore()
	m := NewManager(vault, core, nil, config.SlackConfig{}, config.DiscordConfig{})
	_ = m.LoadChannels(context.Background())

	if st := m.GetChannel("telegram"); st != nil {
		t.Error("expected nil for unknown channel type")
	}
}

func TestEnableChannel_BadType(t *testing.T) {
	vault := newMockVault()
	core := newMockCore()
	m := NewManager(vault, core, nil, config.SlackConfig{}, config.DiscordConfig{})

	err := m.EnableChannel(context.Background(), "telegram", map[string]string{})
	if err == nil {
		t.Error("expected error for unsupported channel type")
	}
}

func TestEnableChannel_MissingToken(t *testing.T) {
	vault := newMockVault()
	core := newMockCore()
	m := NewManager(vault, core, nil, config.SlackConfig{}, config.DiscordConfig{})

	err := m.EnableChannel(context.Background(), "slack", map[string]string{})
	if err == nil {
		t.Error("expected error for missing bot token")
	}
}
