package federation

import (
	"context"
	"reflect"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	localauthority "frameworks/api_balancing/internal/mediaauthority"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	federationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// PlacementSourceIdentity comes from authenticated destination control identity
// and tenant-scoped stream resolution, never URL-supplied placement parameters.
type PlacementSourceIdentity struct {
	TenantID, ObjectID, InternalName string
	ClusterID, NodeID                string
	DestinationFence                 int64
}

type PreparedPlacementSource struct {
	DTSCURL   string
	AttemptID string
	ExpiresAt time.Time
}

// ResolvePreparedSource verifies completed preparation against current signed
// authority and physical source state without reranking or extending receipts.
// The caller must fence the emitting control registration before and after this
// read. An expired preparation needs fresh admission, not an unbound URL fallback.
func (runtime *LivePushPreparationRuntime) ResolvePreparedSource(ctx context.Context, receipts *PlacementReceiptStore, input PlacementSourceIdentity) (PreparedPlacementSource, error) {
	if runtime == nil || runtime.Authority == nil || runtime.Registry == nil || runtime.Paths == nil || receipts == nil ||
		runtime.Paths.CellID == "" || receipts.CellID != runtime.Paths.CellID {
		return PreparedPlacementSource{}, status.Error(codes.Unavailable, "prepared source enforcement is unavailable")
	}
	for _, id := range []string{input.TenantID, input.ObjectID, input.InternalName, input.ClusterID, input.NodeID} {
		if id == "" {
			return PreparedPlacementSource{}, status.Error(codes.InvalidArgument, "prepared source identity is required")
		}
	}
	if input.DestinationFence <= 0 {
		return PreparedPlacementSource{}, status.Error(codes.InvalidArgument, "authenticated source connection fence is required")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	authority, err := runtime.sourceAuthority(ctx, input)
	if err != nil {
		return PreparedPlacementSource{}, err
	}
	pull, found, err := runtime.Registry.CurrentInboundPull(ctx, input.InternalName, input.NodeID)
	if err != nil {
		return PreparedPlacementSource{}, err
	}
	if !found || pull.TenantID != input.TenantID || pull.DestClusterID != input.ClusterID || pull.DestNodeID != input.NodeID {
		return PreparedPlacementSource{}, status.Error(codes.FailedPrecondition, "prepared source has no current destination pull")
	}
	query := PlacementSourceReceiptQuery{
		TenantID: input.TenantID, InternalName: input.InternalName, ClusterID: input.ClusterID, NodeID: input.NodeID,
		TenantAuthorityVersion: authority.TenantAuthorityVersion, ObjectAuthorityVersion: authority.ObjectAuthorityVersion,
		Pull: PlacementPullBinding{AttemptID: pull.AttemptID, SourceCellID: pull.SourceClusterID, SourceClusterID: pull.SourceMediaClusterID,
			DestinationFence: input.DestinationFence,
			SourceNodeID:     pull.SourceNodeID, SourceGeneration: pull.SourceGeneration, SourceRevision: pull.SourceRevision},
	}
	receipt, err := receipts.CompletedPull(ctx, query)
	if err != nil {
		return PreparedPlacementSource{}, err
	}
	if receipt.Request.GetQuery().GetObjectId() != input.ObjectID || !authority.MatchesQuery(receipt.Request.Query, runtime.Paths.CellID) {
		return PreparedPlacementSource{}, status.Error(codes.FailedPrecondition, "prepared source authority differs from completed placement")
	}
	if err = runtime.Revalidate(ctx, receipt.Request, receipt); err != nil {
		return PreparedPlacementSource{}, err
	}
	// A restarted Mist input needs fresh source-side acceptance, even when
	// the destination can reuse its existing physical attempt and receipt.
	if pull.SourceAcceptedAt.IsZero() || !runtime.now().Before(pull.SourceAcceptedAt.Add(3*time.Minute)) {
		if runtime.Arrange == nil {
			return PreparedPlacementSource{}, status.Error(codes.Unavailable, "source acceptance renewal is unavailable")
		}
		renewed, renewalErr := runtime.Arrange.ArrangeOriginPull(ctx, ArrangeOriginPullRequest{
			TenantID: input.TenantID, InternalName: input.InternalName, DestClusterID: input.ClusterID,
			DestNodeID: input.NodeID, DestNodeBaseURL: pull.DestNodeBaseURL, DestinationFence: input.DestinationFence,
			RemoteCluster: pull.SourceClusterID, SourceGeneration: pull.SourceGeneration, SourceRevision: pull.SourceRevision,
			AttemptID: pull.AttemptID, RefreshAcceptance: true,
			Remote: &federationpb.EdgeCandidate{ClusterId: pull.SourceMediaClusterID, NodeId: pull.SourceNodeID,
				SourceGeneration: pull.SourceGeneration, SourceRevision: pull.SourceRevision},
			BindPull: func(binding *PlacementPullBinding) error {
				if binding == nil || *binding != query.Pull {
					return ErrPlacementReceiptConflict
				}
				return nil
			},
		})
		if renewalErr != nil {
			return PreparedPlacementSource{}, renewalErr
		}
		// Compare the media path, not the credentialed URL. A renewal reissues
		// the per-attempt credential, and the source secret can rotate under it,
		// so the same physical pull legitimately comes back with a different
		// token. The attempt id above is what binds identity.
		if renewed == nil || renewed.AttemptID != pull.AttemptID ||
			control.SourcePullBaseURL(renewed.PullDTSCURL) != control.SourcePullBaseURL(pull.DTSCURL) {
			return PreparedPlacementSource{}, ErrPlacementReceiptConflict
		}
	}
	current, err := runtime.sourceAuthority(ctx, input)
	if err != nil {
		return PreparedPlacementSource{}, err
	}
	if !reflect.DeepEqual(authority, current) {
		return PreparedPlacementSource{}, status.Error(codes.FailedPrecondition, "prepared source authority changed during resolution")
	}
	latest, found, err := runtime.Registry.CurrentInboundPull(ctx, input.InternalName, input.NodeID)
	if err != nil {
		return PreparedPlacementSource{}, err
	}
	if !found || !samePreparedSourcePull(pull, latest) {
		return PreparedPlacementSource{}, status.Error(codes.FailedPrecondition, "prepared source pull changed during resolution")
	}
	// Physical/authority reads can span a coordination restart or expiry.
	// Reconfirm completed evidence without extending the original result lifetime.
	if _, err = receipts.CompletedPull(ctx, query); err != nil {
		return PreparedPlacementSource{}, err
	}
	fence, err := runtime.currentDestinationFence(ctx, input.NodeID, input.ClusterID)
	if err != nil {
		return PreparedPlacementSource{}, err
	}
	if fence != input.DestinationFence {
		return PreparedPlacementSource{}, status.Error(codes.FailedPrecondition, "destination connection changed during source resolution")
	}
	if err = ctx.Err(); err != nil {
		return PreparedPlacementSource{}, status.FromContextError(err).Err()
	}
	until := receipt.Response.GetExpiresAt().AsTime()
	if !runtime.now().Before(until) {
		return PreparedPlacementSource{}, ErrPlacementReceiptExpired
	}
	return PreparedPlacementSource{DTSCURL: latest.DTSCURL, AttemptID: latest.AttemptID, ExpiresAt: until}, nil
}

func (runtime *LivePushPreparationRuntime) sourceAuthority(ctx context.Context, input PlacementSourceIdentity) (balancer.PlacementAuthority, error) {
	ctx, cancel := context.WithTimeout(ctx, localauthority.PlacementReadTimeout)
	defer cancel()
	pair, err := runtime.Authority.Placement(ctx, input.TenantID, input.ObjectID, input.InternalName)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return balancer.PlacementAuthority{}, ctxErr
	}
	if err != nil {
		return balancer.PlacementAuthority{}, err
	}
	authority, err := balancer.CompilePlacementAuthority(pair, placement.Serve, runtime.now())
	if err != nil {
		return balancer.PlacementAuthority{}, err
	}
	if authority.TenantID != input.TenantID || authority.ObjectID != input.ObjectID || authority.InternalName != input.InternalName {
		return balancer.PlacementAuthority{}, status.Error(codes.FailedPrecondition, "prepared source authority identity differs")
	}
	return authority, nil
}

func samePreparedSourcePull(a, b control.InboundPull) bool {
	return a.TenantID == b.TenantID && a.AttemptID == b.AttemptID && a.SourceClusterID == b.SourceClusterID &&
		a.SourceMediaClusterID == b.SourceMediaClusterID && a.SourceNodeID == b.SourceNodeID && a.SourceGeneration == b.SourceGeneration &&
		a.SourceRevision == b.SourceRevision && a.DestClusterID == b.DestClusterID && a.DestNodeID == b.DestNodeID && a.DTSCURL == b.DTSCURL
}
