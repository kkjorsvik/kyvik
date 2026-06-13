package openai_test

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
	"github.com/kkjorsvik/kyvik/internal/models/openai"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) (*openai.Client, *httptest.Server) {
	t.Helper()
	srv := newHTTPServer(t, handler)
	c := openai.New("test-key",
		openai.WithBaseURL(srv.URL),
		openai.WithMaxRetries(3),
		openai.WithBaseBackoff(time.Millisecond),
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
	c := openai.New("key")
	if got := c.Name(); got != "openai" {
		t.Errorf("Name() = %q, want %q", got, "openai")
	}
}

// --- Complete ---

func TestComplete_BasicResponse(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"model": "gpt-4o-mini",
			"choices": []map[string]any{
				{
					"finish_reason": "stop",
					"message":       map[string]any{"role": "assistant", "content": "Hello!"},
				},
			},
			"usage": map[string]any{
				"prompt_tokens":     10,
				"completion_tokens": 5,
			},
		})
	})
	defer srv.Close()

	resp, err := c.Complete(context.Background(), models.CompletionRequest{
		Model:    "gpt-4o-mini",
		Messages: []models.ChatMessage{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "Hello!" {
		t.Errorf("Content = %q, want %q", resp.Content, "Hello!")
	}
	if resp.Model != "gpt-4o-mini" {
		t.Errorf("Model = %q, want %q", resp.Model, "gpt-4o-mini")
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
			"model": "gpt-4o",
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
		Model:    "gpt-4o",
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
	apiErr, ok := err.(*openai.APIError)
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

// --- OpenAI-specific: Local cost calculation ---

func TestComplete_LocalCostCalculation(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"model": "gpt-4o-mini",
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": "ok"}},
			},
			"usage": map[string]any{
				"prompt_tokens":     1000,
				"completion_tokens": 500,
			},
		})
	})
	defer srv.Close()

	resp, err := c.Complete(context.Background(), models.CompletionRequest{
		Model:    "gpt-4o-mini",
		Messages: []models.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// gpt-4o-mini: input=$0.15/M, output=$0.60/M
	// cost = (1000 * 0.15 + 500 * 0.60) / 1_000_000 = (150 + 300) / 1_000_000 = 0.00045
	want := (1000.0*0.15 + 500.0*0.60) / 1_000_000
	if math.Abs(resp.Cost-want) > 1e-10 {
		t.Errorf("Cost = %f, want %f", resp.Cost, want)
	}
}

func TestComplete_UnknownModelZeroCost(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"model": "unknown-model-xyz",
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": "ok"}},
			},
			"usage": map[string]any{
				"prompt_tokens":     100,
				"completion_tokens": 50,
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
			"model": "gpt-4o-2024-08-06",
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": "ok"}},
			},
			"usage": map[string]any{
				"prompt_tokens":     1000,
				"completion_tokens": 500,
			},
		})
	})
	defer srv.Close()

	resp, err := c.Complete(context.Background(), models.CompletionRequest{
		Model:    "gpt-4o-2024-08-06",
		Messages: []models.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// gpt-4o-2024-08-06 should match gpt-4o pricing: input=$2.50/M, output=$10.00/M
	want := (1000.0*2.50 + 500.0*10.00) / 1_000_000
	if math.Abs(resp.Cost-want) > 1e-10 {
		t.Errorf("Cost = %f, want %f (prefix match to gpt-4o)", resp.Cost, want)
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
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %q, want %q", r.URL.Path, "/v1/models")
		}
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "gpt-4o", "owned_by": "openai"},
				{"id": "gpt-4o-mini", "owned_by": "openai"},
				{"id": "text-embedding-3-small", "owned_by": "openai"}, // Should be filtered out.
				{"id": "dall-e-3", "owned_by": "openai"},               // Should be filtered out.
			},
		})
	})
	defer srv.Close()

	ms, err := c.ListModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ms) != 2 {
		t.Fatalf("got %d models, want 2 (only chat models)", len(ms))
	}

	// Verify first model has pricing from local table.
	var found bool
	for _, m := range ms {
		if m.ID == "gpt-4o" {
			found = true
			if m.Provider != "openai" {
				t.Errorf("Provider = %q, want %q", m.Provider, "openai")
			}
			if m.CostPerMInput != 2.50 {
				t.Errorf("CostPerMInput = %f, want 2.50", m.CostPerMInput)
			}
			if m.CostPerMOut != 10.00 {
				t.Errorf("CostPerMOut = %f, want 10.00", m.CostPerMOut)
			}
		}
	}
	if !found {
		t.Error("gpt-4o not found in model list")
	}
}

// --- Embeddings ---

func TestEmbed_BasicResponse(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("path = %q, want %q", r.URL.Path, "/v1/embeddings")
		}
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{
					"embedding": []float64{0.1, 0.2, 0.3},
					"index":     0,
				},
			},
			"usage": map[string]any{
				"prompt_tokens": 5,
				"total_tokens":  5,
			},
		})
	})
	defer srv.Close()

	vec, err := c.Embed(context.Background(), "hello world")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vec) != 3 {
		t.Fatalf("got %d dimensions, want 3", len(vec))
	}
	if vec[0] < 0.09 || vec[0] > 0.11 {
		t.Errorf("vec[0] = %f, want ~0.1", vec[0])
	}
}

func TestEmbedBatch(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		// Return in reversed index order to verify sorting.
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"embedding": []float64{0.4, 0.5, 0.6}, "index": 1},
				{"embedding": []float64{0.1, 0.2, 0.3}, "index": 0},
			},
		})
	})
	defer srv.Close()

	results, err := c.EmbedBatch(context.Background(), []string{"first", "second"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	// Verify ordering by index — first result should be index 0.
	if results[0][0] < 0.09 || results[0][0] > 0.11 {
		t.Errorf("results[0][0] = %f, want ~0.1 (index 0 first)", results[0][0])
	}
	if results[1][0] < 0.39 || results[1][0] > 0.41 {
		t.Errorf("results[1][0] = %f, want ~0.4 (index 1 second)", results[1][0])
	}
}

func TestEmbed_ModelAndDimensions(t *testing.T) {
	c := openai.New("key",
		openai.WithEmbeddingModel("text-embedding-3-large", 3072),
	)
	if got := c.Model(); got != "text-embedding-3-large" {
		t.Errorf("Model() = %q, want %q", got, "text-embedding-3-large")
	}
	if got := c.Dimensions(); got != 3072 {
		t.Errorf("Dimensions() = %d, want 3072", got)
	}
}

func TestEmbed_DefaultModelAndDimensions(t *testing.T) {
	c := openai.New("key")
	if got := c.Model(); got != "text-embedding-3-small" {
		t.Errorf("Model() = %q, want %q", got, "text-embedding-3-small")
	}
	if got := c.Dimensions(); got != 1536 {
		t.Errorf("Dimensions() = %d, want 1536", got)
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
