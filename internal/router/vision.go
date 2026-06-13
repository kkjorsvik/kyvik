package router

import (
	"strings"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

// VisionResult holds the outcome of a vision auto-routing check.
type VisionResult struct {
	ShouldRoute bool
	SlotName    string // canonical slot name, empty if not routing
	Reason      string
}

// CheckVisionRoute checks if the message has image attachments and a "vision"
// slot is configured. Returns a routing decision without making any API calls.
func CheckVisionRoute(attachments []types.Attachment, slots []ModelSlot) VisionResult {
	hasImage := false
	for _, a := range attachments {
		if types.IsImageMIME(a.ContentType) {
			hasImage = true
			break
		}
	}

	if !hasImage {
		return VisionResult{Reason: "no image attachments"}
	}

	for _, s := range slots {
		if strings.EqualFold(s.Name, "vision") {
			return VisionResult{
				ShouldRoute: true,
				SlotName:    s.Name,
				Reason:      "image attachment detected",
			}
		}
	}

	return VisionResult{Reason: "image attachment present, no vision slot configured"}
}

// visionKeywords are substrings that suggest a model supports vision input.
var visionKeywords = []string{"vision", "gpt-4o", "claude-3", "gemini", "llava", "pixtral"}

// IsLikelyVisionCapable is a heuristic check for informational logging only.
// It is NOT used for routing decisions.
func IsLikelyVisionCapable(modelName string) bool {
	lower := strings.ToLower(modelName)
	for _, kw := range visionKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}
