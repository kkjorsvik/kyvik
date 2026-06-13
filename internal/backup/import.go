package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/kkjorsvik/kyvik/internal/secrets"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// ImportAgent extracts an agent export archive, decrypts secrets using
// the passphrase, and creates a new agent with a fresh ID.
func ImportAgent(ctx context.Context, deps ExportDeps, archivePath, passphrase string) (*types.AgentConfig, error) {
	// Extract archive.
	tmpDir, err := os.MkdirTemp("", "kyvik-import-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	if err := extractTarGz(archivePath, tmpDir); err != nil {
		return nil, fmt.Errorf("extract archive: %w", err)
	}

	// Read export JSON.
	exportPath := filepath.Join(tmpDir, "agent_export.json")
	data, err := os.ReadFile(exportPath)
	if err != nil {
		return nil, fmt.Errorf("read export json: %w", err)
	}

	var export AgentExport
	if err := json.Unmarshal(data, &export); err != nil {
		return nil, fmt.Errorf("parse export json: %w", err)
	}

	// Derive passphrase key.
	passphraseKey, err := deriveKey(passphrase, export.PassphraseSalt)
	if err != nil {
		return nil, fmt.Errorf("derive key: %w", err)
	}

	// Verify passphrase by attempting to decrypt the first secret (if any).
	if len(export.Secrets) > 0 {
		if _, err := secrets.Decrypt(passphraseKey, export.Secrets[0].Ciphertext); err != nil {
			return nil, fmt.Errorf("wrong passphrase: decryption failed")
		}
	}

	// Generate new agent ID.
	newID := ulid.Make().String()
	oldID := export.Agent.ID
	export.Agent.ID = newID
	export.Agent.Name = export.Agent.Name + " (imported)"
	export.Agent.DesiredState = types.DesiredStateStopped
	export.Agent.ActualState = types.AgentStatusStopped
	export.Agent.CreatedAt = time.Now()

	// Create agent in store.
	if err := deps.Store.CreateAgent(ctx, export.Agent); err != nil {
		return nil, fmt.Errorf("create agent: %w", err)
	}

	// Import memories.
	if deps.MemoryStore != nil && len(export.Memories) > 0 {
		// Remap agent IDs.
		for i := range export.Memories {
			export.Memories[i].AgentID = newID
			export.Memories[i].ID = 0 // let DB assign new IDs
		}
		imported, err := deps.MemoryStore.Import(ctx, newID, export.Memories)
		if err != nil {
			slog.Warn("agent import: memory import failed", "error", err)
		} else {
			slog.Info("agent import: memories imported", "count", imported)
		}
	}

	// Import conversations and messages.
	if deps.ConvStore != nil && deps.HistoryStore != nil {
		for _, convExport := range export.Conversations {
			newConv, err := deps.ConvStore.CreateConversation(ctx, newID, convExport.Conversation.Title)
			if err != nil {
				slog.Warn("agent import: conversation create failed", "error", err)
				continue
			}
			for _, msg := range convExport.Messages {
				msg.AgentID = newID
				msg.ChannelID = newConv.ID
				msg.ID = 0
				_ = deps.HistoryStore.Append(ctx, msg)
			}
		}
	}

	// Import schedules with new agent ID.
	if deps.ScheduleStore != nil && len(export.Schedules) > 0 {
		for _, sched := range export.Schedules {
			sched.ID = ulid.Make().String()
			sched.AgentID = newID
			sched.LastRunAt = nil
			sched.NextRunAt = nil
			sched.CreatedAt = time.Now()
			sched.UpdatedAt = time.Now()
			if err := deps.ScheduleStore.CreateSchedule(ctx, sched); err != nil {
				slog.Warn("agent import: schedule create failed", "name", sched.Name, "error", err)
			}
		}
	}

	// Import secrets: decrypt with passphrase key, re-encrypt with master key.
	if deps.Vault != nil {
		for _, sec := range export.Secrets {
			plaintext, err := secrets.Decrypt(passphraseKey, sec.Ciphertext)
			if err != nil {
				slog.Warn("agent import: secret decrypt failed", "key", sec.Key, "error", err)
				continue
			}
			newScope := "agent:" + newID
			if err := deps.Vault.Set(ctx, newScope, sec.Key, string(plaintext), sec.Description); err != nil {
				slog.Warn("agent import: secret store failed", "key", sec.Key, "error", err)
			}
		}
	}

	// Warn about tool grants that reference the old agent.
	if len(export.Agent.ToolGrants) > 0 {
		slog.Info("agent import: tool grants preserved from original agent",
			"agent_id", newID, "old_agent_id", oldID, "grants", export.Agent.ToolGrants)
	}

	return &export.Agent, nil
}

