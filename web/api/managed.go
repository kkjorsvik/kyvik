package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"github.com/kkjorsvik/kyvik/internal/auth"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// ManagedStore is the subset of users.Store required by the managed API.
type ManagedStore interface {
	CreateUser(ctx context.Context, user types.User) error
	GetUser(ctx context.Context, id string) (*types.User, error)
	UpdateUser(ctx context.Context, user types.User) error

	ListUserGroupRoles(ctx context.Context, userID string) ([]types.UserGroupRole, error)
	SetUserGroupRole(ctx context.Context, ugr types.UserGroupRole) error
	DeleteUserGroupRole(ctx context.Context, userID, groupID string) error

	DeleteSessionsByUserID(ctx context.Context, userID string) (int64, error)
}

// ManagedAPI exposes Sett-to-Kyvik sync endpoints.
// These are mounted only when auth.type == "delegated".
type ManagedAPI struct {
	store         ManagedStore
	sharedSecret  string
	version       string
	healthLimiter *rate.Limiter
}

// NewManagedAPI creates a new managed API handler.
func NewManagedAPI(store ManagedStore, sharedSecret, version string) *ManagedAPI {
	return &ManagedAPI{
		store:         store,
		sharedSecret:  sharedSecret,
		version:       version,
		healthLimiter: rate.NewLimiter(60, 10), // 60 req/s sustained, burst of 10
	}
}

// Routes returns the managed API mux with all three endpoints.
func (m *ManagedAPI) Routes() http.Handler {
	mux := http.NewServeMux()

	// Authenticated endpoints.
	mux.Handle("PUT /users/{id}", m.RequireSharedSecret(http.HandlerFunc(m.HandleUpsertUser)))
	mux.Handle("DELETE /users/{id}/sessions", m.RequireSharedSecret(http.HandlerFunc(m.HandleRevokeSessions)))

	// Public health endpoint.
	mux.HandleFunc("GET /health", m.HandleHealth)

	return mux
}

// RequireSharedSecret is middleware that validates the Authorization: Bearer {shared_secret} header
// using constant-time comparison.
func (m *ManagedAPI) RequireSharedSecret(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if header == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing authorization header")
			return
		}

		const prefix = "Bearer "
		if !strings.HasPrefix(header, prefix) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid authorization format")
			return
		}

		token := header[len(prefix):]
		if subtle.ConstantTimeCompare([]byte(token), []byte(m.sharedSecret)) != 1 {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid shared secret")
			return
		}

		next.ServeHTTP(w, r)
	})
}

// upsertUserRequest is the request body for PUT /users/{id}.
type upsertUserRequest struct {
	Username    string            `json:"username"`
	DisplayName string            `json:"display_name"`
	IsAdmin     bool              `json:"is_admin"`
	IsActive    bool              `json:"is_active"`
	Roles       []upsertUserRole  `json:"roles"`
}

// upsertUserRole is a group role assignment in the upsert request.
type upsertUserRole struct {
	GroupID string `json:"group_id"`
	Role    string `json:"role"`
}

// HandleUpsertUser handles PUT /users/{id} -- Sett pushes a user record + roles.
func (m *ManagedAPI) HandleUpsertUser(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("id")
	if userID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "missing user ID in path")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB limit
	var req upsertUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		return
	}

	if strings.TrimSpace(req.Username) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "username is required")
		return
	}

	// Validate roles.
	for _, role := range req.Roles {
		if strings.TrimSpace(role.GroupID) == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "role group_id is required")
			return
		}
		if !auth.IsDefaultRole(role.Role) {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid role: "+role.Role)
			return
		}
	}

	ctx := r.Context()
	now := time.Now().UTC()

	// Try to get existing user to determine create vs update.
	existing, err := m.store.GetUser(ctx, userID)
	if err != nil && !errors.Is(err, types.ErrUserNotFound) {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to lookup user")
		return
	}

	if existing == nil {
		// Create new user.
		displayName := strings.TrimSpace(req.DisplayName)
		if displayName == "" {
			displayName = req.Username
		}
		user := types.User{
			ID:          userID,
			Username:    req.Username,
			DisplayName: displayName,
			IsAdmin:     req.IsAdmin,
			IsActive:    req.IsActive,
			CreatedAt:   now,
		}
		if err := m.store.CreateUser(ctx, user); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to create user")
			return
		}
	} else {
		// Update existing user.
		existing.Username = req.Username
		if strings.TrimSpace(req.DisplayName) != "" {
			existing.DisplayName = req.DisplayName
		}
		existing.IsAdmin = req.IsAdmin
		existing.IsActive = req.IsActive
		if err := m.store.UpdateUser(ctx, *existing); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to update user")
			return
		}
	}

	// Sync roles: clear existing, then set new ones.
	if err := m.syncRoles(ctx, userID, req.Roles); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to sync roles")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// syncRoles clears existing group roles for a user and sets new ones.
func (m *ManagedAPI) syncRoles(ctx context.Context, userID string, roles []upsertUserRole) error {
	// Get current roles.
	existing, err := m.store.ListUserGroupRoles(ctx, userID)
	if err != nil {
		return err
	}

	// Delete all existing roles.
	for _, ugr := range existing {
		if err := m.store.DeleteUserGroupRole(ctx, userID, ugr.GroupID); err != nil {
			return err
		}
	}

	// Set new roles.
	for _, r := range roles {
		if !auth.IsDefaultRole(r.Role) {
			continue
		}
		if err := m.store.SetUserGroupRole(ctx, types.UserGroupRole{
			UserID:  userID,
			GroupID: r.GroupID,
			Role:    r.Role,
		}); err != nil {
			return err
		}
	}
	return nil
}

// HandleRevokeSessions handles DELETE /users/{id}/sessions -- immediate session revocation.
func (m *ManagedAPI) HandleRevokeSessions(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("id")
	if userID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "missing user ID in path")
		return
	}

	count, err := m.store.DeleteSessionsByUserID(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to revoke sessions")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": count})
}

// HandleHealth handles GET /health -- instance health check for Sett.
// Rate limited since this endpoint is public (no auth).
func (m *ManagedAPI) HandleHealth(w http.ResponseWriter, _ *http.Request) {
	if !m.healthLimiter.Allow() {
		writeError(w, http.StatusTooManyRequests, "rate_limit_exceeded", "health endpoint rate limit exceeded")
		return
	}
	resp := map[string]any{
		"status": "ok",
	}
	if m.version != "" {
		resp["version"] = m.version
	}
	writeJSON(w, http.StatusOK, resp)
}
