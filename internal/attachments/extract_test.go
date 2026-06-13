package attachments

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

// --- HTMLExtractor ---

func TestHTMLExtractor_CanExtract(t *testing.T) {
	e := &HTMLExtractor{}

	cases := []struct {
		contentType string
		filename    string
		want        bool
	}{
		{"text/html", "", true},
		{"text/html; charset=utf-8", "", false}, // exact match only
		{"", "page.html", true},
		{"", "page.htm", true},
		{"", "page.HTML", true},
		{"", "doc.pdf", false},
		{"application/pdf", "", false},
	}

	for _, c := range cases {
		got := e.CanExtract(c.contentType, c.filename)
		if got != c.want {
			t.Errorf("CanExtract(%q, %q) = %v, want %v", c.contentType, c.filename, got, c.want)
		}
	}
}

func TestHTMLExtractor_Extract(t *testing.T) {
	e := &HTMLExtractor{}

	html := []byte(`<!DOCTYPE html>
<html>
<head>
  <title>Test Page</title>
  <style>body { color: red; }</style>
  <script>alert("should not appear")</script>
</head>
<body>
  <h1>Hello World</h1>
  <p>This is a test paragraph.</p>
</body>
</html>`)

	text, err := e.Extract(html)
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}

	// Script tag content must be stripped
	if strings.Contains(text, "alert") {
		t.Errorf("script content leaked into extracted text: %q", text)
	}
	if strings.Contains(text, "color: red") {
		t.Errorf("style content leaked into extracted text: %q", text)
	}

	// Visible text must be present
	if !strings.Contains(text, "Hello World") {
		t.Errorf("expected 'Hello World' in extracted text, got: %q", text)
	}
	if !strings.Contains(text, "This is a test paragraph.") {
		t.Errorf("expected paragraph text in extracted text, got: %q", text)
	}
}

// --- DOCXExtractor ---

func TestDOCXExtractor_CanExtract(t *testing.T) {
	e := &DOCXExtractor{}

	cases := []struct {
		contentType string
		filename    string
		want        bool
	}{
		{"application/vnd.openxmlformats-officedocument.wordprocessingml.document", "", true},
		{"", "report.docx", true},
		{"", "report.DOCX", true},
		{"", "report.doc", false},
		{"application/pdf", "", false},
	}

	for _, c := range cases {
		got := e.CanExtract(c.contentType, c.filename)
		if got != c.want {
			t.Errorf("CanExtract(%q, %q) = %v, want %v", c.contentType, c.filename, got, c.want)
		}
	}
}

func TestDOCXExtractor_Extract(t *testing.T) {
	e := &DOCXExtractor{}

	// Build a minimal DOCX (zip) with word/document.xml containing known text.
	const knownText = "Hello from DOCX"
	docXML := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>
    <w:p>
      <w:r><w:t>` + knownText + `</w:t></w:r>
    </w:p>
  </w:body>
</w:document>`

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatalf("create zip entry: %v", err)
	}
	if _, err := f.Write([]byte(docXML)); err != nil {
		t.Fatalf("write xml: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}

	text, err := e.Extract(buf.Bytes())
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	if !strings.Contains(text, knownText) {
		t.Errorf("expected %q in extracted text, got: %q", knownText, text)
	}
}

// --- PDFExtractor ---

func TestPDFExtractor_CanExtract(t *testing.T) {
	e := &PDFExtractor{}

	cases := []struct {
		contentType string
		filename    string
		want        bool
	}{
		{"application/pdf", "", true},
		{"", "document.pdf", true},
		{"", "document.PDF", true},
		{"", "document.docx", false},
		{"text/plain", "", false},
	}

	for _, c := range cases {
		got := e.CanExtract(c.contentType, c.filename)
		if got != c.want {
			t.Errorf("CanExtract(%q, %q) = %v, want %v", c.contentType, c.filename, got, c.want)
		}
	}
}

// --- XLSXExtractor ---

func TestXLSXExtractor_CanExtract(t *testing.T) {
	e := &XLSXExtractor{}

	cases := []struct {
		contentType string
		filename    string
		want        bool
	}{
		{"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "", true},
		{"", "data.xlsx", true},
		{"", "data.XLSX", true},
		{"", "data.xls", false},
		{"application/pdf", "", false},
	}

	for _, c := range cases {
		got := e.CanExtract(c.contentType, c.filename)
		if got != c.want {
			t.Errorf("CanExtract(%q, %q) = %v, want %v", c.contentType, c.filename, got, c.want)
		}
	}
}

// --- OCRExtractor ---

func TestOCRExtractor_CanExtract(t *testing.T) {
	e := &OCRExtractor{}

	cases := []struct {
		contentType string
		filename    string
		want        bool
	}{
		{"image/png", "", true},
		{"image/jpeg", "", true},
		{"image/gif", "", true},
		{"image/webp", "", true},
		{"image/tiff", "", true},
		{"image/bmp", "", true},
		{"", "scan.png", true},
		{"", "photo.jpg", true},
		{"", "photo.jpeg", true},
		{"", "photo.JPEG", true},
		{"", "doc.pdf", false},
		{"application/pdf", "", false},
	}

	for _, c := range cases {
		got := e.CanExtract(c.contentType, c.filename)
		if got != c.want {
			t.Errorf("CanExtract(%q, %q) = %v, want %v", c.contentType, c.filename, got, c.want)
		}
	}
}

func TestOCRExtractor_Extract_SkipIfNoTesseract(t *testing.T) {
	if !OCRAvailable() {
		t.Skip("tesseract not installed, skipping OCR extract test")
	}
	// If tesseract is available, just verify Extract doesn't panic on empty-ish input.
	e := &OCRExtractor{}
	// Pass a 1x1 white PNG (minimal valid image)
	minimalPNG := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, // PNG signature
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52, // IHDR chunk length + type
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, // 1x1
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53, // 8-bit RGB
		0xde, 0x00, 0x00, 0x00, 0x0c, 0x49, 0x44, 0x41, // IDAT chunk
		0x54, 0x08, 0xd7, 0x63, 0xf8, 0xcf, 0xc0, 0x00,
		0x00, 0x00, 0x02, 0x00, 0x01, 0xe2, 0x21, 0xbc,
		0x33, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, // IEND chunk
		0x44, 0xae, 0x42, 0x60, 0x82,
	}
	_, err := e.Extract(minimalPNG)
	// An error is acceptable (e.g., tesseract can't read the tiny image), but it must not panic.
	_ = err
}

// --- truncateText ---

func TestTruncateText_ShortUnchanged(t *testing.T) {
	input := "hello world"
	got := truncateText(input, 100)
	if got != input {
		t.Errorf("expected unchanged text, got: %q", got)
	}
}

func TestTruncateText_ExactLimit(t *testing.T) {
	input := strings.Repeat("a", 100)
	got := truncateText(input, 100)
	if got != input {
		t.Errorf("expected unchanged text at exact limit, got length %d", len(got))
	}
}

func TestTruncateText_LongTruncated(t *testing.T) {
	input := strings.Repeat("x", 200)
	got := truncateText(input, 100)
	if !strings.HasPrefix(got, strings.Repeat("x", 100)) {
		t.Errorf("expected first 100 chars preserved")
	}
	if !strings.Contains(got, "truncated") {
		t.Errorf("expected truncation notice in output, got: %q", got)
	}
}
