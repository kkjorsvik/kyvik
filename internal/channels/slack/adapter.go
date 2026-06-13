// Package slack implements the channels.Adapter interface for Slack
// using Socket Mode for real-time event delivery.
package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/kkjorsvik/kyvik/internal/attachments"
	"github.com/kkjorsvik/kyvik/internal/channels"
	"github.com/kkjorsvik/kyvik/internal/config"
	"github.com/kkjorsvik/kyvik/pkg/types"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

// Compile-time interface check.
var _ channels.Adapter = (*SlackAdapter)(nil)

// slackAPI abstracts the Slack Web API methods the adapter needs.
// *slack.Client satisfies this implicitly.
type slackAPI interface {
	PostMessage(channelID string, options ...slack.MsgOption) (string, string, error)
	CreateConversation(params slack.CreateConversationParams) (*slack.Channel, error)
	ArchiveConversation(channelID string) error
	GetConversationInfo(input *slack.GetConversationInfoInput) (*slack.Channel, error)
	InviteUsersToConversation(channelID string, users ...string) (*slack.Channel, error)
	AuthTest() (*slack.AuthTestResponse, error)
	GetFile(downloadURL string, writer io.Writer) error
}

// slackEvent is an internal representation of a relevant Slack message event.
type slackEvent struct {
	channelID string
	userID    string
	text      string
	files     []slackFile
}

// slackFile represents a file attachment from a Slack message.
type slackFile struct {
	name        string
	mimetype    string
	size        int64
	downloadURL string
}

// eventSource abstracts Slack event delivery for testability.
type eventSource interface {
	Start(ctx context.Context) (<-chan slackEvent, error)
}

// socketModeSource wraps socketmode.Client to implement eventSource.
type socketModeSource struct {
	client *socketmode.Client
}

func (s *socketModeSource) Start(ctx context.Context) (<-chan slackEvent, error) {
	log := slog.With("channel", "slack")
	ch := make(chan slackEvent, 64)

	log.Info("socket mode connecting")

	go func() {
		defer close(ch)
		// Run blocks until the context is cancelled or an unrecoverable error occurs.
		go s.client.RunContext(ctx)

		for {
			select {
			case <-ctx.Done():
				return
			case evt, ok := <-s.client.Events:
				if !ok {
					return
				}
				if evt.Type != socketmode.EventTypeEventsAPI {
					log.Debug("skipping non-events-api event", "type", evt.Type)
					continue
				}

				eventsAPIEvent, ok := evt.Data.(slackevents.EventsAPIEvent)
				if !ok {
					log.Warn("unexpected data type in events API event")
					continue
				}
				s.client.Ack(*evt.Request)

				if eventsAPIEvent.Type != slackevents.CallbackEvent {
					log.Debug("skipping non-callback event", "type", eventsAPIEvent.Type)
					continue
				}

				innerEvent := eventsAPIEvent.InnerEvent

				var se slackEvent
				switch ev := innerEvent.Data.(type) {
				case *slackevents.MessageEvent:
					se = slackEvent{channelID: ev.Channel, userID: ev.User, text: ev.Text}
					// Extract files from the raw event payload since
					// MessageEvent doesn't have a Files field.
					if evt.Request != nil {
						se.files = extractFilesFromPayload(evt.Request.Payload)
					}
				case *slackevents.AppMentionEvent:
					se = slackEvent{channelID: ev.Channel, userID: ev.User, text: ev.Text}
				default:
					log.Warn("unexpected inner event type", "type", innerEvent.Type)
					continue
				}

				log.Debug("socket event received", "slack_channel", se.channelID, "user", se.userID, "event_type", innerEvent.Type)

				select {
				case ch <- se:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return ch, nil
}

// SlackAdapter implements channels.Adapter for Slack.
type SlackAdapter struct {
	api              slackAPI
	events           eventSource
	botUserID        string
	botToken         string // stored for attachment download auth
	autoProvision    bool
	dedicatedAgentID string // when set, all incoming messages route to this agent

	attachmentSvc *attachments.Service

	mu              sync.RWMutex
	agentToChannel  map[string]string // agentID → Slack channel ID
	channelToAgent  map[string]string // Slack channel ID → agentID
	autoProvisioned map[string]bool   // channelID → true if we created it

	incoming chan channels.IncomingMessage
	cancel   context.CancelFunc
	done     chan struct{}
	closed   bool
}

// Option configures a SlackAdapter.
type Option func(*SlackAdapter)

// WithAPI overrides the Slack API client (for testing).
func WithAPI(api slackAPI) Option {
	return func(a *SlackAdapter) { a.api = api }
}

// WithEventSource overrides the event source (for testing).
func WithEventSource(es eventSource) Option {
	return func(a *SlackAdapter) { a.events = es }
}

// WithBotUserID sets the bot user ID, bypassing AuthTest (for testing).
func WithBotUserID(id string) Option {
	return func(a *SlackAdapter) { a.botUserID = id }
}

// WithAutoProvision overrides the auto-provision default.
func WithAutoProvision(b bool) Option {
	return func(a *SlackAdapter) { a.autoProvision = b }
}

// WithAgentID sets a dedicated agent ID. When set, all incoming messages
// are routed to this agent regardless of channel-to-agent mappings.
func WithAgentID(id string) Option {
	return func(a *SlackAdapter) { a.dedicatedAgentID = id }
}

// WithAttachmentService sets the shared attachment processing service.
func WithAttachmentService(svc *attachments.Service) Option {
	return func(a *SlackAdapter) { a.attachmentSvc = svc }
}

// New creates a new Slack adapter from configuration.
func New(cfg config.SlackConfig, opts ...Option) (*SlackAdapter, error) {
	a := &SlackAdapter{
		autoProvision:   cfg.AutoProvision,
		agentToChannel:  make(map[string]string),
		channelToAgent:  make(map[string]string),
		autoProvisioned: make(map[string]bool),
		incoming:        make(chan channels.IncomingMessage, 64),
		done:            make(chan struct{}),
	}

	for _, opt := range opts {
		opt(a)
	}

	// Build real clients only if not injected via options.
	if a.api == nil || a.events == nil {
		botToken := cfg.BotToken
		if botToken == "" {
			botToken = os.Getenv("KYVIK_SLACK_BOT_TOKEN")
		}
		appToken := cfg.AppToken
		if appToken == "" {
			appToken = os.Getenv("KYVIK_SLACK_APP_TOKEN")
		}

		if botToken == "" {
			return nil, fmt.Errorf("slack: bot token is required (config or KYVIK_SLACK_BOT_TOKEN)")
		}
		if appToken == "" {
			return nil, fmt.Errorf("slack: app token is required (config or KYVIK_SLACK_APP_TOKEN)")
		}

		a.botToken = botToken

		client := slack.New(botToken, slack.OptionAppLevelToken(appToken))

		if a.api == nil {
			a.api = client
		}
		if a.events == nil {
			a.events = &socketModeSource{
				client: socketmode.New(client),
			}
		}
	}

	// Discover bot user ID if not set by option.
	if a.botUserID == "" {
		resp, err := a.api.AuthTest()
		if err != nil {
			slog.Error("slack auth test failed", "channel", "slack", "error", err)
			return nil, fmt.Errorf("slack: auth test failed: %w", err)
		}
		a.botUserID = resp.UserID
		slog.Info("slack adapter initialized", "channel", "slack", "bot_user_id", a.botUserID)
	}

	return a, nil
}

// Name returns the channel identifier.
func (a *SlackAdapter) Name() string { return "slack" }

// BotUserID returns the resolved bot user ID for this adapter.
func (a *SlackAdapter) BotUserID() string { return a.botUserID }

// ProvisionAgent sets up an agent's Slack channel mapping.
func (a *SlackAdapter) ProvisionAgent(ctx context.Context, cfg types.AgentConfig) error {
	log := slog.With("channel", "slack", "agent_id", cfg.ID)

	// Find the slack channel mapping in the agent config.
	var mapping *types.ChannelMapping
	for i := range cfg.Channels {
		if cfg.Channels[i].ChannelType == "slack" {
			mapping = &cfg.Channels[i]
			break
		}
	}

	shouldAutoProvision := a.autoProvision
	if mapping != nil {
		shouldAutoProvision = mapping.AutoProvision
	}

	if shouldAutoProvision {
		name := sanitizeChannelName(cfg.Name)
		ch, err := a.api.CreateConversation(slack.CreateConversationParams{
			ChannelName: name,
		})
		if err != nil {
			return fmt.Errorf("slack: create conversation %q: %w", name, err)
		}

		if _, err := a.api.InviteUsersToConversation(ch.ID, a.botUserID); err != nil {
			// already_in_channel is not a real error
			if !strings.Contains(err.Error(), "already_in_channel") {
				return fmt.Errorf("slack: invite bot to %q: %w", ch.ID, err)
			}
		}

		a.mu.Lock()
		a.agentToChannel[cfg.ID] = ch.ID
		a.channelToAgent[ch.ID] = cfg.ID
		a.autoProvisioned[ch.ID] = true
		a.mu.Unlock()

		log.Info("agent provisioned (auto)", "slack_channel", ch.ID)
		return nil
	}

	// Manual provisioning: require a channel ID.
	if mapping == nil || mapping.ChannelID == "" {
		return fmt.Errorf("slack: channel_id required when auto_provision is disabled for agent %q", cfg.ID)
	}

	_, err := a.api.GetConversationInfo(&slack.GetConversationInfoInput{
		ChannelID: mapping.ChannelID,
	})
	if err != nil {
		return fmt.Errorf("slack: validate channel %q: %w", mapping.ChannelID, err)
	}

	a.mu.Lock()
	a.agentToChannel[cfg.ID] = mapping.ChannelID
	a.channelToAgent[mapping.ChannelID] = cfg.ID
	a.mu.Unlock()

	log.Info("agent provisioned (manual)", "slack_channel", mapping.ChannelID)
	return nil
}

// DeprovisionAgent removes an agent's Slack channel mapping.
func (a *SlackAdapter) DeprovisionAgent(ctx context.Context, agentID string) error {
	log := slog.With("channel", "slack", "agent_id", agentID)

	a.mu.Lock()
	channelID, ok := a.agentToChannel[agentID]
	if !ok {
		a.mu.Unlock()
		return types.ErrNotProvisioned
	}

	wasAutoProvisioned := a.autoProvisioned[channelID]
	delete(a.agentToChannel, agentID)
	delete(a.channelToAgent, channelID)
	delete(a.autoProvisioned, channelID)
	a.mu.Unlock()

	if wasAutoProvisioned {
		if err := a.api.ArchiveConversation(channelID); err != nil {
			return fmt.Errorf("slack: archive channel %q: %w", channelID, err)
		}
	}

	log.Info("agent deprovisioned", "slack_channel", channelID)
	return nil
}

// Send delivers a message from an agent to its mapped Slack channel.
func (a *SlackAdapter) Send(ctx context.Context, agentID string, msg types.Message) error {
	a.mu.RLock()
	channelID, ok := a.agentToChannel[agentID]
	a.mu.RUnlock()

	if !ok {
		return types.ErrNotProvisioned
	}

	_, _, err := a.api.PostMessage(channelID, slack.MsgOptionText(msg.Content, false))
	if err != nil {
		slog.Error("slack send failed", "channel", "slack", "agent_id", agentID, "slack_channel", channelID, "error", err)
		return fmt.Errorf("slack: post message to %q: %w", channelID, err)
	}
	slog.Debug("slack message sent", "channel", "slack", "agent_id", agentID, "slack_channel", channelID)
	return nil
}

// Receive returns a channel of incoming messages from Slack.
func (a *SlackAdapter) Receive(ctx context.Context) (<-chan channels.IncomingMessage, error) {
	log := slog.With("channel", "slack")

	a.mu.RLock()
	closed := a.closed
	a.mu.RUnlock()
	if closed {
		return nil, types.ErrAdapterClosed
	}

	ctx, cancel := context.WithCancel(ctx)

	a.mu.Lock()
	a.cancel = cancel
	a.mu.Unlock()

	events, err := a.events.Start(ctx)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("slack: start event source: %w", err)
	}

	log.Info("receive listener started")

	go func() {
		defer func() {
			close(a.done)
		}()

		for evt := range events {
			// Skip bot's own messages.
			if evt.userID == a.botUserID {
				log.Debug("skipping bot's own message", "slack_channel", evt.channelID)
				continue
			}

			var agentID string
			if a.dedicatedAgentID != "" {
				agentID = a.dedicatedAgentID
			} else {
				a.mu.RLock()
				mapped, ok := a.channelToAgent[evt.channelID]
				a.mu.RUnlock()

				if !ok {
					log.Warn("message from unmapped channel (no agent)", "slack_channel", evt.channelID, "user", evt.userID)
					continue
				}
				agentID = mapped
			}

			log.Debug("routing incoming message", "slack_channel", evt.channelID, "agent_id", agentID, "user", evt.userID)

			inMsg := channels.IncomingMessage{
				ChannelType: "slack",
				ChannelID:   evt.channelID,
				SenderID:    evt.userID,
				Content:     evt.text,
				AgentID:     agentID,
			}

			// Process Slack file attachments via the shared attachment service.
			if len(evt.files) > 0 && a.attachmentSvc != nil {
				var raw []attachments.RawAttachment
				for _, f := range evt.files {
					raw = append(raw, attachments.RawAttachment{
						URL:         f.downloadURL,
						Filename:    f.name,
						ContentType: f.mimetype,
						Size:        f.size,
						AuthHeader:  "Bearer " + a.botToken,
					})
				}
				processed, err := a.attachmentSvc.Process(ctx, agentID, raw)
				if err != nil {
					log.Error("attachment processing failed", "error", err)
				} else {
					inMsg.Attachments = processed
				}
			} else if len(evt.files) > 0 {
				log.Warn("attachment service not available, skipping file attachments")
			}

			select {
			case a.incoming <- inMsg:
			case <-ctx.Done():
				return
			}
		}
	}()

	return a.incoming, nil
}

// Close shuts down the adapter.
func (a *SlackAdapter) Close() error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	cancel := a.cancel
	a.mu.Unlock()

	if cancel != nil {
		cancel()

		select {
		case <-a.done:
		case <-time.After(5 * time.Second):
		}
	}

	return nil
}

// invalidChannelChars matches characters not allowed in Slack channel names.
var invalidChannelChars = regexp.MustCompile(`[^a-z0-9-]`)

// sanitizeChannelName converts an agent name to a valid Slack channel name.
// Slack channel rules: lowercase, hyphens and alphanumeric only, max 80 chars.
func sanitizeChannelName(agentName string) string {
	name := strings.ToLower(agentName)
	name = strings.ReplaceAll(name, " ", "-")
	name = invalidChannelChars.ReplaceAllString(name, "")

	// Remove consecutive hyphens.
	for strings.Contains(name, "--") {
		name = strings.ReplaceAll(name, "--", "-")
	}
	name = strings.Trim(name, "-")

	name = "kyvik-" + name

	if len(name) > 80 {
		name = name[:80]
		name = strings.TrimRight(name, "-")
	}

	return name
}

// extractFilesFromPayload parses the raw socket mode event payload JSON
// to extract file attachments. The slack-go events package does not include
// files on MessageEvent, so we parse them from the raw JSON.
func extractFilesFromPayload(payload json.RawMessage) []slackFile {
	var envelope struct {
		Event struct {
			Files []struct {
				Name               string `json:"name"`
				Mimetype           string `json:"mimetype"`
				Size               int64  `json:"size"`
				URLPrivateDownload string `json:"url_private_download"`
			} `json:"files"`
		} `json:"event"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil
	}
	var files []slackFile
	for _, f := range envelope.Event.Files {
		if f.URLPrivateDownload == "" {
			continue
		}
		files = append(files, slackFile{
			name:        f.Name,
			mimetype:    f.Mimetype,
			size:        f.Size,
			downloadURL: f.URLPrivateDownload,
		})
	}
	return files
}
