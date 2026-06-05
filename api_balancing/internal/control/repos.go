package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"frameworks/api_balancing/internal/state"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/lib/pq"
)

// ============================================================================
// UNIFIED ARTIFACT REPOSITORIES
// ============================================================================
// These repositories work with the new unified artifact model:
//   - foghorn.artifacts      = lifecycle state (1 row per artifact)
//   - foghorn.artifact_nodes = warm storage distribution (N rows per artifact)
//
// Business metadata (tenant_id, user_id, stream_id) is in Commodore.
// See: docs/architecture/clips-dvr.md
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
		SELECT a.artifact_hash, '' as tenant_id, COALESCE(a.stream_internal_name,''),
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
		SELECT COALESCE(stream_internal_name,'') FROM foghorn.artifacts
		WHERE request_id = $1 AND artifact_type = 'clip'
	`, requestID).Scan(&internalName)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return internalName, err
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
			  AND sync_status = 'synced'
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
		SELECT a.artifact_hash, '' as tenant_id, COALESCE(a.stream_internal_name,''),
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
		SELECT COALESCE(stream_internal_name,'') FROM foghorn.artifacts
		WHERE artifact_hash = $1 AND artifact_type = 'dvr'
	`, dvrHash).Scan(&internalName)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return internalName, err
}

func (r *dvrRepositoryDB) UpdateDVRProgressByHash(ctx context.Context, dvrHash string, status string, sizeBytes int64) error {
	if db == nil {
		return sql.ErrConnDone
	}
	// Progress writes are only meaningful before finalization. The first
	// storage-node progress event is what promotes requested/starting DVRs into
	// recording so the chapter sweeper can materialize the active EVENT view.
	_, err := db.ExecContext(ctx, `
		UPDATE foghorn.artifacts
		SET status = $2,
		    size_bytes = GREATEST(COALESCE(size_bytes, 0), $3),
		    updated_at = NOW()
		WHERE artifact_hash = $1
		  AND artifact_type = 'dvr'
		  AND status IN ('requested', 'starting', 'recording')
	`, dvrHash, status, sizeBytes)
	return err
}

func (r *dvrRepositoryDB) UpdateDVRCompletionByHash(ctx context.Context, dvrHash string, finalStatus string, durationSeconds int64, sizeBytes int64, manifestPath string, errorMsg string) error {
	if db == nil {
		return sql.ErrConnDone
	}
	// Completion may legitimately race FinalizeDVR. Only overwrite when the
	// row is still pre-terminal; FinalizeDVR's transition to a terminal
	// status wins if it lands first.
	_, err := db.ExecContext(ctx, `
		UPDATE foghorn.artifacts
		SET status = $1,
		    ended_at = NOW(),
		    duration_seconds = $2,
		    size_bytes = $3,
		    manifest_path = $4,
		    error_message = NULLIF($5, ''),
		    updated_at = NOW()
		WHERE artifact_hash = $6
		  AND artifact_type = 'dvr'
		  AND status IN ('requested', 'starting', 'recording', 'finalizing')
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
			  AND sync_status = 'synced'
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
	defer func() { _ = rows.Close() }()

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

func (r *nodeRepositoryDB) UpsertNodeLifecycles(ctx context.Context, updates []*ipcpb.NodeLifecycleUpdate) error {
	if db == nil {
		return sql.ErrConnDone
	}
	if len(updates) == 0 {
		return nil
	}

	deduped := dedupeNodeLifecycleUpdates(updates)
	nodeIDs := make([]string, 0, len(deduped))
	lifecycles := make([]string, 0, len(deduped))
	for _, update := range deduped {
		b, err := json.Marshal(update)
		if err != nil {
			return err
		}
		nodeIDs = append(nodeIDs, update.GetNodeId())
		lifecycles = append(lifecycles, string(b))
	}
	if len(nodeIDs) == 0 {
		return nil
	}

	_, err := db.ExecContext(ctx, `
		INSERT INTO foghorn.node_lifecycle (node_id, lifecycle, last_updated)
		SELECT node_id, lifecycle::jsonb, NOW()
		FROM unnest($1::text[], $2::text[]) AS t(node_id, lifecycle)
		ON CONFLICT (node_id) DO UPDATE SET
			lifecycle = EXCLUDED.lifecycle,
			last_updated = NOW()
	`, nodeIDs, lifecycles)
	return err
}

func (r *nodeRepositoryDB) UpsertNodeComponents(ctx context.Context, updates []*ipcpb.NodeLifecycleUpdate) error {
	if db == nil {
		return sql.ErrConnDone
	}
	entries := dedupeNodeComponentUpdates(updates)
	nodeIDs := make([]string, 0, len(entries))
	components := make([]string, 0, len(entries))
	versions := make([]string, 0, len(entries))
	for _, entry := range entries {
		nodeIDs = append(nodeIDs, entry.nodeID)
		components = append(components, entry.component)
		versions = append(versions, entry.version)
	}
	if len(nodeIDs) == 0 {
		return nil
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO foghorn.node_components (node_id, component, current_version, last_reported_at)
		SELECT node_id, component, NULLIF(version, ''), NOW()
		FROM unnest($1::text[], $2::text[], $3::text[]) AS t(node_id, component, version)
		ON CONFLICT (node_id, component) DO UPDATE SET
			current_version = EXCLUDED.current_version,
			last_reported_at = NOW()
	`, nodeIDs, components, versions)
	return err
}

func dedupeNodeLifecycleUpdates(updates []*ipcpb.NodeLifecycleUpdate) []*ipcpb.NodeLifecycleUpdate {
	order := make([]string, 0, len(updates))
	byNode := make(map[string]*ipcpb.NodeLifecycleUpdate, len(updates))
	for _, update := range updates {
		if update == nil || update.GetNodeId() == "" {
			continue
		}
		nodeID := update.GetNodeId()
		if _, seen := byNode[nodeID]; !seen {
			order = append(order, nodeID)
		}
		byNode[nodeID] = update
	}
	out := make([]*ipcpb.NodeLifecycleUpdate, 0, len(order))
	for _, nodeID := range order {
		out = append(out, byNode[nodeID])
	}
	return out
}

type nodeComponentUpdate struct {
	nodeID    string
	component string
	version   string
}

func dedupeNodeComponentUpdates(updates []*ipcpb.NodeLifecycleUpdate) []nodeComponentUpdate {
	order := make([]string, 0)
	byKey := make(map[string]nodeComponentUpdate)
	for _, update := range updates {
		if update == nil || update.GetNodeId() == "" {
			continue
		}
		nodeID := update.GetNodeId()
		for _, component := range update.GetComponentVersions() {
			if component == nil || component.GetComponent() == "" {
				continue
			}
			key := nodeID + "\x00" + component.GetComponent()
			if _, seen := byKey[key]; !seen {
				order = append(order, key)
			}
			byKey[key] = nodeComponentUpdate{
				nodeID:    nodeID,
				component: component.GetComponent(),
				version:   component.GetVersion(),
			}
		}
	}
	out := make([]nodeComponentUpdate, 0, len(order))
	for _, key := range order {
		out = append(out, byKey[key])
	}
	return out
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

	// Concurrent reports can overlap; stable row order keeps transactions from
	// locking the same artifact set in opposite sequences.
	records := append([]state.ArtifactRecord(nil), artifacts...)
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].ArtifactHash != records[j].ArtifactHash {
			return records[i].ArtifactHash < records[j].ArtifactHash
		}
		return records[i].FilePath < records[j].FilePath
	})

	var err error
	for attempt := 0; attempt < 3; attempt++ {
		err = r.upsertArtifactsOnce(ctx, nodeID, records)
		if err == nil || !isRetryableArtifactUpsertError(err) || ctx.Err() != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 25 * time.Millisecond):
		}
	}
	return err
}

func (r *artifactRepositoryDB) upsertArtifactsOnce(ctx context.Context, nodeID string, artifacts []state.ArtifactRecord) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort

	for _, a := range artifacts {
		_, errExec := tx.ExecContext(ctx, `
			UPDATE foghorn.artifacts SET
				stream_internal_name = COALESCE(stream_internal_name, $2),
				access_count = GREATEST(COALESCE(access_count, 0), $3),
				last_accessed_at = CASE
					WHEN $4 = 0 THEN last_accessed_at
					WHEN last_accessed_at IS NULL THEN to_timestamp($4)
					ELSE GREATEST(last_accessed_at, to_timestamp($4))
				END,
				updated_at = NOW()
			WHERE artifact_hash = $1
		`, a.ArtifactHash, a.StreamName, a.AccessCount, a.LastAccessed)
		if errExec != nil {
			return errExec
		}

		// Upsert warm storage tracking — only if the lifecycle row exists (FK guard).
		//
		// Origin-wins: poller reports default to role='cache'. The CASE
		// guards on UPDATE ensure that once a finalizer RPC has stamped a
		// row as role='origin', no subsequent poller report can downgrade
		// it. is_complete follows the same rule — only the origin
		// authority flips it true; polling cannot.
		role := a.Role
		if role == "" {
			role = "cache"
		}
		_, errExec = tx.ExecContext(ctx, `
			INSERT INTO foghorn.artifact_nodes
				(artifact_hash, node_id, file_path, size_bytes, segment_count, segment_bytes, access_count, last_accessed, last_seen_at, is_orphaned, cached_at, role, is_complete)
			SELECT $1, $2, $3, $4, $5, $6, $7, CASE WHEN $8 > 0 THEN to_timestamp($8) ELSE NULL END, NOW(), false, COALESCE((SELECT cached_at FROM foghorn.artifact_nodes WHERE artifact_hash = $1::varchar AND node_id = $2::varchar), NOW()), $9, $10
			WHERE EXISTS (SELECT 1 FROM foghorn.artifacts WHERE artifact_hash = $1)
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
				is_orphaned = false,
				role = CASE WHEN foghorn.artifact_nodes.role = 'origin' THEN 'origin' ELSE EXCLUDED.role END,
				is_complete = CASE WHEN foghorn.artifact_nodes.role = 'origin' THEN foghorn.artifact_nodes.is_complete ELSE EXCLUDED.is_complete END
		`, a.ArtifactHash, nodeID, a.FilePath, a.SizeBytes, a.SegmentCount, a.SegmentBytes, a.AccessCount, a.LastAccessed, role, a.IsComplete)
		if errExec != nil {
			return errExec
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

func isRetryableArtifactUpsertError(err error) bool {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		switch string(pqErr.Code) {
		case "40P01", "40001":
			return true
		}
	}
	return false
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

	if errors.Is(err, sql.ErrNoRows) {
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
	if status == "synced" {
		_, err := db.ExecContext(ctx, `
			UPDATE foghorn.artifacts
			SET sync_status = 'synced',
			    s3_url = NULLIF($2, ''),
			    last_sync_attempt = NOW(),
			    sync_error = NULL
			WHERE artifact_hash = $1
		`, artifactHash, s3URL)
		return err
	}
	_, err := db.ExecContext(ctx, `
		UPDATE foghorn.artifacts
		SET sync_status = $2,
		    s3_url = NULLIF($3, ''),
		    last_sync_attempt = NOW(),
		    sync_error = NULL
		WHERE artifact_hash = $1
	`, artifactHash, status, s3URL)
	return err
}

// AddCachedNode records that a node has a local copy of an artifact.
// Cache-side write — does NOT downgrade an existing origin row.
func (r *artifactRepositoryDB) AddCachedNode(ctx context.Context, artifactHash, nodeID string) error {
	if db == nil {
		return sql.ErrConnDone
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO foghorn.artifact_nodes (artifact_hash, node_id, last_seen_at, is_orphaned, cached_at, role, is_complete)
		VALUES ($1, $2, NOW(), false, NOW(), 'cache', false)
		ON CONFLICT (artifact_hash, node_id) DO UPDATE SET
			last_seen_at = NOW(),
			is_orphaned = false,
			cached_at = COALESCE(foghorn.artifact_nodes.cached_at, NOW())
	`, artifactHash, nodeID)
	return err
}

// AddCachedNodeWithPath records that a node has a local copy of an artifact with path details.
// Cache-side write — does NOT downgrade an existing origin row.
func (r *artifactRepositoryDB) AddCachedNodeWithPath(ctx context.Context, artifactHash, nodeID, filePath string, sizeBytes int64) error {
	if db == nil {
		return sql.ErrConnDone
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO foghorn.artifact_nodes (artifact_hash, node_id, file_path, size_bytes, last_seen_at, is_orphaned, cached_at, role, is_complete)
		VALUES ($1, $2, $3, NULLIF($4, 0), NOW(), false, NOW(), 'cache', false)
		ON CONFLICT (artifact_hash, node_id) DO UPDATE SET
			file_path = EXCLUDED.file_path,
			size_bytes = COALESCE(EXCLUDED.size_bytes, foghorn.artifact_nodes.size_bytes),
			last_seen_at = NOW(),
			is_orphaned = false,
			cached_at = COALESCE(foghorn.artifact_nodes.cached_at, NOW())
	`, artifactHash, nodeID, filePath, sizeBytes)
	return err
}

// RegisterOriginArtifact marks a node as the origin (canonical full
// file holder) for an artifact. Called from finalizers that wrote the
// file to disk: clip create, processing finalize, and DVR chapter
// finalize (each with its own VOD artifact hash). complete=true flips
// is_complete authoritative; pass complete=false at recording start to
// register the row before finalization.
//
// Idempotent for the same writer. Origin upserts always set role to
// 'origin'; once set, only another origin write can flip is_complete
// (cache writes via AddCachedNode* preserve the existing
// role/is_complete via their own guards).
func (r *artifactRepositoryDB) RegisterOriginArtifact(ctx context.Context, artifactHash, nodeID, filePath string, sizeBytes int64, complete bool) error {
	if db == nil {
		return sql.ErrConnDone
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO foghorn.artifact_nodes (artifact_hash, node_id, file_path, size_bytes, last_seen_at, is_orphaned, cached_at, role, is_complete)
		VALUES ($1, $2, NULLIF($3, ''), NULLIF($4, 0), NOW(), false, NOW(), 'origin', $5)
		ON CONFLICT (artifact_hash, node_id) DO UPDATE SET
			file_path = COALESCE(NULLIF(EXCLUDED.file_path, ''), foghorn.artifact_nodes.file_path),
			size_bytes = COALESCE(EXCLUDED.size_bytes, foghorn.artifact_nodes.size_bytes),
			last_seen_at = NOW(),
			is_orphaned = false,
			role = 'origin',
			is_complete = CASE WHEN EXCLUDED.is_complete THEN true ELSE foghorn.artifact_nodes.is_complete END
	`, artifactHash, nodeID, filePath, sizeBytes, complete)
	return err
}

// ListOriginNodes returns node IDs that hold the canonical full file
// for an artifact and have is_complete=true AND are not orphaned.
// Empty result means no peer-relay fallback source is available.
func (r *artifactRepositoryDB) ListOriginNodes(ctx context.Context, artifactHash string) ([]string, error) {
	if db == nil {
		return nil, sql.ErrConnDone
	}
	rows, err := db.QueryContext(ctx, `
		SELECT node_id FROM foghorn.artifact_nodes
		WHERE artifact_hash = $1
		  AND role = 'origin'
		  AND is_complete = true
		  AND is_orphaned = false
		ORDER BY last_seen_at DESC
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

// ListAllNodeArtifacts returns all non-orphaned artifacts grouped by node ID (for rehydration)
func (r *artifactRepositoryDB) ListAllNodeArtifacts(ctx context.Context) (map[string][]state.ArtifactRecord, error) {
	if db == nil {
		return nil, sql.ErrConnDone
	}

	rows, err := db.QueryContext(ctx, `
		SELECT
			an.node_id,
			an.artifact_hash,
			COALESCE(a.artifact_type, 'clip'),
			COALESCE(a.stream_internal_name, ''),
			COALESCE(an.file_path, ''),
			COALESCE(an.size_bytes, 0),
			COALESCE(EXTRACT(EPOCH FROM a.created_at)::bigint, 0),
			COALESCE(an.access_count, 0),
			COALESCE(EXTRACT(EPOCH FROM an.last_accessed), 0)::bigint
		FROM foghorn.artifact_nodes an
		JOIN foghorn.artifacts a ON a.artifact_hash = an.artifact_hash
		WHERE an.is_orphaned = false
		  AND a.status != 'deleted'
		ORDER BY an.node_id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string][]state.ArtifactRecord)
	for rows.Next() {
		var nodeID string
		var rec state.ArtifactRecord
		if err := rows.Scan(
			&nodeID,
			&rec.ArtifactHash,
			&rec.ArtifactType,
			&rec.StreamName,
			&rec.FilePath,
			&rec.SizeBytes,
			&rec.CreatedAt,
			&rec.AccessCount,
			&rec.LastAccessed,
		); err != nil {
			return nil, err
		}
		result[nodeID] = append(result[nodeID], rec)
	}
	return result, rows.Err()
}

func (r *artifactRepositoryDB) MarkNodeArtifactsOrphaned(ctx context.Context, nodeID string) error {
	if db == nil {
		return sql.ErrConnDone
	}
	_, err := db.ExecContext(ctx, `
		UPDATE foghorn.artifact_nodes
		SET is_orphaned = true, last_seen_at = NOW()
		WHERE node_id = $1 AND is_orphaned = false
	`, nodeID)
	return err
}

func (r *artifactRepositoryDB) NeedsVODDtshSync(ctx context.Context, artifactHash string) bool {
	if db == nil {
		return false
	}
	var needsSync bool
	err := db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM foghorn.artifacts
			WHERE artifact_hash = $1
			  AND artifact_type = 'vod'
			  AND sync_status = 'synced'
			  AND COALESCE(dtsh_synced, false) = false
		)
	`, artifactHash).Scan(&needsSync)
	if err != nil {
		return false
	}
	return needsSync
}
