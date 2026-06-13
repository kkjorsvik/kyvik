// Package busadapter implements the channels.Adapter interface for inter-agent
// messaging via the teams.Bus. Messages sent between agents through the bus
// are converted to IncomingMessage and delivered through the standard channel
// adapter pipeline, so agents process them identically to external messages.
package busadapter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/kkjorsvik/kyvik/internal/audit"
	"github.com/kkjorsvik/kyvik/internal/channels"
	"github.com/kkjorsvik/kyvik/internal/queue"
	"github.com/kkjorsvik/kyvik/internal/teams"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// AgentConfigLookup retrieves an agent's configuration by ID.
type AgentConfigLookup func(ctx context.Context, id string) (*types.AgentConfig, error)

// TeamLookup retrieves the team for an agent, or nil if not in a team.
type TeamLookup func(ctx context.Context, agentID string) (*types.Team, error)

// Compile-time interface check.
var _ channels.Adapter = (*Adapter)(nil)

// subInfo tracks a single agent's bus subscription.
type subInfo struct {
	ch     <-chan types.InternalMessage
	cancel context.CancelFunc
}

// Adapter bridges the teams.Bus into the channel adapter pipeline.
type Adapter struct {
	bus          *teams.Bus
	mu           sync.RWMutex
	subs         map[string]subInfo // agentID -> subscription
	incoming     chan channels.IncomingMessage
	closed       bool
	wg           sync.WaitGroup    // tracks forwardMessages goroutines
	configLookup AgentConfigLookup // nil = skip permission check
	teamLookup   TeamLookup        // nil = skip communication mode checks
	audit        audit.Logger      // nil = no audit on deny
}

// New creates a new internal channel adapter backed by the given bus.
func New(bus *teams.Bus) *Adapter {
	return &Adapter{
		bus:      bus,
		subs:     make(map[string]subInfo),
		incoming: make(chan channels.IncomingMessage, 128),
	}
}

// SetConfigLookup sets the function used to resolve agent configs for
// permission checks. When nil (the default), permission checks are skipped.
func (a *Adapter) SetConfigLookup(fn AgentConfigLookup) { a.configLookup = fn }

// SetAuditLogger sets the audit logger for recording denied messages.
func (a *Adapter) SetAuditLogger(l audit.Logger) { a.audit = l }

// SetTeamLookup sets team lookup used for communication mode enforcement.
func (a *Adapter) SetTeamLookup(fn TeamLookup) { a.teamLookup = fn }

// Name returns the channel identifier.
func (a *Adapter) Name() string { return "internal" }

// ProvisionAgent subscribes an agent to bus messages and starts a forwarding
// goroutine. Idempotent: calling again for an already-provisioned agent is a no-op.
func (a *Adapter) ProvisionAgent(ctx context.Context, config types.AgentConfig) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.closed {
		return types.ErrAdapterClosed
	}

	// Idempotent — skip if already provisioned.
	if _, ok := a.subs[config.ID]; ok {
		return nil
	}

	subCtx, cancel := context.WithCancel(context.Background())
	ch := a.bus.Subscribe(ctx, config.ID)

	a.subs[config.ID] = subInfo{ch: ch, cancel: cancel}

	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		a.forwardMessages(subCtx, config.ID, ch)
	}()

	return nil
}

// DeprovisionAgent cancels the forwarding goroutine and unsubscribes from the bus.
func (a *Adapter) DeprovisionAgent(_ context.Context, agentID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	sub, ok := a.subs[agentID]
	if !ok {
		return types.ErrNotProvisioned
	}

	sub.cancel()
	a.bus.Unsubscribe(agentID, sub.ch)
	delete(a.subs, agentID)

	return nil
}

// Receive returns the shared incoming channel.
func (a *Adapter) Receive(_ context.Context) (<-chan channels.IncomingMessage, error) {
	return a.incoming, nil
}

// Send routes a response back through the bus when it originated from an
// internal message. Only messages with Channel=="internal" and a non-empty
// Sender are routed; all others return ErrNotProvisioned so the outbox
// consumer silently skips them.
func (a *Adapter) Send(ctx context.Context, agentID string, msg types.Message) error {
	if msg.Channel != "internal" || msg.Sender == "" {
		return types.ErrNotProvisioned
	}

	return a.bus.Send(ctx, types.InternalMessage{
		From:    agentID,
		To:      msg.Sender,
		Content: msg.Content,
		Type:    types.MessageTypeResult,
	})
}

// Close cancels all forwarding goroutines, unsubscribes from the bus,
// and closes the incoming channel. It waits for all goroutines to exit
// before closing the channel to prevent send-on-closed-channel panics.
func (a *Adapter) Close() error {
	a.mu.Lock()

	if a.closed {
		a.mu.Unlock()
		return nil
	}

	a.closed = true
	for agentID, sub := range a.subs {
		sub.cancel()
		a.bus.Unsubscribe(agentID, sub.ch)
		delete(a.subs, agentID)
	}
	a.mu.Unlock()

	// Wait for all forwardMessages goroutines to exit before closing.
	a.wg.Wait()
	close(a.incoming)

	return nil
}

// ReplayUndelivered queries internal_messages for bus messages addressed to
// agentID that have no corresponding entry in the message_queue, and enqueues
// them. This bridges messages sent while the agent was offline.
func (a *Adapter) ReplayUndelivered(ctx context.Context, agentID string, q queue.Queue) error {
	pending, err := a.bus.PendingMessagesFor(ctx, agentID)
	if err != nil {
		return fmt.Errorf("query pending bus messages: %w", err)
	}
	if len(pending) == 0 {
		return nil
	}

	log := slog.With("adapter", "internal", "agent_id", agentID)
	var enqueued int
	for _, msg := range pending {
		if a.teamLookup != nil {
			team, err := a.teamLookup(ctx, msg.From)
			if err != nil && !errors.Is(err, types.ErrTeamNotFound) {
				log.Warn("team lookup failed for sender", "from", msg.From, "err", err)
				continue
			}
			if team != nil && !team.Active {
				log.Warn("skipping replay: team communication paused", "from", msg.From, "team_id", team.ID)
				continue
			}
		}
		conversationID := interAgentConversationID(agentID, msg.From)
		messageType := string(msg.Type)
		if messageType == "" {
			messageType = "message"
		}
		_, err := q.Enqueue(ctx, queue.QueueMessage{
			AgentID:        agentID,
			Channel:        "internal",
			ConversationID: conversationID,
			Sender:         msg.From,
			Content:        msg.Content,
			MessageType:    messageType,
		})
		if err != nil {
			log.Warn("failed to enqueue replayed bus message", "from", msg.From, "error", err)
			continue
		}
		enqueued++
	}
	if enqueued > 0 {
		log.Info("replayed undelivered bus messages", "count", enqueued)
	}
	return nil
}

// interAgentConversationID returns a deterministic conversation ID for
// messages between two agents.
func interAgentConversationID(agentA, agentB string) string {
	if agentA < agentB {
		return "interagent:" + agentA + ":" + agentB
	}
	return "interagent:" + agentB + ":" + agentA
}

// forwardMessages reads from a bus subscription and converts each message
// into an IncomingMessage on the shared incoming channel. The message type
// is carried through so the router can handle results differently.
func (a *Adapter) forwardMessages(ctx context.Context, agentID string, ch <-chan types.InternalMessage) {
	log := slog.With("adapter", "internal", "agent_id", agentID)

	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}

			// Permission check (skipped when configLookup is nil).
			if a.configLookup != nil {
				fromCfg, err := a.configLookup(ctx, msg.From)
				if err != nil {
					log.Warn("config lookup failed for sender", "from", msg.From, "err", err)
					continue
				}
				toCfg, err := a.configLookup(ctx, agentID)
				if err != nil {
					log.Warn("config lookup failed for recipient", "to", agentID, "err", err)
					continue
				}
				if err := teams.CheckMessagePermission(*fromCfg, *toCfg); err != nil {
					log.Warn("message denied by permission check",
						"from", msg.From, "to", agentID)
					if a.audit != nil {
						_ = a.audit.Log(ctx, types.AuditEntry{
							AgentID:   msg.From,
							EventType: types.EventInternalMessage,
							Action:    "message_denied",
							Resource:  agentID,
							Decision:  "denied",
							Details:   fmt.Sprintf("agent %s not permitted to message %s", msg.From, agentID),
							Timestamp: time.Now(),
						})
					}
					continue
				}

				if a.teamLookup != nil {
					team, err := a.teamLookup(ctx, msg.From)
					if err != nil {
						if !errors.Is(err, types.ErrTeamNotFound) {
							log.Warn("team lookup failed for sender", "from", msg.From, "err", err)
							continue
						}
					} else if team != nil && team.Communication == types.TeamCommLeaderMediated {
						isLeader := msg.From == team.LeaderID
						if !isLeader && agentID != team.LeaderID {
							log.Warn("message denied by leader-mediated team communication",
								"from", msg.From, "to", agentID, "team_id", team.ID)
							if a.audit != nil {
								_ = a.audit.Log(ctx, types.AuditEntry{
									AgentID:   msg.From,
									EventType: types.EventInternalMessage,
									Action:    "message_denied",
									Resource:  agentID,
									Decision:  "denied",
									Details:   fmt.Sprintf("leader-mediated team %s: member %s may only message leader %s", team.ID, msg.From, team.LeaderID),
									Timestamp: time.Now(),
								})
							}
							continue
						}
					}
					if team != nil && !team.Active {
						log.Warn("message denied: team communication paused",
							"from", msg.From, "to", agentID, "team_id", team.ID)
						if a.audit != nil {
							_ = a.audit.Log(ctx, types.AuditEntry{
								AgentID:   msg.From,
								EventType: types.EventInternalMessage,
								Action:    "message_denied",
								Resource:  agentID,
								Decision:  "denied",
								Details:   fmt.Sprintf("team %s communication paused", team.ID),
								Timestamp: time.Now(),
							})
						}
						continue
					}
				}
			}

			select {
			case a.incoming <- channels.IncomingMessage{
				ChannelType: "internal",
				ChannelID:   msg.From,
				SenderID:    msg.From,
				AgentID:     msg.To,
				Content:     msg.Content,
				MessageType: string(msg.Type),
			}:
			case <-ctx.Done():
				return
			}
		}
	}
}
