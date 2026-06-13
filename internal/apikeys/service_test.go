package apikeys_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/apikeys"
	"github.com/kkjorsvik/kyvik/internal/testutil"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

func newTestService(t *testing.T) *apikeys.Service {
	t.Helper()
	tdb := testutil.RequirePostgres(t)
	return apikeys.New(tdb.Store)
}

func TestCreateAndValidate(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	result, err := svc.Create(ctx, "test-key", "viewer", nil, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Key format: kv_ + 64 hex chars = 67 total.
	if !strings.HasPrefix(result.PlainKey, "kv_") {
		t.Errorf("key should start with kv_, got %q", result.PlainKey[:5])
	}
	if len(result.PlainKey) != 67 {
		t.Errorf("key length should be 67, got %d", len(result.PlainKey))
	}

	// Prefix = first 11 chars.
	if result.Key.KeyPrefix != result.PlainKey[:11] {
		t.Errorf("prefix mismatch: %q vs %q", result.Key.KeyPrefix, result.PlainKey[:11])
	}

	// Validate with correct key.
	key, err := svc.Validate(ctx, result.PlainKey)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if key.ID != result.Key.ID {
		t.Errorf("validated key ID mismatch: %q vs %q", key.ID, result.Key.ID)
	}

	// Validate with wrong key.
	_, err = svc.Validate(ctx, "kv_0000000000000000000000000000000000000000000000000000000000000000")
	if err != types.ErrAPIKeyInvalid {
		t.Errorf("expected ErrAPIKeyInvalid, got %v", err)
	}
}

func TestInvalidScope(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	_, err := svc.Create(ctx, "bad-scope", "superadmin", nil, nil)
	if err == nil {
		t.Fatal("expected error for invalid scope")
	}
}

func TestExpiredKey(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	past := time.Now().Add(-time.Hour)
	result, err := svc.Create(ctx, "expired-key", "viewer", nil, &past)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	_, err = svc.Validate(ctx, result.PlainKey)
	if err != types.ErrAPIKeyInactive {
		t.Errorf("expected ErrAPIKeyInactive for expired key, got %v", err)
	}
}

func TestRevokeKey(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	result, err := svc.Create(ctx, "revoke-test", "operator", nil, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := svc.Revoke(ctx, result.Key.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	_, err = svc.Validate(ctx, result.PlainKey)
	if err != types.ErrAPIKeyInvalid {
		t.Errorf("expected ErrAPIKeyInvalid after revoke, got %v", err)
	}
}

func TestListKeys(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := svc.Create(ctx, "key-"+string(rune('A'+i)), "viewer", nil, nil); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}

	keys, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(keys) != 3 {
		t.Errorf("expected 3 keys, got %d", len(keys))
	}
}

func TestAgentScoping(t *testing.T) {
	key := &types.APIKey{AgentIDs: []string{"agent-1", "agent-2"}}
	if !apikeys.CanAccessAgent(key, "agent-1") {
		t.Error("should allow agent-1")
	}
	if apikeys.CanAccessAgent(key, "agent-3") {
		t.Error("should deny agent-3")
	}

	// Empty agent IDs = all agents.
	allKey := &types.APIKey{AgentIDs: []string{}}
	if !apikeys.CanAccessAgent(allKey, "any-agent") {
		t.Error("empty agent_ids should allow all agents")
	}
}

func TestEmptyName(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	_, err := svc.Create(ctx, "", "viewer", nil, nil)
	if err == nil {
		t.Fatal("expected error for empty name")
	}
}
