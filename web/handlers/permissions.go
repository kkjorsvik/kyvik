package handlers

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/kkjorsvik/kyvik/internal/audit"
	"github.com/kkjorsvik/kyvik/internal/auth"
	"github.com/kkjorsvik/kyvik/internal/permissions"
	"github.com/kkjorsvik/kyvik/internal/security"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// Known tools × actions for the capabilities matrix.
var (
	matrixTools = []string{"filesystem", "http", "database", "shell", "code_exec"}
	matrixActions = map[string][]string{
		"filesystem": {"read", "write", "*"},
		"http":       {"get", "post", "delete", "*"},
		"database":   {"select", "insert", "update", "delete", "*"},
		"shell":      {"execute", "*"},
		"code_exec":  {"execute", "*"},
	}
)

// PermCellSource describes why a capability is granted/denied.
type PermCellSource string

const (
	PermCellTemplate      PermCellSource = "template"
	PermCellOverrideGrant PermCellSource = "override_grant"
	PermCellOverrideDeny  PermCellSource = "override_deny"
	PermCellNone          PermCellSource = "none"
)

// MatrixCell represents one cell in the capabilities matrix.
type MatrixCell struct {
	Tool   string
	Action string
	Source PermCellSource
}

// MatrixRow groups cells by tool for template rendering.
type MatrixRow struct {
	Tool    string
	Actions []string
	Cells   []MatrixCell
}

// buildMatrix constructs the capabilities matrix for display.
func buildMatrix(template *permissions.Template, overrides []permissions.Override) []MatrixRow {
	// Build lookup sets.
	templateCaps := make(map[string]bool)
	if template != nil {
		for _, c := range template.Capabilities {
			templateCaps[c.Tool+"."+c.Action] = true
		}
	}

	grantOverrides := make(map[string]bool)
	denyOverrides := make(map[string]bool)
	for _, o := range overrides {
		key := o.Capability.Tool + "." + o.Capability.Action
		if o.Grant {
			grantOverrides[key] = true
		} else {
			denyOverrides[key] = true
		}
	}

	var rows []MatrixRow
	for _, tool := range matrixTools {
		actions := matrixActions[tool]
		row := MatrixRow{
			Tool:    tool,
			Actions: actions,
			Cells:   make([]MatrixCell, len(actions)),
		}
		for i, action := range actions {
			key := tool + "." + action
			cell := MatrixCell{Tool: tool, Action: action}

			if denyOverrides[key] {
				cell.Source = PermCellOverrideDeny
			} else if grantOverrides[key] {
				cell.Source = PermCellOverrideGrant
			} else if templateCaps[key] || templateCaps[tool+".*"] || templateCaps["*.*"] {
				cell.Source = PermCellTemplate
			} else {
				cell.Source = PermCellNone
			}

			row.Cells[i] = cell
		}
		rows = append(rows, row)
	}
	return rows
}

// PermissionsPage renders the permission management page for an agent.
func (h *Handlers) PermissionsPage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	agent, err := h.kyvik.GetAgent(ctx, id)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	// Load the agent's template.
	templates, _ := h.kyvik.ListTemplates(ctx)
	var agentTemplate *permissions.Template
	for _, t := range templates {
		if t.Name == agent.Template {
			tmpl := t
			agentTemplate = &tmpl
			break
		}
	}

	// Load overrides and effective capabilities.
	overrides, _ := h.kyvik.ListOverrides(ctx, id)
	capabilities, _ := h.kyvik.GetAgentCapabilities(ctx, id)

	// Build capabilities matrix.
	matrix := buildMatrix(agentTemplate, overrides)

	// Check if the current user can edit permissions.
	canEdit := false
	if u, ok := currentDashboardUser(ctx); ok {
		role := u.Role
		if u.IsAdmin {
			role = auth.RoleAdmin
		}
		canEdit = auth.Can(role, auth.PermPermissionsEdit)
	}

	// Load recent permission audit entries.
	var auditEntries []types.AuditEntry
	if al := h.kyvik.Audit(); al != nil {
		auditEntries, _ = al.Query(ctx, audit.Filter{
			AgentID: id,
			Limit:   20,
		})
	}

	data := map[string]any{
		"Title":        fmt.Sprintf("Permissions — %s", agent.Name),
		"Agent":        agent,
		"Template":     agentTemplate,
		"Templates":    templates,
		"Overrides":    overrides,
		"Capabilities": capabilities,
		"Matrix":       matrix,
		"CanEdit":      canEdit,
		"AuditEntries": auditEntries,
		"MatrixTools":  matrixTools,
	}

	h.renderPageWithRequest(w, r, "permissions-page", data)
}

// PermissionsSaveOverrides handles saving permission overrides for an agent.
func (h *Handlers) PermissionsSaveOverrides(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form data", http.StatusBadRequest)
		return
	}

	// Verify agent exists.
	agent, err := h.kyvik.GetAgent(ctx, id)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	// Clear all existing overrides.
	if err := h.kyvik.RemoveAllOverrides(ctx, id); err != nil {
		h.serverError(w, r, "clearing permission overrides", err)
		return
	}

	// Add new overrides from form.
	for key, values := range r.Form {
		if !strings.HasPrefix(key, "override_") || len(values) == 0 {
			continue
		}
		value := values[0]
		if value != "grant" && value != "deny" {
			continue
		}

		// Parse key: override_{tool}_{action}
		parts := strings.SplitN(strings.TrimPrefix(key, "override_"), "_", 2)
		if len(parts) != 2 {
			continue
		}

		override := permissions.Override{
			AgentID: id,
			Capability: types.Capability{
				Tool:     parts[0],
				Action:   parts[1],
				Resource: "*",
			},
			Grant: value == "grant",
		}
		if err := h.kyvik.AddOverride(ctx, override); err != nil {
			h.serverError(w, r, "adding permission override", err)
			return
		}
	}

	// Audit log.
	if al := h.kyvik.Audit(); al != nil {
		_ = al.Log(ctx, types.AuditEntry{
			AgentID:   id,
			EventType: types.EventPermission,
			Action:    "overrides_updated",
			Details:   fmt.Sprintf("permission overrides updated for agent %s", agent.Name),
		})
	}

	http.Redirect(w, r, "/agents/"+id+"/permissions", http.StatusSeeOther)
}

// PermissionsChangeTier handles changing an agent's permission tier.
func (h *Handlers) PermissionsChangeTier(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form data", http.StatusBadRequest)
		return
	}

	agent, err := h.kyvik.GetAgent(ctx, id)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	newTier := r.FormValue("template")
	if newTier == "" {
		http.Error(w, "template is required", http.StatusBadRequest)
		return
	}

	// Validate tier is one of the 4 user-assignable tiers.
	validTiers := map[string]bool{"reader": true, "worker": true, "operator": true, "admin": true}
	if !validTiers[newTier] {
		http.Error(w, "invalid tier: must be reader, worker, operator, or admin", http.StatusBadRequest)
		return
	}

	// Validate tier confirmation for elevated tiers.
	confirmation := &security.TierConfirmation{
		Acknowledged:     r.FormValue("acknowledged") == "true",
		ConfirmationName: r.FormValue("confirm_name"),
	}
	if err := security.ValidateElevatedTier(agent.Name, newTier, agent.Template, confirmation); err != nil {
		http.Error(w, "tier confirmation failed", http.StatusBadRequest)
		return
	}

	// Update the agent's template.
	oldTier := agent.Template
	agent.Template = newTier
	if err := h.kyvik.UpdateAgent(ctx, *agent); err != nil {
		h.serverError(w, r, "updating agent tier", err)
		return
	}

	// Audit log.
	if al := h.kyvik.Audit(); al != nil {
		_ = al.Log(ctx, types.AuditEntry{
			AgentID:   id,
			EventType: types.EventPermission,
			Action:    "tier_changed",
			Details:   fmt.Sprintf("tier changed from %s to %s for agent %s", oldTier, newTier, agent.Name),
		})
	}

	http.Redirect(w, r, "/agents/"+id+"/permissions", http.StatusSeeOther)
}

// PermissionsSavePaths handles saving path-based permissions for an agent.
func (h *Handlers) PermissionsSavePaths(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form data", http.StatusBadRequest)
		return
	}

	agent, err := h.kyvik.GetAgent(ctx, id)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	// Parse newline-separated paths.
	readPaths := splitLines(r.FormValue("read_paths"))
	writePaths := splitLines(r.FormValue("write_paths"))
	denyPaths := splitLines(r.FormValue("deny_paths"))
	httpHosts := splitLines(r.FormValue("http_allowed_hosts"))
	shellCommands := splitLines(r.FormValue("shell_allowed_commands"))

	// Update host paths.
	if len(readPaths) > 0 || len(writePaths) > 0 || len(denyPaths) > 0 {
		agent.HostPaths = &types.HostPathConfig{
			Read:  readPaths,
			Write: writePaths,
			Deny:  denyPaths,
		}
	} else {
		agent.HostPaths = nil
	}

	agent.HTTPAllowedHosts = httpHosts
	agent.ShellAllowedCommands = shellCommands

	if err := h.kyvik.UpdateAgent(ctx, *agent); err != nil {
		h.serverError(w, r, "updating agent paths", err)
		return
	}

	// Audit log.
	if al := h.kyvik.Audit(); al != nil {
		_ = al.Log(ctx, types.AuditEntry{
			AgentID:   id,
			EventType: types.EventPermission,
			Action:    "paths_updated",
			Details:   fmt.Sprintf("path-based permissions updated for agent %s", agent.Name),
		})
	}

	http.Redirect(w, r, "/agents/"+id+"/permissions", http.StatusSeeOther)
}

// splitLines splits a newline-separated string into trimmed, non-empty lines.
func splitLines(s string) []string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
