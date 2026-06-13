package slack

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/config"
	"github.com/kkjorsvik/kyvik/pkg/types"
	goslack "github.com/slack-go/slack"
)

const testBotUserID = "UBOT123"

// --- Mock types ---

type postMessageCall struct {
	channelID string
	text      string
}

type createConversationCall struct {
	name string
}

type mockSlackAPI struct {
	mu sync.Mutex

	postMessageCalls          []postMessageCall
	postMessageErr            error
	createConversationCalls   []createConversationCall
	createConversationResult  *goslack.Channel
	createConversationErr     error
	archiveConversationCalls  []string
	archiveConversationErr    error
	getConversationInfoCalls  []string
	getConversationInfoResult *goslack.Channel
	getConversationInfoErr    error
	inviteUsersCalls          []string
	inviteUsersErr            error
	authTestResult            *goslack.AuthTestResponse
	authTestErr               error
}

func (m *mockSlackAPI) PostMessage(channelID string, options ...goslack.MsgOption) (string, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Extract text from options by examining the call pattern.
	// For tests we record the channelID; the text is set via MsgOptionText.
	m.postMessageCalls = append(m.postMessageCalls, postMessageCall{channelID: channelID})
	return "", "", m.postMessageErr
}

func (m *mockSlackAPI) CreateConversation(params goslack.CreateConversationParams) (*goslack.Channel, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.createConversationCalls = append(m.createConversationCalls, createConversationCall{name: params.ChannelName})
	if m.createConversationErr != nil {
		return nil, m.createConversationErr
	}
	if m.createConversationResult != nil {
		return m.createConversationResult, nil
	}
	return &goslack.Channel{GroupConversation: goslack.GroupConversation{
		Conversation: goslack.Conversation{ID: "C" + params.ChannelName},
		Name:         params.ChannelName,
	}}, nil
}

func (m *mockSlackAPI) ArchiveConversation(channelID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.archiveConversationCalls = append(m.archiveConversationCalls, channelID)
	return m.archiveConversationErr
}

func (m *mockSlackAPI) GetConversationInfo(input *goslack.GetConversationInfoInput) (*goslack.Channel, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getConversationInfoCalls = append(m.getConversationInfoCalls, input.ChannelID)
	if m.getConversationInfoErr != nil {
		return nil, m.getConversationInfoErr
	}
	if m.getConversationInfoResult != nil {
		return m.getConversationInfoResult, nil
	}
	return &goslack.Channel{GroupConversation: goslack.GroupConversation{
		Conversation: goslack.Conversation{ID: input.ChannelID},
	}}, nil
}

func (m *mockSlackAPI) InviteUsersToConversation(channelID string, users ...string) (*goslack.Channel, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inviteUsersCalls = append(m.inviteUsersCalls, channelID)
	return &goslack.Channel{}, m.inviteUsersErr
}

func (m *mockSlackAPI) AuthTest() (*goslack.AuthTestResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.authTestErr != nil {
		return nil, m.authTestErr
	}
	if m.authTestResult != nil {
		return m.authTestResult, nil
	}
	return &goslack.AuthTestResponse{UserID: testBotUserID}, nil
}

func (m *mockSlackAPI) GetFile(_ string, _ io.Writer) error {
	return nil
}

// mockEventSource implements eventSource for tests.
type mockEventSource struct {
	ch  chan slackEvent
	err error
}

func newMockEventSource() *mockEventSource {
	return &mockEventSource{ch: make(chan slackEvent, 64)}
}

func (m *mockEventSource) Start(ctx context.Context) (<-chan slackEvent, error) {
	if m.err != nil {
		return nil, m.err
	}
	out := make(chan slackEvent, 64)
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

// newTestAdapter creates an adapter with mocks, bypassing real Slack clients.
func newTestAdapter(api *mockSlackAPI, events *mockEventSource) *SlackAdapter {
	a, _ := New(config.SlackConfig{}, WithAPI(api), WithEventSource(events), WithBotUserID(testBotUserID))
	return a
}

// --- Tests ---

func TestName(t *testing.T) {
	a := newTestAdapter(&mockSlackAPI{}, newMockEventSource())
	if got := a.Name(); got != "slack" {
		t.Errorf("Name() = %q, want %q", got, "slack")
	}
}

func TestProvisionAgent_AutoProvision(t *testing.T) {
	api := &mockSlackAPI{}
	a := newTestAdapter(api, newMockEventSource())

	cfg := types.AgentConfig{
		ID:   "agent-1",
		Name: "Test Agent",
		Channels: []types.ChannelMapping{{
			ChannelType:   "slack",
			AutoProvision: true,
		}},
	}

	if err := a.ProvisionAgent(context.Background(), cfg); err != nil {
		t.Fatalf("ProvisionAgent() error = %v", err)
	}

	// Verify CreateConversation was called with sanitized name.
	if len(api.createConversationCalls) != 1 {
		t.Fatalf("expected 1 CreateConversation call, got %d", len(api.createConversationCalls))
	}
	if got := api.createConversationCalls[0].name; got != "kyvik-test-agent" {
		t.Errorf("CreateConversation name = %q, want %q", got, "kyvik-test-agent")
	}

	// Verify InviteUsersToConversation was called.
	if len(api.inviteUsersCalls) != 1 {
		t.Fatalf("expected 1 InviteUsers call, got %d", len(api.inviteUsersCalls))
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
	api := &mockSlackAPI{}
	a := newTestAdapter(api, newMockEventSource())

	cfg := types.AgentConfig{
		ID:   "agent-2",
		Name: "Manual Agent",
		Channels: []types.ChannelMapping{{
			ChannelType:   "slack",
			ChannelID:     "C12345",
			AutoProvision: false,
		}},
	}

	if err := a.ProvisionAgent(context.Background(), cfg); err != nil {
		t.Fatalf("ProvisionAgent() error = %v", err)
	}

	// Verify GetConversationInfo was called.
	if len(api.getConversationInfoCalls) != 1 {
		t.Fatalf("expected 1 GetConversationInfo call, got %d", len(api.getConversationInfoCalls))
	}
	if got := api.getConversationInfoCalls[0]; got != "C12345" {
		t.Errorf("GetConversationInfo channelID = %q, want %q", got, "C12345")
	}

	// Verify mapping was stored without auto-provisioned flag.
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.agentToChannel["agent-2"] != "C12345" {
		t.Errorf("agentToChannel[agent-2] = %q, want %q", a.agentToChannel["agent-2"], "C12345")
	}
	if a.autoProvisioned["C12345"] {
		t.Error("manual channel should not be marked as auto-provisioned")
	}
}

func TestProvisionAgent_ManualMissingChannelID(t *testing.T) {
	a := newTestAdapter(&mockSlackAPI{}, newMockEventSource())

	cfg := types.AgentConfig{
		ID:   "agent-3",
		Name: "No Channel",
		Channels: []types.ChannelMapping{{
			ChannelType:   "slack",
			AutoProvision: false,
		}},
	}

	err := a.ProvisionAgent(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error for missing channel_id")
	}
}

func TestProvisionAgent_CreateError(t *testing.T) {
	api := &mockSlackAPI{
		createConversationErr: errors.New("name_taken"),
	}
	a := newTestAdapter(api, newMockEventSource())

	cfg := types.AgentConfig{
		ID:   "agent-4",
		Name: "Dupe Agent",
		Channels: []types.ChannelMapping{{
			ChannelType:   "slack",
			AutoProvision: true,
		}},
	}

	err := a.ProvisionAgent(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error from CreateConversation")
	}
	if !errors.Is(err, api.createConversationErr) {
		t.Errorf("expected wrapped error containing %q, got %q", api.createConversationErr, err)
	}
}

func TestDeprovisionAgent_AutoProvisioned(t *testing.T) {
	api := &mockSlackAPI{}
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

	// Verify channel was archived.
	if len(api.archiveConversationCalls) != 1 {
		t.Fatalf("expected 1 ArchiveConversation call, got %d", len(api.archiveConversationCalls))
	}
	if api.archiveConversationCalls[0] != "CAUTO1" {
		t.Errorf("ArchiveConversation channelID = %q, want %q", api.archiveConversationCalls[0], "CAUTO1")
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
	api := &mockSlackAPI{}
	a := newTestAdapter(api, newMockEventSource())

	// Pre-populate mappings as if manually provisioned.
	a.mu.Lock()
	a.agentToChannel["agent-2"] = "CMANUAL"
	a.channelToAgent["CMANUAL"] = "agent-2"
	a.mu.Unlock()

	if err := a.DeprovisionAgent(context.Background(), "agent-2"); err != nil {
		t.Fatalf("DeprovisionAgent() error = %v", err)
	}

	// Should NOT archive.
	if len(api.archiveConversationCalls) != 0 {
		t.Errorf("expected 0 ArchiveConversation calls, got %d", len(api.archiveConversationCalls))
	}

	// Verify maps cleaned up.
	a.mu.RLock()
	defer a.mu.RUnlock()
	if _, ok := a.agentToChannel["agent-2"]; ok {
		t.Error("agentToChannel should not contain agent-2")
	}
}

func TestDeprovisionAgent_NotProvisioned(t *testing.T) {
	a := newTestAdapter(&mockSlackAPI{}, newMockEventSource())

	err := a.DeprovisionAgent(context.Background(), "unknown-agent")
	if !errors.Is(err, types.ErrNotProvisioned) {
		t.Errorf("expected ErrNotProvisioned, got %v", err)
	}
}

func TestSend(t *testing.T) {
	api := &mockSlackAPI{}
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

	if len(api.postMessageCalls) != 1 {
		t.Fatalf("expected 1 PostMessage call, got %d", len(api.postMessageCalls))
	}
	if api.postMessageCalls[0].channelID != "C12345" {
		t.Errorf("PostMessage channelID = %q, want %q", api.postMessageCalls[0].channelID, "C12345")
	}
}

func TestSend_NotProvisioned(t *testing.T) {
	a := newTestAdapter(&mockSlackAPI{}, newMockEventSource())

	err := a.Send(context.Background(), "unknown-agent", types.Message{Content: "hi"})
	if !errors.Is(err, types.ErrNotProvisioned) {
		t.Errorf("expected ErrNotProvisioned, got %v", err)
	}
}

func TestReceive_RoutesToAgent(t *testing.T) {
	events := newMockEventSource()
	a := newTestAdapter(&mockSlackAPI{}, events)

	// Pre-populate mapping.
	a.mu.Lock()
	a.agentToChannel["agent-1"] = "C12345"
	a.channelToAgent["C12345"] = "agent-1"
	a.mu.Unlock()

	incoming, err := a.Receive(context.Background())
	if err != nil {
		t.Fatalf("Receive() error = %v", err)
	}

	events.ch <- slackEvent{channelID: "C12345", userID: "UUSER1", text: "hello agent"}

	select {
	case msg := <-incoming:
		if msg.AgentID != "agent-1" {
			t.Errorf("AgentID = %q, want %q", msg.AgentID, "agent-1")
		}
		if msg.Content != "hello agent" {
			t.Errorf("Content = %q, want %q", msg.Content, "hello agent")
		}
		if msg.ChannelType != "slack" {
			t.Errorf("ChannelType = %q, want %q", msg.ChannelType, "slack")
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
	a := newTestAdapter(&mockSlackAPI{}, events)

	incoming, err := a.Receive(context.Background())
	if err != nil {
		t.Fatalf("Receive() error = %v", err)
	}

	// Send event from unmapped channel.
	events.ch <- slackEvent{channelID: "CUNKNOWN", userID: "UUSER1", text: "lost message"}

	// Also send one from a mapped channel to verify routing still works.
	a.mu.Lock()
	a.agentToChannel["agent-1"] = "CMAPPED"
	a.channelToAgent["CMAPPED"] = "agent-1"
	a.mu.Unlock()

	events.ch <- slackEvent{channelID: "CMAPPED", userID: "UUSER1", text: "routed message"}

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
	a := newTestAdapter(&mockSlackAPI{}, events)

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
	events.ch <- slackEvent{channelID: "C12345", userID: testBotUserID, text: "bot echo"}

	// Send a real user message.
	events.ch <- slackEvent{channelID: "C12345", userID: "UHUMAN", text: "human message"}

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
	a := newTestAdapter(&mockSlackAPI{}, events)

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
	a := newTestAdapter(&mockSlackAPI{}, events)

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
		// Long name should be truncated to 80 chars.
		{
			"this-is-a-really-long-agent-name-that-should-be-truncated-because-slack-has-a-maximum-channel-name-length",
			"kyvik-this-is-a-really-long-agent-name-that-should-be-truncated-because-slack-ha",
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizeChannelName(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeChannelName(%q) = %q, want %q", tt.input, got, tt.want)
			}
			if len(got) > 80 {
				t.Errorf("sanitizeChannelName(%q) length = %d, exceeds 80", tt.input, len(got))
			}
		})
	}
}
