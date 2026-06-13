package cluster

import (
	"context"
	"database/sql"
	"sync"
	"sync/atomic"
)

const leaderLockID int64 = 0x4B59564B // "KYVK" in hex

type leaderElector struct {
	sourceDB *sql.DB
	lockConn *sql.Conn
	leader   atomic.Bool
	mu       sync.Mutex
}

func newLeaderElector(db *sql.DB) *leaderElector {
	return &leaderElector{sourceDB: db}
}

func (le *leaderElector) tryAcquire(ctx context.Context) (bool, error) {
	le.mu.Lock()
	defer le.mu.Unlock()

	if le.lockConn == nil {
		conn, err := le.sourceDB.Conn(ctx)
		if err != nil {
			return false, err
		}
		le.lockConn = conn
	}

	var acquired bool
	err := le.lockConn.QueryRowContext(ctx,
		"SELECT pg_try_advisory_lock($1)", leaderLockID).Scan(&acquired)
	if err != nil {
		le.lockConn.Close()
		le.lockConn = nil
		le.leader.Store(false)
		return false, err
	}

	le.leader.Store(acquired)
	return acquired, nil
}

func (le *leaderElector) release() {
	le.mu.Lock()
	defer le.mu.Unlock()

	le.leader.Store(false)
	if le.lockConn != nil {
		// Explicitly release the advisory lock before returning the connection to
		// the pool. Closing a *sql.Conn only returns it to the pool — the
		// underlying session (and its advisory locks) remain alive. We must
		// unlock explicitly so the next node can acquire leadership immediately.
		_, _ = le.lockConn.ExecContext(context.Background(),
			"SELECT pg_advisory_unlock($1)", leaderLockID)
		le.lockConn.Close()
		le.lockConn = nil
	}
}

func (le *leaderElector) isLeader() bool {
	return le.leader.Load()
}
