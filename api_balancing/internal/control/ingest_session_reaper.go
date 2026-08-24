package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/google/uuid"
)

// OpenIngestSession is one active (ended_at IS NULL) ingest session the disconnect reaper evaluates.
type OpenIngestSession struct {
	SessionID    string
	TenantID     string
	NodeID       string
	InternalName string
}

// NodePresenceFunc reports whether a node currently has a control connection to ANY Foghorn replica in
// this cell (its conn_owner key exists). present=false means the node is connected nowhere. A non-nil
// error is a LOOKUP failure (e.g. Redis unreachable), NOT an absence: the reaper treats it as unknown
// and leaves the session untouched (fail closed), because a false "disconnected" would tear down a
// live session whose owner is merely momentarily unreadable. It takes the pass context so a slow Redis
// cannot outlive the reconcile's timeout.
//
// The reaper deliberately keys off PRESENCE, not the ownership fence: an ordinary Helmsman control-
// stream reconnect (transport blip) re-registers with a HIGHER fence WITHOUT restarting Mist or its
// publishers, so a fence bump is NOT evidence the ingest session ended. Session end is driven by
// PUSH_INPUT_CLOSE / STREAM_END / DB-side PID-reuse supersession; this reaper only backstops the case
// those cannot reach — the node being gone entirely.
type NodePresenceFunc func(ctx context.Context, nodeID string) (present bool, err error)

// NodeRetireGuardFunc atomically confirms the node is still absent and prevents a new conn_owner
// acquisition until release. A nil release with nil error means presence changed or another reaper won.
type NodeRetireGuardFunc func(ctx context.Context, nodeID string) (release func(), err error)

// IngestReapDwell tracks, per session id, when the reaper FIRST observed its node's conn_owner absent.
// A session is retired for disconnect only after its node has been continuously absent for the grace,
// so a brief control-plane blip — the node drops and re-acquires conn_owner within the grace — never
// tears down a still-live session. It is process-local (each replica dwells independently; the retire
// is an idempotent guarded UPDATE, so two replicas racing is safe) and conservative across restarts (a
// restart just re-observes and re-times the grace, never over-retires).
type IngestReapDwell map[string]time.Time

// ListOpenIngestSessions returns every active (ended_at IS NULL) ingest session in the cell. The set
// is small (at most one active session per stream), so a full scan per pass is cheap. Cell-scoped
// administrative scan over Foghorn's own schema (the tenant-filter rule's documented exception): the
// reaper reconciles the whole cell, and each row's tenant_id scopes the guarded retire it drives.
func ListOpenIngestSessions(ctx context.Context) ([]OpenIngestSession, error) {
	if db == nil {
		return nil, nil
	}
	rows, err := foghorndb.New(db).ListOpenIngestSessions(ctx)
	if err != nil {
		return nil, fmt.Errorf("list open ingest sessions: %w", err)
	}
	out := make([]OpenIngestSession, 0, len(rows))
	for _, row := range rows {
		out = append(out, OpenIngestSession{SessionID: row.SessionID, TenantID: row.TenantID, NodeID: row.NodeID, InternalName: row.StreamInternalName})
	}
	return out, nil
}

// RetireIngestSession ends one active session with the given reason and, in the SAME transaction,
// claims the bound DVR stop and queues the source-offline transition in the same stream-locked
// transaction. The end is guarded on ended_at IS NULL, so another finalizer makes this a no-op.
// DVR dispatch occurs after commit; both stop and offline work are already durable.
func RetireIngestSession(ctx context.Context, sessionID, tenantID, internalName, reason string, logger logging.Logger) (retired bool, err error) {
	if db == nil {
		return false, nil
	}
	if sessionID == "" || tenantID == "" || internalName == "" {
		return false, fmt.Errorf("retire ingest session missing scope: session=%q tenant=%q stream=%q", sessionID, tenantID, internalName)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin retire ingest-session tx: %w", err)
	}
	defer func() {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			logger.WithError(rbErr).Warn("Failed to roll back retire ingest-session tx")
		}
	}()
	qtx := foghorndb.New(tx)
	if lockErr := qtx.AcquireDVRStartLock(ctx, ingestStreamAdvisoryLockKey(tenantID, internalName)); lockErr != nil {
		return false, fmt.Errorf("lock ingest session retirement: %w", lockErr)
	}
	nodeID, err := qtx.RetireIngestSession(ctx, foghorndb.RetireIngestSessionParams{
		EndedReason: reason, SessionID: sessionID, TenantID: tenantID, StreamInternalName: internalName,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return false, tx.Commit()
	}
	if err != nil {
		return false, fmt.Errorf("end ingest session: %w", err)
	}
	claims, err := ClaimDVRStops(ctx, tx, `ingest_generation = $1::uuid AND tenant_id::text = $2`, sessionID, tenantID)
	if err != nil {
		return false, fmt.Errorf("claim DVR stop on retire: %w", err)
	}
	revision, err := nextSourceRevision(ctx, tx)
	if err != nil {
		return false, err
	}
	if err := enqueueOfflineEffectTx(ctx, tx, tenantID, internalName, nodeID, sessionID, revision, OfflineEffectIntent{
		SetNodeOffline: true, TeardownStream: true, BroadcastOffline: true,
	}); err != nil {
		return false, err
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return false, fmt.Errorf("commit retire ingest session: %w", commitErr)
	}
	DispatchDVRStops(claims, logger)
	return true, nil
}

// RetireIngestSessionByClaim fences the exact publisher whose Commodore placement renewal was
// refused and durably queues its offline transition. A different connection has a different claim
// token and is untouched.
func RetireIngestSessionByClaim(ctx context.Context, tenantID, internalName, claimToken string, logger logging.Logger) (string, bool, error) {
	if db == nil {
		return "", false, errors.New("retire ingest claim: no database configured")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer rollbackQuiet(tx)
	qtx := foghorndb.New(tx)
	if lockErr := qtx.AcquireDVRStartLock(ctx, ingestStreamAdvisoryLockKey(tenantID, internalName)); lockErr != nil {
		return "", false, lockErr
	}
	retiredRow, err := qtx.RetireIngestSessionByClaim(ctx, foghorndb.RetireIngestSessionByClaimParams{
		TenantID: tenantID, StreamInternalName: internalName, ClaimToken: claimToken,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, tx.Commit()
	}
	if err != nil {
		return "", false, fmt.Errorf("retire lost placement claim: %w", err)
	}
	sessionID, nodeID := retiredRow.SessionID, retiredRow.NodeID
	claims, err := ClaimDVRStops(ctx, tx, `ingest_generation=$1::uuid AND tenant_id::text=$2`, sessionID, tenantID)
	if err != nil {
		return "", false, err
	}
	revision, err := nextSourceRevision(ctx, tx)
	if err != nil {
		return "", false, err
	}
	if err := enqueueOfflineEffectTx(ctx, tx, tenantID, internalName, nodeID, sessionID, revision, OfflineEffectIntent{
		SetNodeOffline: true, TeardownStream: true, BroadcastOffline: true,
	}); err != nil {
		return "", false, err
	}
	if err := tx.Commit(); err != nil {
		return "", false, err
	}
	DispatchDVRStops(claims, logger)
	return nodeID, true, nil
}

// ReapIngestSessionsOnce runs one disconnect-reaper pass. For each active ingest session it consults
// nodePresent (does the node have a control connection to ANY replica) and decides:
//
//   - lookup error → leave the session (fail closed; a momentarily unreadable owner is not a disconnect);
//   - node PRESENT → the node is connected somewhere → the session is live here as far as this backstop
//     is concerned (a control reconnect is not a session end); clear any dwell;
//   - node ABSENT continuously for `grace` → the node is disconnected from every replica → retire
//     ('control_disconnect'). A first-seen-absent is only recorded now; the retire waits out the grace.
//
// dwell is read and updated in place (first-absent timestamps); entries for sessions no longer open,
// or whose node came back, are pruned so it cannot grow unbounded. Retiring is HA-safe: RetireIngestSession
// is an idempotent guarded UPDATE, so two replicas reaping the same row race harmlessly. Returns the
// number of sessions this pass retired.
func ReapIngestSessionsOnce(ctx context.Context, nodePresent NodePresenceFunc, nodeGuard NodeRetireGuardFunc, dwell IngestReapDwell, now time.Time, grace time.Duration, logger logging.Logger) (int, error) {
	if nodePresent == nil || nodeGuard == nil || dwell == nil {
		return 0, nil
	}
	sessions, err := ListOpenIngestSessions(ctx)
	if err != nil {
		return 0, err
	}
	// Prune dwell entries whose session is no longer open (ended between passes).
	live := make(map[string]struct{}, len(sessions))
	for _, s := range sessions {
		live[s.SessionID] = struct{}{}
	}
	for id := range dwell {
		if _, ok := live[id]; !ok {
			delete(dwell, id)
		}
	}

	// Resolve presence ONCE per DISTINCT node, not once per session: thousands of streams can share a
	// node (up to ~32k publishers per instance), so a per-session Redis lookup would be O(sessions).
	// presenceByNode caches this pass's answer; a lookup error is recorded so its sessions are left
	// untouched (fail closed) without re-querying.
	type presence struct {
		present bool
		err     error
	}
	presenceByNode := make(map[string]presence)
	nodePresence := func(nodeID string) presence {
		if p, ok := presenceByNode[nodeID]; ok {
			return p
		}
		ok, err := nodePresent(ctx, nodeID)
		p := presence{present: ok, err: err}
		presenceByNode[nodeID] = p
		return p
	}

	retired := 0
	for _, s := range sessions {
		np := nodePresence(s.NodeID)
		if np.err != nil {
			// Unknown presence — do NOT reap. Keep any existing dwell so a persistent Redis outage does
			// not silently reset the grace clock, but never advance a decision on an unreadable owner.
			continue
		}
		if np.present {
			// The node is connected somewhere; it is not disconnected. Clear any stale dwell.
			delete(dwell, s.SessionID)
			continue
		}
		// conn_owner absent: the node is disconnected from every replica. Retire only after the grace.
		first, seen := dwell[s.SessionID]
		if !seen {
			dwell[s.SessionID] = now
			continue
		}
		if now.Sub(first) < grace {
			continue
		}
		release, guardErr := nodeGuard(ctx, s.NodeID)
		if guardErr != nil {
			logger.WithError(guardErr).WithField("node_id", s.NodeID).Warn("Ingest reaper: node retirement guard unavailable")
			continue
		}
		if release == nil {
			// The node reconnected after the pass-level presence read, or another reaper owns the guard.
			delete(dwell, s.SessionID)
			continue
		}
		ok, rErr := RetireIngestSession(ctx, s.SessionID, s.TenantID, s.InternalName, "control_disconnect", logger)
		release()
		if rErr != nil {
			logger.WithError(rErr).WithFields(logging.Fields{
				"session_id":    s.SessionID,
				"internal_name": s.InternalName,
				"node_id":       s.NodeID,
			}).Warn("Ingest reaper: failed to retire disconnected session")
			continue
		}
		delete(dwell, s.SessionID)
		if ok {
			retired++
			logger.WithFields(logging.Fields{
				"session_id":    s.SessionID,
				"internal_name": s.InternalName,
				"node_id":       s.NodeID,
				"absent_for":    now.Sub(first).String(),
			}).Info("Ingest reaper: retired session whose node is disconnected past the grace")
		}
	}
	return retired, nil
}

// ReapNeverProjectedIngestSessions retires sessions whose admission never crossed the shared source
// projection CAS. It is independent of node presence: a control connection can remain healthy after
// the blocking PUSH_REWRITE failed, and such a pending row must not hold stream authority forever.
func ReapNeverProjectedIngestSessions(ctx context.Context, olderThan time.Duration, logger logging.Logger) (int, error) {
	if db == nil {
		return 0, nil
	}
	if olderThan <= 0 {
		olderThan = 2 * time.Minute
	}
	rows, err := foghorndb.New(db).ListNeverProjectedIngestSessions(ctx, olderThan.Milliseconds())
	if err != nil {
		return 0, fmt.Errorf("list never-projected ingest sessions: %w", err)
	}
	type pending struct{ id, tenant, stream string }
	candidates := make([]pending, 0, len(rows))
	for _, row := range rows {
		candidates = append(candidates, pending{id: row.SessionID, tenant: row.TenantID, stream: row.StreamInternalName})
	}
	retired := 0
	for _, candidate := range candidates {
		retiredCandidate := false
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return retired, err
		}
		qtx := foghorndb.New(tx)
		if err = qtx.AcquireDVRStartLock(ctx, ingestStreamAdvisoryLockKey(candidate.tenant, candidate.stream)); err == nil {
			var nodeID string
			nodeID, err = qtx.RetireNeverProjectedIngestSession(ctx, foghorndb.RetireNeverProjectedIngestSessionParams{
				SessionID: candidate.id, TenantID: candidate.tenant, OlderThanMs: olderThan.Milliseconds(),
			})
			if errors.Is(err, sql.ErrNoRows) {
				err = nil
			} else if err == nil {
				var revision int64
				revision, err = nextSourceRevision(ctx, tx)
				if err == nil {
					err = enqueueOfflineEffectTx(ctx, tx, candidate.tenant, candidate.stream, nodeID, candidate.id, revision, OfflineEffectIntent{})
				}
				retiredCandidate = err == nil
			}
		}
		if err == nil {
			err = tx.Commit()
		} else if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("rollback never-projected session: %w", rollbackErr))
		}
		if err != nil {
			logger.WithError(err).WithField("session_id", candidate.id).Warn("Failed to retire never-projected ingest session")
		} else if retiredCandidate {
			retired++
		}
	}
	return retired, nil
}

// PurgeExpiredCloseTombstones deletes close-before-insert tombstones older than olderThan. A tombstone
// only has to outlive the window in which a PUSH_REWRITE for the SAME connector can still be
// redelivered behind its close — bounded by Helmsman's blocking-trigger retry window (seconds), so the
// default 10-minute TTL is amply conservative. Beyond it the publisher is long gone; a hypothetical
// even-later rewrite would mint a session on a node that (having no live publisher) eventually
// disconnects and is caught by the disconnect reaper — but that is a backstop, not the reason the TTL
// is safe. Returns the number of rows deleted.
func PurgeExpiredCloseTombstones(ctx context.Context, olderThan time.Duration) (int64, error) {
	if db == nil {
		return 0, nil
	}
	n, err := foghorndb.New(db).PurgeExpiredCloseTombstones(ctx, olderThan.Seconds())
	if err != nil {
		return 0, fmt.Errorf("purge ingest close tombstones: %w", err)
	}
	return n, nil
}

// NodePresenceLookup is the production NodePresenceFunc: it reports whether the node's conn_owner key
// exists in Redis (connected to some replica). present=false when no owner key exists; a Redis error is
// surfaced so the reaper fails closed. A zero-value InstanceID with no error is an absent key (see
// GetConnOwner).
func NodePresenceLookup(ctx context.Context, nodeID string) (bool, error) {
	rs := GetRedisStore()
	if rs == nil {
		// No Redis configured (tests / unconfigured): report unknown so nothing is reaped.
		return false, errConnOwnerUnavailable
	}
	owner, err := rs.GetConnOwner(ctx, nodeID)
	if err != nil {
		return false, err
	}
	return owner.InstanceID != "", nil
}

var errConnOwnerUnavailable = errors.New("conn_owner store unavailable")

// NodeRetireGuardLookup is the production guard. Its Redis script checks conn_owner absence and sets
// the registration barrier atomically; AcquireConnOwnerFenced observes the same barrier.
func NodeRetireGuardLookup(ctx context.Context, nodeID string) (func(), error) {
	rs := GetRedisStore()
	if rs == nil {
		return nil, errConnOwnerUnavailable
	}
	token := uuid.NewString()
	acquired, err := rs.AcquireNodeReapGuard(ctx, nodeID, token, 90*time.Second)
	if err != nil || !acquired {
		return nil, err
	}
	return func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := rs.ReleaseNodeReapGuard(releaseCtx, nodeID, token); err != nil {
			logging.NewLogger().WithError(err).WithField("node_id", nodeID).Warn("Failed to release ingest-session reaper guard")
		}
	}, nil
}
