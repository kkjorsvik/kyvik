package attachments

import (
	"bytes"
	"path/filepath"
	"strings"

	"github.com/ledongthuc/pdf"
)

type PDFExtractor struct{}

func (e *PDFExtractor) CanExtract(contentType string, filename string) bool {
	if contentType == "application/pdf" {
		return true
	}
	return strings.ToLower(filepath.Ext(filename)) == ".pdf"
}

func (e *PDFExtractor) Extract(data []byte) (string, error) {
	reader, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	numPages := reader.NumPage()
	for i := 1; i <= numPages; i++ {
		page := reader.Page(i)
		if page.V.IsNull() {
			continue
		}
		text, err := page.GetPlainText(nil)
		if err != nil {
			continue
		}
		sb.WriteString(text)
		if i < numPages {
			sb.WriteString("\n")
		}
	}
	return strings.TrimSpace(sb.String()), nil
}
