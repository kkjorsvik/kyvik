package secrets

import (
	"context"
	"time"
)

// SecretMeta contains metadata about a secret without its decrypted value.
type SecretMeta struct {
	ID          string
	Scope       string
	Key         string
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// SecretStore defines the interface for encrypted secret storage.
// Scope values are: "global", "agent:<id>", "team:<id>".
type SecretStore interface {
	// Set stores or updates an encrypted secret.
	Set(ctx context.Context, scope, key, plaintext, description string) error

	// Get retrieves and decrypts a secret by scope and key.
	Get(ctx context.Context, scope, key string) (string, error)

	// Resolve looks up a key using cascading scope resolution:
	// agent:<agentID> -> team:<teamID> -> global.
	Resolve(ctx context.Context, agentID, teamID, key string) (string, error)

	// Delete removes a secret by scope and key.
	Delete(ctx context.Context, scope, key string) error

	// List returns metadata for all secrets in a scope.
	List(ctx context.Context, scope string) ([]SecretMeta, error)

	// Exists checks whether a secret exists for the given scope and key.
	Exists(ctx context.Context, scope, key string) (bool, error)
}
