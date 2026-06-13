package cluster

import (
	"fmt"
	"testing"
	"time"
)

func TestNoopManager_ZeroOverhead(t *testing.T) {
	m := NewNoopManager()

	start := time.Now()
	for i := 0; i < 1000; i++ {
		m.IsLocalAgent("agent-" + fmt.Sprint(i))
		m.GetAssignment("agent-" + fmt.Sprint(i))
		m.RequestAssignment("agent-" + fmt.Sprint(i))
	}
	elapsed := time.Since(start)
	if elapsed > 10*time.Millisecond {
		t.Errorf("noop operations took %v, expected < 10ms", elapsed)
	}
}
