package workflow

import (
	"testing"
)

func TestExpandParams_BasicString(t *testing.T) {
	params := map[string]any{"greeting": "Hello {{name}}"}
	vars := map[string]any{"name": "World"}

	result, err := expandParams(params, vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["greeting"] != "Hello World" {
		t.Errorf("got %v, want 'Hello World'", result["greeting"])
	}
}

func TestExpandParams_TypePreservation(t *testing.T) {
	params := map[string]any{"count": "{{count}}"}
	vars := map[string]any{"count": 42}

	result, err := expandParams(params, vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	val, ok := result["count"].(int)
	if !ok {
		t.Fatalf("expected int, got %T (%v)", result["count"], result["count"])
	}
	if val != 42 {
		t.Errorf("got %d, want 42", val)
	}
}

func TestExpandParams_DotNotation(t *testing.T) {
	params := map[string]any{"id": "{{task.id}}"}
	vars := map[string]any{
		"task": map[string]any{"id": "uuid-1"},
	}

	result, err := expandParams(params, vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["id"] != "uuid-1" {
		t.Errorf("got %v, want 'uuid-1'", result["id"])
	}
}

func TestExpandParams_MissingVar(t *testing.T) {
	params := map[string]any{"val": "{{missing}}"}
	vars := map[string]any{}

	_, err := expandParams(params, vars)
	if err == nil {
		t.Fatal("expected error for missing variable")
	}
	if err.Error() != "variable 'missing' not found" {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestExpandParams_NonStringPassthrough(t *testing.T) {
	params := map[string]any{
		"count":   42,
		"enabled": true,
		"rate":    3.14,
		"empty":   nil,
	}
	vars := map[string]any{}

	result, err := expandParams(params, vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["count"] != 42 {
		t.Errorf("count: got %v, want 42", result["count"])
	}
	if result["enabled"] != true {
		t.Errorf("enabled: got %v, want true", result["enabled"])
	}
	if result["rate"] != 3.14 {
		t.Errorf("rate: got %v, want 3.14", result["rate"])
	}
	if result["empty"] != nil {
		t.Errorf("empty: got %v, want nil", result["empty"])
	}
}

func TestExpandParams_MultipleVars(t *testing.T) {
	params := map[string]any{"msg": "{{first}} and {{second}}"}
	vars := map[string]any{"first": "alpha", "second": "beta"}

	result, err := expandParams(params, vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["msg"] != "alpha and beta" {
		t.Errorf("got %v, want 'alpha and beta'", result["msg"])
	}
}

func TestExpandParams_NestedMap(t *testing.T) {
	params := map[string]any{
		"outer": map[string]any{
			"inner": "{{val}}",
		},
	}
	vars := map[string]any{"val": "resolved"}

	result, err := expandParams(params, vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	outer, ok := result["outer"].(map[string]any)
	if !ok {
		t.Fatalf("expected nested map, got %T", result["outer"])
	}
	if outer["inner"] != "resolved" {
		t.Errorf("got %v, want 'resolved'", outer["inner"])
	}
}
