package teamtool

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/kkjorsvik/kyvik/internal/ktp"
	"github.com/kkjorsvik/kyvik/internal/teams"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// TeamManager is the subset of team manager behavior required by team tools.
type TeamManager interface {
	GetTeamForAgent(ctx context.Context, agentID string) (*types.Team, error)
	TeamStatus(ctx context.Context, teamID string) ([]teams.TeamMemberStatus, error)
}

// MessageBus is the subset of bus behavior required by team tools.
type MessageBus interface {
	Send(ctx context.Context, msg types.InternalMessage) error
}

// AgentLookup resolves an agent config by ID.
type AgentLookup func(ctx context.Context, id string) (*types.AgentConfig, error)

// DelegateTool delegates tasks from a team leader to team members.
type DelegateTool struct {
	manager TeamManager
	bus     MessageBus
	lookup  AgentLookup
}

// NewDelegateTool creates the team.delegate tool.
func NewDelegateTool(manager TeamManager, bus MessageBus, lookup AgentLookup) *DelegateTool {
	return &DelegateTool{manager: manager, bus: bus, lookup: lookup}
}

func (t *DelegateTool) Declaration() ktp.ToolDeclaration {
	return ktp.ToolDeclaration{
		Name:         "team.delegate",
		Version:      "1.0.0",
		Description:  "Delegate a task to one or more team members",
		MinTier:      ktp.TierWriter,
		DefaultTiers: []string{ktp.TierWriter, ktp.TierOperator, ktp.TierAdmin},
		Actions: []ktp.ActionSpec{
			{
				Name:        "delegate",
				Description: "Delegate a task to one or more members",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"to":       {Type: "string", Description: "Target member ID/name, or list of IDs/names"},
						"task":     {Type: "string", Description: "Task description/instruction"},
						"context":  {Type: "string", Description: "Optional additional context"},
						"parallel": {Type: "boolean", Description: "Send to all targets in parallel", Default: false},
					},
					Required: []string{"to", "task"},
				},
				Returns: ktp.JSONSchema{Type: "object"},
			},
		},
	}
}

func (t *DelegateTool) Inline() bool { return true }

func (t *DelegateTool) Execute(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	start := time.Now()
	if req.Action != "delegate" {
		return errorResponse(req.ID, fmt.Sprintf("unknown action: %s", req.Action), start), nil
	}
	if t.manager == nil || t.bus == nil || t.lookup == nil {
		return errorResponse(req.ID, "team delegate dependencies not configured", start), nil
	}

	team, err := t.manager.GetTeamForAgent(ctx, req.AgentID)
	if err != nil {
		return errorResponse(req.ID, fmt.Sprintf("failed to resolve team: %s", err), start), nil
	}
	if team == nil {
		return errorResponse(req.ID, "caller is not in a team", start), nil
	}
	if !team.Active {
		return errorResponse(req.ID, "team communication is paused", start), nil
	}
	if team.LeaderID != req.AgentID {
		return errorResponse(req.ID, "only the team leader can delegate tasks", start), nil
	}

	rawTargets, err := parseTargets(req.Parameters["to"])
	if err != nil {
		return errorResponse(req.ID, err.Error(), start), nil
	}
	task, err := stringParam(req.Parameters, "task")
	if err != nil {
		return errorResponse(req.ID, err.Error(), start), nil
	}
	extraContext := stringParamDefault(req.Parameters, "context", "")
	parallel := boolParamDefault(req.Parameters, "parallel", false)

	resolvedTargets, err := resolveTargets(ctx, t.lookup, *team, rawTargets)
	if err != nil {
		return errorResponse(req.ID, err.Error(), start), nil
	}

	results := make([]map[string]any, 0, len(resolvedTargets))
	for _, targetID := range resolvedTargets {
		taskID := ulid.Make().String()
		content := "Delegated task:\n" + task
		if strings.TrimSpace(extraContext) != "" {
			content += "\n\nContext:\n" + strings.TrimSpace(extraContext)
		}
		msg := types.InternalMessage{
			From:     req.AgentID,
			To:       targetID,
			Content:  content,
			Type:     types.MessageTypeTask,
			Priority: types.MessagePriorityNormal,
			Metadata: map[string]string{
				"task_id":      taskID,
				"delegated_by": req.AgentID,
				"parallel":     strconv.FormatBool(parallel),
			},
		}
		if err := t.bus.Send(ctx, msg); err != nil {
			return errorResponse(req.ID, fmt.Sprintf("failed to delegate to %s: %s", targetID, err), start), nil
		}
		results = append(results, map[string]any{"task_id": taskID, "to": targetID})
	}

	resp := ktp.NewToolResponse(req.ID, true, map[string]any{
		"delegated": len(results),
		"tasks":     results,
	}, "", elapsedMs(start))
	return &resp, nil
}

// BroadcastTool broadcasts a message to team members.
type BroadcastTool struct {
	manager TeamManager
	bus     MessageBus
}

// NewBroadcastTool creates the team.broadcast tool.
func NewBroadcastTool(manager TeamManager, bus MessageBus) *BroadcastTool {
	return &BroadcastTool{manager: manager, bus: bus}
}

func (t *BroadcastTool) Declaration() ktp.ToolDeclaration {
	return ktp.ToolDeclaration{
		Name:         "team.broadcast",
		Version:      "1.0.0",
		Description:  "Broadcast a message to all team members",
		MinTier:      ktp.TierWriter,
		DefaultTiers: []string{ktp.TierWriter, ktp.TierOperator, ktp.TierAdmin},
		Actions: []ktp.ActionSpec{
			{
				Name:        "broadcast",
				Description: "Broadcast a message to all team members",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"message":      {Type: "string", Description: "Message to broadcast"},
						"exclude_self": {Type: "boolean", Description: "Exclude sender from recipients", Default: true},
					},
					Required: []string{"message"},
				},
				Returns: ktp.JSONSchema{Type: "object"},
			},
		},
	}
}

func (t *BroadcastTool) Inline() bool { return true }

func (t *BroadcastTool) Execute(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	start := time.Now()
	if req.Action != "broadcast" {
		return errorResponse(req.ID, fmt.Sprintf("unknown action: %s", req.Action), start), nil
	}
	if t.manager == nil || t.bus == nil {
		return errorResponse(req.ID, "team broadcast dependencies not configured", start), nil
	}

	team, err := t.manager.GetTeamForAgent(ctx, req.AgentID)
	if err != nil {
		return errorResponse(req.ID, fmt.Sprintf("failed to resolve team: %s", err), start), nil
	}
	if team == nil {
		return errorResponse(req.ID, "caller is not in a team", start), nil
	}
	if !team.Active {
		return errorResponse(req.ID, "team communication is paused", start), nil
	}

	message, err := stringParam(req.Parameters, "message")
	if err != nil {
		return errorResponse(req.ID, err.Error(), start), nil
	}
	excludeSelf := boolParamDefault(req.Parameters, "exclude_self", true)

	recipients := 0
	for _, memberID := range dedupeMembers(*team) {
		if excludeSelf && memberID == req.AgentID {
			continue
		}
		msg := types.InternalMessage{
			From:     req.AgentID,
			To:       memberID,
			Content:  message,
			Type:     types.MessageTypeMessage,
			Priority: types.MessagePriorityNormal,
		}
		if err := t.bus.Send(ctx, msg); err != nil {
			return errorResponse(req.ID, fmt.Sprintf("failed to broadcast to %s: %s", memberID, err), start), nil
		}
		recipients++
	}

	resp := ktp.NewToolResponse(req.ID, true, map[string]any{
		"notified": recipients,
	}, "", elapsedMs(start))
	return &resp, nil
}

// StatusTool reports team member operational status.
type StatusTool struct {
	manager TeamManager
	lookup  AgentLookup
}

// NewStatusTool creates the team.status tool.
func NewStatusTool(manager TeamManager, lookup AgentLookup) *StatusTool {
	return &StatusTool{manager: manager, lookup: lookup}
}

func (t *StatusTool) Declaration() ktp.ToolDeclaration {
	return ktp.ToolDeclaration{
		Name:         "team.status",
		Version:      "1.0.0",
		Description:  "Check operational status of team members",
		MinTier:      ktp.TierWriter,
		DefaultTiers: []string{ktp.TierWriter, ktp.TierOperator, ktp.TierAdmin},
		Actions: []ktp.ActionSpec{
			{
				Name:        "status",
				Description: "Get status for all members or a specific member",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"member": {Type: "string", Description: "Optional member ID or name"},
					},
				},
				Returns: ktp.JSONSchema{Type: "object"},
			},
		},
	}
}

func (t *StatusTool) Inline() bool { return true }

func (t *StatusTool) Execute(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	start := time.Now()
	if req.Action != "status" {
		return errorResponse(req.ID, fmt.Sprintf("unknown action: %s", req.Action), start), nil
	}
	if t.manager == nil || t.lookup == nil {
		return errorResponse(req.ID, "team status dependencies not configured", start), nil
	}

	team, err := t.manager.GetTeamForAgent(ctx, req.AgentID)
	if err != nil {
		return errorResponse(req.ID, fmt.Sprintf("failed to resolve team: %s", err), start), nil
	}
	if team == nil {
		return errorResponse(req.ID, "caller is not in a team", start), nil
	}
	if !team.Active {
		return errorResponse(req.ID, "team communication is paused", start), nil
	}

	statuses, err := t.manager.TeamStatus(ctx, team.ID)
	if err != nil {
		return errorResponse(req.ID, fmt.Sprintf("failed to fetch team status: %s", err), start), nil
	}

	if member := strings.TrimSpace(stringParamDefault(req.Parameters, "member", "")); member != "" {
		memberID, err := resolveMember(ctx, t.lookup, *team, member)
		if err != nil {
			return errorResponse(req.ID, err.Error(), start), nil
		}
		filtered := make([]teams.TeamMemberStatus, 0, 1)
		for _, st := range statuses {
			if st.AgentID == memberID {
				filtered = append(filtered, st)
				break
			}
		}
		statuses = filtered
	}

	result := make([]map[string]any, 0, len(statuses))
	for _, st := range statuses {
		result = append(result, map[string]any{
			"agent_id":    st.AgentID,
			"agent_name":  st.AgentName,
			"is_leader":   st.IsLeader,
			"status":      st.Status,
			"queue_depth": st.QueueDepth,
		})
	}

	resp := ktp.NewToolResponse(req.ID, true, map[string]any{"members": result}, "", elapsedMs(start))
	return &resp, nil
}

// RecallTool signals a delegated task recall to a member.
type RecallTool struct {
	manager TeamManager
	bus     MessageBus
	lookup  AgentLookup
}

// NewRecallTool creates the team.recall tool.
func NewRecallTool(manager TeamManager, bus MessageBus, lookup AgentLookup) *RecallTool {
	return &RecallTool{manager: manager, bus: bus, lookup: lookup}
}

func (t *RecallTool) Declaration() ktp.ToolDeclaration {
	return ktp.ToolDeclaration{
		Name:         "team.recall",
		Version:      "1.0.0",
		Description:  "Recall a delegated task from a team member",
		MinTier:      ktp.TierWriter,
		DefaultTiers: []string{ktp.TierWriter, ktp.TierOperator, ktp.TierAdmin},
		Actions: []ktp.ActionSpec{
			{
				Name:        "recall",
				Description: "Recall a member's current task or a specific task ID",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"member":  {Type: "string", Description: "Target member ID or name"},
						"task_id": {Type: "string", Description: "Optional task ID to recall"},
					},
					Required: []string{"member"},
				},
				Returns: ktp.JSONSchema{Type: "object"},
			},
		},
	}
}

func (t *RecallTool) Inline() bool { return true }

func (t *RecallTool) Execute(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	start := time.Now()
	if req.Action != "recall" {
		return errorResponse(req.ID, fmt.Sprintf("unknown action: %s", req.Action), start), nil
	}
	if t.manager == nil || t.bus == nil || t.lookup == nil {
		return errorResponse(req.ID, "team recall dependencies not configured", start), nil
	}

	team, err := t.manager.GetTeamForAgent(ctx, req.AgentID)
	if err != nil {
		return errorResponse(req.ID, fmt.Sprintf("failed to resolve team: %s", err), start), nil
	}
	if team == nil {
		return errorResponse(req.ID, "caller is not in a team", start), nil
	}
	if team.LeaderID != req.AgentID {
		return errorResponse(req.ID, "only the team leader can recall delegated tasks", start), nil
	}

	memberRaw, err := stringParam(req.Parameters, "member")
	if err != nil {
		return errorResponse(req.ID, err.Error(), start), nil
	}
	targetID, err := resolveMember(ctx, t.lookup, *team, memberRaw)
	if err != nil {
		return errorResponse(req.ID, err.Error(), start), nil
	}
	taskID := strings.TrimSpace(stringParamDefault(req.Parameters, "task_id", ""))

	content := "Your current task has been recalled by the team leader. Stop work and acknowledge."
	if taskID != "" {
		content += " (task_id: " + taskID + ")"
	}

	msg := types.InternalMessage{
		From:     req.AgentID,
		To:       targetID,
		Content:  content,
		Type:     types.MessageTypeStatus,
		Priority: types.MessagePriorityUrgent,
		Metadata: map[string]string{
			"recall":      "true",
			"recalled_by": req.AgentID,
		},
	}
	if taskID != "" {
		msg.Metadata["task_id"] = taskID
	}
	if err := t.bus.Send(ctx, msg); err != nil {
		return errorResponse(req.ID, fmt.Sprintf("failed to send recall to %s: %s", targetID, err), start), nil
	}

	resp := ktp.NewToolResponse(req.ID, true, map[string]any{
		"recalled": true,
		"member":   targetID,
		"task_id":  taskID,
	}, "", elapsedMs(start))
	return &resp, nil
}

func resolveTargets(ctx context.Context, lookup AgentLookup, team types.Team, rawTargets []string) ([]string, error) {
	seen := make(map[string]struct{}, len(rawTargets))
	resolved := make([]string, 0, len(rawTargets))
	for _, raw := range rawTargets {
		memberID, err := resolveMember(ctx, lookup, team, raw)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[memberID]; ok {
			continue
		}
		seen[memberID] = struct{}{}
		resolved = append(resolved, memberID)
	}
	if len(resolved) == 0 {
		return nil, fmt.Errorf("at least one valid target is required")
	}
	return resolved, nil
}

func resolveMember(ctx context.Context, lookup AgentLookup, team types.Team, raw string) (string, error) {
	candidate := strings.TrimSpace(raw)
	if candidate == "" {
		return "", fmt.Errorf("member target cannot be empty")
	}
	for _, memberID := range dedupeMembers(team) {
		if candidate == memberID {
			return memberID, nil
		}
	}

	var matches []string
	for _, memberID := range dedupeMembers(team) {
		agent, err := lookup(ctx, memberID)
		if err != nil {
			return "", fmt.Errorf("failed to resolve member %s: %w", memberID, err)
		}
		if strings.EqualFold(agent.Name, candidate) {
			matches = append(matches, memberID)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("member name %q is ambiguous", candidate)
	}
	return "", fmt.Errorf("target %q is not a member of team %s", candidate, team.ID)
}

func dedupeMembers(team types.Team) []string {
	seen := make(map[string]struct{}, len(team.MemberIDs)+1)
	members := make([]string, 0, len(team.MemberIDs)+1)
	for _, id := range append(append([]string{}, team.MemberIDs...), team.LeaderID) {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		members = append(members, id)
	}
	return members
}

func parseTargets(raw any) ([]string, error) {
	switch v := raw.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return nil, fmt.Errorf("parameter to cannot be empty")
		}
		return []string{strings.TrimSpace(v)}, nil
	case []string:
		return v, nil
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("parameter to list must contain only strings")
			}
			out = append(out, strings.TrimSpace(s))
		}
		return out, nil
	default:
		return nil, fmt.Errorf("parameter to must be a string or string array")
	}
}

func stringParam(params map[string]any, key string) (string, error) {
	raw, ok := params[key]
	if !ok {
		return "", fmt.Errorf("missing required parameter: %s", key)
	}
	s, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("parameter %s must be a string", key)
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("parameter %s cannot be empty", key)
	}
	return s, nil
}

func stringParamDefault(params map[string]any, key, def string) string {
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

func boolParamDefault(params map[string]any, key string, def bool) bool {
	raw, ok := params[key]
	if !ok {
		return def
	}
	b, ok := raw.(bool)
	if !ok {
		return def
	}
	return b
}

func elapsedMs(start time.Time) int64 {
	return time.Since(start).Milliseconds()
}

func errorResponse(reqID, msg string, start time.Time) *ktp.ToolResponse {
	resp := ktp.NewToolResponse(reqID, false, nil, msg, elapsedMs(start))
	return &resp
}
