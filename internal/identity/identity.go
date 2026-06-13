package identity

// RoleTemplate defines a role/responsibilities template for an agent.
type RoleTemplate struct {
	ID          string
	Name        string
	Description string
	Content     string
}

// GetRoleTemplates returns all available role templates.
func GetRoleTemplates() []RoleTemplate {
	return []RoleTemplate{
		{ID: "general-assistant", Name: "General Assistant", Description: "Broad knowledge, answers questions and performs tasks", Content: roleGeneralAssistant},
		{ID: "researcher", Name: "Researcher", Description: "Investigates topics, summarizes findings, cites sources", Content: roleResearcher},
		{ID: "writer", Name: "Writer", Description: "Creates and edits text content across formats", Content: roleWriter},
		{ID: "devops-monitor", Name: "DevOps Monitor", Description: "Monitors systems, interprets alerts, suggests fixes", Content: roleDevOpsMonitor},
		{ID: "project-manager", Name: "Project Manager", Description: "Tracks tasks, coordinates work, reports status", Content: roleProjectManager},
	}
}

// GetRoleTemplate returns a single role template by ID, or nil if not found.
func GetRoleTemplate(id string) *RoleTemplate {
	for _, t := range GetRoleTemplates() {
		if t.ID == id {
			return &t
		}
	}
	return nil
}
