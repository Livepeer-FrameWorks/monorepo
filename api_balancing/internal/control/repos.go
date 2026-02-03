package control

import (
	"context"
	"database/sql"
	"encoding/json"

	"frameworks/api_balancing/internal/state"
	pb "frameworks/pkg/proto"
)

// ============================================================================
// UNIFIED ARTIFACT REPOSITORIES
// ============================================================================
// These repositories work with the new unified artifact model:
//   - foghorn.artifacts      = lifecycle state (1 row per artifact)
//   - foghorn.artifact_nodes = warm storage distribution (N rows per artifact)
//
// Business metadata (tenant_id, user_id, stream_id) is in Commodore.
// See: docs/architecture/CLIP_DVR_REGISTRY.md
// ============================================================================

// clipRepositoryDB implements state.ClipRepository using foghorn.artifacts
type clipRepositoryDB struct{}

func NewClipRepository() state.ClipRepository { return &clipRepositoryDB{} }

func (r *clipRepositoryDB) ListActiveClips(ctx context.Context) ([]state.ClipRecord, error) {
	if db == nil {
		return nil, sql.ErrConnDone
	}
	// Query artifacts table with type='clip', join with artifact_nodes to get node info
	rows, err := db.QueryContext(ctx, `
		SELECT a.artifact_hash, '' as tenant_id, COALESCE(a.internal_name,''),
		       COALESCE(n.node_id,''), a.status, COALESCE(n.file_path,''),
		       COALESCE(a.size_bytes,0), COALESCE(a.storage_location,'pending')
		FROM foghorn.artifacts a
		LEFT JOIN foghorn.artifact_nodes n ON a.artifact_hash = n.artifact_hash AND n.is_orphaned = false
		WHERE a.artifact_type = 'clip' AND a.status != 'deleted'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []state.ClipRecord
	for rows.Next() {
		var rec state.ClipRecord
		if err := rows.Scan(&rec.ClipHash, &rec.TenantID, &rec.InternalName, &rec.NodeID, &rec.Status, &rec.StoragePath, &rec.SizeBytes, &rec.StorageLocation); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (r *clipRepositoryDB) ResolveInternalNameByRequestID(ctx context.Context, requestID string) (string, error) {
	if db == nil {
		return "", sql.ErrConnDone
	}
	var internalName string
	err := db.QueryRowContext(ctx, `
		SELECT COALESCE(internal_name,'') FROM foghorn.artifacts
		WHERE request_id = $1 AND artifact_type = 'clip'
	`, requestID).Scan(&internalName)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return internalName, err
}

func (r *clipRepositoryDB) UpdateClipProgressByRequestID(ctx context.Context, requestID string, percent uint32) error {
	if db == nil {
		return sql.ErrConnDone
	}
	// Update artifact status based on progress
	status := "processing"
	if percent == 100 {
		status = "ready"
	}
	_, err := db.ExecContext(ctx, `
		UPDATE foghorn.artifacts
		SET status = $2, updated_at = NOW()
		WHERE request_id = $1 AND artifact_type = 'clip'
	`, requestID, status)
	return err
}

func (r *clipRepositoryDB) UpdateClipDoneByRequestID(ctx context.Context, requestID string, status string, storagePath string, sizeBytes int64, errorMsg string) error {
	if db == nil {
		return sql.ErrConnDone
	}
	var clipStatus string
	if status == "success" {
		clipStatus = "ready"
	} else {
		clipStatus = "failed"
	}
	_, err := db.ExecContext(ctx, `
		UPDATE foghorn.artifacts
		SET status = $1,
		    size_bytes = $3,
		    error_message = NULLIF($4, ''),
		    updated_at = NOW()
		WHERE request_id = $5 AND artifact_type = 'clip'
	`, clipStatus, storagePath, sizeBytes, errorMsg, requestID)
	return err
}

// NeedsDtshSync returns true if the clip is synced to S3 but .dtsh wasn't included
func (r *clipRepositoryDB) NeedsDtshSync(ctx context.Context, clipHash string) bool {
	if db == nil {
		return false
	}
	var needsSync bool
	err := db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM foghorn.artifacts
			WHERE artifact_hash = $1
			  AND artifact_type = 'clip'
			  AND frozen_at IS NOT NULL
			  AND COALESCE(dtsh_synced, false) = false
		)
	`, clipHash).Scan(&needsSync)
	if err != nil {
		return false
	}
	return needsSync
}

// dvrRepositoryDB implements state.DVRRepository using foghorn.artifacts
type dvrRepositoryDB struct{}

func NewDVRRepository() state.DVRRepository { return &dvrRepositoryDB{} }

func (r *dvrRepositoryDB) ListAllDVR(ctx context.Context) ([]state.DVRRecord, error) {
	if db == nil {
		return nil, sql.ErrConnDone
	}
	// Query artifacts table with type='dvr', join with artifact_nodes for node info
	rows, err := db.QueryContext(ctx, `
		SELECT a.artifact_hash, '' as tenant_id, COALESCE(a.internal_name,''),
		       COALESCE(n.node_id,''), COALESCE(n.base_url,''), a.status,
		       COALESCE(a.duration_seconds,0), COALESCE(a.size_bytes,0), COALESCE(a.manifest_path,'')
		FROM foghorn.artifacts a
		LEFT JOIN foghorn.artifact_nodes n ON a.artifact_hash = n.artifact_hash AND n.is_orphaned = false
		WHERE a.artifact_type = 'dvr'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []state.DVRRecord
	for rows.Next() {
		var rec state.DVRRecord
		if err := rows.Scan(&rec.Hash, &rec.TenantID, &rec.InternalName, &rec.StorageNodeID, &rec.SourceURL, &rec.Status, &rec.DurationSec, &rec.SizeBytes, &rec.ManifestPath); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (r *dvrRepositoryDB) ResolveInternalNameByHash(ctx context.Context, dvrHash string) (string, error) {
	if db == nil {
		return "", sql.ErrConnDone
	}
	var internalName string
	err := db.QueryRowContext(ctx, `
		SELECT COALESCE(internal_name,'') FROM foghorn.artifacts
		WHERE artifact_hash = $1 AND artifact_type = 'dvr'
	`, dvrHash).Scan(&internalName)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return internalName, err
}

func (r *dvrRepositoryDB) UpdateDVRProgressByHash(ctx context.Context, dvrHash string, status string, sizeBytes int64) error {
	if db == nil {
		return sql.ErrConnDone
	}
	_, err := db.ExecContext(ctx, `
		UPDATE foghorn.artifacts
		SET status = $2,
		    size_bytes = $3,
		    updated_at = NOW()
		WHERE artifact_hash = $1 AND artifact_type = 'dvr'
	`, dvrHash, status, sizeBytes)
	return err
}

func (r *dvrRepositoryDB) UpdateDVRCompletionByHash(ctx context.Context, dvrHash string, finalStatus string, durationSeconds int64, sizeBytes int64, manifestPath string, errorMsg string) error {
	if db == nil {
		return sql.ErrConnDone
	}
	_, err := db.ExecContext(ctx, `
		UPDATE foghorn.artifacts
		SET status = $1,
		    ended_at = NOW(),
		    duration_seconds = $2,
		    size_bytes = $3,
		    manifest_path = $4,
		    error_message = NULLIF($5, ''),
		    updated_at = NOW()
		WHERE artifact_hash = $6 AND artifact_type = 'dvr'
	`, finalStatus, durationSeconds, sizeBytes, manifestPath, errorMsg, dvrHash)
	return err
}

// NeedsDtshSync returns true if the DVR is synced to S3 but .dtsh files weren't included
func (r *dvrRepositoryDB) NeedsDtshSync(ctx context.Context, dvrHash string) bool {
	if db == nil {
		return false
	}
	var needsSync bool
	err := db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM foghorn.artifacts
			WHERE artifact_hash = $1
			  AND artifact_type = 'dvr'
			  AND frozen_at IS NOT NULL
			  AND COALESCE(dtsh_synced, false) = false
		)
	`, dvrHash).Scan(&needsSync)
	if err != nil {
		return false
	}
	return needsSync
}

// ============================================================================
// NODE REPOSITORY
// ============================================================================

type nodeRepositoryDB struct{}

func NewNodeRepository() state.NodeRepository { return &nodeRepositoryDB{} }

func (r *nodeRepositoryDB) ListAllNodes(ctx context.Context) ([]state.NodeRecord, error) {
	if db == nil {
		return nil, sql.ErrConnDone
	}
	rows, err := db.QueryContext(ctx, `SELECT node_id, COALESCE(base_url,''), COALESCE(outputs,'{}') FROM foghorn.node_outputs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []state.NodeRecord
	for rows.Next() {
		var rec state.NodeRecord
		var outputsJSON string
		if err := rows.Scan(&rec.NodeID, &rec.BaseURL, &outputsJSON); err != nil {
			return nil, err
		}
		rec.OutputsJSON = outputsJSON
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (r *nodeRepositoryDB) ListNodeMaintenance(ctx context.Context) ([]state.NodeMaintenanceRecord, error) {
	if db == nil {
		return nil, sql.ErrConnDone
	}
	rows, err := db.QueryContext(ctx, `
		SELECT node_id, mode, set_at, COALESCE(set_by, '')
		FROM foghorn.node_maintenance
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []state.NodeMaintenanceRecord
	for rows.Next() {
		var rec state.NodeMaintenanceRecord
		var mode string
		if err := rows.Scan(&rec.NodeID, &mode, &rec.SetAt, &rec.SetBy); err != nil {
			return nil, err
		}
		rec.Mode = state.NodeOperationalMode(mode)
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (r *nodeRepositoryDB) UpsertNodeOutputs(ctx context.Context, nodeID string, baseURL string, outputsJSON string) error {
	if db == nil {
		return sql.ErrConnDone
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO foghorn.node_outputs (node_id, base_url, outputs, last_updated)
		VALUES ($1, NULLIF($2,''), COALESCE($3::jsonb,'{}'::jsonb), NOW())
		ON CONFLICT (node_id) DO UPDATE SET
			base_url = NULLIF(EXCLUDED.base_url,''),
			outputs = COALESCE(EXCLUDED.outputs,'{}'::jsonb),
			last_updated = NOW()
	`, nodeID, baseURL, outputsJSON)
	return err
}

func (r *nodeRepositoryDB) UpsertNodeLifecycle(ctx context.Context, update *pb.NodeLifecycleUpdate) error {
	if db == nil {
		return sql.ErrConnDone
	}
	if update == nil {
		return nil
	}
	b, err := json.Marshal(update)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO foghorn.node_lifecycle (node_id, lifecycle, last_updated)
		VALUES ($1, $2::jsonb, NOW())
		ON CONFLICT (node_id) DO UPDATE SET
			lifecycle = EXCLUDED.lifecycle,
			last_updated = NOW()
	`, update.GetNodeId(), string(b))
	return err
}

func (r *nodeRepositoryDB) UpsertNodeMaintenance(ctx context.Context, nodeID string, mode state.NodeOperationalMode, setBy string) error {
	if db == nil {
		return sql.ErrConnDone
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO foghorn.node_maintenance (node_id, mode, set_at, set_by)
		VALUES ($1, $2, NOW(), NULLIF($3, ''))
		ON CONFLICT (node_id) DO UPDATE SET
			mode = EXCLUDED.mode,
			set_at = NOW(),
			set_by = EXCLUDED.set_by
	`, nodeID, string(mode), setBy)
	return err
}

// ============================================================================
// ARTIFACT NODE REPOSITORY (Warm Storage Distribution)
// ============================================================================
// Tracks which nodes have local copies of artifacts (foghorn.artifact_nodes)
// ============================================================================

type artifactRepositoryDB struct{}

func NewArtifactRepository() state.ArtifactRepository { return &artifactRepositoryDB{} }

func (r *artifactRepositoryDB) UpsertArtifacts(ctx context.Context, nodeID string, artifacts []state.ArtifactRecord) error {
	if db == nil {
		return sql.ErrConnDone
	}
	if len(artifacts) == 0 {
		return nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort

	for _, a := range artifacts {
		// First, ensure the artifact exists in foghorn.artifacts (lifecycle table)
		_, err := tx.ExecContext(ctx, `
			INSERT INTO foghorn.artifacts
				(artifact_hash, artifact_type, internal_name, status, created_at, updated_at, access_count, last_accessed_at)
			VALUES ($1, $2, $3, 'ready', to_timestamp($4), NOW(), $5, CASE WHEN $6 > 0 THEN to_timestamp($6) ELSE NULL END)
			ON CONFLICT (artifact_hash) DO UPDATE SET
				internal_name = COALESCE(foghorn.artifacts.internal_name, EXCLUDED.internal_name),
				access_count = GREATEST(COALESCE(foghorn.artifacts.access_count, 0), EXCLUDED.access_count),
				last_accessed_at = CASE
					WHEN EXCLUDED.last_accessed_at IS NULL THEN foghorn.artifacts.last_accessed_at
					WHEN foghorn.artifacts.last_accessed_at IS NULL THEN EXCLUDED.last_accessed_at
					ELSE GREATEST(foghorn.artifacts.last_accessed_at, EXCLUDED.last_accessed_at)
				END,
				updated_at = NOW()
		`, a.ArtifactHash, a.ArtifactType, a.StreamName, a.CreatedAt, a.AccessCount, a.LastAccessed)
		if err != nil {
			return err
		}

		// Then, upsert into artifact_nodes (warm storage tracking)
		_, err = tx.ExecContext(ctx, `
			INSERT INTO foghorn.artifact_nodes
				(artifact_hash, node_id, file_path, size_bytes, segment_count, segment_bytes, access_count, last_accessed, last_seen_at, is_orphaned, cached_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, CASE WHEN $8 > 0 THEN to_timestamp($8) ELSE NULL END, NOW(), false, COALESCE((SELECT cached_at FROM foghorn.artifact_nodes WHERE artifact_hash = $1::varchar AND node_id = $2::varchar), NOW()))
			ON CONFLICT (artifact_hash, node_id) DO UPDATE SET
				file_path = EXCLUDED.file_path,
				size_bytes = EXCLUDED.size_bytes,
				segment_count = EXCLUDED.segment_count,
				segment_bytes = EXCLUDED.segment_bytes,
				access_count = GREATEST(COALESCE(foghorn.artifact_nodes.access_count, 0), EXCLUDED.access_count),
				last_accessed = CASE
					WHEN EXCLUDED.last_accessed IS NULL THEN foghorn.artifact_nodes.last_accessed
					WHEN foghorn.artifact_nodes.last_accessed IS NULL THEN EXCLUDED.last_accessed
					ELSE GREATEST(foghorn.artifact_nodes.last_accessed, EXCLUDED.last_accessed)
				END,
				last_seen_at = NOW(),
				is_orphaned = false
		`, a.ArtifactHash, nodeID, a.FilePath, a.SizeBytes, a.SegmentCount, a.SegmentBytes, a.AccessCount, a.LastAccessed)
		if err != nil {
			return err
		}
	}

	// Mark artifacts not in this report as potentially orphaned (not seen for >10 minutes)
	_, err = tx.ExecContext(ctx, `
		UPDATE foghorn.artifact_nodes
		SET is_orphaned = true
		WHERE node_id = $1
		  AND last_seen_at < NOW() - INTERVAL '10 minutes'
		  AND is_orphaned = false
	`, nodeID)
	if err != nil {
		return err
	}

	return tx.Commit()
}

func (r *artifactRepositoryDB) DeleteArtifact(ctx context.Context, nodeID string, artifactHash string) error {
	if db == nil {
		return sql.ErrConnDone
	}
	_, err := db.ExecContext(ctx, `
		DELETE FROM foghorn.artifact_nodes
		WHERE node_id = $1 AND artifact_hash = $2
	`, nodeID, artifactHash)
	return err
}

// GetArtifactSyncInfo retrieves sync tracking info for an artifact
func (r *artifactRepositoryDB) GetArtifactSyncInfo(ctx context.Context, artifactHash string) (*state.ArtifactSyncInfo, error) {
	if db == nil {
		return nil, sql.ErrConnDone
	}
	var info state.ArtifactSyncInfo
	var lastSyncAttempt sql.NullTime
	var syncError sql.NullString
	var s3URL sql.NullString

	// Query from artifacts table for sync info
	err := db.QueryRowContext(ctx, `
		SELECT artifact_hash, artifact_type, COALESCE(sync_status,'pending'),
		       s3_url, last_sync_attempt, sync_error
		FROM foghorn.artifacts
		WHERE artifact_hash = $1
	`, artifactHash).Scan(&info.ArtifactHash, &info.ArtifactType, &info.SyncStatus,
		&s3URL, &lastSyncAttempt, &syncError)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if s3URL.Valid {
		info.S3URL = s3URL.String
	}
	if lastSyncAttempt.Valid {
		info.LastSyncAttempt = lastSyncAttempt.Time.Unix()
	}
	if syncError.Valid {
		info.SyncError = syncError.String
	}

	// Get cached nodes from artifact_nodes
	rows, err := db.QueryContext(ctx, `
		SELECT node_id, cached_at FROM foghorn.artifact_nodes
		WHERE artifact_hash = $1 AND is_orphaned = false
	`, artifactHash)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var nodeID string
		var cachedAt sql.NullTime
		if err := rows.Scan(&nodeID, &cachedAt); err != nil {
			return nil, err
		}
		info.CachedNodes = append(info.CachedNodes, nodeID)
		if cachedAt.Valid && info.CachedAt == 0 {
			info.CachedAt = cachedAt.Time.UnixMilli()
		}
	}

	return &info, nil
}

// SetSyncStatus updates sync status and S3 URL for an artifact
func (r *artifactRepositoryDB) SetSyncStatus(ctx context.Context, artifactHash, status, s3URL string) error {
	if db == nil {
		return sql.ErrConnDone
	}
	_, err := db.ExecContext(ctx, `
		UPDATE foghorn.artifacts
		SET sync_status = $2,
		    s3_url = NULLIF($3, ''),
		    last_sync_attempt = NOW(),
		    sync_error = NULL,
		    frozen_at = CASE WHEN $2 = 'synced' THEN NOW() ELSE frozen_at END
		WHERE artifact_hash = $1
	`, artifactHash, status, s3URL)
	return err
}

// AddCachedNode records that a node has a local copy of an artifact
func (r *artifactRepositoryDB) AddCachedNode(ctx context.Context, artifactHash, nodeID string) error {
	if db == nil {
		return sql.ErrConnDone
	}
	// Upsert into artifact_nodes
	_, err := db.ExecContext(ctx, `
		INSERT INTO foghorn.artifact_nodes (artifact_hash, node_id, last_seen_at, is_orphaned, cached_at)
		VALUES ($1, $2, NOW(), false, NOW())
		ON CONFLICT (artifact_hash, node_id) DO UPDATE SET
			last_seen_at = NOW(),
			is_orphaned = false,
			cached_at = COALESCE(foghorn.artifact_nodes.cached_at, NOW())
	`, artifactHash, nodeID)
	return err
}

// SetCachedAt explicitly sets the cached_at timestamp
func (r *artifactRepositoryDB) SetCachedAt(ctx context.Context, artifactHash string) error {
	if db == nil {
		return sql.ErrConnDone
	}
	_, err := db.ExecContext(ctx, `
		UPDATE foghorn.artifact_nodes
		SET cached_at = NOW()
		WHERE artifact_hash = $1
	`, artifactHash)
	return err
}

// GetCachedAt retrieves the cached_at timestamp for calculating warm duration
func (r *artifactRepositoryDB) GetCachedAt(ctx context.Context, artifactHash string) (int64, error) {
	if db == nil {
		return 0, sql.ErrConnDone
	}
	var cachedAt sql.NullTime
	err := db.QueryRowContext(ctx, `
		SELECT MIN(cached_at) FROM foghorn.artifact_nodes
		WHERE artifact_hash = $1 AND is_orphaned = false
	`, artifactHash).Scan(&cachedAt)
	if err != nil {
		return 0, err
	}
	if !cachedAt.Valid {
		return 0, nil
	}
	return cachedAt.Time.UnixMilli(), nil
}

// RemoveCachedNode removes a node from having a copy of an artifact
func (r *artifactRepositoryDB) RemoveCachedNode(ctx context.Context, artifactHash, nodeID string) error {
	if db == nil {
		return sql.ErrConnDone
	}
	_, err := db.ExecContext(ctx, `
		DELETE FROM foghorn.artifact_nodes
		WHERE artifact_hash = $1 AND node_id = $2
	`, artifactHash, nodeID)
	return err
}

// IsSynced returns true if the artifact is synced to S3
func (r *artifactRepositoryDB) IsSynced(ctx context.Context, artifactHash string) (bool, error) {
	if db == nil {
		return false, sql.ErrConnDone
	}
	var synced bool
	err := db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM foghorn.artifacts
			WHERE artifact_hash = $1 AND sync_status = 'synced'
		)
	`, artifactHash).Scan(&synced)
	if err != nil {
		return false, err
	}
	return synced, nil
}

// GetArtifactNodes returns all node IDs that have a local copy of the artifact
func (r *artifactRepositoryDB) GetArtifactNodes(ctx context.Context, artifactHash string) ([]string, error) {
	if db == nil {
		return nil, sql.ErrConnDone
	}
	rows, err := db.QueryContext(ctx, `
		SELECT node_id FROM foghorn.artifact_nodes
		WHERE artifact_hash = $1 AND is_orphaned = false
	`, artifactHash)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var nodes []string
	for rows.Next() {
		var nodeID string
		if err := rows.Scan(&nodeID); err != nil {
			return nil, err
		}
		nodes = append(nodes, nodeID)
	}
	return nodes, rows.Err()
}
