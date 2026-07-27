package jobs

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"frameworks/api_balancing/internal/artifacts"
	"frameworks/api_balancing/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// PurgeDeletedJob hard-deletes old soft-deleted artifact records and the
// underlying S3 bytes. Failed and stale-uploading rows are also reaped
// here so DB and S3 don't accumulate orphans. S3 deletion goes through
// artifacts.Cleaner so cross-cluster bytes hit the federation delete
// delegate and never touch the wrong bucket.
//
// Convergence invariant: EVERY physically-purged artifact first emits a
// catalog deletion. A purgeable 'failed' row is transitioned to 'deleted'
// (revision-bumped) so the reconciler projects its deletion onto the
// Commodore catalog; only then, once that deletion is confirmed acked
// (catalog_synced_rev >= catalog_revision), are its bytes and DB row
// reaped. This prevents a purged row from stranding a phantom catalog
// asset (a "Failed"/"Ready" library entry with no authoritative source).
type PurgeDeletedJob struct {
	db           *sql.DB
	logger       logging.Logger
	interval     time.Duration
	retentionAge time.Duration
	stopCh       chan struct{}
	wg           sync.WaitGroup
	cleaner      *artifacts.Cleaner
	s3Aborter    UploadAborter
	// crossClusterDeleteEnabled mirrors the federation server's mutation gate. While it is false (the default),
	// remote-owned bytes can never be freed (the delegate delete fails closed), so the bytes+rows sweep skips
	// remote rows entirely — otherwise a page of permanently-undeletable remote rows would occupy every fixed
	// LIMIT pass and starve reapable local rows. When true, remote rows are re-admitted to the sweep.
	crossClusterDeleteEnabled bool
}

// UploadAborter is the local-S3 surface needed for stale uploading-VOD
// cleanup. Multipart abort is not exposed via the federation delegate, so
// rows whose bytes live on a peer cluster are skipped and logged.
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
	// AllowCrossClusterDelete must match the federation server's AllowFederationMutations. Leave false (the
	// default) until cluster-bound delete identity exists; the bytes+rows sweep then skips remote rows so they
	// cannot starve local reaping. See PurgeDeletedJob.crossClusterDeleteEnabled.
	AllowCrossClusterDelete bool
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
		db:                        cfg.DB,
		logger:                    cfg.Logger,
		interval:                  interval,
		retentionAge:              retentionAge,
		stopCh:                    make(chan struct{}),
		cleaner:                   cfg.Cleaner,
		s3Aborter:                 cfg.S3Aborter,
		crossClusterDeleteEnabled: cfg.AllowCrossClusterDelete,
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

// markFailedArtifactsDeleted transitions purgeable 'failed' artifacts to
// 'deleted' so the artifact reconciler projects a catalog DELETION for them
// (the same path an authoritative delete takes) BEFORE their bytes/row are
// ever reaped. Without this a purged 'failed' row would strand a permanent
// phantom "Failed" library asset in Commodore. The status change trips the
// foghorn.artifacts catalog-revision trigger (status is a projected field),
// so catalog_revision is bumped and the row enters the projection queue.
// updated_at is intentionally left untouched so the row stays past-retention
// and the next byte/row sweep can reap it once the deletion is acked.
// Convergent: the row is now 'deleted', so it is never re-selected here.
func (j *PurgeDeletedJob) markFailedArtifactsDeleted(ctx context.Context) {
	res, err := j.db.ExecContext(ctx, `
		UPDATE foghorn.artifacts a
		   SET status = 'deleted'
		 WHERE a.artifact_type IN ('clip', 'dvr', 'vod')
		   AND a.status = 'failed'
		   AND a.updated_at < NOW() - $1::interval
		   AND NOT EXISTS (
		       SELECT 1 FROM foghorn.artifact_nodes an
		       WHERE an.artifact_hash = a.artifact_hash
		         AND an.is_orphaned = false
		   )
	`, j.retentionAge.String())
	if err != nil {
		j.logger.WithError(err).Error("Purge: failed to transition failed artifacts to deleted")
		return
	}
	if n, _ := res.RowsAffected(); n > 0 { //nolint:errcheck // pq populates RowsAffected on UPDATE
		j.logger.WithField("count", n).Info("Purge: marked failed artifacts deleted for catalog projection")
	}
}

// purgeArtifactBytesAndRows handles status='deleted' artifacts past the
// retention age. It first transitions purgeable 'failed' rows to 'deleted'
// (markFailedArtifactsDeleted) so every physically-purged artifact emits a
// catalog deletion first. It then reaps ONLY rows whose catalog deletion has
// been confirmed projected AND acked (catalog_synced_rev >= catalog_revision):
// a row whose projection is still pending/failing is retained (retryable) so
// it never loses its only repair source. S3 bytes and the DB row are cleaned
// together under that coverage gate — a row is never physically freed until
// the catalog no longer depends on it, so a purge can't strand a phantom.
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
	j.markFailedArtifactsDeleted(ctx)

	// While cross-cluster deletion is disabled, exclude remote-owned rows: their bytes can't be freed, so a
	// page full of them would occupy every fixed-LIMIT pass and starve reapable local rows. They are retained
	// (never lost) until a cluster-bound delete path lands. localCluster empty (unknown) → don't filter.
	//
	// "Local" = the authoritative durable_backend_local flag is true, OR the row has NO remote owner recorded
	// (both cluster columns NULL/empty → the third COALESCE arg makes that '' rather than NULL, so it is kept —
	// a plain `IN ('', $2)` would drop both-NULL rows because NULL never matches IN, leaking local rows), OR the
	// resolved owner equals this cluster.
	localCluster := ""
	if j.cleaner != nil {
		localCluster = j.cleaner.LocalCluster
	}
	remoteClause := ""
	args := []any{j.retentionAge.String()}
	if !j.crossClusterDeleteEnabled && localCluster != "" {
		remoteClause = `
		  AND (
		        a.durable_backend_local = true
		     OR COALESCE(NULLIF(a.storage_cluster_id, ''), NULLIF(a.origin_cluster_id, ''), '') = ''
		     OR COALESCE(NULLIF(a.storage_cluster_id, ''), NULLIF(a.origin_cluster_id, '')) = $2
		  )`
		args = append(args, localCluster)
	}

	rows, err := j.db.QueryContext(ctx, `
		SELECT a.artifact_hash, a.artifact_type, a.tenant_id::text,
		       COALESCE(a.stream_internal_name, ''),
		       COALESCE(a.format, ''),
		       COALESCE(a.storage_cluster_id, ''),
		       COALESCE(a.origin_cluster_id, ''),
		       COALESCE(v.s3_key, ''),
		       COALESCE(a.s3_url, ''),
		       COALESCE(a.sync_object_key, ''),
		       COALESCE(a.active_object_key, ''),
		       COALESCE(a.active_dtsh_key, ''),
		       COALESCE(a.durable_backend_local, false),
		       a.status
		FROM foghorn.artifacts a
		LEFT JOIN foghorn.vod_metadata v ON v.artifact_hash = a.artifact_hash
		WHERE a.artifact_type IN ('clip', 'dvr', 'vod')
		  AND a.status = 'deleted'
		  AND a.updated_at < NOW() - $1::interval
		  -- Coverage gate: reap only once the catalog deletion is projected AND acked. A revision-0
		  -- row (never seeded/projected) is NOT covered; it waits for the reconciler to seed+project
		  -- it, so a purge can never drop a row whose deletion the catalog hasn't yet recorded.
		  AND a.catalog_revision > 0
		  AND a.catalog_synced_rev >= a.catalog_revision
		  AND NOT EXISTS (
		      SELECT 1 FROM foghorn.artifact_nodes an
		      WHERE an.artifact_hash = a.artifact_hash
		        AND an.is_orphaned = false
		  )`+remoteClause+`
		-- Deterministic, oldest-first ordering so progress is fair across passes rather than
		-- re-scanning the same arbitrary head each cycle.
		ORDER BY a.updated_at
		LIMIT 1000
	`, args...)
	if err != nil {
		j.logger.WithError(err).Error("Failed to query artifacts for purge")
		return
	}
	defer func() { _ = rows.Close() }()

	type purgeRow struct {
		hash, artifactType, tenantID, streamInternal, format string
		storageClusterID, originClusterID                    string
		vodS3Key, s3URL, pendingObjectKey, activeObjectKey   string
		activeDtshKey                                        string
		durableBackendLocal                                  bool
		status                                               string
	}
	var batch []purgeRow
	for rows.Next() {
		var r purgeRow
		if errScan := rows.Scan(&r.hash, &r.artifactType, &r.tenantID, &r.streamInternal, &r.format, &r.storageClusterID, &r.originClusterID, &r.vodS3Key, &r.s3URL, &r.pendingObjectKey, &r.activeObjectKey, &r.activeDtshKey, &r.durableBackendLocal, &r.status); errScan != nil {
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
			Hash:                r.hash,
			Type:                r.artifactType,
			TenantID:            r.tenantID,
			StreamInternal:      r.streamInternal,
			Format:              r.format,
			StorageClusterID:    r.storageClusterID,
			OriginClusterID:     r.originClusterID,
			VODS3Key:            r.vodS3Key,
			S3URL:               r.s3URL,
			ActiveObjectKey:     r.activeObjectKey,
			ActiveDtshKey:       r.activeDtshKey,
			DurableBackendLocal: r.durableBackendLocal,
			PendingObjectKey:    r.pendingObjectKey,
		}
		errDel := j.cleaner.Delete(ctx, ref)
		switch {
		case errDel == nil:
			// S3 cleanup succeeded; safe to hard-delete the DB row (its catalog deletion is acked).
		case errors.Is(errDel, artifacts.ErrMissingTarget):
			// This artifact's deletion is catalog-acked and it has no derivable S3 target (nothing
			// addressable to free at the deterministic key path); drop the DB row.
			j.logger.WithError(errDel).WithFields(logging.Fields{
				"artifact_hash": r.hash,
				"artifact_type": r.artifactType,
			}).Info("Purge: no S3 target derivable for deleted row; dropping DB row only")
		default:
			// Any transient S3/delegate error: keep the row and retry next cycle. The catalog
			// deletion is already acked, so retaining the row only defers byte cleanup — it never
			// strands a phantom.
			j.logger.WithError(errDel).WithFields(logging.Fields{
				"artifact_hash": r.hash,
				"artifact_type": r.artifactType,
				"status":        r.status,
			}).Warn("Purge: S3 cleanup not confirmed; keeping DB row for retry")
			continue
		}

		// Reclaim the asset's thumbnails, ORDERED to fence against a racing completion: (0) capture the thumbnail
		// destination cluster(s) BEFORE deleting the rows — thumbnails live on the tenant's official-durable
		// destination, NOT the parent artifact's backend, so routing S3 deletion by the parent (ref) would
		// delegate a BYOC-origin artifact's platform-stored thumbnails to the wrong cluster and leak them; (1)
		// drop the control rows (tenant-ownership-proved) so no new publication can proceed — PublishThumbnailAttempt
		// finds no assignment and completion drops on the parent tombstone; (2) THEN sweep the S3 prefix on each
		// destination cluster, freeing any objects a completion promoted before step 1. Sweeping S3 first would
		// let a promote land after the sweep and leak. Idempotent; a transient failure retains the row for retry.
		thumbClusters, errTDC := control.ThumbnailDestinationClusters(ctx, j.db, r.tenantID, r.hash)
		if errTDC != nil {
			j.logger.WithError(errTDC).WithField("artifact_hash", r.hash).Warn("Purge: reading thumbnail destination clusters failed; keeping DB row for retry")
			continue
		}
		if errTC := control.DeleteThumbnailControlRows(ctx, j.db, r.tenantID, r.hash); errTC != nil {
			j.logger.WithError(errTC).WithField("artifact_hash", r.hash).Warn("Purge: thumbnail control-row cleanup failed; keeping DB row for retry")
			continue
		}
		// Sweep the prefix on the thumbnail's OWN destination cluster(s). If none recorded (no attempts), fall
		// back to a local sweep so a stray legacy object is still reclaimed.
		if len(thumbClusters) == 0 {
			thumbClusters = []string{""}
		}
		thumbErr := false
		for _, tc := range thumbClusters {
			if errThumb := j.cleaner.DeleteThumbnailsOnCluster(ctx, r.tenantID, r.hash, tc); errThumb != nil {
				j.logger.WithError(errThumb).WithFields(logging.Fields{
					"artifact_hash":       r.hash,
					"artifact_type":       r.artifactType,
					"destination_cluster": tc,
				}).Warn("Purge: thumbnail byte cleanup not confirmed; keeping DB row for retry")
				thumbErr = true
				break
			}
		}
		if thumbErr {
			continue
		}

		if _, errDelete := j.db.ExecContext(ctx, "DELETE FROM foghorn.artifacts WHERE artifact_hash = $1 AND tenant_id::text = $2", r.hash, r.tenantID); errDelete != nil {
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
