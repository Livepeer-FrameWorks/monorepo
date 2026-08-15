package jobs

import (
	"context"
	"sync"
	"time"

	"frameworks/api_balancing/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// IngestAdmissionEffectsJob drives the durable admission-effects ledger (the mirror of the offline
// teardown job). Each obligation's legs settle by their own rules: local legs (federation live
// broadcast, Decklog delivery) complete in the Apply callback; remote legs (prior-owner drain,
// push-target activation) are dispatched here but completed only by Helmsman's generation-correlated
// acknowledgements; drain/activation/broadcast are mooted for a generation that has already ended
// while the Decklog leg remains owed. The control layer runs LOCKED phases only around durable state
// (never across the callback's dispatches — acknowledgements land on the unlocked row mid-flight),
// and a crashed replica's obligations are picked up by any sibling.
type IngestAdmissionEffectsJob struct {
	logger         logging.Logger
	apply          func(context.Context, control.AdmissionEffect) (control.AdmissionEffectLegResults, error)
	interval       time.Duration
	stopCh         chan struct{}
	purgeCountdown int
	wg             sync.WaitGroup
}

type IngestAdmissionEffectsConfig struct {
	Logger   logging.Logger
	Apply    func(context.Context, control.AdmissionEffect) (control.AdmissionEffectLegResults, error)
	Interval time.Duration
}

func NewIngestAdmissionEffectsJob(cfg IngestAdmissionEffectsConfig) *IngestAdmissionEffectsJob {
	interval := cfg.Interval
	if interval <= 0 {
		interval = time.Second
	}
	return &IngestAdmissionEffectsJob{logger: cfg.Logger, apply: cfg.Apply, interval: interval, stopCh: make(chan struct{})}
}

func (j *IngestAdmissionEffectsJob) Start() {
	if j.apply == nil {
		j.logger.Warn("Ingest admission-effects job not started: apply callback missing")
		return
	}
	j.wg.Add(1)
	go j.run()
}

func (j *IngestAdmissionEffectsJob) Stop() {
	if j.apply == nil {
		return
	}
	close(j.stopCh)
	j.wg.Wait()
}

func (j *IngestAdmissionEffectsJob) run() {
	defer j.wg.Done()
	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()
	j.RunOnce(context.Background())
	for {
		select {
		case <-ticker.C:
			j.RunOnce(context.Background())
		case <-j.stopCh:
			return
		}
	}
}

// RunOnce applies a bounded batch. Exported so the admission path can request immediate convergence
// after enqueue without taking ownership away from the background worker. Claims are
// instance-addressed: rows durably deferred by THIS instance (an owed leg needs an authority it
// lacks) are excluded at the claim, so the authoritative replica is never starved of the SKIP
// LOCKED race. A lease-margin guard hands back the unprocessed batch tail before the lease could
// expire mid-dispatch — every dispatch is individually bounded, but a long batch of bounded
// dispatches must not run past its own lease and let a sibling re-claim a row this worker is still
// touching.
func (j *IngestAdmissionEffectsJob) RunOnce(parent context.Context) {
	// An effect only starts with >= leaseMargin remaining, and every callback-visible DB, Redis,
	// Decklog and relay operation receives effectCtx. A blocked bidi stream.Send can outlive the
	// caller deadline because gRPC exposes no per-Send cancellation; receiver-side age and exact
	// ingest-generation fences make that residual late delivery non-destructive.
	const lease = 30 * time.Second
	const leaseMargin = 15 * time.Second
	const effectBudget = 12 * time.Second
	ctx, cancel := context.WithTimeout(parent, lease)
	defer cancel()
	instanceID := control.GetInstanceID()
	// Retention: terminal rows are diagnostics, not working state — without a purge the table
	// accumulates one row per ingest generation for the deployment lifetime. Runs BATCHED on a
	// slow cadence so maintenance never competes with the claim pass for the batch context.
	j.purgeCountdown--
	if j.purgeCountdown <= 0 {
		j.purgeCountdown = 300
		if _, purgeErr := control.PurgeTerminalAdmissionEffects(ctx, 24*time.Hour); purgeErr != nil {
			j.logger.WithError(purgeErr).Warn("Failed to purge terminal effect rows")
		}
	}
	claimedAt := time.Now()
	effects, err := control.ClaimAdmissionEffects(ctx, 64, lease, instanceID)
	if err != nil {
		j.logger.WithError(err).Warn("Ingest admission-effects claim failed")
		return
	}
	for i, effect := range effects {
		if time.Since(claimedAt) > lease-leaseMargin {
			// Not enough lease left to safely dispatch: hand the tail back cleanly (immediately
			// claimable by anyone, this instance included) instead of dispatching on a lease that
			// could expire mid-flight and let a sibling duplicate a destructive command.
			for _, rest := range effects[i:] {
				if relErr := control.ReleaseAdmissionEffectNotOwner(ctx, rest, ""); relErr != nil {
					j.logger.WithError(relErr).Warn("Failed to release admission-effect batch tail")
				}
			}
			return
		}
		deferred := false
		authority := ""
		effectCtx, cancelEffect := context.WithTimeout(ctx, effectBudget)
		_, applyErr := control.ApplyClaimedAdmissionEffect(effectCtx, effect, func(cbCtx context.Context, e control.AdmissionEffect) (control.AdmissionEffectLegResults, error) {
			legs, err := j.apply(cbCtx, e)
			if legs.Deferred && err == nil {
				deferred = true
				authority = legs.AuthorityInstance
			}
			return legs, err
		})
		cancelEffect()
		if deferred {
			// An owed leg needs an authority this replica lacks — record that authority's instance
			// as the row's durable claim affinity so IT wins the next claim (with a staleness
			// escape if it died or the authority moved).
			if relErr := control.ReleaseAdmissionEffectNotOwner(ctx, effect, authority); relErr != nil {
				j.logger.WithError(relErr).Warn("Failed to defer admission effect to its authoritative replica")
			}
			continue
		}
		if applyErr == nil {
			continue
		}
		j.logger.WithError(applyErr).WithFields(logging.Fields{
			"stream":     effect.InternalName,
			"generation": effect.SourceGeneration,
		}).Warn("Ingest admission effect failed; retaining for retry")
		if failErr := control.FailAdmissionEffect(ctx, effect, applyErr); failErr != nil {
			j.logger.WithError(failErr).Warn("Failed to release ingest admission-effect lease")
		}
	}
}
