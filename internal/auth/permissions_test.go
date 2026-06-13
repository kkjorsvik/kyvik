package auth

import "testing"

func TestCan_DefaultRoles(t *testing.T) {
	if Can(RoleViewer, PermAgentStart) {
		t.Fatal("viewer should not have agent.start")
	}
	if !Can(RoleOperator, PermAgentStart) {
		t.Fatal("operator should have agent.start")
	}
	if Can(RoleOperator, PermAgentCreate) {
		t.Fatal("operator should not have agent.create")
	}
	if !Can(RoleManager, PermAgentCreate) {
		t.Fatal("manager should have agent.create")
	}
	if !Can(RoleAdmin, PermAgentDelete) {
		t.Fatal("admin should have agent.delete")
	}
	// Inherited lower-tier permissions.
	if !Can(RoleManager, PermAgentView) {
		t.Fatal("manager should inherit viewer permissions")
	}
}

func TestCanAny(t *testing.T) {
	if !CanAny(RoleOperator, PermAgentCreate, PermAgentStop) {
		t.Fatal("operator should satisfy canAny via agent.stop")
	}
	if CanAny(RoleViewer, PermAgentCreate, PermAgentDelete) {
		t.Fatal("viewer should not satisfy canAny for create/delete")
	}
}

func TestAllPermissionsIncludesInherited(t *testing.T) {
	perms := AllPermissions(RoleOperator)

	m := map[string]bool{}
	for _, p := range perms {
		m[p] = true
	}

	if !m[PermAgentView] {
		t.Fatal("operator permissions should include inherited agent.view")
	}
	if !m[PermAgentStop] {
		t.Fatal("operator permissions should include agent.stop")
	}
	if m[PermAgentCreate] {
		t.Fatal("operator permissions should not include agent.create")
	}
}

func TestCustomRoleNoHierarchy(t *testing.T) {
	RolePermissions["developer"] = []string{PermAgentView, PermSkillsManage}
	t.Cleanup(func() { delete(RolePermissions, "developer") })

	if !Can("developer", PermSkillsManage) {
		t.Fatal("developer should have skills.manage")
	}
	if Can("developer", PermAgentStart) {
		t.Fatal("developer should not inherit operator permissions")
	}
}
