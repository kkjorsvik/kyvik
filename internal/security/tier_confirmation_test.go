package security

import "testing"

func TestValidateElevatedTier(t *testing.T) {
	tests := []struct {
		name         string
		agentName    string
		newTier      string
		oldTier      string
		confirmation *TierConfirmation
		wantErr      bool
		errContains  string
	}{
		// Non-elevated tiers — no confirmation needed.
		{
			name:    "reader tier needs no confirmation",
			newTier: "reader",
			wantErr: false,
		},
		{
			name:    "worker tier needs no confirmation",
			newTier: "worker",
			wantErr: false,
		},

		// Admin tier — requires acknowledgment + name match.
		{
			name:      "admin with valid full confirmation",
			agentName: "my-agent",
			newTier:   "admin",
			confirmation: &TierConfirmation{
				Acknowledged:     true,
				ConfirmationName: "my-agent",
			},
			wantErr: false,
		},
		{
			name:        "admin with nil confirmation",
			agentName:   "my-agent",
			newTier:     "admin",
			wantErr:     true,
			errContains: "admin tier requires explicit confirmation",
		},
		{
			name:      "admin with acknowledged false",
			agentName: "my-agent",
			newTier:   "admin",
			confirmation: &TierConfirmation{
				Acknowledged:     false,
				ConfirmationName: "my-agent",
			},
			wantErr:     true,
			errContains: "you must acknowledge",
		},
		{
			name:      "admin with wrong name",
			agentName: "my-agent",
			newTier:   "admin",
			confirmation: &TierConfirmation{
				Acknowledged:     true,
				ConfirmationName: "other-agent",
			},
			wantErr:     true,
			errContains: "confirmation name must match",
		},
		{
			name:      "admin with wrong case name",
			agentName: "My-Agent",
			newTier:   "admin",
			confirmation: &TierConfirmation{
				Acknowledged:     true,
				ConfirmationName: "my-agent",
			},
			wantErr:     true,
			errContains: "confirmation name must match",
		},
		{
			name:      "admin acknowledged but no name",
			agentName: "my-agent",
			newTier:   "admin",
			confirmation: &TierConfirmation{
				Acknowledged:     true,
				ConfirmationName: "",
			},
			wantErr:     true,
			errContains: "confirmation name must match",
		},

		// Tier unchanged — no re-confirmation needed.
		{
			name:    "same tier admin to admin",
			newTier: "admin",
			oldTier: "admin",
			// No confirmation at all — should pass.
			wantErr: false,
		},

		// Empty oldTier (creation) requires full validation.
		{
			name:      "creation with empty oldTier requires validation",
			agentName: "my-agent",
			newTier:   "admin",
			oldTier:   "",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateElevatedTier(tt.agentName, tt.newTier, tt.oldTier, tt.confirmation)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.errContains != "" && !containsSubstring(err.Error(), tt.errContains) {
					t.Errorf("error %q should contain %q", err.Error(), tt.errContains)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			}
		})
	}
}

// TestValidateElevatedTier_AdminRequiresFullConfirmation verifies that admin
// tier requires both Acknowledged=true and ConfirmationName matching.
func TestValidateElevatedTier_AdminRequiresFullConfirmation(t *testing.T) {
	// Missing both
	err := ValidateElevatedTier("test-agent", "admin", "", nil)
	if err == nil {
		t.Fatal("expected error for nil confirmation, got nil")
	}

	// Acknowledged only (no name match)
	err = ValidateElevatedTier("test-agent", "admin", "", &TierConfirmation{
		Acknowledged:     true,
		ConfirmationName: "",
	})
	if err == nil {
		t.Fatal("expected error for missing ConfirmationName, got nil")
	}

	// Name match only (not acknowledged)
	err = ValidateElevatedTier("test-agent", "admin", "", &TierConfirmation{
		Acknowledged:     false,
		ConfirmationName: "test-agent",
	})
	if err == nil {
		t.Fatal("expected error for Acknowledged=false, got nil")
	}

	// Both present and correct
	err = ValidateElevatedTier("test-agent", "admin", "", &TierConfirmation{
		Acknowledged:     true,
		ConfirmationName: "test-agent",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestValidateElevatedTier_OperatorNoConfirmation verifies that the operator
// tier does not require any confirmation.
func TestValidateElevatedTier_OperatorNoConfirmation(t *testing.T) {
	err := ValidateElevatedTier("test-agent", "operator", "", nil)
	if err != nil {
		t.Fatalf("operator should not require confirmation, got: %v", err)
	}
}

// TestValidateElevatedTier_AdminUnchangedNoConfirmation verifies that an
// admin->admin transition does not require re-confirmation.
func TestValidateElevatedTier_AdminUnchangedNoConfirmation(t *testing.T) {
	err := ValidateElevatedTier("test-agent", "admin", "admin", nil)
	if err != nil {
		t.Fatalf("admin->admin should not require re-confirmation, got: %v", err)
	}
}

func containsSubstring(s, sub string) bool {
	return len(s) >= len(sub) && searchSubstring(s, sub)
}

func searchSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
