package database

import (
	"testing"
	"time"
)

func TestClickHouseProbeContextIsBounded(t *testing.T) {
	ctx, cancel := clickHouseProbeContext(ClickHouseConfig{ProbeTimeout: 20 * time.Millisecond})
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > 100*time.Millisecond {
		t.Fatalf("probe context deadline = %v, want a near-term bound", deadline)
	}

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("probe context did not expire")
	}
}

func TestDefaultClickHouseConfigBoundsProbes(t *testing.T) {
	if got := DefaultClickHouseConfig().ProbeTimeout; got != defaultClickHouseProbeTimeout {
		t.Fatalf("ProbeTimeout = %s, want %s", got, defaultClickHouseProbeTimeout)
	}
}
