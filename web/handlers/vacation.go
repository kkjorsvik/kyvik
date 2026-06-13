package handlers

import (
	"net/http"
)

// ActivateVacation handles POST /vacation/activate.
func (h *Handlers) ActivateVacation(w http.ResponseWriter, r *http.Request) {
	message := r.FormValue("message")
	if err := h.kyvik.ActivateVacationMode(r.Context(), "dashboard", message); err != nil {
		h.serverError(w, r, "activating vacation mode", err)
		return
	}
	w.Header().Set("HX-Refresh", "true")
	w.WriteHeader(http.StatusOK)
}

// DeactivateVacation handles POST /vacation/deactivate.
func (h *Handlers) DeactivateVacation(w http.ResponseWriter, r *http.Request) {
	if err := h.kyvik.DeactivateVacationMode(r.Context()); err != nil {
		h.serverError(w, r, "deactivating vacation mode", err)
		return
	}
	w.Header().Set("HX-Refresh", "true")
	w.WriteHeader(http.StatusOK)
}
