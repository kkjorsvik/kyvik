package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/kkjorsvik/kyvik/internal/store"
	"github.com/kkjorsvik/kyvik/internal/timeutil"
	"github.com/kkjorsvik/kyvik/internal/workflow"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// SetWorkflowEngine sets the workflow engine on the handlers, enabling manual execution.
func (h *Handlers) SetWorkflowEngine(e *workflow.Engine) {
	h.workflowEngine = e
}

// WorkflowList renders the workflows card on the agent detail page.
func (h *Handlers) WorkflowList(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	s, ok := h.kyvik.Store().(store.Store)
	if !ok || s == nil {
		h.renderFragment(w, r, "card-workflows", map[string]any{
			"AgentID":   id,
			"Workflows": nil,
		})
		return
	}

	workflows, err := s.ListWorkflows(ctx, id)
	if err != nil {
		http.Error(w, "failed to list workflows", http.StatusInternalServerError)
		return
	}

	// Build run counts for each workflow.
	type workflowWithMeta struct {
		types.Workflow
		StepCount    int
		LastRunAt    string
		LastRunStatus string
	}
	var items []workflowWithMeta
	for _, wf := range workflows {
		meta := workflowWithMeta{
			Workflow:   wf,
			StepCount:  len(wf.Steps),
		}
		runs, _ := s.ListWorkflowRuns(ctx, wf.ID, 1)
		if len(runs) > 0 {
			meta.LastRunAt = runs[0].StartedAt.Format("Jan 02 15:04")
			meta.LastRunStatus = runs[0].Status
		}
		items = append(items, meta)
	}

	data := map[string]any{
		"AgentID":   id,
		"Workflows": items,
	}
	h.injectTemplateUser(ctx, data)
	h.renderFragment(w, r, "card-workflows", data)
}

// WorkflowDetail renders a workflow detail page with definition and run history.
func (h *Handlers) WorkflowDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	wid := r.PathValue("wid")
	ctx := r.Context()

	s, ok := h.kyvik.Store().(store.Store)
	if !ok || s == nil {
		http.Error(w, "store not available", http.StatusInternalServerError)
		return
	}

	wf, err := s.GetWorkflow(ctx, wid)
	if err != nil {
		http.Error(w, "workflow not found", http.StatusNotFound)
		return
	}

	runs, _ := s.ListWorkflowRuns(ctx, wid, 10)

	agent, err := h.kyvik.GetAgent(ctx, id)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	data := map[string]any{
		"Nav":       "agents",
		"Title":     wf.Name + " - Workflow",
		"Agent":     agent,
		"Workflow":  wf,
		"Runs":      runs,
		"AgentID":   id,
	}

	if isHTMX(r) {
		h.renderFragment(w, r, "workflow-detail", data)
		return
	}
	h.renderPageWithRequest(w, r, "workflow-detail", data)
}

// WorkflowEditModal renders the edit dialog for a workflow.
func (h *Handlers) WorkflowEditModal(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	wid := r.PathValue("wid")
	ctx := r.Context()

	s, ok := h.kyvik.Store().(store.Store)
	if !ok || s == nil {
		http.Error(w, "store not available", http.StatusInternalServerError)
		return
	}

	wf, err := s.GetWorkflow(ctx, wid)
	if err != nil {
		http.Error(w, "workflow not found", http.StatusNotFound)
		return
	}

	h.renderFragment(w, r, "workflow-edit-modal", map[string]any{
		"AgentID":  id,
		"Workflow": wf,
	})
}

// WorkflowUpdate saves changes to a workflow.
func (h *Handlers) WorkflowUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	wid := r.PathValue("wid")
	ctx := r.Context()

	s, ok := h.kyvik.Store().(store.Store)
	if !ok || s == nil {
		http.Error(w, "store not available", http.StatusInternalServerError)
		return
	}

	wf, err := s.GetWorkflow(ctx, wid)
	if err != nil {
		http.Error(w, "workflow not found", http.StatusNotFound)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form data", http.StatusBadRequest)
		return
	}

	if v := r.FormValue("name"); v != "" {
		wf.Name = v
	}
	wf.Description = r.FormValue("description")

	// Handle enabled checkbox — unchecked means field absent from form.
	wf.Enabled = r.Form.Has("enabled")

	// Parse steps JSON.
	stepsJSON := r.FormValue("steps_json")
	if stepsJSON != "" {
		var steps []types.WorkflowStep
		if err := json.Unmarshal([]byte(stepsJSON), &steps); err != nil {
			http.Error(w, "invalid steps JSON", http.StatusBadRequest)
			return
		}
		wf.Steps = steps
	}

	wf.UpdatedAt = timeutil.NowUTC()
	if err := s.UpdateWorkflow(ctx, *wf); err != nil {
		http.Error(w, fmt.Sprintf("failed to update workflow: %v", err), http.StatusBadRequest)
		return
	}

	_ = id // used for route pattern
	// Re-render the workflows card by redirecting the HTMX request.
	w.Header().Set("HX-Redirect", fmt.Sprintf("/agents/%s/workflows/%s", id, wid))
	w.WriteHeader(http.StatusOK)
}

// WorkflowToggle enables/disables a workflow.
func (h *Handlers) WorkflowToggle(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	wid := r.PathValue("wid")
	ctx := r.Context()

	s, ok := h.kyvik.Store().(store.Store)
	if !ok || s == nil {
		http.Error(w, "store not available", http.StatusInternalServerError)
		return
	}

	wf, err := s.GetWorkflow(ctx, wid)
	if err != nil {
		http.Error(w, "workflow not found", http.StatusNotFound)
		return
	}

	wf.Enabled = !wf.Enabled
	wf.UpdatedAt = timeutil.NowUTC()
	if err := s.UpdateWorkflow(ctx, *wf); err != nil {
		http.Error(w, fmt.Sprintf("failed to toggle workflow: %v", err), http.StatusInternalServerError)
		return
	}

	// Redirect back to wherever makes sense.
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", fmt.Sprintf("/agents/%s/workflows/%s", id, wid))
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/agents/%s/workflows/%s", id, wid), http.StatusSeeOther)
}

// WorkflowExecute manually triggers a workflow.
func (h *Handlers) WorkflowExecute(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	wid := r.PathValue("wid")
	ctx := r.Context()

	s, ok := h.kyvik.Store().(store.Store)
	if !ok || s == nil {
		http.Error(w, "store not available", http.StatusInternalServerError)
		return
	}

	if h.workflowEngine == nil {
		http.Error(w, "workflow engine not available", http.StatusServiceUnavailable)
		return
	}

	wf, err := s.GetWorkflow(ctx, wid)
	if err != nil {
		http.Error(w, "workflow not found", http.StatusNotFound)
		return
	}

	// Parse optional input variables from form.
	var inputVars map[string]any
	if err := r.ParseForm(); err == nil {
		if varsJSON := r.FormValue("vars"); varsJSON != "" {
			if err := json.Unmarshal([]byte(varsJSON), &inputVars); err != nil {
				http.Error(w, "invalid vars JSON", http.StatusBadRequest)
				return
			}
		}
	}

	// Execute the workflow (runs synchronously for now).
	run, err := h.workflowEngine.Execute(ctx, *wf, inputVars, "")
	if err != nil {
		http.Error(w, fmt.Sprintf("workflow execution failed: %v", err), http.StatusInternalServerError)
		return
	}

	_ = run // result is persisted, will show in run history

	// Redirect back to the detail page so the run history refreshes.
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", fmt.Sprintf("/agents/%s/workflows/%s", id, wid))
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/agents/%s/workflows/%s", id, wid), http.StatusSeeOther)
}

// WorkflowDelete deletes a workflow.
func (h *Handlers) WorkflowDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	wid := r.PathValue("wid")
	ctx := r.Context()

	s, ok := h.kyvik.Store().(store.Store)
	if !ok || s == nil {
		http.Error(w, "store not available", http.StatusInternalServerError)
		return
	}

	if err := s.DeleteWorkflow(ctx, wid); err != nil {
		http.Error(w, fmt.Sprintf("failed to delete workflow: %v", err), http.StatusInternalServerError)
		return
	}

	if isHTMX(r) {
		w.Header().Set("HX-Redirect", fmt.Sprintf("/agents/%s", id))
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/agents/%s", id), http.StatusSeeOther)
}
