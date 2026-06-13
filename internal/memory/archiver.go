package memory

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// Archiver periodically archives stale memories that haven't been accessed
// within the configured decay period.
type Archiver struct {
	store      MemoryStore
	agentStore AgentLister
	decayDays  int
	runTime    string // "HH:MM"
	cancel     context.CancelFunc
	done       chan struct{}
}

// NewArchiver creates a new background archiver.
func NewArchiver(store MemoryStore, agentStore AgentLister, decayDays int, runTime string) *Archiver {
	return &Archiver{
		store:      store,
		agentStore: agentStore,
		decayDays:  decayDays,
		runTime:    runTime,
	}
}

// Start launches the background archival goroutine.
func (a *Archiver) Start(ctx context.Context) {
	ctx, a.cancel = context.WithCancel(ctx)
	a.done = make(chan struct{})

	go func() {
		defer close(a.done)
		log := slog.With("component", "memory-archiver")
		log.Info("background archiver started",
			"decay_days", a.decayDays,
			"run_time", a.runTime,
		)

		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()

		var lastRunDate string

		for {
			select {
			case <-ctx.Done():
				log.Info("background archiver stopped")
				return
			case now := <-ticker.C:
				currentTime := fmt.Sprintf("%02d:%02d", now.Hour(), now.Minute())
				currentDate := now.Format("2006-01-02")

				if currentTime == a.runTime && currentDate != lastRunDate {
					a.RunOnce(ctx)
					lastRunDate = currentDate
				}
			}
		}
	}()
}

// Stop cancels the background goroutine and waits for it to exit.
func (a *Archiver) Stop() {
	if a.cancel != nil {
		a.cancel()
	}
	if a.done != nil {
		<-a.done
	}
}

// RunOnce archives stale memories for all agents. Exposed for testing.
func (a *Archiver) RunOnce(ctx context.Context) {
	log := slog.With("component", "memory-archiver")

	agents, err := a.agentStore.ListAgents(ctx)
	if err != nil {
		log.Warn("failed to list agents for archival", "error", err)
		return
	}

	staleAfter := time.Duration(a.decayDays) * 24 * time.Hour

	for _, agent := range agents {
		count, err := a.store.ArchiveStale(ctx, agent.ID, staleAfter)
		if err != nil {
			log.Warn("archival failed", "agent", agent.ID, "error", err)
			continue
		}
		if count > 0 {
			log.Info("archival complete", "agent", agent.ID, "archived", count)
		}
	}
}
