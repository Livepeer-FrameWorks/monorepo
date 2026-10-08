package jobs

import (
	"context"
	"database/sql"
	"math/rand/v2"
	"sync"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
	"frameworks/api_balancing/internal/storage"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/google/uuid"
)

const (
	stagingCleanupDefaultBatch = 500
	// stagingCleanupClaimBuckets partitions the queue by key hash. While a backlog is being drained each worker claims
	// from its own randomly chosen bucket, so the replicas of a cell lock disjoint rows instead of all skipping over the
	// same locked rows.
	stagingCleanupClaimBuckets   = 16
	stagingCleanupBatchesPerPass = 20
	stagingCleanupBatchPause     = 250 * time.Millisecond
	stagingCleanupClaimTimeout   = 15 * time.Second
	stagingCleanupDeleteTimeout  = 2 * time.Minute
	stagingCleanupSettleTimeout  = 15 * time.Second
	stagingCleanupLeaseTTL       = 10 * time.Minute
)

// A claimed batch must finish within its lease, or another worker could re-claim it and send a second delete for the
// same keys while the first is still in flight. A batch is one claim, one multi-object delete request (the batch never
// exceeds one request's key limit), and two settlement statements. The conversion below stops compiling if their
// deadlines add up to more than the lease.
const _ = uint64(stagingCleanupLeaseTTL - stagingCleanupClaimTimeout - stagingCleanupDeleteTimeout - 2*stagingCleanupSettleTimeout)

// StagingCleanupJob drains foghorn.staging_cleanup_queue: it deletes due, durably-enqueued objects from S3 in
// multi-object requests, removing each row on success and applying a capped backoff on failure. The queue holds every
// kind of freeze garbage — staging objects AND superseded/abandoned published CANDIDATE objects (media + co-located
// .dtsh) — enqueued transactionally where the object becomes garbage (completion commit, stale-recovery reset,
// terminal identity-clearing trigger), so a failed/crashed delete is retried from the durable row rather than leaking
// unbilled provider storage. This is the ONLY collector for that garbage.
type StagingCleanupJob struct {
	db             *sql.DB
	s3             StagingObjectDeleter
	logger         logging.Logger
	interval       time.Duration
	batchSize      int
	batchesPerPass int
	batchPause     time.Duration
	backoffBase    time.Duration
	leaseTTL       time.Duration // how long a claimed batch stays leased before another worker may re-claim it
	// localBackendID is this cell's current local backend fingerprint. A queued object is deleted ONLY when its recorded
	// backend_id EXACTLY equals this; empty (unattributed), unwired (empty localBackendID), and mismatched ownership all
	// fail closed (the row is retained), never deleted from a guessed store. A cell's backend is immutable (enforced at
	// startup); fresh enqueues attribute it and legacy rows are adopted once at boot.
	localBackendID string
	stopCh         chan struct{}
	wg             sync.WaitGroup
}

// StagingCleanupConfig configures the staging cleanup worker.
type StagingCleanupConfig struct {
	DB          *sql.DB
	S3          StagingObjectDeleter // required for the worker to do anything (nil => no-op drain)
	Logger      logging.Logger
	Interval    time.Duration // mean time between drain passes (default: 1 minute)
	BatchSize   int           // rows per claim and per S3 request (default 500, at most one request's key limit)
	BackoffBase time.Duration // per-attempt backoff step, capped (default: 1 minute)
	// LocalBackendID is this cell's current local backend fingerprint. Pass main.go's localBackendFingerprint(localS3)
	// so a queued object recorded on a repointed backend fails closed instead of deleting from the wrong store.
	LocalBackendID string
}

// NewStagingCleanupJob creates a new staging cleanup worker.
func NewStagingCleanupJob(cfg StagingCleanupConfig) *StagingCleanupJob {
	interval := cfg.Interval
	if interval <= 0 {
		interval = 1 * time.Minute
	}
	backoffBase := cfg.BackoffBase
	if backoffBase <= 0 {
		backoffBase = 1 * time.Minute
	}
	batchSize := cfg.BatchSize
	if batchSize <= 0 {
		batchSize = stagingCleanupDefaultBatch
	}
	batchSize = min(batchSize, storage.MaxDeleteKeysPerRequest)
	return &StagingCleanupJob{
		db:             cfg.DB,
		s3:             cfg.S3,
		logger:         cfg.Logger,
		interval:       interval,
		batchSize:      batchSize,
		batchesPerPass: stagingCleanupBatchesPerPass,
		batchPause:     stagingCleanupBatchPause,
		backoffBase:    backoffBase,
		leaseTTL:       stagingCleanupLeaseTTL,
		localBackendID: cfg.LocalBackendID,
		stopCh:         make(chan struct{}),
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

// run drains once per jittered interval. Every Foghorn replica of a cell runs this job, and replicas restarted together
// would otherwise drain in lockstep against the same queue; the first pass starts anywhere in the first interval and
// each later pass is scheduled after the previous one finished, so a long pass never queues up ticks behind it.
func (j *StagingCleanupJob) run() {
	defer j.wg.Done()
	timer := time.NewTimer(jitterDuration(j.interval, 0, 1))
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			j.drain()
			timer.Reset(jitterDuration(j.interval, 0.8, 1.2))
		case <-j.stopCh:
			return
		}
	}
}

// jitterDuration returns d scaled by a uniformly random factor in [lo, hi).
func jitterDuration(d time.Duration, lo, hi float64) time.Duration {
	return time.Duration(float64(d) * (lo + rand.Float64()*(hi-lo)))
}

// drain runs one bounded pass. It first claims from any bucket; a short claim means fewer than a batch of rows are
// due, and the pass ends after that one statement. A full claim means a backlog, and the pass moves to a random bucket,
// keeps claiming it while claims come back full and walks to the next bucket when one comes back short, until every
// bucket was visited or the pass has processed batchesPerPass batches. Batches are separated by a jittered pause, so
// a backlog drains at a bounded rate per replica rather than as fast as the database answers.
func (j *StagingCleanupJob) drain() {
	if j.db == nil || j.s3 == nil {
		return
	}
	deleted := 0
	bucket, visited := -1, 0
	for batch := 0; batch < j.batchesPerPass; batch++ {
		if batch > 0 && !j.pauseBetweenBatches() {
			return
		}
		claimed, n, err := j.processBatch(bucket)
		if err != nil {
			j.logger.WithError(err).Warn("Failed to claim staging cleanup batch")
			break
		}
		deleted += n
		if claimed == j.batchSize {
			if bucket < 0 {
				bucket = rand.IntN(stagingCleanupClaimBuckets)
			}
			continue
		}
		if bucket < 0 {
			break
		}
		visited++
		if visited == stagingCleanupClaimBuckets {
			break
		}
		bucket = (bucket + 1) % stagingCleanupClaimBuckets
	}
	if deleted > 0 {
		j.logger.WithField("count", deleted).Debug("Drained staging cleanup queue")
	}
}

// pauseBetweenBatches waits a jittered batchPause and reports false when the job was stopped meanwhile.
func (j *StagingCleanupJob) pauseBetweenBatches() bool {
	if j.batchPause <= 0 {
		return true
	}
	t := time.NewTimer(jitterDuration(j.batchPause, 0.5, 1.5))
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-j.stopCh:
		return false
	}
}

// processBatch leases one batch under a fresh token, deletes the objects this cell owns in one multi-object request,
// and settles the batch in at most two statements: one removing the deleted rows and one releasing the rest with a
// backoff. Both are fenced on the token, so if the lease expired and another worker re-claimed a row (minting a new
// token), this worker's settlement leaves that row to its current owner. bucket < 0 claims from any bucket.
func (j *StagingCleanupJob) processBatch(bucket int) (claimed, deleted int, err error) {
	token := uuid.NewString()
	claimCtx, claimCancel := context.WithTimeout(context.Background(), stagingCleanupClaimTimeout)
	rows, err := foghorndb.New(j.db).ClaimStagingCleanupItems(claimCtx, foghorndb.ClaimStagingCleanupItemsParams{
		LeaseSeconds: int64(j.leaseTTL.Seconds()), LeaseToken: token,
		ClaimBucket: int64(bucket), ClaimBuckets: stagingCleanupClaimBuckets, BatchLimit: int32(j.batchSize),
	})
	claimCancel()
	if err != nil || len(rows) == 0 {
		return 0, 0, err
	}

	owned := make([]string, 0, len(rows))
	var failedKeys, failedErrs []string
	for _, row := range rows {
		// OWNERSHIP GUARD (I2): delete ONLY from this cell's recorded store — the object's recorded backend_id must
		// EXACTLY equal this cell's local fingerprint. Fail closed (backoff + release the lease) otherwise: an EMPTY
		// recorded id (unattributed — fresh enqueues attribute, and legacy rows are adopted at boot), an unwired
		// localBackendID, or a mismatch (a store this cell does not control). Never guess the current store.
		if row.BackendID == "" || j.localBackendID == "" || row.BackendID != j.localBackendID {
			failedKeys = append(failedKeys, row.ObjectKey)
			failedErrs = append(failedErrs, "storage backend repointed (recorded "+row.BackendID+" != current "+j.localBackendID+"); refusing to delete from the wrong store")
			continue
		}
		owned = append(owned, row.ObjectKey)
	}

	var done []string
	if len(owned) > 0 {
		delCtx, delCancel := context.WithTimeout(context.Background(), stagingCleanupDeleteTimeout)
		failed := j.s3.DeleteKeys(delCtx, owned)
		delCancel()
		done = make([]string, 0, len(owned))
		for _, key := range owned {
			if delErr, ok := failed[key]; ok {
				failedKeys = append(failedKeys, key)
				failedErrs = append(failedErrs, delErr.Error())
				continue
			}
			done = append(done, key)
		}
	}

	if len(done) > 0 {
		// A failure here leaves the rows leased; after the lease expires a later pass deletes the already-absent
		// objects again (S3 reports a missing key as deleted) and removes the rows then.
		ctx, cancel := context.WithTimeout(context.Background(), stagingCleanupSettleTimeout)
		n, delErr := foghorndb.New(j.db).DeleteStagingCleanupItems(ctx, foghorndb.DeleteStagingCleanupItemsParams{
			ObjectKeys: done, LeaseToken: token,
		})
		cancel()
		if delErr != nil {
			j.logger.WithError(delErr).WithField("count", len(done)).Warn("Deleted staging objects but failed to drop their queue rows")
		}
		deleted = int(n)
	}
	if len(failedKeys) > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), stagingCleanupSettleTimeout)
		_, failErr := foghorndb.New(j.db).FailStagingCleanupItems(ctx, foghorndb.FailStagingCleanupItemsParams{
			BackoffBaseSeconds: int64(j.backoffBase.Seconds()), ObjectKeys: failedKeys, LastErrors: failedErrs,
			LeaseToken: token,
		})
		cancel()
		if failErr != nil {
			j.logger.WithError(failErr).WithField("count", len(failedKeys)).Warn("Failed to record staging cleanup retries")
		}
	}
	return len(rows), deleted, nil
}
