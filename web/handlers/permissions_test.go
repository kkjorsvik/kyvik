package handlers_test

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

func TestPermissionsPage_RequiresAuth(t *testing.T) {
	ts := newTestServer(t)

	ts.store.CreateAgent(context.Background(), types.AgentConfig{
		ID:       "perm-agent",
		Name:     "Perm Agent",
		Template: "worker",
	})

	// Unauthenticated request should redirect to login.
	resp, err := ts.client.Get(ts.server.URL + "/agents/perm-agent/permissions")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("expected 303 redirect, got %d", resp.StatusCode)
	}
}

func TestPermissionsPage_Renders(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	ts.store.CreateAgent(context.Background(), types.AgentConfig{
		ID:       "perm-render",
		Name:     "Render Agent",
		Template: "worker",
	})

	resp := ts.authedGet(t, "/agents/perm-render/permissions", cookie)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	html := string(body)

	if !strings.Contains(html, "Permissions") {
		t.Error("page should contain 'Permissions' heading")
	}
	if !strings.Contains(html, "Effective Capabilities") {
		t.Error("page should contain capabilities matrix")
	}
	if !strings.Contains(html, "Override Editor") {
		t.Error("page should contain override editor for admin user")
	}
	if !strings.Contains(html, "filesystem") {
		t.Error("page should show filesystem tool in matrix")
	}
}

func TestPermissionsSaveOverrides(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	ts.store.CreateAgent(context.Background(), types.AgentConfig{
		ID:       "perm-override",
		Name:     "Override Agent",
		Template: "worker",
	})

	form := url.Values{}
	form.Set("override_filesystem_write", "deny")
	form.Set("override_http_post", "grant")

	resp := ts.authedPost(t, "/agents/perm-override/permissions/overrides", cookie, form, false)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("expected 303 redirect, got %d", resp.StatusCode)
	}

	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "/agents/perm-override/permissions") {
		t.Errorf("expected redirect to permissions page, got %s", loc)
	}
}

func TestPermissionsChangeTier_Basic(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	ts.store.CreateAgent(context.Background(), types.AgentConfig{
		ID:       "perm-tier",
		Name:     "Tier Agent",
		Template: "worker",
	})

	// Change from worker to reader (no confirmation needed).
	form := url.Values{}
	form.Set("template", "reader")

	resp := ts.authedPost(t, "/agents/perm-tier/permissions/tier", cookie, form, false)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("expected 303 redirect, got %d", resp.StatusCode)
	}
}

func TestPermissionsChangeTier_AdminRequiresAcknowledgment(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	ts.store.CreateAgent(context.Background(), types.AgentConfig{
		ID:       "perm-admin",
		Name:     "Admin Agent",
		Template: "worker",
	})

	// Try admin without acknowledgment — should fail.
	form := url.Values{}
	form.Set("template", "admin")

	resp := ts.authedPost(t, "/agents/perm-admin/permissions/tier", cookie, form, false)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for admin without acknowledgment, got %d", resp.StatusCode)
	}
}

func TestPermissionsChangeTier_AdminWithAcknowledgment(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	ts.store.CreateAgent(context.Background(), types.AgentConfig{
		ID:       "perm-admin-ok",
		Name:     "Admin OK Agent",
		Template: "worker",
	})

	form := url.Values{}
	form.Set("template", "admin")
	form.Set("acknowledged", "true")
	form.Set("confirm_name", "Admin OK Agent")

	resp := ts.authedPost(t, "/agents/perm-admin-ok/permissions/tier", cookie, form, false)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("expected 303 redirect, got %d", resp.StatusCode)
	}
}

func TestPermissionsSavePaths(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	ts.store.CreateAgent(context.Background(), types.AgentConfig{
		ID:       "perm-paths",
		Name:     "Paths Agent",
		Template: "admin",
	})

	form := url.Values{}
	form.Set("read_paths", "/home/user/data\n/var/log")
	form.Set("write_paths", "/tmp/agent-output")
	form.Set("deny_paths", "/etc/shadow")
	form.Set("http_allowed_hosts", "api.example.com")
	form.Set("shell_allowed_commands", "git\nnpm")

	resp := ts.authedPost(t, "/agents/perm-paths/permissions/paths", cookie, form, false)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("expected 303 redirect, got %d", resp.StatusCode)
	}
}
