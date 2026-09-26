package federation

import (
	"context"
	"fmt"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// PolicyBoundPlacementRuntime enforces current global policy around physical
// media reconciliation. The media implementation owns exact source/pull and
// readiness evidence; it cannot substitute another destination or extend policy.
type PolicyBoundPlacementRuntime struct {
	Policy *PlacementPolicyGate
	Media  PlacementPreparationRuntime
}

var _ PlacementPreparationRuntime = (*PolicyBoundPlacementRuntime)(nil)

func (runtime *PolicyBoundPlacementRuntime) Revalidate(ctx context.Context, req *placementpb.PreparePlacementRequest, receipt PlacementReceipt) error {
	if runtime == nil || runtime.Policy == nil {
		return status.Error(codes.Unavailable, "placement policy enforcement is unavailable")
	}
	assessment, err := runtime.Policy.Assess(ctx, req)
	if err != nil {
		return err
	}
	var shortened error
	if receipt.Response != nil {
		if err = placement.ValidatePreparationResponse(req, receipt.Response, runtime.Policy.now()); err != nil {
			return err
		}
		if receipt.Response.TenantAuthorityVersion != assessment.TenantAuthorityVersion || receipt.Response.ObjectAuthorityVersion != assessment.ObjectAuthorityVersion {
			return status.Error(codes.FailedPrecondition, "placement authority versions changed after preparation")
		}
		if assessment.Outcome != placementpb.PreparationOutcome_PREPARATION_OUTCOME_ACCEPTED && receipt.Response.Outcome != assessment.Outcome {
			return status.Error(codes.FailedPrecondition, "placement outcome no longer matches current policy evidence")
		}
		if responseExpiry := receipt.Response.GetExpiresAt().AsTime(); responseExpiry.After(assessment.ExpiresAt) {
			shortened = &PlacementEvidenceShortenedError{Prepared: responseExpiry, Until: assessment.ExpiresAt}
		}
	}
	if assessment.Outcome != placementpb.PreparationOutcome_PREPARATION_OUTCOME_ACCEPTED {
		// A fresh negative decision needs no media operation. Retain any earlier
		// physical binding for exact cleanup/reconciliation; never erase it here.
		return shortened
	}
	if runtime.Media == nil {
		return status.Error(codes.Unavailable, "placement media enforcement is unavailable")
	}
	if err = runtime.Media.Revalidate(ctx, req, receipt); err != nil {
		return err
	}
	return shortened
}

// PlacementEvidenceShortenedError reports that the current policy evidence
// still accepts the preparation but expires before the preparation does. Each
// assessment is an independent observation, so a later one can legitimately
// end sooner. An unpersisted preparation takes the shorter lifetime; a
// persisted receipt is immutable and is refused.
type PlacementEvidenceShortenedError struct {
	Prepared time.Time
	Until    time.Time
}

func (err *PlacementEvidenceShortenedError) Error() string {
	return fmt.Sprintf("placement evidence expires at %s, before the preparation's %s", err.Until.UTC().Format(time.RFC3339Nano), err.Prepared.UTC().Format(time.RFC3339Nano))
}

// GRPCStatus keeps the refusal of a persisted receipt a FailedPrecondition.
func (err *PlacementEvidenceShortenedError) GRPCStatus() *status.Status {
	return status.New(codes.FailedPrecondition, "placement outcome no longer matches current policy evidence: "+err.Error())
}

func (runtime *PolicyBoundPlacementRuntime) Reconcile(ctx context.Context, req *placementpb.PreparePlacementRequest, receipt PlacementReceipt, bind func(*PlacementPullBinding) error) (*placementpb.Preparation, error) {
	if runtime == nil || runtime.Policy == nil {
		return nil, status.Error(codes.Unavailable, "placement policy enforcement is unavailable")
	}
	assessment, err := runtime.Policy.Assess(ctx, req)
	if err != nil {
		return nil, err
	}
	if assessment.Outcome != placementpb.PreparationOutcome_PREPARATION_OUTCOME_ACCEPTED {
		return &placementpb.Preparation{
			Outcome: assessment.Outcome, TenantId: req.Query.TenantId, ObjectId: req.Query.ObjectId, SourceGeneration: req.Query.SourceGeneration,
			ClusterId: req.ClusterId, NodeId: req.NodeId, Protocol: req.Query.Protocol, PolicyRevision: req.Query.PolicyRevision,
			ParentRevision: req.Query.ParentRevision, PolicyDigest: req.Query.PolicyDigest, AttemptId: req.AttemptId,
			ExpiresAt:              timestamppb.New(assessment.ExpiresAt),
			TenantAuthorityVersion: assessment.TenantAuthorityVersion, ObjectAuthorityVersion: assessment.ObjectAuthorityVersion,
		}, nil
	}
	if runtime.Media == nil {
		return nil, status.Error(codes.Unavailable, "placement media enforcement is unavailable")
	}
	if err = runtime.Media.Revalidate(ctx, clonePreparationRequest(req), clonePlacementReceipt(receipt)); err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	if !runtime.Policy.now().Before(assessment.ExpiresAt) {
		return nil, status.Error(codes.FailedPrecondition, "placement policy expired before media preparation")
	}
	response, err := runtime.Media.Reconcile(ctx, req, receipt, bind)
	if err != nil || response == nil {
		return response, err
	}
	if err = placement.ValidatePreparationResponse(req, response, runtime.Policy.now()); err != nil {
		return nil, err
	}
	response = proto.CloneOf(response)
	response.TenantAuthorityVersion = assessment.TenantAuthorityVersion
	response.ObjectAuthorityVersion = assessment.ObjectAuthorityVersion
	if response.GetExpiresAt().AsTime().After(assessment.ExpiresAt) {
		response.ExpiresAt = timestamppb.New(assessment.ExpiresAt)
	}
	return response, nil
}
