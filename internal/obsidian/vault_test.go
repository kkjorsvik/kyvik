package obsidian

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// writeFile is a helper that writes content to a file inside dir, creating
// subdirectories as needed.
func writeFile(t *testing.T, dir, relPath, content string) string {
	t.Helper()
	full := filepath.Join(dir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", relPath, err)
	}
	return full
}

// -------------------------------------------------------------------------
// ResolveVaultPath
// -------------------------------------------------------------------------

func TestResolveVaultPath_Valid(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "notes/hello.md", "# Hello")

	got, err := ResolveVaultPath(dir, "notes/hello.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(dir, "notes/hello.md")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveVaultPath_NonexistentFile(t *testing.T) {
	dir := t.TempDir()
	// File doesn't exist yet — should still resolve without error.
	got, err := ResolveVaultPath(dir, "new-note.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(dir, "new-note.md")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveVaultPath_AbsolutePathRejected(t *testing.T) {
	dir := t.TempDir()
	_, err := ResolveVaultPath(dir, "/etc/passwd")
	if err == nil {
		t.Fatal("expected error for absolute path, got nil")
	}
}

func TestResolveVaultPath_Escape(t *testing.T) {
	dir := t.TempDir()
	_, err := ResolveVaultPath(dir, "../../etc/passwd")
	if err == nil {
		t.Fatal("expected error for path escaping vault root, got nil")
	}
}

func TestResolveVaultPath_EscapeWithDotDot(t *testing.T) {
	dir := t.TempDir()
	_, err := ResolveVaultPath(dir, "notes/../../../sensitive")
	if err == nil {
		t.Fatal("expected error for path escaping vault root, got nil")
	}
}

// -------------------------------------------------------------------------
// ExtractTags — inline
// -------------------------------------------------------------------------

func TestExtractTags_Inline(t *testing.T) {
	content := `# My Note

This is tagged #go and also #project/kyvik and #work-item.
Another line with #tag2.
`
	tags := ExtractTags(content)
	wantSet := map[string]bool{"go": true, "project/kyvik": true, "work-item": true, "tag2": true}
	for _, tag := range tags {
		if !wantSet[tag] {
			t.Errorf("unexpected tag %q", tag)
		}
		delete(wantSet, tag)
	}
	for remaining := range wantSet {
		t.Errorf("missing expected tag %q", remaining)
	}
}

func TestExtractTags_PureNumericIgnored(t *testing.T) {
	content := "See issue #123 and PR #456."
	tags := ExtractTags(content)
	for _, tag := range tags {
		if tag == "123" || tag == "456" {
			t.Errorf("pure-numeric tag %q should be excluded", tag)
		}
	}
}

func TestExtractTags_MustStartWithLetter(t *testing.T) {
	content := "Ref #_private and #-dash and #valid."
	tags := ExtractTags(content)
	for _, tag := range tags {
		if tag == "_private" || tag == "-dash" {
			t.Errorf("tag %q should not be extracted (doesn't start with letter)", tag)
		}
	}
	found := false
	for _, tag := range tags {
		if tag == "valid" {
			found = true
		}
	}
	if !found {
		t.Error("expected tag 'valid' to be extracted")
	}
}

// -------------------------------------------------------------------------
// ExtractTags — frontmatter
// -------------------------------------------------------------------------

func TestExtractTags_Frontmatter_ArrayForm(t *testing.T) {
	content := `---
title: My Note
tags: [go, obsidian, project/kyvik]
---

Body text.
`
	tags := ExtractTags(content)
	wantSet := map[string]bool{"go": true, "obsidian": true, "project/kyvik": true}
	for _, tag := range tags {
		delete(wantSet, tag)
	}
	for remaining := range wantSet {
		t.Errorf("missing frontmatter tag %q", remaining)
	}
}

func TestExtractTags_Frontmatter_ListForm(t *testing.T) {
	content := `---
title: My Note
tags:
  - go
  - obsidian
  - project/kyvik
---

Body text.
`
	tags := ExtractTags(content)
	wantSet := map[string]bool{"go": true, "obsidian": true, "project/kyvik": true}
	for _, tag := range tags {
		delete(wantSet, tag)
	}
	for remaining := range wantSet {
		t.Errorf("missing frontmatter list tag %q", remaining)
	}
}

func TestExtractTags_Frontmatter_NoBodyTagsLeakage(t *testing.T) {
	// Tags in frontmatter should not be extracted again from body.
	content := `---
tags: [mytag]
---

This note has #mytag inline too.
`
	tags := ExtractTags(content)
	count := 0
	for _, tag := range tags {
		if tag == "mytag" {
			count++
		}
	}
	// Deduplicated — should appear at most once.
	if count != 1 {
		t.Errorf("expected 'mytag' exactly once, got %d times", count)
	}
}

// -------------------------------------------------------------------------
// ExtractTags — code blocks
// -------------------------------------------------------------------------

func TestExtractTags_IgnoreCodeBlocks(t *testing.T) {
	content := "Before #real\n" +
		"```\n" +
		"code #fake\n" +
		"```\n" +
		"After #also-real\n"
	tags := ExtractTags(content)
	tagSet := make(map[string]bool)
	for _, tag := range tags {
		tagSet[tag] = true
	}
	if tagSet["fake"] {
		t.Error("tag inside code block should be ignored")
	}
	if !tagSet["real"] {
		t.Error("tag 'real' before code block should be present")
	}
	if !tagSet["also-real"] {
		t.Error("tag 'also-real' after code block should be present")
	}
}

// -------------------------------------------------------------------------
// ExtractOutgoingLinks
// -------------------------------------------------------------------------

func TestExtractOutgoingLinks_Wikilinks(t *testing.T) {
	content := `
See [[Daily Notes/2024-01-01]] and [[Project Overview|Alias Name]].
Also [[simple-note]].
`
	links := ExtractOutgoingLinks(content)
	wantSet := map[string]bool{
		"Daily Notes/2024-01-01": true,
		"Project Overview":       true,
		"simple-note":            true,
	}
	for _, link := range links {
		if !wantSet[link] {
			t.Errorf("unexpected link %q", link)
		}
		delete(wantSet, link)
	}
	for remaining := range wantSet {
		t.Errorf("missing expected link %q", remaining)
	}
}

func TestExtractOutgoingLinks_MarkdownLinks(t *testing.T) {
	content := `
See [the readme](README.md) and [overview](docs/overview.md).
But not [external](https://example.com).
`
	links := ExtractOutgoingLinks(content)
	wantSet := map[string]bool{
		"README.md":        true,
		"docs/overview.md": true,
	}
	for _, link := range links {
		if !wantSet[link] {
			t.Errorf("unexpected link %q", link)
		}
		delete(wantSet, link)
	}
	for remaining := range wantSet {
		t.Errorf("missing expected link %q", remaining)
	}
}

func TestExtractOutgoingLinks_Mixed(t *testing.T) {
	content := `[[WikiLink]] and [text](file.md)`
	links := ExtractOutgoingLinks(content)
	if len(links) != 2 {
		t.Errorf("expected 2 links, got %d: %v", len(links), links)
	}
}

func TestExtractOutgoingLinks_Deduplication(t *testing.T) {
	content := `[[Note]] and [[Note]] again.`
	links := ExtractOutgoingLinks(content)
	if len(links) != 1 {
		t.Errorf("expected 1 deduplicated link, got %d: %v", len(links), links)
	}
}

// -------------------------------------------------------------------------
// FindBacklinks
// -------------------------------------------------------------------------

func TestFindBacklinks(t *testing.T) {
	dir := t.TempDir()

	writeFile(t, dir, "target.md", "# Target")
	writeFile(t, dir, "linker1.md", "This links to [[target]].")
	writeFile(t, dir, "linker2.md", "See [target](target.md).")
	writeFile(t, dir, "no-link.md", "No relevant links here.")
	// .obsidian dir should be skipped.
	writeFile(t, dir, ".obsidian/workspace.json", `{"key":"value"}`)

	backlinks, err := FindBacklinks(dir, "target.md")
	if err != nil {
		t.Fatalf("FindBacklinks: %v", err)
	}

	sort.Strings(backlinks)
	want := []string{
		filepath.Join(dir, "linker1.md"),
		filepath.Join(dir, "linker2.md"),
	}
	sort.Strings(want)

	if len(backlinks) != len(want) {
		t.Fatalf("got %d backlinks, want %d: %v", len(backlinks), len(want), backlinks)
	}
	for i := range want {
		if backlinks[i] != want[i] {
			t.Errorf("backlink[%d]: got %q, want %q", i, backlinks[i], want[i])
		}
	}
}

func TestFindBacklinks_NoMatches(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "note.md", "No links here.")

	backlinks, err := FindBacklinks(dir, "missing.md")
	if err != nil {
		t.Fatalf("FindBacklinks: %v", err)
	}
	if len(backlinks) != 0 {
		t.Errorf("expected no backlinks, got %v", backlinks)
	}
}

// -------------------------------------------------------------------------
// ListTags
// -------------------------------------------------------------------------

func TestListTags(t *testing.T) {
	dir := t.TempDir()

	writeFile(t, dir, "a.md", "Hello #go #obsidian")
	writeFile(t, dir, "b.md", "World #go #project/kyvik")
	writeFile(t, dir, "c.md", "More #obsidian")
	// .obsidian dir should be skipped.
	writeFile(t, dir, ".obsidian/config", "#should-not-appear")

	counts, err := ListTags(dir)
	if err != nil {
		t.Fatalf("ListTags: %v", err)
	}

	expectations := map[string]int{
		"go":            2,
		"obsidian":      2,
		"project/kyvik": 1,
	}
	for tag, wantCount := range expectations {
		if got := counts[tag]; got != wantCount {
			t.Errorf("tag %q: got count %d, want %d", tag, got, wantCount)
		}
	}
	if counts["should-not-appear"] != 0 {
		t.Error("tag from .obsidian directory should not be counted")
	}
}

// -------------------------------------------------------------------------
// SearchNotes
// -------------------------------------------------------------------------

func TestSearchNotes(t *testing.T) {
	dir := t.TempDir()

	writeFile(t, dir, "alpha.md", "The quick brown fox jumps over the lazy dog.")
	writeFile(t, dir, "beta.md", "Pack my box with five dozen liquor jugs.")
	writeFile(t, dir, "gamma.md", "No match here at all.")
	// .obsidian dir should be skipped.
	writeFile(t, dir, ".obsidian/cache", "fox")

	results, err := SearchNotes(dir, "fox", 10)
	if err != nil {
		t.Fatalf("SearchNotes: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d: %v", len(results), results)
	}
	if results[0].Path != filepath.Join(dir, "alpha.md") {
		t.Errorf("wrong path: got %q", results[0].Path)
	}
	if results[0].Line != 1 {
		t.Errorf("expected line 1, got %d", results[0].Line)
	}
	if results[0].Snippet == "" {
		t.Error("snippet should not be empty")
	}
}

func TestSearchNotes_CaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "note.md", "The Quick Brown FOX")

	results, err := SearchNotes(dir, "quick brown fox", 10)
	if err != nil {
		t.Fatalf("SearchNotes: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}

func TestSearchNotes_Limit(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		writeFile(t, dir, filepath.Join("sub", string(rune('a'+i))+".md"), "match me")
	}

	results, err := SearchNotes(dir, "match me", 3)
	if err != nil {
		t.Fatalf("SearchNotes: %v", err)
	}
	if len(results) > 3 {
		t.Errorf("limit=3 but got %d results", len(results))
	}
}

func TestSearchNotes_EmptyQuery(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "note.md", "content")

	results, err := SearchNotes(dir, "", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if results != nil {
		t.Errorf("expected nil results for empty query, got %v", results)
	}
}

func TestSearchNotes_NoMatches(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "note.md", "nothing relevant")

	results, err := SearchNotes(dir, "xyzzy-not-found", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected no results, got %d", len(results))
	}
}
