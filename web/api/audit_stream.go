package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kkjorsvik/kyvik/internal/audit"
)

// activeAPISSEConns tracks active API SSE connections.
var activeAPISSEConns int64

// HandleAuditStreamSSE is an SSE endpoint that streams audit events via the API.
func (a *API) HandleAuditStreamSSE(w http.ResponseWriter, r *http.Request) {
	al := a.kyvik.Audit()
	if al == nil {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit logger not available")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming_unsupported", "streaming not supported")
		return
	}

	// Connection limiter (20 max for API).
	if atomic.AddInt64(&activeAPISSEConns, 1) > 20 {
		atomic.AddInt64(&activeAPISSEConns, -1)
		writeError(w, http.StatusTooManyRequests, "too_many_streams", "too many concurrent audit streams")
		return
	}
	defer atomic.AddInt64(&activeAPISSEConns, -1)

	// Parse filters.
	filter := audit.SubscriptionFilter{
		AgentID: r.URL.Query().Get("agent_id"),
	}
	if actions := r.URL.Query().Get("action"); actions != "" {
		filter.Actions = strings.Split(actions, ",")
	}
	if levels := r.URL.Query().Get("level"); levels != "" {
		filter.Levels = strings.Split(levels, ",")
	}

	ch, err := al.Subscribe(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "subscribe_failed", err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher.Flush()

	heartbeat := time.NewTicker(30 * time.Second)
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
