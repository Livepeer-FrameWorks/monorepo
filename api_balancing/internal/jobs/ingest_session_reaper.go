package jobs

import (
	"context"
	"sync"
	"time"

	"frameworks/api_balancing/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// IngestSessionReaperJob retires active ingest sessions whose ingest node has hard-disconnected. A
// crash / SIGKILL sends neither PUSH_INPUT_CLOSE nor STREAM_END (the node is gone), so without this
// the session stays ended_at=NULL forever and blocks a cross-node republish of the same stream (the
// (tenant, stream) partial unique rejects it as a duplicate). Each pass consults whether every open
// session's node has a conn_owner (a control connection to any replica): a node absent past the grace
// (connected nowhere) retires as a disconnect; a present node is left alone — a control reconnect is
// NOT a session end, so presence, not the ownership fence, is the signal. Presence is resolved once per
// distinct node per pass. The reaper is process-local but HA-safe — the retire is an idempotent guarded
// UPDATE, so sibling replicas race harmlessly — and fails closed when the conn_owner store is unreadable.
type IngestSessionReaperJob struct {
	logger       logging.Logger
	interval     time.Duration
	grace        time.Duration
	tombstoneTTL time.Duration
	pendingTTL   time.Duration
	dwell        control.IngestReapDwell
	stopCh       chan struct{}
	wg           sync.WaitGroup

	// nodePresent reports whether a node is connected to any replica (defaults to control.NodePresenceLookup).
	nodePresent control.NodePresenceFunc
	nodeGuard   control.NodeRetireGuardFunc
	// reapOnce runs a single pass (defaults to control.ReapIngestSessionsOnce). Injectable for tests.
	reapOnce func(ctx context.Context, nodePresent control.NodePresenceFunc, nodeGuard control.NodeRetireGuardFunc, dwell control.IngestReapDwell, now time.Time, grace time.Duration, logger logging.Logger) (int, error)
	// purgeTombstones sweeps expired close-before-insert tombstones (defaults to
	// control.PurgeExpiredCloseTombstones). The reaper is the natural home for ingest-lifecycle GC.
	purgeTombstones func(ctx context.Context, olderThan time.Duration) (int64, error)
	reapPending     func(ctx context.Context, olderThan time.Duration, logger logging.Logger) (int, error)
	// now is the injectable clock (defaults to time.Now) so the disconnect grace is deterministic in tests.
	now func() time.Time
}

// IngestSessionReaperConfig configures the job.
type IngestSessionReaperConfig struct {
	Logger       logging.Logger
	Interval     time.Duration // How often to scan (default: 30s)
	Grace        time.Duration // How long a node's conn_owner must be absent before a disconnect retire (default: 90s)
	TombstoneTTL time.Duration // How long a close-before-insert tombstone is retained (default: 10m)
	PendingTTL   time.Duration // Maximum lifetime of an unconfirmed source projection (default: 2m)
}

// NewIngestSessionReaperJob builds the reaper with defaulted thresholds.
func NewIngestSessionReaperJob(cfg IngestSessionReaperConfig) *IngestSessionReaperJob {
	interval := cfg.Interval
	if interval == 0 {
		interval = 30 * time.Second
	}
	grace := cfg.Grace
	if grace == 0 {
		grace = 90 * time.Second
	}
	tombstoneTTL := cfg.TombstoneTTL
	if tombstoneTTL == 0 {
		tombstoneTTL = 10 * time.Minute
	}
	pendingTTL := cfg.PendingTTL
	if pendingTTL == 0 {
		pendingTTL = 2 * time.Minute
	}
	return &IngestSessionReaperJob{
		logger:          cfg.Logger,
		interval:        interval,
		grace:           grace,
		tombstoneTTL:    tombstoneTTL,
		pendingTTL:      pendingTTL,
		dwell:           make(control.IngestReapDwell),
		stopCh:          make(chan struct{}),
		nodePresent:     control.NodePresenceLookup,
		nodeGuard:       control.NodeRetireGuardLookup,
		reapOnce:        control.ReapIngestSessionsOnce,
		purgeTombstones: control.PurgeExpiredCloseTombstones,
		reapPending:     control.ReapNeverProjectedIngestSessions,
		now:             time.Now,
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
	if j.reapOnce == nil || j.nodePresent == nil || j.nodeGuard == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := j.reapOnce(ctx, j.nodePresent, j.nodeGuard, j.dwell, j.now(), j.grace, j.logger); err != nil {
		j.logger.WithError(err).Warn("Ingest session reaper: pass failed")
	}
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
}
