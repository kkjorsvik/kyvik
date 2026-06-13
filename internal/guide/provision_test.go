package guide

import (
	"context"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/config"
	"github.com/kkjorsvik/kyvik/internal/store/postgres"
	"github.com/kkjorsvik/kyvik/internal/testutil"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

func newTestStore(t *testing.T) *postgres.PostgresStore {
	t.Helper()
	tdb := testutil.RequirePostgres(t)
	return tdb.Store
}

func defaultGuideConfig() config.GuideConfig {
	enabled := true
	return config.GuideConfig{
		Enabled: &enabled,
		Mode:    "basic",
		SpendingLimits: types.SpendingLimits{
			MaxSpendPerDay:   1.00,
			MaxSpendPerMonth: 30.00,
		},
	}
}

func TestEnsureGuideAgent_CreatesOnFirstRun(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	created, err := EnsureGuideAgent(ctx, ProvisionDeps{
		Store:        store,
		DefaultModel: types.ModelConfig{Provider: "openai", Model: "gpt-4o-mini"},
		GuideConfig:  defaultGuideConfig(),
	})
	if err != nil {
		t.Fatalf("EnsureGuideAgent: %v", err)
	}
	if !created {
		t.Fatal("expected created=true on first run")
	}

	agent, err := store.GetAgent(ctx, GuideAgentID)
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if agent.ID != GuideAgentID {
		t.Errorf("expected ID=%q, got %q", GuideAgentID, agent.ID)
	}
	if agent.Name != GuideAgentName {
		t.Errorf("expected Name=%q, got %q", GuideAgentName, agent.Name)
	}
	if !agent.IsGuide {
		t.Error("expected IsGuide=true")
	}
	if agent.Template != "guide" {
		t.Errorf("expected Template=guide, got %q", agent.Template)
	}
	if agent.DesiredState != types.DesiredStateStopped {
		t.Errorf("expected DesiredState=stopped, got %q", agent.DesiredState)
	}
	if agent.ModelConfig.Provider != "openai" {
		t.Errorf("expected Provider=openai, got %q", agent.ModelConfig.Provider)
	}
	if !agent.WebUIEnabled {
		t.Error("expected WebUIEnabled=true")
	}
	if !agent.AutoExtractMemories {
		t.Error("expected AutoExtractMemories=true")
	}

	// Verify first_run state was set.
	state, err := store.GetSystemState(ctx, "guide_first_run")
	if err != nil {
		t.Fatalf("GetSystemState: %v", err)
	}
	if state != "pending" {
		t.Errorf("expected guide_first_run=pending, got %q", state)
	}
}

func TestEnsureGuideAgent_Idempotent(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	deps := ProvisionDeps{
		Store:        store,
		DefaultModel: types.ModelConfig{Provider: "openai", Model: "gpt-4o-mini"},
		GuideConfig:  defaultGuideConfig(),
	}

	created1, err := EnsureGuideAgent(ctx, deps)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if !created1 {
		t.Fatal("expected first call to create")
	}

	created2, err := EnsureGuideAgent(ctx, deps)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if created2 {
		t.Fatal("expected second call to return created=false")
	}
}

func TestEnsureGuideAgent_ToolGrants(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	created, err := EnsureGuideAgent(ctx, ProvisionDeps{
		Store:        store,
		DefaultModel: types.ModelConfig{Provider: "openai", Model: "gpt-4o-mini"},
		GuideConfig:  defaultGuideConfig(),
	})
	if err != nil {
		t.Fatalf("EnsureGuideAgent: %v", err)
	}
	if !created {
		t.Fatal("expected created=true")
	}

	agent, err := store.GetAgent(ctx, GuideAgentID)
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}

	// Guide should always get the canonical KTP tool grants.
	expected := map[string]bool{"system_status": true, "memory": true, "my_spending": true}
	if len(agent.ToolGrants) != len(expected) {
		t.Fatalf("expected %d tool grants, got %d: %v", len(expected), len(agent.ToolGrants), agent.ToolGrants)
	}
	for _, g := range agent.ToolGrants {
		if !expected[g] {
			t.Errorf("unexpected tool grant %q", g)
		}
	}
}

func TestEnsureGuideAgent_PatchesStaleToolGrants(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Create guide with old phantom tool grants directly.
	now := time.Now().UTC()
	staleAgent := types.AgentConfig{
		ID:          GuideAgentID,
		Name:        GuideAgentName,
		Template:    "guide",
		ToolGrants:  []string{"system_docs", "system_logs"},
		IsGuide:     true,
		ModelConfig: types.ModelConfig{Provider: "openai", Model: "gpt-4o-mini"},
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := store.CreateAgent(ctx, staleAgent); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	// EnsureGuideAgent should detect stale grants and patch them.
	created, err := EnsureGuideAgent(ctx, ProvisionDeps{
		Store:        store,
		DefaultModel: types.ModelConfig{Provider: "openai", Model: "gpt-4o-mini"},
		GuideConfig:  defaultGuideConfig(),
	})
	if err != nil {
		t.Fatalf("EnsureGuideAgent: %v", err)
	}
	if created {
		t.Fatal("expected created=false for existing agent")
	}

	agent, err := store.GetAgent(ctx, GuideAgentID)
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}

	expected := map[string]bool{"system_status": true, "memory": true, "my_spending": true}
	if len(agent.ToolGrants) != len(expected) {
		t.Fatalf("expected %d tool grants after patch, got %d: %v", len(expected), len(agent.ToolGrants), agent.ToolGrants)
	}
	for _, g := range agent.ToolGrants {
		if !expected[g] {
			t.Errorf("unexpected tool grant %q after patch", g)
		}
	}
}

func TestEnsureGuideAgent_PatchesNilToolGrants(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Simulate the UI form bug (ed0b9c9) that wiped tool grants to nil.
	now := time.Now().UTC()
	brokenAgent := types.AgentConfig{
		ID:          GuideAgentID,
		Name:        GuideAgentName,
		Template:    "guide",
		ToolGrants:  nil, // wiped by form bug
		IsGuide:     true,
		ModelConfig: types.ModelConfig{Provider: "openai", Model: "gpt-4o-mini"},
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := store.CreateAgent(ctx, brokenAgent); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	// EnsureGuideAgent should detect nil grants and patch them.
	created, err := EnsureGuideAgent(ctx, ProvisionDeps{
		Store:        store,
		DefaultModel: types.ModelConfig{Provider: "openai", Model: "gpt-4o-mini"},
		GuideConfig:  defaultGuideConfig(),
	})
	if err != nil {
		t.Fatalf("EnsureGuideAgent: %v", err)
	}
	if created {
		t.Fatal("expected created=false for existing agent")
	}

	agent, err := store.GetAgent(ctx, GuideAgentID)
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}

	expected := map[string]bool{"system_status": true, "memory": true, "my_spending": true}
	if len(agent.ToolGrants) != len(expected) {
		t.Fatalf("expected %d tool grants after patch, got %d: %v", len(expected), len(agent.ToolGrants), agent.ToolGrants)
	}
	for _, g := range agent.ToolGrants {
		if !expected[g] {
			t.Errorf("unexpected tool grant %q after patch", g)
		}
	}
}

func TestIsGuideAgent(t *testing.T) {
	if !IsGuideAgent("kyvik-guide") {
		t.Error("expected kyvik-guide to be guide agent")
	}
	if IsGuideAgent("other-agent") {
		t.Error("expected other-agent to not be guide agent")
	}
	if IsGuideAgent("") {
		t.Error("expected empty string to not be guide agent")
	}
}

// mockSender records SendMessage calls for testing.
type mockSender struct {
	calls []types.Message
}

func (m *mockSender) SendMessage(_ context.Context, _ string, msg types.Message) error {
	m.calls = append(m.calls, msg)
	return nil
}

func TestSendWelcomeMessage_OnlyOnce(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	sender := &mockSender{}

	// First call should send.
	if err := SendWelcomeMessage(ctx, store, sender, true); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if len(sender.calls) != 1 {
		t.Fatalf("expected 1 message, got %d", len(sender.calls))
	}

	// Second call should be a no-op.
	if err := SendWelcomeMessage(ctx, store, sender, true); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if len(sender.calls) != 1 {
		t.Fatalf("expected still 1 message after idempotent call, got %d", len(sender.calls))
	}
}

func TestSendWelcomeMessage_NoModelSkipsMessage(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	sender := &mockSender{}

	if err := SendWelcomeMessage(ctx, store, sender, false); err != nil {
		t.Fatalf("SendWelcomeMessage: %v", err)
	}
	if len(sender.calls) != 0 {
		t.Errorf("expected no messages when hasModel=false, got %d", len(sender.calls))
	}

	// Verify state was still set.
	state, err := store.GetSystemState(ctx, "guide_welcome_sent")
	if err != nil {
		t.Fatalf("GetSystemState: %v", err)
	}
	if state != "true" {
		t.Errorf("expected guide_welcome_sent=true, got %q", state)
	}
}
