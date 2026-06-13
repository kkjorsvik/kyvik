package discord

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"math/big"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

// DiscordAuthorizer is the narrow store interface for Discord user authorization.
type DiscordAuthorizer interface {
	GetDiscordAuth(ctx context.Context, agentID, discordUserID string) (*types.DiscordAuthorization, error)
	CreateDiscordAuth(ctx context.Context, auth types.DiscordAuthorization) error
	UpdateDiscordAuth(ctx context.Context, auth types.DiscordAuthorization) error
}

// pairingCodeAlphabet excludes ambiguous characters: 0/O, 1/I.
const pairingCodeAlphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"

const pairingCodeLength = 6
const pairingCodeTTL = 15 * time.Minute

// generatePairingCode creates a 6-char uppercase alphanumeric code using crypto/rand.
func generatePairingCode() (string, error) {
	code := make([]byte, pairingCodeLength)
	for i := range code {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(pairingCodeAlphabet))))
		if err != nil {
			return "", fmt.Errorf("generate pairing code: %w", err)
		}
		code[i] = pairingCodeAlphabet[n.Int64()]
	}
	return string(code), nil
}

// checkAuth checks whether a Discord user is authorized to message an agent.
// Returns true if the message should be forwarded, false if it should be dropped.
// When the user is not approved, a reply is sent with the pairing code.
func (a *DiscordAdapter) checkAuth(ctx context.Context, agentID string, evt discordEvent) bool {
	log := slog.With("channel", "discord", "agent_id", agentID, "discord_user", evt.userID)

	if a.auth == nil {
		return true // auth not configured
	}

	a.mu.RLock()
	mode := a.authMode[agentID]
	a.mu.RUnlock()

	if mode != types.DiscordAuthModeRestricted {
		return true // open mode or empty (default)
	}

	existing, err := a.auth.GetDiscordAuth(ctx, agentID, evt.userID)
	if err != nil && err != types.ErrNotFound {
		log.Error("discord auth lookup failed", "error", err)
		return false // fail closed
	}

	now := time.Now().UTC()

	if existing != nil {
		switch existing.Status {
		case types.DiscordAuthStatusApproved:
			return true
		case types.DiscordAuthStatusDenied:
			return false // silently drop
		case types.DiscordAuthStatusPending:
			// Check if code is still valid.
			if existing.CodeExpiresAt != nil && existing.CodeExpiresAt.After(now) {
				a.sendAuthReply(evt.channelID, existing.PairingCode)
				return false
			}
			// Code expired — regenerate.
			code, err := generatePairingCode()
			if err != nil {
				log.Error("failed to generate pairing code", "error", err)
				return false
			}
			expires := now.Add(pairingCodeTTL)
			existing.PairingCode = code
			existing.CodeExpiresAt = &expires
			existing.UpdatedAt = now
			if err := a.auth.UpdateDiscordAuth(ctx, *existing); err != nil {
				log.Error("failed to update pairing code", "error", err)
				return false
			}
			a.sendAuthReply(evt.channelID, code)
			return false
		}
	}

	// No record — create pending with new code.
	code, err := generatePairingCode()
	if err != nil {
		log.Error("failed to generate pairing code", "error", err)
		return false
	}
	expires := now.Add(pairingCodeTTL)
	auth := types.DiscordAuthorization{
		ID:            ulid.Make().String(),
		AgentID:       agentID,
		DiscordUserID: evt.userID,
		Status:        types.DiscordAuthStatusPending,
		PairingCode:   code,
		AddedBy:       types.DiscordAuthAddedByPairing,
		CodeExpiresAt: &expires,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := a.auth.CreateDiscordAuth(ctx, auth); err != nil {
		log.Error("failed to create discord auth", "error", err)
		return false
	}

	log.Info("discord auth: new pairing code issued", "discord_user", evt.userID)
	a.sendAuthReply(evt.channelID, code)
	return false
}

// sendAuthReply sends the pairing code message to the Discord channel.
func (a *DiscordAdapter) sendAuthReply(channelID, code string) {
	msg := fmt.Sprintf("To use this agent, enter this code in the Kyvik dashboard: **%s**\nThis code expires in 15 minutes.", code)
	if _, err := a.api.ChannelMessageSend(channelID, msg); err != nil {
		slog.Error("discord auth: failed to send pairing code", "channel", channelID, "error", err)
	}
}
