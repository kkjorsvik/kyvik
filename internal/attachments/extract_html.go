package attachments

import (
	"bytes"
	"path/filepath"
	"strings"

	"golang.org/x/net/html"
)

type HTMLExtractor struct{}

func (e *HTMLExtractor) CanExtract(contentType string, filename string) bool {
	if contentType == "text/html" {
		return true
	}
	ext := strings.ToLower(filepath.Ext(filename))
	return ext == ".html" || ext == ".htm"
}

func (e *HTMLExtractor) Extract(data []byte) (string, error) {
	doc, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	extractText(doc, &sb)
	// Collapse multiple newlines
	text := sb.String()
	for strings.Contains(text, "\n\n\n") {
		text = strings.ReplaceAll(text, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(text), nil
}

func extractText(n *html.Node, sb *strings.Builder) {
	if n.Type == html.TextNode {
		text := strings.TrimSpace(n.Data)
		if text != "" {
			sb.WriteString(text)
			sb.WriteString(" ")
		}
	}
	// Skip script and style elements
	if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style") {
		return
	}
	// Add newline after block elements
	if n.Type == html.ElementNode {
		switch n.Data {
		case "p", "div", "br", "h1", "h2", "h3", "h4", "h5", "h6", "li", "tr":
			sb.WriteString("\n")
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		extractText(c, sb)
	}
}
