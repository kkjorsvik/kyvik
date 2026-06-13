package restapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"text/template"
)

// jsonWrapped is a marker type for values that have already been
// JSON-serialized by wrapDataForJSON. The json template function
// detects this and returns the raw JSON instead of double-encoding.
type jsonWrapped string

// templateFuncs provides custom functions available in all REST API templates.
var templateFuncs = template.FuncMap{
	// json marshals a value to a JSON string. Useful for nested objects/arrays
	// that would otherwise render as Go's native fmt output (e.g. map[k:v]).
	"json": func(v any) (string, error) {
		// If value was already JSON-wrapped by wrapDataForJSON, return as-is.
		if jw, ok := v.(jsonWrapped); ok {
			return string(jw), nil
		}
		// Handle nil/missing values gracefully — produce valid JSON null
		// instead of letting Go's template engine render "<no value>".
		if v == nil {
			return "null", nil
		}
		b, err := json.Marshal(v)
		if err != nil {
			return "", fmt.Errorf("json template func: %w", err)
		}
		return string(b), nil
	},
}

// wrapDataForJSON returns a copy of data where slice/map values are
// pre-serialized to JSON so they render correctly when used in body templates
// with bare {{.field}} syntax. Without this, Go's text/template uses
// fmt.Sprint which produces [value] instead of ["value"] — invalid JSON.
func wrapDataForJSON(data map[string]any) map[string]any {
	if data == nil {
		return nil
	}
	wrapped := make(map[string]any, len(data))
	for k, v := range data {
		switch v.(type) {
		case []any, []string, map[string]any:
			b, err := json.Marshal(v)
			if err != nil {
				wrapped[k] = v
			} else {
				wrapped[k] = jsonWrapped(b)
			}
		default:
			wrapped[k] = v
		}
	}
	return wrapped
}

// renderTemplate parses and executes a Go text/template with the given data.
func renderTemplate(name, tmplStr string, data map[string]any, missingKeyOpt string) (string, error) {
	if tmplStr == "" {
		return "", nil
	}
	t, err := template.New(name).Funcs(templateFuncs).Option("missingkey=" + missingKeyOpt).Parse(tmplStr)
	if err != nil {
		return "", fmt.Errorf("parse template %s: %w", name, err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("execute template %s: %w", name, err)
	}
	return buf.String(), nil
}

// renderURL renders the URL template with strict missing-key behavior.
func renderURL(tmplStr string, data map[string]any) (string, error) {
	return renderTemplate("url", tmplStr, data, "error")
}

// renderHeaders renders each header value template with strict missing-key behavior.
func renderHeaders(headers map[string]string, data map[string]any) (map[string]string, error) {
	if len(headers) == 0 {
		return nil, nil
	}
	result := make(map[string]string, len(headers))
	for k, v := range headers {
		rendered, err := renderTemplate("header:"+k, v, data, "error")
		if err != nil {
			return nil, fmt.Errorf("header %s: %w", k, err)
		}
		result[k] = rendered
	}
	return result, nil
}

// renderQueryParams renders each query parameter value template with tolerant
// missing-key behavior so that optional params with defaults (e.g.
// {{or .units "metric"}}) work without error when the caller omits the key.
func renderQueryParams(params map[string]string, data map[string]any) (map[string]string, error) {
	if len(params) == 0 {
		return nil, nil
	}
	result := make(map[string]string, len(params))
	for k, v := range params {
		rendered, err := renderTemplate("param:"+k, v, data, "zero")
		if err != nil {
			return nil, fmt.Errorf("query param %s: %w", k, err)
		}
		result[k] = rendered
	}
	return result, nil
}

// renderBody renders the body template with tolerant missing-key behavior.
// Templates encode required vs optional through syntax: bare {{.field}} for
// required fields (renders as zero value — API server rejects with meaningful
// error), {{if .field}} for optional fields, {{or .field "default"}} for defaults.
// Using missingkey=error would cause Go to evaluate the variable access before
// conditional logic runs, making {{if .field}} and {{or .field "default"}} fail
// on missing keys.
func renderBody(tmplStr string, data map[string]any) (string, error) {
	return renderTemplate("body", tmplStr, wrapDataForJSON(data), "zero")
}

// renderResponse renders the response template with tolerant missing-key behavior.
// This allows response templates to reference fields that may not exist in the API response.
func renderResponse(tmplStr string, data map[string]any) (string, error) {
	return renderTemplate("response", tmplStr, data, "zero")
}
