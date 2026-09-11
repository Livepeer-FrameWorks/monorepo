package resolvers

import (
	"context"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/demo"
	"frameworks/api_gateway/internal/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func placementPreviewInput(in model.PreviewMediaPlacementInput) (*placementpb.PreviewRequest, error) {
	scope, err := placementScopeInput(in.Scope)
	if err != nil {
		return nil, err
	}
	if !in.Verb.IsValid() {
		return nil, fmt.Errorf("valid preview verb is required")
	}
	out := &placementpb.PreviewRequest{Scope: scope, Verb: placementpb.Verb_VERB_INGEST, StreamId: placementString(in.StreamID), Protocol: placementString(in.Protocol)}
	if in.Verb == model.MediaPlacementVerbServe {
		out.Verb = placementpb.Verb_VERB_SERVE
	}
	for _, revision := range []struct {
		input  *string
		target **uint64
	}{{in.ExpectedRevision, &out.ExpectedRevision}, {in.ExpectedParentRevision, &out.ExpectedParentRevision}} {
		if revision.input == nil {
			continue
		}
		parsed, parseErr := placementRevisionInput(*revision.input)
		if parseErr != nil {
			return nil, parseErr
		}
		*revision.target = proto.Uint64(parsed)
	}
	if geo := in.Coordinates; geo != nil {
		out.Coordinates = &placementpb.Coordinates{Latitude: geo.Latitude, Longitude: geo.Longitude}
	}
	if in.DraftUpdate != nil {
		if in.ExpectedRevision == nil || in.ExpectedParentRevision == nil {
			return nil, fmt.Errorf("draft preview requires both expected revisions")
		}
		review, reviewErr := placementReviewInput(model.ReviewMediaPlacementChangeInput{Scope: in.Scope, ExpectedRevision: *in.ExpectedRevision, ExpectedParentRevision: *in.ExpectedParentRevision, Updates: []*model.MediaPlacementVerbUpdateInput{in.DraftUpdate}})
		if reviewErr != nil {
			return nil, reviewErr
		}
		out.DraftUpdate = review.Updates[0]
	}
	if validationErr := placement.ValidatePreviewRequest(out); validationErr != nil {
		return nil, validationErr
	}
	return out, nil
}

func (r *Resolver) DoPreviewMediaPlacement(ctx context.Context, input model.PreviewMediaPlacementInput) (model.MediaPlacementPreviewResult, error) {
	if denied := r.placementAccess(ctx, false); denied != nil {
		return denied, nil
	}
	request, err := placementPreviewInput(input)
	if err != nil {
		return placementInvalidInput(err), nil
	}
	ctx, cancel := context.WithTimeout(ctx, 9*time.Second)
	defer cancel()
	var response *placementpb.Preview
	if middleware.IsDemoMode(ctx) {
		response, err = demo.GenerateMediaPlacementPreview(request, time.Now().UTC())
	} else {
		response, err = r.Clients.Commodore.PreviewMediaPlacement(ctx, request)
	}
	if err != nil {
		return placementPreviewFailure(err), nil
	}
	if ctx.Err() != nil {
		return placementPreviewFailure(status.FromContextError(ctx.Err()).Err()), nil
	}
	out, err := placementPreviewOutput(request, response, time.Now().UTC())
	if err != nil {
		return placementPreviewFailure(err), nil
	}
	return out, nil
}

func placementPreviewFailure(err error) placementAPIError {
	failure := placementFailure(err)
	if detail, ok := failure.(*model.MediaPlacementError); ok {
		switch status.Code(err) {
		case codes.Aborted:
			detail.Message = "Placement changed during preview. Reload the rules and preview again."
		case codes.InvalidArgument:
			detail.Message = "The preview input is invalid. Check the rules, stream and location."
		case codes.FailedPrecondition:
			detail.Code, detail.Message = model.MediaPlacementErrorCodeUnavailable, "Media placement preview is not currently available for this account."
		case codes.Unimplemented:
			detail.Message = "Preview is not yet supported for this media source."
		default:
			if detail.Code == model.MediaPlacementErrorCodeUnavailable {
				detail.Message = "Fresh placement evidence is unavailable. Try previewing again."
			}
		}
	}
	return failure
}

func placementPreviewOutput(request *placementpb.PreviewRequest, in *placementpb.Preview, now time.Time) (*model.MediaPlacementPreview, error) {
	invalid := fmt.Errorf("inconsistent placement preview response")
	if in == nil || proto.Size(in) > 8<<20 || !proto.Equal(in.GetScope(), request.GetScope()) || in.GetVerb() != request.GetVerb() || in.GetRevision() > math.MaxInt64 || in.GetParentRevision() > math.MaxInt64 || len(in.GetCandidates()) > 4096 || len(in.GetTransitions()) > 64 || in.GetReason() == "" || len(in.GetReason()) > 100 {
		return nil, invalid
	}
	if request.ExpectedRevision != nil && in.GetRevision() != *request.ExpectedRevision || request.ExpectedParentRevision != nil && in.GetParentRevision() != *request.ExpectedParentRevision || in.GetScope().GetKind() == placementpb.ScopeKind_SCOPE_KIND_TENANT && in.GetParentRevision() != 0 {
		return nil, invalid
	}
	digest, err := hex.DecodeString(in.GetDigest())
	if err != nil || len(digest) != 32 || hex.EncodeToString(digest) != in.GetDigest() {
		return nil, invalid
	}
	if in.GetObservedAt() == nil || !in.GetObservedAt().IsValid() || in.GetExpiresAt() == nil || !in.GetExpiresAt().IsValid() {
		return nil, invalid
	}
	observed, expiry := in.GetObservedAt().AsTime(), in.GetExpiresAt().AsTime()
	if observed.After(now.Add(time.Second)) || !now.Before(expiry) || !observed.Before(expiry) || expiry.After(observed.Add(30*time.Second)) {
		return nil, invalid
	}
	scope, err := placementScopeOutput(in.GetScope())
	if err != nil {
		return nil, err
	}
	out := &model.MediaPlacementPreview{Scope: scope, Verb: model.MediaPlacementVerbServe, Revision: strconv.FormatUint(in.GetRevision(), 10), ParentRevision: strconv.FormatUint(in.GetParentRevision(), 10), Digest: in.GetDigest(), Reason: in.GetReason(), Candidates: []*model.MediaPlacementCandidateExplanation{}, Transitions: []*model.MediaPlacementTransition{}, ObservedAt: observed, ExpiresAt: expiry, Complete: in.GetComplete(), SourceEvaluated: in.GetSourceEvaluated()}
	if in.GetVerb() == placementpb.Verb_VERB_INGEST {
		out.Verb = model.MediaPlacementVerbIngest
	}
	if id := in.GetActiveIngestClusterId(); id != "" {
		if in.GetVerb() != placementpb.Verb_VERB_INGEST || len(id) > 100 {
			return nil, invalid
		}
		out.ActiveIngestClusterID = strPtr(id)
	}
	convert := func(candidate *placementpb.PreviewCandidate) (*model.MediaPlacementCandidateExplanation, error) {
		if candidate == nil || candidate.GetClusterId() == "" || len(candidate.GetClusterId()) > 100 || candidate.GetClusterName() == "" || len(candidate.GetClusterName()) > 255 || candidate.GetReason() == "" || len(candidate.GetReason()) > 100 || len(candidate.GetNodeId()) > 100 || len(candidate.GetRegion()) > 100 || len(candidate.GetGroupId()) > 100 || candidate.GetRequiresSourcePull() && !in.GetSourceEvaluated() {
			return nil, invalid
		}
		row := &model.MediaPlacementCandidateExplanation{ClusterID: candidate.GetClusterId(), ClusterName: candidate.GetClusterName(), Reason: candidate.GetReason(), RequiresSourcePull: candidate.GetRequiresSourcePull()}
		for _, optional := range []struct {
			value  string
			target **string
		}{{candidate.GetNodeId(), &row.NodeID}, {candidate.GetRegion(), &row.Region}, {candidate.GetGroupId(), &row.GroupID}} {
			if optional.value != "" {
				*optional.target = strPtr(optional.value)
			}
		}
		if candidate.DistanceKm != nil {
			distance := candidate.GetDistanceKm()
			if math.IsNaN(distance) || math.IsInf(distance, 0) || distance < 0 {
				return nil, invalid
			}
			row.DistanceKm = &distance
		}
		if price := candidate.GetPrice(); price != nil {
			if price.GetCurrency() == "" || len(price.GetCurrency()) > 16 || price.GetUnit() == "" || len(price.GetUnit()) > 100 || price.GetRevision() == "" || len(price.GetRevision()) > 255 || price.GetExpiresAt() == nil || !price.GetExpiresAt().IsValid() || price.GetExpiresAt().AsTime().Before(expiry) {
				return nil, invalid
			}
			row.Price = &model.MediaPlacementPrice{AmountMicros: strconv.FormatUint(price.GetAmountMicros(), 10), Currency: price.GetCurrency(), Unit: price.GetUnit(), Revision: price.GetRevision(), ExpiresAt: price.GetExpiresAt().AsTime()}
		}
		return row, nil
	}
	if in.GetSelected() != nil {
		out.Selected, err = convert(in.GetSelected())
		if err != nil {
			return nil, err
		}
	}
	for _, candidate := range in.GetCandidates() {
		row, convertErr := convert(candidate)
		if convertErr != nil {
			return nil, convertErr
		}
		out.Candidates = append(out.Candidates, row)
	}
	for _, transition := range in.GetTransitions() {
		if transition == nil || transition.GetFromGroup() == "" || len(transition.GetFromGroup()) > 100 || transition.GetReason() == "" || len(transition.GetReason()) > 100 {
			return nil, invalid
		}
		out.Transitions = append(out.Transitions, &model.MediaPlacementTransition{FromGroup: transition.GetFromGroup(), Reason: transition.GetReason()})
	}
	return out, nil
}
