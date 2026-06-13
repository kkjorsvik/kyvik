// Package identity provides soul (personality/values) and role (responsibilities)
// presets for agent system prompts.
package identity

// SoulPreset defines a personality/values template for an agent.
type SoulPreset struct {
	ID          string
	Name        string
	Description string
	Content     string
}

// GetSoulPresets returns all available soul presets.
func GetSoulPresets() []SoulPreset {
	return []SoulPreset{
		{ID: "friendly-helper", Name: "Friendly Helper", Description: "Warm, approachable, and eager to assist", Content: soulFriendlyHelper},
		{ID: "professional-analyst", Name: "Professional Analyst", Description: "Precise, data-driven, and methodical", Content: soulProfessionalAnalyst},
		{ID: "creative-thinker", Name: "Creative Thinker", Description: "Imaginative, open-minded, and inventive", Content: soulCreativeThinker},
		{ID: "no-nonsense-operator", Name: "No-Nonsense Operator", Description: "Direct, efficient, and action-oriented", Content: soulNoNonsenseOperator},
		{ID: "kyvik", Name: "Kyvik (Built-in)", Description: "Balanced default personality for Kyvik agents", Content: soulKyvik},
	}
}

// GetSoulPreset returns a single soul preset by ID, or nil if not found.
func GetSoulPreset(id string) *SoulPreset {
	for _, p := range GetSoulPresets() {
		if p.ID == id {
			return &p
		}
	}
	return nil
}

// HeartbeatPreset defines a heartbeat prompt template for agents.
type HeartbeatPreset struct {
	ID          string
	Name        string
	Description string
	Content     string
}

// GetHeartbeatPresets returns all available heartbeat presets.
func GetHeartbeatPresets() []HeartbeatPreset {
	return []HeartbeatPreset{
		{ID: "task-checker", Name: "Task Checker", Description: "Reviews pending tasks, checks overdue items, alerts if needed", Content: heartbeatTaskChecker},
		{ID: "status-reporter", Name: "Status Reporter", Description: "Summarizes current state, pending items, and blockers", Content: heartbeatStatusReporter},
		{ID: "proactive-assistant", Name: "Proactive Assistant", Description: "Thinks about what user needs based on time and context", Content: heartbeatProactiveAssistant},
		{ID: "silent-monitor", Name: "Silent Monitor", Description: "Checks health and activity, alerts only if something is wrong", Content: heartbeatSilentMonitor},
	}
}

// GetHeartbeatPreset returns a single heartbeat preset by ID, or nil if not found.
func GetHeartbeatPreset(id string) *HeartbeatPreset {
	for _, p := range GetHeartbeatPresets() {
		if p.ID == id {
			return &p
		}
	}
	return nil
}
