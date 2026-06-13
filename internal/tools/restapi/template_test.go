package restapi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRenderURL(t *testing.T) {
	data := map[string]any{"id": "42", "slug": "hello-world"}
	got, err := renderURL("https://api.example.com/items/{{.id}}/{{.slug}}", data)
	if err != nil {
		t.Fatalf("renderURL: %v", err)
	}
	if got != "https://api.example.com/items/42/hello-world" {
		t.Fatalf("got %q", got)
	}
}

func TestRenderURL_MissingKey(t *testing.T) {
	data := map[string]any{}
	_, err := renderURL("https://api.example.com/items/{{.id}}", data)
	if err == nil {
		t.Fatal("expected error for missing key")
	}
}

func TestRenderURL_InvalidSyntax(t *testing.T) {
	_, err := renderURL("https://api.example.com/{{.id", nil)
	if err == nil {
		t.Fatal("expected error for invalid syntax")
	}
}

func TestRenderHeaders(t *testing.T) {
	headers := map[string]string{
		"X-Custom": "val-{{.token}}",
		"Accept":   "application/json",
	}
	data := map[string]any{"token": "abc123"}
	got, err := renderHeaders(headers, data)
	if err != nil {
		t.Fatalf("renderHeaders: %v", err)
	}
	if got["X-Custom"] != "val-abc123" {
		t.Fatalf("X-Custom = %q", got["X-Custom"])
	}
	if got["Accept"] != "application/json" {
		t.Fatalf("Accept = %q", got["Accept"])
	}
}

func TestRenderHeaders_Nil(t *testing.T) {
	got, err := renderHeaders(nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

func TestRenderQueryParams(t *testing.T) {
	params := map[string]string{"q": "{{.query}}", "limit": "10"}
	data := map[string]any{"query": "search term"}
	got, err := renderQueryParams(params, data)
	if err != nil {
		t.Fatalf("renderQueryParams: %v", err)
	}
	if got["q"] != "search term" {
		t.Fatalf("q = %q", got["q"])
	}
	if got["limit"] != "10" {
		t.Fatalf("limit = %q", got["limit"])
	}
}

func TestRenderBody(t *testing.T) {
	tmpl := `{"name": "{{.name}}", "value": {{.value}}}`
	data := map[string]any{"name": "test", "value": 42}
	got, err := renderBody(tmpl, data)
	if err != nil {
		t.Fatalf("renderBody: %v", err)
	}
	if !strings.Contains(got, `"name": "test"`) {
		t.Fatalf("body = %q", got)
	}
}

func TestRenderBody_Empty(t *testing.T) {
	got, err := renderBody("", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestRenderBody_OptionalFieldWithIf(t *testing.T) {
	// {{if .field}} should not error when field is missing — it should skip the block.
	tmpl := `{"title": "{{.title}}"{{if .due_date}}, "due_date": "{{.due_date}}"{{end}}}`
	data := map[string]any{"title": "My Task"}
	got, err := renderBody(tmpl, data)
	if err != nil {
		t.Fatalf("renderBody should tolerate missing optional field: %v", err)
	}
	if got != `{"title": "My Task"}` {
		t.Fatalf("got %q", got)
	}
}

func TestRenderBody_DefaultWithOr(t *testing.T) {
	// {{or .field "default"}} should use default when field is missing.
	tmpl := `{"limit": {{or .limit 10}}}`
	data := map[string]any{}
	got, err := renderBody(tmpl, data)
	if err != nil {
		t.Fatalf("renderBody should tolerate missing field with default: %v", err)
	}
	if got != `{"limit": 10}` {
		t.Fatalf("got %q", got)
	}
}

func TestRenderBody_RequiredFieldStillWorks(t *testing.T) {
	// Bare {{.field}} for required fields should render the provided value.
	tmpl := `{"query": "{{.query}}"}`
	data := map[string]any{"query": "search term"}
	got, err := renderBody(tmpl, data)
	if err != nil {
		t.Fatalf("renderBody: %v", err)
	}
	if got != `{"query": "search term"}` {
		t.Fatalf("got %q", got)
	}
}

func TestRenderBody_JsonFunc_Object(t *testing.T) {
	tmpl := `{"type":"morning","responses":{{json .responses}}}`
	data := map[string]any{
		"responses": map[string]any{"mood": 4, "sleep_hours": 7.5},
	}
	got, err := renderBody(tmpl, data)
	if err != nil {
		t.Fatalf("renderBody: %v", err)
	}
	// Verify it's valid JSON by round-tripping.
	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v\ngot: %s", err, got)
	}
	resp, ok := parsed["responses"].(map[string]any)
	if !ok {
		t.Fatalf("responses not an object: %T", parsed["responses"])
	}
	if resp["mood"] != float64(4) {
		t.Errorf("mood = %v", resp["mood"])
	}
}

func TestRenderBody_JsonFunc_Array(t *testing.T) {
	tmpl := `{"tags":{{json .tags}}}`
	data := map[string]any{
		"tags": []any{"dev", "support"},
	}
	got, err := renderBody(tmpl, data)
	if err != nil {
		t.Fatalf("renderBody: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v\ngot: %s", err, got)
	}
	tags, ok := parsed["tags"].([]any)
	if !ok {
		t.Fatalf("tags not an array: %T", parsed["tags"])
	}
	if len(tags) != 2 || tags[0] != "dev" {
		t.Errorf("tags = %v", tags)
	}
}

func TestRenderBody_BareArrayAutoJSON(t *testing.T) {
	// Bare {{.tags}} (without json func) should still produce valid JSON
	// for array values. This is the fix for the [meeting] vs ["meeting"] bug.
	tmpl := `{"summary":"test","tags":{{.tags}}}`
	data := map[string]any{
		"tags": []any{"meeting"},
	}
	got, err := renderBody(tmpl, data)
	if err != nil {
		t.Fatalf("renderBody: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v\ngot: %s", err, got)
	}
	tags, ok := parsed["tags"].([]any)
	if !ok {
		t.Fatalf("tags not an array: %T", parsed["tags"])
	}
	if len(tags) != 1 || tags[0] != "meeting" {
		t.Errorf("tags = %v, want [meeting]", tags)
	}
}

func TestRenderBody_BareObjectAutoJSON(t *testing.T) {
	// Bare {{.metadata}} should produce valid JSON for map values.
	tmpl := `{"data":{{.metadata}}}`
	data := map[string]any{
		"metadata": map[string]any{"key": "value"},
	}
	got, err := renderBody(tmpl, data)
	if err != nil {
		t.Fatalf("renderBody: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v\ngot: %s", err, got)
	}
}

func TestRenderBody_ScalarUnchanged(t *testing.T) {
	// Scalar values should still render normally (no extra quoting).
	tmpl := `{"name":"{{.name}}","count":{{.count}}}`
	data := map[string]any{"name": "test", "count": 42}
	got, err := renderBody(tmpl, data)
	if err != nil {
		t.Fatalf("renderBody: %v", err)
	}
	if got != `{"name":"test","count":42}` {
		t.Fatalf("got %q", got)
	}
}

func TestRenderResponse(t *testing.T) {
	tmpl := `Status: {{.status}}, Name: {{.data.name}}`
	data := map[string]any{
		"status": 200,
		"data":   map[string]any{"name": "alice"},
	}
	got, err := renderResponse(tmpl, data)
	if err != nil {
		t.Fatalf("renderResponse: %v", err)
	}
	if got != "Status: 200, Name: alice" {
		t.Fatalf("got %q", got)
	}
}

func TestRenderResponse_MissingTopLevelKey(t *testing.T) {
	// missingkey=zero tolerates missing top-level keys (produces "<no value>").
	tmpl := `Name: {{.missing_field}}`
	data := map[string]any{"status": 200}
	got, err := renderResponse(tmpl, data)
	if err != nil {
		t.Fatalf("renderResponse should tolerate missing top-level keys, got: %v", err)
	}
	if !strings.Contains(got, "Name:") {
		t.Fatalf("unexpected output: %q", got)
	}
}
