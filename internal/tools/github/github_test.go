package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/kkjorsvik/kyvik/internal/ktp"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestDeclaration(t *testing.T) {
	decl := New().Declaration()
	if decl.Name != "github" {
		t.Fatalf("name=%q, want github", decl.Name)
	}
	if len(decl.RequiredSecrets) != 1 || decl.RequiredSecrets[0] != "github:token" {
		t.Fatalf("required secrets=%v, want [github:token]", decl.RequiredSecrets)
	}
}

func TestExecute_GetRepo(t *testing.T) {
	t.Setenv("KYVIK_SECRET_GITHUB_TOKEN", "ghp_test_token")

	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodGet {
				t.Fatalf("method=%s, want GET", req.Method)
			}
			if req.URL.Path != "/repos/acme/widgets" {
				t.Fatalf("path=%s, want /repos/acme/widgets", req.URL.Path)
			}
			auth := req.Header.Get("Authorization")
			if auth != "Bearer ghp_test_token" {
				t.Fatalf("authorization=%q", auth)
			}
			body := `{"id":123,"name":"widgets","full_name":"acme/widgets","private":false,"default_branch":"main","html_url":"https://github.com/acme/widgets","description":"repo"}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     make(http.Header),
			}, nil
		}),
	}

	tool := New(WithBaseURL("https://api.github.test"), WithHTTPClient(client))
	req := ktp.NewToolRequest("agent-1", "github", "get_repo", map[string]any{
		"owner": "acme",
		"repo":  "widgets",
	})
	resp, err := tool.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type=%T, want map[string]any", resp.Result)
	}
	if result["full_name"] != "acme/widgets" {
		t.Fatalf("full_name=%v, want acme/widgets", result["full_name"])
	}
}

func TestExecute_MissingSecret(t *testing.T) {
	tool := New()
	req := ktp.NewToolRequest("agent-1", "github", "get_repo", map[string]any{
		"owner": "acme",
		"repo":  "widgets",
	})
	resp, err := tool.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if resp.Success {
		t.Fatal("expected failure when secret is missing")
	}
	if !strings.Contains(resp.Error, "missing required secret") {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
}

func TestExecute_CreateIssue(t *testing.T) {
	t.Setenv("KYVIK_SECRET_GITHUB_TOKEN", "ghp_test_token")

	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodPost {
				t.Fatalf("method=%s, want POST", req.Method)
			}
			if req.URL.Path != "/repos/acme/widgets/issues" {
				t.Fatalf("path=%s, want /repos/acme/widgets/issues", req.URL.Path)
			}
			body := `{"number":77,"title":"Bug","state":"open","html_url":"https://github.com/acme/widgets/issues/77"}`
			return &http.Response{
				StatusCode: http.StatusCreated,
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     make(http.Header),
			}, nil
		}),
	}

	tool := New(WithBaseURL("https://api.github.test"), WithHTTPClient(client))
	req := ktp.NewToolRequest("agent-1", "github", "create_issue", map[string]any{
		"owner": "acme",
		"repo":  "widgets",
		"title": "Bug",
		"body":  "details",
	})
	resp, err := tool.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
}

func TestExecute_GetContent_File(t *testing.T) {
	t.Setenv("KYVIK_SECRET_GITHUB_TOKEN", "ghp_test_token")

	fileContent := "package main\n\nfunc main() {}\n"
	encoded := base64.StdEncoding.EncodeToString([]byte(fileContent))

	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodGet {
				t.Fatalf("method=%s, want GET", req.Method)
			}
			if req.URL.Path != "/repos/acme/widgets/contents/main.go" {
				t.Fatalf("path=%s, want /repos/acme/widgets/contents/main.go", req.URL.Path)
			}
			if ref := req.URL.Query().Get("ref"); ref != "develop" {
				t.Fatalf("ref=%s, want develop", ref)
			}
			body := `{"name":"main.go","path":"main.go","size":30,"type":"file","encoding":"base64","content":"` + encoded + `","sha":"abc123","html_url":"https://github.com/acme/widgets/blob/develop/main.go"}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     make(http.Header),
			}, nil
		}),
	}

	tool := New(WithBaseURL("https://api.github.test"), WithHTTPClient(client))
	req := ktp.NewToolRequest("agent-1", "github", "get_content", map[string]any{
		"owner": "acme",
		"repo":  "widgets",
		"path":  "main.go",
		"ref":   "develop",
	})
	resp, err := tool.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
	result := resp.Result.(map[string]any)
	if result["content"] != fileContent {
		t.Fatalf("content=%q, want %q", result["content"], fileContent)
	}
	if result["type"] != "file" {
		t.Fatalf("type=%v, want file", result["type"])
	}
}

func TestExecute_GetContent_Directory(t *testing.T) {
	t.Setenv("KYVIK_SECRET_GITHUB_TOKEN", "ghp_test_token")

	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path != "/repos/acme/widgets/contents/src" {
				t.Fatalf("path=%s, want /repos/acme/widgets/contents/src", req.URL.Path)
			}
			body := `[{"name":"main.go","path":"src/main.go","size":100,"type":"file","sha":"abc"},{"name":"lib","path":"src/lib","size":0,"type":"dir","sha":"def"}]`
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     make(http.Header),
			}, nil
		}),
	}

	tool := New(WithBaseURL("https://api.github.test"), WithHTTPClient(client))
	req := ktp.NewToolRequest("agent-1", "github", "get_content", map[string]any{
		"owner": "acme",
		"repo":  "widgets",
		"path":  "src",
	})
	resp, err := tool.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
	result := resp.Result.(map[string]any)
	entries, ok := result["entries"].([]map[string]any)
	if !ok {
		t.Fatalf("entries type=%T, want []map[string]any", result["entries"])
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries)=%d, want 2", len(entries))
	}
	if entries[0]["name"] != "main.go" {
		t.Fatalf("entries[0].name=%v, want main.go", entries[0]["name"])
	}
}

func TestExecute_ListPulls(t *testing.T) {
	t.Setenv("KYVIK_SECRET_GITHUB_TOKEN", "ghp_test_token")

	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodGet {
				t.Fatalf("method=%s, want GET", req.Method)
			}
			if !strings.HasPrefix(req.URL.Path, "/repos/acme/widgets/pulls") {
				t.Fatalf("path=%s", req.URL.Path)
			}
			body := `[{"number":42,"title":"Add feature","state":"open","html_url":"https://github.com/acme/widgets/pull/42","user":{"login":"dev1"},"head":{"ref":"feature"},"base":{"ref":"main"},"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z","draft":false}]`
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     make(http.Header),
			}, nil
		}),
	}

	tool := New(WithBaseURL("https://api.github.test"), WithHTTPClient(client))
	req := ktp.NewToolRequest("agent-1", "github", "list_pulls", map[string]any{
		"owner": "acme",
		"repo":  "widgets",
	})
	resp, err := tool.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
	result := resp.Result.(map[string]any)
	pulls := result["pulls"].([]map[string]any)
	if len(pulls) != 1 {
		t.Fatalf("len(pulls)=%d, want 1", len(pulls))
	}
	if pulls[0]["user_login"] != "dev1" {
		t.Fatalf("user_login=%v, want dev1", pulls[0]["user_login"])
	}
	if pulls[0]["head_ref"] != "feature" {
		t.Fatalf("head_ref=%v, want feature", pulls[0]["head_ref"])
	}
}

func TestExecute_CreatePull(t *testing.T) {
	t.Setenv("KYVIK_SECRET_GITHUB_TOKEN", "ghp_test_token")

	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodPost {
				t.Fatalf("method=%s, want POST", req.Method)
			}
			if req.URL.Path != "/repos/acme/widgets/pulls" {
				t.Fatalf("path=%s, want /repos/acme/widgets/pulls", req.URL.Path)
			}
			var payload map[string]any
			b, _ := io.ReadAll(req.Body)
			json.Unmarshal(b, &payload)
			if payload["title"] != "New PR" {
				t.Fatalf("title=%v, want New PR", payload["title"])
			}
			if payload["head"] != "feature" {
				t.Fatalf("head=%v, want feature", payload["head"])
			}
			if payload["base"] != "main" {
				t.Fatalf("base=%v, want main", payload["base"])
			}
			body := `{"number":99,"title":"New PR","state":"open","html_url":"https://github.com/acme/widgets/pull/99","head":{"ref":"feature"},"base":{"ref":"main"}}`
			return &http.Response{
				StatusCode: http.StatusCreated,
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     make(http.Header),
			}, nil
		}),
	}

	tool := New(WithBaseURL("https://api.github.test"), WithHTTPClient(client))
	req := ktp.NewToolRequest("agent-1", "github", "create_pull", map[string]any{
		"owner": "acme",
		"repo":  "widgets",
		"title": "New PR",
		"head":  "feature",
		"base":  "main",
	})
	resp, err := tool.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
	result := resp.Result.(map[string]any)
	if result["number"] != 99 {
		t.Fatalf("number=%v, want 99", result["number"])
	}
	if result["head_ref"] != "feature" {
		t.Fatalf("head_ref=%v, want feature", result["head_ref"])
	}
}

func TestExecute_GetWorkflowRuns(t *testing.T) {
	t.Setenv("KYVIK_SECRET_GITHUB_TOKEN", "ghp_test_token")

	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if !strings.HasPrefix(req.URL.Path, "/repos/acme/widgets/actions/runs") {
				t.Fatalf("path=%s", req.URL.Path)
			}
			body := `{"total_count":1,"workflow_runs":[{"id":555,"name":"CI","status":"completed","conclusion":"success","html_url":"https://github.com/acme/widgets/actions/runs/555","head_branch":"main","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:05:00Z"}]}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     make(http.Header),
			}, nil
		}),
	}

	tool := New(WithBaseURL("https://api.github.test"), WithHTTPClient(client))
	req := ktp.NewToolRequest("agent-1", "github", "get_workflow_runs", map[string]any{
		"owner": "acme",
		"repo":  "widgets",
	})
	resp, err := tool.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
	result := resp.Result.(map[string]any)
	runs := result["runs"].([]map[string]any)
	if len(runs) != 1 {
		t.Fatalf("len(runs)=%d, want 1", len(runs))
	}
	if runs[0]["conclusion"] != "success" {
		t.Fatalf("conclusion=%v, want success", runs[0]["conclusion"])
	}
}
