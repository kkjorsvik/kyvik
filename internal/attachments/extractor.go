package attachments

// Extractor converts file content to text.
type Extractor interface {
	CanExtract(contentType string, filename string) bool
	Extract(data []byte) (string, error)
}

// truncateText caps extracted text at maxBytes.
func truncateText(text string, maxBytes int) string {
	if len(text) <= maxBytes {
		return text
	}
	return text[:maxBytes] + "\n... [truncated, full content saved to workspace]"
}

const maxExtractedBytes = 100 * 1024 // 100KB cap
