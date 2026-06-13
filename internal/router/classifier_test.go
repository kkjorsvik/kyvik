package router

import (
	"context"
	"fmt"
	"testing"

	"github.com/kkjorsvik/kyvik/internal/history"
	"github.com/kkjorsvik/kyvik/internal/models"
)

// classifierMockProvider returns a configurable response for classifier tests.
type classifierMockProvider struct {
	name     string
	response string
	err      error
	calls    int
}

func (m *classifierMockProvider) Complete(_ context.Context, _ models.CompletionRequest) (*models.CompletionResponse, error) {
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	return &models.CompletionResponse{
		Content:   m.response,
		TokensIn:  10,
		TokensOut: 5,
		Cost:      0.001,
	}, nil
}

func (m *classifierMockProvider) Stream(_ context.Context, _ models.CompletionRequest) (<-chan models.StreamChunk, error) {
	return nil, nil
}

func (m *classifierMockProvider) ListModels(_ context.Context) ([]models.ModelInfo, error) {
	return nil, nil
}

func (m *classifierMockProvider) Name() string { return m.name }

var testSlots = []ModelSlot{
	{Name: "fast", Provider: "openrouter", Model: "gpt-4o-mini"},
	{Name: "reason", Provider: "openrouter", Model: "o1-preview"},
	{Name: "creative", Provider: "openrouter", Model: "claude-3-opus"},
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name       string
		response   string
		providerErr error
		wantSlot   string
		wantConf   string
		wantErr    bool
	}{
		{
			name:     "successful classification",
			response: `{"slot":"reason","confidence":"high","reason":"deep thinking"}`,
			wantSlot: "reason",
			wantConf: "high",
		},
		{
			name:     "low confidence",
			response: `{"slot":"fast","confidence":"low","reason":"unclear"}`,
			wantSlot: "fast",
			wantConf: "low",
		},
		{
			name:     "malformed JSON",
			response: `use the reason slot`,
			wantErr:  true,
		},
		{
			name:     "markdown-fenced JSON",
			response: "```json\n{\"slot\":\"creative\",\"confidence\":\"medium\",\"reason\":\"artistic request\"}\n```",
			wantSlot: "creative",
			wantConf: "medium",
		},
		{
			name:     "unknown slot",
			response: `{"slot":"nonexistent","confidence":"high","reason":"test"}`,
			wantErr:  true,
		},
		{
			name:        "provider error",
			providerErr: fmt.Errorf("model unavailable"),
			wantErr:     true,
		},
		{
			name:     "empty message with valid classification",
			response: `{"slot":"fast","confidence":"high","reason":"simple query"}`,
			wantSlot: "fast",
			wantConf: "high",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clf := NewClassifier()
			mock := &classifierMockProvider{
				name:     "openrouter",
				response: tt.response,
				err:      tt.providerErr,
			}

			result, err := clf.Classify(context.Background(), "test-agent-"+tt.name, "hello",
				nil, testSlots, mock, "classifier-model")

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.SlotName != tt.wantSlot {
				t.Errorf("SlotName = %q, want %q", result.SlotName, tt.wantSlot)
			}
			if result.Confidence != tt.wantConf {
				t.Errorf("Confidence = %q, want %q", result.Confidence, tt.wantConf)
			}
		})
	}
}

func TestClassify_CacheHit(t *testing.T) {
	clf := NewClassifier()
	mock := &classifierMockProvider{
		name:     "openrouter",
		response: `{"slot":"reason","confidence":"high","reason":"thinking required"}`,
	}

	// First call — should hit the provider
	result1, err := clf.Classify(context.Background(), "agent-a", "question 1",
		nil, testSlots, mock, "classifier-model")
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if result1.SlotName != "reason" {
		t.Errorf("first call SlotName = %q, want %q", result1.SlotName, "reason")
	}
	if mock.calls != 1 {
		t.Errorf("expected 1 call after first classify, got %d", mock.calls)
	}

	// Second call same agent — should use cache
	result2, err := clf.Classify(context.Background(), "agent-a", "question 2",
		nil, testSlots, mock, "classifier-model")
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if result2.SlotName != "reason" {
		t.Errorf("cached SlotName = %q, want %q", result2.SlotName, "reason")
	}
	if result2.Reason != "cached" {
		t.Errorf("expected cached reason, got %q", result2.Reason)
	}
	if mock.calls != 1 {
		t.Errorf("expected 1 call after cache hit, got %d", mock.calls)
	}
}

func TestClassify_CacheMissDifferentAgent(t *testing.T) {
	clf := NewClassifier()
	mock := &classifierMockProvider{
		name:     "openrouter",
		response: `{"slot":"fast","confidence":"high","reason":"quick response"}`,
	}

	// Call for agent-a
	_, err := clf.Classify(context.Background(), "agent-a", "hello",
		nil, testSlots, mock, "classifier-model")
	if err != nil {
		t.Fatalf("agent-a call: %v", err)
	}

	// Call for agent-b — different agent, should not use cache
	_, err = clf.Classify(context.Background(), "agent-b", "hello",
		nil, testSlots, mock, "classifier-model")
	if err != nil {
		t.Fatalf("agent-b call: %v", err)
	}

	if mock.calls != 2 {
		t.Errorf("expected 2 calls for different agents, got %d", mock.calls)
	}
}

func TestClassify_WithHistory(t *testing.T) {
	clf := NewClassifier()
	mock := &classifierMockProvider{
		name:     "openrouter",
		response: `{"slot":"reason","confidence":"high","reason":"continuing deep discussion"}`,
	}

	recentHistory := []history.HistoryEntry{
		{Role: "user", Content: "explain quantum computing"},
		{Role: "assistant", Content: "Quantum computing uses qubits..."},
		{Role: "user", Content: "go deeper"},
	}

	result, err := clf.Classify(context.Background(), "agent-history", "elaborate further",
		recentHistory, testSlots, mock, "classifier-model")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.SlotName != "reason" {
		t.Errorf("SlotName = %q, want %q", result.SlotName, "reason")
	}
	if mock.calls != 1 {
		t.Errorf("expected 1 call, got %d", mock.calls)
	}
}

func TestExtractJSON(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
	}{
		{
			name:  "raw JSON",
			input: `{"slot":"fast"}`,
			want:  `{"slot":"fast"}`,
		},
		{
			name:  "markdown fenced",
			input: "```json\n{\"slot\":\"fast\"}\n```",
			want:  `{"slot":"fast"}`,
		},
		{
			name:  "embedded in text",
			input: `Sure, here is the result: {"slot":"fast","confidence":"high","reason":"test"} done`,
			want:  `{"slot":"fast","confidence":"high","reason":"test"}`,
		},
		{
			name:  "no JSON",
			input: "just plain text",
			want:  "",
		},
		{
			name:  "whitespace padded",
			input: "  \n  {\"slot\":\"fast\"}  \n  ",
			want:  `{"slot":"fast"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractJSON(tt.input)
			if got != tt.want {
				t.Errorf("extractJSON(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
