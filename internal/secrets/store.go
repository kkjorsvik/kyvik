package secrets

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/kkjorsvik/kyvik/internal/audit"
	"github.com/kkjorsvik/kyvik/internal/sqlutil"
	"github.com/kkjorsvik/kyvik/internal/timeutil"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// Vault implements SecretStore with AES-256-GCM encryption backed by SQL storage.
type Vault struct {
	db    *sql.DB
	key   []byte
	audit audit.Logger
}

// NewVault creates a new encrypted secrets vault.
func NewVault(db *sql.DB, masterKey []byte, auditLogger audit.Logger) *Vault {
	return &Vault{
		db:    db,
		key:   masterKey,
		audit: auditLogger,
	}
}

func (v *Vault) Set(ctx context.Context, scope, key, plaintext, description string) error {
	encrypted, err := Encrypt(v.key, []byte(plaintext))
	if err != nil {
		return fmt.Errorf("encrypt secret: %w", err)
	}

	nowUTC := timeutil.NowUTC()
	id := ulid.MustNew(ulid.Timestamp(nowUTC), rand.Reader).String()

	updatedAt := nowUTC.UTC().Format(dbTimeFmt)

	// Update-first strategy avoids relying on ON CONFLICT(scope,key), which may be
	// missing on some migrated PostgreSQL schemas.
	result, err := sqlutil.ExecContext(ctx, v.db,
		`UPDATE secrets
		 SET encrypted_value = ?, description = ?, updated_at = ?
		 WHERE scope = ? AND key = ?`,
		encrypted, description, updatedAt, scope, key,
	)
	if err != nil {
		return fmt.Errorf("update secret: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if rows == 0 {
		if _, err := sqlutil.ExecContext(ctx, v.db,
			`INSERT INTO secrets (id, scope, key, encrypted_value, description, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			id, scope, key, encrypted, description, updatedAt,
		); err != nil {
			return fmt.Errorf("insert secret: %w", err)
		}
	}

	_ = audit.LogSecret(ctx, v.audit, "secret.created", scope, key, "")
	return nil
}

func (v *Vault) Get(ctx context.Context, scope, key string) (string, error) {
	var encrypted []byte
	err := sqlutil.QueryRowContext(ctx, v.db,
		`SELECT encrypted_value FROM secrets WHERE scope = ? AND key = ?`,
		scope, key,
	).Scan(&encrypted)
	if errors.Is(err, sql.ErrNoRows) {
		return "", types.ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("query secret: %w", err)
	}

	plaintext, err := Decrypt(v.key, encrypted)
	if err != nil {
		return "", err
	}

	_ = audit.LogSecret(ctx, v.audit, "secret.accessed", scope, key, "")
	return string(plaintext), nil
}

func (v *Vault) Resolve(ctx context.Context, agentID, teamID, key string) (string, error) {
	// Try agent scope first
	if val, err := v.Get(ctx, "agent:"+agentID, key); err == nil {
		return val, nil
	}

	// Try team scope
	if teamID != "" {
		if val, err := v.Get(ctx, "team:"+teamID, key); err == nil {
			return val, nil
		}
	}

	// Try global scope
	if val, err := v.Get(ctx, "global", key); err == nil {
		return val, nil
	}

	_ = audit.LogSecret(ctx, v.audit, "secret.resolve.miss", "agent:"+agentID, key, "")
	return "", types.ErrNotFound
}

func (v *Vault) Delete(ctx context.Context, scope, key string) error {
	result, err := sqlutil.ExecContext(ctx, v.db,
		`DELETE FROM secrets WHERE scope = ? AND key = ?`,
		scope, key,
	)
	if err != nil {
		return fmt.Errorf("delete secret: %w", err)
	}

	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return types.ErrNotFound
	}

	_ = audit.LogSecret(ctx, v.audit, "secret.deleted", scope, key, "")
	return nil
}

func (v *Vault) List(ctx context.Context, scope string) ([]SecretMeta, error) {
	rows, err := sqlutil.QueryContext(ctx, v.db,
		`SELECT id, scope, key, description, created_at, updated_at
		 FROM secrets WHERE scope = ? ORDER BY key`,
		scope,
	)
	if err != nil {
		return nil, fmt.Errorf("query secrets: %w", err)
	}
	defer rows.Close()

	var secrets []SecretMeta
	for rows.Next() {
		var s SecretMeta
		var createdAt, updatedAt any
		if err := rows.Scan(&s.ID, &s.Scope, &s.Key, &s.Description, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan secret: %w", err)
		}
		if s.CreatedAt, err = parseDBTime(createdAt); err != nil {
			return nil, fmt.Errorf("parse created_at: %w", err)
		}
		if s.UpdatedAt, err = parseDBTime(updatedAt); err != nil {
			return nil, fmt.Errorf("parse updated_at: %w", err)
		}
		secrets = append(secrets, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate secrets: %w", err)
	}
	return secrets, nil
}

func (v *Vault) Exists(ctx context.Context, scope, key string) (bool, error) {
	var exists bool
	err := sqlutil.QueryRowContext(ctx, v.db,
		`SELECT EXISTS(SELECT 1 FROM secrets WHERE scope = ? AND key = ?)`,
		scope, key,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check secret exists: %w", err)
	}
	return exists, nil
}

// dbTimeFmt matches the database CURRENT_TIMESTAMP format.
const dbTimeFmt = "2006-01-02 15:04:05"

// parseTime tries RFC3339 first, then falls back to the database CURRENT_TIMESTAMP format.
func parseTime(s string) (time.Time, error) {
	return timeutil.ParseTimestampUTC(s)
}

func parseDBTime(v any) (time.Time, error) {
	switch t := v.(type) {
	case time.Time:
		return t.UTC(), nil
	case string:
		return parseTime(t)
	case []byte:
		return parseTime(string(t))
	default:
		return time.Time{}, fmt.Errorf("unsupported timestamp type %T", v)
	}
}
