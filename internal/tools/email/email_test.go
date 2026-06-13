package email

import (
	"context"
	"fmt"
	"testing"

	"github.com/kkjorsvik/kyvik/internal/ktp"
)

func TestDeclaration(t *testing.T) {
	tool := New(nil, Config{})
	decl := tool.Declaration()

	if decl.Name != "email" {
		t.Errorf("Name = %q, want email", decl.Name)
	}
	if decl.MinTier != ktp.TierReader {
		t.Errorf("MinTier = %q, want %q", decl.MinTier, ktp.TierReader)
	}
	if len(decl.DefaultTiers) != 0 {
		t.Errorf("DefaultTiers should be empty (opt-in), got %v", decl.DefaultTiers)
	}

	actions := make(map[string]bool)
	for _, a := range decl.Actions {
		actions[a.Name] = true
	}
	for _, expected := range []string{"send", "read_inbox", "search"} {
		if !actions[expected] {
			t.Errorf("missing action %q", expected)
		}
	}
}

func TestInline(t *testing.T) {
	tool := New(nil, Config{})
	if !tool.Inline() {
		t.Error("expected Inline() to return true")
	}
}

func TestSendMissingParams(t *testing.T) {
	resolver := func(_ context.Context, _, _, _ string) (string, error) {
		return "test-value", nil
	}
	tool := New(resolver, Config{})

	resp, err := tool.Execute(context.Background(), ktp.ToolRequest{
		ID:     "test-1",
		Action: "send",
		Parameters: map[string]any{
			"to": "user@example.com",
			// Missing subject and body.
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Success {
		t.Error("expected failure for missing params")
	}
}

func TestSendTooManyRecipients(t *testing.T) {
	resolver := func(_ context.Context, _, _, key string) (string, error) {
		switch key {
		case "email_smtp_host":
			return "smtp.example.com", nil
		case "email_smtp_user":
			return "user@example.com", nil
		case "email_smtp_password":
			return "password", nil
		default:
			return "", fmt.Errorf("not found: %s", key)
		}
	}
	tool := New(resolver, Config{MaxRecipientsPerSend: 2})

	resp, err := tool.Execute(context.Background(), ktp.ToolRequest{
		ID:     "test-2",
		Action: "send",
		Parameters: map[string]any{
			"to":      "a@example.com, b@example.com, c@example.com",
			"subject": "Test",
			"body":    "Hello",
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Success {
		t.Error("expected failure for too many recipients")
	}
}

func TestReadInboxMissingCreds(t *testing.T) {
	resolver := func(_ context.Context, _, _, _ string) (string, error) {
		return "", fmt.Errorf("not found")
	}
	tool := New(resolver, Config{})

	resp, err := tool.Execute(context.Background(), ktp.ToolRequest{
		ID:         "test-3",
		Action:     "read_inbox",
		Parameters: map[string]any{},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Success {
		t.Error("expected failure for missing IMAP creds")
	}
}

func TestSearchMissingQuery(t *testing.T) {
	tool := New(nil, Config{})

	resp, err := tool.Execute(context.Background(), ktp.ToolRequest{
		ID:         "test-4",
		Action:     "search",
		Parameters: map[string]any{},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Success {
		t.Error("expected failure for missing query")
	}
}

func TestUnknownAction(t *testing.T) {
	tool := New(nil, Config{})
	resp, err := tool.Execute(context.Background(), ktp.ToolRequest{
		ID:         "test-5",
		Action:     "unknown",
		Parameters: map[string]any{},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Success {
		t.Error("expected failure for unknown action")
	}
}

func TestBuildMessage(t *testing.T) {
	msg := buildMessage("from@test.com", []string{"to@test.com"}, "cc@test.com", "", "Test Subject", "Hello World")

	if !contains(msg, "From: from@test.com") {
		t.Error("missing From header")
	}
	if !contains(msg, "To: to@test.com") {
		t.Error("missing To header")
	}
	if !contains(msg, "Subject: Test Subject") {
		t.Error("missing Subject header")
	}
	if !contains(msg, "Cc: cc@test.com") {
		t.Error("missing Cc header")
	}
	if !contains(msg, "Hello World") {
		t.Error("missing body")
	}
}

func TestParseAddresses(t *testing.T) {
	addrs := parseAddresses("a@test.com, b@test.com , c@test.com")
	if len(addrs) != 3 {
		t.Errorf("got %d addresses, want 3", len(addrs))
	}
}

func TestQuoteIMAP(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"simple", `"simple"`},
		{`has "quotes"`, `"has \"quotes\""`},
		{`has\backslash`, `"has\\backslash"`},
	}
	for _, tt := range tests {
		got := quoteIMAP(tt.input)
		if got != tt.want {
			t.Errorf("quoteIMAP(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
