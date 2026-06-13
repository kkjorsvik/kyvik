package ktp

import (
	"context"
	"errors"
	"testing"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

func TestCheck_WorkerTierAllowed(t *testing.T) {
	gate, _ := newTestGate(map[string]*types.AgentConfig{
		"a1": agentWithTemplate("a1", "worker"),
	})
	result, err := gate.Check(context.Background(), "a1", minimalToolDecl("fs", TierWriter), "do", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Allowed {
		t.Fatalf("expected allowed, got denied: %s", result.Reason)
	}
}

func TestCheck_TierDenied(t *testing.T) {
	gate, _ := newTestGate(map[string]*types.AgentConfig{
		"a1": agentWithTemplate("a1", "reader"),
	})
	result, err := gate.Check(context.Background(), "a1", minimalToolDecl("admin-tool", TierAdmin), "do", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Allowed {
		t.Fatal("expected denied, got allowed")
	}
	if result.Reason == "" {
		t.Fatal("expected a denial reason")
	}
}

func TestCheck_TierExceeds(t *testing.T) {
	gate, _ := newTestGate(map[string]*types.AgentConfig{
		"a1": agentWithTemplate("a1", "admin"),
	})
	result, err := gate.Check(context.Background(), "a1", minimalToolDecl("fs", TierWriter), "do", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Allowed {
		t.Fatalf("expected allowed, got denied: %s", result.Reason)
	}
}

func TestCheck_UnknownTemplateDenied(t *testing.T) {
	gate, _ := newTestGate(map[string]*types.AgentConfig{
		"a1": agentWithTemplate("a1", "custom-unknown"),
	})
	result, err := gate.Check(context.Background(), "a1", minimalToolDecl("fs", TierReader), "do", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Allowed {
		t.Fatal("expected denied for unknown template")
	}
}

func TestCheck_ActionNotFound(t *testing.T) {
	gate, _ := newTestGate(map[string]*types.AgentConfig{
		"a1": agentWithTemplate("a1", "admin"),
	})
	result, err := gate.Check(context.Background(), "a1", minimalToolDecl("fs", TierReader), "nonexistent", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Allowed {
		t.Fatal("expected denied for missing action")
	}
}

func TestCheck_ToolGrantsAllowed(t *testing.T) {
	agent := agentWithTemplate("a1", "writer")
	agent.ToolGrants = []string{"fs", "memory"}
	gate, _ := newTestGate(map[string]*types.AgentConfig{"a1": agent})

	result, err := gate.Check(context.Background(), "a1", minimalToolDecl("fs", TierWriter), "do", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Allowed {
		t.Fatalf("expected allowed, got denied: %s", result.Reason)
	}
}

func TestCheck_ToolGrantsDenied(t *testing.T) {
	agent := agentWithTemplate("a1", "writer")
	agent.ToolGrants = []string{"memory"}
	gate, _ := newTestGate(map[string]*types.AgentConfig{"a1": agent})

	result, err := gate.Check(context.Background(), "a1", minimalToolDecl("fs", TierWriter), "do", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Allowed {
		t.Fatal("expected denied, tool not in grants")
	}
}

func TestCheck_EmptyToolGrantsAllowsAll(t *testing.T) {
	agent := agentWithTemplate("a1", "writer")
	// ToolGrants is nil — no restriction
	gate, _ := newTestGate(map[string]*types.AgentConfig{"a1": agent})

	result, err := gate.Check(context.Background(), "a1", minimalToolDecl("any-tool", TierWriter), "do", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Allowed {
		t.Fatalf("expected allowed with empty grants, got denied: %s", result.Reason)
	}
}

func TestCheck_CapabilityAllowed(t *testing.T) {
	gate, _ := newTestGate(map[string]*types.AgentConfig{
		"a1": agentWithTemplate("a1", "writer"),
	})
	action := ActionSpec{
		Name:       "write",
		Parameters: JSONSchema{Type: "object"},
		RequiredCapabilities: []Capability{
			{Type: "filesystem", Access: "write", Resource: "{workspace}/file.txt"},
		},
	}
	decl := minimalToolDecl("fs", TierWriter, action)
	result, err := gate.Check(context.Background(), "a1", decl, "write", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Allowed {
		t.Fatalf("expected allowed, got denied: %s", result.Reason)
	}
}

func TestCheck_CapabilityDenied(t *testing.T) {
	gate, _ := newTestGate(map[string]*types.AgentConfig{
		"a1": agentWithTemplate("a1", "reader"),
	})
	action := ActionSpec{
		Name:       "write",
		Parameters: JSONSchema{Type: "object"},
		RequiredCapabilities: []Capability{
			{Type: "filesystem", Access: "write", Resource: "{workspace}/file.txt"},
		},
	}
	decl := minimalToolDecl("fs", TierReader, action)
	result, err := gate.Check(context.Background(), "a1", decl, "write", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Allowed {
		t.Fatal("expected denied, reader lacks filesystem/write")
	}
}

func TestCheck_CapabilityPathMismatch(t *testing.T) {
	agent := agentWithTemplate("a1", "reader")
	// Explicit grants scoped to data/*
	agent.CapabilityGrants = []types.Capability{
		{Tool: "filesystem", Action: "read", Resource: "data/*"},
	}
	gate, _ := newTestGate(map[string]*types.AgentConfig{"a1": agent})

	action := ActionSpec{
		Name:       "read",
		Parameters: JSONSchema{Type: "object"},
		RequiredCapabilities: []Capability{
			{Type: "filesystem", Access: "read", Resource: "etc/passwd"},
		},
	}
	decl := minimalToolDecl("fs", TierReader, action)
	result, err := gate.Check(context.Background(), "a1", decl, "read", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Allowed {
		t.Fatal("expected denied, data/* should not match etc/passwd")
	}
}

func TestCheck_ExplicitCapabilityGrants(t *testing.T) {
	agent := agentWithTemplate("a1", "reader")
	// Explicit grants override tier defaults — give reader write access.
	agent.CapabilityGrants = []types.Capability{
		{Tool: "filesystem", Action: "write", Resource: "{workspace}/*"},
	}
	gate, _ := newTestGate(map[string]*types.AgentConfig{"a1": agent})

	action := ActionSpec{
		Name:       "write",
		Parameters: JSONSchema{Type: "object"},
		RequiredCapabilities: []Capability{
			{Type: "filesystem", Access: "write", Resource: "{workspace}/file.txt"},
		},
	}
	decl := minimalToolDecl("fs", TierReader, action)
	result, err := gate.Check(context.Background(), "a1", decl, "write", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Allowed {
		t.Fatalf("expected allowed via explicit grants, got denied: %s", result.Reason)
	}
}

func TestCheck_NoRequiredCapabilities(t *testing.T) {
	gate, _ := newTestGate(map[string]*types.AgentConfig{
		"a1": agentWithTemplate("a1", "reader"),
	})
	// Action has no RequiredCapabilities — vacuously passes.
	action := ActionSpec{
		Name:       "info",
		Parameters: JSONSchema{Type: "object"},
	}
	decl := minimalToolDecl("tool", TierReader, action)
	result, err := gate.Check(context.Background(), "a1", decl, "info", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Allowed {
		t.Fatalf("expected allowed (no required caps), got denied: %s", result.Reason)
	}
}

func TestCheck_AdminAllowsAdminTierTools(t *testing.T) {
	gate, _ := newTestGate(map[string]*types.AgentConfig{
		"a1": agentWithTemplate("a1", "admin"),
	})
	action := ActionSpec{
		Name:       "configure",
		Parameters: JSONSchema{Type: "object"},
	}
	decl := minimalToolDecl("system", TierAdmin, action)
	result, err := gate.Check(context.Background(), "a1", decl, "configure", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Allowed {
		t.Fatalf("expected admin to allow admin-tier tools, got denied: %s", result.Reason)
	}
}

func TestCheck_DestructiveAction(t *testing.T) {
	gate, audit := newTestGate(map[string]*types.AgentConfig{
		"a1": agentWithTemplate("a1", "admin"),
	})
	action := ActionSpec{
		Name:        "delete",
		Parameters:  JSONSchema{Type: "object"},
		Destructive: true,
	}
	decl := minimalToolDecl("fs", TierWriter, action)
	result, err := gate.Check(context.Background(), "a1", decl, "delete", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Allowed {
		t.Fatalf("expected allowed (admin tier), got denied: %s", result.Reason)
	}
	if !result.Destructive {
		t.Fatal("expected Destructive flag to be set")
	}
	// Verify audit log captured the destructive flag.
	if len(audit.entries) == 0 {
		t.Fatal("expected audit entry")
	}
	if !audit.entries[len(audit.entries)-1].Result.Destructive {
		t.Fatal("audit entry should have Destructive=true")
	}
}

func TestCheck_AllowedGeneratesToken(t *testing.T) {
	gate, _ := newTestGate(map[string]*types.AgentConfig{
		"a1": agentWithTemplate("a1", "reader"),
	})
	result, err := gate.Check(context.Background(), "a1", minimalToolDecl("tool", TierReader), "do", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Allowed {
		t.Fatalf("expected allowed, got denied: %s", result.Reason)
	}
	if len(result.Token) != 26 {
		t.Fatalf("expected 26-char ULID token, got %d chars: %q", len(result.Token), result.Token)
	}
}

func TestCheck_AuditLoggedOnDeny(t *testing.T) {
	gate, audit := newTestGate(map[string]*types.AgentConfig{
		"a1": agentWithTemplate("a1", "reader"),
	})
	result, err := gate.Check(context.Background(), "a1", minimalToolDecl("tool", TierAdmin), "do", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Allowed {
		t.Fatal("expected denied")
	}
	if len(audit.entries) == 0 {
		t.Fatal("expected audit entry for denied check")
	}
	last := audit.entries[len(audit.entries)-1]
	if last.Result.Allowed {
		t.Fatal("audit entry should show denied")
	}
}

func TestCheck_AgentNotFound(t *testing.T) {
	gate, _ := newTestGate(map[string]*types.AgentConfig{})
	_, err := gate.Check(context.Background(), "nonexistent", minimalToolDecl("tool", TierReader), "do", nil)
	if err == nil {
		t.Fatal("expected error for missing agent")
	}
	if !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got: %v", err)
	}
}

func TestResolveAgentTier_Mapping(t *testing.T) {
	tests := []struct {
		template string
		want     string
	}{
		{"reader", TierReader},
		{"worker", TierWriter},
		{"admin", TierAdmin},
		{"writer", TierWriter},
		{"operator", TierOperator},
		{"guide", TierGuide},
		{"unknown", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got := ResolveAgentTier(tt.template)
		if got != tt.want {
			t.Errorf("ResolveAgentTier(%q) = %q, want %q", tt.template, got, tt.want)
		}
	}
}

func TestCheck_AdminAllowedForAdminTierTools(t *testing.T) {
	gate, _ := newTestGate(map[string]*types.AgentConfig{
		"a1": agentWithTemplate("a1", "admin"),
	})
	result, err := gate.Check(context.Background(), "a1", minimalToolDecl("system", TierAdmin), "do", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Allowed {
		t.Fatalf("expected admin to be allowed for admin-tier tools, got denied: %s", result.Reason)
	}
}

func TestSetAllowUnrestricted(t *testing.T) {
	gate, _ := newTestGate(map[string]*types.AgentConfig{})
	if gate.AllowUnrestricted() {
		t.Fatal("expected default allowUnrestricted to be false")
	}
	gate.SetAllowUnrestricted(true)
	if !gate.AllowUnrestricted() {
		t.Fatal("expected allowUnrestricted to be true after SetAllowUnrestricted(true)")
	}
}

func TestReaderHasMemoryWrite(t *testing.T) {
	caps := defaultCapabilities(TierReader)
	found := false
	for _, c := range caps {
		if c.Type == "memory" && c.Access == "write" {
			found = true
		}
	}
	if !found {
		t.Fatal("reader tier should have memory/write capability")
	}
}

func TestReaderHasNetworkRead(t *testing.T) {
	caps := defaultCapabilities(TierReader)
	found := false
	for _, c := range caps {
		if c.Type == "network" && c.Access == "read" {
			found = true
		}
	}
	if !found {
		t.Fatal("reader tier should have network/read capability")
	}
}

func TestWriterStillHasNetworkWrite(t *testing.T) {
	caps := defaultCapabilities(TierWriter)
	hasRead := false
	hasWrite := false
	for _, c := range caps {
		if c.Type == "network" && c.Access == "read" {
			hasRead = true
		}
		if c.Type == "network" && c.Access == "write" {
			hasWrite = true
		}
	}
	if !hasRead {
		t.Fatal("writer should still have network/read (inherited from reader)")
	}
	if !hasWrite {
		t.Fatal("writer should still have network/write")
	}
}

func TestOperatorHasBrowserExecute(t *testing.T) {
	caps := defaultCapabilities(TierOperator)
	found := false
	for _, c := range caps {
		if c.Type == "browser" && c.Access == "execute" {
			found = true
		}
	}
	if !found {
		t.Fatal("operator tier should have browser/execute capability")
	}
}

func TestAdminStillHasBrowserExecute(t *testing.T) {
	caps := defaultCapabilities(TierAdmin)
	found := false
	for _, c := range caps {
		if c.Type == "browser" && c.Access == "execute" {
			found = true
		}
	}
	if !found {
		t.Fatal("admin tier should still have browser/execute (inherited from operator)")
	}
}

func TestGetEffectiveCapabilities(t *testing.T) {
	t.Run("explicit grants used when set", func(t *testing.T) {
		agent := &types.AgentConfig{
			Template: "reader",
			CapabilityGrants: []types.Capability{
				{Tool: "custom", Action: "do", Resource: "*"},
			},
		}
		caps := getEffectiveCapabilities(agent, TierReader)
		if len(caps) != 1 {
			t.Fatalf("expected 1 explicit cap, got %d", len(caps))
		}
		if caps[0].Type != "custom" {
			t.Fatalf("expected custom cap, got %+v", caps[0])
		}
	})

	t.Run("tier defaults when empty", func(t *testing.T) {
		agent := &types.AgentConfig{Template: "reader"}
		caps := getEffectiveCapabilities(agent, TierReader)
		if len(caps) == 0 {
			t.Fatal("expected non-empty default caps for reader")
		}
		// Reader should have filesystem/read and memory/read.
		found := false
		for _, c := range caps {
			if c.Type == "filesystem" && c.Access == "read" {
				found = true
			}
		}
		if !found {
			t.Fatal("expected filesystem/read in reader defaults")
		}
	})

	t.Run("writer tier includes reader caps", func(t *testing.T) {
		agent := &types.AgentConfig{Template: "writer"}
		caps := getEffectiveCapabilities(agent, TierWriter)
		hasRead := false
		hasWrite := false
		for _, c := range caps {
			if c.Type == "filesystem" && c.Access == "read" {
				hasRead = true
			}
			if c.Type == "filesystem" && c.Access == "write" {
				hasWrite = true
			}
		}
		if !hasRead || !hasWrite {
			t.Fatalf("writer should have both read and write caps, hasRead=%v hasWrite=%v", hasRead, hasWrite)
		}
	})
}
