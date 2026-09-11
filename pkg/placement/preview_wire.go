package placement

import (
	"fmt"
	"math"
	"strings"

	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/proto"
)

func ValidatePreviewRequest(req *placementpb.PreviewRequest) error {
	if req == nil || req.GetScope() == nil || proto.Size(req) > 64<<10 {
		return fmt.Errorf("bounded preview request and scope are required")
	}
	if err := rejectUnknownWire(req); err != nil {
		return err
	}
	scope := req.GetScope()
	switch scope.GetKind() {
	case placementpb.ScopeKind_SCOPE_KIND_TENANT:
		if scope.GetStreamId() != "" || req.GetExpectedParentRevision() != 0 {
			return fmt.Errorf("tenant preview has no parent scope")
		}
	case placementpb.ScopeKind_SCOPE_KIND_STREAM:
		if !validReviewID(scope.GetStreamId()) || req.GetStreamId() != "" && req.GetStreamId() != scope.GetStreamId() {
			return fmt.Errorf("stream preview must use its own stream")
		}
	default:
		return fmt.Errorf("unsupported preview scope")
	}
	if req.GetStreamId() != "" && !validReviewID(req.GetStreamId()) {
		return fmt.Errorf("invalid preview stream")
	}
	if req.GetVerb() != placementpb.Verb_VERB_INGEST && req.GetVerb() != placementpb.Verb_VERB_SERVE {
		return fmt.Errorf("unsupported preview verb")
	}
	if req.GetProtocol() != "" && (!validReviewID(req.GetProtocol()) || len(req.GetProtocol()) > 32 || strings.ToLower(req.GetProtocol()) != req.GetProtocol()) {
		return fmt.Errorf("preview protocol must be canonical")
	}
	if req.GetExpectedRevision() > math.MaxInt64 || req.GetExpectedParentRevision() > math.MaxInt64 {
		return fmt.Errorf("preview revision exceeds bounds")
	}
	if geo := req.GetCoordinates(); geo != nil && !validCoordinates(&Coordinates{Latitude: geo.GetLatitude(), Longitude: geo.GetLongitude()}) {
		return fmt.Errorf("invalid hypothetical coordinates")
	}
	if update := req.GetDraftUpdate(); update != nil {
		if req.ExpectedRevision == nil || req.ExpectedParentRevision == nil || update.GetVerb() != req.GetVerb() {
			return fmt.Errorf("draft preview requires exact revisions and matching verb")
		}
		if _, err := ApplyUpdates(nil, []*placementpb.VerbUpdate{update}); err != nil {
			return err
		}
	}
	return nil
}
