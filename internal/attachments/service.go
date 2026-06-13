package attachments

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

// RawAttachment is what channel adapters provide before processing.
type RawAttachment struct {
	URL         string
	Filename    string
	ContentType string
	Size        int64
	AuthHeader  string // Optional (e.g., Slack Bearer token)
}

// Service handles downloading, saving, and classifying attachments.
type Service struct {
	workspaceFunc func(agentID string) (string, error)
	maxSizeFunc   func(agentID string) int64
	client        *http.Client
	extractors    []Extractor
}

// RegisterExtractor adds an extractor to the service. Extractors are tried in
// registration order; the first matching extractor wins.
func (s *Service) RegisterExtractor(ext Extractor) {
	s.extractors = append(s.extractors, ext)
}

// New creates an attachment service.
func New(workspaceFunc func(agentID string) (string, error), maxSizeFunc func(agentID string) int64) *Service {
	s := &Service{
		workspaceFunc: workspaceFunc,
		maxSizeFunc:   maxSizeFunc,
		client: &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return fmt.Errorf("too many redirects")
				}
				return nil
			},
		},
	}

	// Register extractors in priority order
	s.RegisterExtractor(&PDFExtractor{})
	s.RegisterExtractor(&DOCXExtractor{})
	s.RegisterExtractor(&XLSXExtractor{})
	s.RegisterExtractor(&HTMLExtractor{})
	if OCRAvailable() {
		slog.Info("tesseract OCR available for image text extraction")
		s.RegisterExtractor(&OCRExtractor{})
	} else {
		slog.Warn("tesseract not found, image OCR disabled — install with: sudo apt install tesseract-ocr")
	}

	return s
}

// Process downloads, saves, and classifies raw attachments.
// Returns successfully processed attachments. Failed individual attachments
// become metadata-only entries with error descriptions.
func (s *Service) Process(ctx context.Context, agentID string, raw []RawAttachment) ([]types.Attachment, error) {
	if len(raw) > types.MaxAttachmentsPerMsg {
		raw = raw[:types.MaxAttachmentsPerMsg]
	}

	workspace, err := s.workspaceFunc(agentID)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}

	attachDir := filepath.Join(workspace, "attachments")
	if err := os.MkdirAll(attachDir, 0o755); err != nil {
		return nil, fmt.Errorf("create attachments dir: %w", err)
	}

	maxSize := s.maxSizeFunc(agentID)
	if maxSize <= 0 {
		maxSize = 25 * 1024 * 1024 // 25 MB default
	}

	var result []types.Attachment
	for _, r := range raw {
		att := s.processOne(ctx, r, attachDir, maxSize)
		result = append(result, att)
	}
	return result, nil
}

func (s *Service) processOne(ctx context.Context, raw RawAttachment, attachDir string, maxSize int64) types.Attachment {
	filename := sanitizeFilename(raw.Filename)
	contentType := raw.ContentType

	// Check size before downloading
	if raw.Size > 0 && raw.Size > maxSize {
		slog.Warn("attachment too large, skipping download", "filename", filename, "size", raw.Size, "max", maxSize)
		return types.Attachment{
			Filename:    filename,
			ContentType: contentType,
			Size:        raw.Size,
		}
	}

	// Download
	data, err := s.download(ctx, raw.URL, raw.AuthHeader, maxSize)
	if err != nil {
		slog.Error("attachment download failed", "filename", filename, "error", err)
		return types.Attachment{
			Filename:    filename,
			ContentType: contentType,
			Size:        raw.Size,
		}
	}

	// Detect content type if missing
	if contentType == "" {
		contentType = detectContentType(filename, data)
	}

	size := int64(len(data))

	// Save to workspace
	savePath := filepath.Join(attachDir, fmt.Sprintf("%d_%s", time.Now().UnixNano(), filename))
	if err := os.WriteFile(savePath, data, 0o644); err != nil {
		slog.Error("failed to save attachment", "path", savePath, "error", err)
	}

	att := types.Attachment{
		Filename:    filename,
		ContentType: contentType,
		Size:        size,
	}

	// Try extractors first (precedence over inline classification)
	extracted := false
	for _, ext := range s.extractors {
		if ext.CanExtract(contentType, filename) {
			text, err := ext.Extract(data)
			if err != nil {
				slog.Warn("extraction failed, falling back", "filename", filename, "error", err)
				break
			}
			if text != "" {
				att.ExtractedText = truncateText(text, maxExtractedBytes)
				extracted = true
				if isInlineImage(contentType) {
					att.Data = data
				}
			}
			break
		}
	}

	if !extracted {
		if isInlineImage(contentType) || isInlineText(contentType, filename) {
			att.Data = data
		}
	}

	return att
}

func (s *Service) download(ctx context.Context, url, authHeader string, maxSize int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}

	// Read with size limit
	limited := io.LimitReader(resp.Body, maxSize+1) // +1 to detect over-limit
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxSize {
		return nil, fmt.Errorf("file exceeds max size (%d bytes)", maxSize)
	}

	return data, nil
}

// isInlineImage returns true for image types the LLM can process via vision.
func isInlineImage(contentType string) bool {
	switch contentType {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	}
	return false
}

// isInlineText returns true for text-based files whose content should be
// inlined into the LLM context.
func isInlineText(contentType, filename string) bool {
	// Check MIME type
	if strings.HasPrefix(contentType, "text/") {
		return true
	}
	switch contentType {
	case "application/json", "application/xml", "application/yaml",
		"application/x-yaml", "application/javascript", "application/typescript",
		"application/x-sh", "application/toml", "image/svg+xml":
		return true
	}

	// Check file extension as fallback
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".txt", ".md", ".json", ".csv", ".yaml", ".yml", ".xml", ".go",
		".py", ".js", ".ts", ".html", ".css", ".sql", ".sh", ".toml",
		".ini", ".log", ".svg", ".cfg", ".conf":
		// Note: .env files excluded to avoid inlining secrets into LLM context
		return true
	}
	return false
}

func detectContentType(filename string, data []byte) string {
	// Try extension first
	ext := filepath.Ext(filename)
	if ext != "" {
		if mt := mime.TypeByExtension(ext); mt != "" {
			return mt
		}
	}
	// Fall back to content sniffing
	return http.DetectContentType(data)
}

func sanitizeFilename(name string) string {
	name = filepath.Base(name)
	name = strings.ReplaceAll(name, "\x00", "")
	if name == "" || name == "." {
		name = "attachment"
	}
	return name
}
