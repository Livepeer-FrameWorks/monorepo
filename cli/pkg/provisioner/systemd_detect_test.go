package provisioner

import (
	"context"
	"strings"
	"testing"
)

func TestDetectSystemdUnitUsesExactUnit(t *testing.T) {
	runner := &mockRunner{stdout: "LoadState=loaded\nActiveState=active\n"}
	state, err := detectSystemdUnit(context.Background(), runner, "frameworks-redis-foghorn-media-us-1-sentinel")
	if err != nil {
		t.Fatalf("detectSystemdUnit returned error: %v", err)
	}
	if !state.Exists || !state.Running {
		t.Fatalf("state = %+v, want exists+running", state)
	}
	if !strings.Contains(runner.lastCmd, `"frameworks-redis-foghorn-media-us-1-sentinel"`) {
		t.Fatalf("command %q did not quote exact unit name", runner.lastCmd)
	}
}

func TestDetectSystemdUnitTreatsNotFoundAsAbsent(t *testing.T) {
	runner := &mockRunner{stdout: "LoadState=not-found\nActiveState=inactive\n"}
	state, err := detectSystemdUnit(context.Background(), runner, "vmauth")
	if err != nil {
		t.Fatalf("detectSystemdUnit returned error: %v", err)
	}
	if state.Exists || state.Running {
		t.Fatalf("state = %+v, want absent", state)
	}
}
