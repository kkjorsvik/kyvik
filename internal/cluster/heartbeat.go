package cluster

import (
	"context"
	"log/slog"
	"time"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

type heartbeatStore interface {
	UpdateHeartbeat(ctx context.Context, nodeID string, capacity types.NodeCapacity) error
	GetDeadNodes(ctx context.Context, timeout time.Duration) ([]types.NodeInfo, error)
	SetNodeStatus(ctx context.Context, nodeID, status string) error
}

type heartbeat struct {
	nodeID   string
	store    heartbeatStore
	interval time.Duration
	timeout  time.Duration
	cancel   context.CancelFunc
	done     chan struct{}
	onDead   func(nodeID string)
	onTick   func()
}

func newHeartbeat(nodeID string, store heartbeatStore, interval, timeout time.Duration) *heartbeat {
	return &heartbeat{
		nodeID:   nodeID,
		store:    store,
		interval: interval,
		timeout:  timeout,
	}
}

func (h *heartbeat) start(ctx context.Context) {
	ctx, h.cancel = context.WithCancel(ctx)
	h.done = make(chan struct{})
	go h.loop(ctx)
}

func (h *heartbeat) stop() {
	if h.cancel != nil {
		h.cancel()
		<-h.done
	}
}

func (h *heartbeat) loop(ctx context.Context) {
	defer close(h.done)
	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.tick(ctx)
		}
	}
}

func (h *heartbeat) tick(ctx context.Context) {
	if h.onTick != nil {
		h.onTick()
	}
	capacity := collectCapacity()
	if err := h.store.UpdateHeartbeat(ctx, h.nodeID, capacity); err != nil {
		slog.Error("heartbeat update failed", "error", err)
		return
	}
	dead, err := h.store.GetDeadNodes(ctx, h.timeout)
	if err != nil {
		slog.Error("dead node check failed", "error", err)
		return
	}
	for _, node := range dead {
		slog.Warn("node heartbeat expired", "node_id", node.NodeID, "node_name", node.NodeName)
		if err := h.store.SetNodeStatus(ctx, node.NodeID, types.NodeStatusDisconnected); err != nil {
			slog.Error("failed to mark node disconnected", "node_id", node.NodeID, "error", err)
		}
		if h.onDead != nil {
			h.onDead(node.NodeID)
		}
	}
}

func collectCapacity() types.NodeCapacity {
	return types.NodeCapacity{}
}
