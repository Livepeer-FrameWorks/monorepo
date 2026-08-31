package jobs

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"frameworks/api_balancing/internal/artifacts"
	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// PurgeDeletedJob hard-deletes old soft-deleted artifact records and the
// underlying S3 bytes. Failed and stale-uploading rows are also reaped
// here so DB and S3 don't accumulate orphans. S3 deletion goes through
// artifacts.Cleaner so cross-cluster bytes hit the federation delete
// delegate and never touch the wrong bucket.
//
// Convergence invariant for origin-owned artifacts: every physical purge first
// emits a catalog deletion. Federated pointers are not catalog authority; their
// signed tombstone/expiry fence and ordered local-derivative cleanup replace
// that catalog acknowledgement. A purgeable 'failed' row is transitioned to 'deleted'
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
	lifecycleCtx context.Context
	cancel       context.CancelFunc
	// crossClusterDeleteEnabled mirrors the federation server's mutation gate. While it is false (the default),
	// remote-owned bytes can never be freed (the delegate delete fails closed), so the bytes+rows sweep skips
	// remote rows entirely — otherwise a page of permanently-undeletable remote rows would occupy every fixed
	// LIMIT pass and starve reapable local rows. When true, remote rows are re-admitted to the sweep.
	crossClusterDeleteEnabled bool
}

const (
	federatedPointerCleanupTimeout   = 2 * time.Minute
	federatedPointerDailyBudget      = 2 * time.Minute
	federatedPointerRecoveryInterval = 30 * time.Second
	federatedPointerCleanupWorkers   = 8
	federatedPointerReleaseTimeout   = 5 * time.Second
	federatedPointerMinimumRunBudget = 5 * time.Second
	federatedPointerEvidenceRetry    = 15 * time.Minute
)

// PurgeDeletedConfig holds configuration for the purge job
type PurgeDeletedConfig struct {
	DB           *sql.DB
	Logger       logging.Logger
	Interval     time.Duration
	RetentionAge time.Duration
	Cleaner      *artifacts.Cleaner
	// AllowCrossClusterDelete must match the federation server's AllowFederationMutations. Disabled deployments
	// skip remote rows so undelegatable work cannot starve local reaping.
	AllowCrossClusterDelete bool
}

// NewPurgeDeletedJob creates a new purge deleted job
func NewPurgeDeletedJob(cfg PurgeDeletedConfig) *PurgeDeletedJob {
	lifecycleCtx, cancel := context.WithCancel(context.Background())
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
		lifecycleCtx:              lifecycleCtx,
		cancel:                    cancel,
		crossClusterDeleteEnabled: cfg.AllowCrossClusterDelete,
	}
}

// Start begins the background purge loop
func (j *PurgeDeletedJob) Start() {
	j.wg.Add(2)
	go j.run()
	go j.runFederatedPointerRecovery()
	j.logger.Info("Purge deleted job started")
}

// Stop gracefully stops the job
func (j *PurgeDeletedJob) Stop() {
	j.cancel()
	close(j.stopCh)
	j.wg.Wait()
	j.logger.Info("Purge deleted job stopped")
}

func (j *PurgeDeletedJob) run() {
	defer j.wg.Done()
	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()
	// Run once at startup, staggered by 1 hour to avoid startup load.
	// Keep the timer inside the lifecycle loop so Stop cannot leave a delayed
	// purge callback behind.
	firstRun := time.NewTimer(time.Hour)
	defer firstRun.Stop()

	for {
		select {
		case <-firstRun.C:
			j.purge()
		case <-ticker.C:
			j.purge()
		case <-j.stopCh:
			return
		}
	}
}

func (j *PurgeDeletedJob) runFederatedPointerRecovery() {
	defer j.wg.Done()
	ticker := time.NewTicker(federatedPointerRecoveryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(j.lifecycleCtx, federatedPointerCleanupTimeout)
			j.purgeRecoverableFederatedPointers(ctx)
			cancel()
		case <-j.stopCh:
			return
		}
	}
}

func (j *PurgeDeletedJob) purge() {
	ctx, cancel := context.WithTimeout(j.lifecycleCtx, 10*time.Minute)
	defer cancel()

	j.purgeStaleUploadingVODs(ctx)
	j.purgeArtifactBytesAndRows(ctx)
	j.purgeStaleNodeRows(ctx)
	// Federated discovery follows local cleanup in wall-clock order, but it does
	// not inherit the local sweep's possibly exhausted deadline. Its independent
	// lifecycle-bound budget lets a slow local store defer neither discovery nor
	// shutdown indefinitely.
	dailyCtx, dailyCancel := j.federatedPointerScheduleContext()
	j.purgeFederatedPointers(dailyCtx)
	dailyCancel()
}

func (j *PurgeDeletedJob) federatedPointerScheduleContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(j.lifecycleCtx, federatedPointerDailyBudget)
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
	n, err := foghorndb.New(j.db).MarkFailedArtifactsDeleted(ctx, j.retentionAge.String())
	if err != nil {
		j.logger.WithError(err).Error("Purge: failed to transition failed artifacts to deleted")
		return
	}
	if n > 0 {
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
		j.logger.Debug("Purge: artifact cleaner not wired; skipping all bytes+rows sweeps this cycle")
		return
	}
	j.markFailedArtifactsDeleted(ctx)

	// BACKEND AFFINITY: only claim rows recorded on THIS cell's store — ownership is read from recorded evidence, never
	// reconstructed (invariant I2). A row's backend_id names the physical store its bytes live on; a bare delete against
	// the wrong store is a no-op success that would then hard-delete the row and orphan the real bytes. So we claim ONLY
	// `backend_id = this cell's fingerprint`. A NULL backend is unattributed and RETAINED — a safe row leak, never byte
	// orphaning, and never a guessed-store delete. A cell without a fingerprint cannot prove ownership of anything.
	localBackendID := ""
	if j.cleaner != nil {
		localBackendID = j.cleaner.LocalBackendID
	}
	type purgeRow struct {
		hash, artifactType, tenantID, streamInternal, format string
		storageClusterID, originClusterID                    string
		vodS3Key, s3URL, pendingObjectKey, activeObjectKey   string
		activeDtshKey                                        string
		durableBackendLocal                                  bool
		backendID                                            string
		status                                               string
	}
	var batch []purgeRow
	queries := foghorndb.New(j.db)
	if j.crossClusterDeleteEnabled {
		rows, err := queries.ListPurgeableArtifacts(ctx, j.retentionAge.String())
		if err != nil {
			j.logger.WithError(err).Error("Failed to query artifacts for purge")
			return
		}
		for _, r := range rows {
			batch = append(batch, purgeRow{r.ArtifactHash, r.ArtifactType, r.ATenantID, r.StreamInternalName, r.Format, r.StorageClusterID, r.OriginClusterID, r.VodS3Key, r.S3Url, r.SyncObjectKey, r.ActiveObjectKey, r.ActiveDtshKey, r.DurableBackendLocal, r.BackendID, r.Status.String})
		}
	} else if localBackendID != "" {
		rows, err := queries.ListPurgeableLocalArtifacts(ctx, foghorndb.ListPurgeableLocalArtifactsParams{RetentionInterval: j.retentionAge.String(), BackendID: localBackendID})
		if err != nil {
			j.logger.WithError(err).Error("Failed to query artifacts for purge")
			return
		}
		for _, r := range rows {
			batch = append(batch, purgeRow{r.ArtifactHash, r.ArtifactType, r.ATenantID, r.StreamInternalName, r.Format, r.StorageClusterID, r.OriginClusterID, r.VodS3Key, r.S3Url, r.SyncObjectKey, r.ActiveObjectKey, r.ActiveDtshKey, r.DurableBackendLocal, r.BackendID, r.Status.String})
		}
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
			BackendID:           r.backendID,
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
		// Sweep the prefix on the thumbnail's OWN destination cluster(s), routed by the recorded backend-local
		// fact. If none recorded (no attempts), fall back to a LOCAL sweep on this artifact's recorded backend (this
		// cell's store) so a stray legacy object is reclaimed — never an empty identity, which the strict adapter
		// refuses.
		if len(thumbClusters) == 0 {
			thumbClusters = []control.ThumbnailDestination{{BackendID: r.backendID, BackendLocal: true}}
		}
		thumbErr := false
		for _, tc := range thumbClusters {
			if errThumb := j.cleaner.DeleteThumbnailsOnCluster(ctx, r.tenantID, r.hash, tc.Cluster, tc.BackendLocal, tc.BackendID); errThumb != nil {
				j.logger.WithError(errThumb).WithFields(logging.Fields{
					"artifact_hash":       r.hash,
					"artifact_type":       r.artifactType,
					"destination_cluster": tc.Cluster,
				}).Warn("Purge: thumbnail byte cleanup not confirmed; keeping DB row for retry")
				thumbErr = true
				break
			}
		}
		if thumbErr {
			continue
		}

		if errDelete := queries.DeletePurgedArtifact(ctx, foghorndb.DeletePurgedArtifactParams{ArtifactHash: r.hash, TenantID: r.tenantID}); errDelete != nil {
			j.logger.WithError(errDelete).WithField("artifact_hash", r.hash).Warn("Purge: failed to hard-delete row")
			continue
		}
		switch r.artifactType {
		case "clip":
			clipCount++
		case "dvr":
			dvrCount++
		case "vod", "chapter":
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

type federatedPointerPurgeCandidate struct {
	hash, tenantID string
	kind           control.FederatedPointerPurgeKind
}

// purgeFederatedPointers reaps replaceable routing rows without bypassing the
// derivative lifecycle. The parent is first made terminal while holding the
// same per-asset lock as thumbnail claim/publication. That fence prevents a
// writer appearing after the destination snapshot. Bytes are then swept while
// the control rows still retain their routing evidence; only confirmed cleanup
// permits the control rows and parent pointer to be hard-deleted atomically.
func (j *PurgeDeletedJob) purgeFederatedPointers(ctx context.Context) {
	if j.db == nil || j.cleaner == nil {
		return
	}
	q := foghorndb.New(j.db)
	retention := j.retentionAge.String()
	var candidates []federatedPointerPurgeCandidate
	tombstoned, err := q.ListTombstonedFederatedArtifactPointersForPurge(ctx, retention)
	if err != nil {
		j.logger.WithError(err).Warn("Purge: failed to list tombstoned federated artifact pointers")
		return
	}
	for _, row := range tombstoned {
		candidates = append(candidates, federatedPointerPurgeCandidate{
			hash: row.ArtifactHash, tenantID: row.ArtifactTenantID, kind: control.FederatedPointerPurgeTombstone,
		})
	}
	stale, err := q.ListStaleFederatedArtifactPointersForPurge(ctx, retention)
	if err != nil {
		j.logger.WithError(err).Warn("Purge: failed to list stale federated artifact pointers")
		return
	}
	for _, row := range stale {
		candidates = append(candidates, federatedPointerPurgeCandidate{
			hash: row.ArtifactHash, tenantID: row.ArtifactTenantID, kind: control.FederatedPointerPurgeStale,
		})
	}
	j.purgeFederatedPointerCandidatesConcurrent(ctx, candidates, retention)
}

// purgeRecoverableFederatedPointers is the short recovery loop for workers
// that died after fencing. It covers every expired token state; active
// authority is restored only by successful settlement, while tombstoned and
// stale pointers resume their original guarded deletion path.
func (j *PurgeDeletedJob) purgeRecoverableFederatedPointers(ctx context.Context) {
	if j.db == nil || j.cleaner == nil {
		return
	}
	rows, err := foghorndb.New(j.db).ListRecoverableFederatedArtifactPointerPurges(ctx, j.retentionAge.String())
	if err != nil {
		j.logger.WithError(err).Warn("Purge: failed to list recoverable federated pointer cleanups")
		return
	}
	candidates := make([]federatedPointerPurgeCandidate, 0, len(rows))
	for _, row := range rows {
		kind := control.FederatedPointerPurgeStale
		switch row.PurgeKind {
		case "tombstone":
			kind = control.FederatedPointerPurgeTombstone
		case "interrupted_active":
			kind = control.FederatedPointerPurgeInterruptedActive
		}
		candidates = append(candidates, federatedPointerPurgeCandidate{
			hash: row.ArtifactHash, tenantID: row.ArtifactTenantID, kind: kind,
		})
	}
	j.purgeFederatedPointerCandidatesConcurrent(ctx, candidates, j.retentionAge.String())
}

// purgeFederatedPointerCandidatesConcurrent bounds cleanup parallelism while
// preventing one slow remote destination from consuming the entire pass. The
// scheduling context bounds how long the pass may admit more work; each
// admitted candidate gets a full independent cleanup budget. At most the
// worker count can outlive scheduling cancellation, and each of those is
// bounded by federatedPointerCleanupTimeout.
func (j *PurgeDeletedJob) purgeFederatedPointerCandidatesConcurrent(scheduleCtx context.Context, candidates []federatedPointerPurgeCandidate, retention string) {
	if len(candidates) == 0 {
		return
	}
	workerCount := min(federatedPointerCleanupWorkers, len(candidates))
	work := make(chan federatedPointerPurgeCandidate)
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for candidate := range work {
				if deadline, ok := scheduleCtx.Deadline(); ok && time.Until(deadline) < federatedPointerMinimumRunBudget {
					return
				}
				candidateCtx, cancel := context.WithTimeout(j.lifecycleCtx, federatedPointerCleanupTimeout)
				j.purgeFederatedPointerCandidates(candidateCtx, []federatedPointerPurgeCandidate{candidate}, retention)
				cancel()
			}
		}()
	}
	for _, candidate := range candidates {
		select {
		case work <- candidate:
		case <-scheduleCtx.Done():
			close(work)
			workers.Wait()
			return
		}
	}
	close(work)
	workers.Wait()
}

func (j *PurgeDeletedJob) purgeFederatedPointerCandidates(ctx context.Context, candidates []federatedPointerPurgeCandidate, retention string) {
	q := foghorndb.New(j.db)
	purged := 0
	restored := 0
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		key := candidate.tenantID + "\x00" + candidate.hash
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}

		claimToken, fenced, fenceErr := control.FenceFederatedPointerForPurge(ctx, j.db, candidate.tenantID, candidate.hash, retention, candidate.kind, j.crossClusterDeleteEnabled)
		if fenceErr != nil {
			j.logger.WithError(fenceErr).WithField("artifact_hash", candidate.hash).Warn("Purge: failed to fence federated pointer")
			continue
		}
		if !fenced {
			continue
		}
		releaseClaim := func(cause string) {
			// Cleanup cancellation must not strand the durable lease. Claim release
			// gets a small independent settlement budget because its only external
			// dependency is this cell's database.
			releaseCtx, releaseCancel := context.WithTimeout(context.Background(), federatedPointerReleaseTimeout)
			defer releaseCancel()
			released, releaseErr := control.ReleaseFederatedPointerPurgeClaim(releaseCtx, j.db, candidate.tenantID, candidate.hash, claimToken)
			if releaseErr != nil {
				j.logger.WithError(releaseErr).WithFields(logging.Fields{"artifact_hash": candidate.hash, "cause": cause}).Warn("Purge: failed to release federated pointer cleanup claim")
			} else if released {
				j.logger.WithFields(logging.Fields{"artifact_hash": candidate.hash, "cause": cause}).Info("Purge: released federated pointer cleanup claim for retry")
			}
		}
		deferClaim := func(cause string) {
			deferCtx, deferCancel := context.WithTimeout(context.Background(), federatedPointerReleaseTimeout)
			defer deferCancel()
			deferred, deferErr := control.DeferFederatedPointerPurgeClaim(deferCtx, j.db, candidate.tenantID, candidate.hash, claimToken, federatedPointerEvidenceRetry)
			if deferErr != nil {
				j.logger.WithError(deferErr).WithFields(logging.Fields{"artifact_hash": candidate.hash, "cause": cause}).Warn("Purge: failed to defer federated pointer cleanup claim")
			} else if deferred {
				j.logger.WithFields(logging.Fields{"artifact_hash": candidate.hash, "cause": cause, "retry_after": federatedPointerEvidenceRetry}).Warn("Purge: deferred federated pointer cleanup pending backend evidence repair")
			}
		}
		state, stateErr := q.GetFencedFederatedArtifactPointerPurgeState(ctx, foghorndb.GetFencedFederatedArtifactPointerPurgeStateParams{
			ArtifactHash: candidate.hash, TenantID: candidate.tenantID, PurgeToken: claimToken,
		})
		if stateErr != nil {
			j.logger.WithError(stateErr).WithField("artifact_hash", candidate.hash).Warn("Purge: failed to read fenced federated pointer")
			releaseClaim("state_read")
			continue
		}
		destinations, destinationErr := control.ThumbnailDestinationClusters(ctx, j.db, candidate.tenantID, candidate.hash)
		if destinationErr != nil {
			j.logger.WithError(destinationErr).WithField("artifact_hash", candidate.hash).Warn("Purge: failed to read federated pointer thumbnail destinations")
			releaseClaim("destination_read")
			continue
		}
		if len(destinations) == 0 && state.HasThumbnails.Valid && state.HasThumbnails.Bool {
			if state.BackendID == "" || state.BackendID != j.cleaner.LocalBackendID {
				j.logger.WithFields(logging.Fields{
					"artifact_hash":    candidate.hash,
					"recorded_backend": state.BackendID,
					"local_backend":    j.cleaner.LocalBackendID,
				}).Warn("Purge: fenced pointer lacks exact local thumbnail backend evidence; retaining for repair")
				deferClaim("unusable_backend_evidence")
				continue
			}
			destinations = []control.ThumbnailDestination{{BackendID: state.BackendID, BackendLocal: true}}
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(ctx, federatedPointerCleanupTimeout)
		cleanupFailed := false
		for _, destination := range destinations {
			if cleanupErr := j.cleaner.DeleteThumbnailsOnCluster(cleanupCtx, candidate.tenantID, candidate.hash, destination.Cluster, destination.BackendLocal, destination.BackendID); cleanupErr != nil {
				j.logger.WithError(cleanupErr).WithFields(logging.Fields{
					"artifact_hash": candidate.hash, "destination_cluster": destination.Cluster,
				}).Warn("Purge: federated pointer thumbnail cleanup not confirmed; retaining for retry")
				cleanupFailed = true
				break
			}
		}
		cleanupCancel()
		if cleanupFailed {
			releaseClaim("byte_cleanup")
			continue
		}
		settlement, settleErr := control.FinalizeFederatedPointerPurge(ctx, j.db, candidate.tenantID, candidate.hash, claimToken)
		if settleErr != nil {
			j.logger.WithError(settleErr).WithField("artifact_hash", candidate.hash).Warn("Purge: failed to atomically settle fenced federated pointer")
			releaseClaim("settle_error")
			continue
		}
		switch settlement {
		case control.FederatedPointerPurgeDeleted:
			purged++
		case control.FederatedPointerPurgeRestoredActive:
			restored++
		default:
			releaseClaim("settle_fenced")
		}
	}
	if purged > 0 {
		j.logger.WithField("count", purged).Info("Purged federated artifact pointers after derivative cleanup")
	}
	if restored > 0 {
		j.logger.WithField("count", restored).Info("Restored active federated artifact pointers after completing prior derivative cleanup")
	}
}

// purgeStaleUploadingVODs claims expired multipart uploads for teardown by transitioning the row
// 'uploading' -> 'aborting' (durable, guarded, tenant-scoped) — it does NOT abort S3 itself. The
// AbortingVodRecoveryJob then owns the idempotent S3 abort and convergence to 'deleted'. Aborting S3 here
// (before the claim) would race CompleteVodUpload: completion can move the row 'uploading' -> 'completing'
// after this SELECT, so a bare abort would destroy a multipart another path now owns while its own UPDATE
// affected zero rows. The guarded CAS claim makes the race safe — a row already moved off 'uploading' matches
// 0 rows and is left alone. Rows whose storage_cluster_id points to a peer cluster are skipped+logged.
func (j *PurgeDeletedJob) purgeStaleUploadingVODs(ctx context.Context) {
	queries := foghorndb.New(j.db)
	rows, err := queries.ListStaleUploadingVODs(ctx)
	if err != nil {
		j.logger.WithError(err).Error("Failed to query stale uploading VODs")
		return
	}
	type uploadRow struct {
		hash, tenantID, storageClusterID, originClusterID, backendID string
	}
	var batch []uploadRow
	for _, r := range rows {
		batch = append(batch, uploadRow{r.ArtifactHash, r.ATenantID, r.StorageClusterID, r.OriginClusterID, r.BackendID})
	}
	localBackendID := ""
	if j.cleaner != nil {
		localBackendID = j.cleaner.LocalBackendID
	}

	var claimed int
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
		// FENCE before claiming: only take ownership of a multipart this cell owns (recorded backend == local). A
		// foreign/unattributed row is left untouched, so the purge never routes an upload on another backend into the
		// local aborting saga.
		if ownErr := artifacts.VerifyLocalMultipartOwnership(r.backendID, localBackendID); ownErr != nil {
			j.logger.WithError(ownErr).WithField("artifact_hash", r.hash).Warn("Stale uploading VOD failed ownership fence; leaving untouched")
			continue
		}
		// Guarded CAS claim 'uploading' -> 'aborting', tenant-scoped. Do NOT touch S3 here — the AbortingVodRecoveryJob
		// aborts the multipart idempotently and converges to 'deleted'. A row concurrently completed matches 0 rows.
		n, errClaim := queries.ClaimStaleUploadingVOD(ctx, foghorndb.ClaimStaleUploadingVODParams{ArtifactHash: r.hash, TenantID: r.tenantID})
		if errClaim != nil {
			j.logger.WithError(errClaim).WithField("artifact_hash", r.hash).Warn("Stale upload abort-claim failed; will retry next cycle")
			continue
		}
		if n == 0 {
			continue // concurrently completed/aborted — left alone
		}
		claimed++
	}
	if claimed > 0 {
		j.logger.WithField("count", claimed).Info("Claimed stale uploading VODs for abort (aborting saga will tear down S3)")
	}
}

func (j *PurgeDeletedJob) purgeStaleNodeRows(ctx context.Context) {
	affected, err := foghorndb.New(j.db).PurgeStaleArtifactNodes(ctx)
	if err != nil {
		j.logger.WithError(err).Error("Failed to purge stale artifact_nodes entries")
		return
	}
	if affected > 0 {
		j.logger.WithField("count", affected).Info("Purged stale artifact_nodes entries")
	}
}
