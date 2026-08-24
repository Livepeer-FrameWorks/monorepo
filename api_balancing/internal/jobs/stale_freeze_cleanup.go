package jobs

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// StagingObjectDeleter deletes a freeze staging object by key. *storage.S3Client satisfies it.
// (Used by StagingCleanupJob, the worker that drains the durable cleanup queue.)
type StagingObjectDeleter interface {
	Delete(ctx context.Context, key string) error
}

// StaleFreezeCleanupJob resets artifacts stuck in freezing state.
type StaleFreezeCleanupJob struct {
	db         *sql.DB
	logger     logging.Logger
	interval   time.Duration
	staleAfter time.Duration
	stopCh     chan struct{}
	wg         sync.WaitGroup
}

// StaleFreezeCleanupConfig holds configuration for the cleanup job.
type StaleFreezeCleanupConfig struct {
	DB         *sql.DB
	Logger     logging.Logger
	Interval   time.Duration // How often to run (default: 1 minute)
	StaleAfter time.Duration // Reset freezing artifacts older than this (default: 30 minutes)
}

// NewStaleFreezeCleanupJob creates a new stale freeze cleanup job.
func NewStaleFreezeCleanupJob(cfg StaleFreezeCleanupConfig) *StaleFreezeCleanupJob {
	interval := cfg.Interval
	if interval == 0 {
		interval = 1 * time.Minute
	}
	staleAfter := cfg.StaleAfter
	if staleAfter == 0 {
		staleAfter = 30 * time.Minute
	}
	return &StaleFreezeCleanupJob{
		db:         cfg.DB,
		logger:     cfg.Logger,
		interval:   interval,
		staleAfter: staleAfter,
		stopCh:     make(chan struct{}),
	}
}

// Start begins the background cleanup loop.
func (j *StaleFreezeCleanupJob) Start() {
	j.wg.Add(1)
	go j.run()
	j.logger.Info("Stale freeze cleanup job started")
}

// Stop gracefully stops the job.
func (j *StaleFreezeCleanupJob) Stop() {
	close(j.stopCh)
	j.wg.Wait()
	j.logger.Info("Stale freeze cleanup job stopped")
}

func (j *StaleFreezeCleanupJob) run() {
	defer j.wg.Done()
	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()

	j.cleanup()

	for {
		select {
		case <-ticker.C:
			j.cleanup()
		case <-j.stopCh:
			return
		}
	}
}

func (j *StaleFreezeCleanupJob) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	staleAfterSeconds := int64(j.staleAfter.Seconds())
	if staleAfterSeconds <= 0 {
		staleAfterSeconds = 1
	}

	// The reset AND the durable enqueue of each abandoned attempt's staging object commit as ONE transaction,
	// so recovery is atomic: a crash can never reset the row without also scheduling its staging cleanup.
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		j.logger.WithError(err).Warn("Failed to begin stale freeze recovery tx")
		return
	}
	defer tx.Rollback() //nolint:errcheck // best-effort on the non-commit paths

	// Recover FREEZE attempts whose completion never arrived (node crashed mid-sync, dropped stream). A
	// freeze attempt is uniquely identified by storage_location='freezing' AND a complete attempt identity
	// (request + node) — the state ClaimFreezeAttempt sets. This is scoped narrowly ON PURPOSE: a
	// multipart VOD ingest (CreateVodUpload) is ALSO sync_status='in_progress' but with
	// storage_location='pending' and NO freeze identity, and has its own upload-expiry recovery — this job
	// must never rewrite a live multipart upload to failed/local. Timeout keyed on last_sync_attempt (when
	// the attempt was dispatched), not updated_at, so unrelated row touches don't reset the clock. Clearing
	// the identity ensures a late completion from the abandoned attempt is rejected by the completion guard.
	// Capture the attempt's canonical key + attempt id in a CTE BEFORE the UPDATE clears sync_request_id —
	// a plain RETURNING would observe the already-NULLed value (PostgreSQL RETURNING sees the NEW row), so
	// the staging enqueue below would enqueue nothing. The CTE's SELECT ... FOR UPDATE snapshots the old
	// identity; the UPDATE joins it and RETURNS the OLD attempt id so we can enqueue that attempt's staging
	// object for durable deletion.
	rowsRes, err := foghorndb.New(tx).ResetStaleFreezeAttempts(ctx, staleAfterSeconds)
	if err != nil {
		j.logger.WithError(err).Warn("Failed to reset stale in-progress sync attempts")
		return
	}

	type staleAttempt struct{ canonicalKey, attemptID string }
	var stale []staleAttempt
	for _, row := range rowsRes {
		stale = append(stale, staleAttempt{row.CanonicalKey, row.AttemptID})
	}

	// Durably enqueue each abandoned attempt's STAGING object AND its published CANDIDATE (+ the candidate's
	// co-located .dtsh) for deletion, on THIS transaction. The candidate is what a completion promotes to
	// OUTSIDE its transaction, so a promote that succeeded but whose completion then failed/rolled back leaves
	// that candidate orphaned; it is deterministic from the persisted (sync_object_key, attempt id), so it is
	// derived and collected here. Safe because recovery CLEARS the attempt identity, so no future attempt
	// reuses this attempt id / candidate key. The StagingCleanupJob drains with retries.
	for _, a := range stale {
		if a.canonicalKey == "" || a.attemptID == "" {
			continue
		}
		for _, key := range []string{
			control.FreezeStagingKey(a.canonicalKey, a.attemptID),         // main staging
			control.FreezeStagingKey(a.canonicalKey+".dtsh", a.attemptID), // .dtsh staging
			control.FreezePublishKey(a.canonicalKey, a.attemptID),         // published media candidate
			control.FreezePublishDtshKey(a.canonicalKey, a.attemptID),     // published .dtsh candidate
		} {
			if eErr := control.EnqueueStagingCleanupTx(ctx, tx, key); eErr != nil {
				j.logger.WithError(eErr).WithField("attempt_id", a.attemptID).Warn("Failed to enqueue abandoned freeze cleanup")
				return
			}
		}
	}

	if cErr := tx.Commit(); cErr != nil {
		j.logger.WithError(cErr).Warn("Failed to commit stale freeze recovery")
		return
	}
	if len(stale) > 0 {
		j.logger.WithField("count", len(stale)).Warn("Recovered stale in-progress sync attempts for retry")
	}
}
