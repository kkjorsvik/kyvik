package cluster

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/testutil"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	tdb := testutil.RequirePostgres(t)
	return tdb.DB
}

func TestLeaderElection_SingleNode(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	le := newLeaderElector(db)
	acquired, err := le.tryAcquire(ctx)
	if err != nil {
		t.Fatalf("tryAcquire: %v", err)
	}
	if !acquired {
		t.Fatal("single node should acquire leadership")
	}
	defer le.release()

	if !le.isLeader() {
		t.Error("should report as leader after acquiring")
	}
}

func TestLeaderElection_TwoNodes(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	le1 := newLeaderElector(db)
	le2 := newLeaderElector(db)

	acquired1, _ := le1.tryAcquire(ctx)
	acquired2, _ := le2.tryAcquire(ctx)

	if !acquired1 {
		t.Fatal("first node should acquire")
	}
	if acquired2 {
		t.Fatal("second node should not acquire")
	}

	// Release leader, second should be able to acquire
	le1.release()
	time.Sleep(100 * time.Millisecond)

	acquired2, _ = le2.tryAcquire(ctx)
	if !acquired2 {
		t.Fatal("second node should acquire after first releases")
	}
	le2.release()
}
