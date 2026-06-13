package attachments

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

func TestProcess_ImageAttachment(t *testing.T) {
	// Mock HTTP server serving a PNG
	pngData := []byte{0x89, 0x50, 0x4E, 0x47} // PNG magic bytes
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(pngData)
	}))
	defer srv.Close()

	workspace := t.TempDir()
	svc := New(
		func(agentID string) (string, error) { return workspace, nil },
		func(agentID string) int64 { return 25 * 1024 * 1024 },
	)

	raw := []RawAttachment{{URL: srv.URL, Filename: "test.png", ContentType: "image/png", Size: 4}}
	result, err := svc.Process(context.Background(), "agent-1", raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(result))
	}
	if result[0].Data == nil {
		t.Error("image data should be included for vision models")
	}

	// Verify file saved to workspace
	files, _ := os.ReadDir(filepath.Join(workspace, "attachments"))
	if len(files) != 1 {
		t.Errorf("expected 1 file in attachments dir, got %d", len(files))
	}
}

func TestProcess_TextAttachment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("hello world"))
	}))
	defer srv.Close()

	workspace := t.TempDir()
	svc := New(
		func(agentID string) (string, error) { return workspace, nil },
		func(agentID string) int64 { return 25 * 1024 * 1024 },
	)

	raw := []RawAttachment{{URL: srv.URL, Filename: "readme.txt", ContentType: "text/plain", Size: 11}}
	result, err := svc.Process(context.Background(), "agent-1", raw)
	if err != nil {
		t.Fatal(err)
	}
	if string(result[0].Data) != "hello world" {
		t.Errorf("text content should be inlined, got %q", string(result[0].Data))
	}
}

func TestProcess_BinaryAttachment_MetadataOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.Write([]byte{0x00, 0x00, 0x00})
	}))
	defer srv.Close()

	workspace := t.TempDir()
	svc := New(
		func(agentID string) (string, error) { return workspace, nil },
		func(agentID string) int64 { return 25 * 1024 * 1024 },
	)

	raw := []RawAttachment{{URL: srv.URL, Filename: "video.mp4", ContentType: "video/mp4", Size: 3}}
	result, err := svc.Process(context.Background(), "agent-1", raw)
	if err != nil {
		t.Fatal(err)
	}
	if result[0].Data != nil {
		t.Error("binary files should have nil Data (metadata only)")
	}
	// File should still be saved to workspace
	files, _ := os.ReadDir(filepath.Join(workspace, "attachments"))
	if len(files) != 1 {
		t.Errorf("expected file saved to workspace")
	}
}

func TestProcess_OversizedAttachment_Skipped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(make([]byte, 100))
	}))
	defer srv.Close()

	workspace := t.TempDir()
	svc := New(
		func(agentID string) (string, error) { return workspace, nil },
		func(agentID string) int64 { return 50 }, // 50 bytes max
	)

	raw := []RawAttachment{{URL: srv.URL, Filename: "big.bin", Size: 200}}
	result, _ := svc.Process(context.Background(), "agent-1", raw)
	// Should return metadata-only entry with error description
	if len(result) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result))
	}
	if result[0].Data != nil {
		t.Error("oversized attachment should have nil data")
	}
}

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"../../etc/passwd", "passwd"},
		{"normal.txt", "normal.txt"},
		{"has spaces.pdf", "has spaces.pdf"},
		{"", "attachment"},
	}
	for _, tt := range tests {
		got := sanitizeFilename(tt.input)
		if got != tt.expected {
			t.Errorf("sanitize(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

// Compile-time check that types.Attachment is used correctly.
var _ []types.Attachment
