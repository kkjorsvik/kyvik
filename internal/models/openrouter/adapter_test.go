package openrouter_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/models"
	"github.com/kkjorsvik/kyvik/internal/models/openrouter"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) (*openrouter.Client, *httptest.Server) {
	t.Helper()
	srv := newHTTPServer(t, handler)
	c := openrouter.New("test-key",
		openrouter.WithBaseURL(srv.URL),
		openrouter.WithMaxRetries(3),
		openrouter.WithBaseBackoff(time.Millisecond),
	)
	return c, srv
}

// --- Name ---

func TestName(t *testing.T) {
	c := openrouter.New("key")
	if got := c.Name(); got != "openrouter" {
		t.Errorf("Name() = %q, want %q", got, "openrouter")
	}
}

// --- Complete ---

func TestComplete_BasicResponse(t *testing.T) {
	cost := 0.0042
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"model": "test/model",
			"choices": []map[string]any{
				{
					"finish_reason": "stop",
					"message":       map[string]any{"role": "assistant", "content": "Hello!"},
				},
			},
			"usage": map[string]any{
				"prompt_tokens":     10,
				"completion_tokens": 5,
				"total_cost":        cost,
			},
		})
	})
	defer srv.Close()

	resp, err := c.Complete(context.Background(), models.CompletionRequest{
		Model:    "test/model",
		Messages: []models.ChatMessage{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "Hello!" {
		t.Errorf("Content = %q, want %q", resp.Content, "Hello!")
	}
	if resp.Model != "test/model" {
		t.Errorf("Model = %q, want %q", resp.Model, "test/model")
	}
	if resp.TokensIn != 10 {
		t.Errorf("TokensIn = %d, want 10", resp.TokensIn)
	}
	if resp.TokensOut != 5 {
		t.Errorf("TokensOut = %d, want 5", resp.TokensOut)
	}
	if resp.Cost != cost {
		t.Errorf("Cost = %f, want %f", resp.Cost, cost)
	}
	if resp.StopReason != "end" {
		t.Errorf("StopReason = %q, want %q", resp.StopReason, "end")
	}
}

func TestComplete_WithToolCalls(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"model": "test/model",
			"choices": []map[string]any{
				{
					"finish_reason": "tool_calls",
					"message": map[string]any{
						"role":    "assistant",
						"content": "",
						"tool_calls": []map[string]any{
							{
								"id":   "call_123",
								"type": "function",
								"function": map[string]any{
									"name":      "get_weather",
									"arguments": `{"location":"London"}`,
								},
							},
						},
					},
				},
			},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5},
		})
	})
	defer srv.Close()

	resp, err := c.Complete(context.Background(), models.CompletionRequest{
		Model:    "test/model",
		Messages: []models.ChatMessage{{Role: "user", Content: "weather?"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StopReason != "tool_use" {
		t.Errorf("StopReason = %q, want %q", resp.StopReason, "tool_use")
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc.ID != "call_123" {
		t.Errorf("ToolCall.ID = %q, want %q", tc.ID, "call_123")
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
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want %q", got, "Bearer test-key")
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want %q", got, "application/json")
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": "ok"}},
			},
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
		if tool["type"] != "function" {
			t.Errorf("tool type = %v, want %q", tool["type"], "function")
		}
		fn := tool["function"].(map[string]any)
		if fn["name"] != "search" {
			t.Errorf("function name = %v, want %q", fn["name"], "search")
		}

		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": "ok"}},
			},
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

func TestComplete_APIError(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{
				"message": "bad request",
				"type":    "invalid_request_error",
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
	apiErr, ok := err.(*openrouter.APIError)
	if !ok {
		t.Fatalf("error type = %T, want *APIError", err)
	}
	if apiErr.StatusCode != 400 {
		t.Errorf("StatusCode = %d, want 400", apiErr.StatusCode)
	}
	if apiErr.Message != "bad request" {
		t.Errorf("Message = %q, want %q", apiErr.Message, "bad request")
	}
}

func TestComplete_NoRetryOn4xx(t *testing.T) {
	var count atomic.Int32
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		w.WriteHeader(401)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"message": "unauthorized"},
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
			fmt.Fprint(w, `{"error":{"message":"rate limited"}}`)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": "ok"}},
			},
		})
	})
	defer srv.Close()

	// Use short retry client to avoid slow tests.
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

func TestComplete_Retry5xx(t *testing.T) {
	var count atomic.Int32
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		n := count.Add(1)
		if n == 1 {
			w.WriteHeader(502)
			fmt.Fprint(w, `{"error":{"message":"bad gateway"}}`)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": "ok"}},
			},
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
		fmt.Fprint(w, `{"error":{"message":"rate limited"}}`)
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
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": "late"}},
			},
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

func TestComplete_NoCostField(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": "ok"}},
			},
			"usage": map[string]any{
				"prompt_tokens":     10,
				"completion_tokens": 5,
			},
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
	if resp.Cost != 0 {
		t.Errorf("Cost = %f, want 0", resp.Cost)
	}
}

// --- Per-Agent Key Resolution ---

func TestComplete_UsesResolvedAgentKey(t *testing.T) {
	srv := newHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer agent-specific-key" {
			t.Errorf("Authorization = %q, want %q", got, "Bearer agent-specific-key")
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": "ok"}},
			},
		})
	}))
	defer srv.Close()

	c := openrouter.New("default-key",
		openrouter.WithBaseURL(srv.URL),
		openrouter.WithMaxRetries(1),
		openrouter.WithKeyResolver(func(ctx context.Context, agentID string) (string, error) {
			if agentID == "agent-1" {
				return "agent-specific-key", nil
			}
			return "", fmt.Errorf("not found")
		}),
	)

	ctx := models.WithAgentID(context.Background(), "agent-1")
	_, err := c.Complete(ctx, models.CompletionRequest{
		Model:    "m",
		Messages: []models.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestComplete_FallsBackToDefaultKey(t *testing.T) {
	srv := newHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer default-key" {
			t.Errorf("Authorization = %q, want %q", got, "Bearer default-key")
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": "ok"}},
			},
		})
	}))
	defer srv.Close()

	c := openrouter.New("default-key",
		openrouter.WithBaseURL(srv.URL),
		openrouter.WithMaxRetries(1),
		openrouter.WithKeyResolver(func(ctx context.Context, agentID string) (string, error) {
			return "", fmt.Errorf("not found")
		}),
	)

	ctx := models.WithAgentID(context.Background(), "agent-unknown")
	_, err := c.Complete(ctx, models.CompletionRequest{
		Model:    "m",
		Messages: []models.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestComplete_NoAgentIDUsesDefaultKey(t *testing.T) {
	srv := newHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer default-key" {
			t.Errorf("Authorization = %q, want %q", got, "Bearer default-key")
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": "ok"}},
			},
		})
	}))
	defer srv.Close()

	resolverCalled := false
	c := openrouter.New("default-key",
		openrouter.WithBaseURL(srv.URL),
		openrouter.WithMaxRetries(1),
		openrouter.WithKeyResolver(func(ctx context.Context, agentID string) (string, error) {
			resolverCalled = true
			return "should-not-use", nil
		}),
	)

	// No agent ID in context — resolver should not be called
	_, err := c.Complete(context.Background(), models.CompletionRequest{
		Model:    "m",
		Messages: []models.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolverCalled {
		t.Error("resolver was called even though no agent ID was in context")
	}
}

// --- Stream ---

func TestStream_BasicContent(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		chunks := []string{"Hello", " world", "!"}
		for _, chunk := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", mustJSON(map[string]any{
				"choices": []map[string]any{
					{"delta": map[string]any{"content": chunk}},
				},
			}))
			flusher.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
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

		fmt.Fprintf(w, "data: %s\n\n", mustJSON(map[string]any{
			"choices": []map[string]any{
				{"delta": map[string]any{"content": "partial"}},
			},
		}))
		flusher.Flush()
		// Abrupt close — no [DONE].
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

		// Send one chunk, then block.
		fmt.Fprintf(w, "data: %s\n\n", mustJSON(map[string]any{
			"choices": []map[string]any{
				{"delta": map[string]any{"content": "start"}},
			},
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
			// Might get one more chunk, that's fine. Drain.
			for range ch {
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("channel did not close after context cancellation")
	}
}

// --- ListModels ---

func TestListModels(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/models" {
			t.Errorf("path = %q, want %q", r.URL.Path, "/api/v1/models")
		}
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{
					"id":             "openai/gpt-4",
					"name":           "GPT-4",
					"context_length": 8192,
					"pricing": map[string]any{
						"prompt":     "0.00003",
						"completion": "0.00006",
					},
				},
			},
		})
	})
	defer srv.Close()

	ms, err := c.ListModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ms) != 1 {
		t.Fatalf("got %d models, want 1", len(ms))
	}
	m := ms[0]
	if m.ID != "openai/gpt-4" {
		t.Errorf("ID = %q, want %q", m.ID, "openai/gpt-4")
	}
	if m.Name != "GPT-4" {
		t.Errorf("Name = %q, want %q", m.Name, "GPT-4")
	}
	if m.ContextSize != 8192 {
		t.Errorf("ContextSize = %d, want 8192", m.ContextSize)
	}
	if m.Provider != "openrouter" {
		t.Errorf("Provider = %q, want %q", m.Provider, "openrouter")
	}
	// 0.00003 * 1_000_000 = 30.0
	if m.CostPerMInput != 30.0 {
		t.Errorf("CostPerMInput = %f, want 30.0", m.CostPerMInput)
	}
	// 0.00006 * 1_000_000 = 60.0
	if m.CostPerMOut != 60.0 {
		t.Errorf("CostPerMOut = %f, want 60.0", m.CostPerMOut)
	}
}

func TestListModels_PricingParseError(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{
					"id":             "test/model",
					"name":           "Test",
					"context_length": 4096,
					"pricing": map[string]any{
						"prompt":     "not-a-number",
						"completion": "",
					},
				},
			},
		})
	})
	defer srv.Close()

	ms, err := c.ListModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ms) != 1 {
		t.Fatalf("got %d models, want 1", len(ms))
	}
	if ms[0].CostPerMInput != 0 {
		t.Errorf("CostPerMInput = %f, want 0", ms[0].CostPerMInput)
	}
	if ms[0].CostPerMOut != 0 {
		t.Errorf("CostPerMOut = %f, want 0", ms[0].CostPerMOut)
	}
}

// --- Provider Preferences ---

func TestComplete_ProviderPrefsWhenToolsPresent(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)

		// Verify provider prefs are included in the request
		provider, ok := body["provider"].(map[string]any)
		if !ok {
			t.Fatal("expected 'provider' field in request body when tools are present and ProviderIgnore is set")
		}

		ignore, ok := provider["ignore"].([]any)
		if !ok || len(ignore) == 0 {
			t.Fatal("expected 'ignore' list in provider prefs")
		}
		if ignore[0] != "Google" {
			t.Errorf("ignore[0] = %v, want %q", ignore[0], "Google")
		}
		if ignore[1] != "Phala" {
			t.Errorf("ignore[1] = %v, want %q", ignore[1], "Phala")
		}

		allowFallbacks, ok := provider["allow_fallbacks"].(bool)
		if !ok || !allowFallbacks {
			t.Errorf("allow_fallbacks = %v, want true", provider["allow_fallbacks"])
		}

		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": "ok"}},
			},
		})
	})
	defer srv.Close()

	_, err := c.Complete(context.Background(), models.CompletionRequest{
		Model:    "deepseek/deepseek-v3.2",
		Messages: []models.ChatMessage{{Role: "user", Content: "hi"}},
		Tools: []models.ToolDefinition{
			{Name: "search", Description: "Search", Parameters: map[string]any{"type": "object"}},
		},
		ProviderIgnore: []string{"Google", "Phala"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestComplete_NoProviderPrefsWithoutTools(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)

		// Provider prefs should NOT be included when no tools
		if _, ok := body["provider"]; ok {
			t.Error("provider prefs should not be included when no tools are present")
		}

		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": "ok"}},
			},
		})
	})
	defer srv.Close()

	_, err := c.Complete(context.Background(), models.CompletionRequest{
		Model:          "deepseek/deepseek-v3.2",
		Messages:       []models.ChatMessage{{Role: "user", Content: "hi"}},
		ProviderIgnore: []string{"Google"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestComplete_NoProviderPrefsWithoutIgnoreList(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)

		// Provider prefs should NOT be included when no ignore list
		if _, ok := body["provider"]; ok {
			t.Error("provider prefs should not be included when ProviderIgnore is empty")
		}

		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": "ok"}},
			},
		})
	})
	defer srv.Close()

	_, err := c.Complete(context.Background(), models.CompletionRequest{
		Model:    "deepseek/deepseek-v3.2",
		Messages: []models.ChatMessage{{Role: "user", Content: "hi"}},
		Tools: []models.ToolDefinition{
			{Name: "search", Description: "Search", Parameters: map[string]any{"type": "object"}},
		},
		// No ProviderIgnore
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
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
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
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
