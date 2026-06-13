package security

import "regexp"

// SanitizeResult contains the outcome of sanitizing input text.
type SanitizeResult struct {
	Cleaned     string
	WasModified bool
	PatternsHit []string
}

// Sanitizer detects and replaces prompt injection patterns in text.
type Sanitizer struct {
	patterns []namedPattern
}

type namedPattern struct {
	name string
	re   *regexp.Regexp
}

// NewSanitizer creates a Sanitizer with pre-compiled patterns.
func NewSanitizer() *Sanitizer {
	raw := []struct {
		name    string
		pattern string
	}{
		{"ignore_previous", `(?i)ignore\s+(all\s+)?previous\s+instructions`},
		{"you_are_now", `(?i)you\s+are\s+now\b`},
		{"new_instructions", `(?i)new\s+instructions:`},
		{"system_prompt", `(?i)system\s+prompt:`},
		{"forget_everything", `(?i)forget\s+everything`},
		{"disregard", `(?i)disregard\s+your\s+instructions`},
		{"new_role", `(?i)your\s+new\s+role\s+is`},
		{"override", `(?i)override:`},
		{"role_switch", `(?im)^(assistant|system|human):`},
	}

	patterns := make([]namedPattern, 0, len(raw))
	for _, r := range raw {
		patterns = append(patterns, namedPattern{
			name: r.name,
			re:   regexp.MustCompile(r.pattern),
		})
	}
	return &Sanitizer{patterns: patterns}
}

// Sanitize checks input for injection patterns and replaces matches.
func (s *Sanitizer) Sanitize(input string) SanitizeResult {
	result := SanitizeResult{Cleaned: input}
	for _, p := range s.patterns {
		if p.re.MatchString(result.Cleaned) {
			result.WasModified = true
			result.PatternsHit = append(result.PatternsHit, p.name)
			result.Cleaned = p.re.ReplaceAllString(result.Cleaned, "[content filtered]")
		}
	}
	return result
}
