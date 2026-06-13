package teams

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/kkjorsvik/kyvik/internal/audit"
	"github.com/kkjorsvik/kyvik/internal/sqlutil"
	"github.com/kkjorsvik/kyvik/internal/store"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// PairedMessage represents a single turn in a paired conversation.
type PairedMessage struct {
	AgentID    string    `json:"agent_id"`
	AgentName  string    `json:"agent_name"`
	Content    string    `json:"content"`
	Tokens     int       `json:"tokens"`
	Cost       float64   `json:"cost"`
	TurnNumber int       `json:"turn_number"`
	InjectedBy string    `json:"injected_by,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// PairedUpdate is a real-time update sent to dashboard subscribers.
type PairedUpdate struct {
	Type    string                    `json:"type"`
	Message *PairedMessage            `json:"message,omitempty"`
	Conv    *types.PairedConversation `json:"conversation,omitempty"`
}

type breakerStatusProvider func(agentID string) bool

// PairedOrchestrator manages turn-based conversations between two agents.
type PairedOrchestrator struct {
	bus   *Bus
	store store.Store
	db    *sql.DB
	audit audit.Logger

	active map[string]*activeConversation
	mu     sync.RWMutex

	breakerStatus breakerStatusProvider

	updatesMu   sync.RWMutex
	subscribers map[string]map[string]chan PairedUpdate
}

type activeConversation struct {
	conv     types.PairedConversation
	cancel   context.CancelFunc
	pauseCh  chan struct{}
	resumeCh chan struct{}
	injectCh chan string
}

type usageSnapshot struct {
	tokens int64
	cost   float64
}

// NewPairedOrchestrator creates the orchestrator.
func NewPairedOrchestrator(bus *Bus, store store.Store, db *sql.DB, auditLogger audit.Logger) *PairedOrchestrator {
	return &PairedOrchestrator{
		bus:         bus,
		store:       store,
		db:          db,
		audit:       auditLogger,
		active:      make(map[string]*activeConversation),
		subscribers: make(map[string]map[string]chan PairedUpdate),
	}
}

// SetCircuitBreakerStatusProvider sets optional breaker status checks.
func (o *PairedOrchestrator) SetCircuitBreakerStatusProvider(provider func(agentID string) bool) {
	o.breakerStatus = provider
}

// Start begins a new paired conversation.
func (o *PairedOrchestrator) Start(ctx context.Context, conv types.PairedConversation) error {
	if conv.AgentA == "" || conv.AgentB == "" {
		return fmt.Errorf("both agent_a and agent_b are required")
	}
	if conv.AgentA == conv.AgentB {
		return fmt.Errorf("agent_a and agent_b must be different")
	}
	if conv.ID == "" {
		conv.ID = ulid.Make().String()
	}
	if conv.MaxTurns <= 0 {
		conv.MaxTurns = 10
	}
	if conv.TurnDelayMs < 0 {
		conv.TurnDelayMs = 0
	}
	if conv.Topic == "" {
		conv.Topic = "Start a productive conversation."
	}
	conv.Status = types.PairedStatusActive
	conv.CurrentTurn = 0
	conv.TotalTokens = 0
	conv.EstimatedCost = 0
	conv.CreatedAt = time.Now().UTC()
	conv.CompletedAt = nil

	if _, err := o.store.GetAgent(ctx, conv.AgentA); err != nil {
		return fmt.Errorf("agent_a %s: %w", conv.AgentA, err)
	}
	if _, err := o.store.GetAgent(ctx, conv.AgentB); err != nil {
		return fmt.Errorf("agent_b %s: %w", conv.AgentB, err)
	}

	o.mu.Lock()
	if _, exists := o.active[conv.ID]; exists {
		o.mu.Unlock()
		return fmt.Errorf("conversation %s already active", conv.ID)
	}

	runCtx, cancel := context.WithTimeout(ctx, deriveConversationTimeout(conv))
	ac := &activeConversation{
		conv:     conv,
		cancel:   cancel,
		pauseCh:  make(chan struct{}, 1),
		resumeCh: make(chan struct{}, 1),
		injectCh: make(chan string, 8),
	}
	o.active[conv.ID] = ac
	o.mu.Unlock()

	if err := o.insertConversation(runCtx, conv); err != nil {
		o.mu.Lock()
		delete(o.active, conv.ID)
		o.mu.Unlock()
		cancel()
		return fmt.Errorf("create paired conversation: %w", err)
	}

	go o.runConversation(runCtx, ac)
	return nil
}

// Pause pauses an active conversation after the current turn completes.
func (o *PairedOrchestrator) Pause(_ context.Context, convID string) error {
	o.mu.RLock()
	ac, ok := o.active[convID]
	o.mu.RUnlock()
	if !ok {
		return types.ErrPairedConvNotActive
	}
	select {
	case ac.pauseCh <- struct{}{}:
	default:
	}
	return nil
}

// Resume resumes a paused conversation.
func (o *PairedOrchestrator) Resume(_ context.Context, convID string) error {
	o.mu.RLock()
	ac, ok := o.active[convID]
	o.mu.RUnlock()
	if !ok {
		return types.ErrPairedConvNotActive
	}
	select {
	case ac.resumeCh <- struct{}{}:
	default:
	}
	return nil
}

// Stop terminates a conversation immediately.
func (o *PairedOrchestrator) Stop(ctx context.Context, convID string) error {
	o.mu.Lock()
	ac, ok := o.active[convID]
	if ok {
		delete(o.active, convID)
	}
	o.mu.Unlock()
	if !ok {
		return types.ErrPairedConvNotActive
	}

	ac.cancel()
	now := time.Now().UTC()
	conv, err := o.setConversationStatus(ctx, convID, types.PairedStatusStopped, &now)
	if err == nil {
		o.publishUpdate(convID, PairedUpdate{Type: "stopped", Conv: conv})
	}
	if err != nil {
		return err
	}
	return nil
}

// Delete permanently removes a paired conversation and all its messages.
func (o *PairedOrchestrator) Delete(ctx context.Context, convID string) error {
	// If active, cancel and remove from map.
	o.mu.Lock()
	if ac, ok := o.active[convID]; ok {
		delete(o.active, convID)
		ac.cancel()
	}
	o.mu.Unlock()

	// Delete from DB (CASCADE removes messages).
	res, err := sqlutil.ExecContext(ctx, o.db,
		`DELETE FROM paired_conversations WHERE id = ?`, convID,
	)
	if err != nil {
		return fmt.Errorf("delete paired conversation: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return types.ErrPairedConvNotFound
	}

	// Clean up SSE subscribers.
	o.updatesMu.Lock()
	if subs, ok := o.subscribers[convID]; ok {
		for _, ch := range subs {
			close(ch)
		}
		delete(o.subscribers, convID)
	}
	o.updatesMu.Unlock()

	return nil
}

// Inject inserts a user message into the conversation.
func (o *PairedOrchestrator) Inject(_ context.Context, convID string, message string) error {
	msg := strings.TrimSpace(message)
	if msg == "" {
		return fmt.Errorf("inject message cannot be empty")
	}

	o.mu.RLock()
	ac, ok := o.active[convID]
	o.mu.RUnlock()
	if !ok {
		return types.ErrPairedConvNotActive
	}
	if !ac.conv.AllowUserInjection {
		return fmt.Errorf("user injection disabled for conversation")
	}

	select {
	case ac.injectCh <- msg:
		return nil
	default:
		return fmt.Errorf("inject queue is full")
	}
}

// Continue resumes a completed or stopped conversation for additional turns.
func (o *PairedOrchestrator) Continue(ctx context.Context, convID string, additionalTurns int) error {
	if additionalTurns < 1 {
		return fmt.Errorf("additional_turns must be at least 1")
	}
	if additionalTurns > 100 {
		return fmt.Errorf("additional_turns must be at most 100")
	}

	conv, err := o.getConversation(ctx, convID)
	if err != nil {
		return err
	}

	if conv.Status != types.PairedStatusCompleted && conv.Status != types.PairedStatusStopped {
		return fmt.Errorf("can only continue completed or stopped conversations (current: %s)", conv.Status)
	}

	o.mu.RLock()
	_, isActive := o.active[convID]
	o.mu.RUnlock()
	if isActive {
		return fmt.Errorf("conversation %s is still active", convID)
	}

	// Verify both agents still exist.
	if _, err := o.store.GetAgent(ctx, conv.AgentA); err != nil {
		return fmt.Errorf("agent_a %s no longer exists", conv.AgentA)
	}
	if _, err := o.store.GetAgent(ctx, conv.AgentB); err != nil {
		return fmt.Errorf("agent_b %s no longer exists", conv.AgentB)
	}

	// Get last message content to use as the initial outbound.
	msgs, err := o.Messages(ctx, convID)
	if err != nil {
		return fmt.Errorf("load messages for continue: %w", err)
	}
	lastContent := conv.Topic
	if len(msgs) > 0 {
		lastContent = msgs[len(msgs)-1].Content
	}

	// Update DB: increase max_turns, reset status, clear completed_at.
	conv.MaxTurns += additionalTurns
	conv.Status = types.PairedStatusActive
	conv.CompletedAt = nil
	_, err = sqlutil.ExecContext(ctx, o.db,
		`UPDATE paired_conversations SET max_turns = ?, status = ?, completed_at = NULL WHERE id = ?`,
		conv.MaxTurns, string(conv.Status), conv.ID,
	)
	if err != nil {
		return fmt.Errorf("update conversation for continue: %w", err)
	}

	// Determine next speaker from current turn.
	nextSpeaker := conv.AgentA
	nextRecipient := conv.AgentB
	if conv.CurrentTurn%2 != 0 {
		nextSpeaker = conv.AgentB
		nextRecipient = conv.AgentA
	}

	runCtx, cancel := context.WithTimeout(ctx, deriveConversationTimeout(*conv))
	ac := &activeConversation{
		conv:     *conv,
		cancel:   cancel,
		pauseCh:  make(chan struct{}, 1),
		resumeCh: make(chan struct{}, 1),
		injectCh: make(chan string, 8),
	}

	o.mu.Lock()
	o.active[convID] = ac
	o.mu.Unlock()

	go o.runConversationContinued(runCtx, ac, lastContent, nextSpeaker, nextRecipient)
	return nil
}

// UpdateAgents changes the agents on a non-active conversation.
func (o *PairedOrchestrator) UpdateAgents(ctx context.Context, convID, newAgentA, newAgentB string) error {
	if newAgentA == "" || newAgentB == "" {
		return fmt.Errorf("both agent_a and agent_b are required")
	}
	if newAgentA == newAgentB {
		return fmt.Errorf("agent_a and agent_b must be different")
	}

	conv, err := o.getConversation(ctx, convID)
	if err != nil {
		return err
	}

	// Block if active or paused.
	if conv.Status == types.PairedStatusActive || conv.Status == types.PairedStatusPaused {
		return fmt.Errorf("cannot update agents on an %s conversation", conv.Status)
	}
	o.mu.RLock()
	_, isActive := o.active[convID]
	o.mu.RUnlock()
	if isActive {
		return fmt.Errorf("conversation %s is still active", convID)
	}

	// Verify both agents exist.
	if _, err := o.store.GetAgent(ctx, newAgentA); err != nil {
		return fmt.Errorf("agent_a %s: %w", newAgentA, err)
	}
	if _, err := o.store.GetAgent(ctx, newAgentB); err != nil {
		return fmt.Errorf("agent_b %s: %w", newAgentB, err)
	}

	_, err = sqlutil.ExecContext(ctx, o.db,
		`UPDATE paired_conversations SET agent_a = ?, agent_b = ? WHERE id = ?`,
		newAgentA, newAgentB, convID,
	)
	if err != nil {
		return fmt.Errorf("update paired conversation agents: %w", err)
	}
	return nil
}

// GetConversation returns a conversation by ID.
func (o *PairedOrchestrator) GetConversation(ctx context.Context, id string) (*types.PairedConversation, error) {
	return o.getConversation(ctx, id)
}

// ListActive returns all currently active paired conversations.
func (o *PairedOrchestrator) ListActive(_ context.Context) ([]types.PairedConversation, error) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	out := make([]types.PairedConversation, 0, len(o.active))
	for _, ac := range o.active {
		out = append(out, ac.conv)
	}
	return out, nil
}

// ListAll returns all paired conversations.
func (o *PairedOrchestrator) ListAll(ctx context.Context) ([]types.PairedConversation, error) {
	rows, err := sqlutil.QueryContext(ctx, o.db,
		`SELECT id, agent_a, agent_b, topic, max_turns, turn_delay_ms, allow_user_injection,
		        auto_stop_phrases_json, status, current_turn, total_tokens, estimated_cost,
		        created_at, completed_at
		 FROM paired_conversations ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("query paired conversations: %w", err)
	}
	defer rows.Close()

	var out []types.PairedConversation
	for rows.Next() {
		conv, err := scanConversation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, conv)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate paired conversations: %w", err)
	}
	return out, nil
}

// Messages returns all messages in a paired conversation.
func (o *PairedOrchestrator) Messages(ctx context.Context, convID string) ([]PairedMessage, error) {
	rows, err := sqlutil.QueryContext(ctx, o.db,
		`SELECT agent_id, content, tokens, cost, turn_number, injected_by, created_at
		 FROM paired_messages WHERE conversation_id = ? ORDER BY turn_number ASC, id ASC`,
		convID,
	)
	if err != nil {
		return nil, fmt.Errorf("query paired messages: %w", err)
	}
	defer rows.Close()

	var out []PairedMessage
	for rows.Next() {
		var m PairedMessage
		if err := rows.Scan(&m.AgentID, &m.Content, &m.Tokens, &m.Cost, &m.TurnNumber, &m.InjectedBy, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan paired message: %w", err)
		}
		agent, err := o.store.GetAgent(ctx, m.AgentID)
		if err == nil {
			m.AgentName = agent.Name
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate paired messages: %w", err)
	}
	return out, nil
}

// SubscribeUpdates returns a channel that receives real-time updates.
func (o *PairedOrchestrator) SubscribeUpdates(convID string) (<-chan PairedUpdate, func()) {
	ch := make(chan PairedUpdate, 32)
	subID := ulid.Make().String()

	o.updatesMu.Lock()
	if o.subscribers[convID] == nil {
		o.subscribers[convID] = make(map[string]chan PairedUpdate)
	}
	o.subscribers[convID][subID] = ch
	o.updatesMu.Unlock()

	unsubscribe := func() {
		o.updatesMu.Lock()
		if subs, ok := o.subscribers[convID]; ok {
			if c, ok := subs[subID]; ok {
				delete(subs, subID)
				close(c)
			}
			if len(subs) == 0 {
				delete(o.subscribers, convID)
			}
		}
		o.updatesMu.Unlock()
	}

	return ch, unsubscribe
}

func (o *PairedOrchestrator) runConversation(ctx context.Context, ac *activeConversation) {
	convID := ac.conv.ID
	defer func() {
		o.mu.Lock()
		delete(o.active, convID)
		o.mu.Unlock()
		ac.cancel()
	}()

	subA := o.bus.Subscribe(ctx, ac.conv.AgentA)
	subB := o.bus.Subscribe(ctx, ac.conv.AgentB)
	defer o.bus.Unsubscribe(ac.conv.AgentA, subA)
	defer o.bus.Unsubscribe(ac.conv.AgentB, subB)

	seenUsage := map[string]usageSnapshot{}
	nextSpeaker := ac.conv.AgentA
	nextRecipient := ac.conv.AgentB
	outbound := ac.conv.Topic
	pendingInjection := ""

	for {
		if err := o.applyPauseIfRequested(ctx, ac); err != nil {
			o.completeWithStatus(context.Background(), ac.conv.ID, types.PairedStatusStopped, "paused and cancelled")
			return
		}

		if inj := o.drainInject(ac); inj != "" {
			pendingInjection = inj
		}

		if stop, reason := o.shouldStopForSafety(ctx, ac.conv); stop {
			o.completeWithStatus(context.Background(), ac.conv.ID, types.PairedStatusStopped, reason)
			return
		}

		toSend := outbound
		injectedBy := ""
		if pendingInjection != "" {
			toSend = "User-injected message:\n" + pendingInjection + "\n\n" + outbound
			injectedBy = "user"
			pendingInjection = ""
		}

		if err := o.bus.Send(ctx, types.InternalMessage{
			From:     nextRecipient,
			To:       nextSpeaker,
			Content:  toSend,
			Type:     types.MessageTypeMessage,
			Priority: types.MessagePriorityNormal,
			Metadata: map[string]string{"paired_conversation_id": ac.conv.ID},
		}); err != nil {
			o.completeWithStatus(context.Background(), ac.conv.ID, types.PairedStatusStopped, fmt.Sprintf("send failed: %v", err))
			return
		}

		expectedSub := subA
		if nextRecipient == ac.conv.AgentB {
			expectedSub = subB
		}

		resp, err := waitForResult(ctx, expectedSub, nextSpeaker, nextRecipient)
		if err != nil {
			o.completeWithStatus(context.Background(), ac.conv.ID, types.PairedStatusStopped, fmt.Sprintf("wait response failed: %v", err))
			return
		}

		tokens, cost := o.resolveUsageDelta(ctx, nextSpeaker, seenUsage)
		if tokens <= 0 {
			tokens = estimateTokens(resp.Content)
		}

		turnNumber := ac.conv.CurrentTurn + 1
		createdAt := time.Now().UTC()
		if err := o.insertMessage(ctx, ac.conv.ID, PairedMessage{
			AgentID:    nextSpeaker,
			Content:    resp.Content,
			Tokens:     tokens,
			Cost:       cost,
			TurnNumber: turnNumber,
			InjectedBy: injectedBy,
			CreatedAt:  createdAt,
		}); err != nil {
			o.completeWithStatus(context.Background(), ac.conv.ID, types.PairedStatusStopped, fmt.Sprintf("persist message failed: %v", err))
			return
		}

		ac.conv.CurrentTurn = turnNumber
		ac.conv.TotalTokens += int64(tokens)
		ac.conv.EstimatedCost += cost
		updatedConv, err := o.updateConversationProgress(ctx, ac.conv)
		if err != nil {
			o.completeWithStatus(context.Background(), ac.conv.ID, types.PairedStatusStopped, fmt.Sprintf("update conversation failed: %v", err))
			return
		}
		if updatedConv != nil {
			ac.conv = *updatedConv
		}

		agentName := nextSpeaker
		if cfg, err := o.store.GetAgent(ctx, nextSpeaker); err == nil {
			agentName = cfg.Name
		}
		msg := &PairedMessage{
			AgentID:    nextSpeaker,
			AgentName:  agentName,
			Content:    resp.Content,
			Tokens:     tokens,
			Cost:       cost,
			TurnNumber: turnNumber,
			InjectedBy: injectedBy,
			CreatedAt:  createdAt,
		}
		o.publishUpdate(ac.conv.ID, PairedUpdate{Type: "message", Message: msg, Conv: &ac.conv})

		if containsStopPhrase(resp.Content, ac.conv.AutoStopPhrases) {
			o.completeWithStatus(ctx, ac.conv.ID, types.PairedStatusCompleted, "auto-stop phrase detected")
			return
		}
		if ac.conv.CurrentTurn >= ac.conv.MaxTurns*2 {
			o.completeWithStatus(ctx, ac.conv.ID, types.PairedStatusCompleted, "max turns reached")
			return
		}

		if ac.conv.TurnDelayMs > 0 {
			select {
			case <-ctx.Done():
				o.completeWithStatus(context.Background(), ac.conv.ID, types.PairedStatusStopped, "conversation cancelled")
				return
			case <-time.After(time.Duration(ac.conv.TurnDelayMs) * time.Millisecond):
			}
		}

		outbound = resp.Content
		nextSpeaker, nextRecipient = nextRecipient, nextSpeaker
	}
}

// runConversationContinued is like runConversation but picks up from where
// a completed/stopped conversation left off with a specified initial state.
func (o *PairedOrchestrator) runConversationContinued(ctx context.Context, ac *activeConversation, lastContent string, nextSpeaker, nextRecipient string) {
	convID := ac.conv.ID
	defer func() {
		o.mu.Lock()
		delete(o.active, convID)
		o.mu.Unlock()
		ac.cancel()
	}()

	subA := o.bus.Subscribe(ctx, ac.conv.AgentA)
	subB := o.bus.Subscribe(ctx, ac.conv.AgentB)
	defer o.bus.Unsubscribe(ac.conv.AgentA, subA)
	defer o.bus.Unsubscribe(ac.conv.AgentB, subB)

	seenUsage := map[string]usageSnapshot{}
	outbound := lastContent
	pendingInjection := ""

	for {
		if err := o.applyPauseIfRequested(ctx, ac); err != nil {
			o.completeWithStatus(context.Background(), ac.conv.ID, types.PairedStatusStopped, "paused and cancelled")
			return
		}

		if inj := o.drainInject(ac); inj != "" {
			pendingInjection = inj
		}

		if stop, reason := o.shouldStopForSafety(ctx, ac.conv); stop {
			o.completeWithStatus(context.Background(), ac.conv.ID, types.PairedStatusStopped, reason)
			return
		}

		toSend := outbound
		injectedBy := ""
		if pendingInjection != "" {
			toSend = "User-injected message:\n" + pendingInjection + "\n\n" + outbound
			injectedBy = "user"
			pendingInjection = ""
		}

		if err := o.bus.Send(ctx, types.InternalMessage{
			From:     nextRecipient,
			To:       nextSpeaker,
			Content:  toSend,
			Type:     types.MessageTypeMessage,
			Priority: types.MessagePriorityNormal,
			Metadata: map[string]string{"paired_conversation_id": ac.conv.ID},
		}); err != nil {
			o.completeWithStatus(context.Background(), ac.conv.ID, types.PairedStatusStopped, fmt.Sprintf("send failed: %v", err))
			return
		}

		expectedSub := subA
		if nextRecipient == ac.conv.AgentB {
			expectedSub = subB
		}

		resp, err := waitForResult(ctx, expectedSub, nextSpeaker, nextRecipient)
		if err != nil {
			o.completeWithStatus(context.Background(), ac.conv.ID, types.PairedStatusStopped, fmt.Sprintf("wait response failed: %v", err))
			return
		}

		tokens, cost := o.resolveUsageDelta(ctx, nextSpeaker, seenUsage)
		if tokens <= 0 {
			tokens = estimateTokens(resp.Content)
		}

		turnNumber := ac.conv.CurrentTurn + 1
		createdAt := time.Now().UTC()
		if err := o.insertMessage(ctx, ac.conv.ID, PairedMessage{
			AgentID:    nextSpeaker,
			Content:    resp.Content,
			Tokens:     tokens,
			Cost:       cost,
			TurnNumber: turnNumber,
			InjectedBy: injectedBy,
			CreatedAt:  createdAt,
		}); err != nil {
			o.completeWithStatus(context.Background(), ac.conv.ID, types.PairedStatusStopped, fmt.Sprintf("persist message failed: %v", err))
			return
		}

		ac.conv.CurrentTurn = turnNumber
		ac.conv.TotalTokens += int64(tokens)
		ac.conv.EstimatedCost += cost
		updatedConv, err := o.updateConversationProgress(ctx, ac.conv)
		if err != nil {
			o.completeWithStatus(context.Background(), ac.conv.ID, types.PairedStatusStopped, fmt.Sprintf("update conversation failed: %v", err))
			return
		}
		if updatedConv != nil {
			ac.conv = *updatedConv
		}

		agentName := nextSpeaker
		if cfg, err := o.store.GetAgent(ctx, nextSpeaker); err == nil {
			agentName = cfg.Name
		}
		msg := &PairedMessage{
			AgentID:    nextSpeaker,
			AgentName:  agentName,
			Content:    resp.Content,
			Tokens:     tokens,
			Cost:       cost,
			TurnNumber: turnNumber,
			InjectedBy: injectedBy,
			CreatedAt:  createdAt,
		}
		o.publishUpdate(ac.conv.ID, PairedUpdate{Type: "message", Message: msg, Conv: &ac.conv})

		if containsStopPhrase(resp.Content, ac.conv.AutoStopPhrases) {
			o.completeWithStatus(ctx, ac.conv.ID, types.PairedStatusCompleted, "auto-stop phrase detected")
			return
		}
		if ac.conv.CurrentTurn >= ac.conv.MaxTurns*2 {
			o.completeWithStatus(ctx, ac.conv.ID, types.PairedStatusCompleted, "max turns reached")
			return
		}

		if ac.conv.TurnDelayMs > 0 {
			select {
			case <-ctx.Done():
				o.completeWithStatus(context.Background(), ac.conv.ID, types.PairedStatusStopped, "conversation cancelled")
				return
			case <-time.After(time.Duration(ac.conv.TurnDelayMs) * time.Millisecond):
			}
		}

		outbound = resp.Content
		nextSpeaker, nextRecipient = nextRecipient, nextSpeaker
	}
}

func (o *PairedOrchestrator) applyPauseIfRequested(ctx context.Context, ac *activeConversation) error {
	select {
	case <-ac.pauseCh:
		conv, err := o.setConversationStatus(ctx, ac.conv.ID, types.PairedStatusPaused, nil)
		if err == nil {
			ac.conv = *conv
			o.publishUpdate(ac.conv.ID, PairedUpdate{Type: "paused", Conv: conv})
		}
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ac.resumeCh:
				resumed, err := o.setConversationStatus(context.Background(), ac.conv.ID, types.PairedStatusActive, nil)
				if err == nil {
					ac.conv = *resumed
					o.publishUpdate(ac.conv.ID, PairedUpdate{Type: "resumed", Conv: resumed})
				}
				return nil
			}
		}
	default:
		return nil
	}
}

func (o *PairedOrchestrator) drainInject(ac *activeConversation) string {
	latest := ""
	for {
		select {
		case msg := <-ac.injectCh:
			latest = msg
		default:
			return latest
		}
	}
}

func (o *PairedOrchestrator) shouldStopForSafety(ctx context.Context, conv types.PairedConversation) (bool, string) {
	if o.breakerStatus != nil {
		if o.breakerStatus(conv.AgentA) {
			return true, fmt.Sprintf("circuit breaker tripped for %s", conv.AgentA)
		}
		if o.breakerStatus(conv.AgentB) {
			return true, fmt.Sprintf("circuit breaker tripped for %s", conv.AgentB)
		}
	}

	for _, agentID := range []string{conv.AgentA, conv.AgentB} {
		exceeded, reason, err := o.isAgentBudgetExceeded(ctx, agentID)
		if err != nil {
			return true, fmt.Sprintf("spending check failed for %s: %v", agentID, err)
		}
		if exceeded {
			return true, reason
		}
	}

	return false, ""
}

func (o *PairedOrchestrator) resolveUsageDelta(ctx context.Context, agentID string, seen map[string]usageSnapshot) (int, float64) {
	var (
		tokens int64
		cost   float64
	)
	if err := sqlutil.QueryRowContext(ctx, o.db,
		`SELECT COALESCE(SUM(tokens_in + tokens_out), 0), COALESCE(SUM(cost_usd), 0)
		 FROM usage_records WHERE agent_id = ?`,
		agentID,
	).Scan(&tokens, &cost); err != nil {
		return 0, 0
	}
	prev := seen[agentID]
	deltaTokens := tokens - prev.tokens
	deltaCost := cost - prev.cost
	if deltaTokens < 0 {
		deltaTokens = 0
	}
	if deltaCost < 0 {
		deltaCost = 0
	}
	seen[agentID] = usageSnapshot{tokens: tokens, cost: cost}
	return int(deltaTokens), deltaCost
}

func (o *PairedOrchestrator) isAgentBudgetExceeded(ctx context.Context, agentID string) (bool, string, error) {
	agent, err := o.store.GetAgent(ctx, agentID)
	if err != nil {
		return false, "", err
	}
	day, err := o.store.AggregateUsage(ctx, agentID, "day")
	if err != nil {
		return false, "", err
	}
	month, err := o.store.AggregateUsage(ctx, agentID, "month")
	if err != nil {
		return false, "", err
	}

	if limit := agent.Limits.MaxSpendPerDay; limit > 0 && day.TotalCost >= limit {
		return true, fmt.Sprintf("%s exceeded daily spend limit", agentID), nil
	}
	if limit := agent.Limits.MaxSpendPerMonth; limit > 0 && month.TotalCost >= limit {
		return true, fmt.Sprintf("%s exceeded monthly spend limit", agentID), nil
	}
	if limit := agent.Limits.MaxTokensPerDay; limit > 0 && day.TotalTokens >= limit {
		return true, fmt.Sprintf("%s exceeded daily token limit", agentID), nil
	}
	if limit := agent.Limits.MaxTokensPerMonth; limit > 0 && month.TotalTokens >= limit {
		return true, fmt.Sprintf("%s exceeded monthly token limit", agentID), nil
	}

	return false, "", nil
}

func (o *PairedOrchestrator) completeWithStatus(ctx context.Context, convID string, status types.PairedStatus, reason string) {
	now := time.Now().UTC()
	conv, err := o.setConversationStatus(ctx, convID, status, &now)
	if err == nil {
		eventType := "completed"
		if status == types.PairedStatusStopped {
			eventType = "stopped"
		}
		o.publishUpdate(convID, PairedUpdate{Type: eventType, Conv: conv})
	}
	if o.audit != nil {
		_ = o.audit.Log(ctx, types.AuditEntry{
			AgentID:   convID,
			EventType: types.EventInternalMessage,
			Action:    "paired_conversation_" + string(status),
			Decision:  "allowed",
			Details:   reason,
			Timestamp: time.Now().UTC(),
		})
	}
}

func waitForResult(ctx context.Context, ch <-chan types.InternalMessage, fromAgent, toAgent string) (types.InternalMessage, error) {
	for {
		select {
		case <-ctx.Done():
			return types.InternalMessage{}, ctx.Err()
		case msg, ok := <-ch:
			if !ok {
				return types.InternalMessage{}, fmt.Errorf("bus subscription closed")
			}
			if msg.Type != types.MessageTypeResult {
				continue
			}
			if msg.From != fromAgent || msg.To != toAgent {
				continue
			}
			return msg, nil
		}
	}
}

func deriveConversationTimeout(conv types.PairedConversation) time.Duration {
	maxTurns := conv.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 10
	}
	delay := conv.TurnDelayMs
	if delay < 0 {
		delay = 0
	}
	avgResponseMs := 3000
	totalTurns := maxTurns * 2
	totalMs := totalTurns*(delay+avgResponseMs)*3 + 30000
	if totalMs < 60000 {
		totalMs = 60000
	}
	return time.Duration(totalMs) * time.Millisecond
}

func containsStopPhrase(content string, phrases []string) bool {
	lower := strings.ToLower(content)
	for _, phrase := range phrases {
		p := strings.TrimSpace(strings.ToLower(phrase))
		if p == "" {
			continue
		}
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

func estimateTokens(content string) int {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return 0
	}
	// Simple heuristic used for running totals when provider metrics aren't available.
	n := len(trimmed) / 4
	if n < 1 {
		return 1
	}
	return n
}

func (o *PairedOrchestrator) publishUpdate(convID string, update PairedUpdate) {
	o.updatesMu.RLock()
	defer o.updatesMu.RUnlock()
	for _, ch := range o.subscribers[convID] {
		select {
		case ch <- update:
		default:
		}
	}
}

func (o *PairedOrchestrator) insertConversation(ctx context.Context, conv types.PairedConversation) error {
	phrasesJSON, err := json.Marshal(conv.AutoStopPhrases)
	if err != nil {
		return fmt.Errorf("marshal auto-stop phrases: %w", err)
	}
	_, err = sqlutil.ExecContext(ctx, o.db,
		`INSERT INTO paired_conversations (
			id, agent_a, agent_b, topic, max_turns, turn_delay_ms, allow_user_injection,
			auto_stop_phrases_json, status, current_turn, total_tokens, estimated_cost, created_at, completed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		conv.ID, conv.AgentA, conv.AgentB, conv.Topic, conv.MaxTurns, conv.TurnDelayMs,
		boolToInt(conv.AllowUserInjection), string(phrasesJSON), string(conv.Status),
		conv.CurrentTurn, conv.TotalTokens, conv.EstimatedCost,
		conv.CreatedAt.UTC().Format(time.RFC3339Nano), nil,
	)
	if err != nil {
		return fmt.Errorf("insert paired conversation: %w", err)
	}
	return nil
}

func (o *PairedOrchestrator) insertMessage(ctx context.Context, convID string, msg PairedMessage) error {
	_, err := sqlutil.ExecContext(ctx, o.db,
		`INSERT INTO paired_messages (
			conversation_id, agent_id, content, tokens, cost, turn_number, injected_by, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		convID, msg.AgentID, msg.Content, msg.Tokens, msg.Cost, msg.TurnNumber, msg.InjectedBy,
		msg.CreatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("insert paired message: %w", err)
	}
	return nil
}

func (o *PairedOrchestrator) updateConversationProgress(ctx context.Context, conv types.PairedConversation) (*types.PairedConversation, error) {
	_, err := sqlutil.ExecContext(ctx, o.db,
		`UPDATE paired_conversations
		 SET current_turn = ?, total_tokens = ?, estimated_cost = ?, status = ?
		 WHERE id = ?`,
		conv.CurrentTurn, conv.TotalTokens, conv.EstimatedCost, string(conv.Status), conv.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("update paired conversation progress: %w", err)
	}
	return o.getConversation(ctx, conv.ID)
}

func (o *PairedOrchestrator) setConversationStatus(ctx context.Context, convID string, status types.PairedStatus, completedAt *time.Time) (*types.PairedConversation, error) {
	var err error
	if completedAt != nil {
		_, err = sqlutil.ExecContext(ctx, o.db,
			`UPDATE paired_conversations SET status = ?, completed_at = ? WHERE id = ?`,
			string(status), completedAt.UTC().Format(time.RFC3339Nano), convID,
		)
	} else {
		_, err = sqlutil.ExecContext(ctx, o.db,
			`UPDATE paired_conversations SET status = ? WHERE id = ?`,
			string(status), convID,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("update paired conversation status: %w", err)
	}
	return o.getConversation(ctx, convID)
}

func (o *PairedOrchestrator) getConversation(ctx context.Context, id string) (*types.PairedConversation, error) {
	row := sqlutil.QueryRowContext(ctx, o.db,
		`SELECT id, agent_a, agent_b, topic, max_turns, turn_delay_ms, allow_user_injection,
		        auto_stop_phrases_json, status, current_turn, total_tokens, estimated_cost,
		        created_at, completed_at
		 FROM paired_conversations WHERE id = ?`,
		id,
	)
	conv, err := scanConversation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, types.ErrPairedConvNotFound
	}
	if err != nil {
		return nil, err
	}
	return &conv, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanConversation(rs rowScanner) (types.PairedConversation, error) {
	var (
		conv         types.PairedConversation
		autoStopJSON string
		status       string
		allowInject  int
		completedAt  sql.NullTime
	)
	err := rs.Scan(
		&conv.ID, &conv.AgentA, &conv.AgentB, &conv.Topic,
		&conv.MaxTurns, &conv.TurnDelayMs, &allowInject,
		&autoStopJSON, &status, &conv.CurrentTurn,
		&conv.TotalTokens, &conv.EstimatedCost,
		&conv.CreatedAt, &completedAt,
	)
	if err != nil {
		return types.PairedConversation{}, err
	}
	conv.AllowUserInjection = allowInject == 1
	conv.Status = types.PairedStatus(status)
	if strings.TrimSpace(autoStopJSON) != "" {
		if err := json.Unmarshal([]byte(autoStopJSON), &conv.AutoStopPhrases); err != nil {
			return types.PairedConversation{}, fmt.Errorf("unmarshal auto_stop_phrases: %w", err)
		}
	}
	if completedAt.Valid {
		t := completedAt.Time.UTC()
		conv.CompletedAt = &t
	}
	return conv, nil
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
