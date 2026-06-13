package types

import (
	"fmt"
	"strings"
)

// ValidateAttachments checks attachment count and sizes.
func ValidateAttachments(atts []Attachment) error {
	if len(atts) > MaxAttachmentsPerMsg {
		return fmt.Errorf("too many attachments: %d (max %d)", len(atts), MaxAttachmentsPerMsg)
	}
	for _, a := range atts {
		if a.Size > MaxAttachmentSize {
			return fmt.Errorf("attachment %q too large: %d bytes (max %d)", a.Filename, a.Size, MaxAttachmentSize)
		}
	}
	return nil
}

// IsImageMIME returns true if the content type is a supported image format.
func IsImageMIME(contentType string) bool {
	switch contentType {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return true
	}
	return false
}

// HasAttachmentsWithData returns true if any attachment has non-empty Data.
func HasAttachmentsWithData(atts []Attachment) bool {
	for _, a := range atts {
		if len(a.Data) > 0 {
			return true
		}
	}
	return false
}

// AnnotateAttachments returns a text summary of attachments for use when
// actual file data is unavailable (e.g. history replay). Attachments with
// extracted text are rendered with their full content; others are shown as
// metadata placeholders.
func AnnotateAttachments(attachments []Attachment) string {
	if len(attachments) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, a := range attachments {
		if a.ExtractedText != "" {
			fmt.Fprintf(&sb, "\n[Content of %s]:\n%s\n", a.Filename, a.ExtractedText)
		} else {
			sizeKB := a.Size / 1024
			if sizeKB == 0 {
				sizeKB = 1
			}
			fmt.Fprintf(&sb, "[Attachment: %s (%dKB)] ", a.Filename, sizeKB)
		}
	}
	return sb.String()
}

// HasExtractedText returns true if any attachment has extracted text.
func HasExtractedText(attachments []Attachment) bool {
	for _, a := range attachments {
		if a.ExtractedText != "" {
			return true
		}
	}
	return false
}

// ExtractedTextAnnotation returns OCR text for attachments that have both
// image data AND extracted text. Used by vision adapters to append OCR text
// alongside image data in multimodal content blocks.
func ExtractedTextAnnotation(attachments []Attachment) string {
	var sb strings.Builder
	for _, a := range attachments {
		if a.ExtractedText != "" && a.Data != nil {
			fmt.Fprintf(&sb, "\n[OCR text from %s]:\n%s\n", a.Filename, a.ExtractedText)
		}
	}
	return sb.String()
}
