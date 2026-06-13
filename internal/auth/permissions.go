package auth

import "sort"

// Permission strings.
const (
	PermAgentView               = "agent.view"
	PermAgentCreate             = "agent.create"
	PermAgentCreateUnrestricted = "agent.create.unrestricted"
	PermAgentEdit               = "agent.edit"
	PermAgentDelete             = "agent.delete"
	PermAgentStart              = "agent.start"
	PermAgentStop               = "agent.stop"
	PermAgentKill               = "agent.kill"
	PermAgentQuarantine         = "agent.quarantine"
	PermMemoryView              = "memory.view"
	PermMemoryEdit              = "memory.edit"
	PermMemoryBulkDelete        = "memory.bulk_delete"
	PermHistoryView             = "history.view"
	PermAuditView               = "audit.view"
	PermSpendingView            = "spending.view"
	PermSpendingAdjust          = "spending.adjust"
	PermPermissionsView         = "permissions.view"
	PermPermissionsEdit         = "permissions.edit"
	PermTeamsView               = "teams.view"
	PermTeamsManage             = "teams.manage"
	PermSecretsManage           = "secrets.manage"
	PermUsersManage             = "users.manage"
	PermTemplatesManage         = "templates.manage"
	PermBackupManage            = "backup.manage"
	PermSystemConfig            = "system.config"
	PermAPIKeysManage           = "api_keys.manage"
	PermWebhooksManage          = "webhooks.manage"
	PermSkillsManage            = "skills.manage"
	PermIntegrationsManage      = "integrations.manage"
	PermCapabilitiesManage      = "capabilities.manage"
	PermProvidersManage         = "providers.manage"
	PermChannelsManage          = "channels.manage"
	PermSecurityView            = "security.view"
	PermChatWithAgent           = "agent.chat"
)

const (
	RoleViewer   = "viewer"
	RoleOperator = "operator"
	RoleManager  = "manager"
	RoleAdmin    = "admin"
)

var defaultRoleOrder = []string{RoleViewer, RoleOperator, RoleManager, RoleAdmin}

// RolePermissions maps each role to explicitly granted permissions.
// For default linear roles, Can() also includes lower-tier role permissions.
var RolePermissions = map[string][]string{
	RoleViewer: {
		PermAgentView,
		PermMemoryView,
		PermHistoryView,
		PermSpendingView,
		PermChatWithAgent,
	},
	RoleOperator: {
		PermAgentStart,
		PermAgentStop,
		PermAgentKill,
		PermAgentQuarantine,
		PermAuditView,
		PermTeamsView,
		PermSecurityView,
	},
	RoleManager: {
		PermAgentCreate,
		PermAgentEdit,
		PermMemoryEdit,
		PermSpendingAdjust,
		PermPermissionsView,
		PermPermissionsEdit,
		PermTeamsManage,
		PermWebhooksManage,
		PermSkillsManage,
		PermIntegrationsManage,
		PermCapabilitiesManage,
	},
	RoleAdmin: {
		PermAgentCreateUnrestricted,
		PermAgentDelete,
		PermMemoryBulkDelete,
		PermSecretsManage,
		PermUsersManage,
		PermTemplatesManage,
		PermBackupManage,
		PermSystemConfig,
		PermAPIKeysManage,
		PermProvidersManage,
		PermChannelsManage,
	},
}

// Can reports whether role has permission.
// Default roles (viewer/operator/manager/admin) include lower tiers.
// Non-default roles are looked up directly in RolePermissions.
func Can(role string, permission string) bool {
	perms := effectivePermissions(role)
	_, ok := perms[permission]
	return ok
}

// CanAny reports whether role has any permission in the list.
func CanAny(role string, permissions ...string) bool {
	perms := effectivePermissions(role)
	for _, p := range permissions {
		if _, ok := perms[p]; ok {
			return true
		}
	}
	return false
}

// AllPermissions returns a sorted list of effective permissions for role.
func AllPermissions(role string) []string {
	perms := effectivePermissions(role)
	out := make([]string, 0, len(perms))
	for p := range perms {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func effectivePermissions(role string) map[string]struct{} {
	set := make(map[string]struct{})

	// Default linear hierarchy.
	if idx := roleIndex(role); idx >= 0 {
		for i := 0; i <= idx; i++ {
			for _, p := range RolePermissions[defaultRoleOrder[i]] {
				set[p] = struct{}{}
			}
		}
		return set
	}

	// Non-default custom role: direct lookup only.
	for _, p := range RolePermissions[role] {
		set[p] = struct{}{}
	}
	return set
}

func roleIndex(role string) int {
	for i, r := range defaultRoleOrder {
		if r == role {
			return i
		}
	}
	return -1
}

// IsDefaultRole reports whether role is one of the built-in linear roles.
func IsDefaultRole(role string) bool {
	return roleIndex(role) >= 0
}

// MorePermissiveRole returns the stronger of two default roles.
// For non-default roles, the first argument is returned.
func MorePermissiveRole(a, b string) string {
	ia := roleIndex(a)
	ib := roleIndex(b)
	if ia < 0 || ib < 0 {
		return a
	}
	if ib > ia {
		return b
	}
	return a
}
