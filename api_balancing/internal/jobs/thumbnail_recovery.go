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
	db          *sql.DB
	logger      logging.Logger
	interval    time.Duration
	batch       int
	staleness   time.Duration
	leaseTTL    time.Duration // how long a claimed completion re-drive stays leased before a peer may re-claim it
	itemTimeout time.Duration // per-attempt re-drive deadline (so one slow completion can't eat the pass budget)
	backoffBase time.Duration // per-attempt backoff step for a non-progressing (poison) re-drive, capped
	complete    func(ctx context.Context, attemptID string) (bool, error)
	reproject   func(ctx context.Context, attemptID string) (bool, error)
	reassert    func(ctx context.Context, attemptID string) (bool, error)
	stopCh      chan struct{}
	wg          sync.WaitGroup
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
	// Reproject re-drives a published-but-unprojected attempt's deterministic projection (copy the winning version
	// objects to the served key + expose has_thumbnails), returning progressed=true only when the projection
	// actually landed this pass. Wired to control.ReprojectPublishedThumbnailAttempt; when nil, the projection
	// recovery phase is skipped.
	Reproject func(ctx context.Context, attemptID string) (bool, error)
	// Reassert re-copies a projected winner's objects to the deterministic key once past the max-copy window
	// (correcting a straggler overwrite) and clears its reassert clock. Wired to control.ReassertThumbnailProjection;
	// when nil, the reassert phase is skipped.
	Reassert func(ctx context.Context, attemptID string) (bool, error)
}

// NewThumbnailRecoveryJob creates a new thumbnail recovery reconciler.
func NewThumbnailRecoveryJob(cfg ThumbnailRecoveryConfig) *ThumbnailRecoveryJob {
	interval := cfg.Interval
	if interval == 0 {
		interval = 1 * time.Minute
	}
	staleness := cfg.Staleness
	if staleness <= 0 {
		staleness = 2 * time.Minute
	}
	// Budget invariant (mirrors StagingCleanupJob): the leased batch is processed SERIALLY, each item under its own
	// itemTimeout plus a settle write, so the lease must PROVABLY cover the whole batch — otherwise the lease
	// expires mid-batch, a peer re-claims the not-yet-processed tail, and the two workers double-drive the same
	// completion. So cap batch ≤ (leaseTTL − claim/margin) / (itemTimeout + settleTimeout). With leaseTTL=10m,
	// itemTimeout=20s, settleTimeout=10s, claim+margin≈40s: (600−40)/30 = 18.
	const settleTimeout = 10 * time.Second
	const claimAndMarginBudget = 40 * time.Second
	leaseTTL := 10 * time.Minute
	itemTimeout := 20 * time.Second
	maxBatch := int((leaseTTL - claimAndMarginBudget) / (itemTimeout + settleTimeout))
	batch := cfg.Batch
	if batch <= 0 {
		batch = maxBatch
	}
	if batch > maxBatch {
		batch = maxBatch
	}
	return &ThumbnailRecoveryJob{
		db:          cfg.DB,
		logger:      cfg.Logger,
		interval:    interval,
		batch:       batch,
		staleness:   staleness,
		leaseTTL:    leaseTTL,
		itemTimeout: itemTimeout,
		backoffBase: 30 * time.Second,
		complete:    cfg.Complete,
		reproject:   cfg.Reproject,
		reassert:    cfg.Reassert,
		stopCh:      make(chan struct{}),
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
	// The claim + backlog queries run under a bounded ctx. Each re-drive item and its settlement/backoff write get
	// their OWN FRESH context (NOT derived from a pass deadline), so a slow early item can neither cancel the tail
	// nor cancel the settlement write that releases/backs-off its lease — the batch is capped to fit the lease, so
	// its total serial cost is already bounded without a pass deadline that could strand a leased tail.
	staleBefore := time.Now().Add(-j.staleness)

	// Phase 0: re-drive completions whose ThumbnailUploaded was lost — LEASED so replicas never double-drive one, each
	// under its own deadline, non-progress backed off (poison isolation).
	var completed, backedOff int
	if j.complete != nil {
		c, b := j.drainLeasedRecoveryPhase(staleBefore, control.ClaimStuckIncompleteThumbnailAttempts, j.complete, "complete")
		completed += c
		backedOff += b
	}

	// Phase 0b: re-drive published-but-unprojected attempts — the deterministic served object never landed after the
	// publish CAS. LEASED for the same reason (source may still be absent / the S3 copy may keep failing, so a plain
	// oldest-N drain would re-select the same poison rows every tick and starve newer projections). Progress = the
	// projection actually landed; non-progress backs off.
	if j.reproject != nil {
		c, b := j.drainLeasedRecoveryPhase(staleBefore, control.ClaimUnprojectedPublishedThumbnailAttempts, j.reproject, "reproject")
		completed += c
		backedOff += b
	}

	// Phase 0c: bounded REASSERT of projected winners past the max-copy window — the eventual-convergence step that
	// corrects a straggler overwrite of the deterministic key (a loser's accepted copy that completed late). LEASED with
	// per-row backoff like the other phases: a re-copy that keeps failing (e.g. its source went missing) must space out
	// its retries and NOT re-select at the head every tick, or it would starve newer winners' reasserts.
	if j.reassert != nil {
		c, b := j.drainLeasedRecoveryPhase(staleBefore, control.ClaimDueReassertThumbnailAttempts, j.reassert, "reassert")
		completed += c
		backedOff += b
	}

	// Observable backlog: due leased-phase attempts (stuck-incomplete + unprojected) still awaiting a re-drive
	// (excludes backed-off + leased), so it reflects actionable work and shrinks toward zero as the reconciler drains.
	blCtx, blCancel := context.WithTimeout(context.Background(), 15*time.Second)
	backlog, blErr := control.ThumbnailRecoveryBacklog(blCtx, j.db, staleBefore)
	if blErr != nil {
		j.logger.WithError(blErr).Debug("Thumbnail recovery: backlog query failed")
	}
	unprojBacklog, ubErr := control.UnprojectedThumbnailRecoveryBacklog(blCtx, j.db, staleBefore)
	blCancel()
	if ubErr != nil {
		j.logger.WithError(ubErr).Debug("Thumbnail recovery: unprojected backlog query failed")
	}
	backlog += unprojBacklog

	// DRAIN the guarded, self-clearing phases (re-drive 'publishing', fail+sweep expired, GC superseded). These
	// transitions are idempotent under concurrency and terminalize/delete their rows, so they need no lease; the
	// drain bounds a high-frequency-publication GC backlog. Each pass is oldest-first, so progress is fair.
	const maxDrainPasses = 20
	var redriven, failed, gced int
	for pass := 0; pass < maxDrainPasses; pass++ {
		drainCtx, drainCancel := context.WithTimeout(context.Background(), 30*time.Second)
		r, f, g, err := control.RecoverStuckThumbnailAttempts(drainCtx, j.db, time.Now(), j.batch)
		drainCancel()
		if err != nil {
			j.logger.WithError(err).Warn("Thumbnail recovery pass failed")
			break
		}
		redriven += r
		failed += f
		gced += g
		// Nothing left to re-drive/fail this tick and the GC batch was not full → the backlog is drained.
		if r == 0 && f == 0 && g < j.batch {
			break
		}
	}
	if completed > 0 || backedOff > 0 || redriven > 0 || failed > 0 || gced > 0 || backlog > 0 {
		j.logger.WithFields(logging.Fields{
			"completed": completed, "backed_off": backedOff, "redriven": redriven,
			"failed": failed, "gced": gced, "backlog": backlog,
		}).Info("Thumbnail recovery pass complete")
	}
}

// drainLeasedRecoveryPhase runs one leased recovery phase: it claims a due batch via `claim` (SKIP LOCKED + fencing
// token, poison rows backed off so they sink), drives each attempt with `drive` under its OWN item deadline, and on a
// FRESH short context settles a progressed attempt (lease/backoff cleared) or backs off a non-progressing one with
// capped-linear growth (so it is not re-selected at the head every tick). Shared by the lost-completion and the
// unprojected-projection phases — both count only REAL progress. Returns (progressed, backedOff).
func (j *ThumbnailRecoveryJob) drainLeasedRecoveryPhase(
	staleBefore time.Time,
	claim func(ctx context.Context, dbh *sql.DB, staleBefore time.Time, leaseTTL time.Duration, limit int) ([]control.ClaimedRecoveryAttempt, error),
	drive func(ctx context.Context, attemptID string) (bool, error),
	phase string,
) (progressed, backedOff int) {
	claimCtx, claimCancel := context.WithTimeout(context.Background(), 30*time.Second)
	claimed, err := claim(claimCtx, j.db, staleBefore, j.leaseTTL, j.batch)
	claimCancel()
	if err != nil {
		j.logger.WithError(err).WithField("phase", phase).Warn("Thumbnail recovery: claim failed")
	}
	for _, c := range claimed {
		itemCtx, itemCancel := context.WithTimeout(context.Background(), j.itemTimeout)
		ok, dErr := drive(itemCtx, c.AttemptID)
		itemCancel()
		// Settlement/backoff writes use their OWN fresh short context so they always complete (releasing or backing-off
		// the lease) even if the re-drive above spent its deadline.
		settleCtx, settleCancel := context.WithTimeout(context.Background(), 10*time.Second)
		if dErr == nil && ok {
			if sErr := control.SettleThumbnailRecoveryDone(settleCtx, j.db, c.AttemptID, c.Token); sErr != nil {
				j.logger.WithError(sErr).WithField("attempt_id", c.AttemptID).Debug("Thumbnail recovery: settle progressed attempt failed")
			}
			settleCancel()
			progressed++
			continue
		}
		backoff := j.backoffBase * time.Duration(min(c.Attempts+1, 30))
		msg := ""
		if dErr != nil {
			msg = dErr.Error()
		}
		if bErr := control.BackoffThumbnailRecovery(settleCtx, j.db, c.AttemptID, c.Token, backoff, msg); bErr != nil {
			j.logger.WithError(bErr).WithField("attempt_id", c.AttemptID).Debug("Thumbnail recovery: backoff failed")
		}
		settleCancel()
		backedOff++
	}
	return progressed, backedOff
}
