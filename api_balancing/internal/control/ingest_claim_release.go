package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"frameworks/api_balancing/internal/database/foghorndb"
	"frameworks/api_balancing/internal/domainevents"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

// IngestAdmissionWindow is how long after it is minted a pending ingest session can still become
// active. Only a delivery of the same PUSH_REWRITE execution resumes a pending row (by its trigger
// UUID), and Mist delivers it only within mist.BlockingTriggerLifetime of firing it, which precedes
// the mint. A session still pending past the window never had its answer reach Mist: Mist denied
// that push and the connector is gone, so the session can never be projected.
const IngestAdmissionWindow = mist.BlockingTriggerLifetime

// IngestEndedAdmissionLapsed ends a pending session whose admission window passed without the
// source projection confirming.
const IngestEndedAdmissionLapsed = "admission_lapsed"

// IngestClaimClient is the Commodore surface a publisher admission uses to take the stream's
// placement claim and to give back a claim this cell's records prove is stale.
type IngestClaimClient interface {
	ValidateStreamKeyForClaim(ctx context.Context, streamKey, clusterID, claimToken string) (*commodorepb.ValidateStreamKeyResponse, error)
	SyncActiveIngestPlacement(ctx context.Context, clusterID string, renew, release []*commodorepb.ActiveIngestStream) (*commodorepb.SyncActiveIngestPlacementResponse, error)
}

// ClaimIngestPlacement takes the stream's placement claim for one admission from nodeID.
//
// Ending a session (reconcile on re-register, node lost, claim lost) commits in this cell's database
// at once, but nothing gives its Commodore claim back until the claim's lease lapses. A publisher
// reconnecting within that window is refused as a duplicate of a session that no longer exists.
// When Commodore refuses with the owning token and this cell's database shows every session under
// that token ENDED in this cluster, the admission releases exactly that token (owner-fenced at
// Commodore, so a claim that has since moved is untouched) and claims once more. An owner that is
// a pending session on the same node past its admission window is ended first (it can never become
// active), then released the same way. A claim whose owner is live here, still inside its admission
// window, on another node, unknown here, or held by another cluster stays a duplicate.
func ClaimIngestPlacement(ctx context.Context, client IngestClaimClient, streamKey, tenantID, internalName, nodeID, clusterID, claimToken string, logger logging.Logger) (*commodorepb.ValidateStreamKeyResponse, error) {
	resp, err := client.ValidateStreamKeyForClaim(ctx, streamKey, clusterID, claimToken)
	if err != nil || resp == nil || resp.GetValid() ||
		resp.GetRejectionReason() != commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_DUPLICATE_INGEST {
		return resp, err
	}
	heldToken := strings.TrimSpace(resp.GetHeldClaimToken())
	if heldToken == "" || heldToken == claimToken || tenantID == "" || internalName == "" || clusterID == "" {
		return resp, nil
	}
	fields := logging.Fields{"tenant_id": tenantID, "internal_name": internalName, "ingest_cluster_id": clusterID, "node_id": nodeID}
	ended, err := IngestClaimOwnerEnded(ctx, tenantID, internalName, clusterID, heldToken)
	if err != nil {
		logger.WithError(err).WithFields(fields).Warn("Duplicate ingest claim kept: cannot read whether the owning session has ended")
		return resp, nil
	}
	if !ended {
		superseded, supErr := EndLapsedPendingIngestSession(ctx, tenantID, internalName, nodeID, heldToken, logger)
		if supErr != nil {
			logger.WithError(supErr).WithFields(fields).Warn("Duplicate ingest claim kept: cannot end the owning session's lapsed admission")
			return resp, nil
		}
		if !superseded {
			logger.WithFields(fields).Info("Duplicate ingest claim kept: the owning session is live or still inside its admission window, or not one this cell holds for this node and cluster")
			return resp, nil
		}
		if ended, err = IngestClaimOwnerEnded(ctx, tenantID, internalName, clusterID, heldToken); err != nil || !ended {
			logger.WithError(err).WithFields(fields).Warn("Duplicate ingest claim kept: the lapsed owner is not an ended session of this cluster")
			return resp, nil
		}
	}
	release := []*commodorepb.ActiveIngestStream{{TenantId: tenantID, InternalName: internalName, ClusterId: clusterID, ClaimToken: heldToken}}
	released, err := client.SyncActiveIngestPlacement(ctx, clusterID, nil, release)
	if err != nil {
		logger.WithError(err).WithFields(fields).Warn("Duplicate ingest claim kept: releasing the ended session's claim failed")
		return resp, nil
	}
	fields["released"] = released.GetReleased()
	logger.WithFields(fields).Info("Released the placement claim of an ended ingest session; claiming again")
	return client.ValidateStreamKeyForClaim(ctx, streamKey, clusterID, claimToken)
}

// EndLapsedPendingIngestSession ends the session nodeID minted under claimToken when it is still
// pending past IngestAdmissionWindow, with reason admission_lapsed. It runs under the stream's
// admission lock, so a projection of the same session serializes against it: either the projection
// confirmed first and nothing is ended, or the session ends first and the projection is refused.
// Like the never-projected reaper it emits stream.idle for the stream.connected its mint emitted,
// claims any DVR stop bound to it, and queues an offline effect that only advances the source
// revision (a pending session never projected a source to tear down).
func EndLapsedPendingIngestSession(ctx context.Context, tenantID, internalName, nodeID, claimToken string, logger logging.Logger) (bool, error) {
	if db == nil {
		return false, errors.New("ingest session database unavailable")
	}
	if tenantID == "" || internalName == "" || nodeID == "" || claimToken == "" {
		return false, nil
	}
	var (
		claims    []DVRStopClaim
		sessionID string
	)
	playbackID := streamEventPlaybackID(ctx, internalName)
	err := database.WithRetryablePostgresTx(ctx, db, nil, func(tx *sql.Tx) error {
		claims, sessionID = nil, ""
		qtx := foghorndb.New(tx)
		if err := qtx.LockIngestStream(ctx, ingestStreamAdvisoryLockKey(tenantID, internalName)); err != nil {
			return fmt.Errorf("acquire ingest-session stream lock: %w", err)
		}
		row, err := endLapsedPendingIngestSessionTx(ctx, tx, tenantID, internalName, nodeID, claimToken, playbackID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		claimed, err := ClaimDVRStops(ctx, tx, `ingest_generation = $1::uuid AND tenant_id::text = $2`, row.SessionID, tenantID)
		if err != nil {
			return fmt.Errorf("claim DVR stop of a lapsed admission: %w", err)
		}
		revision, err := nextSourceRevision(ctx, tx, tenantID, internalName)
		if err != nil {
			return err
		}
		if err := enqueueOfflineEffectTx(ctx, tx, tenantID, internalName, nodeID, row.SessionID, revision, OfflineEffectIntent{}); err != nil {
			return err
		}
		claims, sessionID = claimed, row.SessionID
		return nil
	})
	if err != nil {
		return false, err
	}
	if sessionID == "" {
		return false, nil
	}
	logger.WithFields(logging.Fields{
		"tenant_id": tenantID, "internal_name": internalName, "node_id": nodeID, "ingest_generation": sessionID,
		"admission_window": IngestAdmissionWindow.String(),
	}).Warn("Ended a pending ingest session whose admission window passed without projection: Mist never received its PUSH_REWRITE answer and refused that push")
	DispatchDVRStops(claims, logger)
	return true, nil
}

// endLapsedPendingIngestSessionTx ends the lapsed pending session and emits its stream.idle inside
// the caller's transaction, which must hold the stream's admission lock. sql.ErrNoRows means no
// session matched: it projected, ended, is still inside its admission window, or is not this node's.
func endLapsedPendingIngestSessionTx(ctx context.Context, tx *sql.Tx, tenantID, internalName, nodeID, claimToken, playbackID string) (foghorndb.EndLapsedPendingIngestSessionRow, error) {
	row, err := foghorndb.New(tx).EndLapsedPendingIngestSession(ctx, foghorndb.EndLapsedPendingIngestSessionParams{
		TenantID: tenantID, StreamInternalName: internalName, NodeID: nodeID, ClaimToken: claimToken,
		AdmissionWindowMs: IngestAdmissionWindow.Milliseconds(),
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return row, err
		}
		return row, fmt.Errorf("end lapsed pending ingest session: %w", err)
	}
	if err := domainevents.StreamIdle(ctx, tx, tenantID, row.StreamID, playbackID); err != nil {
		return row, err
	}
	return row, nil
}

// IngestClaimOwnerEnded reports whether this cell ended the session that owns claimToken in
// clusterID: at least one session under the token was admitted into that cluster and ended, and no
// session under the token is still open.
func IngestClaimOwnerEnded(ctx context.Context, tenantID, internalName, clusterID, claimToken string) (bool, error) {
	if db == nil {
		return false, errors.New("ingest session database unavailable")
	}
	ended, err := foghorndb.New(db).IngestClaimOwnerEnded(ctx, foghorndb.IngestClaimOwnerEndedParams{
		IngestClusterID: clusterID, TenantID: tenantID, StreamInternalName: internalName, ClaimToken: claimToken,
	})
	if err != nil {
		return false, fmt.Errorf("read ingest claim owner: %w", err)
	}
	return ended, nil
}
