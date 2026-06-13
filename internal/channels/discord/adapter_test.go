package discord

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/kkjorsvik/kyvik/internal/config"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

const testBotUserID = "BOT123"

// --- Mock types ---

type sendCall struct {
	channelID string
	content   string
}

type createChannelCall struct {
	guildID string
	name    string
}

type mockDiscordAPI struct {
	mu sync.Mutex

	sendCalls           []sendCall
	sendErr             error
	createChannelCalls  []createChannelCall
	createChannelResult *discordgo.Channel
	createChannelErr    error
	channelCalls        []string
	channelResult       *discordgo.Channel
	channelErr          error
	deleteChannelCalls  []string
	deleteChannelErr    error
	typingCalls         []string
}

func (m *mockDiscordAPI) ChannelMessageSend(channelID, content string, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sendCalls = append(m.sendCalls, sendCall{channelID: channelID, content: content})
	if m.sendErr != nil {
		return nil, m.sendErr
	}
	return &discordgo.Message{ID: "msg-1"}, nil
}

func (m *mockDiscordAPI) GuildChannelCreateComplex(guildID string, data discordgo.GuildChannelCreateData, _ ...discordgo.RequestOption) (*discordgo.Channel, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.createChannelCalls = append(m.createChannelCalls, createChannelCall{guildID: guildID, name: data.Name})
	if m.createChannelErr != nil {
		return nil, m.createChannelErr
	}
	if m.createChannelResult != nil {
		return m.createChannelResult, nil
	}
	return &discordgo.Channel{ID: "C" + data.Name, Name: data.Name}, nil
}

func (m *mockDiscordAPI) Channel(channelID string, _ ...discordgo.RequestOption) (*discordgo.Channel, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.channelCalls = append(m.channelCalls, channelID)
	if m.channelErr != nil {
		return nil, m.channelErr
	}
	if m.channelResult != nil {
		return m.channelResult, nil
	}
	return &discordgo.Channel{ID: channelID}, nil
}

func (m *mockDiscordAPI) ChannelTyping(channelID string, _ ...discordgo.RequestOption) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.typingCalls = append(m.typingCalls, channelID)
	return nil
}

func (m *mockDiscordAPI) ChannelDelete(channelID string, _ ...discordgo.RequestOption) (*discordgo.Channel, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleteChannelCalls = append(m.deleteChannelCalls, channelID)
	if m.deleteChannelErr != nil {
		return nil, m.deleteChannelErr
	}
	return &discordgo.Channel{ID: channelID}, nil
}

// mockEventSource implements eventSource for tests.
type mockEventSource struct {
	ch  chan discordEvent
	err error
}

func newMockEventSource() *mockEventSource {
	return &mockEventSource{ch: make(chan discordEvent, 64)}
}

func (m *mockEventSource) Start(ctx context.Context) (<-chan discordEvent, error) {
	if m.err != nil {
		return nil, m.err
	}
	out := make(chan discordEvent, 64)
	go func() {
		defer close(out)
		for {
			select {
			case evt, ok := <-m.ch:
				if !ok {
					return
				}
				select {
				case out <- evt:
				case <-ctx.Done():
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// newTestAdapter creates an adapter with mocks, bypassing real Discord clients.
func newTestAdapter(api *mockDiscordAPI, events *mockEventSource) *DiscordAdapter {
	a, _ := New(config.DiscordConfig{}, WithAPI(api), WithEventSource(events), WithBotUserID(testBotUserID), WithGuildID("guild-1"))
	return a
}

// --- Tests ---

func TestName(t *testing.T) {
	a := newTestAdapter(&mockDiscordAPI{}, newMockEventSource())
	if got := a.Name(); got != "discord" {
		t.Errorf("Name() = %q, want %q", got, "discord")
	}
}

func TestProvisionAgent_AutoProvision(t *testing.T) {
	api := &mockDiscordAPI{}
	a := newTestAdapter(api, newMockEventSource())
	a.autoProvision = true

	cfg := types.AgentConfig{
		ID:   "agent-1",
		Name: "Test Agent",
	}

	if err := a.ProvisionAgent(context.Background(), cfg); err != nil {
		t.Fatalf("ProvisionAgent() error = %v", err)
	}

	// Verify GuildChannelCreateComplex was called with sanitized name.
	if len(api.createChannelCalls) != 1 {
		t.Fatalf("expected 1 CreateChannel call, got %d", len(api.createChannelCalls))
	}
	if got := api.createChannelCalls[0].name; got != "kyvik-test-agent" {
		t.Errorf("CreateChannel name = %q, want %q", got, "kyvik-test-agent")
	}
	if got := api.createChannelCalls[0].guildID; got != "guild-1" {
		t.Errorf("CreateChannel guildID = %q, want %q", got, "guild-1")
	}

	// Verify mapping was stored.
	a.mu.RLock()
	defer a.mu.RUnlock()
	if _, ok := a.agentToChannel["agent-1"]; !ok {
		t.Error("agentToChannel missing agent-1")
	}
	if a.autoProvisioned[a.agentToChannel["agent-1"]] != true {
		t.Error("expected channel to be marked as auto-provisioned")
	}
}

func TestProvisionAgent_ManualProvision(t *testing.T) {
	api := &mockDiscordAPI{}
	a := newTestAdapter(api, newMockEventSource())
	a.autoProvision = false

	cfg := types.AgentConfig{
		ID:               "agent-2",
		Name:             "Manual Agent",
		DiscordChannelID: "123456789",
	}

	if err := a.ProvisionAgent(context.Background(), cfg); err != nil {
		t.Fatalf("ProvisionAgent() error = %v", err)
	}

	// Verify Channel was called.
	if len(api.channelCalls) != 1 {
		t.Fatalf("expected 1 Channel call, got %d", len(api.channelCalls))
	}
	if got := api.channelCalls[0]; got != "123456789" {
		t.Errorf("Channel channelID = %q, want %q", got, "123456789")
	}

	// Verify mapping was stored without auto-provisioned flag.
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.agentToChannel["agent-2"] != "123456789" {
		t.Errorf("agentToChannel[agent-2] = %q, want %q", a.agentToChannel["agent-2"], "123456789")
	}
	if a.autoProvisioned["123456789"] {
		t.Error("manual channel should not be marked as auto-provisioned")
	}
}

func TestProvisionAgent_ManualMissingChannelID(t *testing.T) {
	a := newTestAdapter(&mockDiscordAPI{}, newMockEventSource())
	a.autoProvision = false

	cfg := types.AgentConfig{
		ID:   "agent-3",
		Name: "No Channel",
	}

	err := a.ProvisionAgent(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error for missing discord_channel_id")
	}
}

func TestProvisionAgent_CreateError(t *testing.T) {
	api := &mockDiscordAPI{
		createChannelErr: errors.New("channel already exists"),
	}
	a := newTestAdapter(api, newMockEventSource())
	a.autoProvision = true

	cfg := types.AgentConfig{
		ID:   "agent-4",
		Name: "Dupe Agent",
	}

	err := a.ProvisionAgent(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error from GuildChannelCreateComplex")
	}
}

func TestDeprovisionAgent_AutoProvisioned(t *testing.T) {
	api := &mockDiscordAPI{}
	a := newTestAdapter(api, newMockEventSource())

	// Pre-populate mappings as if auto-provisioned.
	a.mu.Lock()
	a.agentToChannel["agent-1"] = "CAUTO1"
	a.channelToAgent["CAUTO1"] = "agent-1"
	a.autoProvisioned["CAUTO1"] = true
	a.mu.Unlock()

	if err := a.DeprovisionAgent(context.Background(), "agent-1"); err != nil {
		t.Fatalf("DeprovisionAgent() error = %v", err)
	}

	// Verify channel was deleted.
	if len(api.deleteChannelCalls) != 1 {
		t.Fatalf("expected 1 ChannelDelete call, got %d", len(api.deleteChannelCalls))
	}
	if api.deleteChannelCalls[0] != "CAUTO1" {
		t.Errorf("ChannelDelete channelID = %q, want %q", api.deleteChannelCalls[0], "CAUTO1")
	}

	// Verify maps cleaned up.
	a.mu.RLock()
	defer a.mu.RUnlock()
	if _, ok := a.agentToChannel["agent-1"]; ok {
		t.Error("agentToChannel should not contain agent-1")
	}
	if _, ok := a.channelToAgent["CAUTO1"]; ok {
		t.Error("channelToAgent should not contain CAUTO1")
	}
}

func TestDeprovisionAgent_Manual(t *testing.T) {
	api := &mockDiscordAPI{}
	a := newTestAdapter(api, newMockEventSource())

	// Pre-populate mappings as if manually provisioned.
	a.mu.Lock()
	a.agentToChannel["agent-2"] = "CMANUAL"
	a.channelToAgent["CMANUAL"] = "agent-2"
	a.mu.Unlock()

	if err := a.DeprovisionAgent(context.Background(), "agent-2"); err != nil {
		t.Fatalf("DeprovisionAgent() error = %v", err)
	}

	// Should NOT delete.
	if len(api.deleteChannelCalls) != 0 {
		t.Errorf("expected 0 ChannelDelete calls, got %d", len(api.deleteChannelCalls))
	}

	// Verify maps cleaned up.
	a.mu.RLock()
	defer a.mu.RUnlock()
	if _, ok := a.agentToChannel["agent-2"]; ok {
		t.Error("agentToChannel should not contain agent-2")
	}
}

func TestDeprovisionAgent_NotProvisioned(t *testing.T) {
	a := newTestAdapter(&mockDiscordAPI{}, newMockEventSource())

	err := a.DeprovisionAgent(context.Background(), "unknown-agent")
	if !errors.Is(err, types.ErrNotProvisioned) {
		t.Errorf("expected ErrNotProvisioned, got %v", err)
	}
}

func TestSend(t *testing.T) {
	api := &mockDiscordAPI{}
	a := newTestAdapter(api, newMockEventSource())

	// Pre-populate mapping.
	a.mu.Lock()
	a.agentToChannel["agent-1"] = "C12345"
	a.channelToAgent["C12345"] = "agent-1"
	a.mu.Unlock()

	msg := types.Message{Content: "Hello from agent"}
	if err := a.Send(context.Background(), "agent-1", msg); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if len(api.sendCalls) != 1 {
		t.Fatalf("expected 1 ChannelMessageSend call, got %d", len(api.sendCalls))
	}
	if api.sendCalls[0].channelID != "C12345" {
		t.Errorf("ChannelMessageSend channelID = %q, want %q", api.sendCalls[0].channelID, "C12345")
	}
}

func TestSend_NotProvisioned(t *testing.T) {
	a := newTestAdapter(&mockDiscordAPI{}, newMockEventSource())

	err := a.Send(context.Background(), "unknown-agent", types.Message{Content: "hi"})
	if !errors.Is(err, types.ErrNotProvisioned) {
		t.Errorf("expected ErrNotProvisioned, got %v", err)
	}
}

func TestReceive_RoutesToAgent(t *testing.T) {
	events := newMockEventSource()
	a := newTestAdapter(&mockDiscordAPI{}, events)

	// Pre-populate mapping.
	a.mu.Lock()
	a.agentToChannel["agent-1"] = "C12345"
	a.channelToAgent["C12345"] = "agent-1"
	a.mu.Unlock()

	incoming, err := a.Receive(context.Background())
	if err != nil {
		t.Fatalf("Receive() error = %v", err)
	}

	events.ch <- discordEvent{channelID: "C12345", userID: "UUSER1", text: "hello agent"}

	select {
	case msg := <-incoming:
		if msg.AgentID != "agent-1" {
			t.Errorf("AgentID = %q, want %q", msg.AgentID, "agent-1")
		}
		if msg.Content != "hello agent" {
			t.Errorf("Content = %q, want %q", msg.Content, "hello agent")
		}
		if msg.ChannelType != "discord" {
			t.Errorf("ChannelType = %q, want %q", msg.ChannelType, "discord")
		}
		if msg.SenderID != "UUSER1" {
			t.Errorf("SenderID = %q, want %q", msg.SenderID, "UUSER1")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for incoming message")
	}

	a.Close()
}

func TestReceive_IgnoresUnmapped(t *testing.T) {
	events := newMockEventSource()
	a := newTestAdapter(&mockDiscordAPI{}, events)

	// Map two agents so fallback requires a bot mention.
	a.mu.Lock()
	a.agentToChannel["agent-1"] = "CMAPPED1"
	a.channelToAgent["CMAPPED1"] = "agent-1"
	a.agentToChannel["agent-2"] = "CMAPPED2"
	a.channelToAgent["CMAPPED2"] = "agent-2"
	a.mu.Unlock()

	incoming, err := a.Receive(context.Background())
	if err != nil {
		t.Fatalf("Receive() error = %v", err)
	}

	// Send event from unmapped channel without bot mention — should be dropped.
	events.ch <- discordEvent{channelID: "CUNKNOWN", userID: "UUSER1", text: "lost message"}

	// Send from a mapped channel to verify routing still works.
	events.ch <- discordEvent{channelID: "CMAPPED1", userID: "UUSER1", text: "routed message"}

	select {
	case msg := <-incoming:
		if msg.Content != "routed message" {
			t.Errorf("expected routed message, got %q", msg.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for routed message")
	}

	a.Close()
}

func TestReceive_IgnoresBotMessages(t *testing.T) {
	events := newMockEventSource()
	a := newTestAdapter(&mockDiscordAPI{}, events)

	// Map a channel.
	a.mu.Lock()
	a.agentToChannel["agent-1"] = "C12345"
	a.channelToAgent["C12345"] = "agent-1"
	a.mu.Unlock()

	incoming, err := a.Receive(context.Background())
	if err != nil {
		t.Fatalf("Receive() error = %v", err)
	}

	// Send bot's own message (should be filtered).
	events.ch <- discordEvent{channelID: "C12345", userID: testBotUserID, text: "bot echo"}

	// Send a real user message.
	events.ch <- discordEvent{channelID: "C12345", userID: "UHUMAN", text: "human message"}

	select {
	case msg := <-incoming:
		if msg.Content != "human message" {
			t.Errorf("expected human message, got %q (bot message was not filtered)", msg.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for human message")
	}

	a.Close()
}

func TestClose(t *testing.T) {
	events := newMockEventSource()
	a := newTestAdapter(&mockDiscordAPI{}, events)

	_, err := a.Receive(context.Background())
	if err != nil {
		t.Fatalf("Receive() error = %v", err)
	}

	if err := a.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	a.mu.RLock()
	defer a.mu.RUnlock()
	if !a.closed {
		t.Error("expected closed to be true")
	}
}

func TestClose_Idempotent(t *testing.T) {
	events := newMockEventSource()
	a := newTestAdapter(&mockDiscordAPI{}, events)

	_, err := a.Receive(context.Background())
	if err != nil {
		t.Fatalf("Receive() error = %v", err)
	}

	if err := a.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

func TestProvisionAgent_MultipleChannels(t *testing.T) {
	api := &mockDiscordAPI{}
	a := newTestAdapter(api, newMockEventSource())
	a.autoProvision = false

	cfg := types.AgentConfig{
		ID:               "agent-multi",
		Name:             "Multi Channel",
		DiscordChannelID: "111:all, 222:mention, 333",
	}

	if err := a.ProvisionAgent(context.Background(), cfg); err != nil {
		t.Fatalf("ProvisionAgent() error = %v", err)
	}

	// Verify all channels were validated.
	api.mu.Lock()
	if len(api.channelCalls) != 3 {
		t.Errorf("expected 3 Channel calls, got %d", len(api.channelCalls))
	}
	api.mu.Unlock()

	// Verify all channels map to the agent with correct modes.
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, chID := range []string{"111", "222", "333"} {
		if a.channelToAgent[chID] != "agent-multi" {
			t.Errorf("channelToAgent[%q] = %q, want %q", chID, a.channelToAgent[chID], "agent-multi")
		}
	}
	if a.channelMode["111"] != "all" {
		t.Errorf("channelMode[111] = %q, want %q", a.channelMode["111"], "all")
	}
	if a.channelMode["222"] != "mention" {
		t.Errorf("channelMode[222] = %q, want %q", a.channelMode["222"], "mention")
	}
	if a.channelMode["333"] != "all" {
		t.Errorf("channelMode[333] = %q, want %q (default)", a.channelMode["333"], "all")
	}

	// Default Send channel is the first one.
	if a.agentToChannel["agent-multi"] != "111" {
		t.Errorf("agentToChannel = %q, want %q", a.agentToChannel["agent-multi"], "111")
	}
}

func TestReceive_MultiChannelRouting(t *testing.T) {
	events := newMockEventSource()
	a := newTestAdapter(&mockDiscordAPI{}, events)

	// Map agent to multiple channels.
	a.mu.Lock()
	a.agentToChannel["agent-1"] = "C1"
	a.channelToAgent["C1"] = "agent-1"
	a.channelToAgent["C2"] = "agent-1"
	a.channelToAgent["C3"] = "agent-1"
	a.mu.Unlock()

	incoming, err := a.Receive(context.Background())
	if err != nil {
		t.Fatalf("Receive() error = %v", err)
	}

	// Messages from any mapped channel should route to the same agent.
	for _, chID := range []string{"C1", "C2", "C3"} {
		events.ch <- discordEvent{channelID: chID, userID: "UUSER1", text: "from " + chID}

		select {
		case msg := <-incoming:
			if msg.AgentID != "agent-1" {
				t.Errorf("channel %s: AgentID = %q, want %q", chID, msg.AgentID, "agent-1")
			}
			if msg.ChannelID != chID {
				t.Errorf("ChannelID = %q, want %q", msg.ChannelID, chID)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for message from %s", chID)
		}
	}

	a.Close()
}

func TestDeprovisionAgent_MultipleChannels(t *testing.T) {
	api := &mockDiscordAPI{}
	a := newTestAdapter(api, newMockEventSource())

	// Pre-populate agent mapped to 3 channels (1 auto-provisioned).
	a.mu.Lock()
	a.agentToChannel["agent-1"] = "C1"
	a.channelToAgent["C1"] = "agent-1"
	a.channelToAgent["C2"] = "agent-1"
	a.channelToAgent["C3"] = "agent-1"
	a.autoProvisioned["C1"] = true
	a.mu.Unlock()

	if err := a.DeprovisionAgent(context.Background(), "agent-1"); err != nil {
		t.Fatalf("DeprovisionAgent() error = %v", err)
	}

	// Auto-provisioned channel should be deleted.
	api.mu.Lock()
	if len(api.deleteChannelCalls) != 1 || api.deleteChannelCalls[0] != "C1" {
		t.Errorf("expected ChannelDelete for C1, got %v", api.deleteChannelCalls)
	}
	api.mu.Unlock()

	// All maps should be cleaned up.
	a.mu.RLock()
	defer a.mu.RUnlock()
	if _, ok := a.agentToChannel["agent-1"]; ok {
		t.Error("agentToChannel should not contain agent-1")
	}
	for _, chID := range []string{"C1", "C2", "C3"} {
		if _, ok := a.channelToAgent[chID]; ok {
			t.Errorf("channelToAgent should not contain %s", chID)
		}
	}
}

func TestReceive_MentionModeIgnoresNonMention(t *testing.T) {
	events := newMockEventSource()
	a := newTestAdapter(&mockDiscordAPI{}, events)

	// Map agent to a mention-only channel and an all channel.
	a.mu.Lock()
	a.agentToChannel["agent-1"] = "CALL"
	a.channelToAgent["CALL"] = "agent-1"
	a.channelMode["CALL"] = "all"
	a.channelToAgent["CMENTION"] = "agent-1"
	a.channelMode["CMENTION"] = "mention"
	a.mu.Unlock()

	incoming, err := a.Receive(context.Background())
	if err != nil {
		t.Fatalf("Receive() error = %v", err)
	}

	// Non-mention message in mention-only channel — should be dropped.
	events.ch <- discordEvent{channelID: "CMENTION", userID: "UUSER1", text: "casual chat", botMentioned: false}

	// Message in all-mode channel — should be delivered.
	events.ch <- discordEvent{channelID: "CALL", userID: "UUSER1", text: "hello"}

	select {
	case msg := <-incoming:
		if msg.Content != "hello" {
			t.Errorf("expected 'hello', got %q (mention-only message leaked)", msg.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for message")
	}

	a.Close()
}

func TestReceive_MentionModeDeliversMention(t *testing.T) {
	events := newMockEventSource()
	a := newTestAdapter(&mockDiscordAPI{}, events)

	// Map agent to a mention-only channel.
	a.mu.Lock()
	a.agentToChannel["agent-1"] = "CMENTION"
	a.channelToAgent["CMENTION"] = "agent-1"
	a.channelMode["CMENTION"] = "mention"
	a.mu.Unlock()

	incoming, err := a.Receive(context.Background())
	if err != nil {
		t.Fatalf("Receive() error = %v", err)
	}

	// Message WITH mention in mention-only channel — should be delivered.
	events.ch <- discordEvent{channelID: "CMENTION", userID: "UUSER1", text: "help me", botMentioned: true}

	select {
	case msg := <-incoming:
		if msg.Content != "help me" {
			t.Errorf("Content = %q, want %q", msg.Content, "help me")
		}
		if msg.AgentID != "agent-1" {
			t.Errorf("AgentID = %q, want %q", msg.AgentID, "agent-1")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for mentioned message")
	}

	a.Close()
}

func TestReceive_DropsUnmappedEvenSingleAgent(t *testing.T) {
	events := newMockEventSource()
	a := newTestAdapter(&mockDiscordAPI{}, events)

	// Map one agent to a known channel.
	a.mu.Lock()
	a.agentToChannel["agent-1"] = "CMAPPED"
	a.channelToAgent["CMAPPED"] = "agent-1"
	a.mu.Unlock()

	incoming, err := a.Receive(context.Background())
	if err != nil {
		t.Fatalf("Receive() error = %v", err)
	}

	// Unmapped channel should be dropped even with one agent.
	events.ch <- discordEvent{channelID: "CDM123", userID: "UUSER1", text: "hello from DM"}

	// Send from mapped channel to prove loop is running.
	events.ch <- discordEvent{channelID: "CMAPPED", userID: "UUSER1", text: "routed"}

	select {
	case msg := <-incoming:
		if msg.Content != "routed" {
			t.Errorf("expected 'routed', got %q (unmapped message leaked)", msg.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for routed message")
	}

	a.Close()
}

func TestSend_ReplyOverride(t *testing.T) {
	api := &mockDiscordAPI{}
	a := newTestAdapter(api, newMockEventSource())

	// Map agent to guild channel.
	a.mu.Lock()
	a.agentToChannel["agent-1"] = "CGUILD"
	a.channelToAgent["CGUILD"] = "agent-1"
	// Set reply override to a DM channel.
	a.replyOverride["agent-1"] = "CDM456"
	a.mu.Unlock()

	msg := types.Message{Content: "reply in DM"}
	if err := a.Send(context.Background(), "agent-1", msg); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.sendCalls) != 1 {
		t.Fatalf("expected 1 send call, got %d", len(api.sendCalls))
	}
	if api.sendCalls[0].channelID != "CDM456" {
		t.Errorf("Send channelID = %q, want %q", api.sendCalls[0].channelID, "CDM456")
	}
}

func TestTypingIndicator(t *testing.T) {
	api := &mockDiscordAPI{}
	events := newMockEventSource()
	a := newTestAdapter(api, events)

	// Map agent to a channel.
	a.mu.Lock()
	a.agentToChannel["agent-1"] = "C12345"
	a.channelToAgent["C12345"] = "agent-1"
	a.mu.Unlock()

	incoming, err := a.Receive(context.Background())
	if err != nil {
		t.Fatalf("Receive() error = %v", err)
	}

	// Send a message — should trigger typing indicator.
	events.ch <- discordEvent{channelID: "C12345", userID: "UUSER1", text: "hello"}

	select {
	case <-incoming:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for message")
	}

	// Give typing goroutine a moment to fire.
	time.Sleep(50 * time.Millisecond)

	api.mu.Lock()
	typingCount := len(api.typingCalls)
	api.mu.Unlock()
	if typingCount == 0 {
		t.Error("expected at least 1 ChannelTyping call")
	}

	// Send reply — should stop typing.
	if err := a.Send(context.Background(), "agent-1", types.Message{Content: "reply"}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	// Verify typing was cancelled.
	a.mu.RLock()
	_, hasTyping := a.typingCancel["agent-1"]
	a.mu.RUnlock()
	if hasTyping {
		t.Error("expected typing to be cancelled after Send")
	}

	a.Close()
}

func TestSend_RepliesToSourceChannel(t *testing.T) {
	api := &mockDiscordAPI{}
	events := newMockEventSource()
	a := newTestAdapter(api, events)

	// Map agent to two channels (C1 is default).
	a.mu.Lock()
	a.agentToChannel["agent-1"] = "C1"
	a.channelToAgent["C1"] = "agent-1"
	a.channelToAgent["C2"] = "agent-1"
	a.mu.Unlock()

	incoming, err := a.Receive(context.Background())
	if err != nil {
		t.Fatalf("Receive() error = %v", err)
	}

	// Message arrives from C2 — Send should reply to C2, not default C1.
	events.ch <- discordEvent{channelID: "C2", userID: "UUSER1", text: "hello from C2"}

	select {
	case <-incoming:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for message")
	}

	if err := a.Send(context.Background(), "agent-1", types.Message{Content: "reply"}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.sendCalls) != 1 {
		t.Fatalf("expected 1 send call, got %d", len(api.sendCalls))
	}
	if api.sendCalls[0].channelID != "C2" {
		t.Errorf("Send channelID = %q, want %q", api.sendCalls[0].channelID, "C2")
	}

	a.Close()
}

func TestReceive_BotMentionStripped(t *testing.T) {
	events := newMockEventSource()
	a := newTestAdapter(&mockDiscordAPI{}, events)

	// Map one agent.
	a.mu.Lock()
	a.agentToChannel["agent-1"] = "CMAPPED"
	a.channelToAgent["CMAPPED"] = "agent-1"
	a.mu.Unlock()

	incoming, err := a.Receive(context.Background())
	if err != nil {
		t.Fatalf("Receive() error = %v", err)
	}

	// Send event from mapped channel with mention stripped in text
	// (as gatewaySource would do).
	events.ch <- discordEvent{
		channelID:    "CMAPPED",
		userID:       "UUSER1",
		text:         "hello",
		botMentioned: true,
	}

	select {
	case msg := <-incoming:
		if msg.Content != "hello" {
			t.Errorf("Content = %q, want %q", msg.Content, "hello")
		}
		if msg.AgentID != "agent-1" {
			t.Errorf("AgentID = %q, want %q", msg.AgentID, "agent-1")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for message")
	}

	a.Close()
}

func TestSanitizeChannelName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Test Agent", "kyvik-test-agent"},
		{"My Cool Bot", "kyvik-my-cool-bot"},
		{"agent_with_underscores", "kyvik-agentwithunderscores"},
		{"UPPERCASE", "kyvik-uppercase"},
		{"special!@#$%chars", "kyvik-specialchars"},
		{"  extra  spaces  ", "kyvik-extra-spaces"},
		{"already-hyphenated", "kyvik-already-hyphenated"},
		{"a", "kyvik-a"},
		// Long name should be truncated to 100 chars.
		{
			"this-is-a-really-long-agent-name-that-should-be-truncated-because-discord-has-a-maximum-channel-name-length-of-100",
			"kyvik-this-is-a-really-long-agent-name-that-should-be-truncated-because-discord-has-a-maximum-channe",
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizeChannelName(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeChannelName(%q) = %q, want %q", tt.input, got, tt.want)
			}
			if len(got) > 100 {
				t.Errorf("sanitizeChannelName(%q) length = %d, exceeds 100", tt.input, len(got))
			}
		})
	}
}
