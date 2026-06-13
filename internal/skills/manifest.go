// Package skills provides skill manifest parsing and validation.
package skills

import (
	"fmt"
	"os"
	"regexp"

	"gopkg.in/yaml.v3"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

// namePattern enforces lowercase alphanumeric with hyphens, starting with alnum.
var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// ParseManifest reads a skill.yaml file from path and returns a validated SkillManifest.
func ParseManifest(path string) (*types.SkillManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}

	var m types.SkillManifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest YAML: %w", err)
	}

	if err := ValidateManifest(&m); err != nil {
		return nil, err
	}

	return &m, nil
}

// ValidateManifest checks that all required fields are present and correctly formatted.
func ValidateManifest(m *types.SkillManifest) error {
	if m.Name == "" {
		return fmt.Errorf("manifest validation: name is required")
	}
	if !namePattern.MatchString(m.Name) {
		return fmt.Errorf("manifest validation: name must match %s", namePattern.String())
	}
	if m.Version == "" {
		return fmt.Errorf("manifest validation: version is required")
	}
	if m.Description == "" {
		return fmt.Errorf("manifest validation: description is required")
	}
	return nil
}
