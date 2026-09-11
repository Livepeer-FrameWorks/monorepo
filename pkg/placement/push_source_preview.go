package placement

import (
	"fmt"
	"math"
	"time"

	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/proto"
)

func ValidatePushSourcePreviewQuery(req *placementpb.PushSourcePreviewQuery) error {
	if req == nil || proto.Size(req) > 4096 {
		return fmt.Errorf("bounded source preview query is required")
	}
	if err := rejectUnknownWire(req); err != nil {
		return err
	}
	for _, id := range []string{req.GetTenantId(), req.GetControlCellId(), req.GetClusterId()} {
		if !validReviewID(id) || len(id) > 100 {
			return fmt.Errorf("source preview scope is invalid")
		}
	}
	if !validReviewID(req.GetInternalName()) {
		return fmt.Errorf("source preview stream is required")
	}
	return nil
}

func ValidatePushSourcePreviewObservation(req *placementpb.PushSourcePreviewQuery, response *placementpb.PushSourcePreviewObservation, now time.Time) error {
	if err := ValidatePushSourcePreviewQuery(req); err != nil {
		return err
	}
	if response == nil || proto.Size(response) > 8192 || !proto.Equal(response.GetScope(), req) || now.IsZero() {
		return fmt.Errorf("source preview scope differs")
	}
	if err := rejectUnknownWire(response); err != nil {
		return err
	}
	if !validReviewID(response.GetNodeId()) || len(response.GetNodeId()) > 100 || !validReviewID(response.GetGeneration()) || response.GetRevision() == 0 || response.GetRevision() > math.MaxInt64 {
		return fmt.Errorf("source preview publisher is invalid")
	}
	if response.GetConsent() == nil || !response.GetConsent().GetAllowIngest() || response.GetConsent().GetRevision() > math.MaxInt64 {
		return fmt.Errorf("source preview consent is invalid")
	}
	observed, expiry := response.GetObservedAt(), response.GetExpiresAt()
	if observed == nil || expiry == nil || !observed.IsValid() || !expiry.IsValid() || observed.AsTime().After(now) || !now.Before(expiry.AsTime()) || !observed.AsTime().Before(expiry.AsTime()) || expiry.AsTime().After(observed.AsTime().Add(30*time.Second)) {
		return fmt.Errorf("source preview observation is stale")
	}
	return nil
}
