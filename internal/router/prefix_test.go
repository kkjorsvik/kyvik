package router

import "testing"

func TestParsePrefix(t *testing.T) {
	slots := []ModelSlot{
		{Name: "reason", Provider: "openrouter", Model: "deepseek/r1"},
		{Name: "code", Provider: "openrouter", Model: "claude-sonnet"},
		{Name: "fast", Provider: "ollama", Model: "llama3"},
	}

	tests := []struct {
		name        string
		message     string
		slots       []ModelSlot
		wantMatched bool
		wantSlot    string
		wantMessage string
	}{
		{
			name:        "basic match reason slot",
			message:     "reason: explain this",
			slots:       slots,
			wantMatched: true,
			wantSlot:    "reason",
			wantMessage: "explain this",
		},
		{
			name:        "basic match code slot",
			message:     "code: implement retry",
			slots:       slots,
			wantMatched: true,
			wantSlot:    "code",
			wantMessage: "implement retry",
		},
		{
			name:        "unknown slot no match",
			message:     "unknown: something",
			slots:       slots,
			wantMatched: false,
			wantSlot:    "",
			wantMessage: "unknown: something",
		},
		{
			name:        "non-slot word no match",
			message:     "hello: world",
			slots:       slots,
			wantMatched: false,
			wantSlot:    "",
			wantMessage: "hello: world",
		},
		{
			name:        "case insensitive match",
			message:     "REASON: explain this",
			slots:       slots,
			wantMatched: true,
			wantSlot:    "reason",
			wantMessage: "explain this",
		},
		{
			name:        "no space after colon",
			message:     "reason:no space",
			slots:       slots,
			wantMatched: false,
			wantSlot:    "",
			wantMessage: "reason:no space",
		},
		{
			name:        "empty message",
			message:     "",
			slots:       slots,
			wantMatched: false,
			wantSlot:    "",
			wantMessage: "",
		},
		{
			name:        "colon in middle of sentence",
			message:     "I said: something interesting",
			slots:       slots,
			wantMatched: false,
			wantSlot:    "",
			wantMessage: "I said: something interesting",
		},
		{
			name:        "empty message after prefix",
			message:     "reason: ",
			slots:       slots,
			wantMatched: true,
			wantSlot:    "reason",
			wantMessage: "",
		},
		{
			name:        "no slots configured",
			message:     "reason: explain",
			slots:       nil,
			wantMatched: false,
			wantSlot:    "",
			wantMessage: "reason: explain",
		},
		{
			name:        "colon at start",
			message:     ": something",
			slots:       slots,
			wantMatched: false,
			wantSlot:    "",
			wantMessage: ": something",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParsePrefix(tt.message, tt.slots)
			if got.Matched != tt.wantMatched {
				t.Errorf("Matched = %v, want %v", got.Matched, tt.wantMatched)
			}
			if got.SlotName != tt.wantSlot {
				t.Errorf("SlotName = %q, want %q", got.SlotName, tt.wantSlot)
			}
			if got.Message != tt.wantMessage {
				t.Errorf("Message = %q, want %q", got.Message, tt.wantMessage)
			}
		})
	}
}
