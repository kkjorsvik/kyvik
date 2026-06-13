package queue

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/kkjorsvik/kyvik/internal/timeutil"
)

const queueTimeFmt = "2006-01-02 15:04:05"

type agentQueueMetrics struct {
	EnqueueDepthBlocked int64
	ChannelFull         int64
	ReplayPushed        int64
	ReplaySkipped       int64
	ReplayInFlightSkip  int64
	ReplayNoChannelSkip int64
}

func parseQueueTime(s string) (time.Time, error) {
	return timeutil.ParseTimestampUTC(s)
}

func parseNullableQueueTime(ns sql.NullString) (*time.Time, error) {
	if !ns.Valid || ns.String == "" {
		return nil, nil
	}
	t, err := parseQueueTime(ns.String)
	if err != nil {
		return nil, fmt.Errorf("parse nullable time %q: %w", ns.String, err)
	}
	return &t, nil
}
