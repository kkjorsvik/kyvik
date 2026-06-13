package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/kkjorsvik/kyvik/internal/auth"
	"github.com/kkjorsvik/kyvik/internal/users"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

type userRowView struct {
	types.User
	RoleSummary string
}

type groupUserRoleView struct {
	UserID      string
	Username    string
	DisplayName string
	Role        string
}

type groupRowView struct {
	types.AgentGroup
	AgentIDs  []string
	UserRoles []groupUserRoleView
}

// UsersPage renders the admin user management page.
func (h *Handlers) UsersPage(w http.ResponseWriter, r *http.Request) {
	if h.userSvc == nil {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()

	users, err := h.userSvc.ListUsers(ctx)
	if err != nil {
		h.serverError(w, r, "listing users", err)
		return
	}

	rows := make([]userRowView, 0, len(users))
	for _, u := range users {
		var summary string
		if u.IsAdmin {
			summary = "admin"
		} else {
			roles, rerr := h.userSvc.UserGroupRoles(ctx, u.ID)
			if rerr != nil {
				h.serverError(w, r, "listing user roles", rerr)
				return
			}
			summary = summarizeRoles(roles)
		}
		rows = append(rows, userRowView{User: u, RoleSummary: summary})
	}

	data := map[string]any{
		"Nav":            "users",
		"Title":          "Users",
		"Users":          rows,
		"Roles":          []string{auth.RoleViewer, auth.RoleOperator, auth.RoleManager, auth.RoleAdmin},
		"ErrorMessage":   strings.TrimSpace(r.URL.Query().Get("error")),
		"SuccessMessage": strings.TrimSpace(r.URL.Query().Get("success")),
	}
	if current, ok := currentDashboardUser(ctx); ok {
		data["CurrentUserID"] = current.ID
	}
	h.renderPageWithRequest(w, r, "users-list", data)
}

// UsersCreate creates a dashboard user.
func (h *Handlers) UsersCreate(w http.ResponseWriter, r *http.Request) {
	if h.userSvc == nil {
		http.NotFound(w, r)
		return
	}
	_, err := h.userSvc.CreateUser(r.Context(), createUserParamsFromRequest(r))
	if err != nil {
		redirectWithNotice(w, r, "/users", "error", "Failed to create user")
		return
	}
	redirectWithNotice(w, r, "/users", "success", "User created.")
}

// UsersUpdate updates display name and active flag.
func (h *Handlers) UsersUpdate(w http.ResponseWriter, r *http.Request) {
	if h.userSvc == nil {
		http.NotFound(w, r)
		return
	}
	userID := r.PathValue("id")
	active := r.FormValue("is_active") == "on"
	if err := h.userSvc.UpdateUserProfile(r.Context(), userID, r.FormValue("display_name"), active); err != nil {
		redirectWithNotice(w, r, "/users", "error", "Failed to update user")
		return
	}
	redirectWithNotice(w, r, "/users", "success", "User updated.")
}

// UsersResetPassword sets a new password and forces password change on next login.
func (h *Handlers) UsersResetPassword(w http.ResponseWriter, r *http.Request) {
	if h.userSvc == nil {
		http.NotFound(w, r)
		return
	}
	userID := r.PathValue("id")
	newPassword := r.FormValue("new_password")
	if err := h.userSvc.ResetPassword(r.Context(), userID, newPassword); err != nil {
		redirectWithNotice(w, r, "/users", "error", "Failed to reset password")
		return
	}
	redirectWithNotice(w, r, "/users", "success", "Password reset.")
}

// UsersDelete deletes a user account.
func (h *Handlers) UsersDelete(w http.ResponseWriter, r *http.Request) {
	if h.userSvc == nil {
		http.NotFound(w, r)
		return
	}
	current, ok := currentDashboardUser(r.Context())
	if !ok {
		h.handleAuthRedirect(w, r)
		return
	}
	userID := r.PathValue("id")
	if current.ID == userID {
		redirectWithNotice(w, r, "/users", "error", "Cannot delete current user.")
		return
	}
	if err := h.userSvc.DeleteUser(r.Context(), userID); err != nil {
		if errors.Is(err, types.ErrUserNotFound) {
			redirectWithNotice(w, r, "/users", "error", "User not found.")
			return
		}
		redirectWithNotice(w, r, "/users", "error", "Failed to delete user")
		return
	}
	redirectWithNotice(w, r, "/users", "success", "User deleted.")
}

// GroupsPage renders agent group and assignment management.
func (h *Handlers) GroupsPage(w http.ResponseWriter, r *http.Request) {
	if h.userSvc == nil {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	groups, err := h.userSvc.ListGroups(ctx)
	if err != nil {
		h.serverError(w, r, "listing groups", err)
		return
	}
	users, err := h.userSvc.ListUsers(ctx)
	if err != nil {
		h.serverError(w, r, "listing users", err)
		return
	}
	agents, err := h.kyvik.ListAgents(ctx)
	if err != nil {
		h.serverError(w, r, "listing agents", err)
		return
	}

	userByID := map[string]types.User{}
	userRolesByGroup := map[string][]groupUserRoleView{}
	for _, u := range users {
		userByID[u.ID] = u
		roles, rerr := h.userSvc.UserGroupRoles(ctx, u.ID)
		if rerr != nil {
			h.serverError(w, r, "listing user roles", rerr)
			return
		}
		for _, rr := range roles {
			uu := userByID[rr.UserID]
			userRolesByGroup[rr.GroupID] = append(userRolesByGroup[rr.GroupID], groupUserRoleView{
				UserID:      rr.UserID,
				Username:    uu.Username,
				DisplayName: uu.DisplayName,
				Role:        rr.Role,
			})
		}
	}
	for gid := range userRolesByGroup {
		sort.Slice(userRolesByGroup[gid], func(i, j int) bool {
			return userRolesByGroup[gid][i].Username < userRolesByGroup[gid][j].Username
		})
	}

	groupRows := make([]groupRowView, 0, len(groups))
	for _, g := range groups {
		agentIDs, aerr := h.userSvc.GroupAgentIDs(ctx, g.ID)
		if aerr != nil {
			h.serverError(w, r, "listing group agents", aerr)
			return
		}
		groupRows = append(groupRows, groupRowView{
			AgentGroup: g,
			AgentIDs:   agentIDs,
			UserRoles:  userRolesByGroup[g.ID],
		})
	}

	data := map[string]any{
		"Nav":            "users",
		"Title":          "Groups",
		"Groups":         groupRows,
		"Users":          users,
		"Agents":         agents,
		"Roles":          []string{auth.RoleViewer, auth.RoleOperator, auth.RoleManager, auth.RoleAdmin},
		"ErrorMessage":   strings.TrimSpace(r.URL.Query().Get("error")),
		"SuccessMessage": strings.TrimSpace(r.URL.Query().Get("success")),
	}
	h.renderPageWithRequest(w, r, "groups-list", data)
}

// GroupsCreate creates a new group.
func (h *Handlers) GroupsCreate(w http.ResponseWriter, r *http.Request) {
	if h.userSvc == nil {
		http.NotFound(w, r)
		return
	}
	if _, err := h.userSvc.CreateGroup(r.Context(), r.FormValue("name"), r.FormValue("description")); err != nil {
		redirectWithNotice(w, r, "/groups", "error", "Failed to create group")
		return
	}
	redirectWithNotice(w, r, "/groups", "success", "Group created.")
}

// GroupsUpdate updates group metadata.
func (h *Handlers) GroupsUpdate(w http.ResponseWriter, r *http.Request) {
	if h.userSvc == nil {
		http.NotFound(w, r)
		return
	}
	groupID := r.PathValue("id")
	if err := h.userSvc.UpdateGroup(r.Context(), groupID, r.FormValue("name"), r.FormValue("description")); err != nil {
		redirectWithNotice(w, r, "/groups", "error", "Failed to update group")
		return
	}
	redirectWithNotice(w, r, "/groups", "success", "Group updated.")
}

// GroupsDelete deletes a group.
func (h *Handlers) GroupsDelete(w http.ResponseWriter, r *http.Request) {
	if h.userSvc == nil {
		http.NotFound(w, r)
		return
	}
	groupID := r.PathValue("id")
	if err := h.userSvc.DeleteGroup(r.Context(), groupID); err != nil {
		redirectWithNotice(w, r, "/groups", "error", "Failed to delete group")
		return
	}
	redirectWithNotice(w, r, "/groups", "success", "Group deleted.")
}

// GroupAddAgent assigns an agent to a group.
func (h *Handlers) GroupAddAgent(w http.ResponseWriter, r *http.Request) {
	if h.userSvc == nil {
		http.NotFound(w, r)
		return
	}
	groupID := r.PathValue("id")
	agentID := strings.TrimSpace(r.FormValue("agent_id"))
	if agentID == "" {
		redirectWithNotice(w, r, "/groups", "error", "Agent is required.")
		return
	}
	if err := h.userSvc.AddAgentToGroup(r.Context(), groupID, agentID); err != nil {
		redirectWithNotice(w, r, "/groups", "error", "Failed to add agent to group")
		return
	}
	redirectWithNotice(w, r, "/groups", "success", "Agent added to group.")
}

// GroupRemoveAgent removes an agent from a group.
func (h *Handlers) GroupRemoveAgent(w http.ResponseWriter, r *http.Request) {
	if h.userSvc == nil {
		http.NotFound(w, r)
		return
	}
	groupID := r.PathValue("id")
	agentID := r.PathValue("agentID")
	if err := h.userSvc.RemoveAgentFromGroup(r.Context(), groupID, agentID); err != nil {
		redirectWithNotice(w, r, "/groups", "error", "Failed to remove agent from group")
		return
	}
	redirectWithNotice(w, r, "/groups", "success", "Agent removed from group.")
}

// GroupSetUserRole sets a user's role in a group.
func (h *Handlers) GroupSetUserRole(w http.ResponseWriter, r *http.Request) {
	if h.userSvc == nil {
		http.NotFound(w, r)
		return
	}
	groupID := r.PathValue("id")
	userID := strings.TrimSpace(r.FormValue("user_id"))
	role := strings.TrimSpace(r.FormValue("role"))
	if userID == "" || role == "" {
		redirectWithNotice(w, r, "/groups", "error", "User and role are required.")
		return
	}
	if err := h.userSvc.SetUserRoleInGroup(r.Context(), userID, groupID, role); err != nil {
		redirectWithNotice(w, r, "/groups", "error", "Failed to set role")
		return
	}
	redirectWithNotice(w, r, "/groups", "success", "User role updated.")
}

// GroupRemoveUserRole removes user role assignment from group.
func (h *Handlers) GroupRemoveUserRole(w http.ResponseWriter, r *http.Request) {
	if h.userSvc == nil {
		http.NotFound(w, r)
		return
	}
	groupID := r.PathValue("id")
	userID := r.PathValue("userID")
	if err := h.userSvc.RemoveUserRoleInGroup(r.Context(), userID, groupID); err != nil {
		redirectWithNotice(w, r, "/groups", "error", "Failed to remove role")
		return
	}
	redirectWithNotice(w, r, "/groups", "success", "User role removed.")
}

func createUserParamsFromRequest(r *http.Request) users.CreateUserParams {
	return users.CreateUserParams{
		Username:    r.FormValue("username"),
		Password:    r.FormValue("password"),
		DisplayName: r.FormValue("display_name"),
		IsAdmin:     r.FormValue("is_admin") == "on",
	}
}

func summarizeRoles(roles []types.UserGroupRole) string {
	if len(roles) == 0 {
		return "none"
	}
	counts := map[string]int{}
	for _, r := range roles {
		counts[r.Role]++
	}
	ordered := []string{auth.RoleAdmin, auth.RoleManager, auth.RoleOperator, auth.RoleViewer}
	for _, role := range ordered {
		if n := counts[role]; n > 0 {
			if n == 1 {
				return role
			}
			return role + " (" + fmt.Sprintf("%d groups", n) + ")"
		}
	}
	return "custom"
}

func redirectWithNotice(w http.ResponseWriter, r *http.Request, path, key, message string) {
	q := url.Values{}
	q.Set(key, message)
	http.Redirect(w, r, path+"?"+q.Encode(), http.StatusSeeOther)
}
