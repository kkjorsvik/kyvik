package notifications

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/slack-go/slack"
)

// EventsConfig controls which event types are forwarded to Slack.
type EventsConfig struct {
	CircuitBreaker    bool
	AgentError        bool
	SpendingThreshold int // percentage of budget; 0 = disabled
	BackupStatus      bool
	SecurityAlerts    bool
	KeyFailure        bool
	ChannelFailure    bool
}

// SlackNotifier sends operator notifications to a Slack channel.
type SlackNotifier struct {
	client      *slack.Client
	channelID   string
	channelName string
	limiter     *RateLimiter
	events      EventsConfig
}

// SlackOption configures a SlackNotifier for testing.
type SlackOption func(*SlackNotifier)

// WithSlackClient overrides the Slack client (for testing).
func WithSlackClient(c *slack.Client) SlackOption {
	return func(sn *SlackNotifier) {
		sn.client = c
	}
}

// WithChannelID sets the channel ID directly, skipping resolution in Start().
func WithChannelID(id string) SlackOption {
	return func(sn *SlackNotifier) {
		sn.channelID = id
	}
}

// NewSlackNotifier creates a SlackNotifier.
func NewSlackNotifier(botToken, channelName string, events EventsConfig, opts ...SlackOption) *SlackNotifier {
	sn := &SlackNotifier{
		client:      slack.New(botToken),
		channelName: channelName,
		events:      events,
	}
	for _, opt := range opts {
		opt(sn)
	}
	return sn
}

// Start resolves the channel name to an ID and initializes the rate limiter.
func (sn *SlackNotifier) Start() error {
	sn.limiter = NewRateLimiter(5*time.Minute, &LogNotifier{})

	if sn.channelID != "" {
		return nil
	}

	// Resolve channel name to ID.
	name := sn.channelName
	if len(name) > 0 && name[0] == '#' {
		name = name[1:]
	}

	cursor := ""
	for {
		params := &slack.GetConversationsParameters{
			Types:           []string{"public_channel", "private_channel"},
			Limit:           200,
			Cursor:          cursor,
			ExcludeArchived: true,
		}
		channels, nextCursor, err := sn.client.GetConversations(params)
		if err != nil {
			slog.Warn("failed to resolve Slack channel, using name as fallback",
				"channel", sn.channelName, "error", err)
			sn.channelID = sn.channelName
			return nil
		}
		for _, ch := range channels {
			if ch.Name == name {
				sn.channelID = ch.ID
				slog.Info("resolved Slack notification channel", "name", sn.channelName, "id", sn.channelID)
				return nil
			}
		}
		if nextCursor == "" {
			break
		}
		cursor = nextCursor
	}

	// Channel not found — use name as fallback.
	slog.Warn("Slack channel not found, using name as fallback", "channel", sn.channelName)
	sn.channelID = sn.channelName
	return nil
}

// Send posts an event to Slack if it passes the event filter and rate limiter.
func (sn *SlackNotifier) Send(ctx context.Context, event Event) error {
	if !sn.isEnabled(event.Type) {
		return nil
	}

	if sn.limiter != nil && !sn.limiter.Allow(event.Type, event.Agent) {
		return nil
	}

	blocks := sn.buildBlocks(event)
	_, _, err := sn.client.PostMessageContext(ctx, sn.channelID,
		slack.MsgOptionBlocks(blocks...),
	)
	if err != nil {
		slog.Warn("failed to send Slack notification", "error", err, "type", event.Type)
		return err
	}
	return nil
}

// Stop shuts down the rate limiter.
func (sn *SlackNotifier) Stop() {
	if sn.limiter != nil {
		sn.limiter.Stop()
	}
}

func (sn *SlackNotifier) isEnabled(eventType string) bool {
	switch eventType {
	case "circuit_breaker":
		return sn.events.CircuitBreaker
	case "agent_error":
		return sn.events.AgentError
	case "spending_alert":
		return sn.events.SpendingThreshold > 0
	case "backup_status":
		return sn.events.BackupStatus
	case "security_alert":
		return sn.events.SecurityAlerts
	case "key_failure":
		return sn.events.KeyFailure
	case "channel_failure":
		return sn.events.ChannelFailure
	default:
		// Unknown event types are always allowed (e.g. rate limiter summaries).
		return true
	}
}

func (sn *SlackNotifier) buildBlocks(event Event) []slack.Block {
	emoji := severityEmoji(event.Severity)

	header := slack.NewHeaderBlock(
		slack.NewTextBlockObject(slack.PlainTextType, emoji+" "+event.Title, true, false),
	)

	agentText := "_system_"
	if event.Agent != "" {
		agentText = event.Agent
	}
	section := slack.NewSectionBlock(
		slack.NewTextBlockObject(slack.MarkdownType,
			fmt.Sprintf("*Agent:* %s\n%s", agentText, event.Detail), false, false),
		nil, nil,
	)

	context := slack.NewContextBlock("",
		slack.NewTextBlockObject(slack.MarkdownType,
			fmt.Sprintf("Severity: *%s* | %s", event.Severity, event.Timestamp.Format(time.RFC3339)),
			false, false),
	)

	return []slack.Block{header, section, context}
}

func severityEmoji(severity string) string {
	switch severity {
	case "critical":
		return "\U0001F534" // red circle
	case "warning":
		return "\u26A0\uFE0F" // warning sign
	case "info":
		return "\U0001F535" // blue circle
	default:
		return "\U0001F535"
	}
}
