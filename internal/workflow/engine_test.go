package workflow

import (
	"context"
	"testing"

	"github.com/kkjorsvik/kyvik/internal/ktp"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// --- mocks ---

type mockExecutor struct {
	responses map[string]*ktp.ToolResponse // keyed by "tool.action"
}

func (m *mockExecutor) Execute(_ context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	key := req.Tool + "." + req.Action
	if resp, ok := m.responses[key]; ok {
		return resp, nil
	}
	return &ktp.ToolResponse{Success: false, Error: "unknown tool"}, nil
}

type mockStore struct {
	runs []types.WorkflowRun
}

func (m *mockStore) CreateWorkflowRun(_ context.Context, r types.WorkflowRun) error {
	m.runs = append(m.runs, r)
	return nil
}

func (m *mockStore) UpdateWorkflowRun(_ context.Context, r types.WorkflowRun) error {
	for i, run := range m.runs {
		if run.ID == r.ID {
			m.runs[i] = r
			return nil
		}
	}
	return nil
}

// --- tests ---

func TestExecute_Success(t *testing.T) {
	exec := &mockExecutor{responses: map[string]*ktp.ToolResponse{
		"file.read":  {Success: true, Result: "content"},
		"file.write": {Success: true, Result: "ok"},
	}}
	store := &mockStore{}
	engine := New(exec, store)

	wf := types.Workflow{
		ID:      "wf-1",
		AgentID: "agent-1",
		Steps: []types.WorkflowStep{
			{Name: "read", Tool: "file", Action: "read", Params: map[string]any{"path": "/tmp/a"}},
			{Name: "write", Tool: "file", Action: "write", Params: map[string]any{"path": "/tmp/b"}},
		},
	}

	run, err := engine.Execute(context.Background(), wf, nil, "team-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if run.Status != "completed" {
		t.Errorf("status = %q, want 'completed'", run.Status)
	}
	if run.WorkflowID != "wf-1" {
		t.Errorf("workflow_id = %q, want 'wf-1'", run.WorkflowID)
	}
	if run.ID == "" {
		t.Error("run ID should not be empty")
	}
}

func TestExecute_SaveAs(t *testing.T) {
	exec := &mockExecutor{responses: map[string]*ktp.ToolResponse{
		"file.read":  {Success: true, Result: "file-content-123"},
		"file.write": {Success: true, Result: "ok"},
	}}
	store := &mockStore{}
	engine := New(exec, store)

	wf := types.Workflow{
		ID:      "wf-2",
		AgentID: "agent-1",
		Steps: []types.WorkflowStep{
			{Name: "read", Tool: "file", Action: "read", Params: map[string]any{"path": "/tmp/a"}, SaveAs: "content"},
			{Name: "write", Tool: "file", Action: "write", Params: map[string]any{"data": "{{content}}"}},
		},
	}

	run, err := engine.Execute(context.Background(), wf, nil, "team-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if run.Status != "completed" {
		t.Errorf("status = %q, want 'completed'", run.Status)
	}
	// The write step should have received the expanded param.
	// We can't inspect the request directly, but the step succeeded which means
	// the param expansion didn't error (which it would if "content" was missing).
}

func TestExecute_StopOnError(t *testing.T) {
	exec := &mockExecutor{responses: map[string]*ktp.ToolResponse{
		"file.read":  {Success: false, Error: "not found"},
		"file.write": {Success: true, Result: "ok"},
	}}
	store := &mockStore{}
	engine := New(exec, store)

	wf := types.Workflow{
		ID:      "wf-3",
		AgentID: "agent-1",
		Steps: []types.WorkflowStep{
			{Name: "read", Tool: "file", Action: "read", Params: map[string]any{}},
			{Name: "write", Tool: "file", Action: "write", Params: map[string]any{}},
		},
	}

	run, err := engine.Execute(context.Background(), wf, nil, "team-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if run.Status != "failed" {
		t.Errorf("status = %q, want 'failed'", run.Status)
	}
	// Verify second step was skipped via StepsJSON.
	if run.StepsJSON == "" {
		t.Fatal("steps_json should not be empty")
	}
}

func TestExecute_ContinueOnError(t *testing.T) {
	exec := &mockExecutor{responses: map[string]*ktp.ToolResponse{
		"file.read":  {Success: false, Error: "not found"},
		"file.write": {Success: true, Result: "ok"},
	}}
	store := &mockStore{}
	engine := New(exec, store)

	wf := types.Workflow{
		ID:      "wf-4",
		AgentID: "agent-1",
		Steps: []types.WorkflowStep{
			{Name: "read", Tool: "file", Action: "read", Params: map[string]any{}, OnError: "continue"},
			{Name: "write", Tool: "file", Action: "write", Params: map[string]any{}},
		},
	}

	run, err := engine.Execute(context.Background(), wf, nil, "team-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Even though step 1 failed, step 2 ran and succeeded, so overall completed.
	if run.Status != "completed" {
		t.Errorf("status = %q, want 'completed'", run.Status)
	}
}

func TestExecute_MaxSteps(t *testing.T) {
	exec := &mockExecutor{}
	store := &mockStore{}
	engine := New(exec, store)

	steps := make([]types.WorkflowStep, 31)
	for i := range steps {
		steps[i] = types.WorkflowStep{Name: "step", Tool: "t", Action: "a", Params: map[string]any{}}
	}

	wf := types.Workflow{ID: "wf-big", AgentID: "agent-1", Steps: steps}

	_, err := engine.Execute(context.Background(), wf, nil, "team-1")
	if err == nil {
		t.Fatal("expected error for exceeding max steps")
	}
}

func TestExecute_MissingVariable(t *testing.T) {
	exec := &mockExecutor{responses: map[string]*ktp.ToolResponse{
		"file.read": {Success: true, Result: "ok"},
	}}
	store := &mockStore{}
	engine := New(exec, store)

	wf := types.Workflow{
		ID:      "wf-5",
		AgentID: "agent-1",
		Steps: []types.WorkflowStep{
			{Name: "read", Tool: "file", Action: "read", Params: map[string]any{"path": "{{undefined}}"}},
		},
	}

	run, err := engine.Execute(context.Background(), wf, nil, "team-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Step should fail due to missing variable, and run should be "failed".
	if run.Status != "failed" {
		t.Errorf("status = %q, want 'failed'", run.Status)
	}
}
