// Package guide provides the built-in Kyvik guide agent.
package guide

import _ "embed"

// GuideAgentID is the deterministic ID for the guide agent.
const GuideAgentID = "kyvik-guide"

// GuideAgentName is the display name for the guide agent.
const GuideAgentName = "Kyvik"

//go:embed SOUL.md
var SoulContent string

//go:embed IDENTITY.md
var IdentityContent string

// IsGuideAgent returns true if the given agent ID is the guide agent.
func IsGuideAgent(agentID string) bool {
	return agentID == GuideAgentID
}
