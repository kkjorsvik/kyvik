package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/kkjorsvik/kyvik/internal/channels"
	"github.com/kkjorsvik/kyvik/internal/history"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

var chatV2Upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true
		}
		u, err := url.Parse(origin)
		if err != nil {
			return false
		}
		return strings.EqualFold(u.Host, r.Host)
	},
}

type chatV2ClientEvent struct {
	Type           string `json:"type"`
	RequestID      string `json:"request_id,omitempty"`
	Content        string `json:"content,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
	SinceID        int64  `json:"since_id,omitempty"`
}

type chatV2HistoryMessage struct {
	ID         int64  `json:"id"`
	Role       string `json:"role"`
	Content    string `json:"content"`
	Timestamp  string `json:"timestamp"`
	IsInternal bool   `json:"is_internal,omitempty"`
	Source     string `json:"source,omitempty"`
}

type chatV2ServerEvent struct {
	Type                string                 `json:"type"`
	RequestID           string                 `json:"request_id,omitempty"`
	Content             string                 `json:"content,omitempty"`
	Error               string                 `json:"error,omitempty"`
	ConversationID      string                 `json:"conversation_id,omitempty"`
	Timestamp           string                 `json:"timestamp,omitempty"`
	Messages            []chatV2HistoryMessage `json:"messages,omitempty"`              // backward compatibility alias
	MessagesUserVisible []chatV2HistoryMessage `json:"messages_user_visible,omitempty"` // default UI payload
	MessagesDebug       []chatV2HistoryMessage `json:"messages_debug,omitempty"`        // full trace payload
}

type requestState struct {
	activeReqID    string
	activeSawChunk bool
	pending        []string
	recentDoneAt   time.Time
	recentDoneReq  string
}

type chatV2RequestTracker struct {
	mu    sync.Mutex
	state map[string]*requestState // conversation_id -> state
}

func newChatV2RequestTracker() *chatV2RequestTracker {
	return &chatV2RequestTracker{
		state: make(map[string]*requestState),
	}
}

func (t *chatV2RequestTracker) enqueue(convID, reqID string) {
	if convID == "" || reqID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.get(convID)
	st.pending = append(st.pending, reqID)
}

type mappedEvent struct {
	event    chatV2ServerEvent
	emit     bool
	terminal bool
}

func (t *chatV2RequestTracker) mapStream(h *Handlers, ev channels.StreamEvent) mappedEvent {
	ts := ev.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}

	convID := ev.ConversationID
	reqID := t.attach(convID)

	switch ev.Type {
	case "chunk":
		t.markChunk(convID)
		return mappedEvent{
			event: chatV2ServerEvent{
				Type:           "assistant_chunk",
				RequestID:      reqID,
				Content:        ev.Content,
				ConversationID: convID,
			},
			emit: true,
		}
	case "done":
		reqID = t.terminal(convID, true)
		return mappedEvent{
			event: chatV2ServerEvent{
				Type:           "assistant_done",
				RequestID:      reqID,
				ConversationID: convID,
				Timestamp:      h.localTime(ts).Format("15:04"),
			},
			emit:     true,
			terminal: true,
		}
	case "error":
		reqID = t.terminal(convID, false)
		return mappedEvent{
			event: chatV2ServerEvent{
				Type:           "assistant_error",
				RequestID:      reqID,
				Error:          ev.Error,
				ConversationID: convID,
			},
			emit:     true,
			terminal: true,
		}
	case "message":
		if t.shouldDropMessage(convID) {
			return mappedEvent{emit: false}
		}
		reqID = t.terminal(convID, false)
		return mappedEvent{
			event: chatV2ServerEvent{
				Type:           "assistant_message",
				RequestID:      reqID,
				Content:        ev.Content,
				ConversationID: convID,
				Timestamp:      h.localTime(ts).Format("15:04"),
			},
			emit:     true,
			terminal: true,
		}
	default:
		return mappedEvent{
			event: chatV2ServerEvent{
				Type:           "event_error",
				Error:          "unsupported stream event",
				ConversationID: convID,
			},
			emit: true,
		}
	}
}

func (t *chatV2RequestTracker) get(convID string) *requestState {
	st := t.state[convID]
	if st == nil {
		st = &requestState{}
		t.state[convID] = st
	}
	return st
}

func (t *chatV2RequestTracker) attach(convID string) string {
	if convID == "" {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.get(convID)
	if st.activeReqID == "" && len(st.pending) > 0 {
		st.activeReqID = st.pending[0]
		st.activeSawChunk = false
	}
	return st.activeReqID
}

func (t *chatV2RequestTracker) markChunk(convID string) {
	if convID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.get(convID)
	if st.activeReqID == "" && len(st.pending) > 0 {
		st.activeReqID = st.pending[0]
	}
	if st.activeReqID != "" {
		st.activeSawChunk = true
	}
}

func (t *chatV2RequestTracker) terminal(convID string, markDone bool) string {
	if convID == "" {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.get(convID)
	if st.activeReqID == "" && len(st.pending) > 0 {
		st.activeReqID = st.pending[0]
	}
	reqID := st.activeReqID
	if reqID != "" && len(st.pending) > 0 && st.pending[0] == reqID {
		st.pending = st.pending[1:]
	}
	st.activeReqID = ""
	st.activeSawChunk = false
	if markDone {
		st.recentDoneAt = time.Now()
		st.recentDoneReq = reqID
	}
	return reqID
}

func (t *chatV2RequestTracker) shouldDropMessage(convID string) bool {
	if convID == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.get(convID)
	if st.activeReqID != "" && st.activeSawChunk {
		return true
	}
	if !st.recentDoneAt.IsZero() && time.Since(st.recentDoneAt) < 3*time.Second {
		return true
	}
	return false
}

// AgentChatV2 renders chat v2 (WebSocket skeleton).
func (h *Handlers) AgentChatV2(w http.ResponseWriter, r *http.Request) {
	if !chatV2Enabled() {
		http.NotFound(w, r)
		return
	}

	id := r.PathValue("id")
	ctx := r.Context()

	config, err := h.kyvik.GetAgent(ctx, id)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	status, _ := h.kyvik.GetAgentStatus(ctx, id)
	convID := r.URL.Query().Get("c")

	var conversations []history.WebConversation
	var groups []history.ConversationGroup
	var activeConv *history.WebConversation
	var messages []history.HistoryEntry
	cs := h.kyvik.Storage.Conversations
	hs := h.kyvik.Storage.History
	if cs != nil {
		conversations, _ = cs.ListConversations(ctx, id)
		if convID != "" {
			activeConv, _ = cs.GetConversation(ctx, convID)
		}
		if activeConv == nil && len(conversations) > 0 {
			activeConv = &conversations[0]
			convID = activeConv.ID
		}
		groups = groupConversations(conversations)
	}
	if hs != nil && convID != "" {
		messages, _ = hs.Recent(ctx, id, "webui", convID, 200)
	}

	data := map[string]any{
		"Nav":           "agents",
		"Title":         "Chat v2 — " + config.Name,
		"Agent":         config,
		"Status":        status,
		"Conversations": groups,
		"ActiveConv":    activeConv,
		"ActiveConvID":  convID,
		"Messages":      messages,
	}

	h.renderPageWithRequest(w, r, "agent-chat-v2", data)
}

// AgentChatV2WS handles the WebSocket session for chat v2.
func (h *Handlers) AgentChatV2WS(w http.ResponseWriter, r *http.Request) {
	if !chatV2Enabled() {
		http.NotFound(w, r)
		return
	}

	if h.webui == nil {
		http.Error(w, "chat not available", http.StatusServiceUnavailable)
		return
	}

	agentID := r.PathValue("id")

	conn, err := chatV2Upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	out := make(chan []byte, 64)
	streamCh := h.webui.Subscribe(agentID)
	defer h.webui.Unsubscribe(agentID, streamCh)
	tracker := newChatV2RequestTracker()
	cs := h.kyvik.Storage.Conversations

	go func() {
		pingTicker := time.NewTicker(25 * time.Second)
		defer pingTicker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case raw, ok := <-out:
				if !ok {
					return
				}
				_ = conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
				if writeErr := conn.WriteMessage(websocket.TextMessage, raw); writeErr != nil {
					cancel()
					return
				}
			case <-pingTicker.C:
				_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if pingErr := conn.WriteControl(websocket.PingMessage, []byte("ping"), time.Now().Add(10*time.Second)); pingErr != nil {
					cancel()
					return
				}
			}
		}
	}()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-streamCh:
				if !ok {
					return
				}
				mapped := tracker.mapStream(h, ev)
				if !mapped.emit {
					continue
				}
				if mapped.terminal && cs != nil && mapped.event.ConversationID != "" {
					_ = cs.IncrementMessageCount(ctx, mapped.event.ConversationID, 1)
				}
				if mapped.event.Type == "assistant_chunk" || mapped.event.Type == "assistant_done" || mapped.event.Type == "assistant_error" {
					slog.Debug("chat_v2 stream event",
						"type", mapped.event.Type,
						"request_id", mapped.event.RequestID,
						"conversation_id", mapped.event.ConversationID,
					)
				}
				_ = sendChatV2Event(ctx, out, mapped.event)
			}
		}
	}()

	_ = sendChatV2Event(ctx, out, chatV2ServerEvent{Type: "connected"})
	_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	})

	for {
		_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		var ev chatV2ClientEvent
		if err := conn.ReadJSON(&ev); err != nil {
			return
		}

		switch ev.Type {
		case "ping":
			_ = sendChatV2Event(ctx, out, chatV2ServerEvent{Type: "pong"})
		case "user_message":
			h.handleChatV2UserMessage(ctx, out, tracker, agentID, ev)
		case "history_sync":
			h.handleChatV2HistorySync(ctx, out, agentID, ev)
		default:
			_ = sendChatV2Event(ctx, out, chatV2ServerEvent{
				Type:  "event_error",
				Error: "unsupported event type",
			})
		}
	}
}

func (h *Handlers) handleChatV2UserMessage(
	ctx context.Context,
	out chan<- []byte,
	tracker *chatV2RequestTracker,
	agentID string,
	ev chatV2ClientEvent,
) {
	content := strings.TrimSpace(ev.Content)
	if content == "" {
		_ = sendChatV2Event(ctx, out, chatV2ServerEvent{
			Type:      "event_error",
			RequestID: ev.RequestID,
			Error:     "message is empty",
		})
		return
	}

	convID := ev.ConversationID
	reqID := strings.TrimSpace(ev.RequestID)
	if reqID == "" {
		reqID = "req_" + time.Now().UTC().Format("20060102150405.000000000")
	}
	cs := h.kyvik.Storage.Conversations

	if convID == "" && cs != nil {
		conv, err := cs.CreateConversation(ctx, agentID, autoTitle(content))
		if err != nil {
			_ = sendChatV2Event(ctx, out, chatV2ServerEvent{
				Type:      "event_error",
				RequestID: ev.RequestID,
				Error:     "failed to create conversation",
			})
			return
		}
		convID = conv.ID
		_ = sendChatV2Event(ctx, out, chatV2ServerEvent{
			Type:           "conversation_created",
			RequestID:      reqID,
			ConversationID: convID,
		})
	}

	msg := types.Message{
		AgentID:        agentID,
		Channel:        "webui",
		ConversationID: convID,
		Role:           "user",
		Content:        content,
		Timestamp:      time.Now(),
	}

	if u, ok := currentDashboardUser(ctx); ok {
		msg.Metadata = map[string]string{
			"user_role": u.Role,
			"user_id":   u.ID,
			"username":  u.Username,
		}
		if u.IsAdmin {
			msg.Metadata["user_role"] = "admin"
		}
	}
	if h.defaultTZ != "" && h.defaultTZ != "UTC" {
		if msg.Metadata == nil {
			msg.Metadata = map[string]string{}
		}
		msg.Metadata["timezone"] = h.defaultTZ
	}

	if err := h.kyvik.SendMessage(ctx, agentID, msg); err != nil {
		_ = sendChatV2Event(ctx, out, chatV2ServerEvent{
			Type:           "assistant_error",
			RequestID:      reqID,
			ConversationID: convID,
			Error:          "agent not running",
		})
		return
	}
	tracker.enqueue(convID, reqID)
	slog.Debug("chat_v2 user message accepted", "request_id", reqID, "conversation_id", convID, "agent_id", agentID)

	if cs != nil && convID != "" {
		_ = cs.IncrementMessageCount(ctx, convID, 1)
	}

	_ = sendChatV2Event(ctx, out, chatV2ServerEvent{
		Type:           "ack",
		RequestID:      reqID,
		ConversationID: convID,
		Timestamp:      h.localTime(msg.Timestamp).Format("15:04"),
	})
}

func (h *Handlers) handleChatV2HistorySync(
	ctx context.Context,
	out chan<- []byte,
	agentID string,
	ev chatV2ClientEvent,
) {
	convID := strings.TrimSpace(ev.ConversationID)
	if convID == "" {
		_ = sendChatV2Event(ctx, out, chatV2ServerEvent{
			Type:                "history_sync",
			Messages:            []chatV2HistoryMessage{},
			MessagesUserVisible: []chatV2HistoryMessage{},
			MessagesDebug:       []chatV2HistoryMessage{},
		})
		return
	}

	hs := h.kyvik.Storage.History
	if hs == nil {
		_ = sendChatV2Event(ctx, out, chatV2ServerEvent{
			Type:                "history_sync",
			ConversationID:      convID,
			Messages:            []chatV2HistoryMessage{},
			MessagesUserVisible: []chatV2HistoryMessage{},
			MessagesDebug:       []chatV2HistoryMessage{},
		})
		return
	}

	entries, err := hs.Recent(ctx, agentID, "webui", convID, 200)
	if err != nil {
		_ = sendChatV2Event(ctx, out, chatV2ServerEvent{
			Type:           "history_sync_error",
			ConversationID: convID,
			Error:          "failed to load history",
		})
		return
	}

	msgsUser := buildChatV2HistoryUserVisible(h, entries, ev.SinceID)
	msgsDebug := buildChatV2HistoryDebug(h, entries, ev.SinceID)
	slog.Debug("chat_v2 history sync", "conversation_id", convID, "user_visible_count", len(msgsUser), "debug_count", len(msgsDebug))
	_ = sendChatV2Event(ctx, out, chatV2ServerEvent{
		Type:                "history_sync",
		ConversationID:      convID,
		Messages:            msgsUser, // compatibility alias
		MessagesUserVisible: msgsUser,
		MessagesDebug:       msgsDebug,
	})
}

func sendChatV2Event(ctx context.Context, out chan<- []byte, ev chatV2ServerEvent) error {
	raw, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case out <- raw:
		return nil
	}
}

func chatV2Enabled() bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv("KYVIK_CHAT_V2")))
	switch v {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func chatV2DefaultEnabled() bool {
	if !chatV2Enabled() {
		return false
	}
	v := strings.TrimSpace(strings.ToLower(os.Getenv("KYVIK_CHAT_V2_DEFAULT")))
	switch v {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func buildChatV2HistoryUserVisible(h *Handlers, entries []history.HistoryEntry, sinceID int64) []chatV2HistoryMessage {
	if len(entries) == 0 {
		return nil
	}
	out := make([]chatV2HistoryMessage, 0, len(entries))
	for _, e := range entries {
		if e.ID <= sinceID {
			continue
		}
		switch e.Role {
		case "user":
		case "assistant":
			if strings.TrimSpace(e.Content) == "" || isInternalHistoryEntry(e) {
				continue
			}
		default:
			continue
		}
		out = append(out, chatV2HistoryMessage{
			ID:         e.ID,
			Role:       e.Role,
			Content:    e.Content,
			Timestamp:  h.localTime(e.CreatedAt).Format("15:04"),
			IsInternal: false,
			Source:     "history",
		})
	}
	return out
}

func buildChatV2HistoryDebug(h *Handlers, entries []history.HistoryEntry, sinceID int64) []chatV2HistoryMessage {
	if len(entries) == 0 {
		return nil
	}
	out := make([]chatV2HistoryMessage, 0, len(entries))
	for _, e := range entries {
		if e.ID <= sinceID {
			continue
		}
		out = append(out, chatV2HistoryMessage{
			ID:         e.ID,
			Role:       e.Role,
			Content:    e.Content,
			Timestamp:  h.localTime(e.CreatedAt).Format("15:04"),
			IsInternal: isInternalHistoryEntry(e),
			Source:     "history",
		})
	}
	return out
}

func isInternalHistoryEntry(e history.HistoryEntry) bool {
	if e.Role == "tool" {
		return true
	}
	if strings.TrimSpace(e.Content) == "" {
		return true
	}
	if strings.TrimSpace(e.ToolCallID) != "" {
		return true
	}
	if strings.TrimSpace(e.ToolCallsJSON) != "" {
		return true
	}
	return false
}
