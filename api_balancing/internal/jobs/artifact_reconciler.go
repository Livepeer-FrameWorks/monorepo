package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/database/foghorndb"
	"frameworks/api_balancing/internal/state"
	commodoreclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// ReconcilerS3Client defines S3 operations needed by the artifact reconciler.
type ReconcilerS3Client interface {
	GeneratePresignedPUT(key string, expiry time.Duration) (string, error)
	BuildClipS3Key(tenantID, streamName, clipHash, format string) string
	BuildDVRS3Key(tenantID, internalName, dvrHash string) string
	BuildVodS3Key(tenantID, artifactHash, filename string) string
	Delete(ctx context.Context, key string) error
	// ParseLocalS3URL extracts the RAW object key from an s3://bucket/prefix/key URL ONLY when the bucket is
	// THIS cell's local bucket (else it errors). Used by the active_object_key backfill so a remote-provider
	// pointer can never be rewritten as a local key.
	ParseLocalS3URL(s3URL string) (string, error)
}

// FreezeRequestSender sends a FreezeRequest to a specific node.
type FreezeRequestSender func(nodeID string, req *ipcpb.FreezeRequest) error

// catalogBackfillBatch bounds the catalog-revision backfill per pass (rows at catalog_revision=0).
// A full batch self-triggers another pass so the backfill converges without waiting a full interval.
const catalogBackfillBatch = 500

// dvrChildRepairBatch bounds the per-pass repair that cascades still-live children of soft-deleted
// DVR parents. Converges once every such parent's children are cascaded.
const dvrChildRepairBatch = 200

// ReconcilerCommodoreClient defines Commodore operations needed by the reconciler.
// Catalog projection goes exclusively through UpdateArtifactCatalogSnapshot (the sole
// revision-guarded writer); the per-field update RPCs were removed.
type ReconcilerCommodoreClient interface {
	ResolveClipHash(ctx context.Context, hash string) (*commodorepb.ResolveClipHashResponse, error)
	ResolveDVRHash(ctx context.Context, hash string) (*commodorepb.ResolveDVRHashResponse, error)
	ResolveVodHash(ctx context.Context, hash string) (*commodorepb.ResolveVodHashResponse, error)
	UpdateArtifactCatalogSnapshot(ctx context.Context, req *commodorepb.UpdateArtifactCatalogSnapshotRequest) (*commodorepb.UpdateArtifactCatalogSnapshotResponse, error)
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
	ClusterID       string        // This cluster's ID; only locally-authoritative (origin) rows are projected
	// OnNodeIndexed emits a node-copy GAINED for a row this reconciler onboarded (which
	// may restore an artifact_nodes row that was previously LOST). Nil = no emit.
	OnNodeIndexed func(ctx context.Context, artifactHash, nodeID string)
}

// ArtifactReconciler periodically scans for artifacts that need sync and
// proactively sends FreezeRequests to the nodes holding them.
type ArtifactReconciler struct {
	db         *sql.DB
	s3Client   ReconcilerS3Client
	commodore  ReconcilerCommodoreClient
	sendFreeze FreezeRequestSender
	// prepareFreeze is the ONE shared freeze-assignment contract (control.PrepareLocalFreezeAssignment);
	// a seam so the reconciler's freeze dispatch is unit-testable without control's routing/backing globals.
	prepareFreeze func(ctx context.Context, assetType, assetHash, tenantID, streamName, serverFormat, originClusterID, nodeID string, expiry time.Duration) (control.FreezeAssignment, string, bool)
	// nodeFreezeProtocolOK reports whether a locally-connected node supports staged freeze (control.NodeFreezeProtocolOK);
	// a seam so the reconciler can pre-skip a known-old local sidecar without a live registry in tests.
	nodeFreezeProtocolOK func(nodeID string) (ok bool, known bool)
	logger               logging.Logger
	interval             time.Duration
	batchSize            int
	clusterID            string
	onNodeIndexed        func(ctx context.Context, artifactHash, nodeID string)
	stopCh               chan struct{}
	triggerCh            chan struct{}
	ledgerTriggerCh      chan struct{}
	wg                   sync.WaitGroup
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
		db:                   cfg.DB,
		s3Client:             cfg.S3Client,
		commodore:            cfg.CommodoreClient,
		sendFreeze:           cfg.SendFreeze,
		prepareFreeze:        control.PrepareLocalFreezeAssignment,
		nodeFreezeProtocolOK: control.NodeFreezeProtocolOK,
		logger:               cfg.Logger,
		interval:             interval,
		batchSize:            batchSize,
		clusterID:            cfg.ClusterID,
		onNodeIndexed:        cfg.OnNodeIndexed,
		stopCh:               make(chan struct{}),
		triggerCh:            make(chan struct{}, 1),
		ledgerTriggerCh:      make(chan struct{}, 1),
	}
}

func (r *ArtifactReconciler) Start() {
	r.wg.Add(2)
	go r.run()
	go r.runLedgerSweep()
	r.logger.Info("Artifact reconciler started")
}

// runLedgerSweep drains the freeze-publication ledger on its OWN schedule + goroutine, fully decoupled from the
// catalog-projection reconcile loop, so a cleanup backlog can never delay projection. It self-triggers via
// ledgerTriggerCh when a pass drains a full batch, so a backlog drains promptly instead of one page per tick.
func (r *ArtifactReconciler) runLedgerSweep() {
	defer r.wg.Done()
	if r.db == nil {
		return
	}
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	r.reconcileFreezePublicationLedgerPass()
	for {
		select {
		case <-r.ledgerTriggerCh:
			r.reconcileFreezePublicationLedgerPass()
		case <-ticker.C:
			r.reconcileFreezePublicationLedgerPass()
		case <-r.stopCh:
			return
		}
	}
}

// triggerLedgerSweep requests an immediate ledger pass (coalesced).
func (r *ArtifactReconciler) triggerLedgerSweep() {
	if r == nil || r.ledgerTriggerCh == nil {
		return
	}
	select {
	case r.ledgerTriggerCh <- struct{}{}:
	default:
	}
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

	// Run an initial pass at startup so the first reconcile begins immediately rather than waiting
	// a full interval on a quiet process.
	r.reconcile()

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

	// Billing attribution runs FIRST and on its OWN advisory lock + timeout, decoupled from the projection
	// critical section below. It makes external per-tenant resolver calls (canMintOfficialLocally), so holding
	// the projection lock across it would let one slow resolver stall catalog projection / orphan reconciliation
	// for every replica's catch-up. It is idempotent (only flips false→true) and per-(tenant,cluster)-scoped, so
	// a distinct single-flight lock (not the projection lock) is all it needs to keep replicas from double-scanning.
	r.reconcileBillingAttribution()

	// NOTE: publication-ledger reconciliation is NOT run here — it runs on its OWN independently-scheduled loop
	// (runLedgerSweep) with its own advisory lock, so a cleanup backlog never occupies this reconcile pass's
	// context and delays catalog projection.

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	conn, err := r.db.Conn(ctx)
	if err != nil {
		r.logger.WithError(err).Warn("Failed to acquire DB connection for reconciler lock")
		return
	}
	defer conn.Close()

	queries := foghorndb.New(conn)
	acquired, err := queries.TryArtifactReconcilerLock(ctx)
	if err != nil || !acquired {
		return
	}
	defer func() { _ = queries.UnlockArtifactReconciler(ctx) }() //nolint:errcheck // best-effort session advisory unlock

	// Attribution must precede projection: a row with no origin_cluster_id is claimed by the cluster
	// that physically owns it (holds an origin node copy in this cluster-local foghorn DB). The
	// projection matches on exact origin, so an unattributed row is never claimed by a non-owning
	// cluster's snapshot.
	r.backfillOriginCluster(ctx)

	// Make active_object_key the SINGLE authoritative pointer: populate it prefix-aware from s3_url for legacy
	// synced clip/vod rows that predate version-addressing (the vod_metadata postdeploy backfill can't reach
	// clips, and pure SQL can't strip the client prefix). Bounded + idempotent; converges as rows leave the set.
	r.backfillActiveObjectKey(ctx)

	// Rows at catalog_revision = 0 are assigned a fresh revision in bounded batches so they project
	// once; converges to a no-op (new rows get their revision from the INSERT trigger).
	seeded := r.backfillCatalogRevisions(ctx)

	// Cascade the still-live children of soft-deleted DVR parents so their deletions project.
	// Bounded and idempotent; runs under the same advisory lock so replicas don't double-cascade.
	repaired := r.repairDeletedDVRChildren(ctx)

	projected, scanned := r.projectCommodoreArtifactState(ctx)
	// Either bounded step filling its batch means more catalog work is pending. Rather than wait a
	// full interval (leaving the catalog diverged), self-trigger another pass — but only AFTER
	// this function returns and releases the advisory lock, so each catch-up pass re-acquires the
	// lock fresh and a peer can interleave. Converges: backfill drains revision-0 rows; projection
	// advances/backs-off rows out of the eligible set.
	if seeded >= catalogBackfillBatch || scanned >= r.batchSize || repaired >= dvrChildRepairBatch {
		r.Trigger()
	}

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

// reconcileBillingAttribution runs the recorded-evidence durable_backend_local reconciliation under its OWN
// single-flight advisory lock ('artifact_billing_attribution') and its own timeout — deliberately NOT the
// 'artifact_reconciler' projection lock, so its bounded per-pass scan/update never delays catalog projection /
// orphan reconciliation. It claims historical local rows by RECORDED EVIDENCE ONLY (authoritative cluster empty
// or == this cell's id); it never consults the live resolver/backing (I2). Idempotent (only flips false→true); a
// lost lock (a peer holds it) or a busy pass simply retries next tick.
func (r *ArtifactReconciler) reconcileBillingAttribution() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	conn, err := r.db.Conn(ctx)
	if err != nil {
		r.logger.WithError(err).Warn("Failed to acquire DB connection for billing attribution lock")
		return
	}
	defer conn.Close()

	queries := foghorndb.New(conn)
	acquired, lErr := queries.TryArtifactBillingAttributionLock(ctx)
	if lErr != nil || !acquired {
		return
	}
	defer func() { _ = queries.UnlockArtifactBillingAttribution(ctx) }() //nolint:errcheck // best-effort session advisory unlock

	if marked, bErr := control.ReconcileBillingAttribution(ctx); bErr != nil {
		r.logger.WithError(bErr).Warn("Billing attribution reconciliation failed")
	} else if marked > 0 {
		r.logger.WithField("marked", marked).Info("Reconciled durable_backend_local billing attribution for historical local rows")
	}
}

// reconcileFreezePublicationLedgerPass runs the publication-ledger sweep under its OWN single-flight advisory
// lock ('freeze_publication_ledger') + timeout, so a cleanup backlog can never hold the 'artifact_reconciler'
// projection lock and delay catalog convergence. It self-triggers another pass when it drained a FULL batch so
// a backlog drains promptly instead of waiting a full interval.
func (r *ArtifactReconciler) reconcileFreezePublicationLedgerPass() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	conn, err := r.db.Conn(ctx)
	if err != nil {
		r.logger.WithError(err).Warn("Failed to acquire DB connection for publication ledger lock")
		return
	}
	defer conn.Close()

	queries := foghorndb.New(conn)
	acquired, lErr := queries.TryFreezePublicationLedgerLock(ctx)
	if lErr != nil || !acquired {
		return
	}
	defer func() { _ = queries.UnlockFreezePublicationLedger(ctx) }() //nolint:errcheck // best-effort session advisory unlock

	if r.reconcileFreezePublicationLedger(ctx) {
		r.triggerLedgerSweep() // a full batch means more remains — drain promptly rather than waiting a full interval
	}
}

// backfillActiveObjectKey populates foghorn.artifacts.active_object_key (the authoritative published object
// pointer) for LEGACY synced clip/vod rows that predate version-addressing, so the pointer converges to the
// single source of truth instead of leaving permanent NULLs behind a runtime read/delete fallback. The exact
// raw key is only recoverable prefix-aware via the S3 client (pure SQL cannot strip the configured prefix, and
// the vod_metadata postdeploy backfill cannot reach clips), so this parses s3_url per row.
//
// STORAGE-OWNERSHIP SAFE: it touches ONLY rows whose bytes are on THIS cell's local backend
// (durable_backend_local = true) AND whose s3_url resolves under the LOCAL bucket (ParseLocalS3URL errors on a
// foreign bucket) — a federation-adopted remote pointer is never rewritten as a local key. Updates are
// tenant-scoped. Progress is a DURABLE KEYSET over foghorn.active_object_key_backfill_cursor so an anomalous
// unparseable row can never starve later rows. DB-only + pure string parse (no external call), safe under the
// projection lock.
func (r *ArtifactReconciler) backfillActiveObjectKey(ctx context.Context) {
	if r.s3Client == nil {
		return
	}
	const backfillBatch = 500

	queries := foghorndb.New(r.db)
	lastHash, cErr := queries.GetActiveObjectKeyBackfillCursor(ctx)
	if cErr != nil {
		r.logger.WithError(cErr).Warn("active_object_key backfill cursor read failed")
		return
	}

	// STORAGE-OWNERSHIP SAFE: only LOCALLY-BACKED rows (durable_backend_local=true) — a federation-adopted
	// remote pointer is never touched. Keyset PAST the durable cursor so an anomalous skipped row can't starve
	// later rows.
	rows, err := queries.ListActiveObjectKeyBackfillRows(ctx, foghorndb.ListActiveObjectKeyBackfillRowsParams{ArtifactHash: lastHash, Limit: backfillBatch})
	if err != nil {
		r.logger.WithError(err).Warn("active_object_key backfill scan failed")
		return
	}
	backfilled := 0
	for _, rr := range rows {
		// ParseLocalS3URL errors on a foreign bucket, so a locally-backed row whose s3_url is somehow NOT under
		// the local bucket is a data anomaly — skip it (the cursor still advances past it) rather than persist a
		// bad local pointer that would misroute playback/deletion.
		key, perr := r.s3Client.ParseLocalS3URL(rr.S3Url.String)
		if perr != nil || strings.TrimSpace(key) == "" {
			r.logger.WithError(perr).WithField("artifact_hash", rr.ArtifactHash).Debug("active_object_key backfill: skipping row with non-local s3_url")
			continue
		}
		n, uErr := queries.SetLegacyActiveObjectKey(ctx, foghorndb.SetLegacyActiveObjectKeyParams{ArtifactHash: rr.ArtifactHash, TenantID: rr.TenantID, ActiveObjectKey: sql.NullString{String: key, Valid: true}})
		if uErr != nil {
			r.logger.WithError(uErr).WithField("artifact_hash", rr.ArtifactHash).Debug("active_object_key backfill update failed")
			continue
		}
		if n > 0 {
			backfilled++
		}
	}

	// Advance the durable cursor: a FULL page means more rows remain → continue past the last hash; a short page
	// means the end → wrap to the start so the next cycle re-scans (picking up newly-eligible legacy rows).
	nextHash := ""
	if len(rows) == backfillBatch {
		nextHash = rows[len(rows)-1].ArtifactHash
	}
	if uErr := queries.SetActiveObjectKeyBackfillCursor(ctx, nextHash); uErr != nil {
		r.logger.WithError(uErr).Warn("active_object_key backfill cursor advance failed")
	}
	if backfilled > 0 {
		r.logger.WithField("backfilled", backfilled).Info("Backfilled active_object_key for legacy synced rows")
	}
}

// reconcileFreezePublicationLedger is the durable backstop for freeze publication: it collects any object a
// completion recorded (BEFORE promoting) but whose guarded transaction never committed — a completion-path DB
// failure or a lost CAS whose winner already cleared the attempt identity. Rows are only considered after a
// grace period, and the sweep is REQ-AWARE: if the attempt that produced the object is STILL on the artifact
// (sync_request_id / dtsh_sync_request_id), it is retrying and the object is left alone, so the sweep can never
// race a retry into deleting an object it is about to make live. A guarded (candidate) object that equals the
// live active_object_key/active_dtsh_key is kept (its ledger row is just dropped); everything else — staging,
// or an orphaned candidate — is enqueued to the staging cleanup queue and its ledger row removed, atomically.
// reconcileFreezePublicationLedger drains one bounded, keyset-cursored page of the ledger. Returns true when it
// processed a FULL batch (more likely remains) so the caller can self-trigger another pass.
func (r *ArtifactReconciler) reconcileFreezePublicationLedger(ctx context.Context) bool {
	const batch = 500

	queries := foghorndb.New(r.db)
	lastKey, cErr := queries.GetFreezePublicationLedgerCursor(ctx)
	if cErr != nil {
		r.logger.WithError(cErr).Warn("freeze publication ledger cursor read failed")
		return false
	}
	// Keyset PAST the cursor by object_key (the PK), so rows skipped-because-retrying advance the cursor and
	// cannot starve later rows; the grace period keeps an in-flight completion from being raced.
	rows, err := queries.ListStaleFreezePublicationLedgerRows(ctx, foghorndb.ListStaleFreezePublicationLedgerRowsParams{ObjectKey: lastKey, Limit: batch})
	if err != nil {
		r.logger.WithError(err).Warn("freeze publication ledger scan failed")
		return false
	}
	collected := 0
	for _, lr := range rows {
		pointers, aErr := queries.GetArtifactPublicationPointers(ctx, foghorndb.GetArtifactPublicationPointersParams{ArtifactHash: lr.ArtifactHash, TenantID: lr.TenantID})
		artifactGone := errors.Is(aErr, sql.ErrNoRows)
		if aErr != nil && !artifactGone {
			r.logger.WithError(aErr).WithField("object_key", lr.ObjectKey).Debug("freeze publication ledger: artifact re-read failed; retrying next pass")
			continue
		}
		// The attempt that produced this object is STILL outstanding → it is retrying; never touch its objects.
		if !artifactGone && (lr.RequestID == pointers.SyncRequestID || lr.RequestID == pointers.DtshSyncRequestID) {
			continue
		}
		// A guarded candidate that is the LIVE pointer must be KEPT — only drop its ledger row.
		if lr.Guarded && !artifactGone && (lr.ObjectKey == pointers.ActiveObjectKey || lr.ObjectKey == pointers.ActiveDtshKey) {
			if dErr := queries.DeleteFreezePublicationLedgerRow(ctx, lr.ObjectKey); dErr != nil {
				r.logger.WithError(dErr).WithField("object_key", lr.ObjectKey).Debug("freeze publication ledger: failed to drop live-candidate row")
			}
			continue
		}
		// Staging, an orphaned candidate, or an object whose artifact is gone → enqueue for cleanup and drop the
		// ledger row atomically, so the object is durably collected exactly once.
		tx, txErr := r.db.BeginTx(ctx, nil)
		if txErr != nil {
			r.logger.WithError(txErr).Debug("freeze publication ledger: begin tx failed")
			continue
		}
		txQueries := queries.WithTx(tx)
		if eErr := txQueries.EnqueueStagingCleanup(ctx, foghorndb.EnqueueStagingCleanupParams{ObjectKey: lr.ObjectKey, BackendID: lr.BackendID}); eErr != nil {
			tx.Rollback() //nolint:errcheck
			r.logger.WithError(eErr).WithField("object_key", lr.ObjectKey).Debug("freeze publication ledger: enqueue failed")
			continue
		}
		if dErr := txQueries.DeleteFreezePublicationLedgerRow(ctx, lr.ObjectKey); dErr != nil {
			tx.Rollback() //nolint:errcheck
			r.logger.WithError(dErr).WithField("object_key", lr.ObjectKey).Debug("freeze publication ledger: ledger delete failed")
			continue
		}
		if cErr := tx.Commit(); cErr != nil {
			r.logger.WithError(cErr).WithField("object_key", lr.ObjectKey).Debug("freeze publication ledger: commit failed")
			continue
		}
		collected++
	}

	// Advance the durable cursor past every reviewed row: a FULL page means more remain → continue past the last
	// object_key; a short page means the end → wrap to the start so the next cycle re-checks from the top
	// (including rows skipped this cycle because their attempt was still retrying).
	nextKey := ""
	if len(rows) == batch {
		nextKey = rows[len(rows)-1].ObjectKey
	}
	if uErr := queries.SetFreezePublicationLedgerCursor(ctx, nextKey); uErr != nil {
		r.logger.WithError(uErr).Warn("freeze publication ledger cursor advance failed")
	}
	if collected > 0 {
		r.logger.WithField("collected", collected).Info("Reconciled orphaned freeze publication objects")
	}
	return len(rows) == batch
}

// backfillOriginCluster deterministically claims origin ownership for rows that have no
// origin_cluster_id but belong to THIS cluster. Because each cluster runs its own Foghorn DB,
// two signals prove local ownership: a live row that physically holds an origin node copy in the
// cluster-local foghorn.artifact_nodes, OR a deleted row (whose copies were already cleaned) that
// still carries the empty-origin "locally-created" marker — claimed so its Commodore catalog row
// can be projected away. Adopted cross-cluster pointers carry the remote origin, so they are never
// mis-claimed. Bounded per pass; converges once every owned unattributed row is attributed (new
// rows get origin_cluster_id at creation). Assigning origin does NOT bump catalog_revision (it
// isn't a projected field), so this doesn't re-dirty the queue.
func (r *ArtifactReconciler) backfillOriginCluster(ctx context.Context) {
	if r.clusterID == "" {
		return
	}
	const backfillBatch = 500
	n, err := foghorndb.New(r.db).BackfillOriginCluster(ctx, foghorndb.BackfillOriginClusterParams{Limit: backfillBatch, OriginClusterID: sql.NullString{String: r.clusterID, Valid: true}})
	if err != nil {
		r.logger.WithError(err).Warn("Origin-cluster attribution backfill failed")
		return
	}
	if n > 0 {
		r.logger.WithField("attributed", n).Info("Attributed origin cluster for legacy artifacts")
	}
}

// repairDeletedDVRChildren enforces the invariant that a deleted DVR parent has no residual chapter
// state: it cascades the children of soft-deleted DVR parents whose chapter rows still exist.
// Delegates to the tenant-scoped batch repair; each cascaded child then projects its own deletion
// to Commodore.
func (r *ArtifactReconciler) repairDeletedDVRChildren(ctx context.Context) int {
	n, err := control.RepairDeletedDVRChildrenBatch(ctx, dvrChildRepairBatch)
	if err != nil {
		r.logger.WithError(err).Warn("Deleted-DVR child repair failed")
		return n
	}
	if n > 0 {
		r.logger.WithField("repaired", n).Info("Repaired orphaned children of deleted DVR parents")
	}
	return n
}

// backfillCatalogRevisions advances catalog_revision from its durable per-artifact watermark for up
// to catalogBackfillBatch of this cluster's authoritative rows still at catalog_revision = 0,
// entering them into the projection queue (catalog_revision > catalog_synced_rev). Runs under the
// reconciler advisory lock so replicas don't double-assign; converges to a no-op.
func (r *ArtifactReconciler) backfillCatalogRevisions(ctx context.Context) int64 {
	n, err := foghorndb.New(r.db).BackfillCatalogRevisions(ctx, foghorndb.BackfillCatalogRevisionsParams{Limit: catalogBackfillBatch, OriginClusterID: sql.NullString{String: r.clusterID, Valid: true}})
	if err != nil {
		r.logger.WithError(err).Warn("Catalog-revision rollout backfill failed")
		return 0
	}
	if n > 0 {
		r.logger.WithField("seeded", n).Info("Seeded catalog revisions for revision-0 artifacts")
	}
	return n
}

// projectCommodoreArtifactState is the sole durable Commodore catalog projector. Each pass
// projects a whole authoritative snapshot for every locally-authoritative row whose
// catalog_revision (a source-owned per-artifact monotonic revision, bumped by trigger on any
// catalog change) exceeds its catalog_synced_rev watermark. Rows are served LEAST-RECENTLY-PROJECTED
// first (ORDER BY catalog_synced_rev) so a continuously-mutating cohort — active DVRs minting
// a fresh revision on every segment — cannot stay at the head of the queue and starve rows
// behind it. Non-origin (adopted pointer) rows are excluded so a remote cluster can't clobber
// origin state. The watermark advances ONLY when Commodore confirms the row is covered at the
// projected revision; a not-found row is retried, and a poison row (bad type / malformed
// tracks) is quarantined so it can't head-of-line block the queue.
// projectCommodoreArtifactState returns (advanced, scanned): advanced is the count of rows whose
// watermark moved this pass; scanned is the number of eligible rows the batch pulled. scanned ==
// batchSize signals the batch was full (more work pending) so reconcile() can self-trigger.
func (r *ArtifactReconciler) projectCommodoreArtifactState(ctx context.Context) (int, int) {
	if r.commodore == nil {
		return 0, 0
	}
	// Commodore now requires the projecting cluster's identity to assign/enforce catalog origin
	// authority. Without a cluster id this reconciler cannot project — fail the pass loudly rather
	// than emit a per-row rejection storm.
	if r.clusterID == "" {
		r.logger.Warn("Artifact reconciler has no cluster id; skipping catalog projection (source authority required)")
		return 0, 0
	}
	var rows []foghorndb.ListArtifactsForCatalogProjectionRow
	queries := foghorndb.New(r.db)
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var err error
		rows, err = queries.ListArtifactsForCatalogProjection(ctx, foghorndb.ListArtifactsForCatalogProjectionParams{Limit: int32(r.batchSize), OriginClusterID: sql.NullString{String: r.clusterID, Valid: true}})
		return err
	})
	if err != nil {
		r.logger.WithError(err).Warn("Failed to query artifacts for Commodore projection")
		return 0, 0
	}
	count := 0
	scanned := 0
	for _, row := range rows {
		scanned++
		hash, artifactType, tenantID := row.ArtifactHash, row.ArtifactType, row.TenantID
		storageCluster, syncStatus, storageLocation := row.StorageClusterID, row.SyncStatus, row.StorageLocation
		hasThumbnails, dtshSynced := row.HasThumbnails, row.DtshSynced
		lifecycleStatus, errorMessage, thumbServingCluster := row.Status, row.ErrorMessage, row.ThumbnailServingClusterID
		sizeBytes, durationMs, revision := row.SizeBytes, row.DurationMs, row.CatalogRevision
		tracksJSON := row.Tracks
		retentionUnix := sql.NullInt64{}
		if row.RetentionUntil.Valid {
			retentionUnix = sql.NullInt64{Int64: row.RetentionUntil.Time.Unix(), Valid: true}
		}
		assetType, ok := artifactAssetTypeFromString(artifactType)
		if !ok {
			// Poison row: record a quarantine watermark (NOT catalog_synced_rev) so it can't
			// head-of-line block, while keeping the row visibly uncovered. A later re-mutation
			// bumps catalog_revision past catalog_quarantined_rev and re-enqueues it.
			r.quarantineCatalogRow(ctx, hash, revision, "unsupported artifact type: "+artifactType)
			continue
		}

		// Deletion projection: a soft-deleted foghorn row removes the catalog business row and writes a
		// durable tombstone marker so /library stops showing the (retention-expired / deleted) asset.
		// Coverage is the marker, not the business row's absence: the deletion is covered only when
		// Commodore reports the tombstone marker present at >= this revision (Found=true). A response
		// without that marker (Found=false) is NOT covered — advancing on it would let purge reap the
		// foghorn row while no surviving marker blocks a lagging writer from resurrecting the asset.
		if lifecycleStatus.Valid && lifecycleStatus.String == "deleted" {
			delReq := &commodorepb.UpdateArtifactCatalogSnapshotRequest{
				TenantId:        tenantID,
				AssetType:       assetType,
				AssetKey:        hash,
				SourceRevision:  revision,
				Deleted:         true,
				SourceClusterId: strPtrOrNil(r.clusterID),
			}
			resp, delErr := r.commodore.UpdateArtifactCatalogSnapshot(ctx, delReq)
			if delErr != nil {
				r.logger.WithError(delErr).WithField("artifact_hash", hash).Warn("Failed to project catalog deletion")
				r.backoffCatalogRow(ctx, hash)
				continue
			}
			if resp.GetFound() && resp.GetCurrentRevision() >= revision {
				r.advanceCatalogWatermark(ctx, hash, revision)
				count++
			} else {
				r.backoffCatalogRow(ctx, hash)
			}
			continue
		}

		tracksPresent := tracksJSON.Valid
		var tracks []*commodorepb.MediaTrack
		if tracksPresent {
			parsed, terr := commodoreclient.UnmarshalMediaTracks([]byte(tracksJSON.String))
			if terr != nil {
				r.quarantineCatalogRow(ctx, hash, revision, "malformed tracks JSON: "+terr.Error())
				continue
			}
			tracks = parsed
		}
		snapshot := &commodorepb.UpdateArtifactCatalogSnapshotRequest{
			TenantId:         tenantID,
			AssetType:        assetType,
			AssetKey:         hash,
			SourceRevision:   revision,
			SizeBytes:        nullInt64Ptr(sizeBytes),
			DurationMs:       nullInt64Ptr(durationMs),
			TracksPresent:    tracksPresent,
			Tracks:           tracks,
			SyncStatus:       nullStringPtr(syncStatus),
			IsSynced:         boolPtr(syncStatus.Valid && syncStatus.String == "synced"),
			IsFinalized:      boolPtr(dtshSynced.Valid && dtshSynced.Bool),
			StorageLocation:  nullStringPtr(storageLocation),
			StorageClusterId: nullStringPtr(storageCluster),
			HasThumbnails:    boolPtr(hasThumbnails.Valid && hasThumbnails.Bool),
			// Authoritative thumbnail serving cluster (the official-durable cluster the thumbnail was projected to);
			// Commodore prefers it over storage/origin when building the Chandler URL. Absent → NULL, readers fall back.
			ThumbnailServingClusterId: nullStringPtr(thumbServingCluster),
			LifecycleStatus:           nullStringPtr(lifecycleStatus),
			ErrorMessage:              nullStringPtr(errorMessage),
			// Assert this cluster as the projection source so Commodore enforces origin authority:
			// only the origin cluster may mutate the artifact's catalog state.
			SourceClusterId: strPtrOrNil(r.clusterID),
			// Retention horizon → catalog, so /library shows accurate expiry for every kind
			// (chapters included). Absent = keep-forever (NULL).
			RetentionUntilUnix: nullInt64Ptr(retentionUnix),
		}
		resp, err := r.commodore.UpdateArtifactCatalogSnapshot(ctx, snapshot)
		if err != nil {
			r.logger.WithError(err).WithField("artifact_hash", hash).Warn("Failed to project Commodore catalog snapshot")
			// Back this row off so a repeatedly-failing projection can't head-of-line block newer
			// rows every pass; retried once the backoff elapses.
			r.backoffCatalogRow(ctx, hash)
			continue
		}
		if !resp.GetFound() {
			// The catalog row isn't there yet (created out of band / registration lag). Do NOT
			// advance the watermark — back off so it can't monopolize the batch, and retry later.
			r.backoffCatalogRow(ctx, hash)
			continue
		}
		if resp.GetCurrentRevision() < revision {
			// Found, but Commodore's stored revision is behind what we asked for: a concurrent
			// insert landed between the guarded UPDATE and the readback, or the guard rejected a
			// stale attempt. Coverage is NOT proven for this revision — back off and retry.
			r.backoffCatalogRow(ctx, hash)
			continue
		}
		// MIXED-VERSION ACK: when we projected a NON-EMPTY thumbnail serving cluster, the row is COVERED only once
		// Commodore ECHOES it back — proof a new Commodore stored field 21. An old Commodore ignores the unknown field
		// and echoes "", so we do NOT advance; the row stays dirty and re-projects after Commodore upgrades (bounded to
		// thumbnailed rows during the window). Rows with no serving cluster (NULL) need no ack.
		if sent := snapshot.GetThumbnailServingClusterId(); sent != "" && resp.GetThumbnailServingClusterId() != sent {
			r.backoffCatalogRow(ctx, hash)
			continue
		}
		// Confirmed covered: Commodore's stored revision is at least this source revision.
		r.advanceCatalogWatermark(ctx, hash, revision)
		count++
	}
	return count, scanned
}

// advanceCatalogWatermark sets catalog_synced_rev = revision when it hasn't already moved
// past it, and clears any stale quarantine error now that the row is covered. The WHERE guard
// keeps the watermark monotonic and the trigger's watermark-only exemption means this write
// does not re-dirty the row (catalog_quarantine_error is not a snapshot-projected field).
func (r *ArtifactReconciler) advanceCatalogWatermark(ctx context.Context, hash string, revision int64) {
	if err := foghorndb.New(r.db).AdvanceCatalogWatermark(ctx, foghorndb.AdvanceCatalogWatermarkParams{CatalogSyncedRev: revision, ArtifactHash: hash}); err != nil {
		r.logger.WithError(err).WithField("artifact_hash", hash).Warn("Failed to advance catalog projection watermark")
	}
}

// backoffCatalogRow stamps catalog_next_attempt_at with an EXPONENTIAL backoff so the scan skips
// this row until it elapses. The base is the reconcile interval and it doubles per consecutive
// failure (capped at 1h): a fixed backoff shorter than the interval would leave a permanently-
// failing row eligible again before every pass (re-filling the oldest-first batch and starving
// newer rows), so the delay must exceed the cadence and grow. It does NOT advance
// catalog_synced_rev (coverage is unproven). catalog_next_attempt_at / attempts are not
// snapshot-projected fields, so this write does not bump catalog_revision.
func (r *ArtifactReconciler) backoffCatalogRow(ctx context.Context, hash string) {
	baseSecs := int(r.interval.Seconds())
	if baseSecs < 1 {
		baseSecs = 1
	}
	if err := foghorndb.New(r.db).BackoffCatalogProjection(ctx, foghorndb.BackoffCatalogProjectionParams{ArtifactHash: hash, BaseSeconds: float64(baseSecs)}); err != nil {
		r.logger.WithError(err).WithField("artifact_hash", hash).Warn("Failed to set catalog projection backoff")
	}
}

// quarantineCatalogRow records that a row could not be projected because its authoritative
// state is unrepresentable (unknown asset type, malformed tracks). It advances
// catalog_quarantined_rev (NOT catalog_synced_rev) and stores the reason on the Foghorn row,
// so the projection scan skips it — it can't head-of-line block the queue — without falsely
// marking it covered. The quarantine state lives on foghorn.artifacts (operator-visible there,
// not in the Commodore catalog that ListStorageArtifacts reads). A later authoritative
// mutation — including a correction to the very field that caused the quarantine, since
// artifact_type is now in the revision-bump trigger — pushes catalog_revision past the
// quarantine mark and re-enqueues the row.
func (r *ArtifactReconciler) quarantineCatalogRow(ctx context.Context, hash string, revision int64, reason string) {
	r.logger.WithFields(logging.Fields{"artifact_hash": hash, "reason": reason}).
		Warn("Quarantining catalog snapshot projection")
	if err := foghorndb.New(r.db).QuarantineCatalogProjection(ctx, foghorndb.QuarantineCatalogProjectionParams{CatalogQuarantinedRev: revision, ArtifactHash: hash, CatalogQuarantineError: sql.NullString{String: reason, Valid: true}}); err != nil {
		r.logger.WithError(err).WithField("artifact_hash", hash).Warn("Failed to record catalog quarantine")
	}
}

func nullInt64Ptr(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	return &v.Int64
}

func nullStringPtr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	return &v.String
}

func boolPtr(b bool) *bool { return &b }

// strPtrOrNil maps an empty string to a nil optional. A projecting cluster always has a cluster
// id set; a nil/absent source_cluster_id is rejected by Commodore (it is required), so this only
// avoids sending a spurious empty string on the wire.
func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// retryFailed re-sends FreezeRequests for artifacts with sync_status='failed'.
func (r *ArtifactReconciler) retryFailed(ctx context.Context) int {
	// sync_status='failed' is a TRANSIENT failure and is retried INDEFINITELY with a capped,
	// jittered exponential backoff keyed off failure_count — never abandoned by a retry budget
	// (stranding an artifact would force an operator SQL edit to recover). Only a classified
	// PERMANENT failure is terminal: lost_local (source gone before any sync) is a separate
	// sync_status excluded here. The ±20% jitter (0.8 + 0.4*random() per row) spreads a burst of
	// same-age failures so retries don't thunder. DVR rows use the segment ledger and are excluded.
	var rows []foghorndb.ListFailedArtifactsForFreezeRetryRow
	queries := foghorndb.New(r.db)
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var err error
		rows, err = queries.ListFailedArtifactsForFreezeRetry(ctx, int32(r.batchSize))
		return err
	})
	if err != nil {
		r.logger.WithError(err).Warn("Failed to query failed artifacts for retry")
		return 0
	}
	count := 0
	for _, row := range rows {
		dispatched, err := r.sendFreezeForArtifact(ctx, row.ArtifactHash, row.ArtifactType, row.StreamInternalName, row.TenantID, row.Format.String, row.NodeID, row.FilePath.String)
		if err != nil {
			r.logger.WithError(err).WithField("artifact_hash", row.ArtifactHash).Warn("Failed to send freeze retry")
			continue
		}
		if dispatched {
			count++
		}
	}
	return count
}

// advancePending sends FreezeRequests for pending artifacts that have never been synced.
func (r *ArtifactReconciler) advancePending(ctx context.Context) int {
	rows, err := foghorndb.New(r.db).ListPendingArtifactsForFreeze(ctx, int32(r.batchSize))
	if err != nil {
		r.logger.WithError(err).Warn("Failed to query pending artifacts")
		return 0
	}
	count := 0
	for _, row := range rows {
		dispatched, err := r.sendFreezeForArtifact(ctx, row.ArtifactHash, row.ArtifactType, row.StreamInternalName, row.TenantID, row.Format.String, row.NodeID, row.FilePath.String)
		if err != nil {
			r.logger.WithError(err).WithField("artifact_hash", row.ArtifactHash).Warn("Failed to send freeze for pending artifact")
			continue
		}
		if dispatched {
			count++
		}
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
	queries := foghorndb.New(r.db)
	rows, err := queries.ExistingArtifactHashes(ctx, hashes)
	if err != nil {
		r.logger.WithError(err).Warn("Failed to batch-check artifact lifecycle rows")
		return 0
	}
	for _, h := range rows {
		existing[h] = true
	}

	count := 0
	skippedDVROrphans := 0
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
			skippedDVROrphans++
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

		err = database.WithRetryablePostgresTx(ctx, r.db, nil, func(tx *sql.Tx) error {
			txQueries := queries.WithTx(tx)
			insertErr := txQueries.InsertDiscoveredArtifact(ctx, foghorndb.InsertDiscoveredArtifactParams{
				ArtifactHash: c.hash, ArtifactType: c.assetType, StreamInternalName: sql.NullString{String: internalName, Valid: internalName != ""}, TenantID: tenantID, Format: c.format,
			})
			if insertErr != nil {
				return insertErr
			}

			return txQueries.UpsertDiscoveredArtifactNode(ctx, foghorndb.UpsertDiscoveredArtifactNodeParams{ArtifactHash: c.hash, NodeID: c.nodeID, FilePath: sql.NullString{String: c.filePath, Valid: c.filePath != ""}, SizeBytes: sql.NullInt64{Int64: int64(c.sizeBytes), Valid: true}})
		})
		if err != nil {
			continue
		}
		if r.onNodeIndexed != nil {
			// This upsert can restore an artifact_nodes row that was previously LOST;
			// emit a node-copy GAINED so the analytics projection isn't left absent.
			r.onNodeIndexed(ctx, c.hash, c.nodeID)
		}

		r.logger.WithFields(logging.Fields{
			"artifact_hash": c.hash,
			"asset_type":    c.assetType,
			"tenant_id":     tenantID,
			"node_id":       c.nodeID,
		}).Info("Indexed untracked edge artifact")
		count++
	}
	if skippedDVROrphans > 0 {
		r.logger.WithField("artifact_count", skippedDVROrphans).Info("Skipped DVR orphans in generic discovery; ledger reconstruction is sidecar-owned")
	}
	return count
}

func artifactTypeFromProto(t ipcpb.ArtifactEvent_ArtifactType) string {
	switch t {
	case ipcpb.ArtifactEvent_ARTIFACT_TYPE_CLIP:
		return "clip"
	case ipcpb.ArtifactEvent_ARTIFACT_TYPE_DVR:
		return "dvr"
	case ipcpb.ArtifactEvent_ARTIFACT_TYPE_VOD:
		return "vod"
	default:
		return ""
	}
}

// sendFreezeForArtifact assigns and sends a proactive FreezeRequest to the node through the ONE shared
// freeze contract the interactive permission path uses — so a background dispatch applies the SAME tenant
// routing, source+destination authorization, local-backing ownership check, server-minted attempt, staging
// key, and claim. It can no longer store to the wrong backend, skip authorization, or attribute bytes to the
// origin cluster.
func (r *ArtifactReconciler) sendFreezeForArtifact(ctx context.Context, hash, assetType, streamName, tenantID, format, nodeID, filePath string) (dispatched bool, err error) {
	if format == "" {
		format = "mp4"
	}
	if assetType == "clip" && streamName == "" {
		return false, fmt.Errorf("clip %s missing stream_internal_name", hash)
	}
	// Proactive-dispatch protocol pre-check: if the target node is LOCALLY connected and declares a
	// pre-staged-freeze protocol version, do not claim+dispatch (it would neither upload to staging nor echo
	// the attempt id, so the attempt could only ever fail closed and churn). Leave it for a later pass, after
	// the sidecar is upgraded. `known=false` (peer-owned / not connected) does NOT skip — the owning instance's
	// session-bound admission is the authority there.
	if r.nodeFreezeProtocolOK != nil {
		if ok, known := r.nodeFreezeProtocolOK(nodeID); known && !ok {
			r.logger.WithFields(map[string]interface{}{"artifact_hash": hash, "node_id": nodeID}).
				Debug("Proactive freeze skipped: sidecar control-protocol predates staged freeze")
			return false, nil
		}
	}
	expiry := 30 * time.Minute

	assignment, reason, ok := r.prepareFreeze(ctx, assetType, hash, tenantID, streamName, format, "", nodeID, expiry)
	if !ok {
		// Not assigned (unauthorized / official storage remote / not eligible / etc.): NOTHING was dispatched,
		// so the caller must not count this as retried/advanced. Left for a later pass; not a loop error.
		r.logger.WithFields(map[string]interface{}{"artifact_hash": hash, "reason": reason}).Debug("Proactive freeze not assigned")
		return false, nil
	}

	req := &ipcpb.FreezeRequest{
		RequestId:        assignment.AttemptID, // server-minted; the node echoes it at completion
		AssetType:        assetType,
		AssetHash:        hash,
		InternalName:     streamName,
		LocalPath:        filePath,
		PresignedPutUrl:  assignment.StagingURL,
		UrlExpirySeconds: int64(expiry.Seconds()),
	}

	if err := r.sendFreeze(nodeID, req); err != nil {
		// The wire send failed. It is AMBIGUOUS whether the node received the command and uploaded to the
		// attempt-scoped staging key, so revert the row to retryable AND durably enqueue that staging object
		// for deletion — both in ONE transaction so a crash can't revert without scheduling cleanup. The
		// revert is guarded by THIS attempt's (attempt id, node, tenant) so a concurrent completion or newer
		// attempt is never clobbered. When the revert affects 0 rows (already completed/superseded), the
		// staging object is the winner's canonical source and must NOT be enqueued.
		if txErr := database.WithRetryablePostgresTx(ctx, r.db, nil, func(tx *sql.Tx) error {
			n, uErr := foghorndb.New(tx).RevertFailedFreezeDispatch(ctx, foghorndb.RevertFailedFreezeDispatchParams{ArtifactHash: hash, SyncRequestID: sql.NullString{String: assignment.AttemptID, Valid: true}, SyncNodeID: sql.NullString{String: nodeID, Valid: true}, TenantID: tenantID})
			if uErr != nil {
				return uErr
			}
			if n == 0 {
				return nil // did not revert THIS attempt → do not touch staging
			}
			return control.EnqueueStagingCleanupTx(ctx, tx, control.FreezeStagingKey(assignment.CanonicalKey, assignment.AttemptID))
		}); txErr != nil {
			r.logger.WithError(txErr).WithField("artifact_hash", hash).Warn("Failed to revert artifact after freeze send failure")
		}
		return false, err
	}
	return true, nil
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
		streamName := resp.InternalName
		if strings.TrimSpace(resp.GetContentType()) == "chapter" && strings.TrimSpace(resp.GetParentStreamInternalName()) != "" {
			streamName = resp.GetParentStreamInternalName()
		}
		return resp.TenantId, streamName, nil

	default:
		return "", "", fmt.Errorf("cannot resolve asset type: %s", assetType)
	}
}

func artifactAssetTypeFromString(t string) (commodorepb.ArtifactAssetType, bool) {
	switch t {
	case "clip":
		return commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_CLIP, true
	case "dvr", "dvr_segment", "dvr_manifest":
		return commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_DVR, true
	case "vod":
		return commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_VOD, true
	default:
		return commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_UNSPECIFIED, false
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
