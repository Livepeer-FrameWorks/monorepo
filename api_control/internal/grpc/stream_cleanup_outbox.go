package grpc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	foghornclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/outbox"
)

const (
	streamCleanupOutboxBaseBackoff = 2 * time.Second
	streamCleanupOutboxMaxBackoff  = 1 * time.Hour
	streamCleanupOutboxPollPeriod  = 30 * time.Second
	// streamCleanupOutboxLease is how long a claimed row hides from other workers once the claim tx commits.
	streamCleanupOutboxLease = 60 * time.Second
	// streamCleanupOutboxItemTimeout bounds ONE row's dispatch so a slow Foghorn call can't run unbounded.
	streamCleanupOutboxItemTimeout = 10 * time.Second
	// streamCleanupOutboxSettleTimeout bounds a settlement (finalize / record-failure) so a blocked DB op cannot
	// exceed the lease using the long-lived worker context. Counted in the lease budget alongside dispatch.
	streamCleanupOutboxSettleTimeout = 5 * time.Second
	// streamCleanupOutboxBatchSize is BUDGETED against the lease: the worker processes the batch SERIALLY. Per row the
	// thumbnail fan-out AND the child cascade share ONE ItemTimeout-bounded dispatch context (10s), the phase-ack marker
	// writes on its own fresh SettleTimeout context (5s), and the generic worker's settlement is another SettleTimeout
	// (5s) — so worst case per row = ItemTimeout + 2*SettleTimeout = 10s + 5s + 5s = 20s. BatchSize * 20s must stay within
	// the 60s lease: 2 * 20s = 40s, leaving 20s of headroom. TestStreamCleanupOutboxLeaseBudget pins this.
	streamCleanupOutboxBatchSize = 2
	// streamCleanupOutboxAlertAfterAttempts flags a sustained Foghorn outage; the worker keeps retrying (no
	// terminal abandon), but logs at Error so on-call is paged.
	streamCleanupOutboxAlertAfterAttempts = 12
)

func streamCleanupOutboxConfig() outbox.Config {
	return outbox.Config{
		BaseBackoff:        streamCleanupOutboxBaseBackoff,
		MaxBackoff:         streamCleanupOutboxMaxBackoff,
		BatchSize:          streamCleanupOutboxBatchSize,
		PollPeriod:         streamCleanupOutboxPollPeriod,
		Lease:              streamCleanupOutboxLease,
		SettleTimeout:      streamCleanupOutboxSettleTimeout,
		AlertAfterAttempts: streamCleanupOutboxAlertAfterAttempts,
	}
}

type streamCleanupOutboxRow struct {
	streamID              string
	tenantID              string
	attempts              int
	leaseToken            string
	thumbnailCleanupAcked bool // phase-1 (multi-cell thumbnail cleanup) already acked durably → skip it, run only children
}

// enqueueStreamCleanupOutbox writes a pending obligation row. The caller passes the same *sql.Tx it used to
// delete the stream so a failed INSERT rolls back the deletion: no durable obligation, no deletion. Idempotent on
// stream_id (a re-delete inserts nothing rather than erroring).
func (s *CommodoreServer) enqueueStreamCleanupOutbox(ctx context.Context, exec outboxExecutor, streamID, tenantID string) error {
	var dummy sql.NullString
	row := exec.QueryRowContext(ctx, `
		INSERT INTO commodore.stream_cleanup_outbox (stream_id, tenant_id)
		VALUES ($1::uuid, $2::uuid)
		ON CONFLICT (stream_id) DO NOTHING
		RETURNING stream_id
	`, streamID, tenantID)
	// ON CONFLICT DO NOTHING returns no row on conflict → sql.ErrNoRows, which is expected (idempotent no-op).
	if scanErr := row.Scan(&dummy); scanErr != nil && scanErr != sql.ErrNoRows {
		return fmt.Errorf("insert stream cleanup outbox row: %w", scanErr)
	}
	return nil
}

// recordStreamCleanupOutboxFailure bumps attempts + schedules the next retry with capped exponential backoff.
// No terminal abandon state: a partitioned/unreachable Foghorn catches up when it recovers. Past the alert
// threshold the row is logged at Error so on-call alerting fires; the row stays pending and the worker retries.
// RETURNS the DB error so the worker adapter propagates it (RetryPostgres retries the settlement write).
func (s *CommodoreServer) recordStreamCleanupOutboxFailure(ctx context.Context, streamID, tenantID string, currentAttempts int, cause error, leaseToken string) error {
	if streamID == "" {
		return nil
	}
	// FAIL rather than settle tenantlessly: the tenant is carried in the opaque claim identity (and is NOT NULL on the
	// row), so an empty tenant here is a decode/programming fault. Returning an error leaves the row pending and lets
	// lease expiry + retry recover it — never a tenant-unscoped write.
	if tenantID == "" {
		return fmt.Errorf("record stream cleanup outbox failure for %s: missing tenant in claim identity", streamID)
	}
	nextAttempts := currentAttempts + 1
	last := ""
	if cause != nil {
		last = cause.Error()
	}
	backoff := outbox.ComputeBackoff(streamCleanupOutboxConfig(), currentAttempts)
	// Token-fenced ($5='' is the un-leased path): a stale worker whose lease was re-claimed cannot reschedule a row
	// a peer owns. Tenant-fenced UNCONDITIONALLY ($6) per the repository-wide tenancy rule — no empty-tenant fallback.
	if _, err := s.db.ExecContext(ctx, `
		UPDATE commodore.stream_cleanup_outbox
		SET attempts = $1,
		    next_attempt_at = NOW() + ($2::bigint * INTERVAL '1 millisecond'),
		    last_error = $3
		WHERE stream_id = $4::uuid AND status = 'pending' AND ($5 = '' OR lease_token = $5)
		  AND tenant_id = $6::uuid
	`, nextAttempts, backoff.Milliseconds(), last, streamID, leaseToken, tenantID); err != nil {
		return fmt.Errorf("record stream cleanup outbox failure: %w", err)
	}
	if nextAttempts >= streamCleanupOutboxAlertAfterAttempts {
		s.logger.WithFields(logging.Fields{
			"stream_id":  streamID,
			"attempts":   nextAttempts,
			"backoff_ms": backoff.Milliseconds(),
			"cause":      last,
		}).Error("Stream thumbnail cleanup has been failing for many attempts; Foghorn likely unreachable. Worker will keep retrying — investigate.")
	}
	return nil
}

// streamCleanupClaimID encodes the OPAQUE claim identity the generic outbox worker round-trips to settlement. The
// worker hands the settle methods only (id, leaseToken), so the tenant travels IN the id — as `tenant_id|stream_id` —
// rather than in mutable side-state. Both are UUIDs, so `|` is an unambiguous separator. This makes every settlement
// tenant-scoped from the claim itself, with no map to miss and no tenantless fallback.
func streamCleanupClaimID(tenantID, streamID string) string {
	return tenantID + "|" + streamID
}

// parseStreamCleanupClaimID splits the opaque claim id back into (tenantID, streamID). A malformed id (no separator)
// yields an empty tenant, which the settle helpers reject — failing the settlement rather than writing tenantlessly.
func parseStreamCleanupClaimID(id string) (tenantID, streamID string) {
	if sep := strings.IndexByte(id, '|'); sep >= 0 {
		return id[:sep], id[sep+1:]
	}
	return "", id
}

// streamCleanupOutboxStore adapts the per-table SQL to the generic outbox.Store contract.
type streamCleanupOutboxStore struct{ server *CommodoreServer }

func (st *streamCleanupOutboxStore) ClaimBatch(ctx context.Context, _ int, _ time.Duration) ([]outbox.Claim[streamCleanupOutboxRow], error) {
	rows, err := st.server.claimStreamCleanupOutboxBatch(ctx)
	if err != nil {
		return nil, err
	}
	claims := make([]outbox.Claim[streamCleanupOutboxRow], 0, len(rows))
	for _, r := range rows {
		// ID carries BOTH tenant and stream so settlement is tenant-scoped straight from the claim.
		claims = append(claims, outbox.Claim[streamCleanupOutboxRow]{ID: streamCleanupClaimID(r.tenantID, r.streamID), Attempts: r.attempts, Payload: r, LeaseToken: r.leaseToken})
	}
	return claims, nil
}

func (st *streamCleanupOutboxStore) MarkCompleted(ctx context.Context, id string) error {
	return st.MarkCompletedToken(ctx, id, "")
}

func (st *streamCleanupOutboxStore) RecordFailure(ctx context.Context, id string, currentAttempts int, _ []string, cause error, _ time.Duration) error {
	return st.RecordFailureToken(ctx, id, currentAttempts, nil, cause, 0, "")
}

// MarkCompletedToken / RecordFailureToken (outbox.TokenFencedStore): settlement CAS-checks the claim's lease token so
// a stale worker (its lease lapsed, row re-claimed by a peer with a new token) cannot settle a row it no longer owns.
// Both also tenant-fence on the tenant decoded from the opaque claim id per the repository-wide tenancy rule.
func (st *streamCleanupOutboxStore) MarkCompletedToken(ctx context.Context, id, leaseToken string) error {
	// Foghorn acked the FULL cascade → FINALIZE the two-phase deletion (hard-delete the soft-deleted row + mark the
	// outbox completed), token-fenced. The whole settlement is bounded by Config.SettleTimeout in the generic worker.
	tenantID, streamID := parseStreamCleanupClaimID(id)
	return st.server.finalizeStreamDeletion(ctx, streamID, tenantID, leaseToken)
}

func (st *streamCleanupOutboxStore) RecordFailureToken(ctx context.Context, id string, currentAttempts int, _ []string, cause error, _ time.Duration, leaseToken string) error {
	tenantID, streamID := parseStreamCleanupClaimID(id)
	return st.server.recordStreamCleanupOutboxFailure(ctx, streamID, tenantID, currentAttempts, cause, leaseToken)
}

// streamCleanupOutboxDispatcher adapts the Foghorn DeleteStreamThumbnails call to outbox.Dispatcher.
type streamCleanupOutboxDispatcher struct{ server *CommodoreServer }

func (d *streamCleanupOutboxDispatcher) Dispatch(ctx context.Context, row streamCleanupOutboxRow) ([]string, error) {
	return d.server.dispatchStreamCleanupOutboxRow(ctx, row)
}

// dispatchStreamCleanupOutboxRow delivers one obligation to the tenant's Foghorn. A transport error or a
// Success=false response returns the row's own id as a failed target (worker records the failure + retries); a
// positive ack returns no failures (worker marks completed). Foghorn's handler is idempotent, so a redelivery
// after a lost ack is a durable no-op.
func (s *CommodoreServer) dispatchStreamCleanupOutboxRow(ctx context.Context, row streamCleanupOutboxRow) ([]string, error) {
	// Bound the Foghorn RPC work (thumbnail fan-out + child cascade) to one per-item deadline shorter than the lease,
	// so a slow call cannot let the lease lapse on the batch tail.
	rpcCtx, cancel := context.WithTimeout(ctx, streamCleanupOutboxItemTimeout)
	defer cancel()
	// PHASE 1 — multi-cell thumbnail cleanup, run only once. On success we durably stamp the phase-ack marker so every
	// later retry SKIPS this phase and hands the whole budget to the child cascade. deleteStreamThumbnails is idempotent,
	// so re-running it (e.g. after a genuinely lost marker) is a durable no-op.
	if !row.thumbnailCleanupAcked {
		if tErr := s.deleteStreamThumbnails(rpcCtx, row.streamID, row.tenantID); tErr != nil {
			return []string{row.streamID}, tErr
		}
		// Persist the phase transition on a FRESH, INDEPENDENT context — NOT rpcCtx, which a slow fan-out may have
		// already exhausted. The marker is a single fast UPDATE, so this adds negligible real time; without it a
		// 9.9s fan-out would leave the marker on an expired context, drop it, and re-run the slow phase every retry,
		// starving child cleanup — the bug the marker exists to prevent.
		mctx, mcancel := context.WithTimeout(context.Background(), streamCleanupOutboxSettleTimeout)
		mErr := s.markStreamThumbnailCleanupAcked(mctx, row.streamID, row.tenantID)
		mcancel()
		if mErr != nil {
			return []string{row.streamID}, mErr
		}
	}
	// PHASE 2 — durable CHILD-MEDIA cascade: the stream's clips + DVR recordings are cascade-owned, so they must be gone
	// before the stream is finalized. A failure returns the row as a failed target and the whole obligation retries; the
	// DeleteStream RPC keeps returning deletion_pending until it converges. Every child delete is idempotent, and
	// already-deleted children no longer enumerate — so many children converge incrementally over retries within budget.
	if cErr := s.deleteStreamChildMedia(rpcCtx, row.streamID, row.tenantID); cErr != nil {
		return []string{row.streamID}, cErr
	}
	return nil, nil
}

// markStreamThumbnailCleanupAcked durably records that Foghorn ACKED the thumbnail-cleanup obligation for every owning
// cell (the tombstone is held; not a proof the S3 bytes are already gone), so later retries skip the phase. Monotonic +
// idempotent (stamps once); not lease-fenced because the fact is permanent and a stale worker marking a genuinely-acked
// obligation is harmless.
func (s *CommodoreServer) markStreamThumbnailCleanupAcked(ctx context.Context, streamID, tenantID string) error {
	// Tenant-fenced UNCONDITIONALLY per the repository-wide tenancy rule. tenantID comes from the claimed row
	// (NOT NULL), so an empty value is a fault: fail so the phase re-runs on retry rather than marking tenantlessly.
	if tenantID == "" {
		return fmt.Errorf("mark thumbnail cleanup acked for %s: missing tenant in claim identity", streamID)
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE commodore.stream_cleanup_outbox SET thumbnail_cleanup_acked_at = NOW() WHERE stream_id = $1::uuid AND thumbnail_cleanup_acked_at IS NULL AND tenant_id = $2::uuid`, streamID, tenantID)
	return err
}

// deleteStreamChildMedia idempotently deletes the stream's cascade-owned child media — clips and DVR recordings —
// through their origin-cluster Foghorn (the same idempotent RPCs a single delete uses; the Foghorn artifact
// reconciler projects the catalog-row removal). VOD assets are NOT stream-owned and are deliberately left untouched.
// Returns an error on the first failed child so the durable obligation retries; a re-run after partial progress is a
// no-op because each Foghorn delete is idempotent and already-projected children no longer enumerate.
func (s *CommodoreServer) deleteStreamChildMedia(ctx context.Context, streamID, tenantID string) error {
	type child struct{ hash, cluster string }
	collect := func(query string) ([]child, error) {
		rows, err := s.db.QueryContext(ctx, query, streamID, tenantID)
		if err != nil {
			return nil, err
		}
		defer rows.Close() //nolint:errcheck
		var out []child
		for rows.Next() {
			var c child
			if sErr := rows.Scan(&c.hash, &c.cluster); sErr != nil {
				return nil, sErr
			}
			out = append(out, c)
		}
		return out, rows.Err()
	}

	clips, err := collect(`SELECT clip_hash, COALESCE(origin_cluster_id::text, '') FROM commodore.clips WHERE stream_id = $1::uuid AND tenant_id = $2::uuid`)
	if err != nil {
		return fmt.Errorf("list stream clips: %w", err)
	}
	for _, c := range clips {
		if cErr := s.deleteOneChildArtifact(ctx, "clip", c.hash, c.cluster, tenantID); cErr != nil {
			return cErr
		}
	}

	dvrs, err := collect(`SELECT dvr_hash, COALESCE(origin_cluster_id::text, '') FROM commodore.dvr_recordings WHERE stream_id = $1::uuid AND tenant_id = $2::uuid`)
	if err != nil {
		return fmt.Errorf("list stream dvr recordings: %w", err)
	}
	for _, d := range dvrs {
		if cErr := s.deleteOneChildArtifact(ctx, "dvr", d.hash, d.cluster, tenantID); cErr != nil {
			return cErr
		}
	}
	return nil
}

// deleteStreamThumbnails delivers the parent stream's thumbnail-cleanup RPC to EVERY cell that ever served the
// stream's live thumbnails. Live thumbnails are minted on the ingest cell's own per-cell Foghorn database, so the
// obligation must reach each owning cell (a tenant-primary-only dispatch would record a tombstone + sweep an empty
// prefix on the wrong cell and falsely ack, leaking the real objects). The durable serving-cell set
// (commodore.streams.thumbnail_serving_cluster_ids, whose sole writer is the service-fenced register-before-mint) is
// the source of truth. It returns an error if ANY cell fails to ack, so the WHOLE obligation retries — Foghorn's
// handler is idempotent, so redelivering to an already-cleaned cell is a durable no-op, and finalization happens only
// once every cell has acked.
func (s *CommodoreServer) deleteStreamThumbnails(ctx context.Context, streamID, tenantID string) error {
	cells, err := s.streamThumbnailServingCells(ctx, streamID, tenantID)
	if err != nil {
		return fmt.Errorf("resolve stream serving cells: %w", err)
	}
	if len(cells) == 0 {
		// No registered owner → nothing to clean. thumbnail_serving_cluster_ids has a SINGLE writer
		// (RegisterStreamThumbnailServingCell, which a cell performs when it first serves a live thumbnail for the stream
		// and caches thereafter), so an empty set means no thumbnail was ever minted for this stream (e.g. created and
		// deleted without going live). There is NO tenant-primary or active-ingest fallback — either would dispatch to a
		// cell that does not own the bytes and falsely ack.
		return nil
	}
	// FAN OUT to every owning cell CONCURRENTLY under the shared deadline and AGGREGATE failures. Concurrency (not a
	// serial loop) is required for isolation: a cell whose RPC HANGS to the item deadline must not consume the budget
	// of the cells after it — under a serial loop a stable-ordered first target that blocks would leave every later
	// cell invoked with an already-cancelled context, starving them forever across retries. Each cell dispatches in its
	// own goroutine; any failure returns an aggregate error so the whole obligation retries (idempotent redelivery
	// re-hits already-cleaned cells as no-ops), finalizing only once every cell has acked.
	errs := make([]error, len(cells))
	var wg sync.WaitGroup
	for i, cell := range cells {
		wg.Add(1)
		go func(i int, cell string) {
			defer wg.Done()
			errs[i] = s.dispatchStreamThumbnailDelete(ctx, streamID, tenantID, cell)
		}(i, cell)
	}
	wg.Wait()
	return errors.Join(errs...)
}

// streamThumbnailServingCells returns the deduped set of cells that own this stream's live thumbnails — the durable
// thumbnail_serving_cluster_ids set ONLY. That set is the single authoritative record (its sole writer,
// RegisterStreamThumbnailServingCell, records a cell on its first/cache-miss mint for the stream), so
// active_ingest_cluster_id — mutable, unfenced placement state — is deliberately NOT unioned in. The soft-deleted
// stream row still exists (hard delete happens only
// after cleanup), so this reads it directly. Filtered by tenant_id per the repo tenant-isolation rule. An unknown
// stream returns an empty set, which the caller treats as "no registered owner" (nothing to clean).
func (s *CommodoreServer) streamThumbnailServingCells(ctx context.Context, streamID, tenantID string) ([]string, error) {
	var recorded []string
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(thumbnail_serving_cluster_ids, '{}')
		  FROM commodore.streams WHERE id = $1::uuid AND tenant_id = $2::uuid`, streamID, tenantID).Scan(database.ArrayScan(&recorded))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(recorded))
	for _, c := range recorded {
		if c = strings.TrimSpace(c); c != "" && !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out, nil
}

// dispatchStreamThumbnailDelete delivers the cleanup RPC to ONE recorded owning cell. The owning cell is resolved via
// Quartermaster SERVICE DISCOVERY (not the tenant route), so a durably-recorded owner stays reachable even if the
// tenant lost entitlement to that cluster since ingest. A transport error or non-ack is returned so the obligation
// retries; the handler is idempotent.
func (s *CommodoreServer) dispatchStreamThumbnailDelete(ctx context.Context, streamID, tenantID, clusterID string) error {
	if s.streamThumbnailDeleteFn != nil {
		return s.streamThumbnailDeleteFn(ctx, streamID, tenantID, clusterID)
	}
	client, err := s.resolveFoghornForClusterDirect(ctx, clusterID)
	if err != nil {
		return fmt.Errorf("resolve foghorn (cluster %q): %w", clusterID, err)
	}
	resp, delErr := client.DeleteStreamThumbnails(ctx, streamID, tenantID)
	if delErr != nil {
		return fmt.Errorf("delete stream thumbnails (cluster %q): %w", clusterID, delErr)
	}
	if resp == nil || !resp.GetSuccess() {
		msg := ""
		if resp != nil {
			msg = resp.GetMessage()
		}
		return fmt.Errorf("foghorn (cluster %q) did not ack stream thumbnail cleanup: %s", clusterID, msg)
	}
	return nil
}

// deleteOneChildArtifact idempotently deletes one cascade-owned child (clip/dvr) through its origin-cluster Foghorn.
// The childArtifactDeleteFn test seam, when set, replaces the live Foghorn call so the fail→retry→ack coordination
// can be exercised deterministically; production leaves it nil.
func (s *CommodoreServer) deleteOneChildArtifact(ctx context.Context, kind, hash, cluster, tenantID string) error {
	if s.childArtifactDeleteFn != nil {
		return s.childArtifactDeleteFn(ctx, kind, hash, cluster, tenantID)
	}
	// A RECORDED origin cluster must stay reachable for cleanup even if the tenant lost entitlement to it since the
	// artifact was created — resolve it via QM service discovery, NOT the tenant route (same as thumbnail cleanup). An
	// empty origin (legacy child with no recorded cluster) falls back to the tenant primary.
	var fc *foghornclient.GRPCClient
	var rErr error
	if c := strings.TrimSpace(cluster); c != "" {
		fc, rErr = s.resolveFoghornForClusterDirect(ctx, c)
	} else {
		fc, rErr = s.resolveFoghornForArtifact(ctx, tenantID, cluster)
	}
	if rErr != nil {
		return fmt.Errorf("resolve foghorn for %s %s: %w", kind, hash, rErr)
	}
	switch kind {
	case "clip":
		resp, _, dErr := fc.DeleteClip(ctx, hash, &tenantID)
		return childDeleteAcked("clip", hash, resp, dErr)
	case "dvr":
		resp, _, dErr := fc.DeleteDVR(ctx, hash, &tenantID)
		return childDeleteAcked("dvr", hash, resp, dErr)
	default:
		return fmt.Errorf("unknown child artifact kind %q", kind)
	}
}

// childArtifactDeleteResponse is the common shape of DeleteClip/DeleteDVR responses.
type childArtifactDeleteResponse interface {
	GetSuccess() bool
	GetMessage() string
}

// childDeleteAcked normalizes a child delete into a durable ack decision. It is IDEMPOTENT-TOLERANT: an
// already-deleted child (a retry after partial progress — the catalog row is projected away asynchronously, so the
// same child can re-enumerate) is treated as DONE, not a failure that would loop the obligation forever. Foghorn
// returns NotFound for a fully-gone artifact and Success=false "already deleted" for one already soft-deleted; both
// are benign. Any other error / non-success is a real failure that fails the obligation so it retries.
func childDeleteAcked[T childArtifactDeleteResponse](kind, hash string, resp T, err error) error {
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil // already gone
		}
		return fmt.Errorf("delete %s %s: %w", kind, hash, err)
	}
	if resp.GetSuccess() {
		return nil
	}
	if strings.Contains(strings.ToLower(resp.GetMessage()), "already deleted") {
		return nil // benign idempotent no-op on a retry
	}
	return fmt.Errorf("foghorn did not ack %s %s deletion: %s", kind, hash, resp.GetMessage())
}

// runStreamCleanupOutboxWorker polls for due obligations and delivers them. SKIP LOCKED + lease-window UPDATE
// make it safe on every Commodore replica without leader election. AlertAfterAttempts is zeroed on the worker
// config so the alert is logged once, from RecordFailure, not twice.
func (s *CommodoreServer) runStreamCleanupOutboxWorker(ctx context.Context) {
	if s.foghornPool == nil {
		s.logger.Info("stream cleanup outbox worker disabled: no foghorn pool")
		return
	}
	cfg := streamCleanupOutboxConfig()
	cfg.AlertAfterAttempts = 0
	worker := &outbox.Worker[streamCleanupOutboxRow]{
		Config:     cfg,
		Store:      &streamCleanupOutboxStore{server: s},
		Dispatcher: &streamCleanupOutboxDispatcher{server: s},
		Logger:     s.logger,
		AlertLabel: "stream thumbnail cleanup",
	}
	worker.Run(ctx)
}

// claimStreamCleanupOutboxBatch selects due pending rows, then in the SAME transaction leases them by pushing
// next_attempt_at into the future so other workers skip them. SKIP LOCKED guards in-flight collisions; the lease
// guards post-commit races between replicas.
func (s *CommodoreServer) claimStreamCleanupOutboxBatch(ctx context.Context) ([]streamCleanupOutboxRow, error) {
	var out []streamCleanupOutboxRow
	err := database.WithRetryablePostgresTxWithHook(ctx, s.db, nil, func(error, int) {
		s.recycleIdlePostgresConns()
	}, func(tx *sql.Tx) error {
		rows, qerr := tx.QueryContext(ctx, `
			SELECT stream_id::text, tenant_id::text, attempts, thumbnail_cleanup_acked_at IS NOT NULL
			FROM commodore.stream_cleanup_outbox
			WHERE status = 'pending' AND next_attempt_at <= NOW()
			ORDER BY next_attempt_at
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		`, streamCleanupOutboxBatchSize)
		if qerr != nil {
			return qerr
		}
		defer rows.Close()

		batch := make([]streamCleanupOutboxRow, 0, streamCleanupOutboxBatchSize)
		for rows.Next() {
			var r streamCleanupOutboxRow
			if scanErr := rows.Scan(&r.streamID, &r.tenantID, &r.attempts, &r.thumbnailCleanupAcked); scanErr != nil {
				return scanErr
			}
			batch = append(batch, r)
		}
		if rowsErr := rows.Err(); rowsErr != nil {
			return rowsErr
		}
		for i := range batch {
			// Stamp a FRESH lease token alongside the schedule push; every settlement CAS-checks it so a stale
			// worker (its lease lapsed, row re-claimed by a peer with a new token) cannot settle a row it lost.
			// Tenant-fenced ($3) like every other outbox mutation — ownership acquisition follows the same
			// mandatory tenant boundary as failure/ack/finalize (tenant is the row's own, just SELECTed above).
			if tErr := tx.QueryRowContext(ctx, `
				UPDATE commodore.stream_cleanup_outbox
				SET next_attempt_at = NOW() + ($1::bigint * INTERVAL '1 millisecond'),
				    lease_token = gen_random_uuid()::text
				WHERE stream_id = $2::uuid AND status = 'pending' AND tenant_id = $3::uuid
				RETURNING lease_token
			`, streamCleanupOutboxLease.Milliseconds(), batch[i].streamID, batch[i].tenantID).Scan(&batch[i].leaseToken); tErr != nil {
				return fmt.Errorf("lease outbox row %s: %w", batch[i].streamID, tErr)
			}
		}
		out = batch
		return nil
	})
	return out, err
}
