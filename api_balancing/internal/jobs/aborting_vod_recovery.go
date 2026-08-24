package jobs

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithy "github.com/aws/smithy-go"

	"frameworks/api_balancing/internal/artifactoutbox"
	"frameworks/api_balancing/internal/artifacts"
	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// AbortingVodRecoveryS3 is the minimal S3 surface the abort-recovery scan needs: re-run the multipart
// abort idempotently. *storage.S3Client satisfies it.
type AbortingVodRecoveryS3 interface {
	AbortMultipartUpload(ctx context.Context, key, uploadID string) error
}

// AbortingVodRecoveryJob is a SERVICE-OWNED reconciliation for VOD aborts stranded in the transient
// 'aborting' state. AbortVodUpload claims 'uploading'->'aborting' with a guarded UPDATE BEFORE calling
// S3 AbortMultipartUpload, then destroys the multipart upload and transitions 'aborting'->'deleted'
// (deleting vod_metadata + emitting the DELETED lifecycle event) in one transaction. A crash anywhere
// from the claim through that transition leaves a durable 'aborting' row this job converges without the
// client retrying: it RE-RUNS AbortMultipartUpload (idempotent — a NoSuchUpload/already-aborted result
// is success) and then advances 'aborting'->'deleted'. Every write is guarded on status='aborting' and
// tenant-scoped, so it is idempotent on every replica and against a concurrent client abort finishing
// the same row.
type AbortingVodRecoveryJob struct {
	db             *sql.DB
	s3             AbortingVodRecoveryS3
	logger         logging.Logger
	interval       time.Duration
	staleAfter     time.Duration // re-run the abort for rows whose last attempt is older than this
	batchSize      int
	localBackendID string // this cell's local backend fingerprint; the abort re-run is fenced on recorded == local
	stopCh         chan struct{}
	wg             sync.WaitGroup
}

// AbortingVodRecoveryConfig configures the recovery scan.
type AbortingVodRecoveryConfig struct {
	DB             *sql.DB
	S3             AbortingVodRecoveryS3
	Logger         logging.Logger
	Interval       time.Duration // How often to run (default: 2 minutes)
	StaleAfter     time.Duration // Re-run 'aborting' rows older than this (default: 5 minutes)
	BatchSize      int           // Max rows per pass (default: 50)
	LocalBackendID string        // this cell's local backend fingerprint; the abort re-run is fenced on recorded == local
}

// NewAbortingVodRecoveryJob builds the recovery scan with defaulted thresholds. A nil S3 client yields
// a job whose passes are no-ops (the abort can't be re-run), so callers without S3 configured can still
// register it uniformly.
func NewAbortingVodRecoveryJob(cfg AbortingVodRecoveryConfig) *AbortingVodRecoveryJob {
	interval := cfg.Interval
	if interval == 0 {
		interval = 2 * time.Minute
	}
	staleAfter := cfg.StaleAfter
	if staleAfter == 0 {
		staleAfter = 5 * time.Minute
	}
	batchSize := cfg.BatchSize
	if batchSize <= 0 {
		batchSize = 50
	}
	return &AbortingVodRecoveryJob{
		db:             cfg.DB,
		s3:             cfg.S3,
		logger:         cfg.Logger,
		interval:       interval,
		staleAfter:     staleAfter,
		batchSize:      batchSize,
		localBackendID: cfg.LocalBackendID,
		stopCh:         make(chan struct{}),
	}
}

// Start begins the background reconciliation loop.
func (j *AbortingVodRecoveryJob) Start() {
	j.wg.Add(1)
	go j.run()
	j.logger.Info("Aborting-VOD recovery job started")
}

// Stop gracefully stops the job.
func (j *AbortingVodRecoveryJob) Stop() {
	close(j.stopCh)
	j.wg.Wait()
	j.logger.Info("Aborting-VOD recovery job stopped")
}

func (j *AbortingVodRecoveryJob) run() {
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

type abortingVodRow struct {
	artifactHash string
	tenantID     string
	userID       string
	s3Key        string
	uploadID     string
	backendID    string // recorded backend owner; the abort re-run is fenced on backendID == j.localBackendID
}

func (j *AbortingVodRecoveryJob) reconcile() {
	if j.db == nil || j.s3 == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	staleSeconds := int64(j.staleAfter.Seconds())
	if staleSeconds <= 0 {
		staleSeconds = 1
	}

	rows, err := foghorndb.New(j.db).ListStaleAbortingVODs(ctx, foghorndb.ListStaleAbortingVODsParams{
		StaleSeconds: staleSeconds, BatchLimit: int32(j.batchSize),
	})
	if err != nil {
		j.logger.WithError(err).Warn("Aborting-VOD recovery: failed to scan stranded aborts")
		return
	}
	var batch []abortingVodRow
	for _, row := range rows {
		batch = append(batch, abortingVodRow{
			artifactHash: row.ArtifactHash, tenantID: row.TenantID, userID: row.UserID,
			s3Key: row.S3Key, uploadID: row.UploadID, backendID: row.BackendID,
		})
	}

	for _, r := range batch {
		select {
		case <-j.stopCh:
			return
		default:
		}
		j.reconcileOne(ctx, r)
	}
}

func (j *AbortingVodRecoveryJob) reconcileOne(ctx context.Context, r abortingVodRow) {
	// FENCE FIRST, before ANY S3 call: only re-run the abort for a multipart this cell owns (recorded backend ==
	// local). A foreign/unattributed row is left 'aborting' untouched — no AbortMultipartUpload, no state change — so
	// this cell never tears down an upload on a store it must not write.
	if ownErr := artifacts.VerifyLocalMultipartOwnership(r.backendID, j.localBackendID); ownErr != nil {
		j.logger.WithError(ownErr).WithField("artifact_hash", r.artifactHash).Warn("Aborting-VOD recovery: ownership fence — leaving row untouched (zero S3 calls)")
		return
	}
	// Re-run AbortMultipartUpload idempotently. A NoSuchUpload/already-aborted result means a prior
	// attempt already tore the multipart upload down — that is success, converge. Any other error leaves
	// the row 'aborting' for a later pass; do NOT delete metadata or emit DELETED while the multipart
	// upload may still exist.
	if r.s3Key != "" && r.uploadID != "" {
		if abortErr := j.s3.AbortMultipartUpload(ctx, r.s3Key, r.uploadID); abortErr != nil && !isNoSuchUploadError(abortErr) {
			j.logger.WithError(abortErr).WithFields(logging.Fields{
				"artifact_hash": r.artifactHash,
				"upload_id":     r.uploadID,
				"s3_key":        r.s3Key,
			}).Warn("Aborting-VOD recovery: re-run of AbortMultipartUpload errored; leaving 'aborting' for a later pass")
			return
		}
	}
	j.convergeToDeleted(ctx, r)
}

// convergeToDeleted advances a stranded 'aborting' row to 'deleted', deletes its vod_metadata, and emits
// the DELETED lifecycle event in ONE transaction — the same atomic shape as AbortVodUpload. Guarded on
// status='aborting' and tenant-scoped, so a re-run (or a concurrent client abort finishing the row) that
// finds the row already moved emits no duplicate DELETED event.
func (j *AbortingVodRecoveryJob) convergeToDeleted(ctx context.Context, r abortingVodRow) {
	moved := false
	err := database.WithRetryablePostgresTx(ctx, j.db, nil, func(tx *sql.Tx) error {
		affected, execErr := foghorndb.New(tx).MarkAbortingVODDeleted(ctx, foghorndb.MarkAbortingVODDeletedParams{
			ArtifactHash: r.artifactHash, TenantID: r.tenantID,
		})
		if execErr != nil {
			return execErr
		}
		if affected == 0 {
			return nil // row left 'aborting' under us (concurrent client abort finished it) — emit no event
		}
		if execErr := foghorndb.New(tx).DeleteVODMetadata(ctx, r.artifactHash); execErr != nil {
			return execErr
		}
		data := &ipcpb.VodLifecycleData{
			Status:      ipcpb.VodLifecycleData_STATUS_DELETED,
			VodHash:     r.artifactHash,
			TenantId:    &r.tenantID,
			CompletedAt: timePtrUnix(),
		}
		if r.uploadID != "" {
			data.UploadId = &r.uploadID
		}
		if r.userID != "" {
			data.UserId = &r.userID
		}
		moved = true
		return artifactoutbox.EnqueueVodLifecycleTx(ctx, tx, data)
	})
	if err != nil {
		j.logger.WithError(err).WithField("artifact_hash", r.artifactHash).Warn("Aborting-VOD recovery: convergence to deleted failed; row stays 'aborting' for a later pass")
		return
	}
	if moved {
		j.logger.WithFields(logging.Fields{
			"artifact_hash": r.artifactHash,
			"s3_key":        r.s3Key,
		}).Info("Aborting-VOD recovery: multipart upload torn down, converged stranded abort to deleted")
	}
}

// timePtrUnix returns a pointer to the current Unix timestamp for the DELETED lifecycle event.
func timePtrUnix() *int64 {
	t := time.Now().Unix()
	return &t
}

// isNoSuchUploadError reports whether an S3 AbortMultipartUpload error is a genuine, SDK-TYPED
// "the multipart upload id no longer exists" signal — a prior attempt already aborted (or completed) it,
// so re-running the abort is a successful idempotent no-op. Classification is by TYPE, never by
// substring: aws-sdk-go-v2 wraps the API error with %w, so a real NoSuchUpload is an *types.NoSuchUpload
// (which also satisfies smithy.APIError with code "NoSuchUpload"). An unrelated provider error
// (AccessDenied, NoSuchBucket, ...) whose message merely contains "does not exist" is NOT a gone upload;
// treating it as gone would converge the abort to 'deleted' WITHOUT proving the multipart is torn down.
// Only a typed NoSuchUpload (or a clean re-abort) may converge; any other error leaves the row 'aborting'.
func isNoSuchUploadError(err error) bool {
	if err == nil {
		return false
	}
	var noSuch *types.NoSuchUpload
	if errors.As(err, &noSuch) {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == "NoSuchUpload"
	}
	return false
}
