package sandbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// secretsSocketName is the filename for the Unix socket inside the workspace tmp dir.
	secretsSocketName = ".kyvik-secrets.sock"

	// secretsConnDeadline is the per-connection read/write deadline.
	secretsConnDeadline = 5 * time.Second
)

// SecretsServer serves secrets to sandbox processes over a Unix domain socket.
// The parent process creates this server before spawning the sandbox binary,
// which connects to the socket to resolve secrets on demand. This avoids
// exposing secrets in environment variables (visible via /proc/*/environ).
type SecretsServer struct {
	socketPath string
	secrets    map[string]string
	mu         sync.RWMutex
	listener   net.Listener
	done       chan struct{}
	wg         sync.WaitGroup
}

// NewSecretsServer creates a new SecretsServer that will listen on
// {workspace}/tmp/.kyvik-secrets.sock. Call Start() to begin accepting connections.
func NewSecretsServer(workspace string, secrets map[string]string) *SecretsServer {
	// Copy the secrets map to avoid aliasing.
	secretsCopy := make(map[string]string, len(secrets))
	maps.Copy(secretsCopy, secrets)

	return &SecretsServer{
		socketPath: filepath.Join(workspace, "tmp", secretsSocketName),
		secrets:    secretsCopy,
		done:       make(chan struct{}),
	}
}

// SocketPath returns the path to the Unix socket.
func (s *SecretsServer) SocketPath() string {
	return s.socketPath
}

// Start begins listening on the Unix socket and accepting connections.
// It returns an error if the socket cannot be created.
func (s *SecretsServer) Start() error {
	// Ensure the parent directory exists.
	dir := filepath.Dir(s.socketPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create socket directory: %w", err)
	}

	// Remove any stale socket file.
	_ = os.Remove(s.socketPath)

	ln, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("listen on unix socket %s: %w", s.socketPath, err)
	}

	// Set socket file permissions to 0600 (owner-only).
	if err := os.Chmod(s.socketPath, 0o600); err != nil {
		ln.Close()
		_ = os.Remove(s.socketPath)
		return fmt.Errorf("chmod socket: %w", err)
	}

	s.listener = ln

	s.wg.Add(1)
	go s.acceptLoop()

	slog.Debug("secrets server started", "socket", s.socketPath)
	return nil
}

// UpdateSecrets replaces the server's secret map. This is thread-safe and can
// be called while the server is running (e.g., for secret rotation).
func (s *SecretsServer) UpdateSecrets(secrets map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.secrets = make(map[string]string, len(secrets))
	maps.Copy(s.secrets, secrets)
}

// Close stops the server, closes the listener, and removes the socket file.
func (s *SecretsServer) Close() error {
	// Signal the accept loop to stop.
	select {
	case <-s.done:
		// Already closed.
		return nil
	default:
		close(s.done)
	}

	// Close the listener to unblock Accept().
	var closeErr error
	if s.listener != nil {
		closeErr = s.listener.Close()
	}

	// Wait for the accept loop to finish.
	s.wg.Wait()

	// Remove the socket file.
	_ = os.Remove(s.socketPath)

	slog.Debug("secrets server stopped", "socket", s.socketPath)
	return closeErr
}

// acceptLoop accepts connections until the server is closed.
func (s *SecretsServer) acceptLoop() {
	defer s.wg.Done()

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			// Check if we're shutting down.
			select {
			case <-s.done:
				return
			default:
			}
			// Log transient errors and continue.
			if !errors.Is(err, net.ErrClosed) {
				slog.Warn("secrets server accept error", "error", err)
			}
			return
		}

		// Handle each connection synchronously — the sandbox binary makes
		// one request per connection, so there's no need for concurrent handling.
		// This also simplifies the protocol (no multiplexing).
		s.handleConnection(conn)
	}
}

// handleConnection reads a SecretsRequest, looks up the key, and writes a SecretsResponse.
func (s *SecretsServer) handleConnection(conn net.Conn) {
	defer conn.Close()

	// Set a deadline for the entire exchange.
	if err := conn.SetDeadline(time.Now().Add(secretsConnDeadline)); err != nil {
		slog.Warn("secrets server: failed to set deadline", "error", err)
		return
	}

	// Read request.
	var req SecretsRequest
	decoder := json.NewDecoder(conn)
	if err := decoder.Decode(&req); err != nil {
		slog.Warn("secrets server: failed to decode request", "error", err)
		writeResponse(conn, SecretsResponse{Error: "invalid request"})
		return
	}

	if req.Key == "" {
		writeResponse(conn, SecretsResponse{Error: "empty key"})
		return
	}

	// Look up the secret.
	s.mu.RLock()
	value, ok := s.secrets[req.Key]
	s.mu.RUnlock()

	if !ok {
		writeResponse(conn, SecretsResponse{Error: fmt.Sprintf("secret not found: %s", req.Key)})
		return
	}

	writeResponse(conn, SecretsResponse{Value: value})
}

// writeResponse encodes a SecretsResponse as JSON to the connection.
func writeResponse(conn net.Conn, resp SecretsResponse) {
	if err := json.NewEncoder(conn).Encode(resp); err != nil {
		slog.Warn("secrets server: failed to write response", "error", err)
	}
}
