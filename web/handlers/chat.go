package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
	"unicode/utf8"

	"github.com/kkjorsvik/kyvik/internal/channels"
	"github.com/kkjorsvik/kyvik/internal/channels/webui"
	"github.com/kkjorsvik/kyvik/internal/history"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// SetWebUI sets the WebUI adapter on the handlers, enabling chat functionality.
func (h *Handlers) SetWebUI(a *webui.Adapter) {
	h.webui = a
}

// AgentChat renders the chat page for an agent.
func (h *Handlers) AgentChat(w http.ResponseWriter, r *http.Request) {
	if chatV2DefaultEnabled() && r.URL.Query().Get("legacy") != "1" {
		target := "/agents/" + url.PathEscape(r.PathValue("id")) + "/chat2"
		if convID := r.URL.Query().Get("c"); convID != "" {
			target += "?c=" + url.QueryEscape(convID)
		}
		http.Redirect(w, r, target, http.StatusTemporaryRedirect)
		return
	}
	h.renderAgentChatV1(w, r)
}

// AgentChatV1 explicitly serves legacy chat, regardless of default route policy.
func (h *Handlers) AgentChatV1(w http.ResponseWriter, r *http.Request) {
	h.renderAgentChatV1(w, r)
}

func (h *Handlers) renderAgentChatV1(w http.ResponseWriter, r *http.Request) {
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
	var activeConv *history.WebConversation
	var messages []history.HistoryEntry
	var groups []history.ConversationGroup

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

		if activeConv != nil && hs != nil {
			messages, _ = hs.Recent(ctx, id, "webui", activeConv.ID, 200)
		}

		groups = groupConversations(conversations)
	}

	data := map[string]any{
		"Nav":           "agents",
		"Title":         "Chat — " + config.Name,
		"Agent":         config,
		"Status":        status,
		"Conversations": groups,
		"ActiveConv":    activeConv,
		"ActiveConvID":  convID,
		"Messages":      messages,
	}

	h.renderPageWithRequest(w, r, "agent-chat", data)
}

// AgentChatSend handles a message sent from the chat UI.
func (h *Handlers) AgentChatSend(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	_ = r.ParseMultipartForm(32 << 20)

	content := r.FormValue("message")
	convID := r.FormValue("conversation_id")

	var attachments []types.Attachment
	if r.MultipartForm != nil {
		for _, fh := range r.MultipartForm.File["files"] {
			if fh.Size > types.MaxAttachmentSize {
				continue
			}
			f, err := fh.Open()
			if err != nil {
				continue
			}
			data, err := io.ReadAll(f)
			f.Close()
			if err != nil {
				continue
			}
			attachments = append(attachments, types.Attachment{
				Filename:    fh.Filename,
				ContentType: fh.Header.Get("Content-Type"),
				Size:        fh.Size,
				Data:        data,
			})
		}
	}

	if err := types.ValidateAttachments(attachments); err != nil {
		http.Error(w, "invalid attachments", http.StatusBadRequest)
		return
	}

	if content == "" && len(attachments) == 0 {
		w.WriteHeader(http.StatusOK)
		return
	}

	cs := h.kyvik.Storage.Conversations
	isNew := false

	// Create conversation on first message if none specified.
	if convID == "" && cs != nil {
		title := autoTitle(content)
		conv, err := cs.CreateConversation(ctx, id, title)
		if err != nil {
			http.Error(w, "failed to create conversation", http.StatusInternalServerError)
			return
		}
		convID = conv.ID
		isNew = true
	}

	msg := types.Message{
		AgentID:        id,
		Channel:        "webui",
		ConversationID: convID,
		Role:           "user",
		Content:        content,
		Attachments:    attachments,
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

	if err := h.kyvik.SendMessage(ctx, id, msg); err != nil {
		http.Error(w, "agent not running", http.StatusBadRequest)
		return
	}

	// Increment message count (user message).
	if cs != nil && convID != "" {
		_ = cs.IncrementMessageCount(ctx, convID, 1)
	}

	// Set HX-Trigger to tell the browser the conversation ID and whether it's new.
	triggerData, _ := json.Marshal(map[string]any{
		"conversationCreated": map[string]any{
			"convID": convID,
			"isNew":  isNew,
		},
	})
	w.Header().Set("HX-Trigger", string(triggerData))

	h.renderFragment(w, r, "chat-msg-user", map[string]any{
		"Content":     content,
		"Attachments": attachments,
		"Timestamp":   h.localTime(msg.Timestamp).Format("15:04"),
	})
}

// AgentChatStream is an SSE endpoint that streams agent responses to the browser.
// It emits named events: "chunk" (partial content), "done" (response complete
// with token/cost metadata), "error" (streaming failure), and "message"
// (full non-streamed message, backward compatibility).
func (h *Handlers) AgentChatStream(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	convID := r.URL.Query().Get("c")

	if h.webui == nil {
		http.Error(w, "chat not available", http.StatusServiceUnavailable)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	ch := h.webui.Subscribe(id)
	defer h.webui.Unsubscribe(id, ch)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher.Flush()

	cs := h.kyvik.Storage.Conversations
	messageCounted := false // track whether we counted this response
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			// Keep SSE alive through proxies/load balancers while waiting on model output.
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case event, ok := <-ch:
			if !ok {
				return
			}
			// Filter: only send events for the active conversation.
			if convID != "" && event.ConversationID != "" && event.ConversationID != convID {
				continue
			}

			h.writeSSEEvent(w, flusher, event, cs, r.Context(), &messageCounted)
		}
	}
}

// writeSSEEvent writes a single StreamEvent as a named SSE event to the response.
func (h *Handlers) writeSSEEvent(
	w http.ResponseWriter,
	flusher http.Flusher,
	event channels.StreamEvent,
	cs history.ConversationStore,
	ctx context.Context,
	messageCounted *bool,
) {
	switch event.Type {
	case "chunk":
		data, _ := json.Marshal(map[string]string{
			"content":         event.Content,
			"conversation_id": event.ConversationID,
		})
		fmt.Fprintf(w, "event: chunk\ndata: %s\n\n", data)
		flusher.Flush()

	case "done":
		// Increment message count on completion (once per response).
		if cs != nil && event.ConversationID != "" && !*messageCounted {
			_ = cs.IncrementMessageCount(ctx, event.ConversationID, 1)
			*messageCounted = true
		}

		ts := event.Timestamp
		if ts.IsZero() {
			ts = time.Now()
		}
		data, _ := json.Marshal(map[string]any{
			"conversation_id": event.ConversationID,
			"timestamp":       h.localTime(ts).Format("15:04"),
			"tokens_in":       event.TokensIn,
			"tokens_out":      event.TokensOut,
			"cost":            event.Cost,
		})
		fmt.Fprintf(w, "event: done\ndata: %s\n\n", data)
		flusher.Flush()
		*messageCounted = false // reset for next response

	case "error":
		data, _ := json.Marshal(map[string]string{
			"error":           event.Error,
			"conversation_id": event.ConversationID,
		})
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", data)
		flusher.Flush()
		*messageCounted = false

	case "message":
		// Full message (backward compatibility / non-streamed path).
		// Increment message count.
		if cs != nil && event.ConversationID != "" && !*messageCounted {
			_ = cs.IncrementMessageCount(ctx, event.ConversationID, 1)
			*messageCounted = true
		}

		ts := event.Timestamp
		if ts.IsZero() {
			ts = time.Now()
		}
		data, _ := json.Marshal(map[string]string{
			"content":         event.Content,
			"timestamp":       h.localTime(ts).Format("15:04"),
			"conversation_id": event.ConversationID,
		})
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
		flusher.Flush()
		*messageCounted = false
	}
}

// AgentChatConversationMessages loads messages for a conversation (HTMX partial).
func (h *Handlers) AgentChatConversationMessages(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	convID := r.URL.Query().Get("c")
	ctx := r.Context()

	if convID == "" {
		h.renderFragment(w, r, "chat-empty-state", nil)
		return
	}

	hs := h.kyvik.Storage.History
	cs := h.kyvik.Storage.Conversations

	var messages []history.HistoryEntry
	var conv *history.WebConversation

	if hs != nil {
		messages, _ = hs.Recent(ctx, id, "webui", convID, 200)
	}
	if cs != nil {
		conv, _ = cs.GetConversation(ctx, convID)
	}

	config, _ := h.kyvik.GetAgent(ctx, id)
	agentName := ""
	if config != nil {
		agentName = config.Name
	}

	h.renderFragment(w, r, "chat-messages-content", map[string]any{
		"Messages":   messages,
		"ActiveConv": conv,
		"AgentName":  agentName,
	})
}

// AgentChatConversationNew clears the chat area for a new conversation (lazy creation).
func (h *Handlers) AgentChatConversationNew(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()
	config, _ := h.kyvik.GetAgent(ctx, id)
	agentName := ""
	if config != nil {
		agentName = config.Name
	}

	h.renderFragment(w, r, "chat-messages-content", map[string]any{
		"Messages":   nil,
		"ActiveConv": nil,
		"AgentName":  agentName,
	})
}

// AgentChatConversationRename renames a conversation inline.
func (h *Handlers) AgentChatConversationRename(w http.ResponseWriter, r *http.Request) {
	convID := r.PathValue("convID")
	_ = r.ParseForm()
	title := r.FormValue("title")
	ctx := r.Context()

	if title == "" {
		http.Error(w, "title required", http.StatusBadRequest)
		return
	}

	cs := h.kyvik.Storage.Conversations
	if cs == nil {
		http.Error(w, "conversations not available", http.StatusServiceUnavailable)
		return
	}

	if err := cs.RenameConversation(ctx, convID, title); err != nil {
		http.Error(w, "rename failed", http.StatusInternalServerError)
		return
	}

	conv, _ := cs.GetConversation(ctx, convID)
	if conv == nil {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Return updated sidebar entry.
	h.renderFragment(w, r, "chat-sidebar-entry", map[string]any{
		"Conv":     *conv,
		"AgentID":  r.PathValue("id"),
		"IsActive": true,
	})
}

// AgentChatConversationDelete deletes a conversation and its messages.
func (h *Handlers) AgentChatConversationDelete(w http.ResponseWriter, r *http.Request) {
	convID := r.PathValue("convID")
	ctx := r.Context()

	cs := h.kyvik.Storage.Conversations
	if cs == nil {
		http.Error(w, "conversations not available", http.StatusServiceUnavailable)
		return
	}

	if err := cs.DeleteConversation(ctx, convID); err != nil {
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}

	// Return empty content — HTMX will remove the element via hx-swap="delete".
	w.WriteHeader(http.StatusOK)
}

// AgentChatSidebar returns the sidebar content fragment for HTMX polling.
func (h *Handlers) AgentChatSidebar(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	activeConvID := r.URL.Query().Get("active")
	ctx := r.Context()

	cs := h.kyvik.Storage.Conversations
	if cs == nil {
		w.WriteHeader(http.StatusOK)
		return
	}

	conversations, _ := cs.ListConversations(ctx, id)
	groups := groupConversations(conversations)

	h.renderFragment(w, r, "chat-sidebar-content", map[string]any{
		"Agent":         map[string]string{"ID": id},
		"Conversations": groups,
		"ActiveConvID":  activeConvID,
	})
}

// autoTitle generates a conversation title from the first message content.
func autoTitle(content string) string {
	if content == "" {
		return "New conversation"
	}
	// Truncate at ~50 chars on a word boundary.
	const maxLen = 50
	if utf8.RuneCountInString(content) <= maxLen {
		return content
	}
	runes := []rune(content)
	truncated := runes[:maxLen]
	// Try to break at last space.
	for i := len(truncated) - 1; i > maxLen/2; i-- {
		if truncated[i] == ' ' {
			return string(truncated[:i]) + "..."
		}
	}
	return string(truncated) + "..."
}

// groupConversations groups conversations by time period for sidebar display.
func groupConversations(convs []history.WebConversation) []history.ConversationGroup {
	if len(convs) == 0 {
		return nil
	}

	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	yesterday := today.AddDate(0, 0, -1)
	lastWeek := today.AddDate(0, 0, -7)

	groups := map[string][]history.WebConversation{
		"Today":       {},
		"Yesterday":   {},
		"Last 7 days": {},
		"Older":       {},
	}

	for _, c := range convs {
		switch {
		case c.UpdatedAt.After(today) || c.UpdatedAt.Equal(today):
			groups["Today"] = append(groups["Today"], c)
		case c.UpdatedAt.After(yesterday) || c.UpdatedAt.Equal(yesterday):
			groups["Yesterday"] = append(groups["Yesterday"], c)
		case c.UpdatedAt.After(lastWeek):
			groups["Last 7 days"] = append(groups["Last 7 days"], c)
		default:
			groups["Older"] = append(groups["Older"], c)
		}
	}

	// Build ordered result, omitting empty groups.
	var result []history.ConversationGroup
	for _, label := range []string{"Today", "Yesterday", "Last 7 days", "Older"} {
		if len(groups[label]) > 0 {
			result = append(result, history.ConversationGroup{
				Label:         label,
				Conversations: groups[label],
			})
		}
	}
	return result
}
