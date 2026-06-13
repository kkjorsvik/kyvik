package audit

import (
	"context"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// AuditStore is the narrow persistence interface that StoreLogger needs.
// It mirrors the audit methods from store.Store, breaking the circular
// import (internal/store imports internal/audit for audit.Filter, so
// internal/audit cannot import internal/store).
// The store implementations satisfy this interface.
type AuditStore interface {
	InsertAuditEntry(ctx context.Context, entry types.AuditEntry) error
	InsertAuditEntries(ctx context.Context, entries []types.AuditEntry) error
	ListAuditEntries(ctx context.Context, filter Filter) ([]types.AuditEntry, error)
}

// Compile-time check that StoreLogger implements Logger.
var _ Logger = (*StoreLogger)(nil)

// subscriber represents a connected real-time audit listener.
type subscriber struct {
	ch     chan types.AuditEntry
	filter SubscriptionFilter
	cancel context.CancelFunc
}

// StoreLogger implements Logger by delegating persistence to an AuditStore.
// It batches writes in a background goroutine for throughput under load.
type StoreLogger struct {
	store        AuditStore
	pollInterval time.Duration
	batchWindow  time.Duration
	entryCh      chan types.AuditEntry
	stopCh       chan struct{}
	done         chan struct{}
	closeOnce    sync.Once

	subMu sync.RWMutex
	subs  map[*subscriber]struct{}
}

// NewStoreLogger creates a StoreLogger with the given batch window (in ms).
// If batchWindowMS <= 0, defaults to 100ms. Starts a background flush goroutine.
func NewStoreLogger(store AuditStore, batchWindowMS int) *StoreLogger {
	if batchWindowMS <= 0 {
		batchWindowMS = 100
	}
	l := &StoreLogger{
		store:        store,
		pollInterval: 2 * time.Second,
		batchWindow:  time.Duration(batchWindowMS) * time.Millisecond,
		entryCh:      make(chan types.AuditEntry, 1000),
		stopCh:       make(chan struct{}),
		done:         make(chan struct{}),
		subs:         make(map[*subscriber]struct{}),
	}
	go l.flushLoop()
	return l
}

// NewStoreLoggerWithPollInterval creates a StoreLogger with a custom poll
// interval (for Stream) and batch window, useful for tests.
func NewStoreLoggerWithPollInterval(store AuditStore, pollInterval time.Duration, batchWindowMS int) *StoreLogger {
	if batchWindowMS <= 0 {
		batchWindowMS = 100
	}
	l := &StoreLogger{
		store:        store,
		pollInterval: pollInterval,
		batchWindow:  time.Duration(batchWindowMS) * time.Millisecond,
		entryCh:      make(chan types.AuditEntry, 1000),
		stopCh:       make(chan struct{}),
		done:         make(chan struct{}),
		subs:         make(map[*subscriber]struct{}),
	}
	go l.flushLoop()
	return l
}

// Log records an audit entry. If entry.ID is empty a UUID is generated.
// If entry.Timestamp is zero it is set to time.Now().UTC().
// The entry is queued for batch writing. If the buffer is full, it falls
// back to a synchronous write to avoid dropping entries.
func (l *StoreLogger) Log(ctx context.Context, entry types.AuditEntry) error {
	if entry.ID == "" {
		entry.ID = uuid.New().String()
	}
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}

	// Broadcast to subscribers (non-blocking).
	l.broadcast(entry)

	select {
	case l.entryCh <- entry:
		return nil // queued for batch write
	default:
		// channel full — write synchronously to avoid dropping entries
		return l.store.InsertAuditEntry(ctx, entry)
	}
}

// Query retrieves audit entries matching the given filter.
func (l *StoreLogger) Query(ctx context.Context, filter Filter) ([]types.AuditEntry, error) {
	return l.store.ListAuditEntries(ctx, filter)
}

// Stream returns a channel of real-time audit events produced by polling the
// store. If agentID is non-empty, only entries for that agent are streamed.
// The channel is closed when ctx is cancelled.
func (l *StoreLogger) Stream(ctx context.Context, agentID string) (<-chan types.AuditEntry, error) {
	// Seed lastSeenID from the most recent entry so only new entries stream.
	var lastSeenID int64
	filter := Filter{AgentID: agentID, Limit: 1}
	existing, err := l.store.ListAuditEntries(ctx, filter)
	if err != nil {
		return nil, err
	}
	if len(existing) > 0 {
		lastSeenID, _ = strconv.ParseInt(existing[0].ID, 10, 64)
	}

	ch := make(chan types.AuditEntry, 16)

	go func() {
		defer close(ch)
		ticker := time.NewTicker(l.pollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				entries, err := l.store.ListAuditEntries(ctx, Filter{
					AgentID: agentID,
					Limit:   50,
				})
				if err != nil {
					continue
				}

				// Collect entries with numeric ID > lastSeenID, then sort
				// ascending by ID for chronological delivery.
				var newEntries []types.AuditEntry
				for _, e := range entries {
					id, _ := strconv.ParseInt(e.ID, 10, 64)
					if id > lastSeenID {
						newEntries = append(newEntries, e)
					}
				}

				slices.SortFunc(newEntries, func(a, b types.AuditEntry) int {
					ai, _ := strconv.ParseInt(a.ID, 10, 64)
					bi, _ := strconv.ParseInt(b.ID, 10, 64)
					if ai < bi {
						return -1
					}
					if ai > bi {
						return 1
					}
					return 0
				})

				for _, e := range newEntries {
					select {
					case ch <- e:
					case <-ctx.Done():
						return
					}
				}

				if len(newEntries) > 0 {
					newest := newEntries[len(newEntries)-1]
					if id, err := strconv.ParseInt(newest.ID, 10, 64); err == nil {
						lastSeenID = id
					}
				}
			}
		}
	}()

	return ch, nil
}

// Subscribe returns a channel that receives audit entries matching the filter.
// The channel is closed when ctx is cancelled. Entries are dropped non-blocking
// if the subscriber channel is full (buffer size: 64).
func (l *StoreLogger) Subscribe(ctx context.Context, filter SubscriptionFilter) (<-chan types.AuditEntry, error) {
	subCtx, cancel := context.WithCancel(ctx)
	sub := &subscriber{
		ch:     make(chan types.AuditEntry, 64),
		filter: filter,
		cancel: cancel,
	}

	l.subMu.Lock()
	l.subs[sub] = struct{}{}
	l.subMu.Unlock()

	// Auto-cleanup when context is cancelled.
	go func() {
		<-subCtx.Done()
		l.subMu.Lock()
		delete(l.subs, sub)
		l.subMu.Unlock()
		close(sub.ch)
	}()

	return sub.ch, nil
}

// broadcast sends an entry to all matching subscribers (non-blocking).
func (l *StoreLogger) broadcast(entry types.AuditEntry) {
	l.subMu.RLock()
	defer l.subMu.RUnlock()

	for sub := range l.subs {
		if !matchesFilter(entry, sub.filter) {
			continue
		}
		// Non-blocking send — drop if subscriber channel is full.
		select {
		case sub.ch <- entry:
		default:
		}
	}
}

// matchesFilter checks whether an audit entry matches a subscription filter.
func matchesFilter(entry types.AuditEntry, filter SubscriptionFilter) bool {
	if filter.AgentID != "" && entry.AgentID != filter.AgentID {
		return false
	}
	if len(filter.Actions) > 0 {
		found := false
		for _, a := range filter.Actions {
			if a == entry.Action {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if len(filter.Levels) > 0 {
		found := false
		for _, lvl := range filter.Levels {
			if lvl == entry.RiskLevel {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if len(filter.Decisions) > 0 {
		found := false
		for _, d := range filter.Decisions {
			if d == entry.Decision {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// Flush drains all buffered entries to the store synchronously.
func (l *StoreLogger) Flush() {
	l.drain()
}

// Close stops the background flush goroutine, cancels all active subscribers,
// and waits for it to drain any remaining buffered entries. Safe to call multiple times.
func (l *StoreLogger) Close() error {
	l.closeOnce.Do(func() {
		// Cancel all subscribers.
		l.subMu.Lock()
		for sub := range l.subs {
			sub.cancel()
		}
		l.subMu.Unlock()

		close(l.stopCh)
		<-l.done
	})
	return nil
}

// flushLoop runs in a background goroutine, periodically draining the
// entry channel and batch-writing to the store.
func (l *StoreLogger) flushLoop() {
	defer close(l.done)
	ticker := time.NewTicker(l.batchWindow)
	defer ticker.Stop()

	for {
		select {
		case <-l.stopCh:
			l.drain()
			return
		case <-ticker.C:
			l.drain()
		}
	}
}

// drain reads all available entries from the channel and batch-writes them.
func (l *StoreLogger) drain() {
	var batch []types.AuditEntry
	for {
		select {
		case entry := <-l.entryCh:
			batch = append(batch, entry)
		default:
			// channel empty
			if len(batch) > 0 {
				// Use background context — must succeed even during shutdown.
				_ = l.store.InsertAuditEntries(context.Background(), batch)
			}
			return
		}
	}
}
