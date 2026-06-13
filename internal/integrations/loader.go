package integrations

import (
	"embed"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Loader scans directories for integration template YAML files.
type Loader struct {
	builtinFS  embed.FS // embedded built-in templates
	localDir   string   // user-created templates directory
}

// NewLoader creates a Loader that reads built-in templates from an embed.FS
// and user-created templates from a local directory.
func NewLoader(builtinFS embed.FS, localDir string) (*Loader, error) {
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		return nil, fmt.Errorf("create local integrations directory: %w", err)
	}
	return &Loader{builtinFS: builtinFS, localDir: localDir}, nil
}

// LoadAll loads all integration templates (built-in + local).
func (l *Loader) LoadAll() ([]Template, error) {
	var templates []Template

	// Load built-in templates from embedded FS.
	builtins, err := l.loadFromEmbedFS()
	if err != nil {
		log.Printf("[integrations] warning: failed to load built-in templates: %v", err)
	} else {
		templates = append(templates, builtins...)
	}

	// Load local templates from disk.
	local, err := l.loadFromDir(l.localDir, "local")
	if err != nil {
		log.Printf("[integrations] warning: failed to load local templates: %v", err)
	} else {
		templates = append(templates, local...)
	}

	return templates, nil
}

// LoadLocal loads only local (user-created) templates.
func (l *Loader) LoadLocal() ([]Template, error) {
	return l.loadFromDir(l.localDir, "local")
}

// LocalDir returns the directory path for user-created templates.
func (l *Loader) LocalDir() string { return l.localDir }

// loadFromEmbedFS loads templates from the embedded filesystem.
func (l *Loader) loadFromEmbedFS() ([]Template, error) {
	var templates []Template

	err := fs.WalkDir(l.builtinFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip errors
		}
		if d.IsDir() || !isYAMLFile(path) {
			return nil
		}

		data, err := l.builtinFS.ReadFile(path)
		if err != nil {
			log.Printf("[integrations] warning: failed to read embedded %s: %v", path, err)
			return nil
		}

		tmpl, err := parseTemplate(data)
		if err != nil {
			log.Printf("[integrations] warning: failed to parse embedded %s: %v", path, err)
			return nil
		}

		tmpl.Source = "builtin"
		tmpl.FilePath = path
		templates = append(templates, *tmpl)
		return nil
	})

	return templates, err
}

// loadFromDir loads templates from a filesystem directory.
func (l *Loader) loadFromDir(dir, source string) ([]Template, error) {
	var templates []Template

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read directory %s: %w", dir, err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !isYAMLFile(entry.Name()) {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			log.Printf("[integrations] warning: failed to read %s: %v", path, err)
			continue
		}

		tmpl, err := parseTemplate(data)
		if err != nil {
			log.Printf("[integrations] warning: failed to parse %s: %v", path, err)
			continue
		}

		tmpl.Source = source
		tmpl.FilePath = path
		templates = append(templates, *tmpl)
	}

	return templates, nil
}

// parseTemplate parses a YAML byte slice into a Template.
func parseTemplate(data []byte) (*Template, error) {
	var tmpl Template
	if err := yaml.Unmarshal(data, &tmpl); err != nil {
		return nil, fmt.Errorf("yaml parse error: %w", err)
	}
	if tmpl.Name == "" {
		return nil, fmt.Errorf("template missing required field: name")
	}
	if len(tmpl.Endpoints) == 0 {
		return nil, fmt.Errorf("template %q has no endpoints", tmpl.Name)
	}
	if tmpl.Version == "" {
		tmpl.Version = "1.0.0"
	}
	if tmpl.DisplayName == "" {
		tmpl.DisplayName = tmpl.Name
	}
	return &tmpl, nil
}

func isYAMLFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".yaml" || ext == ".yml"
}
