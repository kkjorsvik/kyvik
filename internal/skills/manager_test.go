package skills

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/kkjorsvik/kyvik/internal/testutil"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// newTestManager creates a Manager backed by a temp skill dir and a PostgreSQL store.
func newTestManager(t *testing.T, base string) *Manager {
	t.Helper()
	loader, err := NewLoader(base)
	if err != nil {
		t.Fatal(err)
	}
	tdb := testutil.RequirePostgres(t)

	mgr, err := NewManager(loader, tdb.Store)
	if err != nil {
		t.Fatal(err)
	}
	return mgr
}

const promptSkillManifest = `name: greeter
version: "1.0.0"
description: A greeting skill
author: tester
`

const fsWriteSkillManifest = `name: file-writer
version: "1.0.0"
description: Writes files
author: tester
required_capabilities:
  - tool: filesystem
    action: write
    resource: "*"
`

const httpSkillManifest = `name: http-reader
version: "1.0.0"
description: Reads HTTP
author: tester
required_capabilities:
  - tool: http
    action: get
    resource: "*"
`

func readerAgent() types.AgentConfig {
	return types.AgentConfig{ID: "agent-reader", Template: "reader"}
}

func adminAgent() types.AgentConfig {
	return types.AgentConfig{ID: "agent-admin", Template: "admin"}
}

func TestGrant(t *testing.T) {
	base := t.TempDir()
	setupSkillDir(t, filepath.Join(base, "local"), "greeter",
		withManifest(promptSkillManifest),
	)

	mgr := newTestManager(t, base)
	ctx := context.Background()
	agent := adminAgent()

	err := mgr.Grant(ctx, agent.ID, "greeter", "admin", agent)
	if err != nil {
		t.Fatalf("Grant failed: %v", err)
	}

	grants, err := mgr.ListGrants(ctx, agent.ID)
	if err != nil {
		t.Fatalf("ListGrants failed: %v", err)
	}
	if len(grants) != 1 {
		t.Fatalf("expected 1 grant, got %d", len(grants))
	}
	if grants[0].Grant.SkillName != "greeter" {
		t.Errorf("skill name = %q, want %q", grants[0].Grant.SkillName, "greeter")
	}
	if grants[0].Skill == nil {
		t.Error("expected Skill to be non-nil")
	}
}

func TestGrantAlreadyGranted(t *testing.T) {
	base := t.TempDir()
	setupSkillDir(t, filepath.Join(base, "local"), "greeter",
		withManifest(promptSkillManifest),
	)

	mgr := newTestManager(t, base)
	ctx := context.Background()
	agent := adminAgent()

	if err := mgr.Grant(ctx, agent.ID, "greeter", "admin", agent); err != nil {
		t.Fatal(err)
	}

	err := mgr.Grant(ctx, agent.ID, "greeter", "admin", agent)
	if !errors.Is(err, types.ErrSkillAlreadyGranted) {
		t.Fatalf("expected ErrSkillAlreadyGranted, got %v", err)
	}
}

func TestGrantNonexistent(t *testing.T) {
	base := t.TempDir()
	mgr := newTestManager(t, base)
	ctx := context.Background()
	agent := adminAgent()

	err := mgr.Grant(ctx, agent.ID, "no-such-skill", "admin", agent)
	if !errors.Is(err, types.ErrSkillNotFound) {
		t.Fatalf("expected ErrSkillNotFound, got %v", err)
	}
}

func TestGrantUnmetRequirements(t *testing.T) {
	base := t.TempDir()
	setupSkillDir(t, filepath.Join(base, "local"), "file-writer",
		withManifest(fsWriteSkillManifest),
	)

	mgr := newTestManager(t, base)
	ctx := context.Background()
	agent := readerAgent() // reader cannot write filesystem

	err := mgr.Grant(ctx, agent.ID, "file-writer", "admin", agent)
	if !errors.Is(err, types.ErrSkillRequirements) {
		t.Fatalf("expected ErrSkillRequirements, got %v", err)
	}
}

func TestRevoke(t *testing.T) {
	base := t.TempDir()
	setupSkillDir(t, filepath.Join(base, "local"), "greeter",
		withManifest(promptSkillManifest),
	)

	mgr := newTestManager(t, base)
	ctx := context.Background()
	agent := adminAgent()

	if err := mgr.Grant(ctx, agent.ID, "greeter", "admin", agent); err != nil {
		t.Fatal(err)
	}

	if err := mgr.Revoke(ctx, agent.ID, "greeter"); err != nil {
		t.Fatalf("Revoke failed: %v", err)
	}

	grants, err := mgr.ListGrants(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 0 {
		t.Errorf("expected 0 grants after revoke, got %d", len(grants))
	}
}

func TestRevokeNonGranted(t *testing.T) {
	base := t.TempDir()
	mgr := newTestManager(t, base)
	ctx := context.Background()

	err := mgr.Revoke(ctx, "agent-1", "non-granted-skill")
	if !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestPromptContentForAgent(t *testing.T) {
	base := t.TempDir()

	setupSkillDir(t, filepath.Join(base, "local"), "greeter",
		withManifest(promptSkillManifest),
		withPrompt("01-hello.md", "Say hello to the user."),
	)
	setupSkillDir(t, filepath.Join(base, "local"), "http-reader",
		withManifest(httpSkillManifest),
		withPrompt("01-http.md", "You can make HTTP GET requests."),
	)

	mgr := newTestManager(t, base)
	ctx := context.Background()
	agent := adminAgent()

	if err := mgr.Grant(ctx, agent.ID, "greeter", "admin", agent); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Grant(ctx, agent.ID, "http-reader", "admin", agent); err != nil {
		t.Fatal(err)
	}

	content, err := mgr.PromptContentForAgent(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Both prompts should be present (order depends on DB ordering).
	if content == "" {
		t.Fatal("expected non-empty prompt content")
	}
	if !containsAll(content, "Say hello to the user.", "You can make HTTP GET requests.") {
		t.Errorf("prompt content missing expected parts: %q", content)
	}
}

func TestPromptContentForAgentNoGrants(t *testing.T) {
	base := t.TempDir()
	mgr := newTestManager(t, base)
	ctx := context.Background()

	content, err := mgr.PromptContentForAgent(ctx, "agent-no-grants")
	if err != nil {
		t.Fatal(err)
	}
	if content != "" {
		t.Errorf("expected empty content, got %q", content)
	}
}

func TestAvailableForAgentFiltersByTemplate(t *testing.T) {
	base := t.TempDir()

	// Prompt-only skill — no required capabilities.
	setupSkillDir(t, filepath.Join(base, "local"), "greeter",
		withManifest(promptSkillManifest),
	)
	// filesystem/write skill — reader cannot use.
	setupSkillDir(t, filepath.Join(base, "local"), "file-writer",
		withManifest(fsWriteSkillManifest),
	)

	mgr := newTestManager(t, base)

	// Reader should see the prompt-only skill but not file-writer.
	reader := readerAgent()
	readerSkills := mgr.AvailableForAgent(reader)
	if len(readerSkills) != 1 {
		t.Fatalf("reader: expected 1 available skill, got %d", len(readerSkills))
	}
	if readerSkills[0].Name != "greeter" {
		t.Errorf("reader: expected greeter, got %q", readerSkills[0].Name)
	}

	// Admin should see both.
	admin := adminAgent()
	adminSkills := mgr.AvailableForAgent(admin)
	if len(adminSkills) != 2 {
		t.Fatalf("admin: expected 2 available skills, got %d", len(adminSkills))
	}
}

func TestCleanupAgent(t *testing.T) {
	base := t.TempDir()
	setupSkillDir(t, filepath.Join(base, "local"), "greeter",
		withManifest(promptSkillManifest),
	)

	mgr := newTestManager(t, base)
	ctx := context.Background()
	agent := adminAgent()

	if err := mgr.Grant(ctx, agent.ID, "greeter", "admin", agent); err != nil {
		t.Fatal(err)
	}

	if err := mgr.CleanupAgent(ctx, agent.ID); err != nil {
		t.Fatalf("CleanupAgent failed: %v", err)
	}

	grants, err := mgr.ListGrants(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 0 {
		t.Errorf("expected 0 grants after cleanup, got %d", len(grants))
	}
}

func TestRefresh(t *testing.T) {
	base := t.TempDir()

	// Start with one skill.
	setupSkillDir(t, filepath.Join(base, "local"), "greeter",
		withManifest(promptSkillManifest),
	)
	mgr := newTestManager(t, base)

	if len(mgr.Available()) != 1 {
		t.Fatalf("expected 1 skill initially, got %d", len(mgr.Available()))
	}

	// Add a second skill to disk.
	setupSkillDir(t, filepath.Join(base, "local"), "http-reader",
		withManifest(httpSkillManifest),
	)

	if err := mgr.Refresh(); err != nil {
		t.Fatal(err)
	}

	avail := mgr.Available()
	if len(avail) != 2 {
		t.Fatalf("expected 2 skills after refresh, got %d", len(avail))
	}

	// Verify the new skill is findable.
	if _, err := mgr.GetSkill("http-reader"); err != nil {
		t.Errorf("http-reader not found after refresh: %v", err)
	}
}

// containsAll checks that s contains all substrings.
func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !contains(s, sub) {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && searchString(s, sub)
}

func searchString(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
