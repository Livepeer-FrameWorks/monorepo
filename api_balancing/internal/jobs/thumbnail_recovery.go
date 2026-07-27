package jobs

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"frameworks/api_balancing/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// ThumbnailRecoveryJob is the crash-recovery reconciler for the thumbnail publication state machine. Each pass
// re-drives attempts stuck in 'publishing' (the idempotent pointer CAS completes a crash between promote and
// commit) and fails + sweeps attempts abandoned in an earlier state past their lease (their staging + version
// objects are enqueued for the shared staging-cleanup worker). DB-only: the actual S3 deletes are done by
// StagingCleanupJob draining foghorn.staging_cleanup_queue.
type ThumbnailRecoveryJob struct {
	db        *sql.DB
	logger    logging.Logger
	interval  time.Duration
	batch     int
	staleness time.Duration
	complete  func(ctx context.Context, attemptID string) (bool, error)
	stopCh    chan struct{}
	wg        sync.WaitGroup
}

// ThumbnailRecoveryConfig configures the reconciler.
type ThumbnailRecoveryConfig struct {
	DB       *sql.DB
	Logger   logging.Logger
	Interval time.Duration // How often to run (default: 1 minute)
	Batch    int           // Max attempts re-driven / failed per phase per pass (default: 100)
	// Staleness is how long an incomplete (pre-publishing) attempt must be idle before recovery re-drives its
	// completion — long enough that the node's completion is presumed lost, not in-flight (default: 2 minutes).
	Staleness time.Duration
	// Complete re-drives a stuck non-expired attempt's completion (verify -> promote -> publish) against its
	// staged objects, returning progressed=true only when the attempt reached a terminal state this pass. Wired
	// to control.CompleteThumbnailAttemptForRecovery; when nil, that recovery phase is skipped (the DB-only
	// fail/GC phases still run).
	Complete func(ctx context.Context, attemptID string) (bool, error)
}

// NewThumbnailRecoveryJob creates a new thumbnail recovery reconciler.
func NewThumbnailRecoveryJob(cfg ThumbnailRecoveryConfig) *ThumbnailRecoveryJob {
	interval := cfg.Interval
	if interval == 0 {
		interval = 1 * time.Minute
	}
	batch := cfg.Batch
	if batch <= 0 {
		batch = 100
	}
	staleness := cfg.Staleness
	if staleness <= 0 {
		staleness = 2 * time.Minute
	}
	return &ThumbnailRecoveryJob{
		db:        cfg.DB,
		logger:    cfg.Logger,
		interval:  interval,
		batch:     batch,
		staleness: staleness,
		complete:  cfg.Complete,
		stopCh:    make(chan struct{}),
	}
}

// Start begins the background reconciler loop.
func (j *ThumbnailRecoveryJob) Start() {
	j.wg.Add(1)
	go j.run()
	j.logger.Info("Thumbnail recovery job started")
}

// Stop gracefully stops the job.
func (j *ThumbnailRecoveryJob) Stop() {
	close(j.stopCh)
	j.wg.Wait()
	j.logger.Info("Thumbnail recovery job stopped")
}

func (j *ThumbnailRecoveryJob) run() {
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

func (j *ThumbnailRecoveryJob) reconcile() {
	if j.db == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	now := time.Now()

	// Phase 0: re-drive completions whose ThumbnailUploaded was lost. A non-expired attempt idle in a
	// pre-publishing state past the staleness window is presumed to have lost its completion (dropped send or a
	// crash before 'publishing'); re-run it against the staged objects so a one-shot VOD thumbnail isn't lost.
	// Idempotent: a not-yet-uploaded staging object simply leaves the attempt for the next pass.
	var completed int
	if j.complete != nil {
		ids, err := control.StuckIncompleteThumbnailAttemptIDs(ctx, j.db, now, now.Add(-j.staleness), j.batch)
		if err != nil {
			j.logger.WithError(err).Warn("Thumbnail recovery: query stuck-incomplete attempts failed")
		}
		for _, id := range ids {
			progressed, cErr := j.complete(ctx, id)
			if cErr != nil {
				j.logger.WithError(cErr).WithField("attempt_id", id).Warn("Thumbnail recovery: re-drive completion failed; will retry next pass")
				continue
			}
			// Count ONLY attempts that actually reached a terminal state — a not-yet-uploaded / poison row is left
			// stuck and must not be reported as completed.
			if progressed {
				completed++
			}
		}
	}

	// DRAIN: high-frequency live publication (a new version every few seconds per stream) can create superseded
	// versions faster than a single fixed batch removes them. Re-run until a pass reports less than a full batch
	// of GC work (the backlog is drained) or a bounded pass cap is hit (so one tick can't run unbounded). Each
	// pass is oldest-first, so progress is fair.
	const maxDrainPasses = 20
	var redriven, failed, gced int
	for pass := 0; pass < maxDrainPasses; pass++ {
		r, f, g, err := control.RecoverStuckThumbnailAttempts(ctx, j.db, now, j.batch)
		if err != nil {
			j.logger.WithError(err).Warn("Thumbnail recovery pass failed")
			return
		}
		redriven += r
		failed += f
		gced += g
		// Nothing left to re-drive/fail this tick and the GC batch was not full → the backlog is drained.
		if r == 0 && f == 0 && g < j.batch {
			break
		}
	}
	if completed > 0 || redriven > 0 || failed > 0 || gced > 0 {
		j.logger.WithFields(logging.Fields{"completed": completed, "redriven": redriven, "failed": failed, "gced": gced}).Info("Thumbnail recovery pass complete")
	}
}
