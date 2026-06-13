package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kkjorsvik/kyvik/internal/audit"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

const auditPageSize = 50

// activeSSEConns tracks active SSE connections for the connection limiter.
var activeSSEConns int64

// AuditPage renders the audit log dashboard page.
func (h *Handlers) AuditPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	agentFilter := r.URL.Query().Get("agent_id")
	decisionFilter := r.URL.Query().Get("decision")
	riskLevelFilter := r.URL.Query().Get("risk_level")
	filter := audit.Filter{AgentID: agentFilter, Decision: decisionFilter, RiskLevel: riskLevelFilter, Limit: auditPageSize + 1}

	// Load initial audit entries.
	var entries []types.AuditEntry
	if al := h.kyvik.Audit(); al != nil {
		entries, _ = al.Query(ctx, filter)
	}

	hasMore := len(entries) > auditPageSize
	if hasMore {
		entries = entries[:auditPageSize]
	}

	// Load agent list for filter dropdown.
	agents, _ := h.kyvik.ListAgents(ctx)

	data := map[string]any{
		"Nav":             "audit",
		"Title":           "Audit Log",
		"AuditEntries":    entries,
		"Agents":          agents,
		"HasMore":         hasMore,
		"NextOffset":      auditPageSize,
		"AgentFilter":     agentFilter,
		"DecisionFilter":  decisionFilter,
		"RiskLevelFilter": riskLevelFilter,
	}
	h.renderPageWithRequest(w, r, "audit-index", data)
}

// AuditEntriesFragment returns audit table rows for "Load More" pagination.
func (h *Handlers) AuditEntriesFragment(w http.ResponseWriter, r *http.Request) {
	al := h.kyvik.Audit()
	if al == nil {
		http.Error(w, "audit logger not available", http.StatusServiceUnavailable)
		return
	}

	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	agentFilter := r.URL.Query().Get("agent_id")
	decisionFilter := r.URL.Query().Get("decision")
	riskLevelFilter := r.URL.Query().Get("risk_level")

	filter := audit.Filter{
		AgentID:   agentFilter,
		Decision:  decisionFilter,
		RiskLevel: riskLevelFilter,
		Limit:     auditPageSize + 1,
		Offset:    offset,
	}

	entries, _ := al.Query(r.Context(), filter)

	hasMore := len(entries) > auditPageSize
	if hasMore {
		entries = entries[:auditPageSize]
	}

	h.renderFragment(w, r, "audit-entries-fragment", map[string]any{
		"AuditEntries":    entries,
		"HasMore":         hasMore,
		"NextOffset":      offset + auditPageSize,
		"AgentFilter":     agentFilter,
		"DecisionFilter":  decisionFilter,
		"RiskLevelFilter": riskLevelFilter,
	})
}

// AuditEntryDetail renders a modal dialog with full audit entry details.
func (h *Handlers) AuditEntryDetail(w http.ResponseWriter, r *http.Request) {
	entryID := r.PathValue("entryID")
	if entryID == "" {
		http.Error(w, "missing entry ID", http.StatusBadRequest)
		return
	}

	al := h.kyvik.Audit()
	if al == nil {
		http.Error(w, "audit logger not available", http.StatusServiceUnavailable)
		return
	}

	entries, err := al.Query(r.Context(), audit.Filter{ID: entryID, Limit: 1})
	if err != nil || len(entries) == 0 {
		http.Error(w, "audit entry not found", http.StatusNotFound)
		return
	}

	h.renderFragment(w, r, "audit-detail-modal", entries[0])
}

// AuditStreamSSE is an SSE endpoint that streams audit events in real-time.
func (h *Handlers) AuditStreamSSE(w http.ResponseWriter, r *http.Request) {
	al := h.kyvik.Audit()
	if al == nil {
		http.Error(w, "audit logger not available", http.StatusServiceUnavailable)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Connection limiter.
	maxConns := int64(h.auditStreamCfg.MaxConnections)
	if maxConns <= 0 {
		maxConns = 20
	}
	if atomic.AddInt64(&activeSSEConns, 1) > maxConns {
		atomic.AddInt64(&activeSSEConns, -1)
		http.Error(w, "too many concurrent audit streams", http.StatusTooManyRequests)
		return
	}
	defer atomic.AddInt64(&activeSSEConns, -1)

	// Parse filters from query params.
	filter := audit.SubscriptionFilter{
		AgentID: r.URL.Query().Get("agent_id"),
	}
	if actions := r.URL.Query().Get("action"); actions != "" {
		filter.Actions = strings.Split(actions, ",")
	}
	if levels := r.URL.Query().Get("level"); levels != "" {
		filter.Levels = strings.Split(levels, ",")
	}
	if decisions := r.URL.Query().Get("decision"); decisions != "" {
		filter.Decisions = strings.Split(decisions, ",")
	}

	ch, err := al.Subscribe(r.Context(), filter)
	if err != nil {
		h.serverError(w, r, "subscribing to audit stream", err)
		return
	}

	// Set SSE headers.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher.Flush()

	heartbeatSec := h.auditStreamCfg.HeartbeatSec
	if heartbeatSec <= 0 {
		heartbeatSec = 30
	}
	heartbeat := time.NewTicker(time.Duration(heartbeatSec) * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case entry, ok := <-ch:
			if !ok {
				return
			}
			data, _ := json.Marshal(map[string]any{
				"id":         entry.ID,
				"timestamp":  entry.Timestamp.Format(time.RFC3339),
				"agent_id":   entry.AgentID,
				"event_type": entry.EventType,
				"action":     entry.Action,
				"details":    entry.Details,
				"decision":   entry.Decision,
				"risk_level": entry.RiskLevel,
			})
			fmt.Fprintf(w, "event: audit\ndata: %s\n\n", data)
			flusher.Flush()
		}
	}
}
