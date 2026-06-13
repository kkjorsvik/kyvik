package attachments

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

type OCRExtractor struct{}

func (e *OCRExtractor) CanExtract(contentType string, filename string) bool {
	// Only match image types
	switch contentType {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/tiff", "image/bmp":
		return true
	}
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".tiff", ".tif", ".bmp":
		return true
	}
	return false
}

func (e *OCRExtractor) Extract(data []byte) (string, error) {
	// tesseract reads from stdin with "-" and outputs to stdout with "stdout"
	cmd := exec.Command("tesseract", "stdin", "stdout", "--psm", "3")
	cmd.Stdin = bytes.NewReader(data)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("tesseract: %w (%s)", err, stderr.String())
	}

	return strings.TrimSpace(stdout.String()), nil
}

// OCRAvailable checks if tesseract is installed.
func OCRAvailable() bool {
	_, err := exec.LookPath("tesseract")
	return err == nil
}
