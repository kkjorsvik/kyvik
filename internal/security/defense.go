package security

import (
	"context"
	"log/slog"
	"strings"

	"github.com/kkjorsvik/kyvik/internal/ktp"
	"github.com/kkjorsvik/kyvik/internal/notifications"
	"github.com/kkjorsvik/kyvik/internal/timeutil"
	"github.com/kkjorsvik/kyvik/pkg/types"

	"github.com/oklog/ulid/v2"
)

// Defense orchestrates all prompt injection defense layers.
type Defense struct {
	sanitizer *Sanitizer
	validator *Validator
	recorder  *EventRecorder
}

// NewDefense creates a Defense with all subsystems initialized.
func NewDefense(store SecurityStore, notifier notifications.Notifier) *Defense {
	return &Defense{
		sanitizer: NewSanitizer(),
		validator: NewValidator(),
		recorder:  NewEventRecorder(store, notifier),
	}
}

// PrepareSystemPrompt injects a canary token into the system prompt.
// Returns the modified prompt and the canary token (nil if canary tokens are disabled).
func (d *Defense) PrepareSystemPrompt(ctx context.Context, cfg types.SecurityConfig,
	agentID, systemPrompt string) (string, *CanaryToken) {

	if !cfg.CanaryTokens {
		return systemPrompt, nil
	}

	canary, err := GenerateCanary(agentID)
	if err != nil {
		slog.Warn("failed to generate canary token", "agent_id", agentID, "error", err)
		return systemPrompt, nil
	}
	return InjectCanary(systemPrompt, canary), &canary
}

// ValidateToolCall checks tool parameters for destructive/exfiltration patterns.
// Returns nil if validation passes or is disabled.
func (d *Defense) ValidateToolCall(ctx context.Context, cfg types.SecurityConfig,
	agentID string, req ktp.ToolRequest) *ValidationResult {

	if !cfg.OutputValidation {
		return nil
	}

	result := d.validator.ValidateToolCall(agentID, req, cfg.AnomalyDetectionSensitivity)
	if !result.Safe {
		eventType := "destructive_pattern"
		severity := "warning"
		if result.Blocked {
			severity = "critical"
		}
		for _, w := range result.Warnings {
			if strings.Contains(w, "exfiltration") {
				eventType = "exfiltration_attempt"
				break
			}
		}
		d.recorder.Record(ctx, types.SecurityEvent{
			ID:        ulid.Make().String(),
			AgentID:   agentID,
			EventType: eventType,
			Severity:  severity,
			Details:   result.BlockReason + " " + strings.Join(result.Warnings, "; "),
			CreatedAt: timeutil.NowUTC(),
		})
	}

	if result.Blocked {
		return &result
	}
	return nil
}

// ProcessToolResult sanitizes external content, wraps in boundaries, and reinforces identity.
// Returns the (possibly modified) result.
func (d *Defense) ProcessToolResult(ctx context.Context, cfg types.SecurityConfig,
	agentID, agentName, toolName string, result ktp.ModelToolResult) ktp.ModelToolResult {

	content := result.Content

	// Sanitize external content.
	if cfg.SanitizeExternalContent {
		sr := d.sanitizer.Sanitize(content)
		if sr.WasModified {
			content = sr.Cleaned
			d.recorder.Record(ctx, types.SecurityEvent{
				ID:        ulid.Make().String(),
				AgentID:   agentID,
				EventType: "injection_detected",
				Severity:  "warning",
				Details:   "patterns hit in " + toolName + ": " + strings.Join(sr.PatternsHit, ", "),
				CreatedAt: timeutil.NowUTC(),
			})
		}
	}

	// Wrap in content boundaries.
	if cfg.ContentBoundaries {
		content = WrapExternalContent(toolName, content)
	}

	// Identity reinforcement.
	if cfg.IdentityReinforcement && agentName != "" {
		content = Reinforce(agentName, content)
	}

	result.Content = content
	return result
}

// ValidateResponse checks the model response for canary leaks and prompt leaks.
// If a canary leak is detected, the canary value is stripped from the response
// to prevent it from reaching end users.
func (d *Defense) ValidateResponse(ctx context.Context, cfg types.SecurityConfig,
	agentID string, canary *CanaryToken, response, systemPrompt string) string {

	if !cfg.OutputValidation {
		return response
	}

	result := d.validator.ValidateResponse(agentID, response, canary)
	if !result.Safe {
		for _, w := range result.Warnings {
			severity := "warning"
			eventType := "canary_leaked"
			if strings.Contains(w, "canary") {
				severity = "critical"
			}
			d.recorder.Record(ctx, types.SecurityEvent{
				ID:        ulid.Make().String(),
				AgentID:   agentID,
				EventType: eventType,
				Severity:  severity,
				Details:   w,
				CreatedAt: timeutil.NowUTC(),
			})
		}
	}

	// Strip canary token from response to prevent leaking it to users.
	if canary != nil && strings.Contains(response, canary.Value) {
		response = strings.ReplaceAll(response, canary.Value, "[REDACTED]")
	}

	return response
}
