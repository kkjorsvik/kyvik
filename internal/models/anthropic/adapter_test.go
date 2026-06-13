package anthropic_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/models"
	"github.com/kkjorsvik/kyvik/internal/models/anthropic"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) (*anthropic.Client, *httptest.Server) {
	t.Helper()
	srv := newHTTPServer(t, handler)
	c := anthropic.New("test-key",
		anthropic.WithBaseURL(srv.URL),
		anthropic.WithMaxRetries(3),
		anthropic.WithBaseBackoff(time.Millisecond),
	)
	return c, srv
}

func newHTTPServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("listen not permitted: %v", err)
	}
	server := &httptest.Server{
		Listener: listener,
		Config:   &http.Server{Handler: handler},
	}
	server.Start()
	return server
}

// --- Name ---

func TestName(t *testing.T) {
	c := anthropic.New("key")
	if got := c.Name(); got != "anthropic" {
		t.Errorf("Name() = %q, want %q", got, "anthropic")
	}
}

// --- Complete ---

func TestComplete_BasicResponse(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id":          "msg_123",
			"type":        "message",
			"role":        "assistant",
			"model":       "claude-sonnet-4-20250514",
			"stop_reason": "end_turn",
			"content": []map[string]any{
				{"type": "text", "text": "Hello!"},
			},
			"usage": map[string]any{
				"input_tokens":  10,
				"output_tokens": 5,
			},
		})
	})
	defer srv.Close()

	resp, err := c.Complete(context.Background(), models.CompletionRequest{
		Model:    "claude-sonnet-4-20250514",
		Messages: []models.ChatMessage{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "Hello!" {
		t.Errorf("Content = %q, want %q", resp.Content, "Hello!")
	}
	if resp.Model != "claude-sonnet-4-20250514" {
		t.Errorf("Model = %q, want %q", resp.Model, "claude-sonnet-4-20250514")
	}
	if resp.TokensIn != 10 {
		t.Errorf("TokensIn = %d, want 10", resp.TokensIn)
	}
	if resp.TokensOut != 5 {
		t.Errorf("TokensOut = %d, want 5", resp.TokensOut)
	}
	if resp.StopReason != "end" {
		t.Errorf("StopReason = %q, want %q", resp.StopReason, "end")
	}
}

func TestComplete_WithToolCalls(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id":          "msg_456",
			"type":        "message",
			"role":        "assistant",
			"model":       "claude-sonnet-4-20250514",
			"stop_reason": "tool_use",
			"content": []map[string]any{
				{"type": "text", "text": "Let me check the weather."},
				{
					"type":  "tool_use",
					"id":    "toolu_123",
					"name":  "get_weather",
					"input": map[string]any{"location": "London"},
				},
			},
			"usage": map[string]any{"input_tokens": 10, "output_tokens": 5},
		})
	})
	defer srv.Close()

	resp, err := c.Complete(context.Background(), models.CompletionRequest{
		Model:    "claude-sonnet-4-20250514",
		Messages: []models.ChatMessage{{Role: "user", Content: "weather?"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "Let me check the weather." {
		t.Errorf("Content = %q, want %q", resp.Content, "Let me check the weather.")
	}
	if resp.StopReason != "tool_use" {
		t.Errorf("StopReason = %q, want %q", resp.StopReason, "tool_use")
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc.ID != "toolu_123" {
		t.Errorf("ToolCall.ID = %q, want %q", tc.ID, "toolu_123")
	}
	if tc.Name != "get_weather" {
		t.Errorf("ToolCall.Name = %q, want %q", tc.Name, "get_weather")
	}
	params, ok := tc.Parameters.(map[string]any)
	if !ok {
		t.Fatalf("Parameters type = %T, want map[string]any", tc.Parameters)
	}
	if params["location"] != "London" {
		t.Errorf("Parameters[location] = %v, want %q", params["location"], "London")
	}
}

func TestComplete_SendsCorrectHeaders(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("x-api-key"); got != "test-key" {
			t.Errorf("x-api-key = %q, want %q", got, "test-key")
		}
		if got := r.Header.Get("anthropic-version"); got != "2023-06-01" {
			t.Errorf("anthropic-version = %q, want %q", got, "2023-06-01")
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want %q", got, "application/json")
		}
		json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "model": "m",
			"content": []map[string]any{{"type": "text", "text": "ok"}},
		})
	})
	defer srv.Close()

	_, err := c.Complete(context.Background(), models.CompletionRequest{
		Model:    "m",
		Messages: []models.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestComplete_SendsToolDefinitions(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)

		tools, ok := body["tools"].([]any)
		if !ok || len(tools) == 0 {
			t.Fatal("expected tools in request body")
		}
		tool := tools[0].(map[string]any)
		if tool["name"] != "search" {
			t.Errorf("tool name = %v, want %q", tool["name"], "search")
		}
		if tool["description"] != "Search things" {
			t.Errorf("tool description = %v, want %q", tool["description"], "Search things")
		}
		if _, ok := tool["input_schema"]; !ok {
			t.Error("expected input_schema in tool definition")
		}

		json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "model": "m",
			"content": []map[string]any{{"type": "text", "text": "ok"}},
		})
	})
	defer srv.Close()

	_, err := c.Complete(context.Background(), models.CompletionRequest{
		Model:    "m",
		Messages: []models.ChatMessage{{Role: "user", Content: "hi"}},
		Tools: []models.ToolDefinition{
			{Name: "search", Description: "Search things", Parameters: map[string]any{"type": "object"}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestComplete_SystemPromptExtraction(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)

		system, _ := body["system"].(string)
		if system != "You are helpful.\n\nBe concise." {
			t.Errorf("system = %q, want %q", system, "You are helpful.\n\nBe concise.")
		}

		// System messages should not appear in messages array.
		msgs, _ := body["messages"].([]any)
		for _, m := range msgs {
			msg := m.(map[string]any)
			if msg["role"] == "system" {
				t.Error("system message should not appear in messages array")
			}
		}

		json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "model": "m",
			"content": []map[string]any{{"type": "text", "text": "ok"}},
		})
	})
	defer srv.Close()

	_, err := c.Complete(context.Background(), models.CompletionRequest{
		Model: "m",
		Messages: []models.ChatMessage{
			{Role: "system", Content: "You are helpful."},
			{Role: "system", Content: "Be concise."},
			{Role: "user", Content: "hi"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestComplete_ToolResultConversion(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)

		msgs, _ := body["messages"].([]any)
		if len(msgs) < 3 {
			t.Fatalf("expected at least 3 messages, got %d", len(msgs))
		}

		// Third message should be a user message with tool_result content block.
		toolMsg := msgs[2].(map[string]any)
		if toolMsg["role"] != "user" {
			t.Errorf("tool result role = %v, want %q", toolMsg["role"], "user")
		}
		content, ok := toolMsg["content"].([]any)
		if !ok {
			t.Fatal("expected content to be array of content blocks")
		}
		block := content[0].(map[string]any)
		if block["type"] != "tool_result" {
			t.Errorf("block type = %v, want %q", block["type"], "tool_result")
		}
		if block["tool_use_id"] != "toolu_123" {
			t.Errorf("tool_use_id = %v, want %q", block["tool_use_id"], "toolu_123")
		}

		json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "model": "m",
			"content": []map[string]any{{"type": "text", "text": "ok"}},
		})
	})
	defer srv.Close()

	_, err := c.Complete(context.Background(), models.CompletionRequest{
		Model: "m",
		Messages: []models.ChatMessage{
			{Role: "user", Content: "use the tool"},
			{Role: "assistant", Content: "", ToolCalls: []models.ToolUse{
				{ID: "toolu_123", Name: "get_weather", Parameters: map[string]any{"location": "London"}},
			}},
			{Role: "tool", ToolCallID: "toolu_123", Content: "Sunny, 22C"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestComplete_ConsecutiveToolResults(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)

		msgs, _ := body["messages"].([]any)
		// user, assistant (with 2 tool_use), user (with 2 tool_results merged)
		if len(msgs) != 3 {
			t.Fatalf("expected 3 messages, got %d", len(msgs))
		}

		// The third message should have both tool results merged.
		toolMsg := msgs[2].(map[string]any)
		content, ok := toolMsg["content"].([]any)
		if !ok {
			t.Fatal("expected content to be array")
		}
		if len(content) != 2 {
			t.Fatalf("expected 2 tool_result blocks merged, got %d", len(content))
		}

		json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "model": "m",
			"content": []map[string]any{{"type": "text", "text": "ok"}},
		})
	})
	defer srv.Close()

	_, err := c.Complete(context.Background(), models.CompletionRequest{
		Model: "m",
		Messages: []models.ChatMessage{
			{Role: "user", Content: "use both tools"},
			{Role: "assistant", Content: "", ToolCalls: []models.ToolUse{
				{ID: "toolu_1", Name: "tool_a", Parameters: nil},
				{ID: "toolu_2", Name: "tool_b", Parameters: nil},
			}},
			{Role: "tool", ToolCallID: "toolu_1", Content: "result a"},
			{Role: "tool", ToolCallID: "toolu_2", Content: "result b"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestComplete_APIError(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		json.NewEncoder(w).Encode(map[string]any{
			"type": "error",
			"error": map[string]any{
				"type":    "invalid_request_error",
				"message": "bad request",
			},
		})
	})
	defer srv.Close()

	_, err := c.Complete(context.Background(), models.CompletionRequest{
		Model:    "m",
		Messages: []models.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	apiErr, ok := err.(*anthropic.APIError)
	if !ok {
		t.Fatalf("error type = %T, want *APIError", err)
	}
	if apiErr.StatusCode != 400 {
		t.Errorf("StatusCode = %d, want 400", apiErr.StatusCode)
	}
	if apiErr.Message != "bad request" {
		t.Errorf("Message = %q, want %q", apiErr.Message, "bad request")
	}
	if apiErr.Type != "invalid_request_error" {
		t.Errorf("Type = %q, want %q", apiErr.Type, "invalid_request_error")
	}
}

func TestComplete_NoRetryOn4xx(t *testing.T) {
	var count atomic.Int32
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		w.WriteHeader(401)
		json.NewEncoder(w).Encode(map[string]any{
			"type":  "error",
			"error": map[string]any{"type": "authentication_error", "message": "unauthorized"},
		})
	})
	defer srv.Close()

	_, err := c.Complete(context.Background(), models.CompletionRequest{
		Model:    "m",
		Messages: []models.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if got := count.Load(); got != 1 {
		t.Errorf("request count = %d, want 1 (no retry on 4xx)", got)
	}
}

func TestComplete_Retry429(t *testing.T) {
	var count atomic.Int32
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		n := count.Add(1)
		if n <= 2 {
			w.WriteHeader(429)
			json.NewEncoder(w).Encode(map[string]any{
				"type":  "error",
				"error": map[string]any{"type": "rate_limit_error", "message": "rate limited"},
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "model": "m",
			"content": []map[string]any{{"type": "text", "text": "ok"}},
		})
	})
	defer srv.Close()

	resp, err := c.Complete(context.Background(), models.CompletionRequest{
		Model:    "m",
		Messages: []models.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("Content = %q, want %q", resp.Content, "ok")
	}
	if got := count.Load(); got != 3 {
		t.Errorf("request count = %d, want 3", got)
	}
}

func TestComplete_Retry529(t *testing.T) {
	var count atomic.Int32
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		n := count.Add(1)
		if n == 1 {
			w.WriteHeader(529)
			json.NewEncoder(w).Encode(map[string]any{
				"type":  "error",
				"error": map[string]any{"type": "overloaded_error", "message": "overloaded"},
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "model": "m",
			"content": []map[string]any{{"type": "text", "text": "ok"}},
		})
	})
	defer srv.Close()

	resp, err := c.Complete(context.Background(), models.CompletionRequest{
		Model:    "m",
		Messages: []models.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("Content = %q, want %q", resp.Content, "ok")
	}
	if got := count.Load(); got != 2 {
		t.Errorf("request count = %d, want 2", got)
	}
}

func TestComplete_Retry5xx(t *testing.T) {
	var count atomic.Int32
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		n := count.Add(1)
		if n == 1 {
			w.WriteHeader(502)
			json.NewEncoder(w).Encode(map[string]any{
				"type":  "error",
				"error": map[string]any{"type": "api_error", "message": "bad gateway"},
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "model": "m",
			"content": []map[string]any{{"type": "text", "text": "ok"}},
		})
	})
	defer srv.Close()

	resp, err := c.Complete(context.Background(), models.CompletionRequest{
		Model:    "m",
		Messages: []models.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("Content = %q, want %q", resp.Content, "ok")
	}
	if got := count.Load(); got != 2 {
		t.Errorf("request count = %d, want 2", got)
	}
}

func TestComplete_MaxRetriesExceeded(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		json.NewEncoder(w).Encode(map[string]any{
			"type":  "error",
			"error": map[string]any{"type": "rate_limit_error", "message": "rate limited"},
		})
	})
	defer srv.Close()

	_, err := c.Complete(context.Background(), models.CompletionRequest{
		Model:    "m",
		Messages: []models.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if got := err.Error(); !contains(got, "max retries exceeded") {
		t.Errorf("error = %q, want it to contain %q", got, "max retries exceeded")
	}
}

func TestComplete_ContextCancellation(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
		json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "model": "m",
			"content": []map[string]any{{"type": "text", "text": "late"}},
		})
	})
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	_, err := c.Complete(ctx, models.CompletionRequest{
		Model:    "m",
		Messages: []models.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

func TestComplete_LocalCostCalculation(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant",
			"model":   "claude-sonnet-4-20250514",
			"content": []map[string]any{{"type": "text", "text": "ok"}},
			"usage": map[string]any{
				"input_tokens":  1000,
				"output_tokens": 500,
			},
		})
	})
	defer srv.Close()

	resp, err := c.Complete(context.Background(), models.CompletionRequest{
		Model:    "claude-sonnet-4-20250514",
		Messages: []models.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// claude-sonnet-4-20250514: input=$3.00/M, output=$15.00/M
	// cost = (1000 * 3.00 + 500 * 15.00) / 1_000_000 = (3000 + 7500) / 1_000_000 = 0.0105
	want := (1000.0*3.00 + 500.0*15.00) / 1_000_000
	if math.Abs(resp.Cost-want) > 1e-10 {
		t.Errorf("Cost = %f, want %f", resp.Cost, want)
	}
}

func TestComplete_UnknownModelZeroCost(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant",
			"model":   "unknown-model-xyz",
			"content": []map[string]any{{"type": "text", "text": "ok"}},
			"usage": map[string]any{
				"input_tokens":  100,
				"output_tokens": 50,
			},
		})
	})
	defer srv.Close()

	resp, err := c.Complete(context.Background(), models.CompletionRequest{
		Model:    "unknown-model-xyz",
		Messages: []models.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Cost != 0 {
		t.Errorf("Cost = %f, want 0 for unknown model", resp.Cost)
	}
}

func TestComplete_PrefixMatchCost(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant",
			"model":   "claude-sonnet-4-20250514-v2",
			"content": []map[string]any{{"type": "text", "text": "ok"}},
			"usage": map[string]any{
				"input_tokens":  1000,
				"output_tokens": 500,
			},
		})
	})
	defer srv.Close()

	resp, err := c.Complete(context.Background(), models.CompletionRequest{
		Model:    "claude-sonnet-4-20250514-v2",
		Messages: []models.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should match claude-sonnet-4-20250514 pricing: input=$3.00/M, output=$15.00/M
	want := (1000.0*3.00 + 500.0*15.00) / 1_000_000
	if math.Abs(resp.Cost-want) > 1e-10 {
		t.Errorf("Cost = %f, want %f (prefix match to claude-sonnet-4-20250514)", resp.Cost, want)
	}
}

func TestComplete_DefaultMaxTokens(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)

		maxTokens, ok := body["max_tokens"].(float64)
		if !ok {
			t.Fatal("expected max_tokens in request body")
		}
		if maxTokens != 4096 {
			t.Errorf("max_tokens = %v, want 4096 (default)", maxTokens)
		}

		json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "model": "m",
			"content": []map[string]any{{"type": "text", "text": "ok"}},
		})
	})
	defer srv.Close()

	_, err := c.Complete(context.Background(), models.CompletionRequest{
		Model:    "m",
		Messages: []models.ChatMessage{{Role: "user", Content: "hi"}},
		// MaxTokens not set — should default to 4096.
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --- Stream ---

func TestStream_BasicContent(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		// Anthropic stream format: event + data lines.
		chunks := []string{"Hello", " world", "!"}
		for _, chunk := range chunks {
			fmt.Fprintf(w, "event: content_block_delta\ndata: %s\n\n", mustJSON(map[string]any{
				"type":  "content_block_delta",
				"index": 0,
				"delta": map[string]any{"type": "text_delta", "text": chunk},
			}))
			flusher.Flush()
		}
		fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		flusher.Flush()
	})
	defer srv.Close()

	ch, err := c.Stream(context.Background(), models.CompletionRequest{
		Model:    "m",
		Messages: []models.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parts []string
	var gotDone bool
	for chunk := range ch {
		if chunk.Error != "" {
			t.Fatalf("unexpected error chunk: %s", chunk.Error)
		}
		if chunk.Done {
			gotDone = true
			continue
		}
		parts = append(parts, chunk.Content)
	}

	got := join(parts)
	if got != "Hello world!" {
		t.Errorf("stream content = %q, want %q", got, "Hello world!")
	}
	if !gotDone {
		t.Error("did not receive Done chunk")
	}
}

func TestStream_ErrorMidStream(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		fmt.Fprintf(w, "event: content_block_delta\ndata: %s\n\n", mustJSON(map[string]any{
			"type":  "content_block_delta",
			"index": 0,
			"delta": map[string]any{"type": "text_delta", "text": "partial"},
		}))
		flusher.Flush()
		// Abrupt close — no message_stop.
	})
	defer srv.Close()

	ch, err := c.Stream(context.Background(), models.CompletionRequest{
		Model:    "m",
		Messages: []models.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var gotContent bool
	for chunk := range ch {
		if chunk.Content == "partial" {
			gotContent = true
		}
	}
	if !gotContent {
		t.Error("did not receive partial content before close")
	}
}

func TestStream_ContextCancellation(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		fmt.Fprintf(w, "event: content_block_delta\ndata: %s\n\n", mustJSON(map[string]any{
			"type":  "content_block_delta",
			"index": 0,
			"delta": map[string]any{"type": "text_delta", "text": "start"},
		}))
		flusher.Flush()

		// Block until client disconnects.
		<-r.Context().Done()
	})
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())

	ch, err := c.Stream(ctx, models.CompletionRequest{
		Model:    "m",
		Messages: []models.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Read first chunk.
	select {
	case chunk := <-ch:
		if chunk.Content != "start" {
			t.Errorf("first chunk = %q, want %q", chunk.Content, "start")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for first chunk")
	}

	// Cancel and verify the channel closes promptly.
	cancel()

	select {
	case _, ok := <-ch:
		if ok {
			for range ch {
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("channel did not close after context cancellation")
	}
}

// --- ListModels ---

func TestListModels(t *testing.T) {
	c := anthropic.New("key")

	ms, err := c.ListModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ms) == 0 {
		t.Fatal("expected at least one model")
	}

	// Verify all entries have correct provider and pricing.
	found := make(map[string]bool)
	for _, m := range ms {
		if m.Provider != "anthropic" {
			t.Errorf("Provider = %q, want %q", m.Provider, "anthropic")
		}
		if m.CostPerMInput <= 0 {
			t.Errorf("model %s: CostPerMInput = %f, want > 0", m.ID, m.CostPerMInput)
		}
		if m.CostPerMOut <= 0 {
			t.Errorf("model %s: CostPerMOut = %f, want > 0", m.ID, m.CostPerMOut)
		}
		found[m.ID] = true
	}

	// Check for specific expected models.
	for _, want := range []string{"claude-sonnet-4-20250514", "claude-opus-4-5-20250527"} {
		if !found[want] {
			t.Errorf("expected model %q in list", want)
		}
	}
}

// --- Helpers ---

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func join(parts []string) string {
	result := ""
	for _, p := range parts {
		result += p
	}
	return result
}
