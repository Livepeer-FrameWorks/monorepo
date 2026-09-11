package demo

import (
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func demoPlacementRequest() *placementpb.PreviewRequest {
	return &placementpb.PreviewRequest{Scope: &placementpb.Scope{Kind: placementpb.ScopeKind_SCOPE_KIND_TENANT}, Verb: placementpb.Verb_VERB_SERVE,
		Coordinates: &placementpb.Coordinates{Latitude: 37.77, Longitude: -122.42}}
}

func TestDemoMediaPlacementPreviewEvaluatesRealRulesOnSyntheticFacts(t *testing.T) {
	for _, scenario := range []string{"nearest-us", "nearest-eu", "owned-only", "no-official", "deny-all", "geo-hole", "never-spill", "price-first", "live-source", "ingest-owner"} {
		t.Run(scenario, func(t *testing.T) {
			req := demoPlacementRequest()
			rules := &placement.Rules{SchemaVersion: placement.SchemaVersion}
			want := "cluster_demo_us_west"
			draft := true
			switch scenario {
			case "nearest-us":
				draft = false
			case "nearest-eu":
				draft = false
				req.Coordinates = &placementpb.Coordinates{Latitude: 52.37, Longitude: 4.90}
				want = "cluster_demo_eu_west"
			case "owned-only":
				rules.Constraints.Allow = &placement.SelectorSet{Any: []placement.Selector{{Classes: []placement.Class{placement.Private}}}}
				want = DemoSelfHostedCluster
			case "no-official":
				rules.Constraints.Deny = []placement.Selector{{Classes: []placement.Class{placement.Official}}}
				want = "demo-placement-marketplace"
			case "deny-all":
				rules.Constraints.Allow = &placement.SelectorSet{}
				want = ""
			case "geo-hole", "never-spill":
				group := placement.Group{ID: "own", Match: placement.Selector{Classes: []placement.Class{placement.Private}}, Spillover: placement.Never}
				if scenario == "geo-hole" {
					group.Spillover, group.GeoHoleDistanceKM, group.MinImprovementKM = placement.GeoHole, 2500, 500
				} else {
					want = DemoSelfHostedCluster
				}
				rules.Preferences = &placement.Preferences{Groups: []placement.Group{group, {ID: "fallback"}}}
			case "price-first":
				rules.Preferences = &placement.Preferences{Groups: []placement.Group{{ID: "cheap", Match: placement.Selector{Charging: []placement.Charging{placement.Rated}}, Order: placement.PriceFirst, PriceCurrency: "EUR", PriceUnit: "serve:minutes=1;gib=1"}}}
				want = "demo-placement-marketplace"
			case "live-source":
				draft = false
				req.StreamId, req.Protocol = DemoStreamID, "hls"
			case "ingest-owner":
				draft = false
				req.Verb, req.StreamId, req.Protocol = placementpb.Verb_VERB_INGEST, DemoStreamID, "whip"
				want = "cluster_demo_eu_west"
			}
			if draft {
				wire, err := placement.RulesToProto(rules)
				if err != nil {
					t.Fatal(err)
				}
				req.ExpectedRevision, req.ExpectedParentRevision = proto.Uint64(0), proto.Uint64(0)
				req.DraftUpdate = &placementpb.VerbUpdate{Verb: req.Verb, Kind: placementpb.UpdateKind_UPDATE_KIND_SET, Rules: wire}
			}
			before := proto.CloneOf(req)
			result, err := GenerateMediaPlacementPreview(req, time.Now().UTC())
			if err != nil || result.GetSelected().GetClusterId() != want || !strings.HasPrefix(result.GetReason(), "demo_simulated_") {
				t.Fatalf("demo decision differs: %+v, %v, want=%s", result, err, want)
			}
			if !proto.Equal(req, before) {
				t.Fatal("preview mutated draft")
			}
			if result.SourceEvaluated != (scenario == "live-source") || result.GetSelected().GetRequiresSourcePull() != (scenario == "live-source") {
				t.Fatal("capacity-only preview invented source readiness or source-aware preview lost its pull")
			}
			if scenario == "price-first" && (result.GetSelected().GetPrice().GetAmountMicros() != 60000 || result.GetSelected().GetPrice().GetRevision() != "demo-simulated") {
				t.Fatal("simulated comparable price missing")
			}
			if state, err := GenerateMediaPlacementPolicy(req.Scope); err != nil || state.GetOwn().GetRevision() != 0 || state.GetActive() != nil || state.GetActions().GetCanManage() {
				t.Fatal("preview changed saved or active policy")
			}
		})
	}
}

func TestDemoMediaPlacementRejectsRealScopesStaleDraftsAndUnknownProtocols(t *testing.T) {
	for _, scenario := range []string{"foreign-scope", "foreign-source", "stale", "inactive-source", "protocol", "invalid-coordinates"} {
		t.Run(scenario, func(t *testing.T) {
			req := demoPlacementRequest()
			switch scenario {
			case "foreign-scope":
				req.Scope = &placementpb.Scope{Kind: placementpb.ScopeKind_SCOPE_KIND_STREAM, StreamId: "real-stream"}
			case "foreign-source":
				req.StreamId = "real-stream"
			case "stale":
				req.ExpectedRevision = proto.Uint64(1)
			case "inactive-source":
				req.StreamId = "00000000-0000-0000-0000-000000000002"
			case "protocol":
				req.Protocol = "rtmp"
			case "invalid-coordinates":
				req.Coordinates.Latitude = 100
			}
			if result, err := GenerateMediaPlacementPreview(req, time.Now()); err == nil || result != nil {
				t.Fatal("invalid simulated preview succeeded")
			}
		})
	}
}

func TestDemoMediaPlacementOptionsPaginationAndFilterBinding(t *testing.T) {
	req := &placementpb.GetOptionsRequest{Scope: demoPlacementRequest().Scope, First: 1}
	ids := map[string]bool{}
	for {
		page, err := GenerateMediaPlacementOptions(req)
		if err != nil || len(page.GetNodes()) != 1 {
			t.Fatalf("demo page failed: %+v, %v", page, err)
		}
		id := page.Nodes[0].Id
		if ids[id] || !strings.HasPrefix(page.Nodes[0].Name, "Simulated") {
			t.Fatal("duplicate or unmarked demo option")
		}
		ids[id] = true
		if !page.HasNextPage {
			break
		}
		req.After = page.EndCursor
	}
	if len(ids) != 4 {
		t.Fatalf("options incomplete: %v", ids)
	}
	req.Filter = &placementpb.OptionsFilter{Classes: []placementpb.ClusterClass{placementpb.ClusterClass_CLUSTER_CLASS_TENANT_PRIVATE}}
	if _, err := GenerateMediaPlacementOptions(req); status.Code(err) != codes.InvalidArgument {
		t.Fatal("cursor crossed filters")
	}
	req.After = ""
	page, err := GenerateMediaPlacementOptions(req)
	if err != nil || len(page.Nodes) != 1 || page.Nodes[0].Id != DemoSelfHostedCluster {
		t.Fatal("private option filter failed")
	}
	for _, kind := range []placementpb.OptionKind{placementpb.OptionKind_OPTION_KIND_OPERATOR, placementpb.OptionKind_OPTION_KIND_REGION} {
		req.Filter, req.First = &placementpb.OptionsFilter{Kind: kind}, 100
		page, err = GenerateMediaPlacementOptions(req)
		if err != nil || len(page.Nodes) != 3 {
			t.Fatalf("aggregate options failed: %+v, %v", page, err)
		}
	}
}

func TestDemoMediaPlacementReviewIsNonMutatingAndNotAnApplyCredential(t *testing.T) {
	req := &placementpb.ReviewChangeRequest{Scope: demoPlacementRequest().Scope, Updates: []*placementpb.VerbUpdate{{Verb: placementpb.Verb_VERB_SERVE, Kind: placementpb.UpdateKind_UPDATE_KIND_SET,
		Rules: &placementpb.Rules{SchemaVersion: 1, Preferences: &placementpb.Preferences{}}}}}
	review, err := GenerateMediaPlacementReview(req, time.Now())
	if err != nil || len(review.GetDifferences()) == 0 || !strings.HasPrefix(review.GetReviewToken(), "demo-preview-only:") || review.GetImpact().GetComplete() || len(review.GetWarnings()) == 0 {
		t.Fatalf("review claims live state or lost intent: %+v, %v", review, err)
	}
	state, err := GenerateMediaPlacementPolicy(req.Scope)
	if err != nil || state.GetOwn().GetServe() != nil || state.GetActive() != nil {
		t.Fatal("review applied the draft")
	}
	state.Own.Serve = req.Updates[0].Rules
	next, err := GenerateMediaPlacementPolicy(req.Scope)
	if err != nil || next.Own.Serve != nil {
		t.Fatal("demo callers shared mutable policy state")
	}
}
