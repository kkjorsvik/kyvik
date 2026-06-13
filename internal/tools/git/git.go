// Package git implements a local git operations tool for KTP agents.
package git

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kkjorsvik/kyvik/internal/ktp"
	"github.com/kkjorsvik/kyvik/internal/tools/executil"
)

const (
	localTimeout   = 30 * time.Second
	networkTimeout = 120 * time.Second
	maxLogCount    = 100
)

// WorkspaceFunc resolves an agent's workspace directory.
type WorkspaceFunc func(agentID string) (string, error)

// SecretResolver resolves a secret by key.
type SecretResolver func(key string) (string, error)

// Tool implements local git operations.
type Tool struct {
	workspace      WorkspaceFunc
	secretResolver SecretResolver
}

// Option configures Tool.
type Option func(*Tool)

// WithSecretResolver sets a custom secret resolver.
func WithSecretResolver(fn SecretResolver) Option {
	return func(t *Tool) { t.secretResolver = fn }
}

// New creates a Git tool.
func New(workspace WorkspaceFunc, opts ...Option) *Tool {
	t := &Tool{workspace: workspace}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// Declaration returns the tool schema.
func (t *Tool) Declaration() ktp.ToolDeclaration {
	return ktp.ToolDeclaration{
		Name:            "git",
		Version:         "1.0.0",
		Description:     "Local git repository operations",
		MinTier:         ktp.TierWriter,
		RequiredSecrets: []string{"git:token"},
		Capabilities: []ktp.Capability{
			{Type: "git", Access: "read", Resource: "*"},
			{Type: "git", Access: "write", Resource: "*"},
			{Type: "git", Access: "push", Resource: "*"},
		},
		Actions: []ktp.ActionSpec{
			// Read actions
			{
				Name:        "status",
				Description: "Show working tree status",
				Parameters: ktp.JSONSchema{
					Type:       "object",
					Properties: map[string]ktp.JSONSchema{},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"branch": {Type: "string"},
						"clean":  {Type: "boolean"},
						"files":  {Type: "array", Items: &ktp.JSONSchema{Type: "object"}},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "git", Access: "read", Resource: "*"}},
			},
			{
				Name:        "log",
				Description: "Show commit log",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"max_count": {Type: "integer", Description: "Maximum number of commits (default 10, max 100)"},
						"ref":       {Type: "string", Description: "Branch or commit ref"},
					},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"commits": {Type: "array", Items: &ktp.JSONSchema{Type: "object"}},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "git", Access: "read", Resource: "*"}},
			},
			{
				Name:        "diff",
				Description: "Show changes between commits, working tree, etc.",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"ref":    {Type: "string", Description: "Commit ref to diff against"},
						"staged": {Type: "boolean", Description: "Show staged changes only"},
						"path":   {Type: "string", Description: "Restrict diff to path"},
					},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"diff":          {Type: "string"},
						"files_changed": {Type: "integer"},
						"insertions":    {Type: "integer"},
						"deletions":     {Type: "integer"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "git", Access: "read", Resource: "*"}},
			},
			{
				Name:        "branch_list",
				Description: "List branches",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"all": {Type: "boolean", Description: "Include remote branches"},
					},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"branches": {Type: "array", Items: &ktp.JSONSchema{Type: "object"}},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "git", Access: "read", Resource: "*"}},
			},
			// Write actions
			{
				Name:        "branch_create",
				Description: "Create a new branch",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"name":        {Type: "string"},
						"start_point": {Type: "string", Description: "Starting commit or branch"},
					},
					Required: []string{"name"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"name": {Type: "string"},
						"sha":  {Type: "string"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "git", Access: "write", Resource: "*"}},
			},
			{
				Name:        "checkout",
				Description: "Switch branches or restore working tree files",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"ref": {Type: "string"},
					},
					Required: []string{"ref"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"ref":      {Type: "string"},
						"previous": {Type: "string"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "git", Access: "write", Resource: "*"}},
			},
			{
				Name:        "add",
				Description: "Add file contents to the index",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"paths": {Type: "array", Items: &ktp.JSONSchema{Type: "string"}},
					},
					Required: []string{"paths"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"added": {Type: "array", Items: &ktp.JSONSchema{Type: "string"}},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "git", Access: "write", Resource: "*"}},
			},
			{
				Name:        "commit",
				Description: "Record changes to the repository",
				Destructive: true,
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"message":      {Type: "string"},
						"author_name":  {Type: "string", Description: "Override author name"},
						"author_email": {Type: "string", Description: "Override author email"},
					},
					Required: []string{"message"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"sha":     {Type: "string"},
						"message": {Type: "string"},
						"branch":  {Type: "string"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "git", Access: "write", Resource: "*"}},
			},
			{
				Name:        "pull",
				Description: "Fetch and integrate remote changes",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"remote": {Type: "string", Description: "Remote name (default: origin)"},
						"branch": {Type: "string", Description: "Branch to pull"},
					},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"updated": {Type: "boolean"},
						"summary": {Type: "string"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "git", Access: "write", Resource: "*"}},
			},
			// Push actions
			{
				Name:        "push",
				Description: "Update remote refs along with associated objects",
				Destructive: true,
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"remote": {Type: "string", Description: "Remote name (default: origin)"},
						"branch": {Type: "string", Description: "Branch to push"},
						"force":  {Type: "boolean", Description: "Force push (default: false)"},
					},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"remote":  {Type: "string"},
						"branch":  {Type: "string"},
						"summary": {Type: "string"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "git", Access: "push", Resource: "*"}},
			},
			{
				Name:        "clone",
				Description: "Clone a repository into a new directory",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"url":       {Type: "string"},
						"directory": {Type: "string", Description: "Target directory name"},
						"branch":    {Type: "string", Description: "Branch to checkout"},
						"depth":     {Type: "integer", Description: "Shallow clone depth"},
					},
					Required: []string{"url"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"directory": {Type: "string"},
						"branch":    {Type: "string"},
						"sha":      {Type: "string"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "git", Access: "push", Resource: "*"}},
			},
		},
	}
}

// Execute runs the requested git action.
func (t *Tool) Execute(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	switch req.Action {
	case "status":
		return t.execStatus(ctx, req)
	case "log":
		return t.execLog(ctx, req)
	case "diff":
		return t.execDiff(ctx, req)
	case "branch_list":
		return t.execBranchList(ctx, req)
	case "branch_create":
		return t.execBranchCreate(ctx, req)
	case "checkout":
		return t.execCheckout(ctx, req)
	case "add":
		return t.execAdd(ctx, req)
	case "commit":
		return t.execCommit(ctx, req)
	case "pull":
		return t.execPull(ctx, req)
	case "push":
		return t.execPush(ctx, req)
	case "clone":
		return t.execClone(ctx, req)
	}
	return errResp(req.ID, fmt.Sprintf("unknown action: %s", req.Action)), nil
}

func (t *Tool) execStatus(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	ws, err := t.workspace(req.AgentID)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("workspace error: %v", err)), nil
	}

	// Get current branch.
	branchResult, err := t.runGit(ctx, ws, []string{"rev-parse", "--abbrev-ref", "HEAD"}, localTimeout)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("git error: %v", err)), nil
	}
	if branchResult.ExitCode != 0 {
		return errResp(req.ID, fmt.Sprintf("git rev-parse failed: %s", branchResult.Stderr)), nil
	}
	branch := strings.TrimSpace(branchResult.Stdout)

	// Get status with porcelain format.
	statusResult, err := t.runGit(ctx, ws, []string{"status", "--porcelain=v2", "--branch"}, localTimeout)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("git error: %v", err)), nil
	}
	if statusResult.ExitCode != 0 {
		return errResp(req.ID, fmt.Sprintf("git status failed: %s", statusResult.Stderr)), nil
	}

	files := parseStatusPorcelain(statusResult.Stdout)
	return okResp(req.ID, map[string]any{
		"branch": branch,
		"clean":  len(files) == 0,
		"files":  files,
	}), nil
}

func parseStatusPorcelain(output string) []map[string]any {
	var files []map[string]any
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Porcelain v2 changed entries: "1 XY ..."  or "? path" for untracked.
		if strings.HasPrefix(line, "? ") {
			files = append(files, map[string]any{
				"path":   line[2:],
				"status": "untracked",
			})
			continue
		}
		if strings.HasPrefix(line, "1 ") || strings.HasPrefix(line, "2 ") {
			parts := strings.Fields(line)
			if len(parts) >= 9 {
				xy := parts[1]
				path := parts[8]
				files = append(files, map[string]any{
					"path":   path,
					"status": describeXY(xy),
				})
			}
			continue
		}
		if strings.HasPrefix(line, "u ") {
			parts := strings.Fields(line)
			if len(parts) >= 11 {
				path := parts[10]
				files = append(files, map[string]any{
					"path":   path,
					"status": "unmerged",
				})
			}
		}
	}
	return files
}

func describeXY(xy string) string {
	if len(xy) < 2 {
		return "unknown"
	}
	index := xy[0]
	worktree := xy[1]
	switch {
	case index == 'A' || worktree == 'A':
		return "added"
	case index == 'D' || worktree == 'D':
		return "deleted"
	case index == 'M' || worktree == 'M':
		return "modified"
	case index == 'R' || worktree == 'R':
		return "renamed"
	case index == 'C' || worktree == 'C':
		return "copied"
	default:
		return "modified"
	}
}

func (t *Tool) execLog(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	ws, err := t.workspace(req.AgentID)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("workspace error: %v", err)), nil
	}

	maxCount := intDefault(req.Parameters, "max_count", 10)
	if maxCount < 1 {
		maxCount = 1
	}
	if maxCount > maxLogCount {
		maxCount = maxLogCount
	}

	args := []string{"log", "--format=%H%n%an%n%aI%n%s", "-n", strconv.Itoa(maxCount)}
	if ref := strDefault(req.Parameters, "ref", ""); ref != "" {
		if err := validateRef(ref); err != nil {
			return errResp(req.ID, err.Error()), nil
		}
		args = append(args, ref)
	}

	result, err := t.runGit(ctx, ws, args, localTimeout)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("git error: %v", err)), nil
	}
	if result.ExitCode != 0 {
		return errResp(req.ID, fmt.Sprintf("git log failed: %s", result.Stderr)), nil
	}

	commits := parseLogOutput(result.Stdout)
	return okResp(req.ID, map[string]any{"commits": commits}), nil
}

func parseLogOutput(output string) []map[string]any {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	var commits []map[string]any
	for i := 0; i+3 < len(lines); i += 4 {
		commits = append(commits, map[string]any{
			"sha":     lines[i],
			"author":  lines[i+1],
			"date":    lines[i+2],
			"message": lines[i+3],
		})
	}
	return commits
}

func (t *Tool) execDiff(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	ws, err := t.workspace(req.AgentID)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("workspace error: %v", err)), nil
	}

	args := []string{"diff"}
	if boolDefault(req.Parameters, "staged", false) {
		args = append(args, "--cached")
	}
	if ref := strDefault(req.Parameters, "ref", ""); ref != "" {
		if err := validateRef(ref); err != nil {
			return errResp(req.ID, err.Error()), nil
		}
		args = append(args, ref)
	}
	if path := strDefault(req.Parameters, "path", ""); path != "" {
		if _, err := executil.SafePath(ws, path); err != nil {
			return errResp(req.ID, fmt.Sprintf("invalid path: %v", err)), nil
		}
		args = append(args, "--", path)
	}

	result, err := t.runGit(ctx, ws, args, localTimeout)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("git error: %v", err)), nil
	}
	if result.ExitCode != 0 {
		return errResp(req.ID, fmt.Sprintf("git diff failed: %s", result.Stderr)), nil
	}

	// Get diffstat.
	statArgs := []string{"diff", "--stat"}
	if boolDefault(req.Parameters, "staged", false) {
		statArgs = append(statArgs, "--cached")
	}
	if ref := strDefault(req.Parameters, "ref", ""); ref != "" {
		statArgs = append(statArgs, ref)
	}
	if path := strDefault(req.Parameters, "path", ""); path != "" {
		statArgs = append(statArgs, "--", path)
	}
	statResult, _ := t.runGit(ctx, ws, statArgs, localTimeout)

	filesChanged, insertions, deletions := parseDiffStat(statResult)

	return okResp(req.ID, map[string]any{
		"diff":          result.Stdout,
		"files_changed": filesChanged,
		"insertions":    insertions,
		"deletions":     deletions,
	}), nil
}

func parseDiffStat(result *executil.ProcessResult) (int, int, int) {
	if result == nil || result.ExitCode != 0 {
		return 0, 0, 0
	}
	lines := strings.Split(strings.TrimSpace(result.Stdout), "\n")
	if len(lines) == 0 {
		return 0, 0, 0
	}
	// Last line of --stat: " N files changed, M insertions(+), K deletions(-)"
	summary := lines[len(lines)-1]
	var files, ins, dels int
	for _, part := range strings.Split(summary, ",") {
		part = strings.TrimSpace(part)
		fields := strings.Fields(part)
		if len(fields) >= 2 {
			n, err := strconv.Atoi(fields[0])
			if err != nil {
				continue
			}
			switch {
			case strings.Contains(part, "changed"):
				files = n
			case strings.Contains(part, "insertion"):
				ins = n
			case strings.Contains(part, "deletion"):
				dels = n
			}
		}
	}
	return files, ins, dels
}

func (t *Tool) execBranchList(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	ws, err := t.workspace(req.AgentID)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("workspace error: %v", err)), nil
	}

	args := []string{"branch", "--format=%(refname:short)\t%(HEAD)\t%(upstream:short)"}
	if boolDefault(req.Parameters, "all", false) {
		args = append(args, "-a")
	}

	result, err := t.runGit(ctx, ws, args, localTimeout)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("git error: %v", err)), nil
	}
	if result.ExitCode != 0 {
		return errResp(req.ID, fmt.Sprintf("git branch failed: %s", result.Stderr)), nil
	}

	var branches []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(result.Stdout), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		name := parts[0]
		current := len(parts) > 1 && strings.TrimSpace(parts[1]) == "*"
		remote := ""
		if len(parts) > 2 {
			remote = strings.TrimSpace(parts[2])
		}
		branches = append(branches, map[string]any{
			"name":    name,
			"current": current,
			"remote":  remote,
		})
	}

	return okResp(req.ID, map[string]any{"branches": branches}), nil
}

func (t *Tool) execBranchCreate(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	ws, err := t.workspace(req.AgentID)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("workspace error: %v", err)), nil
	}

	name, err := strParam(req.Parameters, "name")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}
	if err := validateRef(name); err != nil {
		return errResp(req.ID, fmt.Sprintf("invalid branch name: %v", err)), nil
	}

	args := []string{"branch", name}
	if sp := strDefault(req.Parameters, "start_point", ""); sp != "" {
		if err := validateRef(sp); err != nil {
			return errResp(req.ID, fmt.Sprintf("invalid start_point: %v", err)), nil
		}
		args = append(args, sp)
	}

	result, err := t.runGit(ctx, ws, args, localTimeout)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("git error: %v", err)), nil
	}
	if result.ExitCode != 0 {
		return errResp(req.ID, fmt.Sprintf("git branch failed: %s", result.Stderr)), nil
	}

	// Get the SHA of the new branch.
	shaResult, err := t.runGit(ctx, ws, []string{"rev-parse", name}, localTimeout)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("git error: %v", err)), nil
	}

	return okResp(req.ID, map[string]any{
		"name": name,
		"sha":  strings.TrimSpace(shaResult.Stdout),
	}), nil
}

func (t *Tool) execCheckout(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	ws, err := t.workspace(req.AgentID)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("workspace error: %v", err)), nil
	}

	ref, err := strParam(req.Parameters, "ref")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}
	if err := validateRef(ref); err != nil {
		return errResp(req.ID, fmt.Sprintf("invalid ref: %v", err)), nil
	}

	// Get current branch before checkout.
	prevResult, _ := t.runGit(ctx, ws, []string{"rev-parse", "--abbrev-ref", "HEAD"}, localTimeout)
	previous := ""
	if prevResult != nil && prevResult.ExitCode == 0 {
		previous = strings.TrimSpace(prevResult.Stdout)
	}

	result, err := t.runGit(ctx, ws, []string{"checkout", ref}, localTimeout)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("git error: %v", err)), nil
	}
	if result.ExitCode != 0 {
		return errResp(req.ID, fmt.Sprintf("git checkout failed: %s", result.Stderr)), nil
	}

	return okResp(req.ID, map[string]any{
		"ref":      ref,
		"previous": previous,
	}), nil
}

func (t *Tool) execAdd(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	ws, err := t.workspace(req.AgentID)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("workspace error: %v", err)), nil
	}

	paths := strSliceParam(req.Parameters, "paths")
	if len(paths) == 0 {
		return errResp(req.ID, "paths must be a non-empty array"), nil
	}

	// Validate all paths first.
	for _, p := range paths {
		if _, err := executil.SafePath(ws, p); err != nil {
			return errResp(req.ID, fmt.Sprintf("invalid path %q: %v", p, err)), nil
		}
	}

	args := append([]string{"add", "--"}, paths...)
	result, err := t.runGit(ctx, ws, args, localTimeout)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("git error: %v", err)), nil
	}
	if result.ExitCode != 0 {
		return errResp(req.ID, fmt.Sprintf("git add failed: %s", result.Stderr)), nil
	}

	return okResp(req.ID, map[string]any{"added": paths}), nil
}

func (t *Tool) execCommit(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	ws, err := t.workspace(req.AgentID)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("workspace error: %v", err)), nil
	}

	message, err := strParam(req.Parameters, "message")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	args := []string{"commit", "-m", message}
	if authorName := strDefault(req.Parameters, "author_name", ""); authorName != "" {
		authorEmail := strDefault(req.Parameters, "author_email", "noreply@kyvik.local")
		args = append(args, "--author", fmt.Sprintf("%s <%s>", authorName, authorEmail))
	}

	result, err := t.runGit(ctx, ws, args, localTimeout)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("git error: %v", err)), nil
	}
	if result.ExitCode != 0 {
		return errResp(req.ID, fmt.Sprintf("git commit failed: %s", result.Stderr)), nil
	}

	// Get commit SHA and branch.
	shaResult, _ := t.runGit(ctx, ws, []string{"rev-parse", "HEAD"}, localTimeout)
	sha := ""
	if shaResult != nil && shaResult.ExitCode == 0 {
		sha = strings.TrimSpace(shaResult.Stdout)
	}
	branchResult, _ := t.runGit(ctx, ws, []string{"rev-parse", "--abbrev-ref", "HEAD"}, localTimeout)
	branch := ""
	if branchResult != nil && branchResult.ExitCode == 0 {
		branch = strings.TrimSpace(branchResult.Stdout)
	}

	return okResp(req.ID, map[string]any{
		"sha":     sha,
		"message": message,
		"branch":  branch,
	}), nil
}

func (t *Tool) execPull(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	ws, err := t.workspace(req.AgentID)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("workspace error: %v", err)), nil
	}

	remote := strDefault(req.Parameters, "remote", "origin")
	if err := validateRef(remote); err != nil {
		return errResp(req.ID, fmt.Sprintf("invalid remote: %v", err)), nil
	}

	// Get HEAD before pull.
	beforeResult, _ := t.runGit(ctx, ws, []string{"rev-parse", "HEAD"}, localTimeout)
	beforeSHA := ""
	if beforeResult != nil && beforeResult.ExitCode == 0 {
		beforeSHA = strings.TrimSpace(beforeResult.Stdout)
	}

	args := []string{"pull", remote}
	if branch := strDefault(req.Parameters, "branch", ""); branch != "" {
		if err := validateRef(branch); err != nil {
			return errResp(req.ID, fmt.Sprintf("invalid branch: %v", err)), nil
		}
		args = append(args, branch)
	}

	result, err := t.runGitWithAuth(ctx, ws, args, networkTimeout)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("git error: %v", err)), nil
	}
	if result.ExitCode != 0 {
		return errResp(req.ID, fmt.Sprintf("git pull failed: %s", result.Stderr)), nil
	}

	// Get HEAD after pull.
	afterResult, _ := t.runGit(ctx, ws, []string{"rev-parse", "HEAD"}, localTimeout)
	afterSHA := ""
	if afterResult != nil && afterResult.ExitCode == 0 {
		afterSHA = strings.TrimSpace(afterResult.Stdout)
	}

	return okResp(req.ID, map[string]any{
		"updated": beforeSHA != afterSHA,
		"summary": strings.TrimSpace(result.Stdout + "\n" + result.Stderr),
	}), nil
}

func (t *Tool) execPush(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	ws, err := t.workspace(req.AgentID)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("workspace error: %v", err)), nil
	}

	remote := strDefault(req.Parameters, "remote", "origin")
	if err := validateRef(remote); err != nil {
		return errResp(req.ID, fmt.Sprintf("invalid remote: %v", err)), nil
	}

	args := []string{"push", remote}
	if branch := strDefault(req.Parameters, "branch", ""); branch != "" {
		if err := validateRef(branch); err != nil {
			return errResp(req.ID, fmt.Sprintf("invalid branch: %v", err)), nil
		}
		args = append(args, branch)
	}
	if boolDefault(req.Parameters, "force", false) {
		args = append(args, "--force")
	}

	result, err := t.runGitWithAuth(ctx, ws, args, networkTimeout)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("git error: %v", err)), nil
	}
	if result.ExitCode != 0 {
		return errResp(req.ID, fmt.Sprintf("git push failed: %s", result.Stderr)), nil
	}

	// Determine branch that was pushed.
	branchResult, _ := t.runGit(ctx, ws, []string{"rev-parse", "--abbrev-ref", "HEAD"}, localTimeout)
	branch := ""
	if branchResult != nil && branchResult.ExitCode == 0 {
		branch = strings.TrimSpace(branchResult.Stdout)
	}

	return okResp(req.ID, map[string]any{
		"remote":  remote,
		"branch":  branch,
		"summary": strings.TrimSpace(result.Stdout + "\n" + result.Stderr),
	}), nil
}

func (t *Tool) execClone(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	ws, err := t.workspace(req.AgentID)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("workspace error: %v", err)), nil
	}

	cloneURL, err := strParam(req.Parameters, "url")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	// Inject auth token into HTTPS URL if available.
	cloneURL = t.injectAuth(cloneURL)

	directory := strDefault(req.Parameters, "directory", "")
	if directory == "" {
		// Derive from URL.
		directory = deriveRepoName(cloneURL)
	}
	if directory == "" {
		return errResp(req.ID, "could not derive directory name from URL"), nil
	}

	// Validate target directory is within workspace.
	if _, err := executil.SafePath(ws, directory); err != nil {
		return errResp(req.ID, fmt.Sprintf("invalid directory: %v", err)), nil
	}

	args := []string{"clone"}
	if branch := strDefault(req.Parameters, "branch", ""); branch != "" {
		if err := validateRef(branch); err != nil {
			return errResp(req.ID, fmt.Sprintf("invalid branch: %v", err)), nil
		}
		args = append(args, "--branch", branch)
	}
	if depth := intDefault(req.Parameters, "depth", 0); depth > 0 {
		args = append(args, "--depth", strconv.Itoa(depth))
	}
	args = append(args, cloneURL, directory)

	result, err := t.runGit(ctx, ws, args, networkTimeout)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("git error: %v", err)), nil
	}
	if result.ExitCode != 0 {
		return errResp(req.ID, fmt.Sprintf("git clone failed: %s", result.Stderr)), nil
	}

	// Get branch and SHA from cloned repo.
	cloneDir := filepath.Join(ws, directory)
	branchResult, _ := t.runGit(ctx, cloneDir, []string{"rev-parse", "--abbrev-ref", "HEAD"}, localTimeout)
	cloneBranch := ""
	if branchResult != nil && branchResult.ExitCode == 0 {
		cloneBranch = strings.TrimSpace(branchResult.Stdout)
	}
	shaResult, _ := t.runGit(ctx, cloneDir, []string{"rev-parse", "HEAD"}, localTimeout)
	sha := ""
	if shaResult != nil && shaResult.ExitCode == 0 {
		sha = strings.TrimSpace(shaResult.Stdout)
	}

	return okResp(req.ID, map[string]any{
		"directory": directory,
		"branch":    cloneBranch,
		"sha":       sha,
	}), nil
}

// runGit executes a git command in the given workspace directory with environment hardening.
func (t *Tool) runGit(ctx context.Context, workDir string, args []string, timeout time.Duration) (*executil.ProcessResult, error) {
	if err := validateArgs(args); err != nil {
		return nil, err
	}

	env := baseGitEnv()
	return executil.RunProcess(ctx, executil.ProcessConfig{
		Command:    "git",
		Args:       args,
		WorkingDir: workDir,
		Env:        env,
		Timeout:    timeout,
	})
}

// runGitWithAuth executes a git command with credential environment variables for network operations.
func (t *Tool) runGitWithAuth(ctx context.Context, workDir string, args []string, timeout time.Duration) (*executil.ProcessResult, error) {
	if err := validateArgs(args); err != nil {
		return nil, err
	}

	env := baseGitEnv()

	// Set up GIT_ASKPASS for authentication if a token is available.
	token, _ := t.lookupSecret("git:token")
	if token != "" {
		// Create a temporary askpass script that echoes the token.
		askpass, cleanup, err := createAskPassScript(workDir, token)
		if err != nil {
			return nil, fmt.Errorf("create askpass script: %w", err)
		}
		defer cleanup()
		env = append(env, "GIT_ASKPASS="+askpass)
	}

	return executil.RunProcess(ctx, executil.ProcessConfig{
		Command:    "git",
		Args:       args,
		WorkingDir: workDir,
		Env:        env,
		Timeout:    timeout,
	})
}

// baseGitEnv returns the hardened environment for git commands.
func baseGitEnv() []string {
	env := os.Environ()
	env = append(env,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
	)
	return env
}

// createAskPassScript creates a temporary script that outputs the token for git auth.
// Returns the script path and a cleanup function.
func createAskPassScript(workDir, token string) (string, func(), error) {
	f, err := os.CreateTemp(workDir, ".git-askpass-*.sh")
	if err != nil {
		return "", nil, err
	}
	script := fmt.Sprintf("#!/bin/sh\necho '%s'\n", strings.ReplaceAll(token, "'", "'\\''"))
	if _, err := f.WriteString(script); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", nil, err
	}
	f.Close()
	if err := os.Chmod(f.Name(), 0700); err != nil {
		os.Remove(f.Name())
		return "", nil, err
	}
	return f.Name(), func() { os.Remove(f.Name()) }, nil
}

// injectAuth embeds the auth token into an HTTPS clone URL if available.
func (t *Tool) injectAuth(rawURL string) string {
	token, _ := t.lookupSecret("git:token")
	if token == "" {
		return rawURL
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return rawURL
	}
	parsed.User = url.UserPassword("x-access-token", token)
	return parsed.String()
}

func deriveRepoName(rawURL string) string {
	// Handle both HTTPS and git@ URLs.
	name := rawURL
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	if i := strings.LastIndex(name, ":"); i >= 0 {
		name = name[i+1:]
	}
	name = strings.TrimSuffix(name, ".git")
	return name
}

// validateArgs checks for blocked git flags and operations.
func validateArgs(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("no git arguments provided")
	}
	for _, arg := range args {
		lower := strings.ToLower(arg)
		switch {
		case lower == "--exec" || strings.HasPrefix(lower, "--exec="):
			return fmt.Errorf("--exec flag is not allowed")
		case lower == "--upload-pack" || strings.HasPrefix(lower, "--upload-pack="):
			return fmt.Errorf("--upload-pack flag is not allowed")
		}
	}
	// Block dangerous subcommands.
	sub := strings.ToLower(args[0])
	switch sub {
	case "clean":
		return fmt.Errorf("git clean is not allowed")
	case "config":
		for _, a := range args[1:] {
			if strings.ToLower(a) == "--global" || strings.ToLower(a) == "--system" {
				return fmt.Errorf("git config --global/--system is not allowed")
			}
		}
	case "reset":
		for _, a := range args[1:] {
			if strings.ToLower(a) == "--hard" {
				return fmt.Errorf("git reset --hard is not allowed")
			}
		}
	}
	return nil
}

// validateRef checks that a ref name doesn't contain shell-dangerous characters.
func validateRef(ref string) error {
	if ref == "" {
		return fmt.Errorf("ref must not be empty")
	}
	// Block characters that could be used for injection.
	for _, ch := range ref {
		switch ch {
		case ' ', '\t', '\n', '\r', '\'', '"', '`', '$', '\\', ';', '&', '|', '>', '<', '(', ')', '{', '}':
			return fmt.Errorf("ref contains invalid character %q", string(ch))
		}
	}
	// Block refs starting with - to prevent flag injection.
	if strings.HasPrefix(ref, "-") {
		return fmt.Errorf("ref must not start with -")
	}
	return nil
}

func (t *Tool) lookupSecret(key string) (string, bool) {
	// Try injected resolver first.
	if t.secretResolver != nil {
		v, err := t.secretResolver(key)
		if err == nil && v != "" {
			return v, true
		}
	}
	// Fallback: try git:token then github:token.
	if key == "git:token" {
		if v, ok := lookupSecretEnv("git:token"); ok {
			return v, true
		}
		return lookupSecretEnv("github:token")
	}
	return lookupSecretEnv(key)
}

func lookupSecretEnv(key string) (string, bool) {
	envKey := "KYVIK_SECRET_" + strings.ToUpper(key)
	if v, ok := os.LookupEnv(envKey); ok {
		return v, true
	}
	var b strings.Builder
	b.WriteString("KYVIK_SECRET_")
	for _, r := range key {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if v, ok := os.LookupEnv(strings.ToUpper(b.String())); ok {
		return v, true
	}
	return "", false
}

// Response helpers (same pattern as github tool).

func okResp(reqID string, result any) *ktp.ToolResponse {
	resp := ktp.NewToolResponse(reqID, true, result, "", 0)
	return &resp
}

func errResp(reqID, msg string) *ktp.ToolResponse {
	resp := ktp.NewToolResponse(reqID, false, nil, msg, 0)
	return &resp
}

// Parameter helpers (same pattern as github tool).

func strParam(params map[string]any, key string) (string, error) {
	v, ok := params[key]
	if !ok {
		return "", fmt.Errorf("missing required parameter: %s", key)
	}
	s, ok := v.(string)
	if !ok || strings.TrimSpace(s) == "" {
		return "", fmt.Errorf("parameter %s must be a non-empty string", key)
	}
	return s, nil
}

func strDefault(params map[string]any, key, def string) string {
	v, ok := params[key]
	if !ok {
		return def
	}
	s, ok := v.(string)
	if !ok {
		return def
	}
	return s
}

func intDefault(params map[string]any, key string, def int) int {
	v, ok := params[key]
	if !ok {
		return def
	}
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case string:
		parsed, err := strconv.Atoi(n)
		if err == nil {
			return parsed
		}
	}
	return def
}

func boolDefault(params map[string]any, key string, def bool) bool {
	v, ok := params[key]
	if !ok {
		return def
	}
	b, ok := v.(bool)
	if !ok {
		return def
	}
	return b
}

func strSliceParam(params map[string]any, key string) []string {
	v, ok := params[key]
	if !ok {
		return nil
	}
	if ss, ok := v.([]string); ok {
		return ss
	}
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, e := range list {
		if s, ok := e.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}
