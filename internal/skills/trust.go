package skills

import "github.com/kkjorsvik/kyvik/pkg/types"

// TrustWarning returns a user-facing warning message for the given trust tier.
// Returns empty string for built-in and verified (no warning needed).
func TrustWarning(tier types.TrustTier) string {
	switch tier {
	case types.TrustBuiltIn, types.TrustVerified:
		return ""
	case types.TrustCommunity:
		return "This skill has not been reviewed. It may contain prompts that modify agent behavior in unexpected ways. Review the skill's documentation and requirements before granting."
	case types.TrustLocal:
		return "This is a locally created skill."
	default:
		return "Unknown trust tier."
	}
}

// RequiresApproval returns whether granting a skill with this trust tier
// needs explicit user approval in the UI.
func RequiresApproval(tier types.TrustTier) bool {
	return tier == types.TrustCommunity
}
