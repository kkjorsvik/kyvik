// Package github implements a dedicated GitHub integration tool.
package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kkjorsvik/kyvik/internal/ktp"
)

const (
	defaultBaseURL = "https://api.github.com"
	defaultTimeout = 30 * time.Second
	maxBodyBytes   = 1 << 20
)

// SecretResolver resolves a secret by key. Used by the sandbox binary to resolve
// secrets via Unix socket instead of env vars.
type SecretResolver func(key string) (string, error)

// Tool is a GitHub REST API tool with vault-backed token auth.
type Tool struct {
	baseURL        string
	client         *http.Client
	secretResolver SecretResolver
}

// Option configures Tool.
type Option func(*Tool)

// WithBaseURL overrides the API base URL (used by tests).
func WithBaseURL(baseURL string) Option {
	return func(t *Tool) { t.baseURL = strings.TrimRight(baseURL, "/") }
}

// WithHTTPClient overrides the HTTP client (used by tests).
func WithHTTPClient(c *http.Client) Option {
	return func(t *Tool) { t.client = c }
}

// WithSecretResolver sets a custom secret resolver (e.g. Unix socket based).
// When set, this is tried first before falling back to env var lookup.
func WithSecretResolver(fn SecretResolver) Option {
	return func(t *Tool) { t.secretResolver = fn }
}

// New creates a GitHub tool.
func New(opts ...Option) *Tool {
	t := &Tool{
		baseURL: defaultBaseURL,
		client:  &http.Client{Timeout: defaultTimeout},
	}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// Declaration returns the tool schema.
func (t *Tool) Declaration() ktp.ToolDeclaration {
	return ktp.ToolDeclaration{
		Name:            "github",
		Version:         "1.0.0",
		Description:     "GitHub repository operations via REST API",
		MinTier:         ktp.TierWriter,
		RequiredSecrets: []string{"github:token"},
		Capabilities: []ktp.Capability{
			{Type: "github", Access: "read", Resource: "api.github.com"},
			{Type: "github", Access: "issues", Resource: "api.github.com"},
			{Type: "github", Access: "pull_requests", Resource: "api.github.com"},
		},
		Actions: []ktp.ActionSpec{
			{
				Name:        "get_repo",
				Description: "Get repository metadata",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"owner": {Type: "string"},
						"repo":  {Type: "string"},
					},
					Required: []string{"owner", "repo"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"id":             {Type: "integer"},
						"name":           {Type: "string"},
						"full_name":      {Type: "string"},
						"private":        {Type: "boolean"},
						"default_branch": {Type: "string"},
						"html_url":       {Type: "string"},
						"description":    {Type: "string"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "github", Access: "read", Resource: "api.github.com"}},
			},
			{
				Name:        "list_issues",
				Description: "List repository issues",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"owner":    {Type: "string"},
						"repo":     {Type: "string"},
						"state":    {Type: "string", Enum: []string{"open", "closed", "all"}},
						"per_page": {Type: "integer"},
					},
					Required: []string{"owner", "repo"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"issues": {Type: "array", Items: &ktp.JSONSchema{Type: "object"}},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "github", Access: "read", Resource: "api.github.com"}},
			},
			{
				Name:        "create_issue",
				Description: "Create an issue in a repository",
				Destructive: true,
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"owner":     {Type: "string"},
						"repo":      {Type: "string"},
						"title":     {Type: "string"},
						"body":      {Type: "string"},
						"labels":    {Type: "array", Items: &ktp.JSONSchema{Type: "string"}},
						"assignees": {Type: "array", Items: &ktp.JSONSchema{Type: "string"}},
					},
					Required: []string{"owner", "repo", "title"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"number":   {Type: "integer"},
						"title":    {Type: "string"},
						"state":    {Type: "string"},
						"html_url": {Type: "string"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "github", Access: "issues", Resource: "api.github.com"}},
			},
			{
				Name:        "comment_issue",
				Description: "Add a comment to an issue",
				Destructive: true,
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"owner":        {Type: "string"},
						"repo":         {Type: "string"},
						"issue_number": {Type: "integer"},
						"body":         {Type: "string"},
					},
					Required: []string{"owner", "repo", "issue_number", "body"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"id":       {Type: "integer"},
						"html_url": {Type: "string"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "github", Access: "issues", Resource: "api.github.com"}},
			},
			{
				Name:        "get_content",
				Description: "Read a file or directory listing from a repository",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"owner": {Type: "string"},
						"repo":  {Type: "string"},
						"path":  {Type: "string"},
						"ref":   {Type: "string"},
					},
					Required: []string{"owner", "repo", "path"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"name":     {Type: "string"},
						"path":     {Type: "string"},
						"size":     {Type: "integer"},
						"type":     {Type: "string"},
						"content":  {Type: "string"},
						"sha":      {Type: "string"},
						"html_url": {Type: "string"},
						"entries":  {Type: "array", Items: &ktp.JSONSchema{Type: "object"}},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "github", Access: "read", Resource: "api.github.com"}},
			},
			{
				Name:        "list_pulls",
				Description: "List pull requests in a repository",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"owner":    {Type: "string"},
						"repo":     {Type: "string"},
						"state":    {Type: "string", Enum: []string{"open", "closed", "all"}},
						"per_page": {Type: "integer"},
					},
					Required: []string{"owner", "repo"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"pulls": {Type: "array", Items: &ktp.JSONSchema{Type: "object"}},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "github", Access: "read", Resource: "api.github.com"}},
			},
			{
				Name:        "list_branches",
				Description: "List branches in a repository",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"owner":    {Type: "string"},
						"repo":     {Type: "string"},
						"per_page": {Type: "integer"},
					},
					Required: []string{"owner", "repo"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"branches": {Type: "array", Items: &ktp.JSONSchema{Type: "object"}},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "github", Access: "read", Resource: "api.github.com"}},
			},
			{
				Name:        "list_commits",
				Description: "List recent commits on a branch",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"owner":    {Type: "string"},
						"repo":     {Type: "string"},
						"sha":      {Type: "string"},
						"per_page": {Type: "integer"},
					},
					Required: []string{"owner", "repo"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"commits": {Type: "array", Items: &ktp.JSONSchema{Type: "object"}},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "github", Access: "read", Resource: "api.github.com"}},
			},
			{
				Name:        "create_pull",
				Description: "Create a pull request",
				Destructive: true,
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"owner": {Type: "string"},
						"repo":  {Type: "string"},
						"title": {Type: "string"},
						"body":  {Type: "string"},
						"head":  {Type: "string"},
						"base":  {Type: "string"},
						"draft": {Type: "boolean"},
					},
					Required: []string{"owner", "repo", "title", "head", "base"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"number":   {Type: "integer"},
						"title":    {Type: "string"},
						"state":    {Type: "string"},
						"html_url": {Type: "string"},
						"head_ref": {Type: "string"},
						"base_ref": {Type: "string"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "github", Access: "pull_requests", Resource: "api.github.com"}},
			},
			{
				Name:        "list_releases",
				Description: "List releases in a repository",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"owner":    {Type: "string"},
						"repo":     {Type: "string"},
						"per_page": {Type: "integer"},
					},
					Required: []string{"owner", "repo"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"releases": {Type: "array", Items: &ktp.JSONSchema{Type: "object"}},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "github", Access: "read", Resource: "api.github.com"}},
			},
			{
				Name:        "get_workflow_runs",
				Description: "List recent workflow runs (CI/CD status)",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"owner":    {Type: "string"},
						"repo":     {Type: "string"},
						"per_page": {Type: "integer"},
						"status":   {Type: "string", Enum: []string{"queued", "in_progress", "completed"}},
					},
					Required: []string{"owner", "repo"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"runs": {Type: "array", Items: &ktp.JSONSchema{Type: "object"}},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "github", Access: "read", Resource: "api.github.com"}},
			},
		},
	}
}

// Execute runs the requested action against GitHub API.
func (t *Tool) Execute(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	token, ok := t.lookupSecret("github:token")
	if !ok || token == "" {
		return errResp(req.ID, "missing required secret github:token"), nil
	}

	switch req.Action {
	case "get_repo":
		owner, err := strParam(req.Parameters, "owner")
		if err != nil {
			return errResp(req.ID, err.Error()), nil
		}
		repo, err := strParam(req.Parameters, "repo")
		if err != nil {
			return errResp(req.ID, err.Error()), nil
		}
		var out map[string]any
		if err := t.doJSON(ctx, token, http.MethodGet, "/repos/"+owner+"/"+repo, nil, &out); err != nil {
			return errResp(req.ID, err.Error()), nil
		}
		return okResp(req.ID, map[string]any{
			"id":             asInt(out["id"]),
			"name":           asString(out["name"]),
			"full_name":      asString(out["full_name"]),
			"private":        asBool(out["private"]),
			"default_branch": asString(out["default_branch"]),
			"html_url":       asString(out["html_url"]),
			"description":    asString(out["description"]),
		}), nil

	case "list_issues":
		owner, err := strParam(req.Parameters, "owner")
		if err != nil {
			return errResp(req.ID, err.Error()), nil
		}
		repo, err := strParam(req.Parameters, "repo")
		if err != nil {
			return errResp(req.ID, err.Error()), nil
		}
		state := strDefault(req.Parameters, "state", "open")
		if state != "open" && state != "closed" && state != "all" {
			return errResp(req.ID, "state must be one of open/closed/all"), nil
		}
		perPage := intDefault(req.Parameters, "per_page", 20)
		if perPage < 1 {
			perPage = 1
		}
		if perPage > 100 {
			perPage = 100
		}
		path := fmt.Sprintf("/repos/%s/%s/issues?state=%s&per_page=%d", owner, repo, state, perPage)
		var issues []map[string]any
		if err := t.doJSON(ctx, token, http.MethodGet, path, nil, &issues); err != nil {
			return errResp(req.ID, err.Error()), nil
		}
		return okResp(req.ID, map[string]any{"issues": issues}), nil

	case "create_issue":
		owner, err := strParam(req.Parameters, "owner")
		if err != nil {
			return errResp(req.ID, err.Error()), nil
		}
		repo, err := strParam(req.Parameters, "repo")
		if err != nil {
			return errResp(req.ID, err.Error()), nil
		}
		title, err := strParam(req.Parameters, "title")
		if err != nil {
			return errResp(req.ID, err.Error()), nil
		}
		body := strDefault(req.Parameters, "body", "")
		payload := map[string]any{
			"title": title,
			"body":  body,
		}
		if labels := strSliceParam(req.Parameters, "labels"); len(labels) > 0 {
			payload["labels"] = labels
		}
		if assignees := strSliceParam(req.Parameters, "assignees"); len(assignees) > 0 {
			payload["assignees"] = assignees
		}
		var out map[string]any
		if err := t.doJSON(ctx, token, http.MethodPost, "/repos/"+owner+"/"+repo+"/issues", payload, &out); err != nil {
			return errResp(req.ID, err.Error()), nil
		}
		return okResp(req.ID, map[string]any{
			"number":   asInt(out["number"]),
			"title":    asString(out["title"]),
			"state":    asString(out["state"]),
			"html_url": asString(out["html_url"]),
		}), nil

	case "comment_issue":
		owner, err := strParam(req.Parameters, "owner")
		if err != nil {
			return errResp(req.ID, err.Error()), nil
		}
		repo, err := strParam(req.Parameters, "repo")
		if err != nil {
			return errResp(req.ID, err.Error()), nil
		}
		issueNumber := intDefault(req.Parameters, "issue_number", 0)
		if issueNumber <= 0 {
			return errResp(req.ID, "issue_number must be > 0"), nil
		}
		body, err := strParam(req.Parameters, "body")
		if err != nil {
			return errResp(req.ID, err.Error()), nil
		}
		payload := map[string]any{"body": body}
		path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments", owner, repo, issueNumber)
		var out map[string]any
		if err := t.doJSON(ctx, token, http.MethodPost, path, payload, &out); err != nil {
			return errResp(req.ID, err.Error()), nil
		}
		return okResp(req.ID, map[string]any{
			"id":       asInt(out["id"]),
			"html_url": asString(out["html_url"]),
		}), nil

	case "get_content":
		owner, err := strParam(req.Parameters, "owner")
		if err != nil {
			return errResp(req.ID, err.Error()), nil
		}
		repo, err := strParam(req.Parameters, "repo")
		if err != nil {
			return errResp(req.ID, err.Error()), nil
		}
		contentPath, err := strParam(req.Parameters, "path")
		if err != nil {
			return errResp(req.ID, err.Error()), nil
		}
		ref := strDefault(req.Parameters, "ref", "")
		apiPath := fmt.Sprintf("/repos/%s/%s/contents/%s", owner, repo, contentPath)
		if ref != "" {
			apiPath += "?ref=" + ref
		}
		rawBody, err := t.doRawJSON(ctx, token, http.MethodGet, apiPath)
		if err != nil {
			return errResp(req.ID, err.Error()), nil
		}
		// Try array first (directory listing).
		var dirEntries []map[string]any
		if err := json.Unmarshal(rawBody, &dirEntries); err == nil {
			entries := make([]map[string]any, 0, len(dirEntries))
			for _, e := range dirEntries {
				entries = append(entries, map[string]any{
					"name": asString(e["name"]),
					"path": asString(e["path"]),
					"size": asInt(e["size"]),
					"type": asString(e["type"]),
					"sha":  asString(e["sha"]),
				})
			}
			return okResp(req.ID, map[string]any{"entries": entries}), nil
		}
		// Otherwise it's a file object.
		var fileObj map[string]any
		if err := json.Unmarshal(rawBody, &fileObj); err != nil {
			return errResp(req.ID, fmt.Sprintf("decode content response: %v", err)), nil
		}
		result := map[string]any{
			"name":     asString(fileObj["name"]),
			"path":     asString(fileObj["path"]),
			"size":     asInt(fileObj["size"]),
			"type":     asString(fileObj["type"]),
			"sha":      asString(fileObj["sha"]),
			"html_url": asString(fileObj["html_url"]),
		}
		if asString(fileObj["encoding"]) == "base64" {
			decoded, err := decodeBase64Content(asString(fileObj["content"]))
			if err != nil {
				return errResp(req.ID, fmt.Sprintf("decode base64 content: %v", err)), nil
			}
			result["content"] = decoded
		} else {
			result["content"] = asString(fileObj["content"])
		}
		return okResp(req.ID, result), nil

	case "list_pulls":
		owner, err := strParam(req.Parameters, "owner")
		if err != nil {
			return errResp(req.ID, err.Error()), nil
		}
		repo, err := strParam(req.Parameters, "repo")
		if err != nil {
			return errResp(req.ID, err.Error()), nil
		}
		state := strDefault(req.Parameters, "state", "open")
		if state != "open" && state != "closed" && state != "all" {
			return errResp(req.ID, "state must be one of open/closed/all"), nil
		}
		perPage := clampPerPage(intDefault(req.Parameters, "per_page", 20))
		path := fmt.Sprintf("/repos/%s/%s/pulls?state=%s&per_page=%d", owner, repo, state, perPage)
		var raw []map[string]any
		if err := t.doJSON(ctx, token, http.MethodGet, path, nil, &raw); err != nil {
			return errResp(req.ID, err.Error()), nil
		}
		pulls := make([]map[string]any, 0, len(raw))
		for _, pr := range raw {
			pulls = append(pulls, map[string]any{
				"number":     asInt(pr["number"]),
				"title":      asString(pr["title"]),
				"state":      asString(pr["state"]),
				"html_url":   asString(pr["html_url"]),
				"user_login": nestedString(pr, "user", "login"),
				"head_ref":   nestedString(pr, "head", "ref"),
				"base_ref":   nestedString(pr, "base", "ref"),
				"created_at": asString(pr["created_at"]),
				"updated_at": asString(pr["updated_at"]),
				"draft":      asBool(pr["draft"]),
			})
		}
		return okResp(req.ID, map[string]any{"pulls": pulls}), nil

	case "list_branches":
		owner, err := strParam(req.Parameters, "owner")
		if err != nil {
			return errResp(req.ID, err.Error()), nil
		}
		repo, err := strParam(req.Parameters, "repo")
		if err != nil {
			return errResp(req.ID, err.Error()), nil
		}
		perPage := clampPerPage(intDefault(req.Parameters, "per_page", 30))
		path := fmt.Sprintf("/repos/%s/%s/branches?per_page=%d", owner, repo, perPage)
		var raw []map[string]any
		if err := t.doJSON(ctx, token, http.MethodGet, path, nil, &raw); err != nil {
			return errResp(req.ID, err.Error()), nil
		}
		branches := make([]map[string]any, 0, len(raw))
		for _, b := range raw {
			branches = append(branches, map[string]any{
				"name":       asString(b["name"]),
				"protected":  asBool(b["protected"]),
				"commit_sha": nestedString(b, "commit", "sha"),
			})
		}
		return okResp(req.ID, map[string]any{"branches": branches}), nil

	case "list_commits":
		owner, err := strParam(req.Parameters, "owner")
		if err != nil {
			return errResp(req.ID, err.Error()), nil
		}
		repo, err := strParam(req.Parameters, "repo")
		if err != nil {
			return errResp(req.ID, err.Error()), nil
		}
		perPage := clampPerPage(intDefault(req.Parameters, "per_page", 20))
		path := fmt.Sprintf("/repos/%s/%s/commits?per_page=%d", owner, repo, perPage)
		if sha := strDefault(req.Parameters, "sha", ""); sha != "" {
			path += "&sha=" + sha
		}
		var raw []map[string]any
		if err := t.doJSON(ctx, token, http.MethodGet, path, nil, &raw); err != nil {
			return errResp(req.ID, err.Error()), nil
		}
		commits := make([]map[string]any, 0, len(raw))
		for _, c := range raw {
			commits = append(commits, map[string]any{
				"sha":         asString(c["sha"]),
				"message":     nestedString(c, "commit", "message"),
				"author_name": nestedString(c, "commit", "author", "name"),
				"author_date": nestedString(c, "commit", "author", "date"),
				"html_url":    asString(c["html_url"]),
			})
		}
		return okResp(req.ID, map[string]any{"commits": commits}), nil

	case "create_pull":
		owner, err := strParam(req.Parameters, "owner")
		if err != nil {
			return errResp(req.ID, err.Error()), nil
		}
		repo, err := strParam(req.Parameters, "repo")
		if err != nil {
			return errResp(req.ID, err.Error()), nil
		}
		title, err := strParam(req.Parameters, "title")
		if err != nil {
			return errResp(req.ID, err.Error()), nil
		}
		head, err := strParam(req.Parameters, "head")
		if err != nil {
			return errResp(req.ID, err.Error()), nil
		}
		baseBranch, err := strParam(req.Parameters, "base")
		if err != nil {
			return errResp(req.ID, err.Error()), nil
		}
		payload := map[string]any{
			"title": title,
			"head":  head,
			"base":  baseBranch,
			"body":  strDefault(req.Parameters, "body", ""),
			"draft": boolDefault(req.Parameters, "draft", false),
		}
		var out map[string]any
		if err := t.doJSON(ctx, token, http.MethodPost, "/repos/"+owner+"/"+repo+"/pulls", payload, &out); err != nil {
			return errResp(req.ID, err.Error()), nil
		}
		return okResp(req.ID, map[string]any{
			"number":   asInt(out["number"]),
			"title":    asString(out["title"]),
			"state":    asString(out["state"]),
			"html_url": asString(out["html_url"]),
			"head_ref": nestedString(out, "head", "ref"),
			"base_ref": nestedString(out, "base", "ref"),
		}), nil

	case "list_releases":
		owner, err := strParam(req.Parameters, "owner")
		if err != nil {
			return errResp(req.ID, err.Error()), nil
		}
		repo, err := strParam(req.Parameters, "repo")
		if err != nil {
			return errResp(req.ID, err.Error()), nil
		}
		perPage := clampPerPage(intDefault(req.Parameters, "per_page", 10))
		path := fmt.Sprintf("/repos/%s/%s/releases?per_page=%d", owner, repo, perPage)
		var raw []map[string]any
		if err := t.doJSON(ctx, token, http.MethodGet, path, nil, &raw); err != nil {
			return errResp(req.ID, err.Error()), nil
		}
		releases := make([]map[string]any, 0, len(raw))
		for _, r := range raw {
			releases = append(releases, map[string]any{
				"id":           asInt(r["id"]),
				"tag_name":     asString(r["tag_name"]),
				"name":         asString(r["name"]),
				"draft":        asBool(r["draft"]),
				"prerelease":   asBool(r["prerelease"]),
				"published_at": asString(r["published_at"]),
				"html_url":     asString(r["html_url"]),
			})
		}
		return okResp(req.ID, map[string]any{"releases": releases}), nil

	case "get_workflow_runs":
		owner, err := strParam(req.Parameters, "owner")
		if err != nil {
			return errResp(req.ID, err.Error()), nil
		}
		repo, err := strParam(req.Parameters, "repo")
		if err != nil {
			return errResp(req.ID, err.Error()), nil
		}
		perPage := clampPerPage(intDefault(req.Parameters, "per_page", 10))
		path := fmt.Sprintf("/repos/%s/%s/actions/runs?per_page=%d", owner, repo, perPage)
		if status := strDefault(req.Parameters, "status", ""); status != "" {
			path += "&status=" + status
		}
		var wrapper map[string]any
		if err := t.doJSON(ctx, token, http.MethodGet, path, nil, &wrapper); err != nil {
			return errResp(req.ID, err.Error()), nil
		}
		rawRuns, _ := wrapper["workflow_runs"].([]any)
		runs := make([]map[string]any, 0, len(rawRuns))
		for _, r := range rawRuns {
			run, ok := r.(map[string]any)
			if !ok {
				continue
			}
			runs = append(runs, map[string]any{
				"id":          asInt(run["id"]),
				"name":        asString(run["name"]),
				"status":      asString(run["status"]),
				"conclusion":  asString(run["conclusion"]),
				"html_url":    asString(run["html_url"]),
				"head_branch": asString(run["head_branch"]),
				"created_at":  asString(run["created_at"]),
				"updated_at":  asString(run["updated_at"]),
			})
		}
		return okResp(req.ID, map[string]any{"runs": runs}), nil
	}

	return errResp(req.ID, fmt.Sprintf("unknown action: %s", req.Action)), nil
}

func (t *Tool) doJSON(ctx context.Context, token, method, path string, payload any, out any) error {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		body = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, t.baseURL+path, body)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("github request failed: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return fmt.Errorf("read github response: %w", err)
	}
	if len(bodyBytes) > maxBodyBytes {
		bodyBytes = bodyBytes[:maxBodyBytes]
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := parseGitHubError(bodyBytes)
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		return fmt.Errorf("github API %d: %s", resp.StatusCode, msg)
	}

	if out == nil {
		return nil
	}
	if err := json.Unmarshal(bodyBytes, out); err != nil {
		return fmt.Errorf("decode github response: %w", err)
	}
	return nil
}

func parseGitHubError(data []byte) string {
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return ""
	}
	msg := asString(m["message"])
	if msg == "" {
		return ""
	}
	return msg
}

func (t *Tool) lookupSecret(key string) (string, bool) {
	// Try injected resolver first (e.g. Unix socket in sandbox).
	if t.secretResolver != nil {
		v, err := t.secretResolver(key)
		if err == nil && v != "" {
			return v, true
		}
	}
	// Fallback: env var lookup (backward compat).
	return lookupSecretEnv(key)
}

// lookupSecretEnv resolves a secret from KYVIK_SECRET_* environment variables.
func lookupSecretEnv(key string) (string, bool) {
	// Primary format used by sandbox manager.
	envKey := "KYVIK_SECRET_" + strings.ToUpper(key)
	if v, ok := os.LookupEnv(envKey); ok {
		return v, true
	}
	// Fallback format with non-alnum normalized to underscore.
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

func okResp(reqID string, result any) *ktp.ToolResponse {
	resp := ktp.NewToolResponse(reqID, true, result, "", 0)
	return &resp
}

func errResp(reqID, msg string) *ktp.ToolResponse {
	resp := ktp.NewToolResponse(reqID, false, nil, msg, 0)
	return &resp
}

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

func (t *Tool) doRawJSON(ctx context.Context, token, method, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, t.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github request failed: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read github response: %w", err)
	}
	if len(bodyBytes) > maxBodyBytes {
		bodyBytes = bodyBytes[:maxBodyBytes]
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := parseGitHubError(bodyBytes)
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		return nil, fmt.Errorf("github API %d: %s", resp.StatusCode, msg)
	}
	return bodyBytes, nil
}

func decodeBase64Content(encoded string) (string, error) {
	cleaned := strings.ReplaceAll(encoded, "\n", "")
	decoded, err := base64.StdEncoding.DecodeString(cleaned)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}

func nestedString(m map[string]any, keys ...string) string {
	current := any(m)
	for _, k := range keys {
		obj, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = obj[k]
	}
	s, _ := current.(string)
	return s
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

func clampPerPage(n int) int {
	if n < 1 {
		return 1
	}
	if n > 100 {
		return 100
	}
	return n
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func asBool(v any) bool {
	b, _ := v.(bool)
	return b
}

func asInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		return 0
	}
}
