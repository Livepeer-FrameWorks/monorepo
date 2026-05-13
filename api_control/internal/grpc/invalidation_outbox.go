package grpc

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/outbox"
)

const (
	invalidationOutboxBaseBackoff = 2 * time.Second
	invalidationOutboxMaxBackoff  = 1 * time.Hour
	invalidationOutboxBatchSize   = 16
	invalidationOutboxPollPeriod  = 30 * time.Second
	// invalidationOutboxLease is how long a claimed row hides from other workers
	// once the claim transaction commits. Must comfortably exceed one dispatch
	// pass (Quartermaster lookup + per-cluster Foghorn calls).
	invalidationOutboxLease = 60 * time.Second
	// invalidationOutboxAlertAfterAttempts is the threshold past which a row
	// indicates a sustained outage (e.g. a partitioned cluster). The worker
	// keeps retrying — there is no terminal abandon state — but the threshold
	// drives an Error log line + counter so on-call gets paged. Tuned so the
	// first alert fires roughly when backoff hits the max-backoff plateau.
	invalidationOutboxAlertAfterAttempts = 12
)

func invalidationOutboxConfig() outbox.Config {
	return outbox.Config{
		BaseBackoff:        invalidationOutboxBaseBackoff,
		MaxBackoff:         invalidationOutboxMaxBackoff,
		BatchSize:          invalidationOutboxBatchSize,
		PollPeriod:         invalidationOutboxPollPeriod,
		Lease:              invalidationOutboxLease,
		AlertAfterAttempts: invalidationOutboxAlertAfterAttempts,
	}
}

// outboxExecutor is the subset of *sql.Tx / *sql.DB this package needs for
// enqueue. Lets RevokeSigningKey / SetPlaybackPolicy enqueue inside their own
// transaction so the mutation rolls back if the outbox INSERT fails.
type outboxExecutor interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type invalidationOutboxRow struct {
	id               string
	tenantID         string
	reason           string
	internalNames    []string
	attempts         int
	streamID         string
	bundleMinVersion int64
}

// enqueueInvalidationOutbox writes a pending mutation row. The caller passes
// the same *sql.Tx it used for the underlying UPDATE so that a failed INSERT
// rolls back the mutation: no durability, no mutation.
func (s *CommodoreServer) enqueueInvalidationOutbox(
	ctx context.Context,
	exec outboxExecutor,
	tenantID, reason string,
	internalNames []string,
) (string, error) {
	if internalNames == nil {
		internalNames = []string{}
	}
	namesJSON, err := json.Marshal(internalNames)
	if err != nil {
		return "", fmt.Errorf("marshal internal_names: %w", err)
	}
	var id string
	row := exec.QueryRowContext(ctx, `
		INSERT INTO commodore.playback_policy_invalidation_outbox
			(tenant_id, reason, internal_names)
		VALUES ($1::uuid, $2, $3::jsonb)
		RETURNING id
	`, tenantID, reason, namesJSON)
	if scanErr := row.Scan(&id); scanErr != nil {
		return "", fmt.Errorf("insert outbox row: %w", scanErr)
	}
	return id, nil
}

// markInvalidationOutboxCompleted is called after every cluster acknowledges
// the invalidation (NodesFailed == 0 from each Foghorn).
func (s *CommodoreServer) markInvalidationOutboxCompleted(ctx context.Context, id string) {
	if id == "" {
		return
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE commodore.playback_policy_invalidation_outbox
		SET status = 'completed', completed_at = NOW(), last_error = NULL,
		    last_failed_clusters = NULL
		WHERE id = $1 AND status = 'pending'
	`, id); err != nil {
		s.logger.WithError(err).WithField("outbox_id", id).Warn("mark invalidation outbox completed failed")
	}
}

// recordInvalidationOutboxFailure bumps attempts, schedules the next retry
// with exponential backoff capped at invalidationOutboxMaxBackoff. There is
// no terminal abandon state: a partitioned cluster catches up the moment it
// becomes reachable. Past invalidationOutboxAlertAfterAttempts the row is
// logged at Error so on-call alerting fires; the row stays pending and the
// worker keeps retrying.
//
// The WHERE clause filters on status='pending' so a competing worker that
// already marked the row completed is not overwritten — defence-in-depth for
// the lease-window worker model.
func (s *CommodoreServer) recordInvalidationOutboxFailure(
	ctx context.Context,
	id string,
	currentAttempts int,
	failedClusters []string,
	cause error,
) {
	if id == "" {
		return
	}
	nextAttempts := currentAttempts + 1
	last := ""
	if cause != nil {
		last = cause.Error()
	}
	failedJSON, mErr := json.Marshal(failedClusters)
	if mErr != nil {
		failedJSON = []byte("null")
	}

	backoff := outbox.ComputeBackoff(invalidationOutboxConfig(), currentAttempts)
	if _, err := s.db.ExecContext(ctx, `
		UPDATE commodore.playback_policy_invalidation_outbox
		SET attempts = $1,
		    next_attempt_at = NOW() + ($2::bigint * INTERVAL '1 millisecond'),
		    last_error = $3,
		    last_failed_clusters = $4::jsonb
		WHERE id = $5 AND status = 'pending'
	`, nextAttempts, backoff.Milliseconds(), last, string(failedJSON), id); err != nil {
		s.logger.WithError(err).WithField("outbox_id", id).Warn("record invalidation outbox failure failed")
		return
	}

	if nextAttempts >= invalidationOutboxAlertAfterAttempts {
		s.logger.WithFields(logging.Fields{
			"outbox_id":       id,
			"attempts":        nextAttempts,
			"failed_clusters": failedClusters,
			"backoff_ms":      backoff.Milliseconds(),
			"cause":           last,
		}).Error("Playback-policy invalidation has been failing for many attempts; cluster likely partitioned. Worker will keep retrying — investigate.")
	}
}

// invalidationOutboxStore adapts CommodoreServer's existing per-table SQL to
// the generic outbox.Store contract. Delegating rather than reimplementing
// keeps the SQL pattern (and the existing tests that assert on it) as a
// single source of truth.
type invalidationOutboxStore struct {
	server *CommodoreServer
}

// dispatchKey passes the per-row routing data (stream_id, bundle version)
// alongside the payload so the Dispatcher can forward bundle_revoke fields
// to Foghorn.
func (st *invalidationOutboxStore) ClaimBatch(ctx context.Context, _ int, _ time.Duration) ([]outbox.Claim[invalidationOutboxRow], error) {
	rows, err := st.server.claimInvalidationOutboxBatch(ctx)
	if err != nil {
		return nil, err
	}
	claims := make([]outbox.Claim[invalidationOutboxRow], 0, len(rows))
	for _, r := range rows {
		claims = append(claims, outbox.Claim[invalidationOutboxRow]{
			ID:       r.id,
			Attempts: r.attempts,
			Payload:  r,
		})
	}
	return claims, nil
}

func (st *invalidationOutboxStore) MarkCompleted(ctx context.Context, id string) error {
	st.server.markInvalidationOutboxCompleted(ctx, id)
	return nil
}

func (st *invalidationOutboxStore) RecordFailure(ctx context.Context, id string, currentAttempts int, failedTargets []string, cause error, _ time.Duration) error {
	// The server method recomputes backoff via the same outbox.ComputeBackoff
	// call so the SQL pattern stays identical to the existing test
	// expectations. The worker-supplied backoff is intentionally unused here.
	st.server.recordInvalidationOutboxFailure(ctx, id, currentAttempts, failedTargets, cause)
	return nil
}

// invalidationOutboxDispatcher adapts the route-lookup + Foghorn fanout to
// outbox.Dispatcher. Worker calls this for both poll-loop and TryDispatch.
type invalidationOutboxDispatcher struct {
	server *CommodoreServer
}

func (d *invalidationOutboxDispatcher) Dispatch(ctx context.Context, row invalidationOutboxRow) ([]string, error) {
	return d.server.dispatchInvalidationOutboxRow(ctx, row)
}

// runInvalidationOutboxWorker polls for due rows and replays them. SKIP LOCKED
// + lease-window UPDATE makes this safe to run on every Commodore replica
// without leader election.
//
// AlertAfterAttempts is zeroed on the worker config so the worker does not
// double-log the on-call alert; recordInvalidationOutboxFailure already
// fires it from within Store.RecordFailure.
func (s *CommodoreServer) runInvalidationOutboxWorker(ctx context.Context) {
	if s.foghornPool == nil {
		s.logger.Info("invalidation outbox worker disabled: no foghorn pool")
		return
	}
	cfg := invalidationOutboxConfig()
	cfg.AlertAfterAttempts = 0
	worker := &outbox.Worker[invalidationOutboxRow]{
		Config:     cfg,
		Store:      &invalidationOutboxStore{server: s},
		Dispatcher: &invalidationOutboxDispatcher{server: s},
		Logger:     s.logger,
		AlertLabel: "playback-policy invalidation",
	}
	worker.Run(ctx)
}

// claimInvalidationOutboxBatch selects pending rows, then in the SAME
// transaction bumps next_attempt_at by the lease window so other workers
// running the predicate `next_attempt_at <= NOW()` skip them. SKIP LOCKED
// guards against in-flight collisions; the lease window guards against
// post-commit races between replicas.
func (s *CommodoreServer) claimInvalidationOutboxBatch(ctx context.Context) ([]invalidationOutboxRow, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort after Commit

	out, err := func() ([]invalidationOutboxRow, error) {
		rows, qerr := tx.QueryContext(ctx, `
			SELECT id, tenant_id, reason, internal_names, attempts,
			       COALESCE(stream_id::text, ''), COALESCE(bundle_min_version, 0)
			FROM commodore.playback_policy_invalidation_outbox
			WHERE status = 'pending' AND next_attempt_at <= NOW()
			ORDER BY next_attempt_at
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		`, invalidationOutboxBatchSize)
		if qerr != nil {
			return nil, qerr
		}
		defer rows.Close()

		batch := make([]invalidationOutboxRow, 0, invalidationOutboxBatchSize)
		for rows.Next() {
			var (
				r        invalidationOutboxRow
				rawNames []byte
			)
			if scanErr := rows.Scan(&r.id, &r.tenantID, &r.reason, &rawNames, &r.attempts, &r.streamID, &r.bundleMinVersion); scanErr != nil {
				return nil, scanErr
			}
			if len(rawNames) > 0 {
				if uErr := json.Unmarshal(rawNames, &r.internalNames); uErr != nil {
					return nil, uErr
				}
			}
			batch = append(batch, r)
		}
		return batch, rows.Err()
	}()
	if err != nil {
		return nil, err
	}

	// Lease the claimed rows by pushing next_attempt_at into the future. If
	// dispatch crashes or the worker dies, the lease expires and another
	// replica picks up the row.
	for _, r := range out {
		if _, lErr := tx.ExecContext(ctx, `
			UPDATE commodore.playback_policy_invalidation_outbox
			SET next_attempt_at = NOW() + ($1::bigint * INTERVAL '1 millisecond')
			WHERE id = $2 AND status = 'pending'
		`, invalidationOutboxLease.Milliseconds(), r.id); lErr != nil {
			return nil, fmt.Errorf("lease outbox row %s: %w", r.id, lErr)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

// tryDispatchInvalidationOutbox is the post-commit synchronous attempt that
// callers run right after enqueue. On full success it marks the row completed
// so the worker has nothing to retry; partial/total failure leaves the row
// pending for the worker. Best-effort: this never fails the calling RPC since
// durability already lives in the outbox row.
func (s *CommodoreServer) tryDispatchInvalidationOutbox(
	ctx context.Context,
	outboxID, tenantID, reason string,
	internalNames []string,
) {
	if outboxID == "" {
		return
	}
	failed, err := s.dispatchInvalidationOutbox(ctx, tenantID, reason, internalNames)
	if err == nil && len(failed) == 0 {
		s.markInvalidationOutboxCompleted(ctx, outboxID)
		return
	}
	s.recordInvalidationOutboxFailure(ctx, outboxID, 0, failed, err)
}

// dispatchInvalidationOutboxRow forwards a claimed outbox row to Foghorn.
// Per-stream invalidations and bundle_revoke entries share the path —
// bundle_revoke carries stream_id + bundle_min_version through to Foghorn
// so the cache watermark bump rides in-band with session invalidation.
func (s *CommodoreServer) dispatchInvalidationOutboxRow(ctx context.Context, row invalidationOutboxRow) ([]string, error) {
	route, err := s.resolveClusterRouteForTenant(ctx, row.tenantID)
	if err != nil {
		return nil, fmt.Errorf("cluster route lookup: %w", err)
	}
	targets := buildClusterFanoutTargets(route)
	if len(targets) == 0 {
		return nil, nil
	}

	var failed []string
	for _, target := range targets {
		client, dialErr := s.foghornPool.GetOrCreate(foghornPoolKey(target.clusterID, target.addr), target.addr)
		if dialErr != nil {
			s.logger.WithError(dialErr).WithFields(logging.Fields{
				"tenant_id":  row.tenantID,
				"cluster_id": target.clusterID,
			}).Warn("invalidation dispatch: dial failed")
			failed = append(failed, target.clusterID)
			continue
		}
		resp, _, callErr := client.InvalidatePlaybackAuthWithBundle(ctx, row.tenantID, row.reason, row.internalNames, row.streamID, row.bundleMinVersion)
		if callErr != nil {
			s.logger.WithError(callErr).WithFields(logging.Fields{
				"tenant_id":  row.tenantID,
				"cluster_id": target.clusterID,
				"reason":     row.reason,
			}).Warn("invalidation dispatch: InvalidatePlaybackAuth failed")
			failed = append(failed, target.clusterID)
			continue
		}
		if resp.GetNodesFailed() > 0 {
			s.logger.WithFields(logging.Fields{
				"tenant_id":       row.tenantID,
				"cluster_id":      target.clusterID,
				"reason":          row.reason,
				"nodes_attempted": resp.GetNodesAttempted(),
				"nodes_failed":    resp.GetNodesFailed(),
				"failed_node_ids": resp.GetFailedNodeIds(),
			}).Warn("invalidation dispatch: foghorn reported partial failure")
			failed = append(failed, target.clusterID)
		}
	}
	return failed, nil
}

// dispatchInvalidationOutbox does a route lookup + per-cluster Foghorn fanout
// for one outbox row. Returns the slug list of clusters whose dispatch was
// not fully acknowledged (NodesFailed > 0 or transport error). A non-nil error
// means the *whole* dispatch failed before per-cluster results were known
// (e.g. Quartermaster outage); failed-clusters will be nil in that case.
//
// internalNames is the snapshot resolved at mutation time (empty = scope-all,
// which Foghorn fans out across the tenant's currently-protected stream set).
func (s *CommodoreServer) dispatchInvalidationOutbox(
	ctx context.Context,
	tenantID, reason string,
	internalNames []string,
) ([]string, error) {
	route, err := s.resolveClusterRouteForTenant(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("cluster route lookup: %w", err)
	}
	targets := buildClusterFanoutTargets(route)
	if len(targets) == 0 {
		// No clusters means there's nothing to invalidate; treat as fully
		// successful so the row clears (e.g. tenant migrated off the platform).
		return nil, nil
	}

	var failed []string
	for _, target := range targets {
		client, dialErr := s.foghornPool.GetOrCreate(foghornPoolKey(target.clusterID, target.addr), target.addr)
		if dialErr != nil {
			s.logger.WithError(dialErr).WithFields(logging.Fields{
				"tenant_id":  tenantID,
				"cluster_id": target.clusterID,
			}).Warn("invalidation dispatch: dial failed")
			failed = append(failed, target.clusterID)
			continue
		}
		resp, _, callErr := client.InvalidatePlaybackAuth(ctx, tenantID, reason, internalNames)
		if callErr != nil {
			s.logger.WithError(callErr).WithFields(logging.Fields{
				"tenant_id":  tenantID,
				"cluster_id": target.clusterID,
				"reason":     reason,
			}).Warn("invalidation dispatch: InvalidatePlaybackAuth failed")
			failed = append(failed, target.clusterID)
			continue
		}
		if resp.GetNodesFailed() > 0 {
			s.logger.WithFields(logging.Fields{
				"tenant_id":       tenantID,
				"cluster_id":      target.clusterID,
				"reason":          reason,
				"nodes_attempted": resp.GetNodesAttempted(),
				"nodes_failed":    resp.GetNodesFailed(),
				"failed_node_ids": resp.GetFailedNodeIds(),
			}).Warn("invalidation dispatch: foghorn reported partial failure")
			failed = append(failed, target.clusterID)
		}
	}
	return failed, nil
}
