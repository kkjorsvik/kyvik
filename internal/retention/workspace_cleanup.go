package retention

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

func (p *Pruner) cleanOrphanWorkspaces(ctx context.Context) (int64, []string) {
	log := slog.With("component", "workspace_pruner")

	// Load orphan state from system_state
	orphanState := p.loadOrphanState(ctx)

	// List workspace directories
	entries, err := os.ReadDir(p.config.WorkspaceRoot)
	if err != nil {
		return 0, []string{fmt.Sprintf("read workspace root: %v", err)}
	}

	gracePeriod := time.Duration(p.config.WorkspaceGraceDays) * 24 * time.Hour
	var deleted int64
	var errors []string
	activeOrphans := make(map[string]time.Time)

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		agentID := entry.Name()

		// Check if agent exists
		var exists int
		err := p.db.QueryRowContext(ctx, "SELECT 1 FROM agents WHERE id = $1", agentID).Scan(&exists)
		if err == nil {
			// Agent exists — active workspace, skip
			continue
		}

		// Agent doesn't exist — orphan
		firstSeen, known := orphanState[agentID]
		if !known {
			// First time seeing this orphan
			firstSeen = time.Now()
			log.Info("new orphan workspace detected", "agent_id", agentID)
		}

		if time.Since(firstSeen) > gracePeriod {
			// Past grace period — delete
			wsPath := filepath.Join(p.config.WorkspaceRoot, agentID)
			if err := os.RemoveAll(wsPath); err != nil {
				errors = append(errors, fmt.Sprintf("remove workspace %s: %v", agentID, err))
			} else {
				log.Info("deleted orphan workspace", "agent_id", agentID, "age_days", int(time.Since(firstSeen).Hours()/24))
				deleted++
			}
		} else {
			// Still within grace period
			activeOrphans[agentID] = firstSeen
			remaining := gracePeriod - time.Since(firstSeen)
			log.Debug("orphan workspace within grace period", "agent_id", agentID, "remaining", remaining.Round(time.Hour))
		}
	}

	// Persist updated orphan state
	p.saveOrphanState(ctx, activeOrphans)

	return deleted, errors
}

func (p *Pruner) loadOrphanState(ctx context.Context) map[string]time.Time {
	raw, err := p.stateStore.GetSystemState(ctx, "workspace_orphans")
	if err != nil || raw == "" {
		return make(map[string]time.Time)
	}
	var state map[string]time.Time
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return make(map[string]time.Time)
	}
	return state
}

func (p *Pruner) saveOrphanState(ctx context.Context, state map[string]time.Time) {
	data, err := json.Marshal(state)
	if err != nil {
		slog.Error("marshal orphan state", "error", err)
		return
	}
	if err := p.stateStore.SetSystemState(ctx, "workspace_orphans", string(data)); err != nil {
		slog.Error("save orphan state", "error", err)
	}
}

// dirSize calculates total size of a directory recursively.
func dirSize(path string) (int64, error) {
	var size int64
	err := filepath.WalkDir(path, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			size += info.Size()
		}
		return nil
	})
	return size, err
}
