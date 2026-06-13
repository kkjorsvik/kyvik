package sandbox

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSecretsServer_StartAndRequestKnownKey(t *testing.T) {
	workspace := t.TempDir()
	// Create the tmp directory (normally done by Manager.Create).
	if err := os.MkdirAll(filepath.Join(workspace, "tmp"), 0o755); err != nil {
		t.Fatalf("create tmp dir: %v", err)
	}

	secrets := map[string]string{
		"github:token": "ghp_test123",
		"api_key":      "sk-secret456",
	}

	srv := NewSecretsServer(workspace, secrets)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Close()

	// Verify socket file exists with correct permissions.
	info, err := os.Stat(srv.SocketPath())
	if err != nil {
		t.Fatalf("socket file should exist: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("expected socket permissions 0600, got %o", perm)
	}

	// Request a known key.
	resp := requestSecret(t, srv.SocketPath(), "github:token")
	if resp.Error != "" {
		t.Errorf("expected no error, got %q", resp.Error)
	}
	if resp.Value != "ghp_test123" {
		t.Errorf("expected value 'ghp_test123', got %q", resp.Value)
	}

	// Request another known key.
	resp = requestSecret(t, srv.SocketPath(), "api_key")
	if resp.Error != "" {
		t.Errorf("expected no error, got %q", resp.Error)
	}
	if resp.Value != "sk-secret456" {
		t.Errorf("expected value 'sk-secret456', got %q", resp.Value)
	}
}

func TestSecretsServer_UnknownKey(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "tmp"), 0o755); err != nil {
		t.Fatalf("create tmp dir: %v", err)
	}

	srv := NewSecretsServer(workspace, map[string]string{
		"known_key": "value",
	})
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Close()

	resp := requestSecret(t, srv.SocketPath(), "unknown_key")
	if resp.Error == "" {
		t.Error("expected error for unknown key")
	}
	if resp.Value != "" {
		t.Errorf("expected empty value for unknown key, got %q", resp.Value)
	}
}

func TestSecretsServer_EmptyKey(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "tmp"), 0o755); err != nil {
		t.Fatalf("create tmp dir: %v", err)
	}

	srv := NewSecretsServer(workspace, map[string]string{
		"key": "value",
	})
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Close()

	resp := requestSecret(t, srv.SocketPath(), "")
	if resp.Error == "" {
		t.Error("expected error for empty key")
	}
}

func TestSecretsServer_UpdateSecrets(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "tmp"), 0o755); err != nil {
		t.Fatalf("create tmp dir: %v", err)
	}

	srv := NewSecretsServer(workspace, map[string]string{
		"key1": "value1",
	})
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Close()

	// Verify initial value.
	resp := requestSecret(t, srv.SocketPath(), "key1")
	if resp.Value != "value1" {
		t.Errorf("expected 'value1', got %q", resp.Value)
	}

	// Update secrets.
	srv.UpdateSecrets(map[string]string{
		"key1": "updated_value",
		"key2": "new_value",
	})

	// Verify updated value.
	resp = requestSecret(t, srv.SocketPath(), "key1")
	if resp.Value != "updated_value" {
		t.Errorf("expected 'updated_value', got %q", resp.Value)
	}

	// Verify new key.
	resp = requestSecret(t, srv.SocketPath(), "key2")
	if resp.Value != "new_value" {
		t.Errorf("expected 'new_value', got %q", resp.Value)
	}
}

func TestSecretsServer_CleanupAfterClose(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "tmp"), 0o755); err != nil {
		t.Fatalf("create tmp dir: %v", err)
	}

	srv := NewSecretsServer(workspace, map[string]string{
		"key": "value",
	})
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	socketPath := srv.SocketPath()

	// Verify socket exists before close.
	if _, err := os.Stat(socketPath); err != nil {
		t.Fatalf("socket should exist before close: %v", err)
	}

	// Close the server.
	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Verify socket file is removed after close.
	if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
		t.Error("socket file should be removed after Close()")
	}
}

func TestSecretsServer_DoubleCloseIsIdempotent(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "tmp"), 0o755); err != nil {
		t.Fatalf("create tmp dir: %v", err)
	}

	srv := NewSecretsServer(workspace, map[string]string{
		"key": "value",
	})
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// First close should succeed.
	if err := srv.Close(); err != nil {
		t.Fatalf("Close 1: %v", err)
	}

	// Second close should not panic or error.
	if err := srv.Close(); err != nil {
		t.Fatalf("Close 2: %v", err)
	}
}

func TestSecretsServer_MultipleConnections(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "tmp"), 0o755); err != nil {
		t.Fatalf("create tmp dir: %v", err)
	}

	secrets := map[string]string{
		"key1": "value1",
		"key2": "value2",
		"key3": "value3",
	}

	srv := NewSecretsServer(workspace, secrets)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Close()

	// Make multiple sequential requests (each on a new connection).
	for key, expected := range secrets {
		resp := requestSecret(t, srv.SocketPath(), key)
		if resp.Error != "" {
			t.Errorf("key %q: unexpected error %q", key, resp.Error)
		}
		if resp.Value != expected {
			t.Errorf("key %q: expected %q, got %q", key, expected, resp.Value)
		}
	}
}

func TestSecretsServer_SecretsCopyIsolation(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "tmp"), 0o755); err != nil {
		t.Fatalf("create tmp dir: %v", err)
	}

	original := map[string]string{
		"key": "original_value",
	}

	srv := NewSecretsServer(workspace, original)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Close()

	// Modify the original map — should NOT affect the server.
	original["key"] = "modified_value"

	resp := requestSecret(t, srv.SocketPath(), "key")
	if resp.Value != "original_value" {
		t.Errorf("expected 'original_value' (copy isolation), got %q", resp.Value)
	}
}

func TestSecretsServer_SocketPath(t *testing.T) {
	workspace := "/tmp/test-workspace"
	srv := NewSecretsServer(workspace, nil)
	expected := filepath.Join(workspace, "tmp", ".kyvik-secrets.sock")
	if srv.SocketPath() != expected {
		t.Errorf("expected socket path %q, got %q", expected, srv.SocketPath())
	}
}

// requestSecret connects to the secrets socket, sends a request, and returns the response.
func requestSecret(t *testing.T, socketPath, key string) SecretsResponse {
	t.Helper()

	conn, err := net.DialTimeout("unix", socketPath, 5*time.Second)
	if err != nil {
		t.Fatalf("connect to socket: %v", err)
	}
	defer conn.Close()

	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}

	// Send request.
	req := SecretsRequest{Key: key}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	// Read response.
	var resp SecretsResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	return resp
}
