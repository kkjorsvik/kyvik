package cluster

import (
	"context"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/testutil"
)

func TestNotify_PubSub(t *testing.T) {
	tdb := testutil.RequirePostgres(t)
	db := tdb.DB
	dsn := testutil.TestDSN()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	n := newNotifier(dsn, db)
	if err := n.start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer n.stop()

	received := make(chan ClusterEvent, 1)
	n.subscribe(ChannelCluster, func(ev ClusterEvent) {
		received <- ev
	})

	time.Sleep(200 * time.Millisecond) // let subscription settle

	ev := ClusterEvent{Type: EventNodeJoined, NodeID: "test-node"}
	if err := n.publish(ctx, ChannelCluster, ev); err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case got := <-received:
		if got.Type != EventNodeJoined || got.NodeID != "test-node" {
			t.Errorf("unexpected event: %+v", got)
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for event")
	}
}
