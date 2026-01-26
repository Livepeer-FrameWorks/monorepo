package jobs

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/pkg/clients/decklog"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

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

	// Mark expired artifacts as deleted
	// Uses retention_until when set, falls back to created_at + default retention days
	// This supports both new artifacts (with retention_until) and legacy artifacts (without)
	rows, err := j.db.QueryContext(ctx, `
		UPDATE foghorn.artifacts
		SET status = 'deleted', updated_at = NOW()
		WHERE status NOT IN ('deleted', 'failed')
		  AND (
			-- Use retention_until if set
			(retention_until IS NOT NULL AND retention_until < NOW())
			OR
			-- Fallback to created_at + default retention for legacy artifacts
			(retention_until IS NULL AND created_at < NOW() - make_interval(days => $1))
		  )
		RETURNING artifact_hash, artifact_type, internal_name, tenant_id, user_id, size_bytes,
		          retention_until, started_at, ended_at, manifest_path
	`, j.retentionDays)

	if err != nil {
		j.logger.WithError(err).Error("Failed to expire artifacts")
		return
	}
	defer rows.Close()

	affected := 0
	for rows.Next() {
		var (
			artifactHash   string
			artifactType   string
			internalName   sql.NullString
			tenantID       sql.NullString
			userID         sql.NullString
			sizeBytes      sql.NullInt64
			retentionUntil sql.NullTime
			startedAt      sql.NullTime
			endedAt        sql.NullTime
			manifestPath   sql.NullString
		)
		if err := rows.Scan(
			&artifactHash,
			&artifactType,
			&internalName,
			&tenantID,
			&userID,
			&sizeBytes,
			&retentionUntil,
			&startedAt,
			&endedAt,
			&manifestPath,
		); err != nil {
			j.logger.WithError(err).Warn("Failed to scan expired artifact")
			continue
		}

		affected++
		j.emitDeletionLifecycle(ctx, artifactHash, artifactType, internalName, tenantID, userID, sizeBytes, retentionUntil, startedAt, endedAt, manifestPath)
	}
	if err := rows.Err(); err != nil {
		j.logger.WithError(err).Warn("Failed to iterate expired artifacts")
	}
	if affected > 0 {
		j.logger.WithField("count", affected).Info("Marked expired artifacts as deleted")
	}
}

func (j *RetentionJob) emitDeletionLifecycle(
	ctx context.Context,
	artifactHash string,
	artifactType string,
	internalName sql.NullString,
	tenantID sql.NullString,
	userID sql.NullString,
	sizeBytes sql.NullInt64,
	retentionUntil sql.NullTime,
	startedAt sql.NullTime,
	endedAt sql.NullTime,
	manifestPath sql.NullString,
) {
	if j.decklogClient == nil {
		return
	}

	switch artifactType {
	case "clip":
		j.emitClipDeleted(ctx, artifactHash, internalName, tenantID, userID, sizeBytes, retentionUntil)
	case "dvr":
		j.emitDVRDeleted(ctx, artifactHash, internalName, tenantID, userID, sizeBytes, retentionUntil, startedAt, endedAt, manifestPath)
	case "upload":
		j.emitVodDeleted(ctx, artifactHash, tenantID, userID, sizeBytes, retentionUntil)
	}
}

func (j *RetentionJob) emitClipDeleted(
	ctx context.Context,
	clipHash string,
	internalName sql.NullString,
	tenantID sql.NullString,
	userID sql.NullString,
	sizeBytes sql.NullInt64,
	retentionUntil sql.NullTime,
) {
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
			if resp.InternalName != "" {
				internalNameStr = resp.InternalName
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

	clipData := &pb.ClipLifecycleData{
		Stage:    pb.ClipLifecycleData_STAGE_DELETED,
		ClipHash: clipHash,
	}
	if tenantIDStr != "" {
		clipData.TenantId = &tenantIDStr
	}
	if userIDStr != "" {
		clipData.UserId = &userIDStr
	}
	if internalNameStr != "" {
		clipData.InternalName = &internalNameStr
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

	go func() {
		_ = j.decklogClient.SendClipLifecycle(clipData)
	}()
}

func (j *RetentionJob) emitDVRDeleted(
	ctx context.Context,
	dvrHash string,
	internalName sql.NullString,
	tenantID sql.NullString,
	userID sql.NullString,
	sizeBytes sql.NullInt64,
	retentionUntil sql.NullTime,
	startedAt sql.NullTime,
	endedAt sql.NullTime,
	manifestPath sql.NullString,
) {
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
			if resp.InternalName != "" {
				internalNameStr = resp.InternalName
			}
			if resp.StreamId != "" {
				streamID = resp.StreamId
			}
		}
	}

	dvrData := &pb.DVRLifecycleData{
		Status:  pb.DVRLifecycleData_STATUS_DELETED,
		DvrHash: dvrHash,
	}
	if tenantIDStr != "" {
		dvrData.TenantId = &tenantIDStr
	}
	if userIDStr != "" {
		dvrData.UserId = &userIDStr
	}
	if internalNameStr != "" {
		dvrData.InternalName = &internalNameStr
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
	if startedAt.Valid {
		st := startedAt.Time.Unix()
		dvrData.StartedAt = &st
	}
	if endedAt.Valid {
		et := endedAt.Time.Unix()
		dvrData.EndedAt = &et
	}
	if manifestPath.Valid && manifestPath.String != "" {
		dvrData.ManifestPath = &manifestPath.String
	}

	go func() {
		_ = j.decklogClient.SendDVRLifecycle(dvrData)
	}()
}

func (j *RetentionJob) emitVodDeleted(
	ctx context.Context,
	vodHash string,
	tenantID sql.NullString,
	userID sql.NullString,
	sizeBytes sql.NullInt64,
	retentionUntil sql.NullTime,
) {
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

	vodData := &pb.VodLifecycleData{
		Status:      pb.VodLifecycleData_STATUS_DELETED,
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

	go func() {
		_ = j.decklogClient.SendVodLifecycle(vodData)
	}()
}
