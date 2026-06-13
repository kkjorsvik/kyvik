package secrets

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/audit"
	"github.com/kkjorsvik/kyvik/internal/testutil"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// mockAuditLogger satisfies audit.Logger for tests.
type mockAuditLogger struct{}

func (m *mockAuditLogger) Log(_ context.Context, _ types.AuditEntry) error { return nil }
func (m *mockAuditLogger) Query(_ context.Context, _ audit.Filter) ([]types.AuditEntry, error) {
	return nil, nil
}
func (m *mockAuditLogger) Stream(_ context.Context, _ string) (<-chan types.AuditEntry, error) {
	return nil, nil
}
func (m *mockAuditLogger) Subscribe(_ context.Context, _ audit.SubscriptionFilter) (<-chan types.AuditEntry, error) {
	ch := make(chan types.AuditEntry)
	close(ch)
	return ch, nil
}
func (m *mockAuditLogger) Close() error { return nil }

func newTestVault(t *testing.T) *Vault {
	t.Helper()
	tdb := testutil.RequirePostgres(t)

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}

	return NewVault(tdb.DB, key, &mockAuditLogger{})
}

func TestSetAndGet(t *testing.T) {
	v := newTestVault(t)
	ctx := context.Background()

	if err := v.Set(ctx, "global", "API_KEY", "sk-test-123", "Test key"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	val, err := v.Get(ctx, "global", "API_KEY")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if val != "sk-test-123" {
		t.Fatalf("got %q, want %q", val, "sk-test-123")
	}
}

func TestSetUpsert(t *testing.T) {
	v := newTestVault(t)
	ctx := context.Background()

	if err := v.Set(ctx, "global", "KEY", "value1", "desc1"); err != nil {
		t.Fatalf("Set 1: %v", err)
	}
	if err := v.Set(ctx, "global", "KEY", "value2", "desc2"); err != nil {
		t.Fatalf("Set 2: %v", err)
	}

	val, err := v.Get(ctx, "global", "KEY")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if val != "value2" {
		t.Fatalf("got %q, want %q", val, "value2")
	}
}

func TestGetNotFound(t *testing.T) {
	v := newTestVault(t)
	ctx := context.Background()

	_, err := v.Get(ctx, "global", "NONEXISTENT")
	if !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestDelete(t *testing.T) {
	v := newTestVault(t)
	ctx := context.Background()

	if err := v.Set(ctx, "global", "KEY", "value", ""); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := v.Delete(ctx, "global", "KEY"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err := v.Get(ctx, "global", "KEY")
	if !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestDeleteNotFound(t *testing.T) {
	v := newTestVault(t)
	ctx := context.Background()

	err := v.Delete(ctx, "global", "NONEXISTENT")
	if !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestList(t *testing.T) {
	v := newTestVault(t)
	ctx := context.Background()

	if err := v.Set(ctx, "global", "A_KEY", "val1", "first"); err != nil {
		t.Fatalf("Set A: %v", err)
	}
	if err := v.Set(ctx, "global", "B_KEY", "val2", "second"); err != nil {
		t.Fatalf("Set B: %v", err)
	}
	if err := v.Set(ctx, "agent:x", "C_KEY", "val3", "other scope"); err != nil {
		t.Fatalf("Set C: %v", err)
	}

	list, err := v.List(ctx, "global")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 secrets, got %d", len(list))
	}
	if list[0].Key != "A_KEY" || list[1].Key != "B_KEY" {
		t.Fatalf("unexpected order: %s, %s", list[0].Key, list[1].Key)
	}
	if list[0].Description != "first" {
		t.Fatalf("unexpected description: %q", list[0].Description)
	}
}

func TestExists(t *testing.T) {
	v := newTestVault(t)
	ctx := context.Background()

	exists, err := v.Exists(ctx, "global", "KEY")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if exists {
		t.Fatal("expected false before Set")
	}

	if err := v.Set(ctx, "global", "KEY", "val", ""); err != nil {
		t.Fatalf("Set: %v", err)
	}

	exists, err = v.Exists(ctx, "global", "KEY")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !exists {
		t.Fatal("expected true after Set")
	}
}

func TestResolveAgentFirst(t *testing.T) {
	v := newTestVault(t)
	ctx := context.Background()

	if err := v.Set(ctx, "global", "TOKEN", "global-val", ""); err != nil {
		t.Fatal(err)
	}
	if err := v.Set(ctx, "agent:a1", "TOKEN", "agent-val", ""); err != nil {
		t.Fatal(err)
	}

	val, err := v.Resolve(ctx, "a1", "", "TOKEN")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if val != "agent-val" {
		t.Fatalf("expected agent scope to win, got %q", val)
	}
}

func TestResolveTeamFallback(t *testing.T) {
	v := newTestVault(t)
	ctx := context.Background()

	if err := v.Set(ctx, "global", "TOKEN", "global-val", ""); err != nil {
		t.Fatal(err)
	}
	if err := v.Set(ctx, "team:t1", "TOKEN", "team-val", ""); err != nil {
		t.Fatal(err)
	}

	val, err := v.Resolve(ctx, "a1", "t1", "TOKEN")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if val != "team-val" {
		t.Fatalf("expected team scope fallback, got %q", val)
	}
}

func TestResolveGlobalFallback(t *testing.T) {
	v := newTestVault(t)
	ctx := context.Background()

	if err := v.Set(ctx, "global", "TOKEN", "global-val", ""); err != nil {
		t.Fatal(err)
	}

	val, err := v.Resolve(ctx, "a1", "t1", "TOKEN")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if val != "global-val" {
		t.Fatalf("expected global fallback, got %q", val)
	}
}

func TestResolveMiss(t *testing.T) {
	v := newTestVault(t)
	ctx := context.Background()

	_, err := v.Resolve(ctx, "a1", "t1", "NONEXISTENT")
	if !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestParseDBTime(t *testing.T) {
	now := time.Now().UTC().Round(time.Second)
	tests := []struct {
		name    string
		input   any
		wantErr bool
	}{
		{name: "time.Time", input: now},
		{name: "sqlite string", input: now.Format("2006-01-02 15:04:05")},
		{name: "rfc3339 bytes", input: []byte(now.Format(time.RFC3339))},
		{name: "unsupported", input: 123, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseDBTime(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseDBTime: %v", err)
			}
			if !got.Equal(now) {
				t.Fatalf("got %s, want %s", got, now)
			}
		})
	}
}
