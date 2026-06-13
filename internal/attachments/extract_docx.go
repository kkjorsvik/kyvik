package attachments

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"path/filepath"
	"strings"
)

type DOCXExtractor struct{}

func (e *DOCXExtractor) CanExtract(contentType string, filename string) bool {
	if contentType == "application/vnd.openxmlformats-officedocument.wordprocessingml.document" {
		return true
	}
	return strings.ToLower(filepath.Ext(filename)) == ".docx"
}

func (e *DOCXExtractor) Extract(data []byte) (string, error) {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}

	for _, f := range r.File {
		if f.Name == "word/document.xml" {
			rc, err := f.Open()
			if err != nil {
				return "", err
			}
			defer rc.Close()
			return extractXMLText(rc)
		}
	}
	return "", nil
}

// extractXMLText walks XML tokens and collects text content from <w:t> elements.
func extractXMLText(r io.Reader) (string, error) {
	decoder := xml.NewDecoder(r)
	var sb strings.Builder
	inText := false
	inParagraph := false

	for {
		tok, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return sb.String(), nil // return what we have
		}

		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				inText = true
			case "p":
				if inParagraph {
					sb.WriteString("\n")
				}
				inParagraph = true
			}
		case xml.EndElement:
			if t.Name.Local == "t" {
				inText = false
			}
		case xml.CharData:
			if inText {
				sb.Write(t)
			}
		}
	}
	return strings.TrimSpace(sb.String()), nil
}
