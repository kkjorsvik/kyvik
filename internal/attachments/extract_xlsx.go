package attachments

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/xuri/excelize/v2"
)

type XLSXExtractor struct{}

func (e *XLSXExtractor) CanExtract(contentType string, filename string) bool {
	if contentType == "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" {
		return true
	}
	return strings.ToLower(filepath.Ext(filename)) == ".xlsx"
}

func (e *XLSXExtractor) Extract(data []byte) (string, error) {
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	defer f.Close()

	var sb strings.Builder
	for i, sheetName := range f.GetSheetList() {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		fmt.Fprintf(&sb, "--- Sheet: %s ---\n", sheetName)

		rows, err := f.GetRows(sheetName)
		if err != nil {
			continue
		}
		for _, row := range rows {
			sb.WriteString(strings.Join(row, ","))
			sb.WriteString("\n")
		}
	}
	return strings.TrimSpace(sb.String()), nil
}
