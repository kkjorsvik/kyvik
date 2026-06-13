package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/kkjorsvik/kyvik/internal/apikeys"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

func (a *API) requireKyvik(w http.ResponseWriter) bool {
	if a.kyvik == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Core runtime not available")
		return false
	}
	return true
}

// HandleListAgents handles GET /agents.
func (a *API) HandleListAgents(w http.ResponseWriter, r *http.Request) {
	if !a.requireKyvik(w) {
		return
	}
	agents, err := a.kyvik.ListAgents(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Failed to list agents")
		return
	}

	// Filter by agent scope if the key is scoped.
	key := APIKeyFromContext(r.Context())
	if key != nil && len(key.AgentIDs) > 0 {
		filtered := make([]types.AgentConfig, 0)
		for _, ag := range agents {
			if apikeys.CanAccessAgent(key, ag.ID) {
				filtered = append(filtered, ag)
			}
		}
		agents = filtered
	}

	p := parsePagination(r)
	start := p.Offset
	if start > len(agents) {
		start = len(agents)
	}
	end := start + p.Limit
	if end > len(agents) {
		end = len(agents)
	}

	writeJSON(w, http.StatusOK, ListResponse[types.AgentConfig]{
		Data:    agents[start:end],
		HasMore: end < len(agents),
	})
}

// HandleCreateAgent handles POST /agents.
func (a *API) HandleCreateAgent(w http.ResponseWriter, r *http.Request) {
	if !a.requireKyvik(w) {
		return
	}
	var config types.AgentConfig
	if err := json.NewDecoder(r.Body).Decode(&config); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid JSON body")
		return
	}

	if config.CreatedAt.IsZero() {
		config.CreatedAt = time.Now().UTC()
	}
	if config.UpdatedAt.IsZero() {
		config.UpdatedAt = time.Now().UTC()
	}
	if err := a.kyvik.CreateAgent(r.Context(), config); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	// Fetch the created agent to return.
	created, err := a.kyvik.GetAgent(r.Context(), config.ID)
	if err != nil {
		writeJSON(w, http.StatusCreated, config)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

// HandleGetAgent handles GET /agents/{id}.
func (a *API) HandleGetAgent(w http.ResponseWriter, r *http.Request) {
	if !a.requireKyvik(w) {
		return
	}
	id := r.PathValue("id")
	agent, err := a.kyvik.GetAgent(r.Context(), id)
	if err != nil {
		if errors.Is(err, types.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Agent not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "Failed to get agent")
		return
	}
	writeJSON(w, http.StatusOK, agent)
}

// HandleUpdateAgent handles PUT /agents/{id}.
func (a *API) HandleUpdateAgent(w http.ResponseWriter, r *http.Request) {
	if !a.requireKyvik(w) {
		return
	}
	id := r.PathValue("id")

	var config types.AgentConfig
	if err := json.NewDecoder(r.Body).Decode(&config); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid JSON body")
		return
	}
	config.ID = id

	if err := a.kyvik.UpdateAgent(r.Context(), config); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	updated, err := a.kyvik.GetAgent(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusOK, config)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// HandleDeleteAgent handles DELETE /agents/{id}.
func (a *API) HandleDeleteAgent(w http.ResponseWriter, r *http.Request) {
	if !a.requireKyvik(w) {
		return
	}
	id := r.PathValue("id")
	if err := a.kyvik.DeleteAgent(r.Context(), id); err != nil {
		if errors.Is(err, types.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Agent not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "Failed to delete agent")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleStartAgent handles POST /agents/{id}/start.
func (a *API) HandleStartAgent(w http.ResponseWriter, r *http.Request) {
	if !a.requireKyvik(w) {
		return
	}
	id := r.PathValue("id")
	agent, err := a.kyvik.GetAgent(r.Context(), id)
	if err != nil {
		if errors.Is(err, types.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Agent not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "Failed to get agent")
		return
	}
	if err := a.kyvik.StartAgent(r.Context(), *agent); err != nil {
		writeError(w, http.StatusConflict, "conflict", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "started", "agent_id": id})
}

// HandleStopAgent handles POST /agents/{id}/stop.
func (a *API) HandleStopAgent(w http.ResponseWriter, r *http.Request) {
	if !a.requireKyvik(w) {
		return
	}
	id := r.PathValue("id")
	if err := a.kyvik.StopAgent(r.Context(), id); err != nil {
		if errors.Is(err, types.ErrAgentNotRunning) {
			writeError(w, http.StatusConflict, "conflict", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "Failed to stop agent")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped", "agent_id": id})
}

// HandleKillAgent handles POST /agents/{id}/kill.
func (a *API) HandleKillAgent(w http.ResponseWriter, r *http.Request) {
	if !a.requireKyvik(w) {
		return
	}
	id := r.PathValue("id")
	if err := a.kyvik.KillAgent(r.Context(), id); err != nil {
		if errors.Is(err, types.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Agent not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "Failed to kill agent")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "killed", "agent_id": id})
}

// HandleGetAgentStatus handles GET /agents/{id}/status.
func (a *API) HandleGetAgentStatus(w http.ResponseWriter, r *http.Request) {
	if !a.requireKyvik(w) {
		return
	}
	id := r.PathValue("id")
	status, err := a.kyvik.GetAgentStatus(r.Context(), id)
	if err != nil {
		if errors.Is(err, types.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Agent not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "Failed to get agent status")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"agent_id": id,
		"status":   status,
	})
}

// HandleSendMessage handles POST /agents/{id}/message.
func (a *API) HandleSendMessage(w http.ResponseWriter, r *http.Request) {
	if !a.requireKyvik(w) {
		return
	}
	id := r.PathValue("id")

	var msg types.Message
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid JSON body")
		return
	}
	msg.AgentID = id
	if msg.Channel == "" {
		msg.Channel = "api"
	}
	if msg.Role == "" {
		msg.Role = "user"
	}

	if err := a.kyvik.SendMessage(r.Context(), id, msg); err != nil {
		if errors.Is(err, types.ErrAgentNotRunning) {
			writeError(w, http.StatusConflict, "conflict", "Agent is not running")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "Failed to send message")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued", "agent_id": id})
}
