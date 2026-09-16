package federation

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"time"

	"frameworks/api_balancing/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	federationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ResolvePreparedSource dispatches final source admission by signed ingest mode.
// Push publishers retain their generation-specific path; configured relays use
// the configured-source preparation that created their own receipt and pull.
func (runtime *MediaServePreparationRuntime) ResolvePreparedSource(ctx context.Context, receipts *PlacementReceiptStore, input PlacementSourceIdentity) (PreparedPlacementSource, error) {
	if runtime == nil || runtime.Push == nil || runtime.Registry == nil || runtime.Arrange == nil || receipts == nil ||
		runtime.CellID == "" || receipts.CellID != runtime.CellID {
		return PreparedPlacementSource{}, status.Error(codes.Unavailable, "prepared media source enforcement is unavailable")
	}
	authority, err := runtime.Push.sourceAuthority(ctx, input)
	if err != nil {
		return PreparedPlacementSource{}, err
	}
	if authority.ObjectKind != mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM {
		return PreparedPlacementSource{}, status.Error(codes.FailedPrecondition, "prepared source is not live media")
	}
	if authority.IngestMode == "push" {
		return runtime.Push.ResolvePreparedSource(ctx, receipts, input)
	}
	if !ConfiguredIngestMode(authority.IngestMode) {
		return PreparedPlacementSource{}, status.Error(codes.FailedPrecondition, "prepared source ingest mode is unsupported")
	}

	pull, found, err := runtime.Registry.CurrentInboundPull(ctx, input.InternalName, input.NodeID)
	if err != nil {
		return PreparedPlacementSource{}, err
	}
	if !found || pull.TenantID != input.TenantID || pull.DestClusterID != input.ClusterID || pull.DestNodeID != input.NodeID {
		return PreparedPlacementSource{}, status.Error(codes.FailedPrecondition, "prepared source has no current configured pull")
	}
	query := PlacementSourceReceiptQuery{
		TenantID: input.TenantID, InternalName: input.InternalName, ClusterID: input.ClusterID, NodeID: input.NodeID,
		TenantAuthorityVersion: authority.TenantAuthorityVersion, ObjectAuthorityVersion: authority.ObjectAuthorityVersion,
		Pull: PlacementPullBinding{AttemptID: pull.AttemptID, SourceCellID: pull.SourceClusterID, SourceClusterID: pull.SourceMediaClusterID,
			DestinationFence: input.DestinationFence, SourceNodeID: pull.SourceNodeID,
			SourceGeneration: pull.SourceGeneration, SourceRevision: pull.SourceRevision},
	}
	receipt, err := receipts.CompletedPull(ctx, query)
	if err != nil {
		return PreparedPlacementSource{}, err
	}
	if receipt.Request.GetQuery().GetObjectId() != input.ObjectID || !authority.MatchesQuery(receipt.Request.Query, runtime.CellID) {
		return PreparedPlacementSource{}, status.Error(codes.FailedPrecondition, "prepared configured source authority differs")
	}
	if err = runtime.Revalidate(ctx, receipt.Request, receipt); err != nil {
		return PreparedPlacementSource{}, err
	}
	if pull.SourceAcceptedAt.IsZero() || !runtime.now().Before(pull.SourceAcceptedAt.Add(3*time.Minute)) {
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
		if renewed == nil || renewed.AttemptID != pull.AttemptID ||
			control.SourcePullBaseURL(renewed.PullDTSCURL) != control.SourcePullBaseURL(pull.DTSCURL) {
			return PreparedPlacementSource{}, ErrPlacementReceiptConflict
		}
	}
	current, err := runtime.Push.sourceAuthority(ctx, input)
	if err != nil {
		return PreparedPlacementSource{}, err
	}
	if !reflect.DeepEqual(authority, current) {
		return PreparedPlacementSource{}, status.Error(codes.FailedPrecondition, "prepared configured source authority changed")
	}
	latest, found, err := runtime.Registry.CurrentInboundPull(ctx, input.InternalName, input.NodeID)
	if err != nil {
		return PreparedPlacementSource{}, err
	}
	if !found || !samePreparedSourcePull(pull, latest) {
		return PreparedPlacementSource{}, status.Error(codes.FailedPrecondition, "prepared configured source pull changed")
	}
	if _, err = receipts.CompletedPull(ctx, query); err != nil {
		return PreparedPlacementSource{}, err
	}
	fence, err := runtime.Push.currentDestinationFence(ctx, input.NodeID, input.ClusterID)
	if err != nil {
		return PreparedPlacementSource{}, err
	}
	if fence != input.DestinationFence {
		return PreparedPlacementSource{}, status.Error(codes.FailedPrecondition, "configured source connection changed")
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

// ResolveOrReauthorizeMediaSource renews permission from retained viewer context
// without treating that context as permission or assuming a publisher source.
func (destination *PlacementDestination) ResolveOrReauthorizeMediaSource(ctx context.Context, runtime *MediaServePreparationRuntime, input PlacementSourceIdentity) (PreparedPlacementSource, error) {
	if destination == nil || runtime == nil || destination.RetainPrepared == nil {
		return PreparedPlacementSource{}, status.Error(codes.Unavailable, "media source reauthorization is unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, err := runtime.ResolvePreparedSource(ctx, destination.Receipts, input)
	renewable := errors.Is(err, ErrPlacementReceiptMissing) || errors.Is(err, ErrPlacementReceiptExpired) ||
		errors.Is(err, ErrPlacementReceiptConflict) || status.Code(err) == codes.FailedPrecondition
	if err == nil || !renewable {
		return result, err
	}
	pull, found, err := runtime.Registry.CurrentInboundPull(ctx, input.InternalName, input.NodeID)
	if err != nil {
		return PreparedPlacementSource{}, err
	}
	if !found || pull.TenantID != input.TenantID || pull.DestClusterID != input.ClusterID || pull.PlacementDemand == "" || len(pull.PlacementDemand) > 65536 {
		return PreparedPlacementSource{}, status.Error(codes.FailedPrecondition, "source has no retained placement demand")
	}
	query := &placementpb.CandidateQuery{}
	if err = protojson.Unmarshal([]byte(pull.PlacementDemand), query); err != nil {
		return PreparedPlacementSource{}, ErrPlacementReceiptConflict
	}
	if query.TenantId != input.TenantID || query.ObjectId != input.ObjectID || query.InternalName != input.InternalName ||
		query.Verb != placementpb.Verb_VERB_SERVE || query.SourceGeneration != pull.SourceGeneration {
		return PreparedPlacementSource{}, ErrPlacementReceiptConflict
	}
	authority, err := runtime.Push.sourceAuthority(ctx, input)
	if err != nil {
		return PreparedPlacementSource{}, err
	}
	query.PolicyDigest, query.PolicyRevision, query.ParentRevision = authority.PolicyDigest, authority.PolicyRevision, authority.ParentRevision
	query.ClusterIds = nil
	for _, cell := range authority.Cells {
		if cell.ID == runtime.CellID {
			query.ClusterIds = slices.Clone(cell.ClusterIDs)
			break
		}
	}
	if !slices.Contains(query.ClusterIds, input.ClusterID) {
		return PreparedPlacementSource{}, status.Error(codes.PermissionDenied, "source destination is no longer granted")
	}
	fence, err := runtime.Push.currentDestinationFence(ctx, input.NodeID, input.ClusterID)
	if err != nil {
		return PreparedPlacementSource{}, err
	}
	if fence != input.DestinationFence {
		return PreparedPlacementSource{}, ErrPlacementReceiptConflict
	}
	now := destination.now()
	attempt, err := placement.NewPreparationAttemptID(now)
	if err != nil {
		return PreparedPlacementSource{}, err
	}
	until := minPlacementExpiry(now.Truncate(time.Millisecond).Add(placement.PreparationLifetime), authority.ExpiresAt)
	if _, err = destination.PreparePlacement(ctx, &placementpb.PreparePlacementRequest{
		Query: query, ClusterId: input.ClusterID, NodeId: input.NodeID, AttemptId: attempt, ExpiresAt: timestamppb.New(until),
	}); err != nil {
		return PreparedPlacementSource{}, err
	}
	result, err = runtime.ResolvePreparedSource(ctx, destination.Receipts, input)
	if err != nil {
		return PreparedPlacementSource{}, err
	}
	latest, found, latestErr := runtime.Registry.CurrentInboundPull(ctx, input.InternalName, input.NodeID)
	if latestErr != nil {
		return PreparedPlacementSource{}, latestErr
	}
	if !found {
		return PreparedPlacementSource{}, ErrPlacementReceiptConflict
	}
	if result.AttemptID != latest.AttemptID || result.DTSCURL != latest.DTSCURL {
		return PreparedPlacementSource{}, ErrPlacementReceiptConflict
	}
	return result, nil
}
