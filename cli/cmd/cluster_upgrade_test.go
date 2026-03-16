package cmd

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/cli/pkg/orchestrator"
)

func TestWaitForHealthRetriesUntilSuccess(t *testing.T) {
	var attempts int32
	check := func() error {
		count := atomic.AddInt32(&attempts, 1)
		if count < 3 {
			return errors.New("not ready")
		}
		return nil
	}

	if err := waitForHealth(context.Background(), check, 5*time.Millisecond, 50*time.Millisecond); err != nil {
		t.Fatalf("expected health check to succeed, got error: %v", err)
	}
}

func TestWaitForHealthTimeout(t *testing.T) {
	errSentinel := errors.New("still failing")
	check := func() error {
		return errSentinel
	}

	err := waitForHealth(context.Background(), check, 5*time.Millisecond, 30*time.Millisecond)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !errors.Is(err, errSentinel) {
		t.Fatalf("expected sentinel error, got: %v", err)
	}
}

func TestCollectUpgradeableServices_DeduplicatesMultiHost(t *testing.T) {
	plan := &orchestrator.ExecutionPlan{
		Batches: [][]*orchestrator.Task{
			{
				{Name: "postgres", Phase: orchestrator.PhaseInfrastructure},
				{Name: "kafka", Phase: orchestrator.PhaseInfrastructure},
			},
			{
				{Name: "privateer@host-a", Phase: orchestrator.PhaseApplications},
				{Name: "privateer@host-b", Phase: orchestrator.PhaseApplications},
			},
			{
				{Name: "bridge@host-a", Phase: orchestrator.PhaseApplications},
				{Name: "bridge@host-b", Phase: orchestrator.PhaseApplications},
				{Name: "commodore", Phase: orchestrator.PhaseApplications},
			},
		},
	}

	got := collectUpgradeableServices(plan)
	want := []string{"privateer", "bridge", "commodore"}

	if len(got) != len(want) {
		t.Fatalf("expected %d services, got %d: %v", len(want), len(got), got)
	}
	for i, s := range want {
		if got[i] != s {
			t.Errorf("service[%d]: expected %q, got %q", i, s, got[i])
		}
	}
}

func TestCollectUpgradeableServices_SingleHost(t *testing.T) {
	plan := &orchestrator.ExecutionPlan{
		Batches: [][]*orchestrator.Task{
			{
				{Name: "privateer", Phase: orchestrator.PhaseApplications},
				{Name: "bridge", Phase: orchestrator.PhaseApplications},
			},
		},
	}

	got := collectUpgradeableServices(plan)
	if len(got) != 2 {
		t.Fatalf("expected 2 services, got %d: %v", len(got), got)
	}
	if got[0] != "privateer" || got[1] != "bridge" {
		t.Fatalf("expected [privateer bridge], got %v", got)
	}
}
