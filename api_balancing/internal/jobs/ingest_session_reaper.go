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
// forever) and purges expired close-before-insert tombstones.
//
// It never ends a session because the session's node lost its control connection. Control loss
// leaves Mist and the publisher running; the session ends on evidence instead — a publisher for the
// same stream admitted on another node while this node is absent (MintIngestSession takeover), or
// the node's next registration not listing the generation (ReconcileNodeIngestSessions).
type IngestSessionReaperJob struct {
	logger       logging.Logger
	interval     time.Duration
	tombstoneTTL time.Duration
	pendingTTL   time.Duration
	stopCh       chan struct{}
	wg           sync.WaitGroup

	purgeTombstones func(ctx context.Context, olderThan time.Duration) (int64, error)
	reapPending     func(ctx context.Context, olderThan time.Duration, logger logging.Logger) (int, error)
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
	return &IngestSessionReaperJob{
		logger:          cfg.Logger,
		interval:        interval,
		tombstoneTTL:    tombstoneTTL,
		pendingTTL:      pendingTTL,
		stopCh:          make(chan struct{}),
		purgeTombstones: control.PurgeExpiredCloseTombstones,
		reapPending:     control.ReapNeverProjectedIngestSessions,
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
}
