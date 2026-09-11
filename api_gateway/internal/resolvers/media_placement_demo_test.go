package resolvers

import (
	"context"
	"strings"
	"testing"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/demo"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
)

func TestPlacementDemoAllSurfacesStayOfflineWithOrWithoutRealCredentials(t *testing.T) {
	for _, authenticated := range []bool{false, true} {
		t.Run(map[bool]string{false: "anonymous", true: "real-credentials"}[authenticated], func(t *testing.T) {
			ctx := context.Background()
			if authenticated {
				ctx = placementAPITestContext()
			}
			ctx = context.WithValue(ctx, ctxkeys.KeyDemoMode, true)
			ctx = context.WithValue(ctx, ctxkeys.KeyReadOnly, true)
			r := &Resolver{}
			scope := model.MediaPlacementScopeInput{Kind: model.MediaPlacementScopeKindTenant}
			result, err := r.DoMediaPlacementPolicy(ctx, scope)
			policy, ok := result.(*model.MediaPlacementPolicyState)
			if err != nil || !ok || policy.Actions.CanManage || !policy.Actions.CanPreview || policy.ActiveRevision != nil {
				t.Fatalf("demo policy missing or claimed live enforcement: %+v, %v", result, err)
			}
			options, err := r.DoMediaPlacementOptions(ctx, scope, nil, nil, nil)
			if _, validOptions := options.(*model.MediaPlacementOptionsConnection); err != nil || !validOptions {
				t.Fatalf("demo options failed: %+v, %v", options, err)
			}
			preview, err := r.DoPreviewMediaPlacement(ctx, model.PreviewMediaPlacementInput{Scope: &scope, Verb: model.MediaPlacementVerbServe,
				StreamID: strPtr(demo.DemoStreamID), Coordinates: &model.MediaPlacementCoordinatesInput{Latitude: 37.77, Longitude: -122.42}})
			observation, ok := preview.(*model.MediaPlacementPreview)
			if err != nil || !ok || observation.Selected == nil || observation.Selected.ClusterID != "cluster_demo_us_west" || !observation.Selected.RequiresSourcePull || !strings.HasPrefix(observation.Reason, "demo_simulated_") {
				t.Fatalf("demo preview failed conversion: %+v, %v", preview, err)
			}
			input := model.ReviewMediaPlacementChangeInput{Scope: &scope, ExpectedRevision: "0", ExpectedParentRevision: "0",
				Updates: []*model.MediaPlacementVerbUpdateInput{{Verb: model.MediaPlacementVerbServe, Kind: model.MediaPlacementUpdateKindClear}}}
			review, err := r.DoReviewMediaPlacementChange(ctx, input)
			if _, validReview := review.(*model.MediaPlacementReview); err != nil || !validReview {
				t.Fatalf("demo review failed: %+v, %v", review, err)
			}
			consent, err := r.DoClusterMediaConsent(ctx, demo.DemoSelfHostedCluster)
			consentState, ok := consent.(*model.MediaCapacityConsent)
			if err != nil || !ok || consentState.CanManage {
				t.Fatalf("demo consent failed: %+v, %v", consent, err)
			}
			consentReview, err := r.DoReviewClusterMediaConsentChange(ctx, model.ReviewMediaCapacityConsentInput{ClusterID: demo.DemoSelfHostedCluster, ExpectedRevision: "0"})
			if _, validConsentReview := consentReview.(*model.MediaPlacementReview); err != nil || !validConsentReview {
				t.Fatalf("demo consent review failed: %+v, %v", consentReview, err)
			}
			pins, err := r.DoMediaPlacementLegacyPins(ctx, demo.DemoStreamID)
			if _, validPins := pins.(*model.MediaPlacementLegacyPins); err != nil || !validPins {
				t.Fatalf("demo legacy pins failed: %+v, %v", pins, err)
			}
			applied, err := r.DoApplyMediaPlacementChange(ctx, model.ApplyMediaPlacementChangeInput{})
			failure, ok := applied.(*model.MediaPlacementError)
			if err != nil || !ok || failure.Code != model.MediaPlacementErrorCodeUnsupported || !strings.Contains(failure.Message, "no changes are saved") {
				t.Fatalf("demo apply pretended to save: %+v, %v", applied, err)
			}
			appliedConsent, err := r.DoApplyClusterMediaConsentChange(ctx, model.ApplyMediaCapacityConsentInput{})
			if failure, ok := appliedConsent.(*model.MediaPlacementError); err != nil || !ok || failure.Code != model.MediaPlacementErrorCodeUnsupported {
				t.Fatalf("demo consent pretended to save: %+v, %v", appliedConsent, err)
			}
			change, err := r.DoMediaPlacementChange(ctx, scope, "demo-change")
			if _, ok := change.(*model.NotFoundError); err != nil || !ok {
				t.Fatalf("demo manufactured saved receipt: %+v, %v", change, err)
			}
			consentChange, err := r.DoClusterMediaConsentChange(ctx, demo.DemoSelfHostedCluster, "demo-change")
			if _, ok := consentChange.(*model.NotFoundError); err != nil || !ok {
				t.Fatalf("demo manufactured consent receipt: %+v, %v", consentChange, err)
			}
		})
	}
}

func TestPlacementDemoRejectsCanceledReadsAndUnknownScopes(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyDemoMode, true)
	r := &Resolver{}
	result, err := r.DoMediaPlacementPolicy(ctx, model.MediaPlacementScopeInput{Kind: model.MediaPlacementScopeKindStream, StreamID: strPtr("real-private-stream")})
	if _, ok := result.(*model.NotFoundError); err != nil || !ok {
		t.Fatalf("demo resolved a real scope: %+v, %v", result, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	result, err = r.DoMediaPlacementPolicy(canceled, model.MediaPlacementScopeInput{Kind: model.MediaPlacementScopeKindTenant})
	if _, ok := result.(*model.MediaPlacementError); err != nil || !ok {
		t.Fatalf("canceled demo read succeeded: %+v, %v", result, err)
	}
}
