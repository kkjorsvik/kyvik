// Package core implements the Kyvik agent lifecycle and message routing.
package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/kkjorsvik/kyvik/internal/channels"
	"github.com/kkjorsvik/kyvik/internal/history"
	"github.com/kkjorsvik/kyvik/internal/queue"
	"github.com/kkjorsvik/kyvik/internal/timeutil"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// StartRouter starts the message router that wires channel adapters to agent
// inboxes. For each registered adapter it calls Receive() and spawns a
// goroutine that forwards incoming messages to the matching agent's inbox
// via SendMessage().
//
// The router is stopped when the context passed to Start() is cancelled, or
// when Shutdown() cancels the router context.
func (p *Kyvik) StartRouter(ctx context.Context) error {
	routerCtx, cancel := context.WithCancel(ctx)
	p.routerCtx = routerCtx
	p.routerCancel = cancel

	p.mu.RLock()
	adapters := make(map[string]channels.Adapter, len(p.channels))
	for name, a := range p.channels {
		adapters[name] = a
	}
	p.mu.RUnlock()

	for name, adapter := range adapters {
		incoming, err := adapter.Receive(routerCtx)
		if err != nil {
			cancel()
			return err
		}
		go p.routeIncoming(routerCtx, name, incoming)
	}

	slog.Info("message router started", "adapters", len(adapters))
	return nil
}

// interAgentConversationID returns a deterministic conversation ID for
// messages between two agents. The IDs are sorted so the same pair always
// produces the same conversation ID regardless of direction.
func interAgentConversationID(agentA, agentB string) string {
	if agentA < agentB {
		return "interagent:" + agentA + ":" + agentB
	}
	return "interagent:" + agentB + ":" + agentA
}

// routeIncoming reads from a single adapter's incoming channel and delivers
// each message to the corresponding agent's inbox.
func (p *Kyvik) routeIncoming(ctx context.Context, adapterName string, incoming <-chan channels.IncomingMessage) {
	log := slog.With("channel", adapterName)

	for {
		select {
		case <-ctx.Done():
			log.Info("router goroutine stopped")
			return
		case msg, ok := <-incoming:
			if !ok {
				log.Info("incoming channel closed, router goroutine exiting")
				return
			}
			if msg.AgentID == "" {
				log.Warn("incoming message has no agent ID, dropping")
				continue
			}

			log.Debug("routing message to agent",
				"agent_id", msg.AgentID,
				"sender", msg.SenderID,
				"content_len", len(msg.Content),
				"message_type", msg.MessageType,
			)

			// Result messages go directly to history, not the queue.
			// This avoids infinite loops: results are saved as context
			// but don't trigger LLM processing.
			if msg.MessageType == "result" {
				if p.Storage.History != nil {
					convID := interAgentConversationID(msg.AgentID, msg.SenderID)
					_ = p.Storage.History.Append(ctx, history.HistoryEntry{
						AgentID:   msg.AgentID,
						Channel:   "internal",
						ChannelID: convID,
						Role:      "user",
						Content:   msg.Content,
						Sender:    msg.SenderID,
					})
				}
				continue
			}

			// Cluster routing: if the agent is assigned to a remote node,
			// enqueue the message with TargetNodeID so the remote queue consumer
			// delivers it. Skip local queue/send.
			if p.cluster != nil && !p.cluster.IsLocalAgent(msg.AgentID) {
				nodeID, _ := p.cluster.GetAssignment(msg.AgentID)
				if nodeID != "" && p.Storage.Queue != nil {
					qMsg := queue.QueueMessage{
						AgentID:      msg.AgentID,
						Content:      msg.Content,
						Channel:      msg.ChannelType,
						TargetNodeID: nodeID,
					}
					p.Storage.Queue.Enqueue(ctx, qMsg)
					continue
				}
			}

			if p.Storage.Queue != nil {
				priority := 0
				for _, pu := range p.Storage.Queue.PriorityUsers() {
					if msg.SenderID == pu {
						priority = 1
						break
					}
				}
				var attachmentsJSON string
				if len(msg.Attachments) > 0 {
					if data, err := json.Marshal(msg.Attachments); err == nil {
						attachmentsJSON = string(data)
					}
				}

				// Generate deterministic ConversationID for inter-agent messages.
				conversationID := ""
				if msg.ChannelType == "internal" {
					conversationID = interAgentConversationID(msg.AgentID, msg.SenderID)
				}

				// Carry message type; default to "message" for external channels.
				messageType := msg.MessageType
				if messageType == "" {
					messageType = "message"
				}

				_, err := p.Storage.Queue.Enqueue(ctx, queue.QueueMessage{
					AgentID:        msg.AgentID,
					Channel:        msg.ChannelType,
					ConversationID: conversationID,
					Sender:         msg.SenderID,
					Content:        msg.Content,
					Attachments:    attachmentsJSON,
					Priority:       priority,
					MessageType:    messageType,
				})
				if err != nil {
					log.Error("failed to enqueue message",
						"agent_id", msg.AgentID,
						"error", err,
					)
				}
			} else {
				err := p.SendMessage(ctx, msg.AgentID, types.Message{
					AgentID:     msg.AgentID,
					Channel:     msg.ChannelType,
					Role:        "user",
					Content:     msg.Content,
					Attachments: msg.Attachments,
					Timestamp:   timeutil.NowUTC(),
				})
				if err != nil {
					log.Error("failed to deliver message to agent inbox",
						"agent_id", msg.AgentID,
						"error", err,
					)
				}
			}
		}
	}
}

// DeregisterChannel removes a channel adapter from the runtime and closes it.
func (p *Kyvik) DeregisterChannel(name string) error {
	p.mu.Lock()
	adapter, ok := p.channels[name]
	if ok {
		delete(p.channels, name)
	}
	p.mu.Unlock()
	if !ok {
		return nil
	}
	return adapter.Close()
}

// ChannelAdapter returns a registered adapter by name, or nil if not found.
func (p *Kyvik) ChannelAdapter(name string) channels.Adapter {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.channels[name]
}

// StartAdapterRouter starts routing incoming messages for a single adapter.
// This is used to add adapters at runtime after the main router has started.
func (p *Kyvik) StartAdapterRouter(adapter channels.Adapter) error {
	ctx := p.routerCtx
	if ctx == nil {
		return fmt.Errorf("router not started")
	}
	incoming, err := adapter.Receive(ctx)
	if err != nil {
		return fmt.Errorf("start adapter receive: %w", err)
	}
	go p.routeIncoming(ctx, adapter.Name(), incoming)
	slog.Info("adapter router started", "adapter", adapter.Name())
	return nil
}
