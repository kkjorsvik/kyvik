package teams

import (
	"strings"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

// CheckMessagePermission checks whether the "from" agent is allowed to send a
// message to the "to" agent. Rules are evaluated in order:
//
//  1. Self-messaging — always allowed.
//  2. Explicit ID — from.CanMessage contains to.ID.
//  3. Team grant — from.CanMessage contains "team:<id>" and to.TeamID matches.
//  4. Implicit team membership — both share the same non-empty TeamID.
//  5. Deny — returns ErrMessageNotPermitted.
func CheckMessagePermission(from, to types.AgentConfig) error {
	// 1. Self-messaging always allowed.
	if from.ID == to.ID {
		return nil
	}

	for _, entry := range from.CanMessage {
		// 2. Explicit agent ID.
		if entry == to.ID {
			return nil
		}

		// 3. Team grant: "team:<id>" where target belongs to that team.
		if teamID, ok := strings.CutPrefix(entry, "team:"); ok {
			if teamID != "" && teamID == to.TeamID {
				return nil
			}
		}
	}

	// 4. Implicit team membership — both in the same non-empty team.
	if from.TeamID != "" && from.TeamID == to.TeamID {
		return nil
	}

	// 5. Deny.
	return types.ErrMessageNotPermitted
}
