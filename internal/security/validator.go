package security

import (
	"encoding/json"
	"regexp"

	"github.com/kkjorsvik/kyvik/internal/ktp"
)

// ValidationResult holds the outcome of a validation check.
type ValidationResult struct {
	Safe        bool
	Warnings    []string
	Blocked     bool
	BlockReason string
}

// Validator checks tool calls and responses for destructive or exfiltration patterns.
type Validator struct {
	destructive  []*regexp.Regexp
	exfiltration []*regexp.Regexp
}

// NewValidator creates a Validator with pre-compiled patterns.
func NewValidator() *Validator {
	destructive := []*regexp.Regexp{
		regexp.MustCompile(`(?i)rm\s+-rf`),
		regexp.MustCompile(`(?i)DROP\s+TABLE`),
		regexp.MustCompile(`(?i)DELETE\s+FROM`),
		regexp.MustCompile(`(?i)TRUNCATE\s+`),
		regexp.MustCompile(`(?i)FORMAT\s+`),
	}
	exfiltration := []*regexp.Regexp{
		regexp.MustCompile(`(?i)system_prompt`),
		regexp.MustCompile(`(?i)KYVIK_CANARY`),
	}
	return &Validator{
		destructive:  destructive,
		exfiltration: exfiltration,
	}
}

// ValidateToolCall checks tool call parameters for dangerous patterns.
// sensitivity: "low" = only block destructive, "medium" = block destructive + warn exfiltration,
// "high" = block both.
func (v *Validator) ValidateToolCall(agentID string, req ktp.ToolRequest, sensitivity string) ValidationResult {
	result := ValidationResult{Safe: true}

	// Stringify parameters for pattern matching.
	paramStr := stringifyParams(req.Parameters)

	// Check destructive patterns (blocked at all sensitivity levels).
	for _, p := range v.destructive {
		if p.MatchString(paramStr) {
			result.Safe = false
			result.Blocked = true
			result.BlockReason = "destructive pattern detected: " + p.String()
			return result
		}
	}

	// Check exfiltration patterns.
	for _, p := range v.exfiltration {
		if p.MatchString(paramStr) {
			warning := "exfiltration pattern detected: " + p.String()
			result.Warnings = append(result.Warnings, warning)
			if sensitivity == "high" {
				result.Safe = false
				result.Blocked = true
				result.BlockReason = warning
				return result
			}
			if sensitivity == "medium" {
				result.Safe = false
			}
		}
	}

	return result
}

// ValidateResponse checks a model response for canary leaks and other issues.
// It does not block (alert only — the response already exists).
func (v *Validator) ValidateResponse(agentID, response string, canary *CanaryToken) ValidationResult {
	result := ValidationResult{Safe: true}

	if canary != nil && CheckCanaryLeak(response, *canary) {
		result.Safe = false
		result.Warnings = append(result.Warnings, "canary token leaked in response")
	}

	return result
}

func stringifyParams(params map[string]any) string {
	if params == nil {
		return ""
	}
	data, err := json.Marshal(params)
	if err != nil {
		return ""
	}
	return string(data)
}
