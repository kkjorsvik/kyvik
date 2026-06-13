package security

import "fmt"

// TierConfirmation carries the user's explicit acknowledgment when promoting
// an agent to the admin tier.
type TierConfirmation struct {
	Acknowledged     bool   // "I understand this agent will have full system access"
	ConfirmationName string // Must match the agent's name exactly
}

// ValidateElevatedTier checks that the required confirmation is present when
// an agent's tier is set to admin.
//
// Rules:
//   - Admin: Acknowledged must be true AND ConfirmationName must match agentName
//   - All other tiers (reader, worker, operator, guide): no confirmation needed
//
// If oldTier == newTier, no re-confirmation is needed (tier unchanged).
func ValidateElevatedTier(agentName, newTier, oldTier string, confirmation *TierConfirmation) error {
	// No re-confirmation needed if tier didn't change.
	if oldTier != "" && oldTier == newTier {
		return nil
	}

	switch newTier {
	case "admin":
		if confirmation == nil {
			return fmt.Errorf("admin tier requires explicit confirmation")
		}
		if !confirmation.Acknowledged {
			return fmt.Errorf("you must acknowledge that this agent will have full system access")
		}
		if confirmation.ConfirmationName != agentName {
			return fmt.Errorf("confirmation name must match agent name exactly")
		}
		return nil

	default:
		// reader, worker, operator, guide — no confirmation needed
		return nil
	}
}
