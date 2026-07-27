package jobs

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"frameworks/api_balancing/internal/artifactoutbox"
	"frameworks/api_balancing/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/decklog"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"google.golang.org/protobuf/proto"
)

// RetentionJob identifies expired assets and marks them as deleted
// This triggers the standard deletion flow:
// 1. Mark as deleted in DB
// 2. OrphanCleanupJob detects deleted record with artifacts
// 3. OrphanCleanupJob sends delete request to storage node (Helmsman)
// 4. Helmsman deletes local files (and notifies Foghorn)
// 5. PurgeDeletedJob eventually hard-deletes the DB record
type RetentionJob struct {
	db            *sql.DB
	logger        logging.Logger
	interval      time.Duration
	retentionDays int // Default retention in days
	decklogClient *decklog.BatchedClient
	stopCh        chan struct{}
	wg            sync.WaitGroup
}

// RetentionConfig holds configuration for the retention job
type RetentionConfig struct {
	DB            *sql.DB
	Logger        logging.Logger
	Interval      time.Duration // How often to run (default: 1 hour)
	RetentionDays int           // Default retention (default: 30 days)
	DecklogClient *decklog.BatchedClient
}

// NewRetentionJob creates a new retention job
func NewRetentionJob(cfg RetentionConfig) *RetentionJob {
	interval := cfg.Interval
	if interval == 0 {
		interval = 1 * time.Hour
	}
	retentionDays := cfg.RetentionDays
	if retentionDays == 0 {
		retentionDays = 30
	}
	return &RetentionJob{
		db:            cfg.DB,
		logger:        cfg.Logger,
		interval:      interval,
		retentionDays: retentionDays,
		decklogClient: cfg.DecklogClient,
		stopCh:        make(chan struct{}),
	}
}

// Start begins the background retention loop
func (j *RetentionJob) Start() {
	j.wg.Add(1)
	go j.run()
	j.logger.Info("Retention job started")
}

// Stop gracefully stops the job
func (j *RetentionJob) Stop() {
	close(j.stopCh)
	j.wg.Wait()
	j.logger.Info("Retention job stopped")
}

func (j *RetentionJob) run() {
	defer j.wg.Done()
	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()

	// Run once at startup (staggered by 5 minutes)
	time.AfterFunc(5*time.Minute, func() {
		j.scan()
	})

	for {
		select {
		case <-ticker.C:
			j.scan()
		case <-j.stopCh:
			return
		}
	}
}

func (j *RetentionJob) scan() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	j.logger.Info("Starting retention scan")

	// Retention only acts on terminal artifacts. An active DVR (recording /
	// finalizing) is never killed by retention: retention_until on a DVR is
	// set by FinalizeDVR at end_at + dvr_retention_days*24h (post-end
	// semantics), so it never starts ticking on started_at.
	//
	// 'requested' and 'starting' are pre-terminal in the new state
	// machine; they only show up briefly and are excluded so a stuck
	// pre-recording artifact doesn't get silently cleaned up.
	// SELECT the eligible artifacts (do NOT bulk-delete): each is then expired in its OWN
	// transaction below, so the soft-delete and its deletion-lifecycle outbox event commit
	// atomically. A bulk UPDATE followed by a fire-and-forget emit loses the analytics event on a
	// crash/nil-client/enqueue error. Bounded per pass so the per-row work can't run unbounded.
	rows, err := j.db.QueryContext(ctx, `
		SELECT artifact_hash, artifact_type, stream_internal_name, tenant_id, user_id, size_bytes,
		       retention_until, started_at, ended_at, manifest_path
		  FROM foghorn.artifacts
		WHERE status IN ('completed', 'completed_partial', 'ready', 'failed')
		  AND (
				-- Explicit deadline: applies to every artifact type INCLUDING dvr chapters. A
				-- chapter inherits its parent DVR's retention_until at allocation (keep-forever ⇒
				-- NULL), so once that horizon elapses the chapter expires with the parent here.
				(retention_until IS NOT NULL AND retention_until < NOW())
				OR
				-- Global VOD-age fallback for legacy clip/vod rows without an explicit deadline.
				-- DVR rows always have retention_until set by FinalizeDVR. DVR CHAPTERS are excluded
				-- from THIS branch only: a keep-forever parent leaves the chapter's retention_until
				-- NULL, and the chapter must not then be reaped by the global age fallback.
				(artifact_type <> 'dvr'
				 AND COALESCE(origin_type, '') <> 'dvr_chapter'
				 AND retention_until IS NULL
				 AND created_at < NOW() - make_interval(days => $1))
			  )
		LIMIT 500
	`, j.retentionDays)

	if err != nil {
		j.logger.WithError(err).Error("Failed to query expired artifacts")
		return
	}

	type expiredRow struct {
		hash, artifactType             string
		internalName, tenantID, userID sql.NullString
		sizeBytes                      sql.NullInt64
		retentionUntil                 sql.NullTime
		startedAt, endedAt             sql.NullTime
		manifestPath                   sql.NullString
	}
	var candidates []expiredRow
	for rows.Next() {
		var r expiredRow
		if scanErr := rows.Scan(&r.hash, &r.artifactType, &r.internalName, &r.tenantID, &r.userID,
			&r.sizeBytes, &r.retentionUntil, &r.startedAt, &r.endedAt, &r.manifestPath); scanErr != nil {
			j.logger.WithError(scanErr).Warn("Failed to scan expired artifact")
			continue
		}
		candidates = append(candidates, r)
	}
	rows.Close() //nolint:sqlclosecheck // close the cursor before opening per-artifact txns
	if rowsErr := rows.Err(); rowsErr != nil {
		j.logger.WithError(rowsErr).Warn("Failed to iterate expired artifacts")
	}

	affected := 0
	for _, r := range candidates {
		if j.expireArtifactTx(ctx, r.hash, r.artifactType, r.internalName, r.tenantID, r.userID,
			r.sizeBytes, r.retentionUntil) {
			affected++
		}
	}

	if affected > 0 {
		j.logger.WithField("count", affected).Info("Marked expired artifacts as deleted")
	}
}

// expireArtifactTx expires ONE artifact atomically and returns true when it was actually deleted.
// The deletion event is BUILT FIRST (with optional Commodore enrichment) OUTSIDE the transaction —
// so no network call is held while the tx is open — then a single transaction: re-applies the FULL
// expiry predicate under the row (a retention EXTEND/CLEAR since the SELECT prevents deletion),
// for a DVR cascades its children + chapter rows + per-child events, and enqueues the parent
// deletion event. All commit together or not at all; a 0-row guard means it's no longer eligible.
func (j *RetentionJob) expireArtifactTx(
	ctx context.Context,
	hash, artifactType string,
	internalName, tenantID, userID sql.NullString,
	sizeBytes sql.NullInt64,
	retentionUntil sql.NullTime,
) bool {
	// FAIL CLOSED on an unknown artifact type: expiry applies type-specific deletion + cascade
	// semantics, and defaulting a future/malformed type to VOD deletion could delete the wrong bytes
	// or skip a required cascade. Only the known types are expired here; anything else is left in place
	// (operator-visible) rather than swept with the wrong semantics. dvr_chapter rows are artifact_type
	// 'vod' (origin_type carries the chapter marker), so they pass this guard.
	switch artifactType {
	case "clip", "dvr", "vod":
	default:
		j.logger.WithFields(logging.Fields{"artifact_hash": hash, "artifact_type": artifactType}).
			Warn("Retention: refusing to expire artifact of unknown type (fail closed)")
		return false
	}

	// Build the event (enrichment RPCs) before opening the tx.
	event := j.buildDeletionEvent(ctx, hash, artifactType, internalName, tenantID, userID, sizeBytes, retentionUntil)

	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		j.logger.WithError(err).WithField("artifact_hash", hash).Warn("Retention: begin expire tx failed")
		return false
	}
	committed := false
	defer func() {
		if !committed {
			tx.Rollback() //nolint:errcheck // best-effort rollback of an uncommitted tx
		}
	}()

	// Guarded soft-delete that RE-EVALUATES the full expiry predicate (not just terminal status),
	// so a concurrent retention change since candidate selection actually prevents deletion.
	var deletedTenant sql.NullString
	scanErr := tx.QueryRowContext(ctx, `
		UPDATE foghorn.artifacts SET status = 'deleted', updated_at = NOW()
		 WHERE artifact_hash = $1
		   AND status IN ('completed', 'completed_partial', 'ready', 'failed')
		   AND (
				(retention_until IS NOT NULL AND retention_until < NOW())
				OR (artifact_type <> 'dvr'
				    AND COALESCE(origin_type, '') <> 'dvr_chapter'
				    AND retention_until IS NULL
				    AND created_at < NOW() - make_interval(days => $2))
		   )
		RETURNING tenant_id::text
	`, hash, j.retentionDays).Scan(&deletedTenant)
	if errors.Is(scanErr, sql.ErrNoRows) {
		return false // no longer eligible (retention extended/cleared, or already deleted)
	}
	if scanErr != nil {
		j.logger.WithError(scanErr).WithField("artifact_hash", hash).Warn("Retention: soft-delete failed")
		return false
	}

	// A DVR cascades to its chapters in the SAME tx (children soft-delete + chapter rows + per-
	// child VOD-deleted events), exactly like an explicit delete — so a 'finalizing' chapter of a
	// now-deleted parent isn't re-dispatched forever.
	if artifactType == "dvr" {
		tenant := ""
		if deletedTenant.Valid {
			tenant = deletedTenant.String
		}
		if _, cErr := control.CascadeDVRChildrenTx(ctx, tx, hash, tenant); cErr != nil {
			j.logger.WithError(cErr).WithField("dvr_hash", hash).Warn("Retention: cascade child chapters failed; rolling back")
			return false
		}
	}

	if err := enqueueBuiltDeletionEvent(ctx, tx, event); err != nil {
		j.logger.WithError(err).WithField("artifact_hash", hash).Warn("Retention: enqueue deletion lifecycle failed; rolling back")
		return false
	}

	if commitErr := tx.Commit(); commitErr != nil {
		j.logger.WithError(commitErr).WithField("artifact_hash", hash).Warn("Retention: commit expire failed")
		return false
	}
	committed = true
	return true
}

// buildDeletionEvent constructs the per-type deletion lifecycle proto (with best-effort Commodore
// enrichment) so the network calls happen BEFORE the deletion transaction opens. The durable core
// (hash + tenant + size + expiry) is always captured even when enrichment is unavailable.
func (j *RetentionJob) buildDeletionEvent(
	ctx context.Context,
	hash, artifactType string,
	internalName, tenantID, userID sql.NullString,
	sizeBytes sql.NullInt64,
	retentionUntil sql.NullTime,
) any {
	switch artifactType {
	case "clip":
		return j.buildClipDeletedEvent(ctx, hash, internalName, tenantID, userID, sizeBytes, retentionUntil)
	case "dvr":
		return j.buildDVRDeletedEvent(ctx, hash, internalName, tenantID, userID, sizeBytes, retentionUntil)
	case "vod":
		// dvr_chapter rows are artifact_type 'vod' (origin_type carries the chapter marker).
		return j.buildVodDeletedEvent(ctx, hash, tenantID, userID, sizeBytes, retentionUntil)
	default:
		// Unreachable — expireArtifactTx rejects unknown types before this. Explicit nil (no event)
		// rather than a VOD fallback, so a future/malformed type never silently gains VOD semantics.
		j.logger.WithFields(logging.Fields{"artifact_hash": hash, "artifact_type": artifactType}).
			Warn("Retention: no deletion event for unknown artifact type")
		return nil
	}
}

// enqueueBuiltDeletionEvent writes a pre-built deletion event onto the caller's transaction.
func enqueueBuiltDeletionEvent(ctx context.Context, tx *sql.Tx, event any) error {
	switch e := event.(type) {
	case *ipcpb.ClipLifecycleData:
		return artifactoutbox.EnqueueClipLifecycleTx(ctx, tx, e)
	case *ipcpb.DVRLifecycleData:
		return artifactoutbox.EnqueueDVRLifecycleTx(ctx, tx, e)
	case *ipcpb.VodLifecycleData:
		return artifactoutbox.EnqueueVodLifecycleTx(ctx, tx, e)
	default:
		return nil
	}
}

func (j *RetentionJob) buildClipDeletedEvent(
	ctx context.Context,
	clipHash string,
	internalName sql.NullString,
	tenantID sql.NullString,
	userID sql.NullString,
	sizeBytes sql.NullInt64,
	retentionUntil sql.NullTime,
) *ipcpb.ClipLifecycleData {
	var (
		tenantIDStr     string
		userIDStr       string
		internalNameStr string
		streamID        string
		clipMode        *string
		startUnix       *int64
		stopUnix        *int64
		startMs         *int64
		stopMs          *int64
		durationSec     *int64
	)

	if tenantID.Valid {
		tenantIDStr = tenantID.String
	}
	if userID.Valid {
		userIDStr = userID.String
	}
	if internalName.Valid {
		internalNameStr = internalName.String
	}

	if control.CommodoreClient != nil {
		cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if resp, err := control.CommodoreClient.ResolveClipHash(cctx, clipHash); err == nil && resp.Found {
			if resp.TenantId != "" {
				tenantIDStr = resp.TenantId
			}
			if resp.UserId != "" {
				userIDStr = resp.UserId
			}
			if resp.StreamInternalName != "" {
				internalNameStr = resp.StreamInternalName
			}
			if resp.StreamId != "" {
				streamID = resp.StreamId
			}
			if resp.ClipMode != "" {
				m := resp.ClipMode
				clipMode = &m
			}
			if resp.StartTime > 0 && resp.Duration > 0 {
				sMs := resp.StartTime
				eMs := resp.StartTime + resp.Duration
				sU := sMs / 1000
				eU := eMs / 1000
				dS := resp.Duration / 1000
				startMs, stopMs = &sMs, &eMs
				startUnix, stopUnix = &sU, &eU
				durationSec = &dS
			}
		}
	}

	clipData := &ipcpb.ClipLifecycleData{
		Stage:    ipcpb.ClipLifecycleData_STAGE_DELETED,
		ClipHash: clipHash,
	}
	if tenantIDStr != "" {
		clipData.TenantId = &tenantIDStr
	}
	if userIDStr != "" {
		clipData.UserId = &userIDStr
	}
	if internalNameStr != "" {
		clipData.StreamInternalName = &internalNameStr
	}
	if streamID != "" {
		clipData.StreamId = &streamID
	}
	if sizeBytes.Valid && sizeBytes.Int64 > 0 {
		sb := uint64(sizeBytes.Int64)
		clipData.SizeBytes = &sb
	}
	if retentionUntil.Valid {
		exp := retentionUntil.Time.Unix()
		clipData.ExpiresAt = &exp
	}
	clipData.ClipMode = clipMode
	clipData.StartUnix = startUnix
	clipData.StopUnix = stopUnix
	clipData.StartMs = startMs
	clipData.StopMs = stopMs
	clipData.DurationSec = durationSec

	return clipData
}

func (j *RetentionJob) buildDVRDeletedEvent(
	ctx context.Context,
	dvrHash string,
	internalName sql.NullString,
	tenantID sql.NullString,
	userID sql.NullString,
	sizeBytes sql.NullInt64,
	retentionUntil sql.NullTime,
) *ipcpb.DVRLifecycleData {
	var (
		tenantIDStr     string
		userIDStr       string
		internalNameStr string
		streamID        string
	)

	if tenantID.Valid {
		tenantIDStr = tenantID.String
	}
	if userID.Valid {
		userIDStr = userID.String
	}
	if internalName.Valid {
		internalNameStr = internalName.String
	}

	if control.CommodoreClient != nil {
		cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if resp, err := control.CommodoreClient.ResolveDVRHash(cctx, dvrHash); err == nil && resp.Found {
			if resp.TenantId != "" {
				tenantIDStr = resp.TenantId
			}
			if resp.UserId != "" {
				userIDStr = resp.UserId
			}
			if resp.StreamInternalName != "" {
				internalNameStr = resp.StreamInternalName
			}
			if resp.StreamId != "" {
				streamID = resp.StreamId
			}
		}
	}

	dvrData := &ipcpb.DVRLifecycleData{
		Status:  ipcpb.DVRLifecycleData_STATUS_DELETED,
		DvrHash: dvrHash,
	}
	if tenantIDStr != "" {
		dvrData.TenantId = &tenantIDStr
	}
	if userIDStr != "" {
		dvrData.UserId = &userIDStr
	}
	if internalNameStr != "" {
		dvrData.StreamInternalName = &internalNameStr
	}
	if streamID != "" {
		dvrData.StreamId = &streamID
	}
	if sizeBytes.Valid && sizeBytes.Int64 > 0 {
		sb := uint64(sizeBytes.Int64)
		dvrData.SizeBytes = &sb
	}
	if retentionUntil.Valid {
		exp := retentionUntil.Time.Unix()
		dvrData.ExpiresAt = &exp
	}

	return dvrData
}

func (j *RetentionJob) buildVodDeletedEvent(
	ctx context.Context,
	vodHash string,
	tenantID sql.NullString,
	userID sql.NullString,
	sizeBytes sql.NullInt64,
	retentionUntil sql.NullTime,
) *ipcpb.VodLifecycleData {
	var tenantIDStr string
	var userIDStr string
	if tenantID.Valid {
		tenantIDStr = tenantID.String
	}
	if userID.Valid {
		userIDStr = userID.String
	}

	if control.CommodoreClient != nil {
		cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if resp, err := control.CommodoreClient.ResolveVodHash(cctx, vodHash); err == nil && resp.Found {
			if resp.TenantId != "" {
				tenantIDStr = resp.TenantId
			}
			if resp.UserId != "" {
				userIDStr = resp.UserId
			}
		}
	}

	vodData := &ipcpb.VodLifecycleData{
		Status:      ipcpb.VodLifecycleData_STATUS_DELETED,
		VodHash:     vodHash,
		CompletedAt: proto.Int64(time.Now().Unix()),
	}
	if tenantIDStr != "" {
		vodData.TenantId = &tenantIDStr
	}
	if userIDStr != "" {
		vodData.UserId = &userIDStr
	}
	if sizeBytes.Valid && sizeBytes.Int64 > 0 {
		sb := uint64(sizeBytes.Int64)
		vodData.SizeBytes = &sb
	}
	if retentionUntil.Valid {
		exp := retentionUntil.Time.Unix()
		vodData.ExpiresAt = &exp
	}

	return vodData
}
