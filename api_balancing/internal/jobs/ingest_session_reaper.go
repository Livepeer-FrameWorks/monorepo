package jobs

import (
	"context"
	"sync"
	"time"

	"frameworks/api_balancing/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// IngestSessionReaperJob is the ingest-lifecycle garbage collector. Each pass retires sessions whose
// admission never confirmed its source projection (a pending row must not hold stream authority
// forever), purges expired close-before-insert tombstones, and, on the replica holding the
// lost-node lease, ends the sessions of nodes that have been gone past control.IngestNodeLostAfter.
//
// Control loss alone ends nothing: Mist and the publisher keep running. A session ends on evidence
// — a publisher for the same stream admitted on another node while this node is absent
// (MintIngestSession takeover), the node's next registration not listing the generation
// (ReconcileNodeIngestSessions), or the node staying without a control connection and without a
// live pulled copy of its streams for the lost-node window (node_lost).
type IngestSessionReaperJob struct {
	logger       logging.Logger
	interval     time.Duration
	tombstoneTTL time.Duration
	pendingTTL   time.Duration
	stopCh       chan struct{}
	wg           sync.WaitGroup

	purgeTombstones func(ctx context.Context, olderThan time.Duration) (int64, error)
	reapPending     func(ctx context.Context, olderThan time.Duration, logger logging.Logger) (int, error)

	// Lost-node tracking. absence is kept only while this replica holds the lease; losing the lease
	// drops it, so a later leadership starts every clock anew.
	leader     func(ctx context.Context) (bool, error)
	lostDeps   control.IngestNodeLostDeps
	now        func() time.Time
	absence    control.IngestNodeAbsence
	leaseHeld  bool
	leaseKnown bool
}

// IngestSessionReaperConfig configures the job.
type IngestSessionReaperConfig struct {
	Logger       logging.Logger
	Interval     time.Duration // How often to scan (default: 30s)
	TombstoneTTL time.Duration // How long a close-before-insert tombstone is retained (default: 10m)
	PendingTTL   time.Duration // Maximum lifetime of an unconfirmed source projection (default: 2m)
}

// NewIngestSessionReaperJob builds the reaper with defaulted thresholds.
func NewIngestSessionReaperJob(cfg IngestSessionReaperConfig) *IngestSessionReaperJob {
	interval := cfg.Interval
	if interval == 0 {
		interval = 30 * time.Second
	}
	tombstoneTTL := cfg.TombstoneTTL
	if tombstoneTTL == 0 {
		tombstoneTTL = 10 * time.Minute
	}
	pendingTTL := cfg.PendingTTL
	if pendingTTL == 0 {
		pendingTTL = 2 * time.Minute
	}
	// The lease outlives several passes so a healthy holder keeps it between ticks.
	leaseTTL := 3 * interval
	return &IngestSessionReaperJob{
		logger:          cfg.Logger,
		interval:        interval,
		tombstoneTTL:    tombstoneTTL,
		pendingTTL:      pendingTTL,
		stopCh:          make(chan struct{}),
		purgeTombstones: control.PurgeExpiredCloseTombstones,
		reapPending:     control.ReapNeverProjectedIngestSessions,
		leader: func(ctx context.Context) (bool, error) {
			return control.IngestNodeLostLeaseHeld(ctx, leaseTTL)
		},
		lostDeps: control.IngestNodeLostDeps{
			Present:  control.NodePresenceLookup,
			Guard:    control.NodeRetireGuardLookup,
			Evidence: control.IngestLifeEvidenceLookup,
		},
		now:     time.Now,
		absence: make(control.IngestNodeAbsence),
	}
}

// Start begins the background reaper loop.
func (j *IngestSessionReaperJob) Start() {
	j.wg.Add(1)
	go j.run()
	j.logger.Info("Ingest session reaper job started")
}

// Stop gracefully stops the job.
func (j *IngestSessionReaperJob) Stop() {
	close(j.stopCh)
	j.wg.Wait()
	j.logger.Info("Ingest session reaper job stopped")
}

func (j *IngestSessionReaperJob) run() {
	defer j.wg.Done()
	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()
	j.reconcile()
	for {
		select {
		case <-ticker.C:
			j.reconcile()
		case <-j.stopCh:
			return
		}
	}
}

func (j *IngestSessionReaperJob) reconcile() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if j.purgeTombstones != nil {
		if _, err := j.purgeTombstones(ctx, j.tombstoneTTL); err != nil {
			j.logger.WithError(err).Warn("Ingest session reaper: close-tombstone purge failed")
		}
	}
	if j.reapPending != nil {
		if _, err := j.reapPending(ctx, j.pendingTTL, j.logger); err != nil {
			j.logger.WithError(err).Warn("Ingest session reaper: never-projected session pass failed")
		}
	}
	j.reapLostNodes(ctx)
}

// reapLostNodes runs the lost-node pass on the lease holder only, so one replica owns the absence
// clocks and each lost session is ended and logged once.
func (j *IngestSessionReaperJob) reapLostNodes(ctx context.Context) {
	if j.leader == nil || j.absence == nil {
		return
	}
	held, err := j.leader(ctx)
	if err != nil {
		held = false
	}
	if !j.leaseKnown || held != j.leaseHeld {
		switch {
		case err != nil:
			j.logger.WithError(err).Warn("Ingest session reaper: lost-node lease unavailable; this replica does not end sessions of lost nodes")
		case held:
			j.logger.Info("Ingest session reaper: took the lost-node lease; absence clocks start now")
		default:
			j.logger.Info("Ingest session reaper: another replica holds the lost-node lease")
		}
	}
	j.leaseKnown, j.leaseHeld = true, held
	if !held {
		clear(j.absence)
		return
	}
	if _, err := control.ReapLostNodeIngestSessionsOnce(ctx, j.lostDeps, j.absence, j.now(), j.logger); err != nil {
		j.logger.WithError(err).Warn("Ingest session reaper: lost-node pass failed")
	}
}
