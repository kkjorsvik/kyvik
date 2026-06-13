package gemini_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kkjorsvik/kyvik/internal/models"
	"github.com/kkjorsvik/kyvik/internal/models/gemini"
)

func TestComplete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request path contains model and API key.
		if !strings.Contains(r.URL.Path, "generateContent") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("key") != "test-key" {
			t.Errorf("missing api key in query")
		}

		// Decode request body.
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)

		// Verify system instruction was extracted.
		if si, ok := req["systemInstruction"]; ok {
			siMap := si.(map[string]any)
			parts := siMap["parts"].([]any)
			p := parts[0].(map[string]any)
			if p["text"] != "You are a helpful assistant." {
				t.Errorf("system instruction = %q", p["text"])
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"candidates": []map[string]any{{
				"content": map[string]any{
					"role": "model",
					"parts": []map[string]any{
						{"text": "Hello! How can I help?"},
					},
				},
				"finishReason": "STOP",
			}},
			"usageMetadata": map[string]any{
				"promptTokenCount":     10,
				"candidatesTokenCount": 8,
				"totalTokenCount":      18,
			},
		})
	}))
	defer srv.Close()

	client := gemini.New("test-key",
		gemini.WithBaseURL(srv.URL),
		gemini.WithMaxRetries(1),
	)

	resp, err := client.Complete(context.Background(), models.CompletionRequest{
		Model: "gemini-2.5-flash",
		Messages: []models.ChatMessage{
			{Role: "system", Content: "You are a helpful assistant."},
			{Role: "user", Content: "Hello"},
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if resp.Content != "Hello! How can I help?" {
		t.Errorf("Content = %q", resp.Content)
	}
	if resp.StopReason != "end" {
		t.Errorf("StopReason = %q, want %q", resp.StopReason, "end")
	}
	if resp.TokensIn != 10 {
		t.Errorf("TokensIn = %d, want 10", resp.TokensIn)
	}
	if resp.TokensOut != 8 {
		t.Errorf("TokensOut = %d, want 8", resp.TokensOut)
	}
}

func TestCompleteWithToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"candidates": []map[string]any{{
				"content": map[string]any{
					"role": "model",
					"parts": []map[string]any{
						{"text": "Let me check the weather."},
						{"functionCall": map[string]any{
							"name": "get_weather",
							"args": map[string]any{"location": "Oslo"},
						}},
					},
				},
				"finishReason": "STOP",
			}},
			"usageMetadata": map[string]any{
				"promptTokenCount":     15,
				"candidatesTokenCount": 20,
				"totalTokenCount":      35,
			},
		})
	}))
	defer srv.Close()

	client := gemini.New("test-key",
		gemini.WithBaseURL(srv.URL),
		gemini.WithMaxRetries(1),
	)

	resp, err := client.Complete(context.Background(), models.CompletionRequest{
		Model: "gemini-2.5-flash",
		Messages: []models.ChatMessage{
			{Role: "user", Content: "What's the weather in Oslo?"},
		},
		Tools: []models.ToolDefinition{{
			Name:        "get_weather",
			Description: "Get weather for a location",
			Parameters:  map[string]any{"type": "object"},
		}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if resp.Content != "Let me check the weather." {
		t.Errorf("Content = %q", resp.Content)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("ToolCalls len = %d, want 1", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].Name != "get_weather" {
		t.Errorf("ToolCall name = %q", resp.ToolCalls[0].Name)
	}
}

func TestStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "streamGenerateContent") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		chunks := []string{
			`{"candidates":[{"content":{"role":"model","parts":[{"text":"Hello"}]}}]}`,
			`{"candidates":[{"content":{"role":"model","parts":[{"text":" world"}]}}]}`,
			`{"candidates":[{"content":{"role":"model","parts":[{"text":"!"}]},"finishReason":"STOP"}]}`,
		}
		for _, chunk := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", chunk)
			flusher.Flush()
		}
	}))
	defer srv.Close()

	client := gemini.New("test-key",
		gemini.WithBaseURL(srv.URL),
		gemini.WithMaxRetries(1),
	)

	ch, err := client.Stream(context.Background(), models.CompletionRequest{
		Model:    "gemini-2.5-flash",
		Messages: []models.ChatMessage{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var collected string
	var gotDone bool
	for chunk := range ch {
		if chunk.Error != "" {
			t.Fatalf("stream error: %s", chunk.Error)
		}
		if chunk.Done {
			gotDone = true
			continue
		}
		collected += chunk.Content
	}

	if collected != "Hello world!" {
		t.Errorf("collected = %q, want %q", collected, "Hello world!")
	}
	if !gotDone {
		t.Error("never received done chunk")
	}
}

func TestListModels(t *testing.T) {
	client := gemini.New("test-key")
	modelList, err := client.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(modelList) == 0 {
		t.Fatal("ListModels returned empty list")
	}

	// All should have provider "gemini".
	for _, m := range modelList {
		if m.Provider != "gemini" {
			t.Errorf("model %q: provider = %q, want %q", m.ID, m.Provider, "gemini")
		}
	}
}

func TestAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{
				"message": "API key not valid",
				"status":  "UNAUTHENTICATED",
			},
		})
	}))
	defer srv.Close()

	client := gemini.New("bad-key",
		gemini.WithBaseURL(srv.URL),
		gemini.WithMaxRetries(1),
	)

	_, err := client.Complete(context.Background(), models.CompletionRequest{
		Model:    "gemini-2.5-flash",
		Messages: []models.ChatMessage{{Role: "user", Content: "Hello"}},
	})
	if err == nil {
		t.Fatal("expected error for 401")
	}
	if !strings.Contains(err.Error(), "gemini") {
		t.Errorf("error = %q, want to contain 'gemini'", err.Error())
	}
}

func TestName(t *testing.T) {
	client := gemini.New("key")
	if client.Name() != "gemini" {
		t.Errorf("Name() = %q, want %q", client.Name(), "gemini")
	}
}
