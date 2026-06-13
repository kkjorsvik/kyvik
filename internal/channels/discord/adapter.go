// Package discord implements the channels.Adapter interface for Discord
// using the Discord Gateway WebSocket for real-time event delivery.
package discord

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/kkjorsvik/kyvik/internal/attachments"
	"github.com/kkjorsvik/kyvik/internal/channels"
	"github.com/kkjorsvik/kyvik/internal/config"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// Compile-time interface check.
var _ channels.Adapter = (*DiscordAdapter)(nil)

// discordAPI abstracts the Discord API methods the adapter needs.
// *discordgo.Session satisfies this implicitly.
type discordAPI interface {
	ChannelMessageSend(channelID, content string, options ...discordgo.RequestOption) (*discordgo.Message, error)
	GuildChannelCreateComplex(guildID string, data discordgo.GuildChannelCreateData, options ...discordgo.RequestOption) (*discordgo.Channel, error)
	Channel(channelID string, options ...discordgo.RequestOption) (*discordgo.Channel, error)
	ChannelDelete(channelID string, options ...discordgo.RequestOption) (*discordgo.Channel, error)
	ChannelTyping(channelID string, options ...discordgo.RequestOption) error
}

// discordEvent is an internal representation of a relevant Discord message event.
type discordEvent struct {
	channelID    string
	userID       string
	text         string
	botMentioned bool
	attachments  []*discordgo.MessageAttachment
	embeds       []*discordgo.MessageEmbed
}

// eventSource abstracts Discord event delivery for testability.
type eventSource interface {
	Start(ctx context.Context) (<-chan discordEvent, error)
}

// gatewaySource wraps discordgo.Session to implement eventSource.
type gatewaySource struct {
	session   *discordgo.Session
	botUserID string
}

func (g *gatewaySource) Start(ctx context.Context) (<-chan discordEvent, error) {
	log := slog.With("channel", "discord")
	ch := make(chan discordEvent, 64)

	log.Info("discord gateway connecting")

	g.session.AddHandler(func(_ *discordgo.Session, m *discordgo.MessageCreate) {
		if m.Author == nil || m.Author.ID == g.botUserID {
			return
		}
		text := m.Content
		mentioned := false
		for _, u := range m.Mentions {
			if u.ID == g.botUserID {
				mentioned = true
				text = strings.ReplaceAll(text, "<@"+g.botUserID+">", "")
				text = strings.ReplaceAll(text, "<@!"+g.botUserID+">", "")
				text = strings.TrimSpace(text)
				break
			}
		}
		select {
		case ch <- discordEvent{channelID: m.ChannelID, userID: m.Author.ID, text: text, botMentioned: mentioned, attachments: m.Attachments, embeds: m.Embeds}:
		case <-ctx.Done():
		}
	})

	g.session.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages | discordgo.IntentsMessageContent

	if err := g.session.Open(); err != nil {
		// Do NOT close(ch) here — the handler closure above already holds a
		// reference to ch and is registered on the session. discordgo can fire
		// handlers during a partial connect or background reconnect even after
		// Open() returns an error, and sending to a closed channel panics.
		// The ctx.Done() case in the handler's select provides safe cancellation
		// once the caller cancels the context.
		return nil, fmt.Errorf("discord: open gateway: %w", err)
	}

	go func() {
		<-ctx.Done()
		g.session.Close()
		// Do not close(ch) — the handler may still be in-flight.
		// Receive() exits via ctx.Done() instead of relying on channel close.
	}()

	return ch, nil
}

// DiscordAdapter implements channels.Adapter for Discord.
type DiscordAdapter struct {
	api              discordAPI
	events           eventSource
	session          *discordgo.Session // nil when mocks are injected
	botUserID        string
	autoProvision    bool
	guildID          string // Discord server (guild) to operate in
	dedicatedAgentID string // when set, all incoming messages route to this agent

	mu              sync.RWMutex
	agentToChannel  map[string]string // agentID → Discord channel ID
	channelToAgent  map[string]string // Discord channel ID → agentID
	channelMode     map[string]string // Discord channel ID → "all" or "mention"
	autoProvisioned map[string]bool   // channelID → true if we created it
	replyOverride   map[string]string            // agentID → override reply channelID (for DMs/unmapped channels)
	typingCancel    map[string]context.CancelFunc // agentID → cancel typing indicator

	attachmentSvc *attachments.Service

	auth     DiscordAuthorizer  // nil means auth disabled
	authMode map[string]string  // agentID → "open" or "restricted"

	incoming chan channels.IncomingMessage
	cancel   context.CancelFunc
	done     chan struct{}
	closed   bool
}

// Option configures a DiscordAdapter.
type Option func(*DiscordAdapter)

// WithAPI overrides the Discord API client (for testing).
func WithAPI(api discordAPI) Option {
	return func(a *DiscordAdapter) { a.api = api }
}

// WithEventSource overrides the event source (for testing).
func WithEventSource(es eventSource) Option {
	return func(a *DiscordAdapter) { a.events = es }
}

// WithBotUserID sets the bot user ID, bypassing gateway discovery (for testing).
func WithBotUserID(id string) Option {
	return func(a *DiscordAdapter) { a.botUserID = id }
}

// WithAutoProvision overrides the auto-provision default.
func WithAutoProvision(b bool) Option {
	return func(a *DiscordAdapter) { a.autoProvision = b }
}

// WithAgentID sets a dedicated agent ID. When set, all incoming messages
// are routed to this agent regardless of channel-to-agent mappings.
func WithAgentID(id string) Option {
	return func(a *DiscordAdapter) { a.dedicatedAgentID = id }
}

// WithGuildID sets the Discord guild (server) ID.
func WithGuildID(id string) Option {
	return func(a *DiscordAdapter) { a.guildID = id }
}

// WithAttachmentService sets the shared attachment processing service.
func WithAttachmentService(svc *attachments.Service) Option {
	return func(a *DiscordAdapter) { a.attachmentSvc = svc }
}

// WithAuthorizer sets the Discord authorization store for pairing/allowlist flows.
func WithAuthorizer(auth DiscordAuthorizer) Option {
	return func(a *DiscordAdapter) { a.auth = auth }
}

// New creates a new Discord adapter from configuration.
func New(cfg config.DiscordConfig, opts ...Option) (*DiscordAdapter, error) {
	a := &DiscordAdapter{
		autoProvision:   cfg.AutoProvision,
		guildID:         cfg.GuildID,
		agentToChannel:  make(map[string]string),
		channelToAgent:  make(map[string]string),
		channelMode:     make(map[string]string),
		autoProvisioned: make(map[string]bool),
		replyOverride:   make(map[string]string),
		typingCancel:    make(map[string]context.CancelFunc),
		authMode:        make(map[string]string),
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
			botToken = os.Getenv("KYVIK_DISCORD_BOT_TOKEN")
		}
		if botToken == "" {
			return nil, fmt.Errorf("discord: bot token is required (config or KYVIK_DISCORD_BOT_TOKEN)")
		}

		session, err := discordgo.New("Bot " + botToken)
		if err != nil {
			return nil, fmt.Errorf("discord: create session: %w", err)
		}
		a.session = session

		if a.api == nil {
			a.api = session
		}
		if a.events == nil {
			// botUserID will be resolved after we know it.
			a.events = &gatewaySource{session: session}
		}
	}

	// Discover bot user ID via REST — no WebSocket needed.
	// Opening the gateway twice (once here, once in Start) triggers Discord's
	// IDENTIFY rate-limit (1 per 5 s) causing repeated connection failures.
	if a.botUserID == "" && a.session != nil {
		u, err := a.session.User("@me")
		if err != nil {
			return nil, fmt.Errorf("discord: resolve bot user ID: %w", err)
		}
		a.botUserID = u.ID
		slog.Info("discord adapter initialized", "channel", "discord", "bot_user_id", a.botUserID)
	}

	// Set bot user ID on gateway source if it exists.
	if gs, ok := a.events.(*gatewaySource); ok {
		gs.botUserID = a.botUserID
	}

	return a, nil
}

// Name returns the channel identifier.
func (a *DiscordAdapter) Name() string { return "discord" }

// BotUserID returns the resolved bot user ID for this adapter.
func (a *DiscordAdapter) BotUserID() string { return a.botUserID }

// ProvisionAgent sets up an agent's Discord channel mapping.
func (a *DiscordAdapter) ProvisionAgent(ctx context.Context, cfg types.AgentConfig) error {
	log := slog.With("channel", "discord", "agent_id", cfg.ID)

	if a.autoProvision && a.guildID != "" {
		name := sanitizeChannelName(cfg.Name)
		ch, err := a.api.GuildChannelCreateComplex(a.guildID, discordgo.GuildChannelCreateData{
			Name: name,
			Type: discordgo.ChannelTypeGuildText,
		})
		if err != nil {
			return fmt.Errorf("discord: create channel %q: %w", name, err)
		}

		a.mu.Lock()
		a.agentToChannel[cfg.ID] = ch.ID
		a.channelToAgent[ch.ID] = cfg.ID
		a.autoProvisioned[ch.ID] = true
		if cfg.DiscordAuthMode != "" {
			a.authMode[cfg.ID] = cfg.DiscordAuthMode
		}
		a.mu.Unlock()

		log.Info("agent provisioned (auto)", "discord_channel", ch.ID)
		return nil
	}

	// Manual provisioning: require at least one channel ID (comma-separated).
	// Format: "channelID" or "channelID:mode" where mode is "all" (default) or "mention".
	if cfg.DiscordChannelID == "" {
		return fmt.Errorf("discord: discord_channel_id required when auto_provision is disabled for agent %q", cfg.ID)
	}

	entries := parseChannelEntries(cfg.DiscordChannelID)
	if len(entries) == 0 {
		return fmt.Errorf("discord: no valid channel IDs in %q for agent %q", cfg.DiscordChannelID, cfg.ID)
	}

	for _, e := range entries {
		if _, err := a.api.Channel(e.id); err != nil {
			return fmt.Errorf("discord: validate channel %q: %w", e.id, err)
		}
	}

	a.mu.Lock()
	a.agentToChannel[cfg.ID] = entries[0].id // first channel is default for Send
	for _, e := range entries {
		a.channelToAgent[e.id] = cfg.ID
		a.channelMode[e.id] = e.mode
	}
	if cfg.DiscordAuthMode != "" {
		a.authMode[cfg.ID] = cfg.DiscordAuthMode
	}
	a.mu.Unlock()

	log.Info("agent provisioned (manual)", "discord_channels", entries)
	return nil
}

// UpdateAuthMode updates the in-memory auth mode for an agent without re-provisioning.
func (a *DiscordAdapter) UpdateAuthMode(agentID, mode string) {
	a.mu.Lock()
	a.authMode[agentID] = mode
	a.mu.Unlock()
}

// DeprovisionAgent removes an agent's Discord channel mapping.
func (a *DiscordAdapter) DeprovisionAgent(ctx context.Context, agentID string) error {
	log := slog.With("channel", "discord", "agent_id", agentID)

	a.mu.Lock()
	_, ok := a.agentToChannel[agentID]
	if !ok {
		a.mu.Unlock()
		return types.ErrNotProvisioned
	}

	// Collect all channels mapped to this agent.
	var autoChannels []string
	for chID, aID := range a.channelToAgent {
		if aID == agentID {
			if a.autoProvisioned[chID] {
				autoChannels = append(autoChannels, chID)
			}
			delete(a.channelToAgent, chID)
			delete(a.channelMode, chID)
			delete(a.autoProvisioned, chID)
		}
	}
	delete(a.agentToChannel, agentID)
	delete(a.replyOverride, agentID)
	a.mu.Unlock()

	for _, chID := range autoChannels {
		if _, err := a.api.ChannelDelete(chID); err != nil {
			return fmt.Errorf("discord: delete channel %q: %w", chID, err)
		}
	}

	log.Info("agent deprovisioned", "channels_removed", len(autoChannels)+1)
	return nil
}

// startTyping begins sending typing indicators in a channel for an agent.
// It cancels any previous typing indicator for the same agent.
func (a *DiscordAdapter) startTyping(ctx context.Context, agentID, channelID string) {
	a.mu.Lock()
	// Cancel any existing typing for this agent.
	if cancel, ok := a.typingCancel[agentID]; ok {
		cancel()
	}
	typingCtx, cancel := context.WithCancel(ctx)
	a.typingCancel[agentID] = cancel
	a.mu.Unlock()

	go func() {
		// Send initial typing indicator.
		_ = a.api.ChannelTyping(channelID)

		// Discord typing indicator lasts ~10s; refresh every 8s.
		ticker := time.NewTicker(8 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-typingCtx.Done():
				return
			case <-ticker.C:
				_ = a.api.ChannelTyping(channelID)
			}
		}
	}()
}

// stopTyping cancels the typing indicator for an agent.
func (a *DiscordAdapter) stopTyping(agentID string) {
	a.mu.Lock()
	if cancel, ok := a.typingCancel[agentID]; ok {
		cancel()
		delete(a.typingCancel, agentID)
	}
	a.mu.Unlock()
}

// Send delivers a message from an agent to its mapped Discord channel.
func (a *DiscordAdapter) Send(ctx context.Context, agentID string, msg types.Message) error {
	// Stop typing indicator — the agent is about to send its response.
	a.stopTyping(agentID)

	a.mu.RLock()
	channelID, ok := a.agentToChannel[agentID]
	if override, has := a.replyOverride[agentID]; has {
		channelID = override
		ok = true
	}
	a.mu.RUnlock()

	if !ok {
		return types.ErrNotProvisioned
	}

	chunks := splitMessage(msg.Content, 2000)
	for _, chunk := range chunks {
		if _, err := a.api.ChannelMessageSend(channelID, chunk); err != nil {
			slog.Error("discord send failed", "channel", "discord", "agent_id", agentID, "discord_channel", channelID, "error", err)
			return fmt.Errorf("discord: send message to %q: %w", channelID, err)
		}
	}
	slog.Debug("discord message sent", "channel", "discord", "agent_id", agentID, "discord_channel", channelID, "chunks", len(chunks))
	return nil
}

// splitMessage splits text into chunks of at most maxLen runes, breaking on
// newlines where possible to avoid cutting mid-sentence.
func splitMessage(text string, maxLen int) []string {
	if len([]rune(text)) <= maxLen {
		return []string{text}
	}
	var chunks []string
	runes := []rune(text)
	for len(runes) > 0 {
		if len(runes) <= maxLen {
			chunks = append(chunks, string(runes))
			break
		}
		// Try to break on a newline within the allowed window.
		end := maxLen
		if cut := lastNewline(runes[:end]); cut > 0 {
			end = cut + 1 // include the newline in the prior chunk
		}
		chunks = append(chunks, string(runes[:end]))
		runes = runes[end:]
	}
	return chunks
}

// lastNewline returns the index of the last '\n' in runes, or -1 if not found.
func lastNewline(runes []rune) int {
	for i := len(runes) - 1; i >= 0; i-- {
		if runes[i] == '\n' {
			return i
		}
	}
	return -1
}

// Receive returns a channel of incoming messages from Discord.
func (a *DiscordAdapter) Receive(ctx context.Context) (<-chan channels.IncomingMessage, error) {
	log := slog.With("channel", "discord")

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
		return nil, fmt.Errorf("discord: start event source: %w", err)
	}

	log.Info("receive listener started")

	go func() {
		defer func() {
			close(a.done)
		}()

		for {
			var evt discordEvent
			select {
			case evt = <-events:
			case <-ctx.Done():
				return
			}

			// Skip bot's own messages.
			if evt.userID == a.botUserID {
				log.Debug("skipping bot's own message", "discord_channel", evt.channelID)
				continue
			}

			var agentID string
			if a.dedicatedAgentID != "" {
				agentID = a.dedicatedAgentID
			} else {
				a.mu.Lock()
				mapped, ok := a.channelToAgent[evt.channelID]
				if !ok {
					a.mu.Unlock()
					log.Debug("message from unmapped channel, ignoring",
						"discord_channel", evt.channelID, "user", evt.userID)
					continue
				}
				// If channel is mention-only, skip messages that don't @mention the bot.
				if a.channelMode[evt.channelID] == "mention" && !evt.botMentioned {
					a.mu.Unlock()
					log.Debug("mention-only channel, no mention, ignoring",
						"discord_channel", evt.channelID, "user", evt.userID)
					continue
				}
				agentID = mapped
				// Track source channel so Send() replies to the right place.
				a.replyOverride[agentID] = evt.channelID
				a.mu.Unlock()
			}

			log.Debug("routing incoming message", "discord_channel", evt.channelID, "agent_id", agentID, "user", evt.userID)

			// Check Discord user authorization (pairing/allowlist).
			if !a.checkAuth(ctx, agentID, evt) {
				log.Debug("discord auth: message blocked", "agent_id", agentID, "user", evt.userID)
				continue
			}

			// Start typing indicator so the user sees the bot is processing.
			a.startTyping(ctx, agentID, evt.channelID)

			inMsg := channels.IncomingMessage{
				ChannelType: "discord",
				ChannelID:   evt.channelID,
				SenderID:    evt.userID,
				Content:     evt.text,
				AgentID:     agentID,
			}

			// Process Discord attachments via the shared attachment service.
			if len(evt.attachments) > 0 && a.attachmentSvc != nil {
				var raw []attachments.RawAttachment
				for _, att := range evt.attachments {
					raw = append(raw, attachments.RawAttachment{
						URL:         att.URL,
						Filename:    att.Filename,
						ContentType: att.ContentType,
						Size:        int64(att.Size),
					})
				}
				processed, err := a.attachmentSvc.Process(ctx, agentID, raw)
				if err != nil {
					log.Error("attachment processing failed", "error", err)
				} else {
					inMsg.Attachments = processed
				}
			}

			// Extract text from Discord embeds and append to message content.
			if len(evt.embeds) > 0 {
				var embedTexts []string
				for _, e := range evt.embeds {
					desc := ""
					if e.Title != "" {
						desc += e.Title
					}
					if e.Description != "" {
						if desc != "" {
							desc += ": "
						}
						desc += e.Description
					}
					if e.URL != "" {
						desc += " (" + e.URL + ")"
					}
					if desc != "" {
						embedTexts = append(embedTexts, desc)
					}
				}
				if len(embedTexts) > 0 {
					inMsg.Content += "\n\n[Embeds: " + strings.Join(embedTexts, "; ") + "]"
				}
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
func (a *DiscordAdapter) Close() error {
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

// channelEntry holds a parsed channel ID and its listen mode.
type channelEntry struct {
	id   string // Discord channel ID
	mode string // "all" (respond to everything) or "mention" (respond only when @mentioned)
}

func (e channelEntry) String() string {
	return e.id + ":" + e.mode
}

// parseChannelEntries parses a comma-separated list of channel entries.
// Each entry is "channelID" or "channelID:mode" where mode is "all" (default) or "mention".
func parseChannelEntries(raw string) []channelEntry {
	var entries []channelEntry
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, mode, _ := strings.Cut(part, ":")
		id = strings.TrimSpace(id)
		mode = strings.TrimSpace(strings.ToLower(mode))
		if mode != "mention" {
			mode = "all"
		}
		if id != "" {
			entries = append(entries, channelEntry{id: id, mode: mode})
		}
	}
	return entries
}

// invalidDiscordChars matches characters not allowed in Discord channel names.
// Discord allows lowercase alphanumeric and hyphens, max 100 chars.
var invalidDiscordChars = regexp.MustCompile(`[^a-z0-9-]`)

// sanitizeChannelName converts an agent name to a valid Discord channel name.
func sanitizeChannelName(agentName string) string {
	name := strings.ToLower(agentName)
	name = strings.ReplaceAll(name, " ", "-")
	name = invalidDiscordChars.ReplaceAllString(name, "")

	// Remove consecutive hyphens.
	for strings.Contains(name, "--") {
		name = strings.ReplaceAll(name, "--", "-")
	}
	name = strings.Trim(name, "-")

	name = "kyvik-" + name

	if len(name) > 100 {
		name = name[:100]
		name = strings.TrimRight(name, "-")
	}

	return name
}
