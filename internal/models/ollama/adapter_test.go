package ollama_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/models"
	"github.com/kkjorsvik/kyvik/internal/models/ollama"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) (*ollama.Client, *httptest.Server) {
	t.Helper()
	srv := newHTTPServer(t, handler)
	c := ollama.New(
		ollama.WithBaseURL(srv.URL),
		ollama.WithMaxRetries(3),
		ollama.WithBaseBackoff(time.Millisecond),
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
	c := ollama.New()
	if got := c.Name(); got != "ollama" {
		t.Errorf("Name() = %q, want %q", got, "ollama")
	}
}

// --- Complete ---

func TestComplete_BasicResponse(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("path = %q, want %q", r.URL.Path, "/api/chat")
		}
		json.NewEncoder(w).Encode(map[string]any{
			"model":   "llama3",
			"message": map[string]any{"role": "assistant", "content": "Hello!"},
			"done":    true,
		})
	})
	defer srv.Close()

	resp, err := c.Complete(context.Background(), models.CompletionRequest{
		Model:    "llama3",
		Messages: []models.ChatMessage{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "Hello!" {
		t.Errorf("Content = %q, want %q", resp.Content, "Hello!")
	}
	if resp.Model != "llama3" {
		t.Errorf("Model = %q, want %q", resp.Model, "llama3")
	}
	if resp.StopReason != "end" {
		t.Errorf("StopReason = %q, want %q", resp.StopReason, "end")
	}
}

func TestComplete_TokenCounts(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"model":             "llama3",
			"message":           map[string]any{"role": "assistant", "content": "ok"},
			"done":              true,
			"prompt_eval_count": 25,
			"eval_count":        10,
		})
	})
	defer srv.Close()

	resp, err := c.Complete(context.Background(), models.CompletionRequest{
		Model:    "llama3",
		Messages: []models.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.TokensIn != 25 {
		t.Errorf("TokensIn = %d, want 25", resp.TokensIn)
	}
	if resp.TokensOut != 10 {
		t.Errorf("TokensOut = %d, want 10", resp.TokensOut)
	}
}

func TestComplete_ZeroCost(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"model":             "llama3",
			"message":           map[string]any{"role": "assistant", "content": "ok"},
			"done":              true,
			"prompt_eval_count": 1000,
			"eval_count":        500,
		})
	})
	defer srv.Close()

	resp, err := c.Complete(context.Background(), models.CompletionRequest{
		Model:    "llama3",
		Messages: []models.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Cost != 0 {
		t.Errorf("Cost = %f, want 0 (local models are free)", resp.Cost)
	}
}

func TestComplete_WithOptions(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)

		opts, ok := body["options"].(map[string]any)
		if !ok {
			t.Fatal("expected options in request body")
		}
		if temp, ok := opts["temperature"].(float64); !ok || temp != 0.7 {
			t.Errorf("temperature = %v, want 0.7", opts["temperature"])
		}
		if np, ok := opts["num_predict"].(float64); !ok || np != 100 {
			t.Errorf("num_predict = %v, want 100", opts["num_predict"])
		}

		// Verify stream is false for Complete.
		if stream, ok := body["stream"].(bool); !ok || stream {
			t.Errorf("stream = %v, want false", body["stream"])
		}

		json.NewEncoder(w).Encode(map[string]any{
			"model":   "llama3",
			"message": map[string]any{"role": "assistant", "content": "ok"},
			"done":    true,
		})
	})
	defer srv.Close()

	_, err := c.Complete(context.Background(), models.CompletionRequest{
		Model:       "llama3",
		Messages:    []models.ChatMessage{{Role: "user", Content: "hi"}},
		MaxTokens:   100,
		Temperature: 0.7,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestComplete_APIError(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		json.NewEncoder(w).Encode(map[string]any{
			"error": "invalid model format",
		})
	})
	defer srv.Close()

	_, err := c.Complete(context.Background(), models.CompletionRequest{
		Model:    "bad-model",
		Messages: []models.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	apiErr, ok := err.(*ollama.APIError)
	if !ok {
		t.Fatalf("error type = %T, want *APIError", err)
	}
	if apiErr.StatusCode != 400 {
		t.Errorf("StatusCode = %d, want 400", apiErr.StatusCode)
	}
}

func TestComplete_ConnectionRefused(t *testing.T) {
	c := ollama.New(
		ollama.WithBaseURL("http://127.0.0.1:1"), // Nothing listening.
		ollama.WithMaxRetries(1),
		ollama.WithBaseBackoff(time.Millisecond),
	)

	_, err := c.Complete(context.Background(), models.CompletionRequest{
		Model:    "llama3",
		Messages: []models.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !contains(err.Error(), "Ollama not running") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "Ollama not running")
	}
}

func TestComplete_ModelNotFound(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		json.NewEncoder(w).Encode(map[string]any{
			"error": "model 'nonexistent' not found",
		})
	})
	defer srv.Close()

	_, err := c.Complete(context.Background(), models.CompletionRequest{
		Model:    "nonexistent",
		Messages: []models.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !contains(err.Error(), "ollama pull") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "ollama pull")
	}
}

func TestComplete_Retry5xx(t *testing.T) {
	var count atomic.Int32
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		n := count.Add(1)
		if n == 1 {
			w.WriteHeader(502)
			fmt.Fprint(w, `{"error":"bad gateway"}`)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"model":   "llama3",
			"message": map[string]any{"role": "assistant", "content": "ok"},
			"done":    true,
		})
	})
	defer srv.Close()

	resp, err := c.Complete(context.Background(), models.CompletionRequest{
		Model:    "llama3",
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

func TestComplete_ContextCancellation(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
		json.NewEncoder(w).Encode(map[string]any{
			"model":   "llama3",
			"message": map[string]any{"role": "assistant", "content": "late"},
			"done":    true,
		})
	})
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	_, err := c.Complete(ctx, models.CompletionRequest{
		Model:    "llama3",
		Messages: []models.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

// --- Stream ---

func TestStream_BasicContent(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)

		chunks := []string{"Hello", " world", "!"}
		for _, chunk := range chunks {
			json.NewEncoder(w).Encode(map[string]any{
				"model":   "llama3",
				"message": map[string]any{"role": "assistant", "content": chunk},
				"done":    false,
			})
			flusher.Flush()
		}
		// Final chunk with done:true.
		json.NewEncoder(w).Encode(map[string]any{
			"model":             "llama3",
			"message":           map[string]any{"role": "assistant", "content": ""},
			"done":              true,
			"prompt_eval_count": 10,
			"eval_count":        5,
		})
		flusher.Flush()
	})
	defer srv.Close()

	ch, err := c.Stream(context.Background(), models.CompletionRequest{
		Model:    "llama3",
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

func TestStream_DoneWithMetrics(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)

		// Verify stream is true.
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if stream, ok := body["stream"].(bool); !ok || !stream {
			t.Errorf("stream = %v, want true", body["stream"])
		}

		json.NewEncoder(w).Encode(map[string]any{
			"model":   "llama3",
			"message": map[string]any{"role": "assistant", "content": "hi"},
			"done":    false,
		})
		flusher.Flush()

		json.NewEncoder(w).Encode(map[string]any{
			"model":             "llama3",
			"message":           map[string]any{"role": "assistant", "content": ""},
			"done":              true,
			"prompt_eval_count": 20,
			"eval_count":        15,
		})
		flusher.Flush()
	})
	defer srv.Close()

	ch, err := c.Stream(context.Background(), models.CompletionRequest{
		Model:    "llama3",
		Messages: []models.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var gotDone bool
	for chunk := range ch {
		if chunk.Done {
			gotDone = true
		}
	}
	if !gotDone {
		t.Error("did not receive Done chunk")
	}
}

func TestStream_ErrorMidStream(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)

		json.NewEncoder(w).Encode(map[string]any{
			"model":   "llama3",
			"message": map[string]any{"role": "assistant", "content": "partial"},
			"done":    false,
		})
		flusher.Flush()
		// Abrupt close — no done:true.
	})
	defer srv.Close()

	ch, err := c.Stream(context.Background(), models.CompletionRequest{
		Model:    "llama3",
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
		flusher := w.(http.Flusher)

		json.NewEncoder(w).Encode(map[string]any{
			"model":   "llama3",
			"message": map[string]any{"role": "assistant", "content": "start"},
			"done":    false,
		})
		flusher.Flush()

		// Block until client disconnects.
		<-r.Context().Done()
	})
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())

	ch, err := c.Stream(ctx, models.CompletionRequest{
		Model:    "llama3",
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
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Errorf("path = %q, want %q", r.URL.Path, "/api/tags")
		}
		json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{
				{"name": "llama3:latest"},
				{"name": "mistral:latest"},
			},
		})
	})
	defer srv.Close()

	ms, err := c.ListModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ms) != 2 {
		t.Fatalf("got %d models, want 2", len(ms))
	}
	if ms[0].ID != "llama3:latest" {
		t.Errorf("models[0].ID = %q, want %q", ms[0].ID, "llama3:latest")
	}
	if ms[0].Provider != "ollama" {
		t.Errorf("Provider = %q, want %q", ms[0].Provider, "ollama")
	}
	if ms[0].CostPerMInput != 0 {
		t.Errorf("CostPerMInput = %f, want 0", ms[0].CostPerMInput)
	}
	if ms[0].CostPerMOut != 0 {
		t.Errorf("CostPerMOut = %f, want 0", ms[0].CostPerMOut)
	}
}

func TestListModels_Empty(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{},
		})
	})
	defer srv.Close()

	ms, err := c.ListModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ms) != 0 {
		t.Errorf("got %d models, want 0", len(ms))
	}
}

// --- Embeddings ---

func TestEmbed_BasicResponse(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			t.Errorf("path = %q, want %q", r.URL.Path, "/api/embed")
		}

		// Verify single string input.
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if _, ok := body["input"].(string); !ok {
			t.Errorf("input type = %T, want string for single input", body["input"])
		}

		json.NewEncoder(w).Encode(map[string]any{
			"embeddings": [][]float64{{0.1, 0.2, 0.3}},
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
		// Verify array input.
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if _, ok := body["input"].([]any); !ok {
			t.Errorf("input type = %T, want []any for batch input", body["input"])
		}

		json.NewEncoder(w).Encode(map[string]any{
			"embeddings": [][]float64{
				{0.1, 0.2, 0.3},
				{0.4, 0.5, 0.6},
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
	if results[0][0] < 0.09 || results[0][0] > 0.11 {
		t.Errorf("results[0][0] = %f, want ~0.1", results[0][0])
	}
	if results[1][0] < 0.39 || results[1][0] > 0.41 {
		t.Errorf("results[1][0] = %f, want ~0.4", results[1][0])
	}
}

func TestEmbed_ModelAndDimensions(t *testing.T) {
	c := ollama.New(
		ollama.WithEmbeddingModel("mxbai-embed-large", 1024),
	)
	if got := c.Model(); got != "mxbai-embed-large" {
		t.Errorf("Model() = %q, want %q", got, "mxbai-embed-large")
	}
	if got := c.Dimensions(); got != 1024 {
		t.Errorf("Dimensions() = %d, want 1024", got)
	}
}

func TestEmbed_DefaultModelAndDimensions(t *testing.T) {
	c := ollama.New()
	if got := c.Model(); got != "nomic-embed-text" {
		t.Errorf("Model() = %q, want %q", got, "nomic-embed-text")
	}
	if got := c.Dimensions(); got != 768 {
		t.Errorf("Dimensions() = %d, want 768", got)
	}
}

// --- Ping ---

func TestPing_Success(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			t.Errorf("path = %q, want %q", r.URL.Path, "/")
		}
		fmt.Fprint(w, "Ollama is running")
	})
	defer srv.Close()

	if err := c.Ping(context.Background()); err != nil {
		t.Errorf("Ping() returned error: %v", err)
	}
}

func TestPing_Failure(t *testing.T) {
	c := ollama.New(
		ollama.WithBaseURL("http://127.0.0.1:1"), // Nothing listening.
	)

	err := c.Ping(context.Background())
	if err == nil {
		t.Fatal("expected error from Ping to unreachable host")
	}
	if !contains(err.Error(), "Ollama not running") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "Ollama not running")
	}
}

// --- Helpers ---

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

func join(parts []string) string {
	return strings.Join(parts, "")
}
