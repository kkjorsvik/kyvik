package handlers

import (
	"fmt"
	"net/http"

	"github.com/kkjorsvik/kyvik/internal/core"
)

// AlertsPage handles GET /alerts — renders the full alerts page.
func (h *Handlers) AlertsPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	filter := core.AlertFilter{
		SourceType: r.URL.Query().Get("type"),
		AgentID:    r.URL.Query().Get("agent"),
		Limit:      100,
	}

	alerts, err := h.kyvik.ListAlerts(ctx, filter)
	if err != nil {
		h.serverError(w, r, "listing alerts", err)
		return
	}

	data := map[string]any{
		"Nav":        "alerts",
		"Title":      "Alerts",
		"Alerts":     alerts,
		"FilterType": filter.SourceType,
		"FilterAgent": filter.AgentID,
	}

	if isHTMX(r) {
		h.renderFragment(w, r, "alerts-list", data)
		return
	}

	h.renderPageWithRequest(w, r, "alerts", data)
}

// AlertAcknowledge handles POST /alerts/ack — acknowledges an alert.
func (h *Handlers) AlertAcknowledge(w http.ResponseWriter, r *http.Request) {
	sourceType := r.FormValue("source_type")
	sourceID := r.FormValue("source_id")

	if sourceType == "" || sourceID == "" {
		http.Error(w, "source_type and source_id required", http.StatusBadRequest)
		return
	}

	if err := h.kyvik.AcknowledgeAlert(r.Context(), sourceType, sourceID); err != nil {
		h.serverError(w, r, "acknowledging alert", err)
		return
	}

	w.Header().Set("HX-Refresh", "true")
	w.WriteHeader(http.StatusOK)
}

// AlertBadge handles GET /alerts/badge — returns unacknowledged count for nav badge.
func (h *Handlers) AlertBadge(w http.ResponseWriter, r *http.Request) {
	count := h.kyvik.UnacknowledgedAlertCount(r.Context())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if count > 0 {
		fmt.Fprintf(w, `<span class="alert-badge">%d</span>`, count)
	}
}
