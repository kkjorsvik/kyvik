// Package api provides the REST API for programmatic access to Kyvik.
package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/kkjorsvik/kyvik/internal/apikeys"
	"github.com/kkjorsvik/kyvik/internal/auth"
	"github.com/kkjorsvik/kyvik/internal/core"
)

// API is the REST API handler. It delegates to the same core.Kyvik
// as the dashboard but uses JSON responses and Bearer token auth.
type API struct {
	kyvik   *core.Kyvik
	keys    *apikeys.Service
	limiter *RateLimiter
	startAt time.Time
}

// RateLimits configures per-scope request limits (requests per minute).
type RateLimits struct {
	Viewer   int
	Operator int
	Manager  int
	Admin    int
}

// DefaultRateLimits returns sensible defaults.
func DefaultRateLimits() RateLimits {
	return RateLimits{
		Viewer:   60,
		Operator: 120,
		Manager:  120,
		Admin:    300,
	}
}

// New creates an API handler.
func New(kyvik *core.Kyvik, keys *apikeys.Service, limits ...RateLimits) *API {
	rl := DefaultRateLimits()
	if len(limits) > 0 {
		rl = limits[0]
	}
	return &API{
		kyvik:   kyvik,
		keys:    keys,
		limiter: NewRateLimiter(rl),
		startAt: time.Now().UTC(),
	}
}

// Stop shuts down the rate limiter cleanup goroutine.
func (a *API) Stop() {
	a.limiter.Stop()
}

// errorEnvelope is the standard error response format.
type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

// writeJSON encodes v as JSON with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError sends a JSON error response.
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorEnvelope{
		Error: errorBody{
			Code:    code,
			Message: message,
			Status:  status,
		},
	})
}

// ListResponse is the standard paginated list response.
type ListResponse[T any] struct {
	Data       []T    `json:"data"`
	NextCursor string `json:"next_cursor,omitempty"`
	HasMore    bool   `json:"has_more"`
}

// pagination holds parsed pagination parameters.
type pagination struct {
	Cursor string
	Limit  int
	Offset int
}

// Routes returns the API route handler to be mounted at /api/v1/.
func (a *API) Routes() http.Handler {
	mux := http.NewServeMux()

	// Wrap all routes in auth + rate limit.
	wrap := func(h http.Handler) http.Handler {
		return a.RequireAPIKey(a.RateLimit(h))
	}

	// scope wraps handler with scope enforcement.
	scope := func(perm string, h http.HandlerFunc) http.Handler {
		return wrap(RequireScope(perm, h))
	}

	// agentScope wraps handler with agent-level scope enforcement.
	agentScope := func(perm string, h http.HandlerFunc) http.Handler {
		return wrap(RequireAgentScope(perm, h))
	}

	// Status (any authenticated key).
	mux.Handle("GET /status", wrap(http.HandlerFunc(a.HandleStatus)))

	// API key management (admin only).
	mux.Handle("POST /keys", scope(auth.PermAPIKeysManage, a.HandleCreateKey))
	mux.Handle("GET /keys", scope(auth.PermAPIKeysManage, a.HandleListKeys))
	mux.Handle("DELETE /keys/{id}", scope(auth.PermAPIKeysManage, a.HandleDeleteKey))

	// Agent CRUD.
	mux.Handle("GET /agents", agentScope(auth.PermAgentView, a.HandleListAgents))
	mux.Handle("POST /agents", scope(auth.PermAgentCreate, a.HandleCreateAgent))
	mux.Handle("GET /agents/{id}", agentScope(auth.PermAgentView, a.HandleGetAgent))
	mux.Handle("PUT /agents/{id}", agentScope(auth.PermAgentEdit, a.HandleUpdateAgent))
	mux.Handle("DELETE /agents/{id}", agentScope(auth.PermAgentDelete, a.HandleDeleteAgent))

	// Agent lifecycle.
	mux.Handle("POST /agents/{id}/start", agentScope(auth.PermAgentStart, a.HandleStartAgent))
	mux.Handle("POST /agents/{id}/stop", agentScope(auth.PermAgentStop, a.HandleStopAgent))
	mux.Handle("POST /agents/{id}/kill", agentScope(auth.PermAgentKill, a.HandleKillAgent))
	mux.Handle("GET /agents/{id}/status", agentScope(auth.PermAgentView, a.HandleGetAgentStatus))
	mux.Handle("POST /agents/{id}/message", agentScope(auth.PermChatWithAgent, a.HandleSendMessage))

	// Agent resources.
	mux.Handle("GET /agents/{id}/memories", agentScope(auth.PermMemoryView, a.HandleListMemories))
	mux.Handle("GET /agents/{id}/history", agentScope(auth.PermHistoryView, a.HandleListHistory))

	// Teams.
	mux.Handle("GET /teams", scope(auth.PermTeamsView, a.HandleListTeams))
	mux.Handle("GET /teams/{id}", scope(auth.PermTeamsView, a.HandleGetTeam))

	// Spending.
	mux.Handle("GET /spending", scope(auth.PermSpendingView, a.HandleGetSpending))
	mux.Handle("GET /spending/{agent_id}", scope(auth.PermSpendingView, a.HandleGetAgentSpending))

	// Audit.
	mux.Handle("GET /audit", scope(auth.PermAuditView, a.HandleListAudit))
	mux.Handle("GET /audit/stream", scope(auth.PermAuditView, a.HandleAuditStreamSSE))

	// Backup.
	mux.Handle("POST /backup", scope(auth.PermBackupManage, a.HandleRunBackup))

	return mux
}

// parsePagination extracts cursor + limit from query params.
func parsePagination(r *http.Request) pagination {
	p := pagination{
		Cursor: r.URL.Query().Get("cursor"),
		Limit:  50,
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			p.Limit = n
		}
	}
	if p.Limit > 200 {
		p.Limit = 200
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			p.Offset = n
		}
	}
	return p
}
