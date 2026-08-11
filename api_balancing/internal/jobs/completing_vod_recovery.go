package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"frameworks/api_balancing/internal/artifactoutbox"
	"frameworks/api_balancing/internal/artifacts"
	"frameworks/api_balancing/internal/storage"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// VodCompletionDescriptor is the durable S3 CompleteMultipartUpload input persisted on
// foghorn.vod_metadata.vod_completion_descriptor ATOMICALLY with the 'uploading'->'completing' claim.
// It carries the object key, the multipart upload id, and the ordered part list so the recovery job
// can RETRY CompleteMultipartUpload after a crash that landed before the client's completion call.
type VodCompletionDescriptor struct {
	S3Key    string              `json:"s3_key"`
	UploadID string              `json:"upload_id"`
	Parts    []VodCompletionPart `json:"parts"`
}

// VodCompletionPart is one entry in the ordered multipart part list.
type VodCompletionPart struct {
	PartNumber int32  `json:"part_number"`
	ETag       string `json:"etag"`
}

// CompletingVodRecoveryS3 is the minimal S3 surface the recovery scan needs: retry the multipart
// completion, probe object existence, and build the object URL for the lifecycle event.
// *storage.S3Client satisfies it.
type CompletingVodRecoveryS3 interface {
	CompleteMultipartUpload(ctx context.Context, key, uploadID string, parts []storage.CompletedPart) error
	Exists(ctx context.Context, key string) (bool, error)
	BuildS3URL(key string) string
}

// CompletingVodRecoveryJob is a SERVICE-OWNED reconciliation for VOD uploads stranded in the
// transient 'completing' state. CompleteVodUpload persists 'completing' — together with the full
// multipart completion descriptor — BEFORE calling S3 CompleteMultipartUpload, so a crash anywhere
// from the claim through the durable transition leaves a row this job can converge without the client
// retrying. Using the persisted descriptor it RETRIES CompleteMultipartUpload (idempotent) and:
//
//   - completion succeeds, or the object is already present -> advance to 'processing' (+ PROCESSING
//     lifecycle event) and dispatch the processing job keyed to the persisted processes_json
//     (idempotent);
//   - the multipart upload is gone AND no object landed, past the grace period -> mark 'failed'
//     (+ FAILED lifecycle event);
//   - object absent within the grace period, or the completion/Exists probe is inconclusive -> leave
//     'completing' for a later pass.
//
// It never default-processes and never marks a row failed while a valid multipart upload or a
// completed object still exists. Every write is guarded on status='completing' and tenant-scoped, so
// it is idempotent and safe on every replica and against a concurrent client completion.
type CompletingVodRecoveryJob struct {
	db         *sql.DB
	s3         CompletingVodRecoveryS3
	logger     logging.Logger
	interval   time.Duration
	staleAfter time.Duration // probe rows whose last attempt is older than this
	failAfter  time.Duration // only mark 'failed' (object absent) once older than this grace
	batchSize  int
	// localBackendID is this cell's local S3 backend fingerprint (control.BackendFingerprint). Recovery does NOT write
	// backend_id (it is set at upload creation); this is the value reconcileOne fences on — a row is reconciled only
	// when its recorded backend_id EXACTLY equals this, so a foreign/unattributed row is left untouched.
	localBackendID string
	stopCh         chan struct{}
	wg             sync.WaitGroup
}

// CompletingVodRecoveryConfig configures the recovery scan.
type CompletingVodRecoveryConfig struct {
	DB         *sql.DB
	S3         CompletingVodRecoveryS3
	Logger     logging.Logger
	Interval   time.Duration // How often to run (default: 2 minutes)
	StaleAfter time.Duration // Probe 'completing' rows older than this (default: 5 minutes)
	FailAfter  time.Duration // Mark absent objects failed only past this grace (default: 1 hour)
	BatchSize  int           // Max rows per pass (default: 50)
	// LocalBackendID is this cell's local S3 backend fingerprint, used to FENCE reconciliation on recorded ownership
	// (recorded backend_id == local). Pass main.go's localBackendFingerprint(localS3). Empty fails every row closed.
	LocalBackendID string
}

// NewCompletingVodRecoveryJob builds the recovery scan with defaulted thresholds. A nil S3 client
// yields a job whose passes are no-ops (existence can't be probed), so callers without S3 configured
// can still register it uniformly.
func NewCompletingVodRecoveryJob(cfg CompletingVodRecoveryConfig) *CompletingVodRecoveryJob {
	interval := cfg.Interval
	if interval == 0 {
		interval = 2 * time.Minute
	}
	staleAfter := cfg.StaleAfter
	if staleAfter == 0 {
		staleAfter = 5 * time.Minute
	}
	failAfter := cfg.FailAfter
	if failAfter == 0 {
		failAfter = 1 * time.Hour
	}
	if failAfter < staleAfter {
		failAfter = staleAfter
	}
	batchSize := cfg.BatchSize
	if batchSize <= 0 {
		batchSize = 50
	}
	return &CompletingVodRecoveryJob{
		db:             cfg.DB,
		s3:             cfg.S3,
		logger:         cfg.Logger,
		interval:       interval,
		staleAfter:     staleAfter,
		failAfter:      failAfter,
		batchSize:      batchSize,
		localBackendID: cfg.LocalBackendID,
		stopCh:         make(chan struct{}),
	}
}

// Start begins the background reconciliation loop.
func (j *CompletingVodRecoveryJob) Start() {
	j.wg.Add(1)
	go j.run()
	j.logger.Info("Completing-VOD recovery job started")
}

// Stop gracefully stops the job.
func (j *CompletingVodRecoveryJob) Stop() {
	close(j.stopCh)
	j.wg.Wait()
	j.logger.Info("Completing-VOD recovery job stopped")
}

func (j *CompletingVodRecoveryJob) run() {
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

type completingVodRow struct {
	artifactHash  string
	tenantID      string
	userID        string
	sizeBytes     int64
	s3Key         string
	uploadID      string
	processesJSON string
	backendID     string // backend identity recorded when the upload was CREATED (invariant I2); verified, never reconstructed
	descriptor    VodCompletionDescriptor
	hasDescriptor bool
	pastFailGrace bool
}

func (j *CompletingVodRecoveryJob) reconcile() {
	if j.db == nil || j.s3 == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	staleSeconds := int64(j.staleAfter.Seconds())
	if staleSeconds <= 0 {
		staleSeconds = 1
	}
	failSeconds := int64(j.failAfter.Seconds())
	if failSeconds < staleSeconds {
		failSeconds = staleSeconds
	}

	rows, err := j.db.QueryContext(ctx, `
		SELECT a.artifact_hash,
		       a.tenant_id::text,
		       COALESCE(a.user_id::text, ''),
		       COALESCE(a.size_bytes, 0),
		       COALESCE(v.s3_key, ''),
		       COALESCE(v.s3_upload_id, ''),
		       COALESCE(v.processes_json, ''),
		       COALESCE(a.backend_id, ''),
		       COALESCE(v.vod_completion_descriptor::text, ''),
		       (COALESCE(a.last_sync_attempt, a.updated_at) < NOW() - ($2 * INTERVAL '1 second')) AS past_fail_grace
		FROM foghorn.artifacts a
		JOIN foghorn.vod_metadata v ON v.artifact_hash = a.artifact_hash
		WHERE a.artifact_type = 'vod'
		  AND a.status = 'completing'
		  AND COALESCE(v.s3_key, '') <> ''
		  AND COALESCE(a.last_sync_attempt, a.updated_at) < NOW() - ($1 * INTERVAL '1 second')
		ORDER BY COALESCE(a.last_sync_attempt, a.updated_at)
		LIMIT $3
	`, staleSeconds, failSeconds, j.batchSize)
	if err != nil {
		j.logger.WithError(err).Warn("Completing-VOD recovery: failed to scan stranded uploads")
		return
	}
	var batch []completingVodRow
	for rows.Next() {
		var r completingVodRow
		var descriptorJSON string
		if scanErr := rows.Scan(&r.artifactHash, &r.tenantID, &r.userID, &r.sizeBytes, &r.s3Key, &r.uploadID, &r.processesJSON, &r.backendID, &descriptorJSON, &r.pastFailGrace); scanErr != nil {
			j.logger.WithError(scanErr).Warn("Completing-VOD recovery: row scan failed")
			continue
		}
		if descriptorJSON != "" {
			if unmErr := json.Unmarshal([]byte(descriptorJSON), &r.descriptor); unmErr != nil {
				j.logger.WithError(unmErr).WithField("artifact_hash", r.artifactHash).Warn("Completing-VOD recovery: undecodable completion descriptor; falling back to existence probe")
			} else if r.descriptor.UploadID != "" && len(r.descriptor.Parts) > 0 {
				r.hasDescriptor = true
			}
		}
		batch = append(batch, r)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		j.logger.WithError(rowsErr).Warn("Completing-VOD recovery: row iteration failed")
	}
	// Close the cursor before opening the per-row txns below (single-connection safety).
	rows.Close() //nolint:sqlclosecheck // fully drained into `batch` above; closed here before per-row txns

	for _, r := range batch {
		select {
		case <-j.stopCh:
			return
		default:
		}
		j.reconcileOne(ctx, r)
	}
}

func (j *CompletingVodRecoveryJob) reconcileOne(ctx context.Context, r completingVodRow) {
	// FENCE FIRST, before ANY S3 call: only reconcile a multipart this cell owns (recorded backend == local). A
	// foreign or unattributed row is left untouched — no CompleteMultipartUpload, no Exists probe, no state change —
	// so recovery never completes an upload on a store this cell must not write (which cleanup would then refuse to
	// delete, leaking it). Pre-cut rows are adopted from the proven cell identity at boot, so a persistent mismatch
	// here is genuinely foreign.
	if ownErr := artifacts.VerifyLocalMultipartOwnership(r.backendID, j.localBackendID); ownErr != nil {
		j.logger.WithError(ownErr).WithField("artifact_hash", r.artifactHash).Warn("Completing-VOD recovery: ownership fence — leaving row untouched (zero S3 calls)")
		return
	}

	// With a persisted completion descriptor, RETRY CompleteMultipartUpload first: a crash BEFORE the
	// client's original completion call leaves a valid multipart upload that only this retry can converge
	// (existence-probing alone would eventually FAIL it even though it was completable). S3 completion is
	// idempotent — a NoSuchUpload/already-completed error just means a prior attempt already finished it,
	// which we resolve by probing the object below.
	if r.hasDescriptor {
		parts := make([]storage.CompletedPart, len(r.descriptor.Parts))
		for i, p := range r.descriptor.Parts {
			parts[i] = storage.CompletedPart{PartNumber: int(p.PartNumber), ETag: p.ETag}
		}
		completeErr := j.s3.CompleteMultipartUpload(ctx, r.s3Key, r.descriptor.UploadID, parts)
		if completeErr == nil {
			// Completion succeeded (or was already done); the object is now present.
			j.convergeToProcessing(ctx, r)
			return
		}
		j.logger.WithError(completeErr).WithFields(logging.Fields{
			"artifact_hash": r.artifactHash,
			"upload_id":     r.descriptor.UploadID,
			"s3_key":        r.s3Key,
		}).Info("Completing-VOD recovery: retried CompleteMultipartUpload errored; probing object to decide")
	}

	// Completion errored or no descriptor exists: probe the object to decide. Present -> a prior attempt
	// already completed it, converge. Absent -> only fail past the grace period (S3 may still be
	// finalizing, or the multipart upload may still be valid for a later retry).
	exists, existsErr := j.s3.Exists(ctx, r.s3Key)
	if existsErr != nil {
		// Can't decide — leave 'completing' for the next pass.
		j.logger.WithError(existsErr).WithFields(logging.Fields{
			"artifact_hash": r.artifactHash,
			"s3_key":        r.s3Key,
		}).Warn("Completing-VOD recovery: object-existence probe failed; leaving 'completing'")
		return
	}

	if exists {
		j.convergeToProcessing(ctx, r)
		return
	}

	// Object absent. Only mark failed once past the grace period; otherwise S3 may still be finalizing.
	if !r.pastFailGrace {
		return
	}
	j.convergeToFailed(ctx, r)
}

// convergeToProcessing advances a stranded 'completing' row to 'processing', inserts its processing
// job, and emits the PROCESSING lifecycle event in ONE transaction — the same atomic shape as
// CompleteVodUpload. If any step fails the whole tx rolls back and the row stays 'completing' (still
// re-scanned next pass), so the row is never left 'processing' with no job. The processing job is
// keyed to the SAME requested outputs the client asked for via the persisted vod_metadata.processes_json
// (falling back to default processing only when no spec was persisted). InsertProcessingJobWithSourceParamsTx
// dedups to any job already inserted by a concurrent client completion, so a re-run converges without
// creating a duplicate.
func (j *CompletingVodRecoveryJob) convergeToProcessing(ctx context.Context, r completingVodRow) {
	// Ownership is already fenced at reconcileOne entry (recorded backend == local), so by here the row is provably
	// owned; backend_id is recorded at creation (invariant I2) and never reconstructed here.
	s3URL := j.s3.BuildS3URL(r.s3Key)
	moved := false
	err := database.WithRetryablePostgresTx(ctx, j.db, nil, func(tx *sql.Tx) error {
		res, execErr := tx.ExecContext(ctx, `
			UPDATE foghorn.artifacts
			SET status = 'processing',
			    storage_location = 's3',
			    sync_status = 'synced',
			    sync_error = NULL,
			    last_sync_attempt = NOW(),
			    frozen_at = COALESCE(frozen_at, NOW()),
			    s3_url = COALESCE(s3_url, $2),
			    -- The recovered multipart upload is durable on THIS cell's local S3 backend; persist attribution.
			    -- backend_id is NOT written here: it was recorded when the upload was CREATED (invariant I2) and
			    -- verified above, so recovery must not reconstruct or overwrite the recorded owner.
			    durable_backend_local = true,
			    -- Populate the authoritative object pointer from the recorded multipart key (see CompleteVodUpload).
			    active_object_key = COALESCE(active_object_key, (SELECT s3_key FROM foghorn.vod_metadata WHERE artifact_hash = foghorn.artifacts.artifact_hash)),
			    updated_at = NOW()
			WHERE artifact_hash = $1 AND tenant_id::text = $3 AND status = 'completing'
		`, r.artifactHash, s3URL, r.tenantID)
		if execErr != nil {
			return execErr
		}
		affected, raErr := res.RowsAffected()
		if raErr != nil {
			return raErr
		}
		if affected == 0 {
			return nil // row left 'completing' under us (concurrent completion/abort) — emit no event, no job
		}
		if _, jobErr := InsertProcessingJobWithSourceParamsTx(ctx, tx, r.tenantID, r.artifactHash, "process", nil, r.processesJSON, "", nil, ""); jobErr != nil {
			return jobErr
		}
		data := &ipcpb.VodLifecycleData{
			Status:   ipcpb.VodLifecycleData_STATUS_PROCESSING,
			VodHash:  r.artifactHash,
			S3Url:    &s3URL,
			TenantId: &r.tenantID,
		}
		if r.uploadID != "" {
			data.UploadId = &r.uploadID
		}
		if r.userID != "" {
			data.UserId = &r.userID
		}
		if r.sizeBytes > 0 {
			sz := uint64(r.sizeBytes)
			data.SizeBytes = &sz
		}
		moved = true
		return artifactoutbox.EnqueueVodLifecycleTx(ctx, tx, data)
	})
	if err != nil {
		j.logger.WithError(err).WithField("artifact_hash", r.artifactHash).Warn("Completing-VOD recovery: convergence to processing failed; row stays 'completing' for a later pass")
		return
	}
	if moved {
		// The tx variant does not notify (the row is not durable until commit); wake dispatchers here.
		NotifyProcessingJobQueued()
		j.logger.WithFields(logging.Fields{
			"artifact_hash": r.artifactHash,
			"s3_key":        r.s3Key,
		}).Info("Completing-VOD recovery: object present, converged stranded upload to processing")
	}
}

// convergeToFailed marks a stranded 'completing' row whose object is confirmed absent past the grace
// period as 'failed', emitting the FAILED lifecycle event in ONE transaction.
func (j *CompletingVodRecoveryJob) convergeToFailed(ctx context.Context, r completingVodRow) {
	errMsg := fmt.Sprintf("VOD upload stranded in 'completing'; object absent past grace (%s)", j.failAfter)
	err := database.WithRetryablePostgresTx(ctx, j.db, nil, func(tx *sql.Tx) error {
		res, execErr := tx.ExecContext(ctx, `
			UPDATE foghorn.artifacts
			SET status = 'failed',
			    sync_status = 'failed',
			    sync_error = $1,
			    error_message = $1,
			    last_sync_attempt = NOW(),
			    updated_at = NOW()
			WHERE artifact_hash = $2 AND tenant_id::text = $3 AND status = 'completing'
		`, errMsg, r.artifactHash, r.tenantID)
		if execErr != nil {
			return execErr
		}
		affected, raErr := res.RowsAffected()
		if raErr != nil {
			return raErr
		}
		if affected == 0 {
			return nil // row left 'completing' under us — emit no FAILED event
		}
		data := &ipcpb.VodLifecycleData{
			Status:   ipcpb.VodLifecycleData_STATUS_FAILED,
			VodHash:  r.artifactHash,
			TenantId: &r.tenantID,
			Error:    &errMsg,
		}
		if r.uploadID != "" {
			data.UploadId = &r.uploadID
		}
		if r.userID != "" {
			data.UserId = &r.userID
		}
		return artifactoutbox.EnqueueVodLifecycleTx(ctx, tx, data)
	})
	if err != nil {
		j.logger.WithError(err).WithField("artifact_hash", r.artifactHash).Warn("Completing-VOD recovery: convergence to failed failed")
		return
	}
	j.logger.WithFields(logging.Fields{
		"artifact_hash": r.artifactHash,
		"s3_key":        r.s3Key,
	}).Warn("Completing-VOD recovery: object confirmed absent past grace, marked stranded upload failed")
}
