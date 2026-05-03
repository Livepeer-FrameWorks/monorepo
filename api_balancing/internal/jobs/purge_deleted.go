package jobs

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"frameworks/api_balancing/internal/artifacts"
	"frameworks/pkg/logging"
)

// PurgeDeletedJob hard-deletes old soft-deleted artifact records and the
// underlying S3 bytes. Failed and stale-uploading rows are also reaped
// here so DB and S3 don't accumulate orphans. S3 deletion goes through
// artifacts.Cleaner so cross-cluster bytes hit the federation delete
// delegate and never touch the wrong bucket.
type PurgeDeletedJob struct {
	db           *sql.DB
	logger       logging.Logger
	interval     time.Duration
	retentionAge time.Duration
	stopCh       chan struct{}
	wg           sync.WaitGroup
	cleaner      *artifacts.Cleaner
	s3Aborter    UploadAborter
}

// UploadAborter is the local-S3 surface needed for stale uploading-VOD
// cleanup. Multipart abort is not exposed via federation; remote rows
// are skipped+logged for now.
type UploadAborter interface {
	AbortMultipartUpload(ctx context.Context, key, uploadID string) error
}

// PurgeDeletedConfig holds configuration for the purge job
type PurgeDeletedConfig struct {
	DB           *sql.DB
	Logger       logging.Logger
	Interval     time.Duration
	RetentionAge time.Duration
	Cleaner      *artifacts.Cleaner
	S3Aborter    UploadAborter
}

// NewPurgeDeletedJob creates a new purge deleted job
func NewPurgeDeletedJob(cfg PurgeDeletedConfig) *PurgeDeletedJob {
	interval := cfg.Interval
	if interval == 0 {
		interval = 24 * time.Hour
	}
	retentionAge := cfg.RetentionAge
	if retentionAge == 0 {
		retentionAge = 30 * 24 * time.Hour
	}
	return &PurgeDeletedJob{
		db:           cfg.DB,
		logger:       cfg.Logger,
		interval:     interval,
		retentionAge: retentionAge,
		stopCh:       make(chan struct{}),
		cleaner:      cfg.Cleaner,
		s3Aborter:    cfg.S3Aborter,
	}
}

// Start begins the background purge loop
func (j *PurgeDeletedJob) Start() {
	j.wg.Add(1)
	go j.run()
	j.logger.Info("Purge deleted job started")
}

// Stop gracefully stops the job
func (j *PurgeDeletedJob) Stop() {
	close(j.stopCh)
	j.wg.Wait()
	j.logger.Info("Purge deleted job stopped")
}

func (j *PurgeDeletedJob) run() {
	defer j.wg.Done()
	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()

	// Run once at startup, staggered by 1 hour to avoid startup load.
	time.AfterFunc(1*time.Hour, func() {
		j.purge()
	})

	for {
		select {
		case <-ticker.C:
			j.purge()
		case <-j.stopCh:
			return
		}
	}
}

func (j *PurgeDeletedJob) purge() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	j.purgeStaleUploadingVODs(ctx)
	j.purgeArtifactBytesAndRows(ctx)
	j.purgeStaleNodeRows(ctx)
}

// purgeArtifactBytesAndRows handles status='deleted' and status='failed'
// artifacts past the retention age. S3 bytes go first (via the shared
// cleaner so cross-cluster deletes route correctly); the DB row is hard-
// deleted only when S3 cleanup definitively succeeded so failures get
// retried next cycle.
//
// Fail-closed: if the cleaner is unwired, this loop does nothing rather
// than hard-deleting rows that may still hold bytes elsewhere (an origin
// Foghorn that delegates all storage to peer clusters has no local S3
// but still needs the federation delegate to free remote bytes).
func (j *PurgeDeletedJob) purgeArtifactBytesAndRows(ctx context.Context) {
	if j.cleaner == nil {
		j.logger.Debug("Purge: artifact cleaner not wired; skipping bytes+rows sweep this cycle")
		return
	}
	rows, err := j.db.QueryContext(ctx, `
		SELECT a.artifact_hash, a.artifact_type, a.tenant_id::text,
		       COALESCE(a.stream_internal_name, ''),
		       COALESCE(a.format, ''),
		       COALESCE(a.storage_cluster_id, ''),
		       COALESCE(a.origin_cluster_id, ''),
		       COALESCE(v.s3_key, ''),
		       COALESCE(a.s3_url, ''),
		       a.status
		FROM foghorn.artifacts a
		LEFT JOIN foghorn.vod_metadata v ON v.artifact_hash = a.artifact_hash
		WHERE a.artifact_type IN ('clip', 'dvr', 'vod')
		  AND a.status IN ('deleted', 'failed')
		  AND a.updated_at < NOW() - $1::interval
		  AND NOT EXISTS (
		      SELECT 1 FROM foghorn.artifact_nodes an
		      WHERE an.artifact_hash = a.artifact_hash
		        AND an.is_orphaned = false
		  )
		LIMIT 1000
	`, j.retentionAge.String())
	if err != nil {
		j.logger.WithError(err).Error("Failed to query artifacts for purge")
		return
	}
	defer func() { _ = rows.Close() }()

	type purgeRow struct {
		hash, artifactType, tenantID, streamInternal, format string
		storageClusterID, originClusterID                    string
		vodS3Key, s3URL, status                              string
	}
	var batch []purgeRow
	for rows.Next() {
		var r purgeRow
		if errScan := rows.Scan(&r.hash, &r.artifactType, &r.tenantID, &r.streamInternal, &r.format, &r.storageClusterID, &r.originClusterID, &r.vodS3Key, &r.s3URL, &r.status); errScan != nil {
			j.logger.WithError(errScan).Warn("Failed to scan artifact purge row")
			continue
		}
		batch = append(batch, r)
	}
	if errIter := rows.Err(); errIter != nil {
		j.logger.WithError(errIter).Warn("Purge: row iteration error; processing partial batch")
	}

	var clipCount, dvrCount, vodCount int
	for _, r := range batch {
		ref := artifacts.ArtifactRef{
			Hash:             r.hash,
			Type:             r.artifactType,
			TenantID:         r.tenantID,
			StreamInternal:   r.streamInternal,
			Format:           r.format,
			StorageClusterID: r.storageClusterID,
			OriginClusterID:  r.originClusterID,
			VODS3Key:         r.vodS3Key,
			S3URL:            r.s3URL,
		}
		errDel := j.cleaner.Delete(ctx, ref)
		switch {
		case errDel == nil:
			// S3 cleanup succeeded; safe to hard-delete the DB row.
		case errors.Is(errDel, artifacts.ErrMissingTarget) && r.status == "deleted":
			// User explicitly soft-deleted this artifact and we have no
			// derivable S3 target. There's nothing addressable to free
			// at the deterministic key path; drop the DB row.
			j.logger.WithError(errDel).WithFields(logging.Fields{
				"artifact_hash": r.hash,
				"artifact_type": r.artifactType,
			}).Info("Purge: no S3 target derivable for soft-deleted row; dropping DB row only")
		default:
			// Failed artifacts with missing metadata may still have
			// partial bytes at the deterministic prefix from an
			// interrupted freeze; keep the row so an operator (or a
			// future repair sweep) can investigate. Same goes for any
			// transient S3/delegate error: keep the row, retry next
			// cycle.
			j.logger.WithError(errDel).WithFields(logging.Fields{
				"artifact_hash": r.hash,
				"artifact_type": r.artifactType,
				"status":        r.status,
			}).Warn("Purge: S3 cleanup not confirmed; keeping DB row for retry")
			continue
		}

		if _, errDelete := j.db.ExecContext(ctx, "DELETE FROM foghorn.artifacts WHERE artifact_hash = $1", r.hash); errDelete != nil {
			j.logger.WithError(errDelete).WithField("artifact_hash", r.hash).Warn("Purge: failed to hard-delete row")
			continue
		}
		switch r.artifactType {
		case "clip":
			clipCount++
		case "dvr":
			dvrCount++
		case "vod":
			vodCount++
		}
	}
	if clipCount > 0 {
		j.logger.WithField("count", clipCount).Info("Purged old clip artifacts")
	}
	if dvrCount > 0 {
		j.logger.WithField("count", dvrCount).Info("Purged old DVR artifacts")
	}
	if vodCount > 0 {
		j.logger.WithField("count", vodCount).Info("Purged old VOD artifacts")
	}
}

// purgeStaleUploadingVODs aborts multipart uploads whose
// upload_expires_at has passed and soft-deletes the artifact row so the
// next purge cycle reaps it. Multipart abort isn't exposed via the
// federation delegate; rows whose storage_cluster_id points to a peer
// cluster are skipped+logged.
func (j *PurgeDeletedJob) purgeStaleUploadingVODs(ctx context.Context) {
	rows, err := j.db.QueryContext(ctx, `
		SELECT a.artifact_hash,
		       COALESCE(a.storage_cluster_id, ''),
		       COALESCE(a.origin_cluster_id, ''),
		       COALESCE(v.s3_key, ''),
		       COALESCE(v.s3_upload_id, '')
		FROM foghorn.artifacts a
		JOIN foghorn.vod_metadata v ON v.artifact_hash = a.artifact_hash
		WHERE a.status = 'uploading'
		  AND v.upload_expires_at IS NOT NULL
		  AND v.upload_expires_at < NOW() - INTERVAL '1 hour'
		LIMIT 1000
	`)
	if err != nil {
		j.logger.WithError(err).Error("Failed to query stale uploading VODs")
		return
	}
	defer func() { _ = rows.Close() }()

	type uploadRow struct {
		hash, storageClusterID, originClusterID, s3Key, uploadID string
	}
	var batch []uploadRow
	for rows.Next() {
		var r uploadRow
		if errScan := rows.Scan(&r.hash, &r.storageClusterID, &r.originClusterID, &r.s3Key, &r.uploadID); errScan != nil {
			continue
		}
		batch = append(batch, r)
	}

	var aborted int
	for _, r := range batch {
		owner := r.storageClusterID
		if owner == "" {
			owner = r.originClusterID
		}
		isLocal := owner == "" || (j.cleaner != nil && owner == j.cleaner.LocalCluster)
		if !isLocal {
			j.logger.WithFields(logging.Fields{
				"artifact_hash":   r.hash,
				"storage_cluster": owner,
				"upload_remote":   true,
			}).Warn("Stale uploading VOD on remote cluster; abort not yet delegated")
			continue
		}
		if r.s3Key != "" && r.uploadID != "" && j.s3Aborter != nil {
			if errAbort := j.s3Aborter.AbortMultipartUpload(ctx, r.s3Key, r.uploadID); errAbort != nil {
				j.logger.WithError(errAbort).WithField("artifact_hash", r.hash).Warn("Stale upload abort failed; will retry next cycle")
				continue
			}
		}
		if _, errMark := j.db.ExecContext(ctx, `
			UPDATE foghorn.artifacts SET status = 'deleted', updated_at = NOW()
			WHERE artifact_hash = $1 AND status = 'uploading'
		`, r.hash); errMark != nil {
			j.logger.WithError(errMark).WithField("artifact_hash", r.hash).Warn("Stale upload soft-delete failed")
			continue
		}
		aborted++
	}
	if aborted > 0 {
		j.logger.WithField("count", aborted).Info("Aborted stale uploading VODs")
	}
}

func (j *PurgeDeletedJob) purgeStaleNodeRows(ctx context.Context) {
	res, err := j.db.ExecContext(ctx, `
		DELETE FROM foghorn.artifact_nodes
		WHERE is_orphaned = true
		  AND last_seen_at < NOW() - INTERVAL '7 days'
	`)
	if err != nil {
		j.logger.WithError(err).Error("Failed to purge stale artifact_nodes entries")
		return
	}
	affected, errAffected := res.RowsAffected()
	if errAffected != nil {
		j.logger.WithError(errAffected).Warn("Failed to read RowsAffected for stale artifact_nodes purge")
		return
	}
	if affected > 0 {
		j.logger.WithField("count", affected).Info("Purged stale artifact_nodes entries")
	}
}
