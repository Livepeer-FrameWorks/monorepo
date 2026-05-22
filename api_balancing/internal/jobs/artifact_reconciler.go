package jobs

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"github.com/lib/pq"

	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

// ReconcilerS3Client defines S3 operations needed by the artifact reconciler.
type ReconcilerS3Client interface {
	GeneratePresignedPUT(key string, expiry time.Duration) (string, error)
	BuildClipS3Key(tenantID, streamName, clipHash, format string) string
	BuildDVRS3Key(tenantID, internalName, dvrHash string) string
	BuildVodS3Key(tenantID, artifactHash, filename string) string
}

// FreezeRequestSender sends a FreezeRequest to a specific node.
type FreezeRequestSender func(nodeID string, req *pb.FreezeRequest) error

// maxArtifactRetries caps the number of generic-freeze retries for a single
// artifact before it is left in sync_status='failed' as a terminal-by-budget
// tombstone. Operators can manually re-enqueue by resetting failure_count.
const maxArtifactRetries = 8

// ReconcilerCommodoreClient defines Commodore operations needed by the reconciler.
type ReconcilerCommodoreClient interface {
	ResolveClipHash(ctx context.Context, hash string) (*pb.ResolveClipHashResponse, error)
	ResolveDVRHash(ctx context.Context, hash string) (*pb.ResolveDVRHashResponse, error)
	ResolveVodHash(ctx context.Context, hash string) (*pb.ResolveVodHashResponse, error)
	MarkArtifactThumbnailsReady(ctx context.Context, tenantID string, assetType pb.ArtifactAssetType, assetKey, storageClusterID string) (*pb.MarkArtifactThumbnailsReadyResponse, error)
	UpdateArtifactStorageCluster(ctx context.Context, tenantID string, assetType pb.ArtifactAssetType, assetKey, storageClusterID string) (*pb.UpdateArtifactStorageClusterResponse, error)
	UpdateArtifactSize(ctx context.Context, tenantID string, assetType pb.ArtifactAssetType, assetKey string, sizeBytes int64) (*pb.UpdateArtifactSizeResponse, error)
}

// ArtifactReconcilerConfig holds configuration for the reconciler job.
type ArtifactReconcilerConfig struct {
	DB              *sql.DB
	S3Client        ReconcilerS3Client
	CommodoreClient ReconcilerCommodoreClient
	SendFreeze      FreezeRequestSender
	Logger          logging.Logger
	Interval        time.Duration // How often to run (default: 5 minutes)
	BatchSize       int           // Max artifacts per pass (default: 50)
}

// ArtifactReconciler periodically scans for artifacts that need sync and
// proactively sends FreezeRequests to the nodes holding them.
type ArtifactReconciler struct {
	db         *sql.DB
	s3Client   ReconcilerS3Client
	commodore  ReconcilerCommodoreClient
	sendFreeze FreezeRequestSender
	logger     logging.Logger
	interval   time.Duration
	batchSize  int
	stopCh     chan struct{}
	triggerCh  chan struct{}
	wg         sync.WaitGroup
}

func NewArtifactReconciler(cfg ArtifactReconcilerConfig) *ArtifactReconciler {
	interval := cfg.Interval
	if interval == 0 {
		interval = 5 * time.Minute
	}
	batchSize := cfg.BatchSize
	if batchSize == 0 {
		batchSize = 50
	}
	return &ArtifactReconciler{
		db:         cfg.DB,
		s3Client:   cfg.S3Client,
		commodore:  cfg.CommodoreClient,
		sendFreeze: cfg.SendFreeze,
		logger:     cfg.Logger,
		interval:   interval,
		batchSize:  batchSize,
		stopCh:     make(chan struct{}),
		triggerCh:  make(chan struct{}, 1),
	}
}

func (r *ArtifactReconciler) Start() {
	r.wg.Add(1)
	go r.run()
	r.logger.Info("Artifact reconciler started")
}

func (r *ArtifactReconciler) Stop() {
	close(r.stopCh)
	r.wg.Wait()
	r.logger.Info("Artifact reconciler stopped")
}

// Trigger requests an immediate reconciliation pass. Multiple concurrent
// triggers are coalesced into a single pending pass.
func (r *ArtifactReconciler) Trigger() {
	if r == nil || r.triggerCh == nil {
		return
	}
	select {
	case r.triggerCh <- struct{}{}:
	default:
	}
}

func (r *ArtifactReconciler) run() {
	defer r.wg.Done()
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-r.triggerCh:
			r.reconcile()
		case <-ticker.C:
			r.reconcile()
		case <-r.stopCh:
			return
		}
	}
}

func (r *ArtifactReconciler) reconcile() {
	if r.db == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	conn, err := r.db.Conn(ctx)
	if err != nil {
		r.logger.WithError(err).Warn("Failed to acquire DB connection for reconciler lock")
		return
	}
	defer conn.Close()

	var acquired bool
	err = conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock(hashtext('artifact_reconciler'))").Scan(&acquired)
	if err != nil || !acquired {
		return
	}
	defer conn.ExecContext(ctx, "SELECT pg_advisory_unlock(hashtext('artifact_reconciler'))") //nolint:errcheck

	projected := r.projectCommodoreArtifactState(ctx)
	if r.s3Client == nil || r.sendFreeze == nil {
		if projected > 0 {
			r.logger.WithField("projected", projected).Info("Artifact projection repair pass complete")
		}
		return
	}
	reconciled := r.reconcileOrphaned(ctx)
	retried := r.retryFailed(ctx)
	advanced := r.advancePending(ctx)

	if retried+advanced+reconciled+projected > 0 {
		r.logger.WithFields(logging.Fields{
			"retried":    retried,
			"advanced":   advanced,
			"reconciled": reconciled,
			"projected":  projected,
		}).Info("Artifact reconciliation pass complete")
	}
}

func (r *ArtifactReconciler) projectCommodoreArtifactState(ctx context.Context) int {
	if r.commodore == nil {
		return 0
	}
	var rows *sql.Rows
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var err error
		//nolint:sqlclosecheck // rows is closed by caller after retry succeeds.
		rows, err = r.db.QueryContext(ctx, `
			SELECT artifact_hash, artifact_type, tenant_id::text,
			       COALESCE(storage_cluster_id, ''), COALESCE(origin_cluster_id, ''),
			       COALESCE(has_thumbnails, false), COALESCE(size_bytes, 0)
			FROM foghorn.artifacts
			WHERE status != 'deleted'
			  AND tenant_id IS NOT NULL
			  AND (COALESCE(storage_cluster_id, '') <> ''
			       OR COALESCE(has_thumbnails, false) = TRUE
			       OR COALESCE(size_bytes, 0) > 0)
			ORDER BY updated_at DESC
			LIMIT $1
		`, r.batchSize)
		return err
	})
	if err != nil {
		r.logger.WithError(err).Warn("Failed to query artifacts for Commodore projection repair")
		return 0
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var hash, artifactType, tenantID, storageCluster, originCluster string
		var hasThumbnails bool
		var sizeBytes int64
		if err := rows.Scan(&hash, &artifactType, &tenantID, &storageCluster, &originCluster, &hasThumbnails, &sizeBytes); err != nil {
			r.logger.WithError(err).Warn("Failed to scan artifact projection row")
			continue
		}
		assetType, ok := artifactAssetTypeFromString(artifactType)
		if !ok {
			continue
		}
		if storageCluster != "" {
			if _, err := r.commodore.UpdateArtifactStorageCluster(ctx, tenantID, assetType, hash, storageCluster); err != nil {
				r.logger.WithError(err).WithField("artifact_hash", hash).Warn("Failed to repair Commodore artifact storage projection")
				continue
			}
			count++
		}
		thumbnailCluster := storageCluster
		if thumbnailCluster == "" {
			thumbnailCluster = originCluster
		}
		if hasThumbnails && thumbnailCluster != "" {
			if _, err := r.commodore.MarkArtifactThumbnailsReady(ctx, tenantID, assetType, hash, thumbnailCluster); err != nil {
				r.logger.WithError(err).WithField("artifact_hash", hash).Warn("Failed to repair Commodore artifact thumbnail projection")
				continue
			}
			count++
		}
		if sizeBytes > 0 {
			if _, err := r.commodore.UpdateArtifactSize(ctx, tenantID, assetType, hash, sizeBytes); err != nil {
				r.logger.WithError(err).WithField("artifact_hash", hash).Warn("Failed to repair Commodore artifact size projection")
				continue
			}
			count++
		}
	}
	return count
}

// retryFailed re-sends FreezeRequests for artifacts with sync_status='failed'.
func (r *ArtifactReconciler) retryFailed(ctx context.Context) int {
	// sync_status='failed' is retryable with an exponential backoff schedule
	// keyed off failure_count. After maxRetries the row stays 'failed' but is
	// excluded from future retry scans — operator-visible terminal-by-budget.
	// lost_local is already terminal (separate filter via sync_status='failed').
	// DVR rows use the segment ledger and are excluded from generic freeze.
	var rows *sql.Rows
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var err error
		//nolint:sqlclosecheck // rows is closed by caller after retry succeeds.
		rows, err = r.db.QueryContext(ctx, `
			SELECT a.artifact_hash, a.artifact_type, COALESCE(a.stream_internal_name,''), a.tenant_id, a.format,
			       an.node_id, an.file_path
			FROM foghorn.artifacts a
			JOIN foghorn.artifact_nodes an ON a.artifact_hash = an.artifact_hash
			WHERE a.sync_status = 'failed'
			  AND a.artifact_type != 'dvr'
			  AND a.failure_count < $2
			  AND a.updated_at < NOW() - LEAST(
			      INTERVAL '5 minutes' * (1 << LEAST(a.failure_count, 4)),
			      INTERVAL '1 hour'
			    )
			  AND a.status != 'deleted'
			  AND an.is_orphaned = false
			ORDER BY a.updated_at ASC
			LIMIT $1
		`, r.batchSize, maxArtifactRetries)
		return err
	})
	if err != nil {
		r.logger.WithError(err).Warn("Failed to query failed artifacts for retry")
		return 0
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var hash, assetType, streamName, tenantID, nodeID, filePath string
		var format sql.NullString
		if err := rows.Scan(&hash, &assetType, &streamName, &tenantID, &format, &nodeID, &filePath); err != nil {
			r.logger.WithError(err).Warn("Failed to scan failed artifact row")
			continue
		}

		if err := r.sendFreezeForArtifact(ctx, hash, assetType, streamName, tenantID, format.String, nodeID, filePath); err != nil {
			r.logger.WithError(err).WithField("artifact_hash", hash).Warn("Failed to send freeze retry")
			continue
		}
		count++
	}
	return count
}

// advancePending sends FreezeRequests for pending artifacts that have never been synced.
func (r *ArtifactReconciler) advancePending(ctx context.Context) int {
	rows, err := r.db.QueryContext(ctx, `
		SELECT a.artifact_hash, a.artifact_type, COALESCE(a.stream_internal_name,''), a.tenant_id, a.format,
		       an.node_id, an.file_path
		FROM foghorn.artifacts a
		JOIN foghorn.artifact_nodes an ON a.artifact_hash = an.artifact_hash
		WHERE a.sync_status = 'pending'
		  AND a.artifact_type != 'dvr'
		  AND a.storage_location = 'local'
		  AND a.status != 'deleted'
		  AND an.is_orphaned = false
		ORDER BY a.created_at ASC
		LIMIT $1
	`, r.batchSize)
	if err != nil {
		r.logger.WithError(err).Warn("Failed to query pending artifacts")
		return 0
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var hash, assetType, streamName, tenantID, nodeID, filePath string
		var format sql.NullString
		if err := rows.Scan(&hash, &assetType, &streamName, &tenantID, &format, &nodeID, &filePath); err != nil {
			r.logger.WithError(err).Warn("Failed to scan pending artifact row")
			continue
		}

		if err := r.sendFreezeForArtifact(ctx, hash, assetType, streamName, tenantID, format.String, nodeID, filePath); err != nil {
			r.logger.WithError(err).WithField("artifact_hash", hash).Warn("Failed to send freeze for pending artifact")
			continue
		}
		count++
	}
	return count
}

// reconcileOrphaned ensures edge-reported artifacts are tracked in the cluster
// index. The event-driven path (CreateClip/StartDVR/etc.) creates lifecycle rows
// on the happy path, but edge ↔ cluster mismatches can occur from reconnections,
// restarts, or race conditions. This pass catches any artifact a node reports
// that the cluster doesn't know about and creates both the lifecycle row and the
// artifact_nodes row so advancePending can pick it up for S3 sync.
func (r *ArtifactReconciler) reconcileOrphaned(ctx context.Context) int {
	if r.commodore == nil {
		return 0
	}

	snapshot := state.DefaultManager().GetAllNodesSnapshot()
	type candidate struct {
		hash      string
		nodeID    string
		filePath  string
		sizeBytes uint64
		assetType string
		format    string
	}
	seen := make(map[string]bool)
	var candidates []candidate
	for _, node := range snapshot.Nodes {
		for _, a := range node.Artifacts {
			if a.ClipHash == "" || seen[a.ClipHash] {
				continue
			}
			seen[a.ClipHash] = true
			aType := artifactTypeFromProto(a.ArtifactType)
			if aType == "" {
				aType = r.inferAssetType(a.FilePath)
			}
			candidates = append(candidates, candidate{
				hash:      a.ClipHash,
				nodeID:    node.NodeID,
				filePath:  a.FilePath,
				sizeBytes: a.SizeBytes,
				assetType: aType,
				format:    a.Format,
			})
		}
	}
	if len(candidates) == 0 {
		return 0
	}

	// Batch-check which hashes already have lifecycle rows
	hashes := make([]string, 0, len(candidates))
	for _, c := range candidates {
		hashes = append(hashes, c.hash)
	}
	existing := make(map[string]bool, len(hashes))
	rows, err := r.db.QueryContext(ctx, `
		SELECT artifact_hash FROM foghorn.artifacts
		WHERE artifact_hash = ANY($1::text[])
	`, pq.Array(hashes))
	if err != nil {
		r.logger.WithError(err).Warn("Failed to batch-check artifact lifecycle rows")
		return 0
	}
	defer rows.Close()
	for rows.Next() {
		var h string
		if rows.Scan(&h) == nil {
			existing[h] = true
		}
	}

	count := 0
	for _, c := range candidates {
		if count >= r.batchSize || existing[c.hash] {
			continue
		}

		// DVR uses the segment ledger (foghorn.dvr_segments) as the source of
		// truth. Generic-freeze sync is for clips/VOD only. Sidecar startup
		// reconciles its local DVR directory against the ledger; Foghorn does
		// not have a playlist or PDT timing, so it cannot reconstruct the
		// ledger from this orphan-discovery context.
		if c.assetType == "dvr" {
			r.logger.WithFields(logging.Fields{
				"artifact_hash": c.hash,
				"node_id":       c.nodeID,
			}).Warn("Skipping DVR orphan in generic discovery; ledger reconstruction is sidecar-owned")
			continue
		}

		tenantID, internalName, err := r.resolveArtifactContext(ctx, c.hash, c.assetType)
		if err != nil {
			r.logger.WithFields(logging.Fields{
				"artifact_hash": c.hash,
				"asset_type":    c.assetType,
				"error":         err,
			}).Debug("Cannot resolve tenant for untracked artifact — skipping")
			continue
		}

		tx, err := r.db.BeginTx(ctx, nil)
		if err != nil {
			continue
		}

		_, err = tx.ExecContext(ctx, `
			INSERT INTO foghorn.artifacts
				(artifact_hash, artifact_type, stream_internal_name, tenant_id,
				 format, storage_location, sync_status, created_at, updated_at)
			VALUES ($1, $2, $3, $4, NULLIF($5,''), 'local', 'pending', NOW(), NOW())
			ON CONFLICT (artifact_hash) DO NOTHING
		`, c.hash, c.assetType, internalName, tenantID, c.format)
		if err != nil {
			tx.Rollback() //nolint:errcheck
			continue
		}

		_, err = tx.ExecContext(ctx, `
			INSERT INTO foghorn.artifact_nodes
				(artifact_hash, node_id, file_path, size_bytes, last_seen_at, is_orphaned, cached_at)
			VALUES ($1, $2, $3, $4, NOW(), false, NOW())
			ON CONFLICT (artifact_hash, node_id) DO UPDATE SET
				file_path = EXCLUDED.file_path,
				size_bytes = EXCLUDED.size_bytes,
				last_seen_at = NOW(),
				is_orphaned = false
		`, c.hash, c.nodeID, c.filePath, c.sizeBytes)
		if err != nil {
			tx.Rollback() //nolint:errcheck
			continue
		}

		if err := tx.Commit(); err != nil {
			continue
		}

		r.logger.WithFields(logging.Fields{
			"artifact_hash": c.hash,
			"asset_type":    c.assetType,
			"tenant_id":     tenantID,
			"node_id":       c.nodeID,
		}).Info("Indexed untracked edge artifact")
		count++
	}
	return count
}

func artifactTypeFromProto(t pb.ArtifactEvent_ArtifactType) string {
	switch t {
	case pb.ArtifactEvent_ARTIFACT_TYPE_CLIP:
		return "clip"
	case pb.ArtifactEvent_ARTIFACT_TYPE_DVR:
		return "dvr"
	case pb.ArtifactEvent_ARTIFACT_TYPE_VOD:
		return "vod"
	default:
		return ""
	}
}

// sendFreezeForArtifact generates presigned URLs and sends a FreezeRequest to the node.
func (r *ArtifactReconciler) sendFreezeForArtifact(ctx context.Context, hash, assetType, streamName, tenantID, format, nodeID, filePath string) error {
	expiry := 30 * time.Minute
	requestID := fmt.Sprintf("reconcile-%s-%d", hash, time.Now().UnixMilli())

	req := &pb.FreezeRequest{
		RequestId:        requestID,
		AssetType:        assetType,
		AssetHash:        hash,
		InternalName:     streamName,
		LocalPath:        filePath,
		UrlExpirySeconds: int64(expiry.Seconds()),
	}

	switch assetType {
	case "clip":
		if streamName == "" {
			return fmt.Errorf("clip %s missing stream_internal_name", hash)
		}
		if format == "" {
			format = "mp4"
		}
		s3Key := r.s3Client.BuildClipS3Key(tenantID, streamName, hash, format)
		url, err := r.s3Client.GeneratePresignedPUT(s3Key, expiry)
		if err != nil {
			return fmt.Errorf("presign clip: %w", err)
		}
		req.PresignedPutUrl = url

	case "vod":
		if format == "" {
			format = "mp4"
		}
		s3Key := r.s3Client.BuildVodS3Key(tenantID, hash, fmt.Sprintf("%s.%s", hash, format))
		url, err := r.s3Client.GeneratePresignedPUT(s3Key, expiry)
		if err != nil {
			return fmt.Errorf("presign vod: %w", err)
		}
		req.PresignedPutUrl = url

	default:
		return fmt.Errorf("unsupported asset type: %s", assetType)
	}

	// Mark as in_progress before sending
	if _, dbErr := r.db.ExecContext(ctx, `
		UPDATE foghorn.artifacts
		SET storage_location = 'freezing', sync_status = 'in_progress', updated_at = NOW()
		WHERE artifact_hash = $1`, hash); dbErr != nil {
		r.logger.WithError(dbErr).WithField("artifact_hash", hash).Warn("Failed to mark artifact as freezing")
	}

	return r.sendFreeze(nodeID, req)
}

// resolveArtifactContext uses Commodore to find the tenant and stream for an artifact.
func (r *ArtifactReconciler) resolveArtifactContext(ctx context.Context, hash, assetType string) (tenantID string, streamName string, err error) {
	resolveCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	switch assetType {
	case "clip":
		resp, err := r.commodore.ResolveClipHash(resolveCtx, hash)
		if err != nil {
			return "", "", fmt.Errorf("resolve clip: %w", err)
		}
		if !resp.Found {
			return "", "", fmt.Errorf("clip %s not found in Commodore", hash)
		}
		return resp.TenantId, resp.StreamInternalName, nil

	case "dvr":
		resp, err := r.commodore.ResolveDVRHash(resolveCtx, hash)
		if err != nil {
			return "", "", fmt.Errorf("resolve dvr: %w", err)
		}
		if !resp.Found {
			return "", "", fmt.Errorf("dvr %s not found in Commodore", hash)
		}
		return resp.TenantId, resp.StreamInternalName, nil

	case "vod":
		resp, err := r.commodore.ResolveVodHash(resolveCtx, hash)
		if err != nil {
			return "", "", fmt.Errorf("resolve vod: %w", err)
		}
		if !resp.Found {
			return "", "", fmt.Errorf("vod %s not found in Commodore", hash)
		}
		return resp.TenantId, resp.InternalName, nil

	default:
		return "", "", fmt.Errorf("cannot resolve asset type: %s", assetType)
	}
}

func artifactAssetTypeFromString(t string) (pb.ArtifactAssetType, bool) {
	switch t {
	case "clip":
		return pb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_CLIP, true
	case "dvr", "dvr_segment", "dvr_manifest":
		return pb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_DVR, true
	case "vod":
		return pb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_VOD, true
	default:
		return pb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_UNSPECIFIED, false
	}
}

// inferAssetType guesses asset type from the file path when artifact_type is empty.
func (r *ArtifactReconciler) inferAssetType(filePath string) string {
	// DVR directories contain manifests; clips/vods are single files
	// This is a best-effort heuristic for orphaned artifacts
	if filePath != "" {
		// DVR paths typically end in a hash (directory), clip/vod end in a file extension
		if ext := getExtension(filePath); ext == "" {
			return "dvr"
		}
	}
	return "clip"
}

func getExtension(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '.' {
			return path[i+1:]
		}
		if path[i] == '/' {
			return ""
		}
	}
	return ""
}
