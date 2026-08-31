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
	"frameworks/api_balancing/internal/database/foghorndb"
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
	rows, err := foghorndb.New(db).ListActiveClips(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]state.ClipRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, state.ClipRecord{
			ClipHash: row.ArtifactHash, TenantID: row.TenantID, InternalName: row.StreamInternalName,
			NodeID: row.NodeID, Status: row.Status.String, StoragePath: row.FilePath,
			SizeBytes: row.SizeBytes, StorageLocation: row.StorageLocation,
		})
	}
	return out, nil
}

func (r *clipRepositoryDB) ResolveInternalNameByRequestID(ctx context.Context, requestID string) (string, error) {
	if db == nil {
		return "", sql.ErrConnDone
	}
	internalName, err := foghorndb.New(db).ResolveClipInternalNameByRequestID(ctx, sql.NullString{String: requestID, Valid: true})
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
	needsSync, err := foghorndb.New(db).ClipNeedsDtshSync(ctx, clipHash)
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
	rows, err := foghorndb.New(db).ListAllDVR(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]state.DVRRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, state.DVRRecord{
			Hash: row.ArtifactHash, TenantID: row.TenantID, InternalName: row.StreamInternalName,
			StorageNodeID: row.NodeID, SourceURL: row.BaseUrl, Status: row.Status.String,
			DurationSec: row.DurationSeconds, SizeBytes: row.SizeBytes, ManifestPath: row.ManifestPath,
		})
	}
	return out, nil
}

func (r *dvrRepositoryDB) ResolveInternalNameByHash(ctx context.Context, dvrHash string) (string, error) {
	if db == nil {
		return "", sql.ErrConnDone
	}
	internalName, err := foghorndb.New(db).ResolveDVRInternalNameByHash(ctx, dvrHash)
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

	qtx := foghorndb.New(tx)
	// Lock the row and read its prior status, identity, and the durable dispatch owner node
	// (dvr_start_dispatch.node_id, persisted at StartDVR and retained through finalize) transactionally
	// with the mutation below.
	row, err := qtx.LockDVRProgressArtifact(ctx, dvrHash)
	if errors.Is(err, sql.ErrNoRows) {
		// Row missing: nothing to promote, nothing to emit, nothing applied.
		return false, "", tx.Commit()
	}
	if err != nil {
		return false, "", err
	}
	prevStatus := row.Status.String
	tenantID := row.TenantID
	streamID := row.StreamID
	internalName := row.StreamInternalName
	dispatchNode := row.DispatchNode

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
	if err = qtx.RecordDVRProgress(ctx, foghorndb.RecordDVRProgressParams{ArtifactHash: dvrHash, SizeBytes: sizeBytes}); err != nil {
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
	return foghorndb.New(db).RecordDVRCompletion(ctx, foghorndb.RecordDVRCompletionParams{
		Status: finalStatus, DurationSeconds: durationSeconds,
		SizeBytes: sizeBytes, ManifestPath: manifestPath,
		ErrorMessage: errorMsg, ArtifactHash: dvrHash,
	})
}

// NeedsDtshSync returns true if the DVR is synced to S3 but .dtsh files weren't included
func (r *dvrRepositoryDB) NeedsDtshSync(ctx context.Context, dvrHash string) bool {
	if db == nil {
		return false
	}
	needsSync, err := foghorndb.New(db).DVRNeedsDtshSync(ctx, dvrHash)
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
	rows, err := foghorndb.New(db).ListAllNodes(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]state.NodeRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, state.NodeRecord{NodeID: row.NodeID, BaseURL: row.BaseUrl, OutputsJSON: string(row.Outputs), LastUpdated: row.LastUpdated.Time})
	}
	return out, nil
}

func (r *nodeRepositoryDB) ListNodeMaintenance(ctx context.Context) ([]state.NodeMaintenanceRecord, error) {
	if db == nil {
		return nil, sql.ErrConnDone
	}
	rows, err := foghorndb.New(db).ListNodeMaintenance(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]state.NodeMaintenanceRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, state.NodeMaintenanceRecord{
			NodeID: row.NodeID, Mode: state.NodeOperationalMode(row.Mode), SetAt: row.SetAt.Time, SetBy: row.SetBy,
		})
	}
	return out, nil
}

func (r *nodeRepositoryDB) UpsertNodeOutputs(ctx context.Context, nodeID string, baseURL string, outputsJSON string) error {
	if db == nil {
		return sql.ErrConnDone
	}
	return foghorndb.New(db).UpsertNodeOutputs(ctx, foghorndb.UpsertNodeOutputsParams{
		NodeID: nodeID, BaseUrl: baseURL, Outputs: json.RawMessage(outputsJSON),
	})
}

func (r *nodeRepositoryDB) DeleteNodeOutputs(ctx context.Context, nodeID string) error {
	if db == nil {
		return nil
	}
	return foghorndb.New(db).DeleteNodeOutputs(ctx, nodeID)
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

	return foghorndb.New(db).UpsertNodeLifecycles(ctx, foghorndb.UpsertNodeLifecyclesParams{
		NodeIds: nodeIDs, Lifecycles: lifecycles,
	})
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
	return foghorndb.New(db).UpsertNodeComponents(ctx, foghorndb.UpsertNodeComponentsParams{
		NodeIds: nodeIDs, Components: components, Versions: versions,
	})
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
	return foghorndb.New(db).UpsertNodeMaintenance(ctx, foghorndb.UpsertNodeMaintenanceParams{
		NodeID: nodeID, Mode: string(mode), SetBy: setBy,
	})
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
	_, qerr := foghorndb.New(tx).AdvanceNodeArtifactReportWatermark(ctx, foghorndb.AdvanceNodeArtifactReportWatermarkParams{
		NodeID: nodeID, ConnectionFence: connectionFence, Seq: seq,
	})
	if errors.Is(qerr, sql.ErrNoRows) {
		return true, nil // a newer (fence, seq) is already stored — drop this stale report
	}
	if qerr != nil {
		return false, qerr
	}
	return false, nil
}

// AllocateNodeControlFence issues a fresh monotonic ownership fence for one node.
// The durable per-node counter matches the comparison domain and survives Redis restarts.
func AllocateNodeControlFence(ctx context.Context, nodeID string) (int64, error) {
	if db == nil {
		return 0, sql.ErrConnDone
	}
	var fence int64
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var allocateErr error
		fence, allocateErr = foghorndb.New(db).AllocateNodeControlFence(ctx, nodeID)
		return allocateErr
	})
	return fence, err
}

func (r *artifactRepositoryDB) upsertArtifactsOnce(ctx context.Context, nodeID string, artifacts []state.ArtifactRecord) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort
	qtx := foghorndb.New(tx)

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
	// per-copy counter assigned when the GAINED/LOST/UPDATED row is
	// emitted, NOT this timestamp.
	var reportedAtMs int64
	if len(artifacts) > 0 {
		reportedAtMs = artifacts[0].ReportedAtMs
	}
	for _, a := range artifacts {
		if errExec := qtx.UpdateArtifactReportMetadata(ctx, foghorndb.UpdateArtifactReportMetadataParams{
			ArtifactHash: a.ArtifactHash, StreamInternalName: a.StreamName,
			AccessCount: a.AccessCount, LastAccessed: a.LastAccessed,
		}); errExec != nil {
			return errExec
		}
		role := a.Role
		if role == "" {
			role = "cache"
		}

		var priorRole string
		var priorOrphaned, priorComplete bool
		priorExisted := true
		writable, lockErr := lockPlacementWriteAgainstDeletion(ctx, qtx, a.ArtifactHash, nodeID, a.ReportedAtMs)
		if lockErr != nil {
			return lockErr
		}
		if !writable {
			continue
		}
		prior, perr := qtx.LockArtifactNodeState(ctx, foghorndb.LockArtifactNodeStateParams{ArtifactHash: a.ArtifactHash, NodeID: nodeID})
		if perr != nil {
			if errors.Is(perr, sql.ErrNoRows) {
				priorExisted = false
			} else {
				return perr
			}
		} else {
			priorRole, priorOrphaned, priorComplete = prior.Role, prior.IsOrphaned.Bool, prior.IsComplete
		}
		upserted, uerr := qtx.UpsertReportedArtifactNode(ctx, foghorndb.UpsertReportedArtifactNodeParams{
			ArtifactHash: a.ArtifactHash, NodeID: nodeID, FilePath: a.FilePath,
			SizeBytes: a.SizeBytes, SegmentCount: int64(a.SegmentCount), SegmentBytes: a.SegmentBytes,
			AccessCount: a.AccessCount, LastAccessed: a.LastAccessed, ReportedAtMs: a.ReportedAtMs,
			Role: role, IsComplete: a.IsComplete,
		})
		if errors.Is(uerr, sql.ErrNoRows) {
			continue // FK guard: artifact unknown, nothing upserted
		}
		if uerr != nil {
			return uerr
		}
		if err = emitPresentTx(ctx, tx, a.ArtifactHash, nodeID, upserted.Role, upserted.IsComplete,
			a.SizeBytes, !priorExisted, priorExisted, priorOrphaned, priorComplete, priorRole, reportedAtMs); err != nil {
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
	swept, sweepErr := qtx.OrphanStaleReportedArtifactNodes(ctx, nodeID)
	if sweepErr != nil {
		return sweepErr
	}
	orphaned := make([]lostRow, 0, len(swept))
	for _, row := range swept {
		orphaned = append(orphaned, lostRow{hash: row.ArtifactHash, role: row.Role})
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

// placementTenant resolves an artifact's owning tenant (artifact_nodes has none).
// A genuine query error propagates so the shared transaction rolls back rather than
// committing a state change without its telemetry. A missing artifact row (no FK
// parent) yields "": the live-mutation caller (enqueueNodeCopy) then FAILS the tx
// fail-closed — no placement change commits without its analytics event — while the
// reconcile re-affirm path (enqueueNodeCopyAtVersion) skips.
func placementTenant(ctx context.Context, q foghorndb.DBTX, artifactHash string) (string, error) {
	tenantID, err := foghorndb.New(q).GetArtifactPlacementTenant(ctx, artifactHash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return tenantID, nil
}

// enqueueNodeCopy writes one node-copy transition to the durable outbox within tx.
// It assigns a fresh monotonic version from a key-scoped counter inside the same
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
	qtx := foghorndb.New(tx)
	version, verr := qtx.AllocateArtifactNodeCopyVersion(ctx, foghorndb.AllocateArtifactNodeCopyVersionParams{
		ArtifactHash: artifactHash,
		NodeID:       nodeID,
	})
	if verr != nil {
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
	if uerr := qtx.SetArtifactNodeLastEmittedVersion(ctx, foghorndb.SetArtifactNodeLastEmittedVersionParams{
		LastEmittedVersion: rowVersion, ArtifactHash: artifactHash, NodeID: nodeID,
	}); uerr != nil {
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
// incomplete→complete. All inputs are derived atomically after locking the parent
// artifact and then the node row, so even an absent-row insert is serialized and
// concurrent duplicate writes don't double-emit. A pure size change does not emit (size travels in every event's
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
	q := foghorndb.New(db)
	row, err := q.GetArtifactSyncInfo(ctx, artifactHash)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	info := state.ArtifactSyncInfo{
		ArtifactHash: row.ArtifactHash, ArtifactType: row.ArtifactType,
		LifecycleStatus: row.Status, SyncStatus: row.SyncStatus,
	}
	if row.S3Url.Valid {
		info.S3URL = row.S3Url.String
	}
	if row.LastSyncAttempt.Valid {
		info.LastSyncAttempt = row.LastSyncAttempt.Time.Unix()
	}
	if row.SyncError.Valid {
		info.SyncError = row.SyncError.String
	}

	rows, err := q.ListArtifactCachedNodes(ctx, artifactHash)
	if err != nil {
		return nil, err
	}
	for _, cached := range rows {
		info.CachedNodes = append(info.CachedNodes, cached.NodeID)
		if cached.CachedAt.Valid && info.CachedAt == 0 {
			info.CachedAt = cached.CachedAt.Time.UnixMilli()
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
		return foghorndb.New(db).MarkArtifactSynced(ctx, foghorndb.MarkArtifactSyncedParams{ArtifactHash: artifactHash, S3Url: s3URL})
	}
	return foghorndb.New(db).SetArtifactSyncStatus(ctx, foghorndb.SetArtifactSyncStatusParams{
		ArtifactHash: artifactHash, SyncStatus: status, S3Url: s3URL,
	})
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

	if _, err := AddCachedNodeCopyTx(ctx, tx, artifactHash, nodeID, filePath, sizeBytes, 0); err != nil {
		return err
	}
	return tx.Commit()
}

// AddCachedNodeCopyTx upserts a cache placement and emits GAINED (cache) on a genuine transition,
// inside the caller's transaction. Extracted so the sync-completion path can record the node copy
// atomically with the artifact's terminal transition rather than in a separate connection.
func AddCachedNodeCopyTx(ctx context.Context, tx *sql.Tx, artifactHash, nodeID, filePath string, sizeBytes, nodeClockCompletedAtMs int64) (bool, error) {
	var priorRole string
	var priorOrphaned, priorComplete bool
	existed := true
	// FOR UPDATE serializes concurrent writers on this (artifact, node).
	qtx := foghorndb.New(tx)
	writable, err := lockPlacementWriteAgainstDeletion(ctx, qtx, artifactHash, nodeID, nodeClockCompletedAtMs)
	if err != nil || !writable {
		return writable, err
	}
	prior, priorErr := qtx.LockArtifactNodeState(ctx, foghorndb.LockArtifactNodeStateParams{ArtifactHash: artifactHash, NodeID: nodeID})
	if priorErr != nil {
		if errors.Is(priorErr, sql.ErrNoRows) {
			existed = false
		} else {
			return false, priorErr
		}
	} else {
		priorRole, priorOrphaned, priorComplete = prior.Role, prior.IsOrphaned.Bool, prior.IsComplete
	}
	upserted, uerr := qtx.UpsertCachedArtifactNode(ctx, foghorndb.UpsertCachedArtifactNodeParams{
		ArtifactHash: artifactHash, NodeID: nodeID, FilePath: filePath, SizeBytes: sizeBytes,
		ReportedAtMs: nodeClockCompletedAtMs,
	})
	if uerr != nil {
		return false, uerr
	}
	// Emit the persisted row size, not the caller's argument: AddCachedNode passes 0,
	// but the row (via COALESCE in the upsert) keeps the real size from a prior write —
	// so the transition doesn't erase a known size to NULL.
	emitSize := sizeBytes
	if upserted.Valid {
		emitSize = upserted.Int64
	}
	// AddCachedNode fires after a successful sync, so the cache copy is complete. This
	// emits even when the row already existed present-but-incomplete (a poller row a
	// sync just completed), because emitPresentTx also fires on incomplete→complete.
	if err := emitPresentTx(ctx, tx, artifactHash, nodeID, roleAfterUpsert(priorRole, existed),
		true, emitSize, !existed, existed, priorOrphaned, priorComplete, priorRole, nodeClockCompletedAtMs); err != nil {
		return false, err
	}
	return true, nil
}

// lockPlacementWriteAgainstDeletion establishes the shared lock order for
// node-copy writers and rejects a node-clock observation superseded by a point
// deletion. Zero is the rolling-upgrade compatibility value and remains
// permissive because it cannot be compared to a deletion timestamp.
func lockPlacementWriteAgainstDeletion(ctx context.Context, qtx *foghorndb.Queries, artifactHash, nodeID string, nodeClockObservedAtMs int64) (bool, error) {
	if _, err := qtx.LockArtifactPlacementParent(ctx, artifactHash); err != nil {
		return false, err
	}
	deletionWatermark, err := qtx.GetArtifactNodeDeletionWatermark(ctx, foghorndb.GetArtifactNodeDeletionWatermarkParams{
		ArtifactHash: artifactHash,
		NodeID:       nodeID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return nodeClockObservedAtMs <= 0 || deletionWatermark < nodeClockObservedAtMs, nil
}

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
	qtx := foghorndb.New(tx)

	var priorRole string
	var priorComplete, priorOrphaned bool
	priorExisted := true
	if _, lockErr := qtx.LockArtifactPlacementParent(ctx, artifactHash); lockErr != nil {
		return lockErr
	}
	prior, priorErr := qtx.LockDVRRecordingOrigin(ctx, foghorndb.LockDVRRecordingOriginParams{ArtifactHash: artifactHash, NodeID: nodeID})
	if priorErr != nil {
		if errors.Is(priorErr, sql.ErrNoRows) {
			priorExisted = false
		} else {
			return priorErr
		}
	} else {
		priorRole, priorComplete, priorOrphaned = prior.Role, prior.IsComplete, prior.IsOrphaned.Bool
	}
	upserted, uerr := qtx.UpsertDVRRecordingOrigin(ctx, foghorndb.UpsertDVRRecordingOriginParams{
		ArtifactHash: artifactHash, NodeID: nodeID, BaseUrl: baseURL,
	})
	if uerr != nil {
		return uerr
	}
	if err = emitPresentTx(ctx, tx, artifactHash, nodeID, "origin", upserted, 0,
		!priorExisted, priorExisted, priorOrphaned, priorComplete, priorRole, 0); err != nil {
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
	qtx := foghorndb.New(tx)
	// FOR UPDATE-locked prior read drives atomic transition detection. priorRole lets
	// emitPresentTx emit UPDATED when a present cache copy is promoted to origin.
	var priorRole string
	var priorComplete, priorOrphaned bool
	priorExisted := true
	if _, lockErr := qtx.LockArtifactPlacementParent(ctx, artifactHash); lockErr != nil {
		return lockErr
	}
	prior, priorErr := qtx.LockDVRRecordingOrigin(ctx, foghorndb.LockDVRRecordingOriginParams{ArtifactHash: artifactHash, NodeID: nodeID})
	if priorErr != nil {
		if errors.Is(priorErr, sql.ErrNoRows) {
			priorExisted = false
		} else {
			return priorErr
		}
	} else {
		priorRole, priorComplete, priorOrphaned = prior.Role, prior.IsComplete, prior.IsOrphaned.Bool
	}
	upserted, uerr := qtx.UpsertOriginArtifactNode(ctx, foghorndb.UpsertOriginArtifactNodeParams{
		ArtifactHash: artifactHash, NodeID: nodeID, FilePath: filePath, SizeBytes: sizeBytes, IsComplete: complete,
	})
	if uerr != nil {
		return uerr
	}
	// GAINED when the origin copy first becomes present; UPDATED when an already-present
	// copy is promoted to origin or flips incomplete→complete.
	return emitPresentTx(ctx, tx, artifactHash, nodeID, "origin", upserted, sizeBytes,
		!priorExisted, priorExisted, priorOrphaned, priorComplete, priorRole, 0)
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
	return foghorndb.New(db).ListOriginNodes(ctx, artifactHash)
}

// GetCachedAt retrieves the cached_at timestamp for calculating warm duration
func (r *artifactRepositoryDB) GetCachedAt(ctx context.Context, artifactHash string) (int64, error) {
	if db == nil {
		return 0, sql.ErrConnDone
	}
	cachedAt, err := foghorndb.New(db).GetArtifactCachedAt(ctx, artifactHash)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return cachedAt.UnixMilli(), nil
}

// IsSynced returns true if the artifact is synced to S3
func (r *artifactRepositoryDB) IsSynced(ctx context.Context, artifactHash string) (bool, error) {
	if db == nil {
		return false, sql.ErrConnDone
	}
	return foghorndb.New(db).IsArtifactSynced(ctx, artifactHash)
}

// ListAllNodeArtifacts returns all non-orphaned artifacts grouped by node ID (for rehydration)
func (r *artifactRepositoryDB) ListAllNodeArtifacts(ctx context.Context) (map[string][]state.ArtifactRecord, error) {
	if db == nil {
		return nil, sql.ErrConnDone
	}

	rows, err := foghorndb.New(db).ListAllNodeArtifacts(ctx)
	if err != nil {
		return nil, err
	}
	result := make(map[string][]state.ArtifactRecord)
	for _, row := range rows {
		result[row.NodeID] = append(result[row.NodeID], state.ArtifactRecord{
			ArtifactHash: row.ArtifactHash, ArtifactType: row.ArtifactType, StreamName: row.StreamInternalName,
			FilePath: row.FilePath, SizeBytes: row.SizeBytes, CreatedAt: row.CreatedAt,
			AccessCount: row.AccessCount, LastAccessed: row.LastAccessed,
		})
	}
	return result, nil
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
	keys, err := foghorndb.New(db).ListUnemittedArtifactNodeKeys(ctx, seedBatch)
	if err != nil {
		return emitted, err
	}

	for _, k := range keys {
		did, rerr := r.reconcileOne(ctx, k.ArtifactHash, k.NodeID)
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
	qtx := foghorndb.New(tx)

	// FOR UPDATE SKIP LOCKED locks each candidate so two replicas can't both orphan it;
	// the final UPDATE re-checks is_orphaned + last_seen_at so a heartbeat landing between
	// selection and update (refreshing last_seen_at, or a concurrent restore) is not
	// clobbered — that row simply isn't updated and emits no LOST.
	rows, err := qtx.OrphanGloballyStaleArtifactNodes(ctx)
	if err != nil {
		return 0, err
	}
	orphaned := make([]lostNodeRow, 0, len(rows))
	for _, row := range rows {
		orphaned = append(orphaned, lostNodeRow{hash: row.ArtifactHash, nodeID: row.NodeID, role: row.Role})
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

	row, err := foghorndb.New(tx).LockUnemittedArtifactNode(ctx, foghorndb.LockUnemittedArtifactNodeParams{
		ArtifactHash: artifactHash, NodeID: nodeID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil // orphaned/deleted/gone, or locked by another replica
	}
	if err != nil {
		return false, err
	}
	if row.LastEmittedVersion != 0 {
		return false, nil // already emitted by a concurrent path
	}
	role := row.Role
	if role == "" {
		role = "cache"
	}
	// enqueueNodeCopy mints a fresh version and records it on the row, so subsequent
	// reconciles skip it and any later LOST supersedes it.
	if err := enqueueNodeCopy(ctx, tx, row.TenantID, artifactHash, nodeID, role,
		ipcpb.ArtifactNodeCopyEvent_GAINED, row.IsComplete, row.SizeBytes, 0); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// DeleteNodeArtifact removes one node's local-copy row (explicit deletion/eviction)
// and emits a LOST placement in the same transaction, so the analytics projection
// doesn't keep the node present=true after the copy is gone. The timestamp is the
// node clock from the deletion signal and fences older placement observations.
func (r *artifactRepositoryDB) DeleteNodeArtifact(ctx context.Context, artifactHash, nodeID string, nodeClockDeletedAtMs int64) (state.NodeArtifactDeletionOutcome, error) {
	if db == nil {
		return "", sql.ErrConnDone
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort

	outcome, err := DeleteNodeArtifactTx(ctx, tx, artifactHash, nodeID, nodeClockDeletedAtMs)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return outcome, nil
}

// DeleteNodeArtifactTx orphans one (artifact, node) copy and emits LOST when it was present, inside
// the caller's transaction — so a completion handler can drop a failed node's copy atomically with
// the artifact's status transition (e.g. local_missing recovery).
func DeleteNodeArtifactTx(ctx context.Context, tx *sql.Tx, artifactHash, nodeID string, nodeClockDeletedAtMs int64) (state.NodeArtifactDeletionOutcome, error) {
	qtx := foghorndb.New(tx)
	if _, err := qtx.LockArtifactPlacementParent(ctx, artifactHash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return state.NodeArtifactDeletionParentMissing, nil
		}
		return "", err
	}
	role, err := qtx.DeleteArtifactNodeIfNotNewer(ctx, foghorndb.DeleteArtifactNodeIfNotNewerParams{
		ArtifactHash: artifactHash,
		NodeID:       nodeID,
		DeletedAtMs:  nodeClockDeletedAtMs,
	})
	if errors.Is(err, sql.ErrNoRows) {
		_, placementErr := qtx.LockArtifactNodeState(ctx, foghorndb.LockArtifactNodeStateParams{
			ArtifactHash: artifactHash,
			NodeID:       nodeID,
		})
		if placementErr == nil {
			return state.NodeArtifactDeletionFenced, nil
		}
		if errors.Is(placementErr, sql.ErrNoRows) {
			return state.NodeArtifactDeletionAbsent, nil
		}
		return "", placementErr
	}
	if err != nil {
		return "", err
	}
	if err := emitLost(ctx, tx, nodeID, []lostRow{{hash: artifactHash, role: role}}, nodeClockDeletedAtMs); err != nil {
		return "", err
	}
	return state.NodeArtifactDeletionApplied, nil
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
	rows, err := foghorndb.New(tx).OrphanNodeArtifacts(ctx, nodeID)
	if err != nil {
		return err
	}
	orphaned := make([]lostRow, 0, len(rows))
	for _, row := range rows {
		orphaned = append(orphaned, lostRow{hash: row.ArtifactHash, role: row.Role})
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
	needsSync, err := foghorndb.New(db).VODNeedsDtshSync(ctx, artifactHash)
	if err != nil {
		return false
	}
	return needsSync
}
