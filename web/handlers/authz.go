package handlers

import (
	"context"
	"fmt"

	"github.com/kkjorsvik/kyvik/internal/guide"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

func (h *Handlers) filterAgentsForUser(ctx context.Context, agents []types.AgentConfig) ([]types.AgentConfig, error) {
	visible, isAdmin, err := h.visibleAgentSet(ctx)
	if err != nil {
		return nil, err
	}
	if isAdmin {
		return agents, nil
	}
	out := make([]types.AgentConfig, 0, len(agents))
	for _, a := range agents {
		if a.IsGuide {
			out = append(out, a)
			continue
		}
		if _, ok := visible[a.ID]; ok {
			out = append(out, a)
		}
	}
	return out, nil
}

func (h *Handlers) visibleAgentSet(ctx context.Context) (map[string]struct{}, bool, error) {
	if h.auth == nil {
		return nil, true, nil
	}
	u, ok := currentDashboardUser(ctx)
	if !ok || u.IsAdmin {
		return nil, true, nil
	}
	visible, err := h.auth.ListVisibleAgentIDs(ctx, u.ID)
	if err != nil {
		return nil, false, err
	}
	return visible, false, nil
}

func (h *Handlers) visibleAgentIDs(ctx context.Context, agentIDs []string) ([]string, error) {
	visible, isAdmin, err := h.visibleAgentSet(ctx)
	if err != nil {
		return nil, err
	}
	if isAdmin {
		return agentIDs, nil
	}
	out := make([]string, 0, len(agentIDs))
	for _, id := range agentIDs {
		if _, ok := visible[id]; ok {
			out = append(out, id)
		}
	}
	return out, nil
}

func (h *Handlers) isAgentVisible(ctx context.Context, agentID string) (bool, error) {
	if guide.IsGuideAgent(agentID) {
		return true, nil
	}
	visible, isAdmin, err := h.visibleAgentSet(ctx)
	if err != nil {
		return false, err
	}
	if isAdmin {
		return true, nil
	}
	_, ok := visible[agentID]
	return ok, nil
}

func (h *Handlers) ensureAgentsVisible(ctx context.Context, agentIDs ...string) error {
	visible, isAdmin, err := h.visibleAgentSet(ctx)
	if err != nil {
		return err
	}
	if isAdmin {
		return nil
	}
	for _, id := range agentIDs {
		if guide.IsGuideAgent(id) {
			continue
		}
		if _, ok := visible[id]; !ok {
			return fmt.Errorf("agent %s is not visible", id)
		}
	}
	return nil
}
