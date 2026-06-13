package handlers

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/kkjorsvik/kyvik/internal/teams"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

type pairedListItem struct {
	Conv       types.PairedConversation
	AgentAName string
	AgentBName string
	TopicShort string
	IsActive   bool
}

type pairedMessageView struct {
	AgentID      string
	AgentName    string
	Content      string
	Tokens       int
	Cost         float64
	TurnNumber   int
	CreatedAt    time.Time
	InjectedBy   string
	Injected     bool
	Side         string
	AgentInitial string
}

// PairedList — GET /paired
func (h *Handlers) PairedList(w http.ResponseWriter, r *http.Request) {
	orch := h.kyvik.PairedOrchestrator()
	if orch == nil {
		http.Error(w, "Paired orchestrator not configured", http.StatusServiceUnavailable)
		return
	}

	ctx := r.Context()
	convs, err := orch.ListAll(ctx)
	if err != nil {
		h.serverError(w, r, "listing paired conversations", err)
		return
	}

	items := make([]pairedListItem, 0, len(convs))
	for _, c := range convs {
		if err := h.ensureAgentsVisible(ctx, c.AgentA, c.AgentB); err != nil {
			continue
		}
		aName := c.AgentA
		if cfg, err := h.kyvik.GetAgent(ctx, c.AgentA); err == nil {
			aName = cfg.Name
		}
		bName := c.AgentB
		if cfg, err := h.kyvik.GetAgent(ctx, c.AgentB); err == nil {
			bName = cfg.Name
		}
		items = append(items, pairedListItem{
			Conv:       c,
			AgentAName: aName,
			AgentBName: bName,
			TopicShort: truncateString(c.Topic, 90),
			IsActive:   c.Status == types.PairedStatusActive,
		})
	}

	data := map[string]any{
		"Nav":           "paired",
		"Title":         "Paired Conversations",
		"Conversations": items,
	}
	if isHTMX(r) {
		h.renderFragment(w, r, "paired-list", data)
		return
	}
	h.renderPageWithRequest(w, r, "paired-list", data)
}

// PairedLaunchForm — GET /paired/new
func (h *Handlers) PairedLaunchForm(w http.ResponseWriter, r *http.Request) {
	agents, err := h.runningAgents(r.Context())
	if err != nil {
		h.serverError(w, r, "listing running agents", err)
		return
	}

	data := map[string]any{
		"Nav":           "paired",
		"Title":         "New Paired Conversation",
		"Agents":        agents,
		"PrefillAgentA": r.URL.Query().Get("agent_a"),
		"PrefillAgentB": r.URL.Query().Get("agent_b"),
		"PrefillTopic":  r.URL.Query().Get("topic"),
		"DefaultTurns":  10,
		"DefaultDelay":  2000,
	}
	if isHTMX(r) {
		h.renderFragment(w, r, "paired-launch", data)
		return
	}
	h.renderPageWithRequest(w, r, "paired-launch", data)
}

// PairedLaunch — POST /paired
func (h *Handlers) PairedLaunch(w http.ResponseWriter, r *http.Request) {
	orch := h.kyvik.PairedOrchestrator()
	if orch == nil {
		http.Error(w, "Paired orchestrator not configured", http.StatusServiceUnavailable)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form data", http.StatusBadRequest)
		return
	}

	agentA := strings.TrimSpace(r.FormValue("agent_a"))
	agentB := strings.TrimSpace(r.FormValue("agent_b"))
	topic := strings.TrimSpace(r.FormValue("topic"))
	if agentA == "" || agentB == "" || topic == "" {
		http.Error(w, "agent_a, agent_b, and topic are required", http.StatusBadRequest)
		return
	}
	if agentA == agentB {
		http.Error(w, "agent_a and agent_b must be different", http.StatusBadRequest)
		return
	}
	if err := h.ensureAgentsVisible(r.Context(), agentA, agentB); err != nil {
		http.Error(w, "agent access denied", http.StatusForbidden)
		return
	}

	maxTurns := 10
	if v := strings.TrimSpace(r.FormValue("max_turns")); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil || parsed <= 0 {
			http.Error(w, "max_turns must be a positive number", http.StatusBadRequest)
			return
		}
		maxTurns = parsed
	}

	delayMs := 2000
	if v := strings.TrimSpace(r.FormValue("turn_delay_ms")); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil || parsed < 0 {
			http.Error(w, "turn_delay_ms must be zero or greater", http.StatusBadRequest)
			return
		}
		delayMs = parsed
	}

	allowInject := r.FormValue("allow_user_injection") != ""
	autoStop := parseAutoStopPhrases(r.FormValue("auto_stop_phrases"))

	conv := types.PairedConversation{
		ID:                 uuid.New().String(),
		AgentA:             agentA,
		AgentB:             agentB,
		Topic:              topic,
		MaxTurns:           maxTurns,
		TurnDelayMs:        delayMs,
		AllowUserInjection: allowInject,
		AutoStopPhrases:    autoStop,
	}
	if err := orch.Start(r.Context(), conv); err != nil {
		http.Error(w, "failed to start paired conversation", http.StatusBadRequest)
		return
	}

	redirect := "/paired/" + conv.ID
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", redirect)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, redirect, http.StatusSeeOther)
}

// PairedView — GET /paired/{id}
func (h *Handlers) PairedView(w http.ResponseWriter, r *http.Request) {
	orch := h.kyvik.PairedOrchestrator()
	if orch == nil {
		http.Error(w, "Paired orchestrator not configured", http.StatusServiceUnavailable)
		return
	}

	id := r.PathValue("id")
	ctx := r.Context()
	conv, err := orch.GetConversation(ctx, id)
	if err != nil {
		http.Error(w, "paired conversation not found", http.StatusNotFound)
		return
	}
	if err := h.ensureAgentsVisible(ctx, conv.AgentA, conv.AgentB); err != nil {
		http.Error(w, "paired conversation not found", http.StatusNotFound)
		return
	}
	msgs, err := orch.Messages(ctx, id)
	if err != nil {
		http.Error(w, "failed to load messages", http.StatusInternalServerError)
		return
	}

	aName := conv.AgentA
	agentAExists := true
	if cfg, err := h.kyvik.GetAgent(ctx, conv.AgentA); err == nil {
		aName = cfg.Name
	} else {
		agentAExists = false
	}
	bName := conv.AgentB
	agentBExists := true
	if cfg, err := h.kyvik.GetAgent(ctx, conv.AgentB); err == nil {
		bName = cfg.Name
	} else {
		agentBExists = false
	}

	data := map[string]any{
		"Nav":          "paired",
		"Title":        "Paired Conversation",
		"Conversation": conv,
		"AgentAName":   aName,
		"AgentBName":   bName,
		"AgentAExists": agentAExists,
		"AgentBExists": agentBExists,
		"Messages":     toPairedMessageViews(msgs, conv.AgentA),
	}

	// Load running agents for the edit-agents form when conversation is not active.
	if conv.Status == types.PairedStatusStopped || conv.Status == types.PairedStatusCompleted {
		if agents, err := h.runningAgents(ctx); err == nil {
			data["Agents"] = agents
		}
	}

	if isHTMX(r) {
		h.renderFragment(w, r, "paired-view", data)
		return
	}
	h.renderPageWithRequest(w, r, "paired-view", data)
}

// PairedPause — POST /paired/{id}/pause
func (h *Handlers) PairedPause(w http.ResponseWriter, r *http.Request) {
	h.pairedControlAction(w, r, func(orch *typesafeOrchestrator, id string) error {
		return orch.Pause(r.Context(), id)
	})
}

// PairedResume — POST /paired/{id}/resume
func (h *Handlers) PairedResume(w http.ResponseWriter, r *http.Request) {
	h.pairedControlAction(w, r, func(orch *typesafeOrchestrator, id string) error {
		return orch.Resume(r.Context(), id)
	})
}

// PairedStop — POST /paired/{id}/stop
func (h *Handlers) PairedStop(w http.ResponseWriter, r *http.Request) {
	h.pairedControlAction(w, r, func(orch *typesafeOrchestrator, id string) error {
		return orch.Stop(r.Context(), id)
	})
}

// PairedInject — POST /paired/{id}/inject
func (h *Handlers) PairedInject(w http.ResponseWriter, r *http.Request) {
	orch := h.kyvik.PairedOrchestrator()
	if orch == nil {
		http.Error(w, "Paired orchestrator not configured", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	conv, err := orch.GetConversation(r.Context(), id)
	if err != nil {
		http.Error(w, "paired conversation not found", http.StatusNotFound)
		return
	}
	if err := h.ensureAgentsVisible(r.Context(), conv.AgentA, conv.AgentB); err != nil {
		http.Error(w, "paired conversation not found", http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	message := strings.TrimSpace(r.FormValue("message"))
	if message == "" {
		http.Error(w, "message is required", http.StatusBadRequest)
		return
	}
	if err := orch.Inject(r.Context(), id, message); err != nil {
		http.Error(w, "failed to inject message", http.StatusBadRequest)
		return
	}

	if isHTMX(r) {
		w.Header().Set("HX-Trigger", "paired-refresh")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, "/paired/"+id, http.StatusSeeOther)
}

// PairedDelete — POST /paired/{id}/delete
func (h *Handlers) PairedDelete(w http.ResponseWriter, r *http.Request) {
	orch := h.kyvik.PairedOrchestrator()
	if orch == nil {
		http.Error(w, "Paired orchestrator not configured", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	conv, err := orch.GetConversation(r.Context(), id)
	if err != nil {
		http.Error(w, "paired conversation not found", http.StatusNotFound)
		return
	}
	if err := h.ensureAgentsVisible(r.Context(), conv.AgentA, conv.AgentB); err != nil {
		http.Error(w, "paired conversation not found", http.StatusNotFound)
		return
	}
	if err := orch.Delete(r.Context(), id); err != nil {
		http.Error(w, "failed to delete conversation", http.StatusBadRequest)
		return
	}

	redirect := "/paired"
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", redirect)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, redirect, http.StatusSeeOther)
}

// PairedContinue — POST /paired/{id}/continue
func (h *Handlers) PairedContinue(w http.ResponseWriter, r *http.Request) {
	orch := h.kyvik.PairedOrchestrator()
	if orch == nil {
		http.Error(w, "Paired orchestrator not configured", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	conv, err := orch.GetConversation(r.Context(), id)
	if err != nil {
		http.Error(w, "paired conversation not found", http.StatusNotFound)
		return
	}
	if err := h.ensureAgentsVisible(r.Context(), conv.AgentA, conv.AgentB); err != nil {
		http.Error(w, "paired conversation not found", http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form data", http.StatusBadRequest)
		return
	}

	additionalTurns := 5
	if v := strings.TrimSpace(r.FormValue("additional_turns")); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil || parsed < 1 || parsed > 100 {
			http.Error(w, "additional_turns must be between 1 and 100", http.StatusBadRequest)
			return
		}
		additionalTurns = parsed
	}

	if err := orch.Continue(r.Context(), id, additionalTurns); err != nil {
		http.Error(w, "failed to continue conversation", http.StatusBadRequest)
		return
	}

	redirect := "/paired/" + id
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", redirect)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, redirect, http.StatusSeeOther)
}

// PairedEditAgents — POST /paired/{id}/agents
func (h *Handlers) PairedEditAgents(w http.ResponseWriter, r *http.Request) {
	orch := h.kyvik.PairedOrchestrator()
	if orch == nil {
		http.Error(w, "Paired orchestrator not configured", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	conv, err := orch.GetConversation(r.Context(), id)
	if err != nil {
		http.Error(w, "paired conversation not found", http.StatusNotFound)
		return
	}
	if err := h.ensureAgentsVisible(r.Context(), conv.AgentA, conv.AgentB); err != nil {
		http.Error(w, "paired conversation not found", http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form data", http.StatusBadRequest)
		return
	}

	agentA := strings.TrimSpace(r.FormValue("agent_a"))
	agentB := strings.TrimSpace(r.FormValue("agent_b"))
	if agentA == "" || agentB == "" {
		http.Error(w, "agent_a and agent_b are required", http.StatusBadRequest)
		return
	}
	if agentA == agentB {
		http.Error(w, "agent_a and agent_b must be different", http.StatusBadRequest)
		return
	}
	if err := h.ensureAgentsVisible(r.Context(), agentA, agentB); err != nil {
		http.Error(w, "agent access denied", http.StatusForbidden)
		return
	}

	if err := orch.UpdateAgents(r.Context(), id, agentA, agentB); err != nil {
		http.Error(w, "failed to update agents", http.StatusBadRequest)
		return
	}

	redirect := "/paired/" + id
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", redirect)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, redirect, http.StatusSeeOther)
}

// PairedMessages — GET /paired/{id}/messages
func (h *Handlers) PairedMessages(w http.ResponseWriter, r *http.Request) {
	orch := h.kyvik.PairedOrchestrator()
	if orch == nil {
		http.Error(w, "Paired orchestrator not configured", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	ctx := r.Context()
	conv, err := orch.GetConversation(ctx, id)
	if err != nil {
		http.Error(w, "paired conversation not found", http.StatusNotFound)
		return
	}
	if err := h.ensureAgentsVisible(ctx, conv.AgentA, conv.AgentB); err != nil {
		http.Error(w, "paired conversation not found", http.StatusNotFound)
		return
	}
	msgs, err := orch.Messages(ctx, id)
	if err != nil {
		http.Error(w, "failed to load conversation messages", http.StatusInternalServerError)
		return
	}

	aName := conv.AgentA
	if cfg, err := h.kyvik.GetAgent(ctx, conv.AgentA); err == nil {
		aName = cfg.Name
	}
	bName := conv.AgentB
	if cfg, err := h.kyvik.GetAgent(ctx, conv.AgentB); err == nil {
		bName = cfg.Name
	}

	h.renderFragment(w, r, "paired-messages", map[string]any{
		"Conversation": conv,
		"AgentAName":   aName,
		"AgentBName":   bName,
		"Messages":     toPairedMessageViews(msgs, conv.AgentA),
	})
}

// PairedSSE — GET /paired/{id}/stream
func (h *Handlers) PairedSSE(w http.ResponseWriter, r *http.Request) {
	orch := h.kyvik.PairedOrchestrator()
	if orch == nil {
		http.Error(w, "Paired orchestrator not configured", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	conv, err := orch.GetConversation(r.Context(), id)
	if err != nil {
		http.Error(w, "paired conversation not found", http.StatusNotFound)
		return
	}
	if err := h.ensureAgentsVisible(r.Context(), conv.AgentA, conv.AgentB); err != nil {
		http.Error(w, "paired conversation not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	updates, unsubscribe := orch.SubscribeUpdates(id)
	defer unsubscribe()

	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		case up := <-updates:
			payload, _ := json.Marshal(up)
			fmt.Fprintf(w, "event: %s\n", up.Type)
			fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		}
	}
}

type typesafeOrchestrator struct {
	inner interface {
		Pause(context.Context, string) error
		Resume(context.Context, string) error
		Stop(context.Context, string) error
	}
}

func (o *typesafeOrchestrator) Pause(ctx context.Context, id string) error {
	return o.inner.Pause(ctx, id)
}
func (o *typesafeOrchestrator) Resume(ctx context.Context, id string) error {
	return o.inner.Resume(ctx, id)
}
func (o *typesafeOrchestrator) Stop(ctx context.Context, id string) error {
	return o.inner.Stop(ctx, id)
}

func (h *Handlers) pairedControlAction(w http.ResponseWriter, r *http.Request, action func(*typesafeOrchestrator, string) error) {
	orch := h.kyvik.PairedOrchestrator()
	if orch == nil {
		http.Error(w, "Paired orchestrator not configured", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	conv, err := orch.GetConversation(r.Context(), id)
	if err != nil {
		http.Error(w, "paired conversation not found", http.StatusNotFound)
		return
	}
	if err := h.ensureAgentsVisible(r.Context(), conv.AgentA, conv.AgentB); err != nil {
		http.Error(w, "paired conversation not found", http.StatusNotFound)
		return
	}
	shim := &typesafeOrchestrator{inner: orch}
	if err := action(shim, id); err != nil {
		http.Error(w, "operation failed", http.StatusBadRequest)
		return
	}
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", "/paired/"+id)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, "/paired/"+id, http.StatusSeeOther)
}

func (h *Handlers) runningAgents(ctx context.Context) ([]types.AgentConfig, error) {
	agents, err := h.kyvik.ListAgents(ctx)
	if err != nil {
		return nil, err
	}
	agents, err = h.filterAgentsForUser(ctx, agents)
	if err != nil {
		return nil, err
	}
	out := make([]types.AgentConfig, 0, len(agents))
	for _, a := range agents {
		if a.ActualState == types.AgentStatusRunning {
			out = append(out, a)
		}
	}
	return out, nil
}

func parseAutoStopPhrases(raw string) []string {
	s := bufio.NewScanner(strings.NewReader(raw))
	var out []string
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func toPairedMessageViews(msgs []teams.PairedMessage, agentA string) []pairedMessageView {
	out := make([]pairedMessageView, 0, len(msgs))
	for _, m := range msgs {
		side := "right"
		if m.AgentID == agentA {
			side = "left"
		}
		name := m.AgentName
		if name == "" {
			name = m.AgentID
		}
		initial := "?"
		if name != "" {
			initial = strings.ToUpper(string([]rune(name)[0]))
		}
		out = append(out, pairedMessageView{
			AgentID:      m.AgentID,
			AgentName:    name,
			Content:      m.Content,
			Tokens:       m.Tokens,
			Cost:         m.Cost,
			TurnNumber:   m.TurnNumber,
			CreatedAt:    m.CreatedAt,
			InjectedBy:   m.InjectedBy,
			Injected:     m.InjectedBy != "",
			Side:         side,
			AgentInitial: initial,
		})
	}
	return out
}

func truncateString(v string, max int) string {
	trimmed := strings.TrimSpace(v)
	if len(trimmed) <= max {
		return trimmed
	}
	return trimmed[:max] + "..."
}
