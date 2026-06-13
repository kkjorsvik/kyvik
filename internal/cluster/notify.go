package cluster

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

type notifier struct {
	dsn         string
	publishDB   *sql.DB
	conn        *pgx.Conn
	subscribers map[string][]func(ClusterEvent)
	mu          sync.RWMutex
	cancel      context.CancelFunc
	done        chan struct{}
}

func newNotifier(dsn string, publishDB *sql.DB) *notifier {
	return &notifier{
		dsn:         dsn,
		publishDB:   publishDB,
		subscribers: make(map[string][]func(ClusterEvent)),
	}
}

func (n *notifier) start(ctx context.Context) error {
	conn, err := pgx.Connect(ctx, n.dsn)
	if err != nil {
		return fmt.Errorf("notifier connect: %w", err)
	}
	n.conn = conn
	ctx, n.cancel = context.WithCancel(ctx)
	n.done = make(chan struct{})
	go n.loop(ctx)
	return nil
}

func (n *notifier) stop() {
	if n.cancel != nil {
		n.cancel()
		<-n.done
	}
	if n.conn != nil {
		n.conn.Close(context.Background())
	}
}

func (n *notifier) subscribe(channel string, fn func(ClusterEvent)) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.subscribers[channel]) == 0 && n.conn != nil {
		if _, err := n.conn.Exec(context.Background(), "LISTEN "+channel); err != nil {
			slog.Error("LISTEN failed", "channel", channel, "error", err)
		}
	}
	n.subscribers[channel] = append(n.subscribers[channel], fn)
}

func (n *notifier) publish(ctx context.Context, channel string, ev ClusterEvent) error {
	payload, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = n.publishDB.ExecContext(ctx, "SELECT pg_notify($1, $2)", channel, string(payload))
	return err
}

func (n *notifier) loop(ctx context.Context) {
	defer close(n.done)
	for {
		notification, err := n.conn.WaitForNotification(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Error("WaitForNotification error", "error", err)
			time.Sleep(time.Second)
			continue
		}
		n.dispatch(notification.Channel, notification.Payload)
	}
}

func (n *notifier) dispatch(channel, payload string) {
	var ev ClusterEvent
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		slog.Error("unmarshal NOTIFY payload", "channel", channel, "error", err)
		return
	}
	n.mu.RLock()
	subs := n.subscribers[channel]
	n.mu.RUnlock()
	for _, fn := range subs {
		fn(ev)
	}
}
