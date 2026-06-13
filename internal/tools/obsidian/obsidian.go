// Package obsidian implements a KTP inline tool for Obsidian vault operations.
package obsidian

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kkjorsvik/kyvik/internal/ktp"
	obs "github.com/kkjorsvik/kyvik/internal/obsidian"
)

// VaultAccessFunc returns the list of vault names an agent is allowed to access.
type VaultAccessFunc func(agentID string) ([]string, error)

// VaultPathFunc resolves a vault name to its filesystem root path.
type VaultPathFunc func(ctx context.Context, name string) (string, error)

// Tool implements ktp.Tool for Obsidian vault operations.
type Tool struct {
	vaultAccess VaultAccessFunc
	vaultPath   VaultPathFunc
}

// Option configures a Tool.
type Option func(*Tool)

// WithVaultAccess sets the function used to check vault access for an agent.
func WithVaultAccess(fn VaultAccessFunc) Option { return func(t *Tool) { t.vaultAccess = fn } }

// WithVaultPath sets the function used to resolve vault names to filesystem paths.
func WithVaultPath(fn VaultPathFunc) Option { return func(t *Tool) { t.vaultPath = fn } }

// New creates a Tool with the given options.
func New(opts ...Option) *Tool {
	t := &Tool{}
	for _, o := range opts {
		o(t)
	}
	return t
}

// Inline returns true because the obsidian tool accesses local files only
// and does not need sandbox isolation.
func (t *Tool) Inline() bool { return true }

// Declaration returns the obsidian tool's KTP declaration.
func (t *Tool) Declaration() ktp.ToolDeclaration {
	return ktp.ToolDeclaration{
		Name:         "obsidian",
		Version:      "1.0.0",
		Description:  "Read, write, search, and manage notes in Obsidian vaults",
		MinTier:      ktp.TierReader,
		DefaultTiers: []string{ktp.TierReader, ktp.TierWriter, ktp.TierOperator, ktp.TierAdmin},
		Actions: []ktp.ActionSpec{
			{
				Name:        "read",
				Description: "Read the contents of a note",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"vault": {Type: "string", Description: "Vault name"},
						"path":  {Type: "string", Description: "Relative path to the note"},
					},
					Required: []string{"vault", "path"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"content": {Type: "string"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "obsidian", Access: "read", Resource: "*"}},
			},
			{
				Name:        "write",
				Description: "Write content to a note (creates or overwrites)",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"vault":   {Type: "string", Description: "Vault name"},
						"path":    {Type: "string", Description: "Relative path to the note"},
						"content": {Type: "string", Description: "Content to write"},
					},
					Required: []string{"vault", "path", "content"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"written": {Type: "boolean"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "obsidian", Access: "write", Resource: "*"}},
			},
			{
				Name:        "edit",
				Description: "Replace a text fragment in a note",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"vault":    {Type: "string", Description: "Vault name"},
						"path":     {Type: "string", Description: "Relative path to the note"},
						"old_text": {Type: "string", Description: "Text to find and replace"},
						"new_text": {Type: "string", Description: "Replacement text"},
					},
					Required: []string{"vault", "path", "old_text", "new_text"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"edited": {Type: "boolean"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "obsidian", Access: "write", Resource: "*"}},
			},
			{
				Name:        "list",
				Description: "List markdown files in a vault folder",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"vault":     {Type: "string", Description: "Vault name"},
						"folder":    {Type: "string", Description: "Folder to list (empty for root)", Default: ""},
						"recursive": {Type: "boolean", Description: "List recursively", Default: false},
					},
					Required: []string{"vault"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"files": {Type: "array", Items: &ktp.JSONSchema{Type: "string"}},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "obsidian", Access: "read", Resource: "*"}},
			},
			{
				Name:        "search",
				Description: "Search for text across vault notes",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"vault": {Type: "string", Description: "Vault name"},
						"query": {Type: "string", Description: "Search query"},
						"limit": {Type: "integer", Description: "Max results to return", Default: 20},
					},
					Required: []string{"vault", "query"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"results": {Type: "array", Items: &ktp.JSONSchema{Type: "object"}},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "obsidian", Access: "read", Resource: "*"}},
			},
			{
				Name:        "delete",
				Description: "Delete a note from the vault",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"vault": {Type: "string", Description: "Vault name"},
						"path":  {Type: "string", Description: "Relative path to the note"},
					},
					Required: []string{"vault", "path"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"deleted": {Type: "boolean"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "obsidian", Access: "delete", Resource: "*"}},
				Destructive:          true,
			},
			{
				Name:        "move",
				Description: "Move or rename a note within the vault",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"vault":     {Type: "string", Description: "Vault name"},
						"from_path": {Type: "string", Description: "Current relative path"},
						"to_path":   {Type: "string", Description: "New relative path"},
					},
					Required: []string{"vault", "from_path", "to_path"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"moved": {Type: "boolean"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "obsidian", Access: "write", Resource: "*"}},
			},
			{
				Name:        "tags",
				Description: "List tags in the vault, optionally filtered by a specific tag",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"vault": {Type: "string", Description: "Vault name"},
						"tag":   {Type: "string", Description: "Filter to a specific tag (optional)"},
					},
					Required: []string{"vault"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"tags": {Type: "object"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "obsidian", Access: "read", Resource: "*"}},
			},
			{
				Name:        "links",
				Description: "Get outgoing links and backlinks for a note",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"vault": {Type: "string", Description: "Vault name"},
						"path":  {Type: "string", Description: "Relative path to the note"},
					},
					Required: []string{"vault", "path"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"outgoing":  {Type: "array", Items: &ktp.JSONSchema{Type: "string"}},
						"backlinks": {Type: "array", Items: &ktp.JSONSchema{Type: "string"}},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "obsidian", Access: "read", Resource: "*"}},
			},
		},
	}
}

// Execute dispatches to the requested action.
func (t *Tool) Execute(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	start := time.Now()

	switch req.Action {
	case "read":
		return t.read(ctx, req, start)
	case "write":
		return t.write(ctx, req, start)
	case "edit":
		return t.edit(ctx, req, start)
	case "list":
		return t.list(ctx, req, start)
	case "search":
		return t.search(ctx, req, start)
	case "delete":
		return t.doDelete(ctx, req, start)
	case "move":
		return t.move(ctx, req, start)
	case "tags":
		return t.tags(ctx, req, start)
	case "links":
		return t.links(ctx, req, start)
	default:
		return errResp(req.ID, "unknown action: "+req.Action), nil
	}
}

// resolveVault validates agent access and returns the vault root path.
func (t *Tool) resolveVault(ctx context.Context, req ktp.ToolRequest) (string, string, error) {
	vaultName, err := strParam(req.Parameters, "vault")
	if err != nil {
		return "", "", err
	}

	if t.vaultAccess != nil {
		allowed, err := t.vaultAccess(req.AgentID)
		if err != nil {
			return "", "", fmt.Errorf("checking vault access: %w", err)
		}
		found := false
		for _, v := range allowed {
			if v == vaultName {
				found = true
				break
			}
		}
		if !found {
			return "", "", fmt.Errorf("agent %q does not have access to vault %q", req.AgentID, vaultName)
		}
	}

	if t.vaultPath == nil {
		return "", "", fmt.Errorf("vault path resolver not configured")
	}

	root, err := t.vaultPath(ctx, vaultName)
	if err != nil {
		return "", "", fmt.Errorf("resolving vault %q: %w", vaultName, err)
	}

	return vaultName, root, nil
}

func (t *Tool) read(ctx context.Context, req ktp.ToolRequest, start time.Time) (*ktp.ToolResponse, error) {
	_, root, err := t.resolveVault(ctx, req)
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	notePath, err := strParam(req.Parameters, "path")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	absPath, err := obs.ResolveVaultPath(root, notePath)
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("failed to read note: %s", err)), nil
	}

	resp := ktp.NewToolResponse(req.ID, true, map[string]any{"content": string(data)}, "", ms(start))
	return &resp, nil
}

func (t *Tool) write(ctx context.Context, req ktp.ToolRequest, start time.Time) (*ktp.ToolResponse, error) {
	_, root, err := t.resolveVault(ctx, req)
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	notePath, err := strParam(req.Parameters, "path")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	content, err := strParam(req.Parameters, "content")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	absPath, err := obs.ResolveVaultPath(root, notePath)
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	if err := os.MkdirAll(filepath.Dir(absPath), 0755); err != nil {
		return errResp(req.ID, fmt.Sprintf("failed to create directory: %s", err)), nil
	}

	if err := os.WriteFile(absPath, []byte(content), 0644); err != nil {
		return errResp(req.ID, fmt.Sprintf("failed to write note: %s", err)), nil
	}

	resp := ktp.NewToolResponse(req.ID, true, map[string]any{"written": true}, "", ms(start))
	return &resp, nil
}

func (t *Tool) edit(ctx context.Context, req ktp.ToolRequest, start time.Time) (*ktp.ToolResponse, error) {
	_, root, err := t.resolveVault(ctx, req)
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	notePath, err := strParam(req.Parameters, "path")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	oldText, err := strParam(req.Parameters, "old_text")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	newText, err := strParam(req.Parameters, "new_text")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	absPath, err := obs.ResolveVaultPath(root, notePath)
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("failed to read note: %s", err)), nil
	}

	content := string(data)
	if !strings.Contains(content, oldText) {
		return errResp(req.ID, "old_text not found in note"), nil
	}

	updated := strings.Replace(content, oldText, newText, 1)
	if err := os.WriteFile(absPath, []byte(updated), 0644); err != nil {
		return errResp(req.ID, fmt.Sprintf("failed to write note: %s", err)), nil
	}

	resp := ktp.NewToolResponse(req.ID, true, map[string]any{"edited": true}, "", ms(start))
	return &resp, nil
}

func (t *Tool) list(ctx context.Context, req ktp.ToolRequest, start time.Time) (*ktp.ToolResponse, error) {
	_, root, err := t.resolveVault(ctx, req)
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	folder := strDefault(req.Parameters, "folder", "")
	recursive := boolDefault(req.Parameters, "recursive", false)

	listRoot := root
	if folder != "" {
		resolved, err := obs.ResolveVaultPath(root, folder)
		if err != nil {
			return errResp(req.ID, err.Error()), nil
		}
		listRoot = resolved
	}

	var files []string

	if recursive {
		err = filepath.WalkDir(listRoot, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() && d.Name() == ".obsidian" {
				return filepath.SkipDir
			}
			if !d.IsDir() && strings.HasSuffix(d.Name(), ".md") {
				rel, _ := filepath.Rel(root, path)
				files = append(files, rel)
			}
			return nil
		})
	} else {
		entries, readErr := os.ReadDir(listRoot)
		if readErr != nil {
			return errResp(req.ID, fmt.Sprintf("failed to list directory: %s", readErr)), nil
		}
		for _, d := range entries {
			if d.Name() == ".obsidian" {
				continue
			}
			if !d.IsDir() && strings.HasSuffix(d.Name(), ".md") {
				rel, _ := filepath.Rel(root, filepath.Join(listRoot, d.Name()))
				files = append(files, rel)
			}
		}
	}

	if err != nil {
		return errResp(req.ID, fmt.Sprintf("failed to list files: %s", err)), nil
	}

	resp := ktp.NewToolResponse(req.ID, true, map[string]any{"files": files}, "", ms(start))
	return &resp, nil
}

func (t *Tool) search(ctx context.Context, req ktp.ToolRequest, start time.Time) (*ktp.ToolResponse, error) {
	_, root, err := t.resolveVault(ctx, req)
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	query, err := strParam(req.Parameters, "query")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	limit := intDefault(req.Parameters, "limit", 20)

	results, err := obs.SearchNotes(root, query, limit)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("search failed: %s", err)), nil
	}

	// Convert to relative paths for the response.
	converted := make([]map[string]any, 0, len(results))
	for _, r := range results {
		rel, _ := filepath.Rel(root, r.Path)
		converted = append(converted, map[string]any{
			"path":    rel,
			"snippet": r.Snippet,
			"line":    r.Line,
		})
	}

	resp := ktp.NewToolResponse(req.ID, true, map[string]any{"results": converted}, "", ms(start))
	return &resp, nil
}

func (t *Tool) doDelete(ctx context.Context, req ktp.ToolRequest, start time.Time) (*ktp.ToolResponse, error) {
	_, root, err := t.resolveVault(ctx, req)
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	notePath, err := strParam(req.Parameters, "path")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	absPath, err := obs.ResolveVaultPath(root, notePath)
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	if err := os.Remove(absPath); err != nil {
		return errResp(req.ID, fmt.Sprintf("failed to delete note: %s", err)), nil
	}

	resp := ktp.NewToolResponse(req.ID, true, map[string]any{"deleted": true}, "", ms(start))
	return &resp, nil
}

func (t *Tool) move(ctx context.Context, req ktp.ToolRequest, start time.Time) (*ktp.ToolResponse, error) {
	_, root, err := t.resolveVault(ctx, req)
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	fromPath, err := strParam(req.Parameters, "from_path")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	toPath, err := strParam(req.Parameters, "to_path")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	absFrom, err := obs.ResolveVaultPath(root, fromPath)
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	absTo, err := obs.ResolveVaultPath(root, toPath)
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	if err := os.MkdirAll(filepath.Dir(absTo), 0755); err != nil {
		return errResp(req.ID, fmt.Sprintf("failed to create target directory: %s", err)), nil
	}

	if err := os.Rename(absFrom, absTo); err != nil {
		return errResp(req.ID, fmt.Sprintf("failed to move note: %s", err)), nil
	}

	resp := ktp.NewToolResponse(req.ID, true, map[string]any{"moved": true}, "", ms(start))
	return &resp, nil
}

func (t *Tool) tags(ctx context.Context, req ktp.ToolRequest, start time.Time) (*ktp.ToolResponse, error) {
	_, root, err := t.resolveVault(ctx, req)
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	tagFilter := strDefault(req.Parameters, "tag", "")

	allTags, err := obs.ListTags(root)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("failed to list tags: %s", err)), nil
	}

	if tagFilter != "" {
		// Filter to the specific tag.
		count, found := allTags[tagFilter]
		filtered := map[string]int{}
		if found {
			filtered[tagFilter] = count
		}
		resp := ktp.NewToolResponse(req.ID, true, map[string]any{"tags": filtered}, "", ms(start))
		return &resp, nil
	}

	resp := ktp.NewToolResponse(req.ID, true, map[string]any{"tags": allTags}, "", ms(start))
	return &resp, nil
}

func (t *Tool) links(ctx context.Context, req ktp.ToolRequest, start time.Time) (*ktp.ToolResponse, error) {
	_, root, err := t.resolveVault(ctx, req)
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	notePath, err := strParam(req.Parameters, "path")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	absPath, err := obs.ResolveVaultPath(root, notePath)
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("failed to read note: %s", err)), nil
	}

	outgoing := obs.ExtractOutgoingLinks(string(data))

	backlinks, err := obs.FindBacklinks(root, notePath)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("failed to find backlinks: %s", err)), nil
	}

	// Convert backlinks to relative paths.
	relBacklinks := make([]string, 0, len(backlinks))
	for _, bl := range backlinks {
		rel, _ := filepath.Rel(root, bl)
		relBacklinks = append(relBacklinks, rel)
	}

	resp := ktp.NewToolResponse(req.ID, true, map[string]any{
		"outgoing":  outgoing,
		"backlinks": relBacklinks,
	}, "", ms(start))
	return &resp, nil
}

// --- parameter helpers ---

func strParam(params map[string]any, key string) (string, error) {
	raw, ok := params[key]
	if !ok {
		return "", fmt.Errorf("missing required parameter: %s", key)
	}
	s, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("parameter %s must be a string", key)
	}
	return s, nil
}

func strDefault(params map[string]any, key, def string) string {
	raw, ok := params[key]
	if !ok {
		return def
	}
	s, ok := raw.(string)
	if !ok {
		return def
	}
	return s
}

func intDefault(params map[string]any, key string, def int) int {
	raw, ok := params[key]
	if !ok {
		return def
	}
	switch v := raw.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	default:
		return def
	}
}

func boolDefault(params map[string]any, key string, def bool) bool {
	raw, ok := params[key]
	if !ok {
		return def
	}
	b, ok := raw.(bool)
	if !ok {
		return def
	}
	return b
}

func errResp(reqID, msg string) *ktp.ToolResponse {
	resp := ktp.NewToolResponse(reqID, false, nil, msg, 0)
	return &resp
}

func ms(start time.Time) int64 {
	return time.Since(start).Milliseconds()
}
