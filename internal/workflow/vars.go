package workflow

import (
	"fmt"
	"regexp"
	"strings"
)

// templatePattern matches {{variable}} templates including dot notation.
var templatePattern = regexp.MustCompile(`\{\{(\w+(?:\.\w+)*)\}\}`)

// expandParams walks a params map and replaces {{variable}} templates with values
// from the vars map. Returns a new map with expanded values.
//
// Type preservation: if a string value is exactly "{{name}}" (single variable, no
// surrounding text), the resolved value keeps its original type. If the template is
// embedded in a larger string ("Started: {{name}}"), the result is a string.
//
// Dot notation: {{task.id}} resolves vars["task"].(map[string]any)["id"] (one level).
//
// Missing variables cause an error.
func expandParams(params map[string]any, vars map[string]any) (map[string]any, error) {
	result := make(map[string]any, len(params))
	for k, v := range params {
		expanded, err := expandValue(v, vars)
		if err != nil {
			return nil, err
		}
		result[k] = expanded
	}
	return result, nil
}

// expandValue expands a single value, recursing into maps.
func expandValue(v any, vars map[string]any) (any, error) {
	switch val := v.(type) {
	case string:
		return expandString(val, vars)
	case map[string]any:
		return expandParams(val, vars)
	default:
		// Non-string values (int, bool, float, nil) pass through unchanged.
		return v, nil
	}
}

// expandString handles template expansion in a string value.
func expandString(s string, vars map[string]any) (any, error) {
	// Check if the entire string is a single {{variable}} template.
	// If so, preserve the original type of the resolved value.
	trimmed := strings.TrimSpace(s)
	if templatePattern.MatchString(trimmed) {
		match := templatePattern.FindStringSubmatch(trimmed)
		if match != nil && match[0] == trimmed {
			return resolveVar(match[1], vars)
		}
	}

	// Mixed template: resolve all {{var}} patterns into strings.
	var expandErr error
	result := templatePattern.ReplaceAllStringFunc(s, func(m string) string {
		if expandErr != nil {
			return m
		}
		name := templatePattern.FindStringSubmatch(m)[1]
		val, err := resolveVar(name, vars)
		if err != nil {
			expandErr = err
			return m
		}
		return fmt.Sprintf("%v", val)
	})
	if expandErr != nil {
		return nil, expandErr
	}
	return result, nil
}

// resolveVar looks up a variable name in the vars map. Supports dot notation
// for one level of nesting (e.g., "task.id").
func resolveVar(name string, vars map[string]any) (any, error) {
	parts := strings.SplitN(name, ".", 2)
	val, ok := vars[parts[0]]
	if !ok {
		return nil, fmt.Errorf("variable '%s' not found", name)
	}
	if len(parts) == 1 {
		return val, nil
	}
	// Dot notation: resolve one level into a map.
	m, ok := val.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("variable '%s' not found", name)
	}
	nested, ok := m[parts[1]]
	if !ok {
		return nil, fmt.Errorf("variable '%s' not found", name)
	}
	return nested, nil
}
