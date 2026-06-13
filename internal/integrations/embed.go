package integrations

import "embed"

// BuiltinTemplates contains the embedded built-in integration template YAML files.
//
//go:embed templates/*.yaml
var BuiltinTemplates embed.FS
