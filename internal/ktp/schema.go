package ktp

import (
	"fmt"
	"log/slog"
	"math"
	"reflect"
	"regexp"
	"slices"
)

// ValidationError describes a single schema violation for a specific field.
type ValidationError struct {
	Field   string // e.g. "path", "parameters.url", "items[2].name"
	Message string
}

// ValidateParams validates tool parameters strictly against a schema.
// Callers should reject the request if the returned slice is non-empty.
func ValidateParams(params map[string]any, schema JSONSchema) []ValidationError {
	var errs []ValidationError
	validateValue("", params, schema, &errs)
	return errs
}

// ValidateResult validates a tool result leniently against a schema.
// Errors are logged as warnings but returned for informational use only.
func ValidateResult(result map[string]any, schema JSONSchema) []ValidationError {
	var errs []ValidationError
	validateValue("", result, schema, &errs)
	for _, e := range errs {
		slog.Warn("schema validation warning", "field", e.Field, "message", e.Message)
	}
	return errs
}

// SchemaToModelFormat converts an ActionSpec into the OpenAI/Anthropic
// function-calling format used by model providers.
func SchemaToModelFormat(action ActionSpec) map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        action.Name,
			"description": action.Description,
			"parameters":  schemaToMap(action.Parameters),
		},
	}
}

// schemaToMap recursively converts a JSONSchema into a plain map,
// including only non-zero fields.
func schemaToMap(s JSONSchema) map[string]any {
	m := map[string]any{}

	if s.Type != "" {
		m["type"] = s.Type
	}
	if s.Description != "" {
		m["description"] = s.Description
	}
	if len(s.Required) > 0 {
		m["required"] = s.Required
	}
	if len(s.Enum) > 0 {
		m["enum"] = s.Enum
	}
	if s.MinLength != nil {
		m["minLength"] = *s.MinLength
	}
	if s.MaxLength != nil {
		m["maxLength"] = *s.MaxLength
	}
	if s.Minimum != nil {
		m["minimum"] = *s.Minimum
	}
	if s.Maximum != nil {
		m["maximum"] = *s.Maximum
	}
	if s.Pattern != "" {
		m["pattern"] = s.Pattern
	}
	if s.Default != nil {
		m["default"] = s.Default
	}
	if len(s.Properties) > 0 {
		props := map[string]any{}
		for k, v := range s.Properties {
			props[k] = schemaToMap(v)
		}
		m["properties"] = props
	}
	if s.Items != nil {
		m["items"] = schemaToMap(*s.Items)
	}

	return m
}

// validateValue recursively checks a single value against a schema,
// appending any violations to errs.
func validateValue(path string, value any, schema JSONSchema, errs *[]ValidationError) {
	// Empty type means no constraints.
	if schema.Type == "" {
		return
	}

	// Type check.
	switch schema.Type {
	case "string":
		s, ok := value.(string)
		if !ok {
			appendErr(errs, path, fmt.Sprintf("expected string, got %T", value))
			return
		}
		validateStringConstraints(path, s, schema, errs)

	case "number":
		f, ok := toFloat64(value)
		if !ok {
			appendErr(errs, path, fmt.Sprintf("expected number, got %T", value))
			return
		}
		validateNumberConstraints(path, f, schema, errs)

	case "integer":
		f, ok := toFloat64(value)
		if !ok {
			appendErr(errs, path, fmt.Sprintf("expected integer, got %T", value))
			return
		}
		if math.Trunc(f) != f {
			appendErr(errs, path, fmt.Sprintf("expected integer, got %v", value))
			return
		}
		validateNumberConstraints(path, f, schema, errs)

	case "boolean":
		if _, ok := value.(bool); !ok {
			appendErr(errs, path, fmt.Sprintf("expected boolean, got %T", value))
		}

	case "object":
		m, ok := value.(map[string]any)
		if !ok {
			appendErr(errs, path, fmt.Sprintf("expected object, got %T", value))
			return
		}
		validateObject(path, m, schema, errs)

	case "array":
		if value == nil {
			return // nil is acceptable for optional arrays
		}
		arr, ok := toAnySlice(value)
		if !ok {
			appendErr(errs, path, fmt.Sprintf("expected array, got %T", value))
			return
		}
		validateArray(path, arr, schema, errs)

	default:
		appendErr(errs, path, fmt.Sprintf("unknown schema type: %s", schema.Type))
	}
}

// validateStringConstraints checks enum, minLength, maxLength, and pattern.
func validateStringConstraints(path, s string, schema JSONSchema, errs *[]ValidationError) {
	if len(schema.Enum) > 0 && !slices.Contains(schema.Enum, s) {
		appendErr(errs, path, fmt.Sprintf("value %q not in enum %v", s, schema.Enum))
	}
	if schema.MinLength != nil && len(s) < *schema.MinLength {
		appendErr(errs, path, fmt.Sprintf("string length %d below minimum %d", len(s), *schema.MinLength))
	}
	if schema.MaxLength != nil && len(s) > *schema.MaxLength {
		appendErr(errs, path, fmt.Sprintf("string length %d above maximum %d", len(s), *schema.MaxLength))
	}
	if schema.Pattern != "" {
		matched, err := regexp.MatchString(schema.Pattern, s)
		if err != nil {
			appendErr(errs, path, fmt.Sprintf("invalid pattern %q: %v", schema.Pattern, err))
		} else if !matched {
			appendErr(errs, path, fmt.Sprintf("value %q does not match pattern %q", s, schema.Pattern))
		}
	}
}

// validateNumberConstraints checks minimum and maximum.
func validateNumberConstraints(path string, f float64, schema JSONSchema, errs *[]ValidationError) {
	if schema.Minimum != nil && f < *schema.Minimum {
		appendErr(errs, path, fmt.Sprintf("value %v below minimum %v", f, *schema.Minimum))
	}
	if schema.Maximum != nil && f > *schema.Maximum {
		appendErr(errs, path, fmt.Sprintf("value %v above maximum %v", f, *schema.Maximum))
	}
}

// validateObject checks required fields, properties, and logs extra fields.
func validateObject(path string, m map[string]any, schema JSONSchema, errs *[]ValidationError) {
	// Required fields.
	for _, req := range schema.Required {
		v, exists := m[req]
		if !exists || v == nil {
			appendErr(errs, joinPath(path, req), "required field missing")
		}
	}

	// Validate known properties.
	for key, propSchema := range schema.Properties {
		if v, exists := m[key]; exists {
			validateValue(joinPath(path, key), v, propSchema, errs)
		}
	}

	// Log extra fields (not an error — forward compatibility).
	for key := range m {
		if _, known := schema.Properties[key]; !known && len(schema.Properties) > 0 {
			slog.Debug("unknown field in schema", "path", joinPath(path, key))
		}
	}
}

// validateArray validates each element against schema.Items.
func validateArray(path string, arr []any, schema JSONSchema, errs *[]ValidationError) {
	if schema.Items == nil {
		return
	}
	for i, elem := range arr {
		elemPath := fmt.Sprintf("%s[%d]", path, i)
		validateValue(elemPath, elem, *schema.Items, errs)
	}
}

// toAnySlice converts any slice type ([]map[string]any, []string, etc.) to []any.
// Go's type system makes []T non-assignable to []any even when T implements any,
// so we use reflect to handle all concrete slice types from JSON unmarshaling.
func toAnySlice(v any) ([]any, bool) {
	if arr, ok := v.([]any); ok {
		return arr, true
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Slice {
		return nil, false
	}
	result := make([]any, rv.Len())
	for i := range result {
		result[i] = rv.Index(i).Interface()
	}
	return result, true
}

// toFloat64 converts JSON-compatible numeric types to float64.
func toFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case int32:
		return float64(n), true
	}
	return 0, false
}

// joinPath builds a dotted field path.
func joinPath(parent, child string) string {
	if parent == "" {
		return child
	}
	return parent + "." + child
}

// appendErr adds a ValidationError to the slice.
func appendErr(errs *[]ValidationError, field, message string) {
	*errs = append(*errs, ValidationError{Field: field, Message: message})
}
