// Package builtins provides embedded built-in skills that ship with the Kyvik binary.
package builtins

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed all:code-reviewer all:data-summarizer all:devops-runbook all:email-assistant all:file-manager all:project-tracker all:report-builder all:research-analyst all:system-auditor all:system-docs all:web-researcher
var content embed.FS

// Skills returns the names of all built-in skills.
func Skills() []string {
	return []string{
		"code-reviewer",
		"data-summarizer",
		"devops-runbook",
		"email-assistant",
		"file-manager",
		"project-tracker",
		"report-builder",
		"research-analyst",
		"system-auditor",
		"system-docs",
		"web-researcher",
	}
}

// Install extracts embedded built-in skills to the given skills directory.
// Skills are written to {skillsDir}/built-in/{name}/.
// Always overwrites existing files to ensure the binary version matches disk.
// Returns the number of skills installed.
func Install(skillsDir string) (int, error) {
	builtInDir := filepath.Join(skillsDir, "built-in")

	for _, name := range Skills() {
		if err := installSkill(builtInDir, name); err != nil {
			return 0, fmt.Errorf("install built-in skill %s: %w", name, err)
		}
	}

	return len(Skills()), nil
}

// installSkill extracts a single embedded skill directory to disk.
func installSkill(builtInDir, name string) error {
	return fs.WalkDir(content, name, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		destPath := filepath.Join(builtInDir, path)

		if d.IsDir() {
			return os.MkdirAll(destPath, 0o755)
		}

		data, err := content.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read embedded file %s: %w", path, err)
		}

		if err := os.WriteFile(destPath, data, 0o644); err != nil {
			return fmt.Errorf("write file %s: %w", destPath, err)
		}

		return nil
	})
}
