package jobs

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// StagingCleanupJob drains foghorn.staging_cleanup_queue: it deletes each durably-enqueued object from S3,
// removing the row on success and applying a capped backoff on failure. The queue holds every kind of freeze
// garbage — staging objects AND superseded/abandoned published CANDIDATE objects (media + co-located .dtsh) —
// enqueued transactionally where the object becomes garbage (completion commit, stale-recovery reset, terminal
// identity-clearing trigger), so a failed/crashed delete is retried from the durable row rather than leaking
// unbilled provider storage. This is the ONLY collector for that garbage.
type StagingCleanupJob struct {
	db          *sql.DB
	s3          StagingObjectDeleter
	logger      logging.Logger
	interval    time.Duration
	batchSize   int
	backoffBase time.Duration
	leaseTTL    time.Duration // how long a claimed batch stays leased before another worker may re-claim it
	itemTimeout time.Duration // per-item S3 delete deadline
	stopCh      chan struct{}
	wg          sync.WaitGroup
}

// StagingCleanupConfig configures the staging cleanup worker.
type StagingCleanupConfig struct {
	DB          *sql.DB
	S3          StagingObjectDeleter // required for the worker to do anything (nil => no-op drain)
	Logger      logging.Logger
	Interval    time.Duration // how often to drain (default: 1 minute)
	BatchSize   int           // max rows per pass (default: 100)
	BackoffBase time.Duration // per-attempt backoff step, capped (default: 1 minute)
}

// NewStagingCleanupJob creates a new staging cleanup worker.
func NewStagingCleanupJob(cfg StagingCleanupConfig) *StagingCleanupJob {
	interval := cfg.Interval
	if interval == 0 {
		interval = 1 * time.Minute
	}
	backoffBase := cfg.BackoffBase
	if backoffBase <= 0 {
		backoffBase = 1 * time.Minute
	}
	// Budget invariant: a claimed batch must PROVABLY finish within its lease, so the lease never expires
	// mid-batch (which would let another worker re-claim and issue a duplicate S3 delete). The worst-case
	// per-item wall-clock is the S3 delete deadline PLUS the settlement (row-delete / failure-update) deadline,
	// and the batch also pays a one-time claim + margin. So: leaseTTL >= claimBudget + batchSize*(itemTimeout +
	// settleTimeout). With leaseTTL=10m, itemTimeout=15s, settleTimeout=10s, claim+margin≈30s: 40s claim/margin
	// + 20*(25s) = 530s < 600s.
	const settleTimeout = 10 * time.Second
	const claimAndMarginBudget = 40 * time.Second
	leaseTTL := 10 * time.Minute
	itemTimeout := 15 * time.Second
	maxBatch := int((leaseTTL - claimAndMarginBudget) / (itemTimeout + settleTimeout)) // 22
	batchSize := cfg.BatchSize
	if batchSize <= 0 {
		batchSize = 20
	}
	if batchSize > maxBatch {
		batchSize = maxBatch
	}
	return &StagingCleanupJob{
		db:          cfg.DB,
		s3:          cfg.S3,
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
func (j *StagingCleanupJob) Start() {
	j.wg.Add(1)
	go j.run()
	j.logger.Info("Staging cleanup job started")
}

// Stop gracefully stops the job.
func (j *StagingCleanupJob) Stop() {
	close(j.stopCh)
	j.wg.Wait()
	j.logger.Info("Staging cleanup job stopped")
}

func (j *StagingCleanupJob) run() {
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

func (j *StagingCleanupJob) drain() {
	if j.db == nil || j.s3 == nil {
		return
	}
	// Atomically LEASE a batch of due, unleased rows. The lease (+ FOR UPDATE SKIP LOCKED) means HA replicas
	// never claim the same keys, so a delete is not issued to S3 twice concurrently.
	claimCtx, claimCancel := context.WithTimeout(context.Background(), 15*time.Second)
	items, err := j.claimBatch(claimCtx)
	claimCancel()
	if err != nil {
		j.logger.WithError(err).Warn("Failed to claim staging cleanup batch")
		return
	}

	deleted := 0
	for _, it := range items {
		// Each item gets its OWN fresh, bounded context: one slow/stuck delete cannot starve the rest of the
		// batch (head-of-line), and the failure-record UPDATE never runs on an already-cancelled context.
		if j.settleOne(it) {
			deleted++
		}
	}
	if deleted > 0 {
		j.logger.WithField("count", deleted).Debug("Drained staging cleanup queue")
	}
}

type stagingCleanupItem struct {
	key      string
	attempts int
	token    string // the lease token this claim minted; every settlement CASes on it
}

// claimBatch atomically leases a batch of due, unleased rows with a FRESH per-claim token and returns them.
// SKIP LOCKED lets concurrent workers/replicas make progress on disjoint rows without blocking or
// double-claiming. The token fences settlement: a worker whose lease later expires and is re-claimed by
// another worker cannot settle the row (its stale token no longer matches). The batch is sized so it fits
// within the lease (batchSize * itemTimeout <= leaseTTL, enforced in the constructor).
func (j *StagingCleanupJob) claimBatch(ctx context.Context) ([]stagingCleanupItem, error) {
	rows, err := j.db.QueryContext(ctx, `
		UPDATE foghorn.staging_cleanup_queue q
		SET leased_until = NOW() + ($2 * INTERVAL '1 second'),
		    lease_token = gen_random_uuid()::text
		WHERE q.object_key IN (
			SELECT object_key FROM foghorn.staging_cleanup_queue
			WHERE next_attempt_at <= NOW()
			  AND (leased_until IS NULL OR leased_until <= NOW())
			ORDER BY next_attempt_at
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING q.object_key, q.attempts, q.lease_token`, j.batchSize, int64(j.leaseTTL.Seconds()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []stagingCleanupItem
	for rows.Next() {
		var it stagingCleanupItem
		if scanErr := rows.Scan(&it.key, &it.attempts, &it.token); scanErr != nil {
			return nil, scanErr
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

// settleOne deletes one leased staging object and reconciles the queue row, each on its OWN bounded context.
// Every settlement is FENCED on the lease token: if the lease expired and another worker re-claimed the row
// (fresh token), this worker's UPDATE/DELETE matches zero rows and is a harmless no-op — no cross-worker
// clobber, and the current owner settles it.
func (j *StagingCleanupJob) settleOne(it stagingCleanupItem) (deleted bool) {
	delCtx, cancel := context.WithTimeout(context.Background(), j.itemTimeout)
	defer cancel()
	if delErr := j.s3.Delete(delCtx, it.key); delErr != nil {
		// Capped-linear backoff so a persistently-failing key doesn't spin: base * min(attempts+1, 30). Release
		// the lease so the row is eligible again after the backoff. Fresh context (delCtx may be spent).
		backoff := j.backoffBase * time.Duration(min(it.attempts+1, 30))
		upCtx, upCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer upCancel()
		if _, upErr := j.db.ExecContext(upCtx, `
			UPDATE foghorn.staging_cleanup_queue
			SET attempts = attempts + 1,
			    next_attempt_at = NOW() + ($2 * INTERVAL '1 second'),
			    leased_until = NULL,
			    lease_token = NULL,
			    last_error = $3
			WHERE object_key = $1 AND lease_token = $4`, it.key, int64(backoff.Seconds()), delErr.Error(), it.token); upErr != nil {
			j.logger.WithError(upErr).WithField("object_key", it.key).Debug("Failed to record staging cleanup retry")
		}
		return false
	}
	// Success: drop the queue row (token-fenced) on a fresh context. If this fails/no-ops the object is already
	// gone, so a later pass Deletes an already-absent key (idempotent) and removes the row then.
	rowCtx, rowCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer rowCancel()
	res, delRowErr := j.db.ExecContext(rowCtx, `DELETE FROM foghorn.staging_cleanup_queue WHERE object_key = $1 AND lease_token = $2`, it.key, it.token)
	if delRowErr != nil {
		j.logger.WithError(delRowErr).WithField("object_key", it.key).Debug("Deleted staging object but failed to drop queue row")
		return false
	}
	if n, raErr := res.RowsAffected(); raErr == nil && n == 0 {
		return false // lease was stolen (token mismatch) — the current owner will settle it
	}
	return true
}
