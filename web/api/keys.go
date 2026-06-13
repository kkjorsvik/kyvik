package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/kkjorsvik/kyvik/internal/auth"
)

// createKeyRequest is the JSON body for POST /keys.
type createKeyRequest struct {
	Name      string    `json:"name"`
	Scope     string    `json:"scope"`
	AgentIDs  []string  `json:"agent_ids,omitempty"`
	ExpiresAt *string   `json:"expires_at,omitempty"` // RFC3339
}

// createKeyResponse includes the plaintext key (shown once).
type createKeyResponse struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Key       string   `json:"key"`
	KeyPrefix string   `json:"key_prefix"`
	Scope     string   `json:"scope"`
	AgentIDs  []string `json:"agent_ids"`
	ExpiresAt *string  `json:"expires_at,omitempty"`
}

// HandleCreateKey handles POST /keys.
func (a *API) HandleCreateKey(w http.ResponseWriter, r *http.Request) {
	var req createKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid JSON body")
		return
	}

	if req.Scope == "" {
		req.Scope = auth.RoleViewer
	}

	var expiresAt *time.Time
	if req.ExpiresAt != nil {
		t, err := time.Parse(time.RFC3339, *req.ExpiresAt)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "expires_at must be RFC3339 format")
			return
		}
		expiresAt = &t
	}

	result, err := a.keys.Create(r.Context(), req.Name, req.Scope, req.AgentIDs, expiresAt)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	resp := createKeyResponse{
		ID:        result.Key.ID,
		Name:      result.Key.Name,
		Key:       result.PlainKey,
		KeyPrefix: result.Key.KeyPrefix,
		Scope:     result.Key.Scope,
		AgentIDs:  result.Key.AgentIDs,
	}
	if result.Key.ExpiresAt != nil {
		s := result.Key.ExpiresAt.Format(time.RFC3339)
		resp.ExpiresAt = &s
	}

	writeJSON(w, http.StatusCreated, resp)
}

// HandleListKeys handles GET /keys.
func (a *API) HandleListKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := a.keys.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Failed to list keys")
		return
	}
	// Convert to list items (never expose hash).
	items := make([]keyListItem, 0, len(keys))
	for _, k := range keys {
		items = append(items, keyListItem{
			ID:         k.ID,
			Name:       k.Name,
			KeyPrefix:  k.KeyPrefix,
			Scope:      k.Scope,
			AgentIDs:   k.AgentIDs,
			IsActive:   k.IsActive,
			ExpiresAt:  k.ExpiresAt,
			CreatedAt:  k.CreatedAt,
			LastUsedAt: k.LastUsedAt,
		})
	}
	writeJSON(w, http.StatusOK, items)
}

type keyListItem = struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	KeyPrefix  string     `json:"key_prefix"`
	Scope      string     `json:"scope"`
	AgentIDs   []string   `json:"agent_ids"`
	IsActive   bool       `json:"is_active"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

// HandleDeleteKey handles DELETE /keys/{id}.
func (a *API) HandleDeleteKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Key ID is required")
		return
	}

	if err := a.keys.Revoke(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, "not_found", "API key not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
