package handlers

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

// discordAuthStore is the narrow store interface for Discord authorization handlers.
type discordAuthStore interface {
	GetDiscordAuthByCode(ctx context.Context, code string) (*types.DiscordAuthorization, error)
	UpdateDiscordAuth(ctx context.Context, auth types.DiscordAuthorization) error
	ListDiscordAuths(ctx context.Context, agentID string) ([]types.DiscordAuthorization, error)
	DeleteDiscordAuth(ctx context.Context, id string) error
	CreateDiscordAuth(ctx context.Context, auth types.DiscordAuthorization) error
}

// SetDiscordAuthStore sets the store for Discord authorization management.
func (h *Handlers) SetDiscordAuthStore(s discordAuthStore) {
	h.discordAuthStore = s
}

// DiscordAuthRedeem handles POST /discord-auth/redeem — any authenticated user
// can redeem a pairing code to prove they have web access.
func (h *Handlers) DiscordAuthRedeem(w http.ResponseWriter, r *http.Request) {
	if h.discordAuthStore == nil {
		http.Error(w, "discord auth not configured", http.StatusServiceUnavailable)
		return
	}

	code := r.FormValue("code")
	if code == "" {
		http.Error(w, "code is required", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	auth, err := h.discordAuthStore.GetDiscordAuthByCode(ctx, code)
	if err != nil {
		http.Error(w, "invalid or expired code", http.StatusBadRequest)
		return
	}

	if auth.Status != types.DiscordAuthStatusPending {
		http.Error(w, "code already used", http.StatusBadRequest)
		return
	}

	now := time.Now().UTC()
	if auth.CodeExpiresAt != nil && auth.CodeExpiresAt.Before(now) {
		http.Error(w, "code has expired", http.StatusBadRequest)
		return
	}

	auth.Status = types.DiscordAuthStatusApproved
	auth.PairingCode = ""
	auth.CodeExpiresAt = nil
	auth.UpdatedAt = now

	if err := h.discordAuthStore.UpdateDiscordAuth(ctx, *auth); err != nil {
		h.serverError(w, r, "approving discord auth", err)
		return
	}

	if isHTMX(r) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<div class="alert alert-success">Discord user authorized successfully.</div>`)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// DiscordAuthPage renders the Discord authorization management page for an agent.
func (h *Handlers) DiscordAuthPage(w http.ResponseWriter, r *http.Request) {
	if h.discordAuthStore == nil {
		http.Error(w, "discord auth not configured", http.StatusServiceUnavailable)
		return
	}

	agentID := r.PathValue("id")
	ctx := r.Context()

	agent, err := h.kyvik.GetAgent(ctx, agentID)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	auths, err := h.discordAuthStore.ListDiscordAuths(ctx, agentID)
	if err != nil {
		h.serverError(w, r, "listing discord authorizations", err)
		return
	}

	data := map[string]any{
		"Nav":    "agents",
		"Title":  fmt.Sprintf("Discord Auth — %s", agent.Name),
		"Agent":  agent,
		"Auths":  auths,
		"Now":    time.Now().UTC(),
	}

	h.renderPageWithRequest(w, r, "discord-auth-list", data)
}

// DiscordAuthTableFragment renders the auth table for HTMX updates.
func (h *Handlers) DiscordAuthTableFragment(w http.ResponseWriter, r *http.Request) {
	if h.discordAuthStore == nil {
		http.Error(w, "discord auth not configured", http.StatusServiceUnavailable)
		return
	}

	agentID := r.PathValue("id")
	ctx := r.Context()

	auths, err := h.discordAuthStore.ListDiscordAuths(ctx, agentID)
	if err != nil {
		h.serverError(w, r, "listing discord authorizations", err)
		return
	}

	h.renderFragment(w, r, "discord-auth-table", map[string]any{
		"Agent": map[string]string{"ID": agentID},
		"Auths": auths,
		"Now":   time.Now().UTC(),
	})
}

// DiscordAuthApprove handles POST /agents/{id}/discord-auth/{user_id}/approve.
func (h *Handlers) DiscordAuthApprove(w http.ResponseWriter, r *http.Request) {
	h.discordAuthSetStatus(w, r, types.DiscordAuthStatusApproved)
}

// DiscordAuthDeny handles POST /agents/{id}/discord-auth/{user_id}/deny.
func (h *Handlers) DiscordAuthDeny(w http.ResponseWriter, r *http.Request) {
	h.discordAuthSetStatus(w, r, types.DiscordAuthStatusDenied)
}

func (h *Handlers) discordAuthSetStatus(w http.ResponseWriter, r *http.Request, status string) {
	if h.discordAuthStore == nil {
		http.Error(w, "discord auth not configured", http.StatusServiceUnavailable)
		return
	}

	agentID := r.PathValue("id")
	userID := r.PathValue("user_id")
	ctx := r.Context()

	auths, err := h.discordAuthStore.ListDiscordAuths(ctx, agentID)
	if err != nil {
		http.Error(w, "failed to lookup authorization", http.StatusInternalServerError)
		return
	}

	var target *types.DiscordAuthorization
	for i := range auths {
		if auths[i].DiscordUserID == userID {
			target = &auths[i]
			break
		}
	}
	if target == nil {
		http.Error(w, "authorization not found", http.StatusNotFound)
		return
	}

	target.Status = status
	target.PairingCode = ""
	target.CodeExpiresAt = nil
	target.UpdatedAt = time.Now().UTC()

	if err := h.discordAuthStore.UpdateDiscordAuth(ctx, *target); err != nil {
		h.serverError(w, r, "updating discord auth status", err)
		return
	}

	h.discordAuthRenderTable(w, r, agentID)
}

// DiscordAuthDelete handles POST /agents/{id}/discord-auth/{user_id}/delete.
func (h *Handlers) DiscordAuthDelete(w http.ResponseWriter, r *http.Request) {
	if h.discordAuthStore == nil {
		http.Error(w, "discord auth not configured", http.StatusServiceUnavailable)
		return
	}

	agentID := r.PathValue("id")
	userID := r.PathValue("user_id")
	ctx := r.Context()

	auths, err := h.discordAuthStore.ListDiscordAuths(ctx, agentID)
	if err != nil {
		http.Error(w, "failed to lookup authorization", http.StatusInternalServerError)
		return
	}

	for _, a := range auths {
		if a.DiscordUserID == userID {
			if err := h.discordAuthStore.DeleteDiscordAuth(ctx, a.ID); err != nil {
				h.serverError(w, r, "deleting discord auth", err)
				return
			}
			break
		}
	}

	h.discordAuthRenderTable(w, r, agentID)
}

// DiscordAuthAllowlist handles POST /agents/{id}/discord-auth/allowlist — add a
// Discord user ID directly as approved (admin allowlist).
func (h *Handlers) DiscordAuthAllowlist(w http.ResponseWriter, r *http.Request) {
	if h.discordAuthStore == nil {
		http.Error(w, "discord auth not configured", http.StatusServiceUnavailable)
		return
	}

	agentID := r.PathValue("id")
	discordUserID := r.FormValue("discord_user_id")
	if discordUserID == "" {
		http.Error(w, "discord_user_id is required", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	now := time.Now().UTC()

	auth := types.DiscordAuthorization{
		ID:            uuid.New().String(),
		AgentID:       agentID,
		DiscordUserID: discordUserID,
		Status:        types.DiscordAuthStatusApproved,
		AddedBy:       types.DiscordAuthAddedByAllowlist,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	if err := h.discordAuthStore.CreateDiscordAuth(ctx, auth); err != nil {
		h.serverError(w, r, "adding discord auth allowlist entry", err)
		return
	}

	h.discordAuthRenderTable(w, r, agentID)
}

// discordAuthRenderTable re-renders the auth table after a mutation.
func (h *Handlers) discordAuthRenderTable(w http.ResponseWriter, r *http.Request, agentID string) {
	auths, err := h.discordAuthStore.ListDiscordAuths(r.Context(), agentID)
	if err != nil {
		h.serverError(w, r, "listing discord authorizations", err)
		return
	}

	h.renderFragment(w, r, "discord-auth-table", map[string]any{
		"Agent": map[string]string{"ID": agentID},
		"Auths": auths,
		"Now":   time.Now().UTC(),
	})
}
