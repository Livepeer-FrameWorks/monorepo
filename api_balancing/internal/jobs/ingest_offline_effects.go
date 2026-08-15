package jobs

import (
	"context"
	"errors"
	"sync"
	"time"

	"frameworks/api_balancing/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// IngestOfflineEffectsJob drains the durable, revision-fenced stream-offline ledger. The control
// layer holds the stream advisory lock across the final authority check and Apply callback, so a
// reconnect cannot interleave with a partial teardown.
type IngestOfflineEffectsJob struct {
	logger         logging.Logger
	apply          func(context.Context, control.OfflineEffect) error
	leaderInstance func() string
	interval       time.Duration
	stopCh         chan struct{}
	purgeCountdown int
	wg             sync.WaitGroup
}

type IngestOfflineEffectsConfig struct {
	Logger   logging.Logger
	Apply    func(context.Context, control.OfflineEffect) error
	Interval time.Duration
	// LeaderInstance resolves the federation leader's instance id for leader-affine deferrals.
	LeaderInstance func() string
}

func NewIngestOfflineEffectsJob(cfg IngestOfflineEffectsConfig) *IngestOfflineEffectsJob {
	interval := cfg.Interval
	if interval <= 0 {
		interval = time.Second
	}
	return &IngestOfflineEffectsJob{logger: cfg.Logger, apply: cfg.Apply, leaderInstance: cfg.LeaderInstance, interval: interval, stopCh: make(chan struct{})}
}

func (j *IngestOfflineEffectsJob) Start() {
	if j.apply == nil {
		j.logger.Warn("Ingest offline-effects job not started: apply callback missing")
		return
	}
	j.wg.Add(1)
	go j.run()
}

func (j *IngestOfflineEffectsJob) Stop() {
	if j.apply == nil {
		return
	}
	close(j.stopCh)
	j.wg.Wait()
}

func (j *IngestOfflineEffectsJob) run() {
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

// RunOnce applies a bounded batch. It is exported so durable trigger handlers can request immediate
// convergence after enqueue without taking ownership away from the background worker.
func (j *IngestOfflineEffectsJob) RunOnce(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	// Retention: terminal rows are diagnostics, not working state — without a purge the table
	// accumulates one row per ingest generation for the deployment lifetime. Runs BATCHED on a
	// slow cadence so maintenance never competes with the claim pass for the batch context.
	j.purgeCountdown--
	if j.purgeCountdown <= 0 {
		j.purgeCountdown = 300
		if _, purgeErr := control.PurgeTerminalOfflineEffects(ctx, 24*time.Hour); purgeErr != nil {
			j.logger.WithError(purgeErr).Warn("Failed to purge terminal effect rows")
		}
	}
	instanceID := control.GetInstanceID()
	claimedAt := time.Now()
	const lease = 30 * time.Second
	const leaseMargin = 15 * time.Second
	const effectBudget = 12 * time.Second
	effects, err := control.ClaimOfflineEffects(ctx, 64, lease, instanceID)
	if err != nil {
		j.logger.WithError(err).Warn("Ingest offline-effects claim failed")
		return
	}
	for i, effect := range effects {
		if time.Since(claimedAt) > lease-leaseMargin {
			// Hand the unprocessed tail back before the lease could expire mid-teardown.
			for _, rest := range effects[i:] {
				if relErr := control.ReleaseOfflineEffectNotOwner(ctx, rest, ""); relErr != nil {
					j.logger.WithError(relErr).Warn("Failed to release offline-effect batch tail")
				}
			}
			return
		}
		effectCtx, cancelEffect := context.WithTimeout(ctx, effectBudget)
		_, applyErr := control.ApplyClaimedOfflineEffect(effectCtx, effect, j.apply)
		cancelEffect()
		if applyErr == nil {
			continue
		}
		if errors.Is(applyErr, control.ErrOfflineEffectDeferred) {
			// Leader-affine: record the leader's instance as the durable claim affinity so IT wins
			// the next claim (staleness escape covers leadership churn).
			authority := ""
			if j.leaderInstance != nil {
				authority = j.leaderInstance()
			}
			if relErr := control.ReleaseOfflineEffectNotOwner(ctx, effect, authority); relErr != nil {
				j.logger.WithError(relErr).Warn("Failed to defer offline effect to its leader replica")
			}
			continue
		}
		j.logger.WithError(applyErr).WithFields(logging.Fields{
			"stream":   effect.InternalName,
			"revision": effect.SourceRevision,
		}).Warn("Ingest offline effect failed; retaining for retry")
		if failErr := control.FailOfflineEffect(ctx, effect, applyErr); failErr != nil {
			j.logger.WithError(failErr).Warn("Failed to release ingest offline-effect lease")
		}
	}
}
