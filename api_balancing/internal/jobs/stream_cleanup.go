package jobs

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"frameworks/api_balancing/internal/artifacts"
	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/database/foghorndb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// StreamCleanupJob drains foghorn.stream_cleanup_obligation: for each pending tombstone it sweeps the deleted
// stream's thumbnail objects from THIS cell's immutable local store, drops the control rows, and marks the row
// 'cleaned' — leaving the row in place as the durable tombstone. This is the byte-deletion half of the
// stream-deletion saga: Commodore durably delivers the obligation to the owning cell's Foghorn (which records the
// tombstone inside a guarded tx); this worker guarantees the bytes actually go away, retried from the durable row
// rather than lost on a crashed/failed one-shot RPC. A live stream (asset_key = stream_id) has no artifact row, so
// neither the purge job nor version GC ever reclaims it — this is its only collector.
type StreamCleanupJob struct {
	db          *sql.DB
	cleaner     *artifacts.Cleaner
	logger      logging.Logger
	interval    time.Duration
	batchSize   int
	backoffBase time.Duration
	leaseTTL    time.Duration
	itemTimeout time.Duration
	stopCh      chan struct{}
	wg          sync.WaitGroup
}

// StreamCleanupConfig configures the stream cleanup worker.
type StreamCleanupConfig struct {
	DB          *sql.DB
	Cleaner     *artifacts.Cleaner // required to sweep bytes (nil => no-op drain)
	Logger      logging.Logger
	Interval    time.Duration // how often to drain (default: 1 minute)
	BatchSize   int           // max rows per pass (default: 20, capped to fit the lease)
	BackoffBase time.Duration // per-attempt backoff step, capped (default: 1 minute)
}

// NewStreamCleanupJob creates a new stream cleanup worker.
func NewStreamCleanupJob(cfg StreamCleanupConfig) *StreamCleanupJob {
	interval := cfg.Interval
	if interval == 0 {
		interval = 1 * time.Minute
	}
	backoffBase := cfg.BackoffBase
	if backoffBase <= 0 {
		backoffBase = 1 * time.Minute
	}
	// Budget invariant (mirrors StagingCleanupJob): a claimed batch must PROVABLY finish within its lease so the lease
	// never expires mid-batch (which would let another worker re-claim and issue a duplicate sweep). The claim unit is
	// a SINGLE obligation, so per unit settleObligation runs the local S3 sweep (itemTimeout) THEN bounded settlement
	// writes — the window-gated finalize (control-row delete + mark-cleaned) or the second-sweep reschedule.
	// settleTimeout bounds all of those.
	const settleTimeout = 25 * time.Second // finalize (control-row delete + mark-cleaned) or second-sweep reschedule
	const claimAndMarginBudget = 40 * time.Second
	leaseTTL := 10 * time.Minute
	itemTimeout := 30 * time.Second // one local-store thumbnail-prefix sweep
	maxBatch := int((leaseTTL - claimAndMarginBudget) / (itemTimeout + settleTimeout))
	batchSize := cfg.BatchSize
	if batchSize <= 0 {
		batchSize = 20
	}
	if batchSize > maxBatch {
		batchSize = maxBatch
	}
	return &StreamCleanupJob{
		db:          cfg.DB,
		cleaner:     cfg.Cleaner,
		logger:      cfg.Logger,
		interval:    interval,
		batchSize:   batchSize,
		backoffBase: backoffBase,
		leaseTTL:    leaseTTL,
		itemTimeout: itemTimeout,
		stopCh:      make(chan struct{}),
	}
}

// Start begins the background drain loop.
func (j *StreamCleanupJob) Start() {
	j.wg.Add(1)
	go j.run()
	j.logger.Info("Stream cleanup job started")
}

// Stop gracefully stops the job.
func (j *StreamCleanupJob) Stop() {
	close(j.stopCh)
	j.wg.Wait()
	j.logger.Info("Stream cleanup job stopped")
}

func (j *StreamCleanupJob) run() {
	defer j.wg.Done()
	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()

	j.drain()

	for {
		select {
		case <-ticker.C:
			j.drain()
		case <-j.stopCh:
			return
		}
	}
}

func (j *StreamCleanupJob) drain() {
	if j.db == nil || j.cleaner == nil {
		return
	}
	claimCtx, claimCancel := context.WithTimeout(context.Background(), 15*time.Second)
	items, err := j.claimBatch(claimCtx)
	claimCancel()
	if err != nil {
		j.logger.WithError(err).Warn("Failed to claim stream cleanup batch")
		return
	}

	progressed := 0
	for _, it := range items {
		if j.settleObligation(it) {
			progressed++
		}
	}
	if progressed > 0 {
		j.logger.WithField("count", progressed).Debug("Drained stream cleanup obligations")
	}
}

// streamCleanupItem is one claimed obligation: the deleted asset, its tenant, the recorded backend fingerprint of the
// cell's immutable store its thumbnails live on, and the claim's fencing token.
type streamCleanupItem struct {
	assetKey  string
	tenantID  string
	backendID string
	token     string
}

// claimBatch atomically leases a batch of due, unleased PENDING obligations with a FRESH per-claim token. One Foghorn
// database belongs to ONE cell, so every obligation here is this cell's to sweep — no cross-cell ownership predicate.
// SKIP LOCKED + the lease keep HA replicas from double-sweeping ONE obligation; the token fences its settlement.
func (j *StreamCleanupJob) claimBatch(ctx context.Context) ([]streamCleanupItem, error) {
	rows, err := foghorndb.New(j.db).ClaimStreamCleanupObligations(ctx, foghorndb.ClaimStreamCleanupObligationsParams{
		LeaseSeconds: int64(j.leaseTTL.Seconds()), BatchLimit: int32(j.batchSize),
	})
	if err != nil {
		return nil, err
	}
	var items []streamCleanupItem
	for _, row := range rows {
		items = append(items, streamCleanupItem{assetKey: row.AssetKey, tenantID: row.TenantID,
			backendID: row.BackendID, token: row.LeaseToken.String})
	}
	return items, nil
}

// settleObligation sweeps the deleted asset's thumbnails from THIS cell's local store, then either FINALIZES the
// obligation (its second, post-window sweep: drops the control rows and marks it cleaned in ONE tx) or arms the delayed
// second sweep — all token-fenced on the claim's lease. A failed sweep applies a capped backoff. Idempotent: DeletePrefix
// on an absent prefix is a NotFound no-op; a stolen lease makes the settlement a no-op.
func (j *StreamCleanupJob) settleObligation(it streamCleanupItem) (progressed bool) {
	sweepCtx, cancel := context.WithTimeout(context.Background(), j.itemTimeout)
	defer cancel()

	// Always sweep LOCAL (backendLocal=true): a Foghorn DB owns one immutable store, and its live-stream thumbnails were
	// minted there. REPOINT SAFETY: the Cleaner resolves the recorded backend fingerprint and fails closed
	// (ErrRecordedBackendMismatch) if it is not this cell's current store — we back off rather than sweep the wrong one.
	if sErr := j.cleaner.DeleteThumbnailsOnCluster(sweepCtx, it.tenantID, it.assetKey, "", true, it.backendID); sErr != nil {
		j.recordFailure(it, "s3 sweep: "+sErr.Error())
		return false
	}

	// ATOMIC finalize. The window-gated mark-cleaned and the control-row cleanup run in ONE transaction, so a crash
	// between them can never drop the control rows without marking cleaned (or vice versa) — the FinalizeAtomic guard.
	// The S3 sweep above is idempotent, so a rolled-back tx just re-sweeps next pass.
	windowSecs := int64(control.DeterministicCopyWindow.Seconds())
	txCtx, txCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer txCancel()
	tx, txErr := j.db.BeginTx(txCtx, nil)
	if txErr != nil {
		j.recordFailure(it, "settle tx begin: "+txErr.Error())
		return false
	}
	committed := false
	defer func() {
		if !committed {
			tx.Rollback() //nolint:errcheck // best-effort rollback backstop on any non-commit return
		}
	}()
	// fail rolls the tx back (releasing the row lock) BEFORE the backoff, which runs on a separate connection and would
	// otherwise block on that lock.
	fail := func(msg string) bool {
		tx.Rollback() //nolint:errcheck // best-effort; the deferred backstop also covers it
		j.recordFailure(it, msg)
		return false
	}
	// Asset is the root lock for every thumbnail mutation. Take it before the
	// obligation row below; RecordStreamCleanupObligation takes asset then
	// inserts that row, so reversing the pair here would create a new ABBA.
	if lErr := control.LockThumbnailAssetTx(txCtx, tx, it.assetKey); lErr != nil {
		return fail("lock thumbnail asset: " + lErr.Error())
	}

	// Finalize ONLY at/after the max-copy window (the resurrection-reclaiming SECOND sweep), token-fenced + DB-clock
	// gated. A lost lease or a pre-window row matches 0 rows here.
	n, fErr := foghorndb.New(tx).FinalizeStreamCleanupObligation(txCtx, foghorndb.FinalizeStreamCleanupObligationParams{
		AssetKey: it.assetKey, LeaseToken: sql.NullString{String: it.token, Valid: true}, WindowSeconds: windowSecs,
	})
	if fErr != nil {
		return fail("finalize obligation: " + fErr.Error())
	}

	if n > 0 {
		// Finalized (second sweep done): drop the control rows IN THIS TX so bytes-gone + rows-gone + marked-cleaned
		// commit together.
		if dcErr := control.DeleteThumbnailControlRowsTx(txCtx, tx, it.tenantID, it.assetKey); dcErr != nil {
			return fail("drop control rows: " + dcErr.Error())
		}
	} else {
		// Pre-window (or lease lost): the first sweep is done. Arm the delayed second sweep at enqueued_at + window and
		// release the lease. Token-fenced: a lost lease matches 0 rows here — harmless.
		if rErr := foghorndb.New(tx).ArmStreamCleanupSecondSweep(txCtx, foghorndb.ArmStreamCleanupSecondSweepParams{
			WindowSeconds: windowSecs, AssetKey: it.assetKey,
			LeaseToken: sql.NullString{String: it.token, Valid: true},
		}); rErr != nil {
			return fail("arm second sweep: " + rErr.Error())
		}
	}

	if cErr := tx.Commit(); cErr != nil {
		return fail("commit settlement: " + cErr.Error())
	}
	committed = true
	return true
}

// recordFailure applies a capped-linear backoff and releases the obligation's lease so it is eligible again after it.
// Token-fenced: a stolen lease makes this a no-op. Backoff uses the OLD attempts (Postgres SET reads pre-update
// values), matching backoffBase * min(attempts+1, 30).
func (j *StreamCleanupJob) recordFailure(it streamCleanupItem, msg string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if upErr := foghorndb.New(j.db).FailStreamCleanupObligation(ctx, foghorndb.FailStreamCleanupObligationParams{
		BackoffSeconds: int64(j.backoffBase.Seconds()), LastError: sql.NullString{String: msg, Valid: true},
		AssetKey: it.assetKey, LeaseToken: sql.NullString{String: it.token, Valid: true},
	}); upErr != nil {
		j.logger.WithError(upErr).WithField("asset_key", it.assetKey).Debug("Failed to record stream cleanup retry")
	}
}
