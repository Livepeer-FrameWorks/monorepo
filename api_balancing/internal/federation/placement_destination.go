package federation

import (
	"context"
	"errors"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// PlacementPreparationRuntime owns current admission and physical media state.
// Revalidate must check current policy, entitlement, owner consent, membership,
// protocol, capacity and source identity. A cached Ready response additionally
// requires current generation-bound media evidence; a receipt is not that evidence.
// Conditional spillover requires the complete policy-relevant census, not a
// destination-only re-score that treats an unobserved preferred pool as empty.
type PlacementPreparationRuntime interface {
	Revalidate(context.Context, *placementpb.PreparePlacementRequest, PlacementReceipt) error
	// Reconcile must check current admission and inspect physical state even
	// for a fresh receipt; it does not rely on a prior Revalidate call. It binds
	// the actual pull before starting it and serializes physical changes across
	// replicas. A pending receipt never licenses an unconditional second start.
	Reconcile(context.Context, *placementpb.PreparePlacementRequest, PlacementReceipt, func(*PlacementPullBinding) error) (*placementpb.Preparation, error)
}

// PlacementDestination composes discovery and receipt-backed preparation for
// both local and federated callers. Missing runtime enforcement fails closed.
type PlacementDestination struct {
	Discovery *PlacementDiscovery
	Receipts  *PlacementReceiptStore
	Runtime   PlacementPreparationRuntime
	// RetainPrepared records non-authorizing source request context after the
	// completed receipt is durable. A failed write must be retried on replay.
	RetainPrepared func(context.Context, *placementpb.PreparePlacementRequest, PlacementReceipt) error
	Now            func() time.Time
}

var _ PlacementRPC = (*PlacementDestination)(nil)

func (destination *PlacementDestination) QueryPlacementCandidates(ctx context.Context, request *placementpb.CandidateQuery) (*placementpb.CandidateObservation, error) {
	if destination == nil || destination.Discovery == nil {
		return nil, status.Error(codes.Unavailable, "placement discovery is not ready")
	}
	return destination.Discovery.QueryPlacementCandidates(ctx, request)
}

func (destination *PlacementDestination) PreparePlacement(ctx context.Context, request *placementpb.PreparePlacementRequest) (*placementpb.Preparation, error) {
	if destination == nil || destination.Discovery == nil || destination.Receipts == nil || destination.Runtime == nil ||
		destination.Discovery.CellID == "" || destination.Discovery.CellID != destination.Receipts.CellID {
		return nil, status.Error(codes.Unavailable, "placement preparation is not ready")
	}
	if err := placement.ValidatePreparationDeadline(request, destination.now()); err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid placement preparation request")
	}
	// Local callers receive the same deadline bound as the federation RPC.
	deadline := time.Now().Add(5 * time.Second)
	if request.GetExpiresAt().AsTime().Before(deadline) {
		deadline = request.GetExpiresAt().AsTime()
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	req, ok := proto.Clone(request).(*placementpb.PreparePlacementRequest)
	if !ok {
		return nil, status.Error(codes.Internal, "placement request could not be copied")
	}
	receipt, err := destination.Receipts.Begin(ctx, req)
	if err != nil {
		return nil, err
	}
	if receipt.Response != nil {
		if err = destination.revalidate(ctx, req, receipt); err != nil {
			return nil, err
		}
		// The runtime check may span an engine change or expiry. Reconfirm the
		// exact persisted outcome against Redis time without renewing retention.
		if err = destination.Receipts.Finish(ctx, req, receipt.Response, receipt.Pull); err != nil {
			return nil, err
		}
		if err = destination.retainPrepared(ctx, req, receipt); err != nil {
			return nil, err
		}
		if err = ctx.Err(); err != nil {
			return nil, status.FromContextError(err).Err()
		}
		return receipt.Response, nil
	}

	// Bind uses the immutable stored request, not a runtime-owned argument.
	// Finish compares the recorded pull again, including after ambiguous work.
	response, err := destination.Runtime.Reconcile(ctx, clonePreparationRequest(req), clonePlacementReceipt(receipt), func(pull *PlacementPullBinding) error {
		if contextErr := ctx.Err(); contextErr != nil {
			return status.FromContextError(contextErr).Err()
		}
		return destination.Receipts.BindPull(ctx, req, pull)
	})
	if err != nil {
		return nil, err
	}
	if err = placement.ValidatePreparationResponse(req, response, destination.now()); err != nil {
		return nil, status.Error(codes.FailedPrecondition, "invalid placement runtime acknowledgement")
	}
	// Re-read after work: another replica may have completed the same attempt.
	// Its outcome is immutable and cannot be replaced by this caller's result.
	receipt, err = destination.Receipts.Begin(ctx, req)
	if err != nil {
		return nil, err
	}
	if receipt.Response != nil && !proto.Equal(receipt.Response, response) {
		return nil, ErrPlacementReceiptConflict
	}
	receipt.Response = response
	var shortened *PlacementEvidenceShortenedError
	if err = destination.revalidate(ctx, req, receipt); errors.As(err, &shortened) {
		// Not persisted yet: take the shorter lifetime current evidence supports.
		response = proto.CloneOf(response)
		response.ExpiresAt = timestamppb.New(shortened.Until)
		receipt.Response = response
		err = destination.checkPreparationWindow(req, receipt)
	}
	if err != nil {
		return nil, err
	}
	if err = destination.Receipts.Finish(ctx, req, response, receipt.Pull); err != nil {
		return nil, err
	}
	if err = destination.retainPrepared(ctx, req, receipt); err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	return response, nil
}

func (destination *PlacementDestination) retainPrepared(ctx context.Context, req *placementpb.PreparePlacementRequest, receipt PlacementReceipt) error {
	if destination.RetainPrepared != nil {
		if err := destination.RetainPrepared(ctx, clonePreparationRequest(req), clonePlacementReceipt(receipt)); err != nil {
			return err
		}
		return destination.Receipts.Finish(ctx, req, receipt.Response, receipt.Pull)
	}
	return nil
}

func (destination *PlacementDestination) revalidate(ctx context.Context, req *placementpb.PreparePlacementRequest, receipt PlacementReceipt) error {
	if err := ctx.Err(); err != nil {
		return status.FromContextError(err).Err()
	}
	if err := destination.Runtime.Revalidate(ctx, clonePreparationRequest(req), clonePlacementReceipt(receipt)); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return status.FromContextError(err).Err()
	}
	return destination.checkPreparationWindow(req, receipt)
}

func (destination *PlacementDestination) checkPreparationWindow(req *placementpb.PreparePlacementRequest, receipt PlacementReceipt) error {
	if err := placement.ValidatePreparationDeadline(req, destination.now()); err != nil {
		return ErrPlacementReceiptExpired
	}
	if receipt.Response != nil {
		if err := placement.ValidatePreparationResponse(req, receipt.Response, destination.now()); err != nil {
			return ErrPlacementReceiptExpired
		}
	}
	return nil
}

func (destination *PlacementDestination) now() time.Time {
	if destination.Now != nil {
		return destination.Now().UTC()
	}
	return time.Now().UTC()
}

func clonePreparationRequest(req *placementpb.PreparePlacementRequest) *placementpb.PreparePlacementRequest {
	if cloned, ok := proto.Clone(req).(*placementpb.PreparePlacementRequest); ok {
		return cloned
	}
	return nil
}

func clonePlacementReceipt(receipt PlacementReceipt) PlacementReceipt {
	copy := receipt
	copy.Request = clonePreparationRequest(receipt.Request)
	if receipt.Response != nil {
		if cloned, ok := proto.Clone(receipt.Response).(*placementpb.Preparation); ok {
			copy.Response = cloned
		}
	}
	if receipt.Pull != nil {
		pull := *receipt.Pull
		copy.Pull = &pull
	}
	return copy
}
