package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/kkjorsvik/kyvik/internal/ktp"
	"github.com/kkjorsvik/kyvik/pkg/types"
	"github.com/oklog/ulid/v2"
)

const (
	MaxStepsPerWorkflow     = 30
	MaxWorkflowsPerAgent    = 20
	DefaultExecutionTimeout = 5 * time.Minute
)

// WorkflowStore is the narrow interface the engine needs for persistence.
type WorkflowStore interface {
	CreateWorkflowRun(ctx context.Context, r types.WorkflowRun) error
	UpdateWorkflowRun(ctx context.Context, r types.WorkflowRun) error
}

// ToolExecutor is the interface the engine needs to execute tool calls.
type ToolExecutor interface {
	Execute(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error)
}

// Engine executes workflows by running steps sequentially through a ToolExecutor.
type Engine struct {
	executor ToolExecutor
	store    WorkflowStore
}

// New creates a workflow Engine.
func New(executor ToolExecutor, store WorkflowStore) *Engine {
	return &Engine{executor: executor, store: store}
}

// Execute runs a workflow sequentially, persisting the run and step results.
func (e *Engine) Execute(ctx context.Context, workflow types.Workflow,
	inputVars map[string]any, teamID string) (*types.WorkflowRun, error) {

	if len(workflow.Steps) > MaxStepsPerWorkflow {
		return nil, fmt.Errorf("workflow has %d steps, maximum is %d", len(workflow.Steps), MaxStepsPerWorkflow)
	}

	ctx, cancel := context.WithTimeout(ctx, DefaultExecutionTimeout)
	defer cancel()

	inputJSON, _ := json.Marshal(inputVars)
	run := types.WorkflowRun{
		ID:            ulid.Make().String(),
		WorkflowID:    workflow.ID,
		AgentID:       workflow.AgentID,
		Status:        "running",
		InputVarsJSON: string(inputJSON),
		StartedAt:     time.Now(),
	}
	if err := e.store.CreateWorkflowRun(ctx, run); err != nil {
		return nil, fmt.Errorf("create workflow run: %w", err)
	}

	// Copy input vars so we don't mutate the caller's map.
	vars := make(map[string]any, len(inputVars))
	for k, v := range inputVars {
		vars[k] = v
	}

	startTime := time.Now()
	results := make([]types.WorkflowStepResult, 0, len(workflow.Steps))
	failed := false

	for i, step := range workflow.Steps {
		stepStart := time.Now()

		// Expand params using current vars.
		expandedParams, err := expandParams(step.Params, vars)
		if err != nil {
			results = append(results, types.WorkflowStepResult{
				Name:       step.Name,
				Tool:       step.Tool,
				Action:     step.Action,
				Success:    false,
				Error:      err.Error(),
				DurationMs: time.Since(stepStart).Milliseconds(),
			})
			if step.OnError == "continue" {
				continue
			}
			// Default or "stop": mark remaining as skipped.
			failed = true
			for _, remaining := range workflow.Steps[i+1:] {
				results = append(results, types.WorkflowStepResult{
					Name:    remaining.Name,
					Tool:    remaining.Tool,
					Action:  remaining.Action,
					Skipped: true,
				})
			}
			break
		}

		// Build and execute the tool request.
		req := ktp.NewToolRequest(workflow.AgentID, step.Tool, step.Action, expandedParams)
		req.TeamID = teamID

		resp, err := e.executor.Execute(ctx, req)
		if err != nil {
			results = append(results, types.WorkflowStepResult{
				Name:       step.Name,
				Tool:       step.Tool,
				Action:     step.Action,
				Success:    false,
				Error:      err.Error(),
				DurationMs: time.Since(stepStart).Milliseconds(),
			})
			if step.OnError == "continue" {
				continue
			}
			failed = true
			for _, remaining := range workflow.Steps[i+1:] {
				results = append(results, types.WorkflowStepResult{
					Name:    remaining.Name,
					Tool:    remaining.Tool,
					Action:  remaining.Action,
					Skipped: true,
				})
			}
			break
		}

		stepResult := types.WorkflowStepResult{
			Name:       step.Name,
			Tool:       step.Tool,
			Action:     step.Action,
			Success:    resp.Success,
			Result:     resp.Result,
			Error:      resp.Error,
			DurationMs: time.Since(stepStart).Milliseconds(),
		}
		results = append(results, stepResult)

		if resp.Success && step.SaveAs != "" {
			vars[step.SaveAs] = resp.Result
		}

		if !resp.Success {
			if step.OnError == "continue" {
				continue
			}
			failed = true
			for _, remaining := range workflow.Steps[i+1:] {
				results = append(results, types.WorkflowStepResult{
					Name:    remaining.Name,
					Tool:    remaining.Tool,
					Action:  remaining.Action,
					Skipped: true,
				})
			}
			break
		}
	}

	// Finalize the run.
	now := time.Now()
	run.DurationMs = now.Sub(startTime).Milliseconds()
	run.CompletedAt = &now
	if failed {
		run.Status = "failed"
	} else {
		run.Status = "completed"
	}

	stepsJSON, _ := json.Marshal(results)
	run.StepsJSON = string(stepsJSON)

	if err := e.store.UpdateWorkflowRun(ctx, run); err != nil {
		return nil, fmt.Errorf("update workflow run: %w", err)
	}

	return &run, nil
}
