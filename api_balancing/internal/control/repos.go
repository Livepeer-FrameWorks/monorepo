package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"frameworks/api_balancing/internal/artifactoutbox"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
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

// UpdateDVRProgressByHash applies a storage-node progress report. It NEVER persists a node-supplied
// status: the ONLY status write is the canonical first-edge promotion requested/starting -> recording,
// which also enqueues the durable STATUS_RECORDING lifecycle event exactly once (first edge only), so
// the recording is recoverable in analytics even when StartDVR's STARTED event was lost. Every other
// report is a pure metrics update. Two guards fail closed:
//   - a report for a terminal/finalizing row is a no-op (a late/racing progress tick can never overwrite
//     a terminal or 'finalizing' status, which would strand a finalizing DVR);
//   - a report from any node other than the one this DVR was dispatched to (dvr_start_dispatch.node_id)
//     is rejected without mutating — only the recording origin may advance this row.
//
// It returns (applied, currentStatus, err). applied is true ONLY for an accepted active transition —
// the first-edge promotion into 'recording' or a continued-recording metrics update; it is false for a
// terminal/finalizing no-op and for a missing row. currentStatus is the row's status AFTER this call
// ('recording' when applied; the terminal/finalizing status on a no-op; "" for a missing row). Callers
// mirror stream-instance state / node presence only when applied, so a late report can never resurrect
// a terminal recording in a downstream sink.
//
// The nodeID argument is the authenticated reporting node bound to the connection; `status` is advisory
// and intentionally never written.
func (r *dvrRepositoryDB) UpdateDVRProgressByHash(ctx context.Context, dvrHash string, status string, sizeBytes int64, segmentCount uint32, nodeID string) (bool, string, error) {
	if db == nil {
		return false, "", sql.ErrConnDone
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, "", err
	}
	defer tx.Rollback() //nolint:errcheck // best-effort on the non-commit paths

	var prevStatus, tenantID, streamID, internalName, dispatchNode string
	// Lock the row and read its prior status, identity, and the durable dispatch owner node
	// (dvr_start_dispatch.node_id, persisted at StartDVR and retained through finalize) transactionally
	// with the mutation below.
	err = tx.QueryRowContext(ctx, `
		SELECT status,
		       tenant_id::text,
		       COALESCE(stream_id::text, ''),
		       COALESCE(stream_internal_name, ''),
		       COALESCE(dvr_start_dispatch->>'node_id', '')
		FROM foghorn.artifacts
		WHERE artifact_hash = $1 AND artifact_type = 'dvr'
		FOR UPDATE
	`, dvrHash).Scan(&prevStatus, &tenantID, &streamID, &internalName, &dispatchNode)
	if errors.Is(err, sql.ErrNoRows) {
		// Row missing: nothing to promote, nothing to emit, nothing applied.
		return false, "", tx.Commit()
	}
	if err != nil {
		return false, "", err
	}

	// A report for a terminal/finalizing row is a no-op: progress must never overwrite a terminal or
	// 'finalizing' status. applied=false so callers leave downstream sinks untouched; the current
	// (unchanged) status is returned so callers can distinguish it from an accepted transition.
	if prevStatus != "requested" && prevStatus != "starting" && prevStatus != "recording" {
		return false, prevStatus, tx.Commit()
	}

	// Bind the report to the dispatched recording node. A progress report from any other node (or an
	// active row with no dispatch owner) is rejected without mutating.
	if dispatchNode == "" || dispatchNode != nodeID {
		return false, "", fmt.Errorf("dvr progress for %s rejected: reporting node %q is not the dispatched recording node %q", dvrHash, nodeID, dispatchNode)
	}

	firstEdge := prevStatus == "requested" || prevStatus == "starting"
	// Metrics update + canonical first-edge promotion. size_bytes grows monotonically (GREATEST); status
	// only ever advances requested/starting -> recording, never to a node-supplied value.
	if _, err = tx.ExecContext(ctx, `
		UPDATE foghorn.artifacts
		SET status = CASE WHEN status IN ('requested', 'starting') THEN 'recording' ELSE status END,
		    size_bytes = GREATEST(COALESCE(size_bytes, 0), $2),
		    updated_at = NOW()
		WHERE artifact_hash = $1 AND artifact_type = 'dvr'
	`, dvrHash, sizeBytes); err != nil {
		return false, "", err
	}

	if firstEdge {
		data := &ipcpb.DVRLifecycleData{
			Status:  ipcpb.DVRLifecycleData_STATUS_RECORDING,
			DvrHash: dvrHash,
		}
		// tenant_id is NOT NULL on the row; the outbox rejects a tenant-less lifecycle event, so an
		// unexpectedly empty tenant rolls the whole transaction back (fail closed).
		if tenantID != "" {
			data.TenantId = &tenantID
		}
		if streamID != "" {
			data.StreamId = &streamID
		}
		if internalName != "" {
			data.StreamInternalName = &internalName
		}
		if nodeID != "" {
			data.NodeId = &nodeID
		}
		sc := int32(segmentCount)
		data.SegmentCount = &sc
		if sizeBytes > 0 {
			sz := uint64(sizeBytes)
			data.SizeBytes = &sz
		}
		if enqErr := artifactoutbox.EnqueueDVRLifecycleTx(ctx, tx, data); enqErr != nil {
			return false, "", enqErr
		}
	}
	// Accepted active transition: the row is now 'recording'.
	return true, "recording", tx.Commit()
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
	for attempt := range 3 {
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

// applyReportRevisionGuard atomically advances the node's report ordering watermark and reports
// whether the (connectionFence, seq) report is STALE, so a delayed older whole-node report can't
// overwrite newer durable placement. The connection fence is issued monotonically by Foghorn when a
// control connection registers, so a reconnect ranks strictly higher and a delayed report from a
// superseded connection loses. The advance is a single atomic compare-and-set (DO UPDATE only when
// the incoming (fence, seq) beats the stored pair; the unique constraint serializes concurrent
// first inserts, so an absent row can't race). fence==0 or seq==0 is unversioned (internal/
// eviction): it always applies and leaves the watermark untouched.
func applyReportRevisionGuard(ctx context.Context, tx *sql.Tx, nodeID string, connectionFence, seq int64) (stale bool, err error) {
	if connectionFence == 0 || seq == 0 {
		return false, nil
	}
	var applied sql.NullInt64
	qerr := tx.QueryRowContext(ctx,
		`INSERT INTO foghorn.node_artifact_report_watermark AS w (node_id, connection_fence, seq)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (node_id) DO UPDATE SET connection_fence = EXCLUDED.connection_fence, seq = EXCLUDED.seq
		   WHERE (w.connection_fence, w.seq) < (EXCLUDED.connection_fence, EXCLUDED.seq)
		 RETURNING connection_fence`,
		nodeID, connectionFence, seq).Scan(&applied)
	if errors.Is(qerr, sql.ErrNoRows) {
		return true, nil // a newer (fence, seq) is already stored — drop this stale report
	}
	if qerr != nil {
		return false, qerr
	}
	return false, nil
}

// AllocateNodeControlFence issues a fresh monotonic ownership fence for a node control connection.
// Called once when a connection registers (not on the report hot path). Monotonic across a Redis
// restart because it is a Postgres sequence.
func AllocateNodeControlFence(ctx context.Context) (int64, error) {
	if db == nil {
		return 0, sql.ErrConnDone
	}
	var fence int64
	if err := db.QueryRowContext(ctx, `SELECT nextval('foghorn.node_control_fence_seq')`).Scan(&fence); err != nil {
		return 0, err
	}
	return fence, nil
}

func (r *artifactRepositoryDB) upsertArtifactsOnce(ctx context.Context, nodeID string, artifacts []state.ArtifactRecord) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort

	var reportFence, reportSeq int64
	if len(artifacts) > 0 {
		reportFence = artifacts[0].ReportConnectionFence
		reportSeq = artifacts[0].ReportSeq
	}
	if stale, gerr := applyReportRevisionGuard(ctx, tx, nodeID, reportFence, reportSeq); gerr != nil {
		return gerr
	} else if stale {
		return nil // a newer report already applied; dropping this stale one (deferred rollback)
	}

	// The poller report is the source of truth for which nodes hold a *materialized*
	// artifact file. Each report that makes a node newly-present (first seen or restored
	// from orphaned) emits a durable GAINED in the same transaction, covering reconnects
	// and sync-complete cache copies. It does NOT observe read-through relay block caches
	// (<asset>.blocks/ directories) — the poller skips directories, so partial edge
	// caches are absent from placement (see docs/architecture/analytics-pipeline.md).
	//
	// All records in one report share the capture time (ArtifactRecord.ReportedAtMs) — that is
	// event time only. The monotonic ordering key for a placement transition is the Postgres
	// sequence (artifact_node_copy_version_seq) assigned when the GAINED/LOST/UPDATED row is
	// emitted, NOT this timestamp.
	var reportedAtMs int64
	if len(artifacts) > 0 {
		reportedAtMs = artifacts[0].ReportedAtMs
	}
	for _, a := range artifacts {
		if _, errExec := tx.ExecContext(ctx, artifactsMetaUpdateSQL,
			a.ArtifactHash, a.StreamName, a.AccessCount, a.LastAccessed); errExec != nil {
			return errExec
		}
		role := a.Role
		if role == "" {
			role = "cache"
		}

		var priorRole string
		var priorOrphaned, priorComplete bool
		priorExisted := true
		if perr := tx.QueryRowContext(ctx,
			`SELECT role, is_orphaned, is_complete FROM foghorn.artifact_nodes WHERE artifact_hash = $1 AND node_id = $2 FOR UPDATE`,
			a.ArtifactHash, nodeID).Scan(&priorRole, &priorOrphaned, &priorComplete); perr != nil {
			if errors.Is(perr, sql.ErrNoRows) {
				priorExisted = false
			} else {
				return perr
			}
		}
		var inserted, rowComplete bool
		var rowRole string
		uerr := tx.QueryRowContext(ctx, pollerNodeUpsertSQL+` RETURNING (xmax = 0), role, is_complete`,
			a.ArtifactHash, nodeID, a.FilePath, a.SizeBytes, a.SegmentCount, a.SegmentBytes,
			a.AccessCount, a.LastAccessed, role, a.IsComplete).Scan(&inserted, &rowRole, &rowComplete)
		if errors.Is(uerr, sql.ErrNoRows) {
			continue // FK guard: artifact unknown, nothing upserted
		}
		if uerr != nil {
			return uerr
		}
		if err = emitPresentTx(ctx, tx, a.ArtifactHash, nodeID, rowRole, rowComplete,
			a.SizeBytes, inserted, priorExisted, priorOrphaned, priorComplete, priorRole, reportedAtMs); err != nil {
			return err
		}
	}

	// A report asserts PRESENCE only: it never negatively diffs a present row that is absent from the
	// report. Absence converges through the routing cordon (ArtifactInventoryReady), fenced takeover on
	// reconnect/disconnect, and the stale sweep below — so a copy the node dropped is orphaned within the
	// stale window rather than immediately.
	//
	// Orphan rows unseen for >10 min, emitting a durable LOST for each in this transaction. Also covers
	// disconnects and any unversioned/eviction path.
	const staleSweepSQL = `
		UPDATE foghorn.artifact_nodes
		SET is_orphaned = true
		WHERE node_id = $1
		  AND last_seen_at < NOW() - INTERVAL '10 minutes'
		  AND is_orphaned = false`
	orphaned, sweepErr := scanLostRows(tx.QueryContext(ctx, staleSweepSQL+" RETURNING artifact_hash, role", nodeID))
	if sweepErr != nil {
		return sweepErr
	}
	if err = emitLost(ctx, tx, nodeID, orphaned, reportedAtMs); err != nil {
		return err
	}

	return tx.Commit()
}

// Invariant: the lifecycle writers below (origin register, cache-fill, orphan, delete) enqueue
// their node-copy transition in the SAME transaction as the artifact_nodes mutation, so state and
// its analytics event commit atomically. DVR segment/progress refreshes and reconciler
// orphan-onboarding are the exception — they emit out-of-band and rely on node-copy reconciliation
// to seed any last_emitted_version=0 row. See docs/architecture/analytics-pipeline.md.

const originUpsertSQL = `
	INSERT INTO foghorn.artifact_nodes (artifact_hash, node_id, file_path, size_bytes, last_seen_at, is_orphaned, cached_at, role, is_complete)
	VALUES ($1, $2, NULLIF($3, ''), NULLIF($4, 0), NOW(), false, NOW(), 'origin', $5)
	ON CONFLICT (artifact_hash, node_id) DO UPDATE SET
		file_path = COALESCE(NULLIF(EXCLUDED.file_path, ''), foghorn.artifact_nodes.file_path),
		size_bytes = COALESCE(EXCLUDED.size_bytes, foghorn.artifact_nodes.size_bytes),
		last_seen_at = NOW(),
		is_orphaned = false,
		role = 'origin',
		is_complete = CASE WHEN EXCLUDED.is_complete THEN true ELSE foghorn.artifact_nodes.is_complete END`

// cacheUpsertSQL records a synced cache copy. AddCachedNode is called after a
// successful sync, so a cache copy is complete — set is_complete=true on insert, and on
// conflict ONLY for cache rows. An existing origin row keeps its role AND its own
// completeness (a parent DVR origin is deliberately incomplete; a cache sync must not
// flip it complete and thus peer-relay-eligible).
const cacheUpsertSQL = `
	INSERT INTO foghorn.artifact_nodes (artifact_hash, node_id, file_path, size_bytes, last_seen_at, is_orphaned, cached_at, role, is_complete)
	VALUES ($1, $2, NULLIF($3, ''), NULLIF($4, 0), NOW(), false, NOW(), 'cache', true)
	ON CONFLICT (artifact_hash, node_id) DO UPDATE SET
		file_path = COALESCE(NULLIF(EXCLUDED.file_path, ''), foghorn.artifact_nodes.file_path),
		size_bytes = COALESCE(EXCLUDED.size_bytes, foghorn.artifact_nodes.size_bytes),
		last_seen_at = NOW(),
		is_orphaned = false,
		is_complete = CASE WHEN foghorn.artifact_nodes.role = 'origin' THEN foghorn.artifact_nodes.is_complete ELSE true END,
		cached_at = COALESCE(foghorn.artifact_nodes.cached_at, NOW())`

const artifactsMetaUpdateSQL = `
	UPDATE foghorn.artifacts SET
		stream_internal_name = COALESCE(stream_internal_name, $2),
		access_count = GREATEST(COALESCE(access_count, 0), $3),
		last_accessed_at = CASE
			WHEN $4 = 0 THEN last_accessed_at
			WHEN last_accessed_at IS NULL THEN to_timestamp($4)
			ELSE GREATEST(last_accessed_at, to_timestamp($4))
		END,
		updated_at = NOW()
	WHERE artifact_hash = $1`

// pollerNodeUpsertSQL records a node's reported artifact. Origin-wins: once a
// finalizer stamps role='origin'/is_complete, poller reports cannot downgrade it.
// Completeness is sticky for cache rows too: the poller reports is_complete=false
// (it doesn't compute completeness), so it must not clear a copy a sync already
// confirmed complete — only orphaning/eviction removes a complete copy.
// The WHERE EXISTS FK guard means a report for an unknown artifact upserts nothing
// (RETURNING then yields no row).
const pollerNodeUpsertSQL = `
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
		is_complete = CASE WHEN foghorn.artifact_nodes.role = 'origin' THEN foghorn.artifact_nodes.is_complete
			ELSE (foghorn.artifact_nodes.is_complete OR EXCLUDED.is_complete) END`

// txQuerier is the read subset the node-copy helpers need (satisfied by *sql.Tx).
type txQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// placementTenant resolves an artifact's owning tenant (artifact_nodes has none).
// A genuine query error propagates so the shared transaction rolls back rather than
// committing a state change without its telemetry. A missing artifact row (no FK
// parent) yields "": the live-mutation caller (enqueueNodeCopy) then FAILS the tx
// fail-closed — no placement change commits without its analytics event — while the
// reconcile re-affirm path (enqueueNodeCopyAtVersion) skips.
func placementTenant(ctx context.Context, q txQuerier, artifactHash string) (string, error) {
	var tenantID string
	err := q.QueryRowContext(ctx,
		`SELECT tenant_id::text FROM foghorn.artifacts WHERE artifact_hash = $1`,
		artifactHash).Scan(&tenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return tenantID, nil
}

// enqueueNodeCopy writes one node-copy transition to the durable outbox within tx.
// It assigns a fresh monotonic version from a Postgres sequence inside the same
// transaction (the analytics ReplacingMergeTree version) AND records it on the row's
// last_emitted_version (the live version on GAINED/UPDATED, 0 on LOST), so
// reconciliation re-emits only rows the projection is missing. The UPDATE is a no-op when
// the row was already deleted (an explicit-delete LOST). atMs (UnixMilli) is the event
// time; 0 uses emit-time.
func enqueueNodeCopy(ctx context.Context, tx *sql.Tx, tenantID, artifactHash, nodeID, role string, transition ipcpb.ArtifactNodeCopyEvent_Transition, isComplete bool, sizeBytes, atMs int64) error {
	if tenantID == "" {
		// A live placement mutation MUST commit its node-copy analytics event in the SAME tx (the
		// state-and-capture atomicity invariant). tenant_id is NOT NULL on foghorn.artifacts, so an empty
		// tenant here means the artifact row is missing — we can't attribute the event, so fail the tx
		// rather than silently commit the mutation without it. (Reconcile re-affirm of an already-deleted
		// artifact uses enqueueNodeCopyAtVersion, which still skips.)
		return fmt.Errorf("node-copy emit for artifact %s has no tenant attribution (artifact row missing?) — refusing to commit placement change without its analytics event", artifactHash)
	}
	var version int64
	if verr := tx.QueryRowContext(ctx, `SELECT nextval('foghorn.artifact_node_copy_version_seq')`).Scan(&version); verr != nil {
		return verr
	}
	// last_emitted_version records the version of the last event EMITTED for this row: a
	// present event (GAINED/UPDATED) records the row's live version; a LOST records 0. It
	// proves emission, not that ClickHouse still holds the projection. A raw writer that
	// later restores presence (is_orphaned=false) without emitting leaves it at 0, so both
	// the synchronous RefreshNodeCopy helper and the reconcile backstop re-emit GAINED.
	rowVersion := version
	if transition == ipcpb.ArtifactNodeCopyEvent_LOST {
		rowVersion = 0
	}
	if _, uerr := tx.ExecContext(ctx,
		`UPDATE foghorn.artifact_nodes SET last_emitted_version = $1 WHERE artifact_hash = $2 AND node_id = $3`,
		rowVersion, artifactHash, nodeID); uerr != nil {
		return uerr
	}
	return enqueueNodeCopyAtVersion(ctx, tx, tenantID, artifactHash, nodeID, role, transition, isComplete, sizeBytes, atMs, version)
}

// enqueueNodeCopyAtVersion emits at an explicit version without minting a new one or
// touching last_emitted_version — used by reconciliation to re-affirm a copy at its
// recorded (or first-assigned) version.
func enqueueNodeCopyAtVersion(ctx context.Context, tx *sql.Tx, tenantID, artifactHash, nodeID, role string, transition ipcpb.ArtifactNodeCopyEvent_Transition, isComplete bool, sizeBytes, atMs, version int64) error {
	if tenantID == "" {
		return nil
	}
	if atMs == 0 {
		atMs = time.Now().UnixMilli()
	}
	return artifactoutbox.EnqueueArtifactNodeCopyTx(ctx, tx, tenantID, &ipcpb.ArtifactNodeCopyEvent{
		ArtifactHash: artifactHash,
		NodeId:       nodeID,
		Role:         role,
		Transition:   transition,
		IsComplete:   isComplete,
		SizeBytes:    sizeBytes,
		TimestampMs:  atMs,
		Version:      uint64(version),
	})
}

// roleAfterUpsert reflects the origin-wins guard: an existing origin row keeps its
// role; anything else is (or becomes) a cache row.
func roleAfterUpsert(priorRole string, existed bool) string {
	if existed && priorRole == "origin" {
		return "origin"
	}
	return "cache"
}

// emitPresentTx enqueues a node-copy event for a row that is present after an upsert:
// GAINED when it first became present (freshly inserted, or previously orphaned and
// now restored), or UPDATED when an already-present row changed its role or flipped
// incomplete→complete. All inputs are derived atomically by the caller (row locked
// FOR UPDATE + `xmax = 0` inserted flag), so concurrent duplicate writes don't
// double-emit. A pure size change does not emit (size travels in every event's
// absolute state; emitting on size alone would flood on growing DVR copies).
func emitPresentTx(ctx context.Context, tx *sql.Tx, hash, nodeID, role string, isComplete bool, size int64, inserted, priorExisted, priorOrphaned, priorComplete bool, priorRole string, atMs int64) error {
	becamePresent := inserted || (priorExisted && priorOrphaned)
	presentBefore := priorExisted && !priorOrphaned
	attrChanged := presentBefore && (role != priorRole || (isComplete && !priorComplete))

	var transition ipcpb.ArtifactNodeCopyEvent_Transition
	switch {
	case becamePresent:
		transition = ipcpb.ArtifactNodeCopyEvent_GAINED
	case attrChanged:
		transition = ipcpb.ArtifactNodeCopyEvent_UPDATED
	default:
		return nil
	}
	tenant, err := placementTenant(ctx, tx, hash)
	if err != nil {
		return err
	}
	return enqueueNodeCopy(ctx, tx, tenant, hash, nodeID, role, transition, isComplete, size, atMs)
}

type lostRow struct {
	hash string
	role string
}

// scanLostRows collects (hash, role) from an orphan-marking UPDATE ... RETURNING.
func scanLostRows(rows *sql.Rows, err error) ([]lostRow, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []lostRow
	for rows.Next() {
		var lr lostRow
		if scanErr := rows.Scan(&lr.hash, &lr.role); scanErr != nil {
			return nil, scanErr
		}
		out = append(out, lr)
	}
	return out, rows.Err()
}

// emitLost enqueues a LOST node-copy event per orphaned/removed row, within tx. atMs
// versions the events in report/signal capture order.
func emitLost(ctx context.Context, tx *sql.Tx, nodeID string, rows []lostRow, atMs int64) error {
	for _, lr := range rows {
		tenant, err := placementTenant(ctx, tx, lr.hash)
		if err != nil {
			return err
		}
		if err := enqueueNodeCopy(ctx, tx, tenant, lr.hash, nodeID, lr.role,
			ipcpb.ArtifactNodeCopyEvent_LOST, false, 0, atMs); err != nil {
			return err
		}
	}
	return nil
}

func isRetryableArtifactUpsertError(err error) bool {
	switch database.SQLState(err) {
	case "40P01", "40001":
		return true
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
	// An empty s3URL means "unchanged" (COALESCE keeps the stored value), never "clear" — a sync
	// update must not erase an artifact's durable S3 attribution.
	if status == "synced" {
		_, err := db.ExecContext(ctx, `
			UPDATE foghorn.artifacts
			SET sync_status = 'synced',
			    s3_url = COALESCE(NULLIF($2, ''), s3_url),
			    last_sync_attempt = NOW(),
			    sync_error = NULL
			WHERE artifact_hash = $1
		`, artifactHash, s3URL)
		return err
	}
	_, err := db.ExecContext(ctx, `
		UPDATE foghorn.artifacts
		SET sync_status = $2,
		    s3_url = COALESCE(NULLIF($3, ''), s3_url),
		    last_sync_attempt = NOW(),
		    sync_error = NULL
		WHERE artifact_hash = $1
	`, artifactHash, status, s3URL)
	return err
}

// AddCachedNode records that a node has a local copy of an artifact.
// Cache-side write — does NOT downgrade an existing origin row.
func (r *artifactRepositoryDB) AddCachedNode(ctx context.Context, artifactHash, nodeID string) error {
	return r.addCached(ctx, artifactHash, nodeID, "", 0)
}

// AddCachedNodeWithPath records that a node has a local copy of an artifact with path details.
// Cache-side write — does NOT downgrade an existing origin row.
func (r *artifactRepositoryDB) AddCachedNodeWithPath(ctx context.Context, artifactHash, nodeID, filePath string, sizeBytes int64) error {
	return r.addCached(ctx, artifactHash, nodeID, filePath, sizeBytes)
}

// addCached upserts a cache placement and emits GAINED (cache) when the node
// becomes newly present for the artifact and isn't already its origin, bounded to
// genuine transitions (not per-poll churn). Its only production caller is the
// sync-complete path (AddCachedNode). Read-through relay block caches
// (<asset>.blocks/ directories) do not reach this path: the sidecar poller skips
// directories, so partial edge caches are absent from placement telemetry.
func (r *artifactRepositoryDB) addCached(ctx context.Context, artifactHash, nodeID, filePath string, sizeBytes int64) error {
	if db == nil {
		return sql.ErrConnDone
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort

	if err := AddCachedNodeCopyTx(ctx, tx, artifactHash, nodeID, filePath, sizeBytes); err != nil {
		return err
	}
	return tx.Commit()
}

// AddCachedNodeCopyTx upserts a cache placement and emits GAINED (cache) on a genuine transition,
// inside the caller's transaction. Extracted so the sync-completion path can record the node copy
// atomically with the artifact's terminal transition rather than in a separate connection.
func AddCachedNodeCopyTx(ctx context.Context, tx *sql.Tx, artifactHash, nodeID, filePath string, sizeBytes int64) error {
	var priorRole string
	var priorOrphaned, priorComplete bool
	existed := true
	// FOR UPDATE serializes concurrent writers on this (artifact, node).
	if priorErr := tx.QueryRowContext(ctx,
		`SELECT role, is_orphaned, is_complete FROM foghorn.artifact_nodes WHERE artifact_hash = $1 AND node_id = $2 FOR UPDATE`,
		artifactHash, nodeID).Scan(&priorRole, &priorOrphaned, &priorComplete); priorErr != nil {
		if errors.Is(priorErr, sql.ErrNoRows) {
			existed = false
		} else {
			return priorErr
		}
	}
	var inserted bool
	var rowSize sql.NullInt64
	if uerr := tx.QueryRowContext(ctx, cacheUpsertSQL+` RETURNING (xmax = 0), size_bytes`,
		artifactHash, nodeID, filePath, sizeBytes).Scan(&inserted, &rowSize); uerr != nil {
		return uerr
	}
	// Emit the persisted row size, not the caller's argument: AddCachedNode passes 0,
	// but the row (via COALESCE in the upsert) keeps the real size from a prior write —
	// so the transition doesn't erase a known size to NULL.
	emitSize := sizeBytes
	if rowSize.Valid {
		emitSize = rowSize.Int64
	}
	// AddCachedNode fires after a successful sync, so the cache copy is complete. This
	// emits even when the row already existed present-but-incomplete (a poller row a
	// sync just completed), because emitPresentTx also fires on incomplete→complete.
	return emitPresentTx(ctx, tx, artifactHash, nodeID, roleAfterUpsert(priorRole, existed),
		true, emitSize, inserted, existed, priorOrphaned, priorComplete, priorRole, 0)
}

// dvrOriginUpsertSQL registers the recording node as the (incomplete) origin of a parent
// DVR row, carrying base_url. is_complete stays whatever it was (a new parent DVR is
// incomplete; completeness is registered per chapter under its own hash), so this never
// makes the parent relayable. role is forced to 'origin'.
const dvrOriginUpsertSQL = `
	INSERT INTO foghorn.artifact_nodes (artifact_hash, node_id, base_url, cached_at, last_seen_at, is_orphaned, role, is_complete)
	VALUES ($1, $2, $3, NOW(), NOW(), false, 'origin', false)
	ON CONFLICT (artifact_hash, node_id) DO UPDATE SET
		base_url = EXCLUDED.base_url,
		last_seen_at = NOW(),
		is_orphaned = false,
		cached_at = COALESCE(foghorn.artifact_nodes.cached_at, NOW()),
		role = 'origin'`

// RegisterDVRRecordingOrigin registers the DVR recording node as origin (with base_url)
// through the transactional transition path, so a cache→origin promotion emits UPDATED
// instead of being missed by presence-only refresh. It mirrors RegisterOriginArtifact
// but keeps the parent DVR incomplete.
func (r *artifactRepositoryDB) RegisterDVRRecordingOrigin(ctx context.Context, artifactHash, nodeID, baseURL string) error {
	if db == nil {
		return sql.ErrConnDone
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort

	var priorRole string
	var priorComplete, priorOrphaned bool
	priorExisted := true
	if priorErr := tx.QueryRowContext(ctx,
		`SELECT role, is_complete, is_orphaned FROM foghorn.artifact_nodes WHERE artifact_hash = $1 AND node_id = $2 FOR UPDATE`,
		artifactHash, nodeID).Scan(&priorRole, &priorComplete, &priorOrphaned); priorErr != nil {
		if errors.Is(priorErr, sql.ErrNoRows) {
			priorExisted = false
		} else {
			return priorErr
		}
	}
	var inserted, nowComplete bool
	if uerr := tx.QueryRowContext(ctx, dvrOriginUpsertSQL+` RETURNING (xmax = 0), is_complete`,
		artifactHash, nodeID, baseURL).Scan(&inserted, &nowComplete); uerr != nil {
		return uerr
	}
	if err = emitPresentTx(ctx, tx, artifactHash, nodeID, "origin", nowComplete, 0,
		inserted, priorExisted, priorOrphaned, priorComplete, priorRole, 0); err != nil {
		return err
	}
	return tx.Commit()
}

// RegisterOriginArtifact marks a node as the origin — the producer that finalized
// the file locally and can serve it as a full-file peer-relay source while that
// local copy survives. It is NOT the durable copy (that lives in object storage);
// the origin's local file is transient and orphaned by the same cleanup as a cache.
// Called from finalizers that wrote the file to disk: clip create, processing
// finalize, and DVR chapter finalize (each with its own VOD artifact hash).
// complete=true flips is_complete authoritative; pass complete=false at recording
// start to register the row before finalization.
//
// Idempotent for the same writer. Origin upserts always set role to
// 'origin'; once set, only another origin write can flip is_complete
// (cache writes via AddCachedNode* preserve the existing
// role/is_complete via their own guards).
// RegisterOriginArtifactTx performs the origin upsert + node-copy transition emission on the
// CALLER's transaction, so a caller (e.g. processing completion) can make origin placement part
// of one atomic terminal transition. RegisterOriginArtifact wraps this in its own tx.
func RegisterOriginArtifactTx(ctx context.Context, tx *sql.Tx, artifactHash, nodeID, filePath string, sizeBytes int64, complete bool) error {
	// FOR UPDATE-locked prior read drives atomic transition detection. priorRole lets
	// emitPresentTx emit UPDATED when a present cache copy is promoted to origin.
	var priorRole string
	var priorComplete, priorOrphaned bool
	priorExisted := true
	if priorErr := tx.QueryRowContext(ctx,
		`SELECT role, is_complete, is_orphaned FROM foghorn.artifact_nodes WHERE artifact_hash = $1 AND node_id = $2 FOR UPDATE`,
		artifactHash, nodeID).Scan(&priorRole, &priorComplete, &priorOrphaned); priorErr != nil {
		if errors.Is(priorErr, sql.ErrNoRows) {
			priorExisted = false
		} else {
			return priorErr
		}
	}
	var inserted, nowComplete bool
	if uerr := tx.QueryRowContext(ctx, originUpsertSQL+` RETURNING (xmax = 0), is_complete`,
		artifactHash, nodeID, filePath, sizeBytes, complete).Scan(&inserted, &nowComplete); uerr != nil {
		return uerr
	}
	// GAINED when the origin copy first becomes present; UPDATED when an already-present
	// copy is promoted to origin or flips incomplete→complete.
	return emitPresentTx(ctx, tx, artifactHash, nodeID, "origin", nowComplete, sizeBytes,
		inserted, priorExisted, priorOrphaned, priorComplete, priorRole, 0)
}

func (r *artifactRepositoryDB) RegisterOriginArtifact(ctx context.Context, artifactHash, nodeID, filePath string, sizeBytes int64, complete bool) error {
	if db == nil {
		return sql.ErrConnDone
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort

	if err = RegisterOriginArtifactTx(ctx, tx, artifactHash, nodeID, filePath, sizeBytes, complete); err != nil {
		return err
	}
	return tx.Commit()
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

// ReconcileNodeCopies emits GAINED for present local copies that have never been emitted
// (last_emitted_version = 0) — i.e. rows created by non-emitting writers (DVR-start / reconciler /
// segment inserts, which leave last_emitted_version at its default 0). Each row is handled in its
// own transaction under `FOR UPDATE`, so it cannot race a concurrent LOST: the LOST either
// commits first (row is then orphaned/gone and skipped) or waits and then supersedes
// this GAINED with a higher version. Once emitted a row's last_emitted_version is > 0 and
// it is never re-emitted, so this never mints fake transition history for stable rows.
// Safe to run on every replica (the FOR UPDATE + version guard makes it idempotent) and
// on a timer (it only touches the small set of un-emitted rows). Returns the count emitted.
func (r *artifactRepositoryDB) ReconcileNodeCopies(ctx context.Context) (int, error) {
	if db == nil {
		return 0, sql.ErrConnDone
	}

	// (1) Durable disconnect-loss backstop: orphan + LOST for present rows unseen well
	// past the per-node poll sweep. A gone node never reports again, so if its one-shot
	// disconnect-orphan failed nothing else would ever emit its LOST; this heals that.
	emitted, err := r.sweepStalePresent(ctx)
	if err != nil {
		return emitted, err
	}

	// (2) Seed never-emitted present copies (last_emitted_version = 0): copies created by
	// non-emitting writers (DVR-start / reconciler / segment inserts). Candidate keys only
	// (the authoritative read happens under FOR UPDATE per row), bounded to seedBatch per
	// pass so a large un-emitted set is not loaded at once — the next pass picks up the
	// remainder.
	const seedBatch = 1000
	keys, err := func() ([][2]string, error) {
		rows, qerr := db.QueryContext(ctx, `
			SELECT artifact_hash, node_id FROM foghorn.artifact_nodes
			WHERE is_orphaned = false AND last_emitted_version = 0
			ORDER BY artifact_hash, node_id
			LIMIT $1`, seedBatch)
		if qerr != nil {
			return nil, qerr
		}
		defer rows.Close()
		var out [][2]string
		for rows.Next() {
			var hash, node string
			if scanErr := rows.Scan(&hash, &node); scanErr != nil {
				return nil, scanErr
			}
			out = append(out, [2]string{hash, node})
		}
		return out, rows.Err()
	}()
	if err != nil {
		return emitted, err
	}

	for _, k := range keys {
		did, rerr := r.reconcileOne(ctx, k[0], k[1])
		if rerr != nil {
			return emitted, rerr
		}
		if did {
			emitted++ // count only rows that actually emitted (the row may have raced away)
		}
	}
	return emitted, nil
}

// sweepStalePresent orphans present rows whose last_seen_at is well past the 10-minute
// per-node poll sweep and emits LOST for each, bounded per pass. LOST always supersedes
// (fresh monotonic version), so a node that legitimately returns is re-GAINED with an
// even newer version — no lost update.
func (r *artifactRepositoryDB) sweepStalePresent(ctx context.Context) (int, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort

	// FOR UPDATE SKIP LOCKED locks each candidate so two replicas can't both orphan it;
	// the final UPDATE re-checks is_orphaned + last_seen_at so a heartbeat landing between
	// selection and update (refreshing last_seen_at, or a concurrent restore) is not
	// clobbered — that row simply isn't updated and emits no LOST.
	orphaned, err := scanLostNodeRows(tx.QueryContext(ctx, `
		WITH stale AS (
			SELECT artifact_hash, node_id FROM foghorn.artifact_nodes
			WHERE is_orphaned = false AND last_seen_at < NOW() - INTERVAL '15 minutes'
			ORDER BY last_seen_at
			LIMIT 500
			FOR UPDATE SKIP LOCKED
		)
		UPDATE foghorn.artifact_nodes an SET is_orphaned = true
		FROM stale
		WHERE an.artifact_hash = stale.artifact_hash AND an.node_id = stale.node_id
		  AND an.is_orphaned = false
		  AND an.last_seen_at < NOW() - INTERVAL '15 minutes'
		RETURNING an.artifact_hash, an.node_id, an.role`))
	if err != nil {
		return 0, err
	}
	for _, lr := range orphaned {
		tenant, terr := placementTenant(ctx, tx, lr.hash)
		if terr != nil {
			return 0, terr
		}
		if eerr := enqueueNodeCopy(ctx, tx, tenant, lr.hash, lr.nodeID, lr.role,
			ipcpb.ArtifactNodeCopyEvent_LOST, false, 0, 0); eerr != nil {
			return 0, eerr
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(orphaned), nil
}

// lostNodeRow is a stale (artifact, node) row being orphaned by the global sweep.
type lostNodeRow struct {
	hash, nodeID, role string
}

func scanLostNodeRows(rows *sql.Rows, err error) ([]lostNodeRow, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []lostNodeRow
	for rows.Next() {
		var lr lostNodeRow
		if scanErr := rows.Scan(&lr.hash, &lr.nodeID, &lr.role); scanErr != nil {
			return nil, scanErr
		}
		out = append(out, lr)
	}
	return out, rows.Err()
}

// RefreshNodeCopy synchronously re-emits GAINED for one present copy whose analytics
// projection is absent (last_emitted_version=0). Writers that restore/create presence
// outside the emitting paths call it so the transition lands immediately; it is the
// same locked, idempotent operation reconciliation runs, so a no-op when the copy is
// already present or gone.
func (r *artifactRepositoryDB) RefreshNodeCopy(ctx context.Context, artifactHash, nodeID string) error {
	if db == nil {
		return sql.ErrConnDone
	}
	_, err := r.reconcileOne(ctx, artifactHash, nodeID)
	return err
}

// reconcileOne emits GAINED for one still-present, still-unemitted copy under FOR UPDATE
// SKIP LOCKED (so a concurrent replica's lock doesn't block or double-emit). Returns
// whether it actually emitted (false when the row raced away or was already emitted).
func (r *artifactRepositoryDB) reconcileOne(ctx context.Context, artifactHash, nodeID string) (bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort

	var role, tenant string
	var complete bool
	var size, lastEmittedVersion int64
	err = tx.QueryRowContext(ctx, `
		SELECT an.role, an.is_complete, COALESCE(an.size_bytes, 0), an.last_emitted_version, a.tenant_id::text
		FROM foghorn.artifact_nodes an
		JOIN foghorn.artifacts a ON a.artifact_hash = an.artifact_hash
		WHERE an.artifact_hash = $1 AND an.node_id = $2 AND an.is_orphaned = false
		FOR UPDATE OF an SKIP LOCKED`, artifactHash, nodeID).Scan(&role, &complete, &size, &lastEmittedVersion, &tenant)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil // orphaned/deleted/gone, or locked by another replica
	}
	if err != nil {
		return false, err
	}
	if lastEmittedVersion != 0 {
		return false, nil // already emitted by a concurrent path
	}
	if role == "" {
		role = "cache"
	}
	// enqueueNodeCopy mints a fresh version and records it on the row, so subsequent
	// reconciles skip it and any later LOST supersedes it.
	if err := enqueueNodeCopy(ctx, tx, tenant, artifactHash, nodeID, role,
		ipcpb.ArtifactNodeCopyEvent_GAINED, complete, size, 0); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

const deleteNodeArtifactSQL = `DELETE FROM foghorn.artifact_nodes WHERE artifact_hash = $1 AND node_id = $2`

// DeleteNodeArtifact removes one node's local-copy row (explicit deletion/eviction)
// and emits a LOST placement in the same transaction, so the analytics projection
// doesn't keep the node present=true after the copy is gone. atMs is the event
// timestamp (signal receipt time; ordering is the monotonic version).
func (r *artifactRepositoryDB) DeleteNodeArtifact(ctx context.Context, artifactHash, nodeID string, reportedAtMs int64) error {
	if db == nil {
		return sql.ErrConnDone
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort

	if err := DeleteNodeArtifactTx(ctx, tx, artifactHash, nodeID, reportedAtMs); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteNodeArtifactTx orphans one (artifact, node) copy and emits LOST when it was present, inside
// the caller's transaction — so a completion handler can drop a failed node's copy atomically with
// the artifact's status transition (e.g. local_missing recovery).
func DeleteNodeArtifactTx(ctx context.Context, tx *sql.Tx, artifactHash, nodeID string, reportedAtMs int64) error {
	var role string
	existed := true
	if perr := tx.QueryRowContext(ctx,
		`SELECT role FROM foghorn.artifact_nodes WHERE artifact_hash = $1 AND node_id = $2 FOR UPDATE`,
		artifactHash, nodeID).Scan(&role); perr != nil {
		if errors.Is(perr, sql.ErrNoRows) {
			existed = false
		} else {
			return perr
		}
	}
	if _, err := tx.ExecContext(ctx, deleteNodeArtifactSQL, artifactHash, nodeID); err != nil {
		return err
	}
	if existed {
		if err := emitLost(ctx, tx, nodeID, []lostRow{{hash: artifactHash, role: role}}, reportedAtMs); err != nil {
			return err
		}
	}
	return nil
}

func (r *artifactRepositoryDB) MarkNodeArtifactsOrphaned(ctx context.Context, nodeID string, reportedAtMs int64, reportFence, reportSeq int64) error {
	if db == nil {
		return sql.ErrConnDone
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort

	// Reject a stale empty report: without this, a delayed older "node holds nothing" report could
	// orphan copies a newer report already restored (the described corruption).
	if stale, gerr := applyReportRevisionGuard(ctx, tx, nodeID, reportFence, reportSeq); gerr != nil {
		return gerr
	} else if stale {
		return nil
	}

	// Orphan every present copy on the node except an incomplete origin row (an active DVR still being
	// written). Runs only on the eviction/disconnect path; reports never drive a negative diff here.
	const orphanNodeSQL = `
		UPDATE foghorn.artifact_nodes
		SET is_orphaned = true, last_seen_at = NOW()
		WHERE node_id = $1 AND is_orphaned = false
		  AND NOT (role = 'origin' AND is_complete = false)
		RETURNING artifact_hash, role`
	orphaned, err := scanLostRows(tx.QueryContext(ctx, orphanNodeSQL, nodeID))
	if err != nil {
		return err
	}
	if err = emitLost(ctx, tx, nodeID, orphaned, reportedAtMs); err != nil {
		return err
	}
	return tx.Commit()
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
