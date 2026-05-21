package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"time"

	"frameworks/cli/pkg/inventory"
	"golang.org/x/sync/errgroup"
)

// ExecutorAction names what the executor should ask the host to do for
// one wave slot. Caller decides which based on diff kinds + service
// capabilities. The executor itself never inspects diffs — it just runs the
// action it's handed.
type ExecutorAction string

const (
	ActionReload  ExecutorAction = "reload"
	ActionRestart ExecutorAction = "restart"
)

// ExecutorInput is one host's slot inside a wave: the action to run plus
// the resolved inventory.Host (executor doesn't re-resolve from manifest).
type ExecutorInput struct {
	Key     string
	Service string
	Host    inventory.Host
	Action  ExecutorAction
}

// ActionFunc actually runs the action against a host. Production cluster
// apply uses the existing provisioner for the host-scoped desired state;
// tests inject a small stub here.
type ActionFunc func(ctx context.Context, input ExecutorInput) error

// HostResult records what happened for one host in one wave.
type HostResult struct {
	Service  string
	Host     string
	Action   ExecutorAction
	Applied  bool // action ran without error
	GatePass bool // readiness gate returned nil
	Duration time.Duration
	Error    error
}

// WaveResult bundles every HostResult for one wave plus a Halted flag
// indicating whether this wave aborted (any host failed action or gate).
type WaveResult struct {
	Index  int
	Hosts  []HostResult
	Halted bool
}

// ExecuteResult is the rollup the caller renders. Halted = true means a
// host in some wave failed; the wave that failed is the last one in
// WaveResults, and any later waves never started. Re-running cluster
// apply against the same manifest is the recovery — the diff classifier
// only re-emits remaining (still-changed) hosts.
type ExecuteResult struct {
	Waves  []WaveResult
	Halted bool
}

// ExecuteOptions tunes executor behavior. Gate selection is per-service
// via GateForService; this struct is for cross-cutting tunables.
type ExecuteOptions struct {
	// GateOverride, when non-nil, replaces the default GateForService(svc)
	// resolution. Used in tests; production leaves this nil.
	GateOverride func(svc string) Gate
}

// ExecuteRolloutPlan runs each wave sequentially. Inside a wave, hosts
// run in parallel via errgroup; the first host to fail (action or gate)
// cancels the rest of the wave and prevents any later waves from
// starting. Per-host outcomes are captured for the caller's report even
// when a wave halts mid-flight (cancelled goroutines record their
// partial state up to the cancellation point).
//
// inputs maps "service@host" → ExecutorInput so the executor can pair
// each wave Task back to its action. The caller built this map when it
// chose reload vs restart per host.
func ExecuteRolloutPlan(
	ctx context.Context,
	plan RolloutPlan,
	inputs map[string]ExecutorInput,
	run ActionFunc,
	probe ProbeFunc,
	opts ExecuteOptions,
) (ExecuteResult, error) {
	if run == nil {
		return ExecuteResult{}, fmt.Errorf("executor: nil ActionFunc")
	}
	if probe == nil {
		return ExecuteResult{}, fmt.Errorf("executor: nil ProbeFunc")
	}
	gateFor := opts.GateOverride
	if gateFor == nil {
		gateFor = GateForService
	}

	out := ExecuteResult{}

	for waveIdx, wave := range plan.Waves {
		waveRes := WaveResult{Index: waveIdx + 1}
		results := make([]HostResult, len(wave.Tasks))

		// Cancel the wave the moment one host fails; remaining goroutines
		// see the cancellation and bail out without doing further work.
		// results[i] writes are index-disjoint (one goroutine per slot),
		// so no mutex needed.
		waveCtx, cancel := context.WithCancel(ctx)
		g, gctx := errgroup.WithContext(waveCtx)

		for i, task := range wave.Tasks {
			g.Go(func() error {
				key := plan.Service + "@" + task.Host
				input, ok := inputs[key]
				if !ok {
					err := fmt.Errorf("executor: no input registered for %s", key)
					results[i] = HostResult{
						Service: plan.Service,
						Host:    task.Host,
						Error:   err,
					}
					return err
				}
				started := time.Now()
				hr := HostResult{
					Service: plan.Service,
					Host:    task.Host,
					Action:  input.Action,
				}
				defer func() {
					hr.Duration = time.Since(started)
					results[i] = hr
				}()

				if err := run(gctx, input); err != nil {
					hr.Error = fmt.Errorf("%s %s: %w", input.Action, task.Host, err)
					return hr.Error
				}
				hr.Applied = true

				gateService := input.Service
				if gateService == "" {
					gateService = plan.Service
				}
				gate := gateFor(gateService)
				if err := gate.Wait(gctx, input.Host, probe); err != nil {
					hr.Error = fmt.Errorf("readiness gate on %s: %w", task.Host, err)
					return hr.Error
				}
				hr.GatePass = true
				return nil
			})
		}

		err := g.Wait()
		cancel()

		waveRes.Hosts = results
		if err != nil {
			waveRes.Halted = true
			out.Waves = append(out.Waves, waveRes)
			out.Halted = true
			return out, err
		}
		out.Waves = append(out.Waves, waveRes)
	}

	return out, nil
}

// ErrExecutorHalted is the sentinel error type the caller can check when
// rendering: any non-nil error from ExecuteRolloutPlan means the rollout
// stopped early. Callers should still render ExecuteResult.Waves to show
// what completed; the error explains why the last wave halted.
var ErrExecutorHalted = errors.New("rollout halted before completion")
