package obsidian

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// SearchResult represents a search match within a note.
type SearchResult struct {
	Path    string `json:"path"`
	Snippet string `json:"snippet"`
	Line    int    `json:"line"`
}

var (
	// tagRe matches inline Obsidian tags. Must start with a letter; allows nested tags with /.
	tagRe = regexp.MustCompile(`(?:^|\s)#([a-zA-Z][a-zA-Z0-9_/\-]*)`)
	// wikilinkRe matches [[wikilinks]].
	wikilinkRe = regexp.MustCompile(`\[\[([^\]]+)\]\]`)
	// mdLinkRe matches [text](path.md) markdown links.
	mdLinkRe = regexp.MustCompile(`\[([^\]]*)\]\(([^)]+\.md)\)`)
	// codeFenceRe matches opening/closing code fences.
	codeFenceRe = regexp.MustCompile("^```")
	// frontmatterTagsArrayRe matches "tags: [tag1, tag2]" in YAML frontmatter.
	frontmatterTagsArrayRe = regexp.MustCompile(`^tags:\s*\[([^\]]*)\]`)
	// frontmatterTagsLineRe matches a "  - tagname" list item in YAML frontmatter.
	frontmatterTagsListItemRe = regexp.MustCompile(`^\s*-\s+(.+)`)
	// pureNumericRe identifies tags that are purely numeric (to exclude).
	pureNumericRe = regexp.MustCompile(`^[0-9]+$`)
)

// ResolveVaultPath resolves a relative note path within vaultRoot.
// It rejects absolute paths and validates the resolved path cannot escape
// the vault root (directory traversal prevention).
func ResolveVaultPath(vaultRoot, notePath string) (string, error) {
	if filepath.IsAbs(notePath) {
		return "", fmt.Errorf("note path must be relative, got absolute path: %s", notePath)
	}

	// Resolve the vault root to its real absolute path.
	realRoot, err := filepath.EvalSymlinks(vaultRoot)
	if err != nil {
		return "", fmt.Errorf("cannot resolve vault root %q: %w", vaultRoot, err)
	}

	// Build the candidate path.
	candidate := filepath.Join(realRoot, notePath)

	// Resolve symlinks in the candidate path. If the file doesn't exist yet,
	// resolve as far as possible by cleaning the path.
	realCandidate, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		// File may not exist yet — use Clean to strip traversal sequences.
		realCandidate = filepath.Clean(candidate)
	}

	// Ensure realCandidate is within realRoot.
	if !strings.HasPrefix(realCandidate, realRoot+string(filepath.Separator)) &&
		realCandidate != realRoot {
		return "", fmt.Errorf("path %q escapes vault root", notePath)
	}

	return realCandidate, nil
}

// ExtractTags extracts all Obsidian tags from markdown content.
// It handles inline tags (#tag), YAML frontmatter tag arrays, and nested
// tags like #project/kyvik. Tags inside code blocks are ignored.
// Pure-numeric patterns like #123 are excluded.
func ExtractTags(content string) []string {
	seen := make(map[string]bool)
	var tags []string

	addTag := func(t string) {
		t = strings.TrimSpace(t)
		if t == "" || pureNumericRe.MatchString(t) {
			return
		}
		if !seen[t] {
			seen[t] = true
			tags = append(tags, t)
		}
	}

	lines := strings.Split(content, "\n")

	inFrontmatter := false
	frontmatterDone := false
	frontmatterOpened := false
	inTagsList := false // true when inside a YAML tags: list block
	inCodeBlock := false

	for i, line := range lines {
		// Handle YAML frontmatter (must start at line 0).
		if i == 0 && line == "---" {
			inFrontmatter = true
			frontmatterOpened = true
			continue
		}
		if inFrontmatter {
			if line == "---" || line == "..." {
				inFrontmatter = false
				frontmatterDone = true
				inTagsList = false
				continue
			}

			// tags: [tag1, tag2] — inline array form.
			if m := frontmatterTagsArrayRe.FindStringSubmatch(line); m != nil {
				inTagsList = false
				for _, part := range strings.Split(m[1], ",") {
					addTag(strings.Trim(strings.TrimSpace(part), `"`))
				}
				continue
			}

			// tags: — block list form (value continues on following lines).
			if strings.HasPrefix(strings.TrimSpace(line), "tags:") && !strings.Contains(line, "[") {
				inTagsList = true
				// Check if there's an inline value after "tags:".
				afterColon := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "tags:"))
				if afterColon != "" {
					addTag(strings.Trim(afterColon, `"`))
					inTagsList = false
				}
				continue
			}

			// List items under tags:.
			if inTagsList {
				if m := frontmatterTagsListItemRe.FindStringSubmatch(line); m != nil {
					addTag(strings.Trim(m[1], `"`))
					continue
				}
				// Non-list-item line ends the tags block.
				inTagsList = false
			}

			continue
		}

		// Skip frontmatter opening that was never closed (treat as regular content).
		_ = frontmatterDone
		_ = frontmatterOpened

		// Track code fences to ignore tags inside them.
		if codeFenceRe.MatchString(line) {
			inCodeBlock = !inCodeBlock
			continue
		}
		if inCodeBlock {
			continue
		}

		// Extract inline tags from body lines.
		matches := tagRe.FindAllStringSubmatch(line, -1)
		for _, m := range matches {
			addTag(m[1])
		}
	}

	return tags
}

// ExtractOutgoingLinks extracts all outgoing links from markdown content.
// It finds both [[wikilinks]] and [text](path.md) markdown links.
func ExtractOutgoingLinks(content string) []string {
	seen := make(map[string]bool)
	var links []string

	add := func(link string) {
		link = strings.TrimSpace(link)
		if link != "" && !seen[link] {
			seen[link] = true
			links = append(links, link)
		}
	}

	// Wikilinks: [[target]] or [[target|alias]].
	for _, m := range wikilinkRe.FindAllStringSubmatch(content, -1) {
		// Strip pipe alias if present: [[target|alias]] -> target.
		target := strings.SplitN(m[1], "|", 2)[0]
		add(target)
	}

	// Markdown links: [text](path.md).
	for _, m := range mdLinkRe.FindAllStringSubmatch(content, -1) {
		add(m[2])
	}

	return links
}

// isObsidianDir returns true if the directory entry is the .obsidian config dir.
func isObsidianDir(d fs.DirEntry) bool {
	return d.IsDir() && d.Name() == ".obsidian"
}

// walkMDFiles calls fn for every .md file in vaultRoot, skipping .obsidian/.
func walkMDFiles(vaultRoot string, fn func(path string) error) error {
	return filepath.WalkDir(vaultRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if isObsidianDir(d) {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		return fn(path)
	})
}

// FindBacklinks walks all .md files in vaultRoot and returns the paths of notes
// that contain links pointing to targetNote.
func FindBacklinks(vaultRoot, targetNote string) ([]string, error) {
	// Normalise targetNote: strip .md suffix for wikilink matching.
	targetBase := strings.TrimSuffix(targetNote, ".md")
	// Also keep the raw name for markdown-link matching.
	targetRaw := targetNote

	var backlinks []string

	err := walkMDFiles(vaultRoot, func(path string) error {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		content := string(data)
		links := ExtractOutgoingLinks(content)
		for _, link := range links {
			linkBase := strings.TrimSuffix(link, ".md")
			if linkBase == targetBase || link == targetRaw {
				backlinks = append(backlinks, path)
				return nil
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("finding backlinks: %w", err)
	}

	return backlinks, nil
}

// ListTags walks all .md files in vaultRoot and returns a map of tag -> count.
func ListTags(vaultRoot string) (map[string]int, error) {
	counts := make(map[string]int)

	err := walkMDFiles(vaultRoot, func(path string) error {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, tag := range ExtractTags(string(data)) {
			counts[tag]++
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("listing tags: %w", err)
	}

	return counts, nil
}

// SearchNotes performs a case-insensitive substring search across all .md files
// in vaultRoot. It returns up to limit matching results, each with a snippet
// and line number. A limit <= 0 means no limit.
func SearchNotes(vaultRoot, query string, limit int) ([]SearchResult, error) {
	if query == "" {
		return nil, nil
	}
	queryLower := strings.ToLower(query)

	var results []SearchResult

	err := walkMDFiles(vaultRoot, func(path string) error {
		if limit > 0 && len(results) >= limit {
			return nil
		}

		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()

		scanner := bufio.NewScanner(f)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			if strings.Contains(strings.ToLower(line), queryLower) {
				snippet := strings.TrimSpace(line)
				if len(snippet) > 200 {
					snippet = snippet[:200] + "..."
				}
				results = append(results, SearchResult{
					Path:    path,
					Snippet: snippet,
					Line:    lineNum,
				})
				// One match per file is sufficient for backlink/search purposes;
				// callers can re-read the file for full context.
				break
			}
		}
		return scanner.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("searching notes: %w", err)
	}

	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}

	return results, nil
}
