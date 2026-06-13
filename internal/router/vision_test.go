package router

import (
	"testing"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

func TestCheckVisionRoute(t *testing.T) {
	visionSlots := []ModelSlot{
		{Name: "default", Provider: "openrouter", Model: "gpt-4o"},
		{Name: "vision", Provider: "openrouter", Model: "gpt-4o"},
	}

	noVisionSlots := []ModelSlot{
		{Name: "default", Provider: "openrouter", Model: "gpt-4o"},
		{Name: "code", Provider: "openrouter", Model: "claude-sonnet"},
	}

	upperVisionSlots := []ModelSlot{
		{Name: "default", Provider: "openrouter", Model: "gpt-4o"},
		{Name: "Vision", Provider: "openrouter", Model: "gemini-pro-vision"},
	}

	tests := []struct {
		name        string
		attachments []types.Attachment
		slots       []ModelSlot
		wantRoute   bool
		wantSlot    string
		wantReason  string
	}{
		{
			name:        "image + vision slot",
			attachments: []types.Attachment{{ContentType: "image/jpeg"}},
			slots:       visionSlots,
			wantRoute:   true,
			wantSlot:    "vision",
			wantReason:  "image attachment detected",
		},
		{
			name:        "image + no vision slot",
			attachments: []types.Attachment{{ContentType: "image/png"}},
			slots:       noVisionSlots,
			wantRoute:   false,
			wantSlot:    "",
			wantReason:  "image attachment present, no vision slot configured",
		},
		{
			name:        "PDF only",
			attachments: []types.Attachment{{ContentType: "application/pdf"}},
			slots:       visionSlots,
			wantRoute:   false,
			wantSlot:    "",
			wantReason:  "no image attachments",
		},
		{
			name:        "no attachments",
			attachments: nil,
			slots:       visionSlots,
			wantRoute:   false,
			wantSlot:    "",
			wantReason:  "no image attachments",
		},
		{
			name:        "empty attachments",
			attachments: []types.Attachment{},
			slots:       visionSlots,
			wantRoute:   false,
			wantSlot:    "",
			wantReason:  "no image attachments",
		},
		{
			name: "mixed image + PDF",
			attachments: []types.Attachment{
				{ContentType: "application/pdf"},
				{ContentType: "image/png"},
			},
			slots:      visionSlots,
			wantRoute:  true,
			wantSlot:   "vision",
			wantReason: "image attachment detected",
		},
		{
			name:        "case-insensitive Vision slot",
			attachments: []types.Attachment{{ContentType: "image/webp"}},
			slots:       upperVisionSlots,
			wantRoute:   true,
			wantSlot:    "Vision",
			wantReason:  "image attachment detected",
		},
		{
			name:        "GIF image",
			attachments: []types.Attachment{{ContentType: "image/gif"}},
			slots:       visionSlots,
			wantRoute:   true,
			wantSlot:    "vision",
			wantReason:  "image attachment detected",
		},
		{
			name:        "nil slots + image",
			attachments: []types.Attachment{{ContentType: "image/jpeg"}},
			slots:       nil,
			wantRoute:   false,
			wantSlot:    "",
			wantReason:  "image attachment present, no vision slot configured",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CheckVisionRoute(tt.attachments, tt.slots)
			if got.ShouldRoute != tt.wantRoute {
				t.Errorf("ShouldRoute = %v, want %v", got.ShouldRoute, tt.wantRoute)
			}
			if got.SlotName != tt.wantSlot {
				t.Errorf("SlotName = %q, want %q", got.SlotName, tt.wantSlot)
			}
			if got.Reason != tt.wantReason {
				t.Errorf("Reason = %q, want %q", got.Reason, tt.wantReason)
			}
		})
	}
}

func TestIsLikelyVisionCapable(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{"gpt-4o", true},
		{"gpt-4o-mini", true},
		{"claude-3-opus", true},
		{"claude-3.5-sonnet", true},
		{"gemini-pro-vision", true},
		{"llava:13b", true},
		{"pixtral-12b", true},
		{"deepseek-chat", false},
		{"llama-3-70b", false},
		{"gpt-3.5-turbo", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			if got := IsLikelyVisionCapable(tt.model); got != tt.want {
				t.Errorf("IsLikelyVisionCapable(%q) = %v, want %v", tt.model, got, tt.want)
			}
		})
	}
}
