// Package testtools provides minimal KTP tool implementations for testing.
package testtools

import (
	"context"
	"errors"

	"github.com/kkjorsvik/kyvik/internal/ktp"
)

// EchoTool is a minimal KTP tool that echoes back its input.
type EchoTool struct{}

// Declaration returns the EchoTool's KTP declaration.
func (e *EchoTool) Declaration() ktp.ToolDeclaration {
	return ktp.ToolDeclaration{
		Name:        "echo",
		Version:     "1.0.0",
		Description: "Echoes back input for testing",
		MinTier:     ktp.TierWriter,
		Actions: []ktp.ActionSpec{{
			Name:        "echo",
			Description: "Echo the message back",
			Parameters: ktp.JSONSchema{
				Type: "object",
				Properties: map[string]ktp.JSONSchema{
					"message": {Type: "string"},
				},
				Required: []string{"message"},
			},
			Returns: ktp.JSONSchema{
				Type: "object",
				Properties: map[string]ktp.JSONSchema{
					"echo": {Type: "string"},
				},
			},
		}},
	}
}

// Execute echoes back the "message" parameter.
func (e *EchoTool) Execute(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	raw, ok := req.Parameters["message"]
	if !ok {
		return nil, errors.New("missing required parameter: message")
	}
	msg, ok := raw.(string)
	if !ok || msg == "" {
		return nil, errors.New("invalid parameter: message must be a non-empty string")
	}
	resp := ktp.NewToolResponse(req.ID, true, map[string]any{"echo": msg}, "", 0)
	return &resp, nil
}
