package ktp

import (
	"strings"
	"testing"
)

// --- ValidateParams ---

func TestSchemaValidParams(t *testing.T) {
	schema := JSONSchema{
		Type: "object",
		Properties: map[string]JSONSchema{
			"name": {Type: "string"},
			"age":  {Type: "number"},
		},
		Required: []string{"name"},
	}
	params := map[string]any{
		"name": "alice",
		"age":  float64(30),
	}
	errs := ValidateParams(params, schema)
	if len(errs) != 0 {
		t.Fatalf("expected 0 errors, got %d: %v", len(errs), errs)
	}
}

func TestSchemaMissingRequired(t *testing.T) {
	schema := JSONSchema{
		Type: "object",
		Properties: map[string]JSONSchema{
			"name": {Type: "string"},
		},
		Required: []string{"name"},
	}
	params := map[string]any{}
	errs := ValidateParams(params, schema)
	if !hasError(errs, "name", "required") {
		t.Fatalf("expected required field error for 'name', got %v", errs)
	}
}

func TestSchemaWrongType(t *testing.T) {
	schema := JSONSchema{
		Type: "object",
		Properties: map[string]JSONSchema{
			"count": {Type: "number"},
		},
	}
	params := map[string]any{
		"count": "not-a-number",
	}
	errs := ValidateParams(params, schema)
	if !hasError(errs, "count", "expected number") {
		t.Fatalf("expected type error for 'count', got %v", errs)
	}
}

func TestSchemaNestedObject(t *testing.T) {
	schema := JSONSchema{
		Type: "object",
		Properties: map[string]JSONSchema{
			"address": {
				Type: "object",
				Properties: map[string]JSONSchema{
					"city": {Type: "string"},
				},
				Required: []string{"city"},
			},
		},
	}
	params := map[string]any{
		"address": map[string]any{},
	}
	errs := ValidateParams(params, schema)
	if !hasError(errs, "address.city", "required") {
		t.Fatalf("expected required error for 'address.city', got %v", errs)
	}
}

func TestSchemaArrayValidation(t *testing.T) {
	schema := JSONSchema{
		Type: "object",
		Properties: map[string]JSONSchema{
			"tags": {
				Type:  "array",
				Items: &JSONSchema{Type: "string"},
			},
		},
	}

	t.Run("valid array", func(t *testing.T) {
		params := map[string]any{
			"tags": []any{"go", "test"},
		}
		errs := ValidateParams(params, schema)
		if len(errs) != 0 {
			t.Fatalf("expected 0 errors, got %v", errs)
		}
	})

	t.Run("invalid item", func(t *testing.T) {
		params := map[string]any{
			"tags": []any{"go", float64(42)},
		}
		errs := ValidateParams(params, schema)
		if !hasError(errs, "tags[1]", "expected string") {
			t.Fatalf("expected type error for 'tags[1]', got %v", errs)
		}
	})
}

func TestSchemaStringTooShort(t *testing.T) {
	min := 3
	schema := JSONSchema{
		Type: "object",
		Properties: map[string]JSONSchema{
			"name": {Type: "string", MinLength: &min},
		},
	}
	params := map[string]any{"name": "ab"}
	errs := ValidateParams(params, schema)
	if !hasError(errs, "name", "below minimum") {
		t.Fatalf("expected minLength error, got %v", errs)
	}
}

func TestSchemaStringTooLong(t *testing.T) {
	max := 5
	schema := JSONSchema{
		Type: "object",
		Properties: map[string]JSONSchema{
			"name": {Type: "string", MaxLength: &max},
		},
	}
	params := map[string]any{"name": "toolong"}
	errs := ValidateParams(params, schema)
	if !hasError(errs, "name", "above maximum") {
		t.Fatalf("expected maxLength error, got %v", errs)
	}
}

func TestSchemaPatternMismatch(t *testing.T) {
	schema := JSONSchema{
		Type: "object",
		Properties: map[string]JSONSchema{
			"email": {Type: "string", Pattern: `^[a-z]+@[a-z]+\.[a-z]+$`},
		},
	}
	params := map[string]any{"email": "not-an-email"}
	errs := ValidateParams(params, schema)
	if !hasError(errs, "email", "does not match pattern") {
		t.Fatalf("expected pattern error, got %v", errs)
	}
}

func TestSchemaNumberBelowMinimum(t *testing.T) {
	min := float64(10)
	schema := JSONSchema{
		Type: "object",
		Properties: map[string]JSONSchema{
			"score": {Type: "number", Minimum: &min},
		},
	}
	params := map[string]any{"score": float64(5)}
	errs := ValidateParams(params, schema)
	if !hasError(errs, "score", "below minimum") {
		t.Fatalf("expected minimum error, got %v", errs)
	}
}

func TestSchemaNumberAboveMaximum(t *testing.T) {
	max := float64(100)
	schema := JSONSchema{
		Type: "object",
		Properties: map[string]JSONSchema{
			"score": {Type: "number", Maximum: &max},
		},
	}
	params := map[string]any{"score": float64(150)}
	errs := ValidateParams(params, schema)
	if !hasError(errs, "score", "above maximum") {
		t.Fatalf("expected maximum error, got %v", errs)
	}
}

func TestSchemaEnumValid(t *testing.T) {
	schema := JSONSchema{
		Type: "object",
		Properties: map[string]JSONSchema{
			"level": {Type: "string", Enum: []string{"low", "medium", "high"}},
		},
	}
	params := map[string]any{"level": "medium"}
	errs := ValidateParams(params, schema)
	if len(errs) != 0 {
		t.Fatalf("expected 0 errors, got %v", errs)
	}
}

func TestSchemaEnumInvalid(t *testing.T) {
	schema := JSONSchema{
		Type: "object",
		Properties: map[string]JSONSchema{
			"level": {Type: "string", Enum: []string{"low", "medium", "high"}},
		},
	}
	params := map[string]any{"level": "ultra"}
	errs := ValidateParams(params, schema)
	if !hasError(errs, "level", "not in enum") {
		t.Fatalf("expected enum error, got %v", errs)
	}
}

func TestSchemaIntegerCoercion(t *testing.T) {
	schema := JSONSchema{
		Type: "object",
		Properties: map[string]JSONSchema{
			"count": {Type: "integer"},
		},
	}

	t.Run("float64 whole number", func(t *testing.T) {
		params := map[string]any{"count": float64(5)}
		errs := ValidateParams(params, schema)
		if len(errs) != 0 {
			t.Fatalf("5.0 should be valid integer, got %v", errs)
		}
	})

	t.Run("float64 fractional", func(t *testing.T) {
		params := map[string]any{"count": float64(5.5)}
		errs := ValidateParams(params, schema)
		if !hasError(errs, "count", "expected integer") {
			t.Fatalf("5.5 should be invalid integer, got %v", errs)
		}
	})

	t.Run("actual int", func(t *testing.T) {
		params := map[string]any{"count": 7}
		errs := ValidateParams(params, schema)
		if len(errs) != 0 {
			t.Fatalf("int 7 should be valid integer, got %v", errs)
		}
	})
}

func TestSchemaExtraFields(t *testing.T) {
	schema := JSONSchema{
		Type: "object",
		Properties: map[string]JSONSchema{
			"name": {Type: "string"},
		},
	}
	params := map[string]any{
		"name":    "alice",
		"unknown": "value",
	}
	errs := ValidateParams(params, schema)
	if len(errs) != 0 {
		t.Fatalf("extra fields should not produce errors, got %v", errs)
	}
}

func TestSchemaEmptySchema(t *testing.T) {
	schema := JSONSchema{} // no type set
	params := map[string]any{
		"anything": "goes",
	}
	errs := ValidateParams(params, schema)
	if len(errs) != 0 {
		t.Fatalf("empty schema should accept everything, got %v", errs)
	}
}

// --- ValidateResult ---

func TestSchemaValidateResultLenient(t *testing.T) {
	schema := JSONSchema{
		Type: "object",
		Properties: map[string]JSONSchema{
			"status": {Type: "string"},
			"count":  {Type: "integer"},
		},
		Required: []string{"status"},
	}
	result := map[string]any{
		"status": 42, // wrong type
	}
	errs := ValidateResult(result, schema)
	// Errors should be returned for informational use.
	if len(errs) == 0 {
		t.Fatal("expected validation errors from result")
	}
	if !hasError(errs, "status", "expected string") {
		t.Fatalf("expected type error for 'status', got %v", errs)
	}
}

// --- SchemaToModelFormat ---

func TestSchemaToModelFormat(t *testing.T) {
	min := 1
	max := float64(100)
	action := ActionSpec{
		Name:        "search",
		Description: "Search for items",
		Parameters: JSONSchema{
			Type: "object",
			Properties: map[string]JSONSchema{
				"query": {
					Type:        "string",
					Description: "search query",
					MinLength:   &min,
				},
				"limit": {
					Type:    "integer",
					Maximum: &max,
				},
			},
			Required: []string{"query"},
		},
	}

	result := SchemaToModelFormat(action)

	if result["type"] != "function" {
		t.Fatalf("expected type 'function', got %v", result["type"])
	}

	fn, ok := result["function"].(map[string]any)
	if !ok {
		t.Fatal("expected function to be a map")
	}
	if fn["name"] != "search" {
		t.Fatalf("expected name 'search', got %v", fn["name"])
	}
	if fn["description"] != "Search for items" {
		t.Fatalf("expected description 'Search for items', got %v", fn["description"])
	}

	params, ok := fn["parameters"].(map[string]any)
	if !ok {
		t.Fatal("expected parameters to be a map")
	}
	if params["type"] != "object" {
		t.Fatalf("expected parameters type 'object', got %v", params["type"])
	}

	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected properties to be a map")
	}
	queryProp, ok := props["query"].(map[string]any)
	if !ok {
		t.Fatal("expected query property to be a map")
	}
	if queryProp["type"] != "string" {
		t.Fatalf("expected query type 'string', got %v", queryProp["type"])
	}
	if queryProp["description"] != "search query" {
		t.Fatalf("expected query description 'search query', got %v", queryProp["description"])
	}
	if queryProp["minLength"] != 1 {
		t.Fatalf("expected query minLength 1, got %v", queryProp["minLength"])
	}

	req, ok := params["required"].([]string)
	if !ok {
		t.Fatal("expected required to be []string")
	}
	if len(req) != 1 || req[0] != "query" {
		t.Fatalf("expected required [query], got %v", req)
	}
}

func TestSchemaToModelFormatWithItems(t *testing.T) {
	action := ActionSpec{
		Name:        "list",
		Description: "List items",
		Parameters: JSONSchema{
			Type: "object",
			Properties: map[string]JSONSchema{
				"tags": {
					Type:  "array",
					Items: &JSONSchema{Type: "string"},
				},
			},
		},
	}

	result := SchemaToModelFormat(action)
	fn := result["function"].(map[string]any)
	params := fn["parameters"].(map[string]any)
	props := params["properties"].(map[string]any)
	tagsProp := props["tags"].(map[string]any)

	if tagsProp["type"] != "array" {
		t.Fatalf("expected tags type 'array', got %v", tagsProp["type"])
	}
	items, ok := tagsProp["items"].(map[string]any)
	if !ok {
		t.Fatal("expected items to be a map")
	}
	if items["type"] != "string" {
		t.Fatalf("expected items type 'string', got %v", items["type"])
	}
}

// --- Boolean type ---

func TestSchemaBooleanValid(t *testing.T) {
	schema := JSONSchema{
		Type: "object",
		Properties: map[string]JSONSchema{
			"active": {Type: "boolean"},
		},
	}
	params := map[string]any{"active": true}
	errs := ValidateParams(params, schema)
	if len(errs) != 0 {
		t.Fatalf("expected 0 errors, got %v", errs)
	}
}

func TestSchemaBooleanWrongType(t *testing.T) {
	schema := JSONSchema{
		Type: "object",
		Properties: map[string]JSONSchema{
			"active": {Type: "boolean"},
		},
	}
	params := map[string]any{"active": "yes"}
	errs := ValidateParams(params, schema)
	if !hasError(errs, "active", "expected boolean") {
		t.Fatalf("expected boolean type error, got %v", errs)
	}
}

// --- Required field nil ---

func TestSchemaRequiredFieldNil(t *testing.T) {
	schema := JSONSchema{
		Type: "object",
		Properties: map[string]JSONSchema{
			"name": {Type: "string"},
		},
		Required: []string{"name"},
	}
	params := map[string]any{"name": nil}
	errs := ValidateParams(params, schema)
	if !hasError(errs, "name", "required") {
		t.Fatalf("expected required error for nil field, got %v", errs)
	}
}

// --- helpers ---

func hasError(errs []ValidationError, field, msgSubstring string) bool {
	for _, e := range errs {
		if e.Field == field && strings.Contains(e.Message, msgSubstring) {
			return true
		}
	}
	return false
}
