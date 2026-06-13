package openrouter_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kkjorsvik/kyvik/internal/models/openrouter"
)

func newTestManagementClient(t *testing.T, handler http.HandlerFunc) (*openrouter.ManagementClient, *httptest.Server) {
	t.Helper()
	srv := newHTTPServer(t, handler)
	mc := openrouter.NewManagementClient("prov-key",
		openrouter.WithManagementBaseURL(srv.URL),
	)
	return mc, srv
}

// --- CreateKey ---

func TestCreateKey_Success(t *testing.T) {
	mc, srv := newTestManagementClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/api/v1/keys" {
			t.Errorf("path = %q, want /api/v1/keys", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer prov-key" {
			t.Errorf("Authorization = %q, want %q", got, "Bearer prov-key")
		}

		var req openrouter.CreateKeyRequest
		json.NewDecoder(r.Body).Decode(&req)
		if req.Name != "kyvik-agent-1" {
			t.Errorf("Name = %q, want %q", req.Name, "kyvik-agent-1")
		}

		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"key":  "sk-or-new-key",
				"hash": "abc123",
				"name": req.Name,
			},
		})
	})
	defer srv.Close()

	resp, err := mc.CreateKey(context.Background(), openrouter.CreateKeyRequest{
		Name:  "kyvik-agent-1",
		Limit: 10.0,
		Label: "test-agent",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Key != "sk-or-new-key" {
		t.Errorf("Key = %q, want %q", resp.Key, "sk-or-new-key")
	}
	if resp.Hash != "abc123" {
		t.Errorf("Hash = %q, want %q", resp.Hash, "abc123")
	}
}

func TestCreateKey_WithLimit(t *testing.T) {
	mc, srv := newTestManagementClient(t, func(w http.ResponseWriter, r *http.Request) {
		var req openrouter.CreateKeyRequest
		json.NewDecoder(r.Body).Decode(&req)
		if req.Limit != 25.0 {
			t.Errorf("Limit = %f, want 25.0", req.Limit)
		}

		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"key":  "sk-or-key",
				"hash": "h1",
				"name": req.Name,
			},
		})
	})
	defer srv.Close()

	_, err := mc.CreateKey(context.Background(), openrouter.CreateKeyRequest{
		Name:  "test",
		Limit: 25.0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCreateKey_APIError(t *testing.T) {
	mc, srv := newTestManagementClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"forbidden"}`))
	})
	defer srv.Close()

	_, err := mc.CreateKey(context.Background(), openrouter.CreateKeyRequest{Name: "test"})
	if err == nil {
		t.Fatal("expected error")
	}
}

// --- DeleteKey ---

func TestDeleteKey_Success(t *testing.T) {
	mc, srv := newTestManagementClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %q, want DELETE", r.Method)
		}
		if r.URL.Path != "/api/v1/keys/hash123" {
			t.Errorf("path = %q, want /api/v1/keys/hash123", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	})
	defer srv.Close()

	err := mc.DeleteKey(context.Background(), "hash123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDeleteKey_NotFound(t *testing.T) {
	mc, srv := newTestManagementClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"not found"}`))
	})
	defer srv.Close()

	err := mc.DeleteKey(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected error")
	}
}

// --- ListKeys ---

func TestListKeys_Success(t *testing.T) {
	mc, srv := newTestManagementClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		if r.URL.Path != "/api/v1/keys" {
			t.Errorf("path = %q, want /api/v1/keys", r.URL.Path)
		}

		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{
					"hash":      "h1",
					"name":      "kyvik-agent-1",
					"label":     "Agent One",
					"limit":     10.0,
					"usage":     2.5,
					"is_active": true,
				},
				{
					"hash":      "h2",
					"name":      "kyvik-agent-2",
					"label":     "Agent Two",
					"limit":     20.0,
					"usage":     0.0,
					"is_active": true,
				},
			},
		})
	})
	defer srv.Close()

	keys, err := mc.ListKeys(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("got %d keys, want 2", len(keys))
	}
	if keys[0].Hash != "h1" {
		t.Errorf("keys[0].Hash = %q, want %q", keys[0].Hash, "h1")
	}
	if keys[0].Name != "kyvik-agent-1" {
		t.Errorf("keys[0].Name = %q, want %q", keys[0].Name, "kyvik-agent-1")
	}
	if keys[0].Usage != 2.5 {
		t.Errorf("keys[0].Usage = %f, want 2.5", keys[0].Usage)
	}
	if !keys[0].IsActive {
		t.Error("keys[0].IsActive = false, want true")
	}
}

func TestListKeys_Empty(t *testing.T) {
	mc, srv := newTestManagementClient(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{},
		})
	})
	defer srv.Close()

	keys, err := mc.ListKeys(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("got %d keys, want 0", len(keys))
	}
}
