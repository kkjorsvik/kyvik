package teams

import (
	"testing"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

func TestCheckMessagePermission(t *testing.T) {
	tests := []struct {
		name    string
		from    types.AgentConfig
		to      types.AgentConfig
		wantErr bool
	}{
		{
			name:    "self-messaging allowed",
			from:    types.AgentConfig{ID: "agent-a"},
			to:      types.AgentConfig{ID: "agent-a"},
			wantErr: false,
		},
		{
			name:    "explicit ID match allowed",
			from:    types.AgentConfig{ID: "agent-a", CanMessage: []string{"agent-b"}},
			to:      types.AgentConfig{ID: "agent-b"},
			wantErr: false,
		},
		{
			name:    "team grant allowed",
			from:    types.AgentConfig{ID: "agent-a", CanMessage: []string{"team:ops"}},
			to:      types.AgentConfig{ID: "agent-b", TeamID: "ops"},
			wantErr: false,
		},
		{
			name:    "implicit team membership allowed",
			from:    types.AgentConfig{ID: "agent-a", TeamID: "ops"},
			to:      types.AgentConfig{ID: "agent-b", TeamID: "ops"},
			wantErr: false,
		},
		{
			name:    "empty can_message and no team denied",
			from:    types.AgentConfig{ID: "agent-a"},
			to:      types.AgentConfig{ID: "agent-b"},
			wantErr: true,
		},
		{
			name:    "wrong explicit ID denied",
			from:    types.AgentConfig{ID: "agent-a", CanMessage: []string{"agent-c"}},
			to:      types.AgentConfig{ID: "agent-b"},
			wantErr: true,
		},
		{
			name:    "team grant with wrong team denied",
			from:    types.AgentConfig{ID: "agent-a", CanMessage: []string{"team:dev"}},
			to:      types.AgentConfig{ID: "agent-b", TeamID: "ops"},
			wantErr: true,
		},
		{
			name:    "different teams no grants denied",
			from:    types.AgentConfig{ID: "agent-a", TeamID: "dev"},
			to:      types.AgentConfig{ID: "agent-b", TeamID: "ops"},
			wantErr: true,
		},
		{
			name:    "team grant with empty team ID on target denied",
			from:    types.AgentConfig{ID: "agent-a", CanMessage: []string{"team:ops"}},
			to:      types.AgentConfig{ID: "agent-b", TeamID: ""},
			wantErr: true,
		},
		{
			name:    "multiple entries one matches allowed",
			from:    types.AgentConfig{ID: "agent-a", CanMessage: []string{"agent-x", "team:dev", "agent-b"}},
			to:      types.AgentConfig{ID: "agent-b"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckMessagePermission(tt.from, tt.to)
			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if tt.wantErr && err != nil && err != types.ErrMessageNotPermitted {
				t.Errorf("expected ErrMessageNotPermitted, got: %v", err)
			}
		})
	}
}
