package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
	"frameworks/api_balancing/internal/domainevents"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/google/uuid"
)

// NodeRetireGuardFunc atomically confirms the node has no control connection to any replica (its
// conn_owner key is absent) and blocks a new conn_owner acquisition until release. A nil release
// with nil error means the node is connected, or another retirement holds the guard. A non-nil error
// means presence is unknown (e.g. Redis unreachable); callers treat that as "connected" and fail
// closed.
type NodeRetireGuardFunc func(ctx context.Context, nodeID string) (release func(), err error)

// Losing the control connection does not by itself end an ingest session. A Helmsman reconnect or a
// Foghorn-side partition leaves Mist and its publishers running, so short absence is not evidence
// the publisher stopped. Sessions end on evidence:
//   - PUSH_INPUT_CLOSE, STREAM_END, and Helmsman's INGEST_RUNTIME_ABSENT report the publisher gone;
//   - a publisher for the same stream admitted on another node while this node is absent takes the
//     stream over (MintIngestSession, reason superseded_by_new_node);
//   - the node re-registers without listing the generation (ReconcileNodeIngestSessions, reason
//     absent_on_reregister);
//   - Commodore refuses the placement renewal (RetireIngestSessionByClaim);
//   - the node stays without a control connection and without evidence of life for
//     IngestNodeLostAfter (ReapLostNodeIngestSessionsOnce, reason node_lost).

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
	var claims []DVRStopClaim
	playbackID := streamEventPlaybackID(ctx, internalName)
	err = database.WithRetryablePostgresTx(ctx, db, nil, func(tx *sql.Tx) error {
		claims, retired = nil, false
		qtx := foghorndb.New(tx)
		if lockErr := qtx.AcquireDVRStartLock(ctx, ingestStreamAdvisoryLockKey(tenantID, internalName)); lockErr != nil {
			return fmt.Errorf("lock ingest session retirement: %w", lockErr)
		}
		retiredRow, retireErr := qtx.RetireIngestSession(ctx, foghorndb.RetireIngestSessionParams{
			EndedReason: reason, SessionID: sessionID, TenantID: tenantID, StreamInternalName: internalName,
		})
		if errors.Is(retireErr, sql.ErrNoRows) {
			return nil
		}
		if retireErr != nil {
			return fmt.Errorf("end ingest session: %w", retireErr)
		}
		nodeID := retiredRow.NodeID
		claimed, claimErr := ClaimDVRStops(ctx, tx, `ingest_generation = $1::uuid AND tenant_id::text = $2`, sessionID, tenantID)
		if claimErr != nil {
			return fmt.Errorf("claim DVR stop on retire: %w", claimErr)
		}
		if idleErr := domainevents.StreamIdle(ctx, tx, tenantID, retiredRow.StreamID, playbackID); idleErr != nil {
			return idleErr
		}
		revision, revErr := nextSourceRevision(ctx, tx, tenantID, internalName)
		if revErr != nil {
			return revErr
		}
		if enqueueErr := enqueueOfflineEffectTx(ctx, tx, tenantID, internalName, nodeID, sessionID, revision, OfflineEffectIntent{
			SetNodeOffline: true, TeardownStream: true, BroadcastOffline: true,
		}); enqueueErr != nil {
			return enqueueErr
		}
		claims, retired = claimed, true
		return nil
	})
	if err != nil {
		return false, err
	}
	if !retired {
		return false, nil
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
	var (
		claims  []DVRStopClaim
		nodeID  string
		retired bool
	)
	playbackID := streamEventPlaybackID(ctx, internalName)
	err := database.WithRetryablePostgresTx(ctx, db, nil, func(tx *sql.Tx) error {
		claims, nodeID, retired = nil, "", false
		qtx := foghorndb.New(tx)
		if lockErr := qtx.AcquireDVRStartLock(ctx, ingestStreamAdvisoryLockKey(tenantID, internalName)); lockErr != nil {
			return lockErr
		}
		retiredRow, err := qtx.RetireIngestSessionByClaim(ctx, foghorndb.RetireIngestSessionByClaimParams{
			TenantID: tenantID, StreamInternalName: internalName, ClaimToken: claimToken,
		})
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("retire lost placement claim: %w", err)
		}
		sessionID := retiredRow.SessionID
		claimed, err := ClaimDVRStops(ctx, tx, `ingest_generation = $1::uuid AND tenant_id::text = $2`, sessionID, tenantID)
		if err != nil {
			return err
		}
		if idleErr := domainevents.StreamIdle(ctx, tx, tenantID, retiredRow.StreamID, playbackID); idleErr != nil {
			return idleErr
		}
		revision, err := nextSourceRevision(ctx, tx, tenantID, internalName)
		if err != nil {
			return err
		}
		if err := enqueueOfflineEffectTx(ctx, tx, tenantID, internalName, retiredRow.NodeID, sessionID, revision, OfflineEffectIntent{
			SetNodeOffline: true, TeardownStream: true, BroadcastOffline: true,
		}); err != nil {
			return err
		}
		claims, nodeID, retired = claimed, retiredRow.NodeID, true
		return nil
	})
	if err != nil {
		return "", false, err
	}
	if !retired {
		return "", false, nil
	}
	DispatchDVRStops(claims, logger)
	return nodeID, true, nil
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
	type pending struct{ id, tenant, stream, claimToken string }
	candidates := make([]pending, 0, len(rows))
	for _, row := range rows {
		candidates = append(candidates, pending{id: row.SessionID, tenant: row.TenantID, stream: row.StreamInternalName, claimToken: row.StartTriggerUuid})
	}
	retired := 0
	for _, candidate := range candidates {
		retiredCandidate, began := false, false
		playbackID := streamEventPlaybackID(ctx, candidate.stream)
		err := database.WithRetryablePostgresTx(ctx, db, nil, func(tx *sql.Tx) error {
			retiredCandidate, began = false, true
			qtx := foghorndb.New(tx)
			if err := qtx.AcquireDVRStartLock(ctx, ingestStreamAdvisoryLockKey(candidate.tenant, candidate.stream)); err != nil {
				return err
			}
			ended, err := qtx.RetireNeverProjectedIngestSession(ctx, foghorndb.RetireNeverProjectedIngestSessionParams{
				SessionID: candidate.id, TenantID: candidate.tenant, OlderThanMs: olderThan.Milliseconds(),
			})
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			if idleErr := domainevents.StreamIdle(ctx, tx, candidate.tenant, ended.StreamID, playbackID); idleErr != nil {
				return idleErr
			}
			revision, err := nextSourceRevision(ctx, tx, candidate.tenant, candidate.stream)
			if err != nil {
				return err
			}
			if err := enqueueOfflineEffectTx(ctx, tx, candidate.tenant, candidate.stream, ended.NodeID, candidate.id, revision, OfflineEffectIntent{}); err != nil {
				return err
			}
			retiredCandidate = true
			return nil
		})
		if err != nil && !began {
			return retired, err
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
// even-later rewrite would mint a session with no live publisher, which Helmsman's Mist absence
// reconciliation or the node's next registration ends. Returns the number of rows deleted.
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
			logging.NewLogger().WithError(err).WithField("node_id", nodeID).Warn("Failed to release ingest node retirement guard")
		}
	}, nil
}
