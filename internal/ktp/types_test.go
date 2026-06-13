package ktp

import (
	"errors"
	"testing"
	"time"
)

// --- Validate ---

func TestValidate_Valid(t *testing.T) {
	d := validDeclaration()
	if err := d.Validate(); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestValidate_MissingName(t *testing.T) {
	d := validDeclaration()
	d.Name = ""
	assertInvalidDeclaration(t, d.Validate())
}

func TestValidate_MissingVersion(t *testing.T) {
	d := validDeclaration()
	d.Version = ""
	assertInvalidDeclaration(t, d.Validate())
}

func TestValidate_EmptyActions(t *testing.T) {
	d := validDeclaration()
	d.Actions = nil
	assertInvalidDeclaration(t, d.Validate())
}

func TestValidate_ActionMissingName(t *testing.T) {
	d := validDeclaration()
	d.Actions[0].Name = ""
	assertInvalidDeclaration(t, d.Validate())
}

func TestValidate_ActionMissingParameterSchemaType(t *testing.T) {
	d := validDeclaration()
	d.Actions[0].Parameters.Type = ""
	assertInvalidDeclaration(t, d.Validate())
}

// --- GetAction ---

func TestGetAction_Found(t *testing.T) {
	d := validDeclaration()
	a, ok := d.GetAction("read")
	if !ok {
		t.Fatal("expected action to be found")
	}
	if a.Name != "read" {
		t.Fatalf("expected name %q, got %q", "read", a.Name)
	}
}

func TestGetAction_NotFound(t *testing.T) {
	d := validDeclaration()
	_, ok := d.GetAction("nonexistent")
	if ok {
		t.Fatal("expected action not to be found")
	}
}

// --- Capability.Matches ---

func TestMatches_Exact(t *testing.T) {
	c := Capability{Type: "file", Access: "read", Resource: "/data"}
	r := Capability{Type: "file", Access: "read", Resource: "/data"}
	if !c.Matches(r) {
		t.Fatal("exact match should succeed")
	}
}

func TestMatches_WildcardType(t *testing.T) {
	c := Capability{Type: "*", Access: "read", Resource: "/data"}
	r := Capability{Type: "file", Access: "read", Resource: "/data"}
	if !c.Matches(r) {
		t.Fatal("wildcard type should match")
	}
}

func TestMatches_WildcardAccess(t *testing.T) {
	c := Capability{Type: "file", Access: "*", Resource: "/data"}
	r := Capability{Type: "file", Access: "write", Resource: "/data"}
	if !c.Matches(r) {
		t.Fatal("wildcard access should match")
	}
}

func TestMatches_WildcardResource(t *testing.T) {
	c := Capability{Type: "file", Access: "read", Resource: "*"}
	r := Capability{Type: "file", Access: "read", Resource: "/anything"}
	if !c.Matches(r) {
		t.Fatal("wildcard resource should match")
	}
}

func TestMatches_AllWildcards(t *testing.T) {
	c := Capability{Type: "*", Access: "*", Resource: "*"}
	r := Capability{Type: "net", Access: "execute", Resource: "/api/v1"}
	if !c.Matches(r) {
		t.Fatal("all wildcards should match anything")
	}
}

func TestMatches_PathPrefix(t *testing.T) {
	c := Capability{Type: "file", Access: "read", Resource: "/data/*"}

	cases := []struct {
		resource string
		want     bool
	}{
		{"/data", true},          // exact prefix
		{"/data/file.txt", true}, // child
		{"/data/sub/deep", true}, // nested child
		{"/other", false},        // no match
		{"/datafoo", false},      // not a child — different prefix
	}
	for _, tc := range cases {
		r := Capability{Type: "file", Access: "read", Resource: tc.resource}
		if got := c.Matches(r); got != tc.want {
			t.Errorf("resource %q: got %v, want %v", tc.resource, got, tc.want)
		}
	}
}

func TestMatches_TypeMismatch(t *testing.T) {
	c := Capability{Type: "net", Access: "read", Resource: "/data"}
	r := Capability{Type: "file", Access: "read", Resource: "/data"}
	if c.Matches(r) {
		t.Fatal("type mismatch should fail")
	}
}

func TestMatches_AccessMismatch(t *testing.T) {
	c := Capability{Type: "file", Access: "read", Resource: "/data"}
	r := Capability{Type: "file", Access: "write", Resource: "/data"}
	if c.Matches(r) {
		t.Fatal("access mismatch should fail")
	}
}

func TestMatches_ResourceMismatch(t *testing.T) {
	c := Capability{Type: "file", Access: "read", Resource: "/data"}
	r := Capability{Type: "file", Access: "read", Resource: "/other"}
	if c.Matches(r) {
		t.Fatal("resource mismatch should fail")
	}
}

func TestMatches_RequiredWildcardDenied(t *testing.T) {
	c := Capability{Type: "file", Access: "read", Resource: "/data"}
	r := Capability{Type: "file", Access: "read", Resource: "*"}
	if c.Matches(r) {
		t.Fatal("required wildcard resource should only be matched by pattern wildcard")
	}
}

// --- TierLevel ---

func TestTierLevel_AllTiers(t *testing.T) {
	cases := []struct {
		tier string
		want int
	}{
		{TierReader, 0},
		{TierGuide, 0},
		{TierWriter, 1},
		{TierOperator, 2},
		{TierAdmin, 3},
		{"unknown", -1},
		{"", -1},
	}
	for _, tc := range cases {
		if got := TierLevel(tc.tier); got != tc.want {
			t.Errorf("TierLevel(%q) = %d, want %d", tc.tier, got, tc.want)
		}
	}
}

// --- TierAtLeast ---

func TestTierAtLeast_SameTier(t *testing.T) {
	if !TierAtLeast(TierWriter, TierWriter) {
		t.Fatal("same tier should pass")
	}
}

func TestTierAtLeast_HigherTier(t *testing.T) {
	if !TierAtLeast(TierAdmin, TierWriter) {
		t.Fatal("higher tier should pass")
	}
}

func TestTierAtLeast_LowerTier(t *testing.T) {
	if TierAtLeast(TierReader, TierWriter) {
		t.Fatal("lower tier should fail")
	}
}

func TestTierAtLeast_UnknownAgent(t *testing.T) {
	if TierAtLeast("unknown", TierReader) {
		t.Fatal("unknown agent tier should fail")
	}
}

func TestTierAtLeast_UnknownRequired(t *testing.T) {
	if TierAtLeast(TierAdmin, "unknown") {
		t.Fatal("unknown required tier should fail")
	}
}

func TestTierAtLeast_GuideRequiredBlocksReader(t *testing.T) {
	if TierAtLeast(TierReader, TierGuide) {
		t.Fatal("reader should NOT satisfy guide requirement")
	}
}

func TestTierAtLeast_GuideRequiredBlocksWriter(t *testing.T) {
	if TierAtLeast(TierWriter, TierGuide) {
		t.Fatal("writer should NOT satisfy guide requirement")
	}
}

func TestTierAtLeast_GuideRequiredBlocksOperator(t *testing.T) {
	if TierAtLeast(TierOperator, TierGuide) {
		t.Fatal("operator should NOT satisfy guide requirement")
	}
}

func TestTierAtLeast_GuideRequiredAllowsGuide(t *testing.T) {
	if !TierAtLeast(TierGuide, TierGuide) {
		t.Fatal("guide should satisfy guide requirement")
	}
}

func TestTierAtLeast_GuideRequiredAllowsAdmin(t *testing.T) {
	if !TierAtLeast(TierAdmin, TierGuide) {
		t.Fatal("admin should satisfy guide requirement")
	}
}

func TestTierAtLeast_GuideCanUseReaderTools(t *testing.T) {
	if !TierAtLeast(TierGuide, TierReader) {
		t.Fatal("guide should still satisfy reader requirement")
	}
}

// --- NewToolRequest ---

func TestNewToolRequest(t *testing.T) {
	before := time.Now()
	req := NewToolRequest("agent-1", "file", "read", map[string]any{"path": "/tmp"})
	after := time.Now()

	if len(req.ID) != 26 {
		t.Fatalf("ULID should be 26 chars, got %d (%q)", len(req.ID), req.ID)
	}
	if req.AgentID != "agent-1" {
		t.Fatalf("AgentID = %q, want %q", req.AgentID, "agent-1")
	}
	if req.Tool != "file" {
		t.Fatalf("Tool = %q, want %q", req.Tool, "file")
	}
	if req.Action != "read" {
		t.Fatalf("Action = %q, want %q", req.Action, "read")
	}
	if req.Parameters["path"] != "/tmp" {
		t.Fatalf("Parameters[path] = %v, want /tmp", req.Parameters["path"])
	}
	if req.Timestamp.Before(before) || req.Timestamp.After(after) {
		t.Fatalf("timestamp %v not in range [%v, %v]", req.Timestamp, before, after)
	}
}

func TestNewToolRequest_UniqueIDs(t *testing.T) {
	r1 := NewToolRequest("a", "t", "a", nil)
	r2 := NewToolRequest("a", "t", "a", nil)
	if r1.ID == r2.ID {
		t.Fatalf("expected unique IDs, both were %q", r1.ID)
	}
}

// --- NewToolResponse ---

func TestNewToolResponse_Success(t *testing.T) {
	resp := NewToolResponse("REQ123", true, "data", "", 42)
	if resp.RequestID != "REQ123" {
		t.Fatalf("RequestID = %q, want %q", resp.RequestID, "REQ123")
	}
	if !resp.Success {
		t.Fatal("expected success")
	}
	if resp.Result != "data" {
		t.Fatalf("Result = %v, want %q", resp.Result, "data")
	}
	if resp.ExecutionMs != 42 {
		t.Fatalf("ExecutionMs = %d, want 42", resp.ExecutionMs)
	}
}

func TestNewToolResponse_Error(t *testing.T) {
	resp := NewToolResponse("REQ456", false, nil, "something broke", 10)
	if resp.Success {
		t.Fatal("expected failure")
	}
	if resp.Result != nil {
		t.Fatalf("Result = %v, want nil", resp.Result)
	}
	if resp.Error != "something broke" {
		t.Fatalf("Error = %q, want %q", resp.Error, "something broke")
	}
}

// --- helpers ---

func validDeclaration() ToolDeclaration {
	return ToolDeclaration{
		Name:    "file-tool",
		Version: "1.0.0",
		Actions: []ActionSpec{
			{
				Name:       "read",
				Parameters: JSONSchema{Type: "object"},
			},
		},
	}
}

func assertInvalidDeclaration(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrInvalidDeclaration) {
		t.Fatalf("expected ErrInvalidDeclaration, got %v", err)
	}
}
