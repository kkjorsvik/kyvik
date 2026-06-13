package skills

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

// Trust tier subdirectory names.
const (
	dirBuiltIn   = "built-in"
	dirVerified  = "verified"
	dirCommunity = "community"
	dirLocal     = "local"
)

// tierDirs maps subdirectory names to trust tiers.
var tierDirs = map[string]types.TrustTier{
	dirBuiltIn:   types.TrustBuiltIn,
	dirVerified:  types.TrustVerified,
	dirCommunity: types.TrustCommunity,
	dirLocal:     types.TrustLocal,
}

// Loader scans a directory tree for skills and builds a catalog.
type Loader struct {
	baseDir string
}

// BaseDir returns the loader's base directory path.
func (l *Loader) BaseDir() string { return l.baseDir }

// NewLoader creates a loader for the given base directory.
// Creates the directory structure if it doesn't exist.
func NewLoader(baseDir string) (*Loader, error) {
	for _, sub := range []string{dirBuiltIn, dirCommunity, dirLocal} {
		dir := filepath.Join(baseDir, sub)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create skills directory %s: %w", dir, err)
		}
	}
	return &Loader{baseDir: baseDir}, nil
}

// LoadAll scans all subdirectories and returns a catalog of valid skills.
// Invalid skills are logged as warnings but don't prevent other skills from loading.
func (l *Loader) LoadAll() ([]types.Skill, error) {
	var skills []types.Skill

	entries, err := os.ReadDir(l.baseDir)
	if err != nil {
		return nil, fmt.Errorf("read skills directory: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		subPath := filepath.Join(l.baseDir, entry.Name())

		// Check if this is a tier directory.
		if _, isTier := tierDirs[entry.Name()]; isTier {
			tierSkills, err := l.loadTierDir(subPath, entry.Name())
			if err != nil {
				log.Printf("[skills] warning: error scanning %s: %v", subPath, err)
				continue
			}
			skills = append(skills, tierSkills...)
			continue
		}

		// Bare directory under baseDir — treat as TrustLocal.
		sk, err := l.LoadSkill(subPath)
		if err != nil {
			log.Printf("[skills] warning: skipping %s: %v", entry.Name(), err)
			continue
		}
		skills = append(skills, *sk)
	}

	return skills, nil
}

// loadTierDir loads all skill directories within a tier subdirectory.
func (l *Loader) loadTierDir(tierPath, tierName string) ([]types.Skill, error) {
	entries, err := os.ReadDir(tierPath)
	if err != nil {
		return nil, fmt.Errorf("read tier directory: %w", err)
	}

	var skills []types.Skill
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillPath := filepath.Join(tierPath, entry.Name())
		sk, err := l.LoadSkill(skillPath)
		if err != nil {
			log.Printf("[skills] warning: skipping %s/%s: %v", tierName, entry.Name(), err)
			continue
		}
		skills = append(skills, *sk)
	}
	return skills, nil
}

// LoadSkill loads a single skill from its directory path.
func (l *Loader) LoadSkill(dir string) (*types.Skill, error) {
	manifestPath := filepath.Join(dir, "skill.yaml")
	manifest, err := ParseManifest(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("load skill from %s: %w", dir, err)
	}

	sk := types.Skill{
		Name:        manifest.Name,
		Version:     manifest.Version,
		Description: manifest.Description,
		Author:      manifest.Author,
		Trust:       l.assignTrust(dir),
		Manifest:    *manifest,
		LoadedAt:    time.Now(),
	}

	// Check for SKILL.md documentation.
	docPath := filepath.Join(dir, "SKILL.md")
	if data, err := os.ReadFile(docPath); err == nil {
		sk.HasDocs = true
		sk.DocContent = string(data)
	}

	// Check for prompts/ directory and load .md files.
	promptsDir := filepath.Join(dir, "prompts")
	if info, err := os.Stat(promptsDir); err == nil && info.IsDir() {
		content, err := loadPrompts(promptsDir)
		if err != nil {
			return nil, fmt.Errorf("load prompts for %s: %w", manifest.Name, err)
		}
		if content != "" {
			sk.HasPrompts = true
			sk.PromptContent = content
		}
	}

	// Check for tools/ directory.
	toolsDir := filepath.Join(dir, "tools")
	if info, err := os.Stat(toolsDir); err == nil && info.IsDir() {
		sk.HasTools = true
	}

	return &sk, nil
}

// RefreshCatalog reloads all skills from disk.
func (l *Loader) RefreshCatalog() ([]types.Skill, error) {
	return l.LoadAll()
}

// assignTrust determines the trust tier based on the skill's parent directory.
func (l *Loader) assignTrust(skillDir string) types.TrustTier {
	rel, err := filepath.Rel(l.baseDir, skillDir)
	if err != nil {
		return types.TrustLocal
	}

	// The first path component is the tier directory.
	parts := strings.SplitN(filepath.ToSlash(rel), "/", 2)
	if len(parts) < 2 {
		// Directly under baseDir — no tier subdirectory.
		return types.TrustLocal
	}

	if tier, ok := tierDirs[parts[0]]; ok {
		return tier
	}
	return types.TrustLocal
}

// loadPrompts reads all .md files from the prompts directory, sorted alphabetically,
// and concatenates them with double newline separators.
func loadPrompts(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read prompts directory: %w", err)
	}

	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	var parts []string
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return "", fmt.Errorf("read prompt file %s: %w", name, err)
		}
		parts = append(parts, string(data))
	}

	return strings.Join(parts, "\n\n"), nil
}
