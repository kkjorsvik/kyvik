// Package apikeys manages API key lifecycle for the REST API.
package apikeys

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"
	"golang.org/x/crypto/bcrypt"

	"github.com/kkjorsvik/kyvik/internal/auth"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// Store is the minimal persistence contract for API keys.
type Store interface {
	CreateAPIKey(ctx context.Context, key types.APIKey) error
	GetAPIKey(ctx context.Context, id string) (*types.APIKey, error)
	GetAPIKeyByPrefix(ctx context.Context, prefix string) (*types.APIKey, error)
	ListAPIKeys(ctx context.Context) ([]types.APIKey, error)
	DeleteAPIKey(ctx context.Context, id string) error
	UpdateAPIKeyLastUsed(ctx context.Context, id string, at time.Time) error
}

// Service manages API key creation, validation, and revocation.
type Service struct {
	store Store
}

// CreateResult is returned on key creation and includes the plaintext key
// (shown once, never stored).
type CreateResult struct {
	Key      types.APIKey
	PlainKey string
}

// New creates an API key service.
func New(store Store) *Service {
	return &Service{store: store}
}

// keyPrefix returns the first 11 chars of a key (e.g. "kv_" + 8 hex).
const keyPrefixLen = 11

// Create generates a new API key with the given parameters.
func (s *Service) Create(ctx context.Context, name, scope string, agentIDs []string, expiresAt *time.Time) (*CreateResult, error) {
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if !auth.IsDefaultRole(scope) {
		return nil, fmt.Errorf("invalid scope %q: must be one of viewer, operator, manager, admin", scope)
	}
	if agentIDs == nil {
		agentIDs = []string{}
	}

	// Generate 32 random bytes → "kv_" + 64 hex chars.
	rawKey := make([]byte, 32)
	if _, err := rand.Read(rawKey); err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	plainKey := "kv_" + hex.EncodeToString(rawKey)
	prefix := plainKey[:keyPrefixLen]

	// Bcrypt hash for storage.
	hash, err := bcrypt.GenerateFromPassword([]byte(plainKey), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash key: %w", err)
	}

	now := time.Now().UTC()
	key := types.APIKey{
		ID:        ulid.Make().String(),
		Name:      name,
		KeyHash:   string(hash),
		KeyPrefix: prefix,
		Scope:     scope,
		AgentIDs:  agentIDs,
		IsActive:  true,
		ExpiresAt: expiresAt,
		CreatedAt: now,
	}

	if err := s.store.CreateAPIKey(ctx, key); err != nil {
		return nil, fmt.Errorf("store key: %w", err)
	}

	return &CreateResult{
		Key:      key,
		PlainKey: plainKey,
	}, nil
}

// Validate checks a plaintext API key and returns the associated key record.
// It verifies: prefix lookup → bcrypt → active → not expired.
func (s *Service) Validate(ctx context.Context, plainKey string) (*types.APIKey, error) {
	if len(plainKey) < keyPrefixLen {
		return nil, types.ErrAPIKeyInvalid
	}
	prefix := plainKey[:keyPrefixLen]

	key, err := s.store.GetAPIKeyByPrefix(ctx, prefix)
	if err != nil {
		return nil, types.ErrAPIKeyInvalid
	}

	if bcrypt.CompareHashAndPassword([]byte(key.KeyHash), []byte(plainKey)) != nil {
		return nil, types.ErrAPIKeyInvalid
	}

	if !key.IsActive {
		return nil, types.ErrAPIKeyInactive
	}

	if key.ExpiresAt != nil && !key.ExpiresAt.After(time.Now().UTC()) {
		return nil, types.ErrAPIKeyInactive
	}

	// Update last_used_at asynchronously.
	go func() {
		_ = s.store.UpdateAPIKeyLastUsed(context.Background(), key.ID, time.Now().UTC())
	}()

	return key, nil
}

// List returns all API keys (without hashes).
func (s *Service) List(ctx context.Context) ([]types.APIKey, error) {
	return s.store.ListAPIKeys(ctx)
}

// Revoke deletes an API key by ID.
func (s *Service) Revoke(ctx context.Context, id string) error {
	return s.store.DeleteAPIKey(ctx, id)
}

// CanAccessAgent checks whether the key is allowed to access the given agent.
// An empty AgentIDs list means access to all agents.
func CanAccessAgent(key *types.APIKey, agentID string) bool {
	if len(key.AgentIDs) == 0 {
		return true
	}
	for _, id := range key.AgentIDs {
		if id == agentID {
			return true
		}
	}
	return false
}
