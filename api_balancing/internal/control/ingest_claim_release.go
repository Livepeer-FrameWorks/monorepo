package control

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

// IngestClaimClient is the Commodore surface a publisher admission uses to take the stream's
// placement claim and to give back a claim this cell's records prove is stale.
type IngestClaimClient interface {
	ValidateStreamKeyForClaim(ctx context.Context, streamKey, clusterID, claimToken string) (*commodorepb.ValidateStreamKeyResponse, error)
	SyncActiveIngestPlacement(ctx context.Context, clusterID string, renew, release []*commodorepb.ActiveIngestStream) (*commodorepb.SyncActiveIngestPlacementResponse, error)
}

// ClaimIngestPlacement takes the stream's placement claim for one admission.
//
// Ending a session (reconcile on re-register, node lost, claim lost) commits in this cell's database
// at once, but nothing gives its Commodore claim back until the claim's lease lapses. A publisher
// reconnecting within that window is refused as a duplicate of a session that no longer exists.
// When Commodore refuses with the owning token and this cell's database shows every session under
// that token ENDED in this cluster, the admission releases exactly that token (owner-fenced at
// Commodore, so a claim that has since moved is untouched) and claims once more. A claim whose
// owner is live here, unknown here, or held by another cluster stays a duplicate.
func ClaimIngestPlacement(ctx context.Context, client IngestClaimClient, streamKey, tenantID, internalName, clusterID, claimToken string, logger logging.Logger) (*commodorepb.ValidateStreamKeyResponse, error) {
	resp, err := client.ValidateStreamKeyForClaim(ctx, streamKey, clusterID, claimToken)
	if err != nil || resp == nil || resp.GetValid() ||
		resp.GetRejectionReason() != commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_DUPLICATE_INGEST {
		return resp, err
	}
	heldToken := strings.TrimSpace(resp.GetHeldClaimToken())
	if heldToken == "" || heldToken == claimToken || tenantID == "" || internalName == "" || clusterID == "" {
		return resp, nil
	}
	fields := logging.Fields{"tenant_id": tenantID, "internal_name": internalName, "ingest_cluster_id": clusterID}
	ended, err := IngestClaimOwnerEnded(ctx, tenantID, internalName, clusterID, heldToken)
	if err != nil {
		logger.WithError(err).WithFields(fields).Warn("Duplicate ingest claim kept: cannot read whether the owning session has ended")
		return resp, nil
	}
	if !ended {
		logger.WithFields(fields).Info("Duplicate ingest claim kept: the owning session is live, or not one this cell ended in this cluster")
		return resp, nil
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
