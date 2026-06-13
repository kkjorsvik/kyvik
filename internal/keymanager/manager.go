// Package keymanager provisions and revokes per-agent OpenRouter API keys,
// storing them encrypted in the secrets vault.
package keymanager

import (
	"context"
	"fmt"
	"time"

	"github.com/kkjorsvik/kyvik/internal/audit"
	"github.com/kkjorsvik/kyvik/internal/models/openrouter"
	"github.com/kkjorsvik/kyvik/internal/notifications"
	"github.com/kkjorsvik/kyvik/internal/secrets"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// KeyManager handles per-agent OpenRouter key lifecycle.
type KeyManager struct {
	mgmt     *openrouter.ManagementClient
	vault    secrets.SecretStore
	audit    audit.Logger
	notifier notifications.Notifier
}

// New creates a new KeyManager.
func New(mgmt *openrouter.ManagementClient, vault secrets.SecretStore, audit audit.Logger) *KeyManager {
	return &KeyManager{
		mgmt:  mgmt,
		vault: vault,
		audit: audit,
	}
}

// SetNotifier configures a notifier for key failure alerts.
func (km *KeyManager) SetNotifier(n notifications.Notifier) {
	km.notifier = n
}

func agentScope(agentID string) string {
	return "agent:" + agentID
}

// ProvisionKey creates a new OpenRouter API key for the given agent and stores
// it in the vault. If a key already exists for this agent, it is a no-op.
func (km *KeyManager) ProvisionKey(ctx context.Context, agentID, agentName string, spendLimit float64) error {
	scope := agentScope(agentID)

	// Skip if already provisioned.
	exists, err := km.vault.Exists(ctx, scope, "openrouter:api_key")
	if err != nil {
		return fmt.Errorf("checking existing key: %w", err)
	}
	if exists {
		return nil
	}

	// Create key via management API.
	resp, err := km.mgmt.CreateKey(ctx, openrouter.CreateKeyRequest{
		Name:  "kyvik-agent-" + agentID,
		Label: agentName,
		Limit: spendLimit,
	})
	if err != nil {
		_ = km.audit.Log(ctx, types.AuditEntry{
			AgentID:   agentID,
			EventType: types.EventSecret,
			Action:    "key.provision_failed",
			Details:   fmt.Sprintf("failed to create OpenRouter key: %v", err),
			Timestamp: time.Now(),
		})
		if km.notifier != nil {
			_ = km.notifier.Send(ctx, notifications.Event{
				Type:      "key_failure",
				Severity:  "warning",
				Agent:     agentID,
				Title:     "Key provisioning failed",
				Detail:    fmt.Sprintf("Failed to create OpenRouter key: %v", err),
				Timestamp: time.Now(),
			})
		}
		return fmt.Errorf("creating OpenRouter key: %w", err)
	}

	// Store key in vault.
	if err := km.vault.Set(ctx, scope, "openrouter:api_key", resp.Key, "Per-agent OpenRouter API key"); err != nil {
		// Clean up the remote key since we can't store it.
		_ = km.mgmt.DeleteKey(ctx, resp.Hash)
		_ = km.audit.Log(ctx, types.AuditEntry{
			AgentID:   agentID,
			EventType: types.EventSecret,
			Action:    "key.provision_failed",
			Details:   fmt.Sprintf("vault write failed, remote key cleaned up: %v", err),
			Timestamp: time.Now(),
		})
		if km.notifier != nil {
			_ = km.notifier.Send(ctx, notifications.Event{
				Type:      "key_failure",
				Severity:  "warning",
				Agent:     agentID,
				Title:     "Key vault write failed",
				Detail:    fmt.Sprintf("Vault write failed, remote key cleaned up: %v", err),
				Timestamp: time.Now(),
			})
		}
		return fmt.Errorf("storing key in vault: %w", err)
	}

	// Store key hash for later revocation.
	if err := km.vault.Set(ctx, scope, "openrouter:key_hash", resp.Hash, "OpenRouter key hash for revocation"); err != nil {
		// Non-fatal — key is usable, but revocation will require manual cleanup.
		_ = km.audit.Log(ctx, types.AuditEntry{
			AgentID:   agentID,
			EventType: types.EventSecret,
			Action:    "key.provision_failed",
			Details:   fmt.Sprintf("failed to store key hash: %v", err),
			Timestamp: time.Now(),
		})
		if km.notifier != nil {
			_ = km.notifier.Send(ctx, notifications.Event{
				Type:      "key_failure",
				Severity:  "info",
				Agent:     agentID,
				Title:     "Key hash storage failed",
				Detail:    fmt.Sprintf("Failed to store key hash (non-fatal): %v", err),
				Timestamp: time.Now(),
			})
		}
	}

	_ = km.audit.Log(ctx, types.AuditEntry{
		AgentID:   agentID,
		EventType: types.EventSecret,
		Action:    "key.provisioned",
		Details:   fmt.Sprintf("provisioned OpenRouter key for agent %s", agentID),
		Timestamp: time.Now(),
	})

	return nil
}

// RevokeKey deletes an agent's OpenRouter key remotely and removes it from the vault.
func (km *KeyManager) RevokeKey(ctx context.Context, agentID string) error {
	scope := agentScope(agentID)

	// Get key hash for remote deletion.
	hash, err := km.vault.Get(ctx, scope, "openrouter:key_hash")
	if err != nil {
		// No hash means no key was provisioned — no-op.
		return nil
	}

	// Delete the remote key. Log warning on failure but continue to clean vault.
	if err := km.mgmt.DeleteKey(ctx, hash); err != nil {
		_ = km.audit.Log(ctx, types.AuditEntry{
			AgentID:   agentID,
			EventType: types.EventSecret,
			Action:    "key.revoke_remote_failed",
			Details:   fmt.Sprintf("failed to delete remote key: %v", err),
			Timestamp: time.Now(),
		})
		if km.notifier != nil {
			_ = km.notifier.Send(ctx, notifications.Event{
				Type:      "key_failure",
				Severity:  "warning",
				Agent:     agentID,
				Title:     "Key revocation failed",
				Detail:    fmt.Sprintf("Failed to delete remote key: %v", err),
				Timestamp: time.Now(),
			})
		}
	}

	// Clean up vault entries regardless of remote deletion result.
	_ = km.vault.Delete(ctx, scope, "openrouter:api_key")
	_ = km.vault.Delete(ctx, scope, "openrouter:key_hash")

	_ = km.audit.Log(ctx, types.AuditEntry{
		AgentID:   agentID,
		EventType: types.EventSecret,
		Action:    "key.revoked",
		Details:   fmt.Sprintf("revoked OpenRouter key for agent %s", agentID),
		Timestamp: time.Now(),
	})

	return nil
}

// GetKeyForAgent retrieves the per-agent API key from the vault.
// Returns the key or an error if not found.
func (km *KeyManager) GetKeyForAgent(ctx context.Context, agentID string) (string, error) {
	return km.vault.Get(ctx, agentScope(agentID), "openrouter:api_key")
}

// ResolverFunc returns a KeyResolverFunc suitable for use with
// openrouter.WithKeyResolver.
func (km *KeyManager) ResolverFunc() openrouter.KeyResolverFunc {
	return km.GetKeyForAgent
}
