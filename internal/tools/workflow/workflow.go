// Package workflow implements a KTP tool that lets agents create, manage, and
// execute multi-step workflows. Each workflow is a saved sequence of tool calls
// that can be executed atomically.
package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/kkjorsvik/kyvik/internal/ktp"
	"github.com/kkjorsvik/kyvik/internal/timeutil"
	engine "github.com/kkjorsvik/kyvik/internal/workflow"
	"github.com/kkjorsvik/kyvik/pkg/types"
	"github.com/oklog/ulid/v2"
)

// StoreInterface is the narrow interface the workflow tool needs for persistence.
type StoreInterface interface {
	CreateWorkflow(ctx context.Context, w types.Workflow) error
	GetWorkflow(ctx context.Context, id string) (*types.Workflow, error)
	GetWorkflowByName(ctx context.Context, agentID, name string) (*types.Workflow, error)
	UpdateWorkflow(ctx context.Context, w types.Workflow) error
	DeleteWorkflow(ctx context.Context, id string) error
	ListWorkflows(ctx context.Context, agentID string) ([]types.Workflow, error)
	ListWorkflowRuns(ctx context.Context, workflowID string, limit int) ([]types.WorkflowRun, error)
	// WorkflowStore methods needed by the engine.
	CreateWorkflowRun(ctx context.Context, r types.WorkflowRun) error
	UpdateWorkflowRun(ctx context.Context, r types.WorkflowRun) error
}

// Tool implements ktp.InlineTool for workflow management.
type Tool struct {
	engine *engine.Engine
	store  StoreInterface
}

// New creates a workflow tool backed by the given engine and store.
func New(eng *engine.Engine, store StoreInterface) *Tool {
	return &Tool{engine: eng, store: store}
}

// Inline returns true because the workflow tool manages local state only.
func (t *Tool) Inline() bool { return true }

// Declaration returns the workflow tool's KTP declaration.
func (t *Tool) Declaration() ktp.ToolDeclaration {
	return ktp.ToolDeclaration{
		Name:    "workflow",
		Version: "1.0.0",
		Description: "Create and manage multi-step workflows — saved sequences of tool calls that execute atomically. " +
			"Use workflows to automate repetitive multi-tool tasks like 'fetch data, transform, save'. " +
			"Steps run sequentially; each step calls a KTP tool action. Use {{variable}} in step params to reference " +
			"results from earlier steps (via save_as). You can have up to 20 workflows with up to 30 steps each. " +
			"Always list workflows first to check existing ones before creating new ones.",
		MinTier:      ktp.TierWriter,
		DefaultTiers: []string{ktp.TierWriter, ktp.TierOperator, ktp.TierAdmin},
		Actions: []ktp.ActionSpec{
			{
				Name: "create",
				Description: "Create a new workflow. Define the steps as an ordered array of tool calls. " +
					"Each step specifies a tool, action, and params. Use save_as to capture a step's result " +
					"for use in later steps via {{variable}} template syntax in params.",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"name":        {Type: "string", Description: "Unique name for this workflow (per agent)"},
						"description": {Type: "string", Description: "Human-readable description of what the workflow does"},
						"steps": {
							Type:        "array",
							Description: "Ordered list of tool call steps",
							Items: &ktp.JSONSchema{
								Type: "object",
								Properties: map[string]ktp.JSONSchema{
									"name":     {Type: "string", Description: "Step label for identification in results"},
									"tool":     {Type: "string", Description: "KTP tool name to invoke"},
									"action":   {Type: "string", Description: "Tool action to call"},
									"params":   {Type: "object", Description: "Parameters for the tool call. Use {{var}} to reference saved results"},
									"save_as":  {Type: "string", Description: "Save this step's result under this variable name for later steps"},
									"on_error": {Type: "string", Description: "Error handling: 'stop' (default) or 'continue'", Enum: []string{"stop", "continue"}},
								},
								Required: []string{"name", "tool", "action"},
							},
						},
					},
					Required: []string{"name", "steps"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"id":   {Type: "string", Description: "The created workflow ID"},
						"name": {Type: "string"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "workflow", Access: "write", Resource: "*"}},
			},
			{
				Name: "execute",
				Description: "Execute a workflow by name or ID. Optionally provide input variables that can be " +
					"referenced in step params via {{variable}} syntax.",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"name": {Type: "string", Description: "Workflow name (alternative to id)"},
						"id":   {Type: "string", Description: "Workflow ID (alternative to name)"},
						"vars": {Type: "object", Description: "Input variables available to step params via {{variable}} templates"},
					},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"run": {Type: "object", Description: "The workflow run result with status, steps, and duration"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "workflow", Access: "execute", Resource: "*"}},
			},
			{
				Name:        "list",
				Description: "List all your workflows with their IDs, names, step counts, and enabled status.",
				Parameters: ktp.JSONSchema{
					Type:       "object",
					Properties: map[string]ktp.JSONSchema{},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"workflows": {Type: "array", Items: &ktp.JSONSchema{Type: "object"}},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "workflow", Access: "read", Resource: "*"}},
			},
			{
				Name: "get",
				Description: "Get details of a workflow by name or ID, including its steps and the last 5 execution runs.",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"name": {Type: "string", Description: "Workflow name (alternative to id)"},
						"id":   {Type: "string", Description: "Workflow ID (alternative to name)"},
					},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"workflow":    {Type: "object"},
						"recent_runs": {Type: "array", Items: &ktp.JSONSchema{Type: "object"}},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "workflow", Access: "read", Resource: "*"}},
			},
			{
				Name:        "update",
				Description: "Update an existing workflow. Only provide fields you want to change; omitted fields keep their current values.",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"id":          {Type: "string", Description: "Workflow ID to update"},
						"name":        {Type: "string", Description: "New name"},
						"description": {Type: "string", Description: "New description"},
						"steps": {
							Type:        "array",
							Description: "New steps (replaces all existing steps)",
							Items: &ktp.JSONSchema{
								Type: "object",
								Properties: map[string]ktp.JSONSchema{
									"name":     {Type: "string"},
									"tool":     {Type: "string"},
									"action":   {Type: "string"},
									"params":   {Type: "object"},
									"save_as":  {Type: "string"},
									"on_error": {Type: "string", Enum: []string{"stop", "continue"}},
								},
								Required: []string{"name", "tool", "action"},
							},
						},
						"enabled": {Type: "boolean", Description: "Enable or disable the workflow"},
					},
					Required: []string{"id"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"updated": {Type: "boolean"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "workflow", Access: "write", Resource: "*"}},
			},
			{
				Name:        "delete",
				Description: "Permanently delete a workflow. Use get first to confirm the correct ID.",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"id": {Type: "string", Description: "Workflow ID to delete"},
					},
					Required: []string{"id"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"deleted": {Type: "boolean"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "workflow", Access: "write", Resource: "*"}},
				Destructive:          true,
			},
		},
	}
}

// Execute dispatches to the requested action.
func (t *Tool) Execute(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	start := time.Now()

	switch req.Action {
	case "create":
		return t.create(ctx, req, start)
	case "execute":
		return t.execute(ctx, req, start)
	case "list":
		return t.list(ctx, req, start)
	case "get":
		return t.get(ctx, req, start)
	case "update":
		return t.update(ctx, req, start)
	case "delete":
		return t.deleteWorkflow(ctx, req, start)
	default:
		return errResp(req.ID, fmt.Sprintf("unknown action: %s", req.Action)), nil
	}
}

func (t *Tool) create(ctx context.Context, req ktp.ToolRequest, start time.Time) (*ktp.ToolResponse, error) {
	name, err := strParam(req.Parameters, "name")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	// Parse steps from the raw JSON parameters.
	stepsRaw, ok := req.Parameters["steps"]
	if !ok {
		return errResp(req.ID, "missing required parameter: steps"), nil
	}
	steps, err := parseSteps(stepsRaw)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("invalid steps: %s", err)), nil
	}
	if len(steps) == 0 {
		return errResp(req.ID, "workflow must have at least one step"), nil
	}
	if len(steps) > engine.MaxStepsPerWorkflow {
		return errResp(req.ID, fmt.Sprintf("workflow has %d steps, maximum is %d", len(steps), engine.MaxStepsPerWorkflow)), nil
	}

	// Enforce per-agent limit.
	existing, err := t.store.ListWorkflows(ctx, req.AgentID)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("failed to check existing workflows: %s", err)), nil
	}
	if len(existing) >= engine.MaxWorkflowsPerAgent {
		return errResp(req.ID, fmt.Sprintf("maximum of %d workflows reached", engine.MaxWorkflowsPerAgent)), nil
	}

	description := strDefault(req.Parameters, "description", "")
	now := timeutil.NowUTC()
	wf := types.Workflow{
		ID:          ulid.Make().String(),
		AgentID:     req.AgentID,
		Name:        name,
		Description: description,
		Steps:       steps,
		Enabled:     true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if err := t.store.CreateWorkflow(ctx, wf); err != nil {
		return errResp(req.ID, fmt.Sprintf("failed to create workflow: %s", err)), nil
	}

	resp := ktp.NewToolResponse(req.ID, true, map[string]any{
		"id":   wf.ID,
		"name": wf.Name,
	}, "", ms(start))
	return &resp, nil
}

func (t *Tool) execute(ctx context.Context, req ktp.ToolRequest, start time.Time) (*ktp.ToolResponse, error) {
	wf, err := t.lookupWorkflow(ctx, req)
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}
	if !wf.Enabled {
		return errResp(req.ID, "workflow is disabled"), nil
	}

	// Parse optional input variables.
	var vars map[string]any
	if raw, ok := req.Parameters["vars"]; ok {
		if m, ok := raw.(map[string]any); ok {
			vars = m
		}
	}

	run, err := t.engine.Execute(ctx, *wf, vars, req.TeamID)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("workflow execution failed: %s", err)), nil
	}

	resp := ktp.NewToolResponse(req.ID, true, map[string]any{
		"run": run,
	}, "", ms(start))
	return &resp, nil
}

func (t *Tool) list(ctx context.Context, req ktp.ToolRequest, start time.Time) (*ktp.ToolResponse, error) {
	workflows, err := t.store.ListWorkflows(ctx, req.AgentID)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("failed to list workflows: %s", err)), nil
	}

	items := make([]map[string]any, 0, len(workflows))
	for _, wf := range workflows {
		items = append(items, map[string]any{
			"id":          wf.ID,
			"name":        wf.Name,
			"description": wf.Description,
			"steps_count": len(wf.Steps),
			"enabled":     wf.Enabled,
			"created_at":  wf.CreatedAt.UTC().Format(time.RFC3339),
			"updated_at":  wf.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}

	resp := ktp.NewToolResponse(req.ID, true, map[string]any{"workflows": items}, "", ms(start))
	return &resp, nil
}

func (t *Tool) get(ctx context.Context, req ktp.ToolRequest, start time.Time) (*ktp.ToolResponse, error) {
	wf, err := t.lookupWorkflow(ctx, req)
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	// Get last 5 runs.
	runs, err := t.store.ListWorkflowRuns(ctx, wf.ID, 5)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("failed to list workflow runs: %s", err)), nil
	}

	runItems := make([]map[string]any, 0, len(runs))
	for _, r := range runs {
		entry := map[string]any{
			"id":          r.ID,
			"status":      r.Status,
			"duration_ms": r.DurationMs,
			"started_at":  r.StartedAt.UTC().Format(time.RFC3339),
		}
		if r.CompletedAt != nil {
			entry["completed_at"] = r.CompletedAt.UTC().Format(time.RFC3339)
		}
		if r.Error != "" {
			entry["error"] = r.Error
		}
		runItems = append(runItems, entry)
	}

	resp := ktp.NewToolResponse(req.ID, true, map[string]any{
		"workflow":    wf,
		"recent_runs": runItems,
	}, "", ms(start))
	return &resp, nil
}

func (t *Tool) update(ctx context.Context, req ktp.ToolRequest, start time.Time) (*ktp.ToolResponse, error) {
	id, err := strParam(req.Parameters, "id")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	wf, err := t.store.GetWorkflow(ctx, id)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("workflow not found: %s", err)), nil
	}
	if wf.AgentID != req.AgentID {
		return errResp(req.ID, "workflow does not belong to this agent"), nil
	}

	// Apply updates.
	if v, ok := req.Parameters["name"]; ok {
		if s, ok := v.(string); ok && s != "" {
			wf.Name = s
		}
	}
	if v, ok := req.Parameters["description"]; ok {
		if s, ok := v.(string); ok {
			wf.Description = s
		}
	}
	if raw, ok := req.Parameters["steps"]; ok {
		steps, err := parseSteps(raw)
		if err != nil {
			return errResp(req.ID, fmt.Sprintf("invalid steps: %s", err)), nil
		}
		if len(steps) == 0 {
			return errResp(req.ID, "workflow must have at least one step"), nil
		}
		if len(steps) > engine.MaxStepsPerWorkflow {
			return errResp(req.ID, fmt.Sprintf("workflow has %d steps, maximum is %d", len(steps), engine.MaxStepsPerWorkflow)), nil
		}
		wf.Steps = steps
	}
	if v, ok := req.Parameters["enabled"]; ok {
		if b, ok := v.(bool); ok {
			wf.Enabled = b
		}
	}
	wf.UpdatedAt = timeutil.NowUTC()

	if err := t.store.UpdateWorkflow(ctx, *wf); err != nil {
		return errResp(req.ID, fmt.Sprintf("failed to update workflow: %s", err)), nil
	}

	resp := ktp.NewToolResponse(req.ID, true, map[string]any{"updated": true}, "", ms(start))
	return &resp, nil
}

func (t *Tool) deleteWorkflow(ctx context.Context, req ktp.ToolRequest, start time.Time) (*ktp.ToolResponse, error) {
	id, err := strParam(req.Parameters, "id")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	// Verify ownership before deleting.
	wf, err := t.store.GetWorkflow(ctx, id)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("workflow not found: %s", err)), nil
	}
	if wf.AgentID != req.AgentID {
		return errResp(req.ID, "workflow does not belong to this agent"), nil
	}

	if err := t.store.DeleteWorkflow(ctx, id); err != nil {
		return errResp(req.ID, fmt.Sprintf("failed to delete workflow: %s", err)), nil
	}

	resp := ktp.NewToolResponse(req.ID, true, map[string]any{"deleted": true}, "", ms(start))
	return &resp, nil
}

// lookupWorkflow finds a workflow by name or id from the request parameters.
func (t *Tool) lookupWorkflow(ctx context.Context, req ktp.ToolRequest) (*types.Workflow, error) {
	if id, ok := req.Parameters["id"]; ok {
		if s, ok := id.(string); ok && s != "" {
			wf, err := t.store.GetWorkflow(ctx, s)
			if err != nil {
				return nil, fmt.Errorf("workflow not found: %s", err)
			}
			if wf.AgentID != req.AgentID {
				return nil, fmt.Errorf("workflow does not belong to this agent")
			}
			return wf, nil
		}
	}
	if name, ok := req.Parameters["name"]; ok {
		if s, ok := name.(string); ok && s != "" {
			wf, err := t.store.GetWorkflowByName(ctx, req.AgentID, s)
			if err != nil {
				return nil, fmt.Errorf("workflow not found: %s", err)
			}
			return wf, nil
		}
	}
	return nil, fmt.Errorf("either 'name' or 'id' is required")
}

// parseSteps converts the raw steps parameter ([]any from JSON) into typed WorkflowStep slice.
func parseSteps(raw any) ([]types.WorkflowStep, error) {
	stepsJSON, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal steps: %w", err)
	}
	var steps []types.WorkflowStep
	if err := json.Unmarshal(stepsJSON, &steps); err != nil {
		return nil, fmt.Errorf("failed to parse steps: %w", err)
	}
	return steps, nil
}

// --- parameter helpers ---

func strParam(params map[string]any, key string) (string, error) {
	raw, ok := params[key]
	if !ok {
		return "", fmt.Errorf("missing required parameter: %s", key)
	}
	s, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("parameter %s must be a string", key)
	}
	return s, nil
}

func strDefault(params map[string]any, key, def string) string {
	raw, ok := params[key]
	if !ok {
		return def
	}
	s, ok := raw.(string)
	if !ok {
		return def
	}
	return s
}

func errResp(reqID, msg string) *ktp.ToolResponse {
	resp := ktp.NewToolResponse(reqID, false, nil, msg, 0)
	return &resp
}

func ms(start time.Time) int64 {
	return time.Since(start).Milliseconds()
}
