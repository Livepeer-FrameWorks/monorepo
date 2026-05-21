package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/cli/pkg/inventory"
)

// recordingActions captures the sequence of host names the executor
// asked for, in the order it asked. Synchronized because hosts inside a
// wave run in parallel.
type recordingActions struct {
	mu    sync.Mutex
	seen  []string
	fails map[string]error // host → error to return
}

func (r *recordingActions) record(host string) error {
	r.mu.Lock()
	r.seen = append(r.seen, host)
	err := r.fails[host]
	r.mu.Unlock()
	return err
}

func (r *recordingActions) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.seen))
	copy(out, r.seen)
	return out
}

// mkPlan builds a RolloutPlan with the given wave→hosts structure for a
// single service. Each Task has Host and Name set to the host name.
func mkPlan(service string, waves [][]string) RolloutPlan {
	plan := RolloutPlan{Service: service}
	for _, hosts := range waves {
		w := Wave{}
		for _, h := range hosts {
			w.Tasks = append(w.Tasks, &Task{Name: h, Host: h, ServiceID: service})
		}
		plan.Waves = append(plan.Waves, w)
	}
	return plan
}

// mkExecutorInputs builds the inputs map for a single-service plan: every host
// gets ActionRestart by default.
func mkExecutorInputs(service string, hosts ...string) map[string]ExecutorInput {
	out := map[string]ExecutorInput{}
	for _, h := range hosts {
		out[service+"@"+h] = ExecutorInput{
			Service: service,
			Host:    inventory.Host{Name: h, ExternalIP: "127.0.0.1", User: "deploy"},
			Action:  ActionRestart,
		}
	}
	return out
}

// stubGate is a Gate that returns a configurable error. Used via
// ExecuteOptions.GateOverride to make readiness deterministic in tests.
type stubGate struct {
	err error
}

func (g stubGate) Wait(_ context.Context, _ inventory.Host, _ ProbeFunc) error {
	return g.err
}
func (g stubGate) Describe() string { return "stub" }

// alwaysReady returns a GateOverride that makes every host instantly ready.
func alwaysReady() func(string) Gate {
	return func(string) Gate { return stubGate{nil} }
}

// noopProbe satisfies the ProbeFunc parameter when the stub gate ignores it.
func noopProbe(_ context.Context, _ inventory.Host, _ string) (ProbeResult, error) {
	return ProbeResult{}, nil
}

func TestExecute_SingleWaveHappyPath(t *testing.T) {
	plan := mkPlan("foghorn", [][]string{{"a"}})
	inputs := mkExecutorInputs("foghorn", "a")
	act := &recordingActions{}
	action := func(_ context.Context, in ExecutorInput) error {
		return act.record(in.Host.Name)
	}

	res, err := ExecuteRolloutPlan(context.Background(), plan, inputs, action, noopProbe,
		ExecuteOptions{GateOverride: alwaysReady()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Halted {
		t.Error("expected non-halted result")
	}
	if !slices.Equal(act.snapshot(), []string{"a"}) {
		t.Errorf("action sequence: want [a], got %v", act.snapshot())
	}
	if got := res.Waves[0].Hosts[0]; !got.Applied || !got.GatePass || got.Error != nil {
		t.Errorf("host result not fully successful: %+v", got)
	}
	if res.Waves[0].Hosts[0].Duration <= 0 {
		t.Errorf("expected non-zero Duration, got %v", res.Waves[0].Hosts[0].Duration)
	}
}

func TestExecute_WavesAreSequential(t *testing.T) {
	plan := mkPlan("foghorn", [][]string{{"a"}, {"b"}, {"c"}})
	inputs := mkExecutorInputs("foghorn", "a", "b", "c")
	act := &recordingActions{}
	action := func(_ context.Context, in ExecutorInput) error {
		// Mark each action with a brief delay so any concurrency leaks
		// across waves would surface as out-of-order recordings.
		time.Sleep(5 * time.Millisecond)
		return act.record(in.Host.Name)
	}

	res, err := ExecuteRolloutPlan(context.Background(), plan, inputs, action, noopProbe,
		ExecuteOptions{GateOverride: alwaysReady()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !slices.Equal(act.snapshot(), []string{"a", "b", "c"}) {
		t.Errorf("waves not sequential: %v", act.snapshot())
	}
	if len(res.Waves) != 3 {
		t.Errorf("expected 3 wave results, got %d", len(res.Waves))
	}
}

func TestExecute_HostsInWaveParallel(t *testing.T) {
	plan := mkPlan("bridge", [][]string{{"a", "b", "c"}})
	inputs := mkExecutorInputs("bridge", "a", "b", "c")

	// All three actions must be in-flight at the same point for the
	// barrier to release — proves they ran concurrently.
	var inFlight atomic.Int32
	barrier := make(chan struct{})
	action := func(_ context.Context, in ExecutorInput) error {
		_ = in
		if inFlight.Add(1) == 3 {
			close(barrier)
		}
		<-barrier
		return nil
	}

	res, err := ExecuteRolloutPlan(context.Background(), plan, inputs, action, noopProbe,
		ExecuteOptions{GateOverride: alwaysReady()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Halted {
		t.Errorf("expected non-halted")
	}
	if len(res.Waves[0].Hosts) != 3 {
		t.Errorf("expected 3 hosts in wave 1, got %d", len(res.Waves[0].Hosts))
	}
}

func TestExecute_HaltsOnActionFailure(t *testing.T) {
	plan := mkPlan("foghorn", [][]string{{"a"}, {"b"}, {"c"}})
	inputs := mkExecutorInputs("foghorn", "a", "b", "c")
	act := &recordingActions{
		fails: map[string]error{"b": errors.New("systemctl returned 1")},
	}
	action := func(_ context.Context, in ExecutorInput) error {
		return act.record(in.Host.Name)
	}

	res, err := ExecuteRolloutPlan(context.Background(), plan, inputs, action, noopProbe,
		ExecuteOptions{GateOverride: alwaysReady()})
	if err == nil {
		t.Fatal("expected error from failed action, got nil")
	}
	if !res.Halted {
		t.Error("expected Halted = true")
	}
	// Wave 1 (a) completed, wave 2 (b) failed, wave 3 (c) must never run.
	seen := act.snapshot()
	if !slices.Equal(seen, []string{"a", "b"}) {
		t.Errorf("expected actions to stop after b's failure; got %v", seen)
	}
	if len(res.Waves) != 2 {
		t.Errorf("expected 2 wave results captured (1 ok + 1 halted), got %d", len(res.Waves))
	}
	if res.Waves[1].Hosts[0].Error == nil {
		t.Error("expected wave 2 host result to carry the error")
	}
	if res.Waves[1].Hosts[0].Applied {
		t.Error("failed host should not be marked Applied")
	}
}

func TestExecute_HaltsOnGateFailure(t *testing.T) {
	plan := mkPlan("foghorn", [][]string{{"a"}, {"b"}})
	inputs := mkExecutorInputs("foghorn", "a", "b")
	action := func(context.Context, ExecutorInput) error { return nil }
	gateErr := errors.New("readiness timeout")
	gates := func(_ string) Gate { return stubGate{err: gateErr} }

	res, err := ExecuteRolloutPlan(context.Background(), plan, inputs, action, noopProbe,
		ExecuteOptions{GateOverride: gates})
	if err == nil {
		t.Fatal("expected error from failed gate")
	}
	if !res.Halted {
		t.Error("expected Halted = true")
	}
	// First wave halted; second wave never started.
	if len(res.Waves) != 1 {
		t.Errorf("expected exactly 1 wave captured, got %d", len(res.Waves))
	}
	h := res.Waves[0].Hosts[0]
	if !h.Applied {
		t.Error("action ran successfully — host should be Applied=true even though gate failed")
	}
	if h.GatePass {
		t.Error("gate failed — should be GatePass=false")
	}
}

func TestExecute_ContextCancelMidFlight(t *testing.T) {
	plan := mkPlan("foghorn", [][]string{{"a"}})
	inputs := mkExecutorInputs("foghorn", "a")
	action := func(ctx context.Context, _ ExecutorInput) error {
		<-ctx.Done()
		return ctx.Err()
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var (
		res ExecuteResult
		err error
	)
	go func() {
		res, err = ExecuteRolloutPlan(ctx, plan, inputs, action, noopProbe,
			ExecuteOptions{GateOverride: alwaysReady()})
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	<-done

	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if !res.Halted {
		t.Error("expected Halted = true")
	}
}

func TestExecute_MissingInput(t *testing.T) {
	plan := mkPlan("foghorn", [][]string{{"unmapped"}})
	// Empty inputs — wave references a host with no ExecutorInput.
	res, err := ExecuteRolloutPlan(context.Background(), plan, map[string]ExecutorInput{},
		func(context.Context, ExecutorInput) error { return nil }, noopProbe,
		ExecuteOptions{GateOverride: alwaysReady()})
	if err == nil {
		t.Fatal("expected error for missing input")
	}
	if !res.Halted {
		t.Error("expected Halted = true")
	}
}

func TestExecute_NilActionFunc(t *testing.T) {
	_, err := ExecuteRolloutPlan(context.Background(), RolloutPlan{}, nil, nil, noopProbe, ExecuteOptions{})
	if err == nil {
		t.Error("expected error for nil ActionFunc")
	}
}

func TestExecute_NilProbeFunc(t *testing.T) {
	_, err := ExecuteRolloutPlan(context.Background(), RolloutPlan{}, nil,
		func(context.Context, ExecutorInput) error { return nil }, nil, ExecuteOptions{})
	if err == nil {
		t.Error("expected error for nil ProbeFunc")
	}
}

func TestExecute_EmptyPlanIsNoOp(t *testing.T) {
	res, err := ExecuteRolloutPlan(context.Background(), RolloutPlan{Service: "foghorn"},
		map[string]ExecutorInput{},
		func(context.Context, ExecutorInput) error { return fmt.Errorf("should not run") },
		noopProbe, ExecuteOptions{GateOverride: alwaysReady()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Halted {
		t.Error("expected non-halted empty result")
	}
	if len(res.Waves) != 0 {
		t.Errorf("expected zero waves for empty plan, got %d", len(res.Waves))
	}
}
