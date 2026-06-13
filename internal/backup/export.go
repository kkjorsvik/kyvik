package backup

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/scrypt"

	"github.com/kkjorsvik/kyvik/internal/history"
	"github.com/kkjorsvik/kyvik/internal/memory"
	"github.com/kkjorsvik/kyvik/internal/secrets"
	"github.com/kkjorsvik/kyvik/internal/store"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// AgentExport is the JSON structure written inside an agent export archive.
type AgentExport struct {
	Version        string            `json:"version"`
	ExportedAt     time.Time         `json:"exported_at"`
	Agent          types.AgentConfig `json:"agent"`
	Memories       []memory.Memory   `json:"memories,omitempty"`
	Conversations  []ConvExport      `json:"conversations,omitempty"`
	Secrets        []EncryptedSecret `json:"secrets,omitempty"`
	Schedules      []types.Schedule  `json:"schedules,omitempty"`
	PassphraseSalt []byte            `json:"passphrase_salt"`
}

// ConvExport bundles a conversation with its messages.
type ConvExport struct {
	Conversation history.WebConversation `json:"conversation"`
	Messages     []history.HistoryEntry  `json:"messages"`
}

// EncryptedSecret holds a re-encrypted secret value.
type EncryptedSecret struct {
	Scope       string `json:"scope"`
	Key         string `json:"key"`
	Description string `json:"description"`
	Ciphertext  []byte `json:"ciphertext"` // encrypted with passphrase-derived key
}

// ExportDeps holds the stores needed for agent export/import.
type ExportDeps struct {
	Store         store.Store
	Vault         secrets.SecretStore
	MemoryStore   memory.MemoryStore
	HistoryStore  history.HistoryStore
	ConvStore     history.ConversationStore
	ScheduleStore store.Store // same as Store, uses ListSchedules
}

// ExportAgent creates a compressed archive containing an agent's configuration,
// memories, conversations, schedules, and re-encrypted secrets.
//
// Secrets are decrypted with the master key and re-encrypted with a key derived
// from the user-supplied passphrase using scrypt.
func ExportAgent(ctx context.Context, deps ExportDeps, agentID, passphrase, tmpDir string) (string, error) {
	// Get agent config.
	agent, err := deps.Store.GetAgent(ctx, agentID)
	if err != nil {
		return "", fmt.Errorf("get agent: %w", err)
	}

	// Generate salt and derive passphrase key.
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	passphraseKey, err := deriveKey(passphrase, salt)
	if err != nil {
		return "", fmt.Errorf("derive key: %w", err)
	}

	export := AgentExport{
		Version:        "1",
		ExportedAt:     time.Now(),
		Agent:          *agent,
		PassphraseSalt: salt,
	}

	// Memories (including embeddings).
	if deps.MemoryStore != nil {
		allMems, err := deps.MemoryStore.List(ctx, agentID, memory.ListOptions{
			IncludeArchived: boolPtr(true),
			Limit:           10000,
		})
		if err == nil {
			export.Memories = allMems
		}
	}

	// Conversations and messages.
	if deps.ConvStore != nil {
		convs, err := deps.ConvStore.ListConversations(ctx, agentID)
		if err == nil {
			for _, conv := range convs {
				var msgs []history.HistoryEntry
				if deps.HistoryStore != nil {
					msgs, _ = deps.HistoryStore.Recent(ctx, agentID, "webui", conv.ID, 10000)
				}
				export.Conversations = append(export.Conversations, ConvExport{
					Conversation: conv,
					Messages:     msgs,
				})
			}
		}
	}

	// Schedules.
	if deps.ScheduleStore != nil {
		scheds, err := deps.ScheduleStore.ListSchedules(ctx, agentID)
		if err == nil {
			export.Schedules = scheds
		}
	}

	// Secrets: decrypt with master key, re-encrypt with passphrase key.
	if deps.Vault != nil {
		scope := "agent:" + agentID
		metas, err := deps.Vault.List(ctx, scope)
		if err == nil {
			for _, meta := range metas {
				plaintext, err := deps.Vault.Get(ctx, scope, meta.Key)
				if err != nil {
					continue
				}
				ciphertext, err := secrets.Encrypt(passphraseKey, []byte(plaintext))
				if err != nil {
					continue
				}
				export.Secrets = append(export.Secrets, EncryptedSecret{
					Scope:       meta.Scope,
					Key:         meta.Key,
					Description: meta.Description,
					Ciphertext:  ciphertext,
				})
			}
		}
	}

	// Write export JSON.
	exportDir, err := os.MkdirTemp(tmpDir, "kyvik-export-*")
	if err != nil {
		return "", fmt.Errorf("create export dir: %w", err)
	}

	exportData, err := json.MarshalIndent(export, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal export: %w", err)
	}

	exportJSONPath := filepath.Join(exportDir, "agent_export.json")
	if err := os.WriteFile(exportJSONPath, exportData, 0o644); err != nil {
		return "", fmt.Errorf("write export json: %w", err)
	}

	// Create tar.gz archive.
	safeName := sanitizeFilename(agent.Name)
	archiveName := fmt.Sprintf("kyvik-agent-%s-%s.tar.gz", safeName, time.Now().Format("20060102-150405"))
	archivePath := filepath.Join(tmpDir, archiveName)
	if err := createTarGz(archivePath, exportDir); err != nil {
		return "", fmt.Errorf("create archive: %w", err)
	}

	// Clean up export dir.
	os.RemoveAll(exportDir)

	return archivePath, nil
}

// deriveKey uses scrypt to derive a 32-byte AES key from a passphrase and salt.
func deriveKey(passphrase string, salt []byte) ([]byte, error) {
	return scrypt.Key([]byte(passphrase), salt, 32768, 8, 1, 32)
}

// sanitizeFilename returns a safe version of a name for use in filenames.
func sanitizeFilename(name string) string {
	safe := make([]byte, 0, len(name))
	for _, c := range []byte(name) {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			safe = append(safe, c)
		} else if c == ' ' {
			safe = append(safe, '_')
		}
	}
	if len(safe) == 0 {
		return "agent"
	}
	if len(safe) > 50 {
		safe = safe[:50]
	}
	return string(safe)
}

func boolPtr(b bool) *bool { return &b }
