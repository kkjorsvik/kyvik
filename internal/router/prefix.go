package router

import "strings"

// PrefixResult holds the outcome of a prefix trigger parse.
type PrefixResult struct {
	Matched  bool
	SlotName string // canonical slot name (as configured), empty if not matched
	Message  string // message with prefix stripped, or original if not matched
}

// ParsePrefix checks if message starts with "slotname: " where slotname
// matches one of the configured model slots (case-insensitive).
// Returns the matched slot's canonical name and the message with the prefix stripped.
func ParsePrefix(message string, slots []ModelSlot) PrefixResult {
	idx := strings.IndexByte(message, ':')
	if idx <= 0 {
		return PrefixResult{Message: message}
	}

	// Colon must be followed by a space
	if idx+1 >= len(message) || message[idx+1] != ' ' {
		return PrefixResult{Message: message}
	}

	candidate := strings.ToLower(message[:idx])

	for _, s := range slots {
		if strings.ToLower(s.Name) == candidate {
			return PrefixResult{
				Matched:  true,
				SlotName: s.Name,
				Message:  message[idx+2:], // skip ": "
			}
		}
	}

	return PrefixResult{Message: message}
}
