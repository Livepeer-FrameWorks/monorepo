package grpc

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func previewSourceResponse(query *placementpb.PushSourcePreviewQuery) *placementpb.PushSourcePreviewObservation {
	now := time.Now().UTC()
	return &placementpb.PushSourcePreviewObservation{Scope: proto.CloneOf(query), NodeId: query.ClusterId + "-node", Generation: "generation", Revision: 9, PullAvailable: true, ObservedAt: timestamppb.New(now), ExpiresAt: timestamppb.New(now.Add(10 * time.Second)), Consent: &placementpb.CapacityConsent{AllowIngest: true, AllowServe: true, AllowExternalSource: true}}
}

func TestMediaPlacementPreviewSourceCollectionBindsOwner(t *testing.T) {
	for _, scenario := range []string{"ok", "timeout", "scope", "consent", "expired", "no claim", "denied", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			preview, _, inventory := previewCapacityFixture(t)
			preview.internalName, preview.sourceCluster = "stream", "own-eu"
			if scenario == "no claim" {
				preview.sourceCluster = ""
			}
			if scenario == "denied" {
				inventory.peers["own-eu"].MediaConsent.AllowIngest = false
			}
			calls := 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			server := &CommodoreServer{placementPushSource: func(callCtx context.Context, cluster string, query *placementpb.PushSourcePreviewQuery) (*placementpb.PushSourcePreviewObservation, error) {
				calls++
				if cluster != "own-eu" || query.ClusterId != cluster || query.ControlCellId != "eu-cell" || query.InternalName != "stream" || query.TenantId != preview.snapshot.Scope.TenantID {
					t.Error("source owner scope lost")
				}
				if deadline, ok := callCtx.Deadline(); !ok || time.Until(deadline) > 4*time.Second {
					t.Error("source deadline missing")
				}
				response := previewSourceResponse(query)
				switch scenario {
				case "consent":
					response.Consent.Revision++
				case "timeout":
					return nil, status.Error(codes.DeadlineExceeded, "offline")
				case "scope":
					response.Scope.ClusterId = "other"
				case "expired":
					response.ExpiresAt = timestamppb.New(time.Now().Add(-time.Second))
				case "cancelled":
					cancel()
				}
				return response, nil
			}}
			out := server.collectPreviewPushSource(ctx, preview, inventory)
			if (out != nil) != (scenario == "ok") {
				t.Fatalf("invalid source accepted: %+v", out)
			}
			if (scenario == "no claim" || scenario == "denied") && calls != 0 {
				t.Fatal("unowned source was inspected")
			}
		})
	}
}

func TestMediaPlacementPreviewSourcePathsRespectConsentAndMissingEvidence(t *testing.T) {
	for _, scenario := range []string{"cold US", "missing", "external denied", "no listener", "same cluster", "deny origin destination", "ingest-only source"} {
		t.Run(scenario, func(t *testing.T) {
			preview, _, inventory := previewCapacityFixture(t)
			if scenario == "ingest-only source" {
				inventory.peers["own-eu"].MediaConsent.AllowServe = false
			}
			server := &CommodoreServer{placementCapacitySource: func(_ context.Context, _ string, query *placementpb.CapacityPreviewQuery) (*placementpb.CapacityPreviewObservation, error) {
				return previewCapacityResponse(t, query, inventory), nil
			}}
			capacity, err := server.collectPreviewCapacity(context.Background(), preview, inventory, placementpb.Verb_VERB_SERVE)
			if err != nil {
				t.Fatal(err)
			}
			source := previewSourceResponse(&placementpb.PushSourcePreviewQuery{TenantId: preview.snapshot.Scope.TenantID, ControlCellId: "eu-cell", ClusterId: "own-eu", InternalName: "stream"})
			source.Consent = proto.CloneOf(inventory.peers["own-eu"].GetMediaConsent())
			request := placement.Request{TenantID: preview.snapshot.Scope.TenantID, Verb: placement.Serve, Now: time.Now().UTC(), Location: &placement.Coordinates{Latitude: 39, Longitude: -77}, Candidates: capacity.Candidates, Complete: true}
			switch scenario {
			case "missing":
				source = nil
			case "external denied":
				inventory.peers["official-us"].MediaConsent.AllowExternalSource = false
			case "no listener":
				source.PullAvailable = false
			case "same cluster":
				source.NodeId = "another-own-node"
				inventory.peers["own-eu"].MediaConsent.AllowExternalSource = false
				inventory.peers["official-us"].MediaConsent.AllowExternalSource = false
			case "deny origin destination":
				request.Policy = &placement.Policy{SchemaVersion: 1, Layers: []placement.Constraints{{Deny: []placement.Selector{{ClusterIDs: []string{"own-eu"}}}}}, Groups: []placement.Group{{ID: "entitled"}}}
			}
			before := append([]placement.Candidate(nil), request.Candidates...)
			out, err := evaluateMediaPreview(request, true, source, inventory)
			if err != nil || !reflect.DeepEqual(before, request.Candidates) {
				t.Fatalf("source preview failed or mutated input: %+v %v", out, err)
			}
			if scenario == "missing" {
				if out.Complete || out.sourceEvaluated || len(out.Choices) != 0 {
					t.Fatalf("unknown source became a playback destination: %+v", out)
				}
				return
			}
			want := "official-us"
			pull := true
			if scenario == "external denied" || scenario == "no listener" || scenario == "same cluster" {
				want = "own-eu"
				pull = scenario == "same cluster"
			}
			if !out.Complete || !out.sourceEvaluated || len(out.Choices) == 0 || out.Choices[0].ClusterID != want || out.requiresPull[out.Choices[0].NodeID+"\x00"+out.Choices[0].GroupID] != pull {
				t.Fatalf("source path or destination consent incorrect: %+v", out)
			}
		})
	}
}
