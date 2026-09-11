package grpc

import (
	"context"
	"slices"
	"testing"
	"time"

	"frameworks/api_control/internal/placementpolicy"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	clusterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func previewCapacityFixture(t *testing.T) (*mediaPlacementPreviewContext, *quartermasterpb.GetTenantEntitlementResponse, *mediaPreviewInventory) {
	t.Helper()
	const tenant = "10000000-0000-4000-8000-000000000091"
	peers := []*clusterpb.TenantClusterPeer{
		{ClusterId: "own-eu", ClusterName: "My EU", ControlCellId: "eu-cell", ClusterType: "edge", ClusterClass: "tenant_private", OwnerTenantId: tenant, RegionId: "eu", AccessSource: clusterpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_OWNER},
		{ClusterId: "official-us", ClusterName: "Official US", ControlCellId: "us-cell", ClusterType: "edge", ClusterClass: "platform_official", RegionId: "us", AccessSource: clusterpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PLATFORM_TIER},
	}
	entitlement := &quartermasterpb.GetTenantEntitlementResponse{}
	for _, peer := range peers {
		peer.AccessActive, peer.SubscriptionStatus = true, "active"
		peer.MediaConsent = &placementpb.CapacityConsent{AllowIngest: true, AllowServe: true, AllowExternalSource: true}
		entitlement.AllowedClusterIds = append(entitlement.AllowedClusterIds, peer.ClusterId)
		entitlement.EffectiveAccess = append(entitlement.EffectiveAccess, peer)
	}
	inventory, err := mediaPreviewEntitlement(tenant, entitlement, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return &mediaPlacementPreviewContext{snapshot: placementpolicy.Snapshot{Scope: placementpolicy.Scope{TenantID: tenant, Kind: "tenant", ID: tenant}}, verb: placement.Serve, protocol: "hls"}, entitlement, inventory
}

func previewCapacityResponse(t *testing.T, query *placementpb.CapacityPreviewQuery, inventory *mediaPreviewInventory) *placementpb.CapacityPreviewObservation {
	t.Helper()
	now := time.Now().UTC()
	response := &placementpb.CapacityPreviewObservation{TenantId: query.TenantId, ControlCellId: query.ControlCellId, ClusterIds: slices.Clone(query.ClusterIds), Verb: query.Verb, Protocol: query.Protocol, InternalName: query.InternalName, ObservedAt: timestamppb.New(now), ExpiresAt: timestamppb.New(now.Add(20 * time.Second)), Complete: true}
	response.ClusterConsents = map[string]*placementpb.CapacityConsent{}
	for _, id := range query.ClusterIds {
		peer := inventory.peers[id]
		response.ClusterConsents[id] = proto.CloneOf(peer.GetMediaConsent())
		location := &placement.Coordinates{Latitude: 52, Longitude: 5}
		if peer.RegionId == "us" {
			location = &placement.Coordinates{Latitude: 39, Longitude: -77}
		}
		candidate := placement.Candidate{TenantID: query.TenantId, ClusterID: id, NodeID: id + "-node", OwnerTenantID: peer.OwnerTenantId, Official: peer.ClusterClass == "platform_official", Region: peer.RegionId, AllowedVerbs: []placement.Verb{placement.Ingest, placement.Serve}, Location: location, ObservedAt: now, ExpiresAt: now.Add(20 * time.Second), Capacity: placement.CapacityAvailable, BWAvailable: 800, BWLimit: 1000, CPUPercent: 10, RAMUsed: 100, RAMMax: 1000}
		candidate.AllowedVerbs = nil
		if peer.GetMediaConsent().GetAllowIngest() {
			candidate.AllowedVerbs = append(candidate.AllowedVerbs, placement.Ingest)
		}
		if peer.GetMediaConsent().GetAllowServe() {
			candidate.AllowedVerbs = append(candidate.AllowedVerbs, placement.Serve)
		}
		wire, err := placement.CandidateToProto(candidate, placement.Serve)
		if query.Verb == placementpb.Verb_VERB_INGEST {
			wire, err = placement.CandidateToProto(candidate, placement.Ingest)
		}
		if err != nil {
			t.Fatal(err)
		}
		response.Candidates = append(response.Candidates, wire)
	}
	return response
}

func TestMediaPlacementPreviewCapacityRejectsUnboundCellEvidence(t *testing.T) {
	for _, scenario := range []string{"ok", "timeout", "cell", "owner", "region", "consent", "external consent", "consent revision", "class", "source", "price", "duplicate node", "incomplete"} {
		t.Run(scenario, func(t *testing.T) {
			preview, _, inventory := previewCapacityFixture(t)
			server := &CommodoreServer{placementCapacitySource: func(ctx context.Context, cluster string, query *placementpb.CapacityPreviewQuery) (*placementpb.CapacityPreviewObservation, error) {
				if cluster != query.ClusterIds[0] || query.TenantId != preview.snapshot.Scope.TenantID {
					t.Error("cell read escaped authorized scope")
				}
				response := previewCapacityResponse(t, query, inventory)
				if scenario == "duplicate node" {
					response.Candidates[0].NodeId = "duplicate"
				}
				if cluster != "own-eu" {
					return response, nil
				}
				switch scenario {
				case "external consent":
					response.ClusterConsents["own-eu"].AllowExternalSource = false
				case "consent revision":
					response.ClusterConsents["own-eu"].Revision++
				case "timeout":
					return nil, status.Error(codes.DeadlineExceeded, "offline")
				case "cell":
					response.ControlCellId = "other"
				case "owner":
					response.Candidates[0].OwnerTenantId = "other"
				case "region":
					response.Candidates[0].Region = "other"
				case "consent":
					response.Candidates[0].AllowedVerbs = nil
				case "class":
					response.Candidates[0].Official = true
				case "source":
					response.Candidates[0].SourceFeasible = true
				case "price":
					response.Candidates[0].CommercialFacts = &placementpb.CommercialFacts{}
				case "incomplete":
					response.Complete = false
				}
				return response, nil
			}}
			out, err := server.collectPreviewCapacity(context.Background(), preview, inventory, placementpb.Verb_VERB_SERVE)
			if scenario == "duplicate node" {
				if status.Code(err) != codes.Unavailable {
					t.Fatalf("duplicate node accepted: %v", err)
				}
				return
			}
			if err != nil || out.Complete != (scenario == "ok") {
				t.Fatalf("cell completeness incorrect: %+v %v", out, err)
			}
			want := 1
			if scenario == "ok" || scenario == "incomplete" {
				want = 2
			}
			if len(out.Candidates) != want {
				t.Fatalf("untrusted cell evidence survived: %+v", out)
			}
			preview.policy = &placement.Policy{SchemaVersion: 1, Groups: []placement.Group{{ID: "own", Match: placement.Selector{Classes: []placement.Class{placement.Private}}, Spillover: placement.CapacityOnly}, {ID: "fallback"}}}
			decision, err := placement.EvaluateCapacity(placement.Request{TenantID: preview.snapshot.Scope.TenantID, Verb: placement.Serve, Now: time.Now(), Policy: preview.policy, Candidates: out.Candidates, Complete: out.Complete})
			if err != nil {
				t.Fatal(err)
			}
			if scenario != "ok" && scenario != "incomplete" && len(decision.Choices) != 0 {
				t.Fatalf("unknown own cell permitted spill: %+v", decision)
			}
		})
	}
}

func TestMediaPlacementPreviewEntitlementCensusAndExpiry(t *testing.T) {
	for _, scenario := range []string{"missing", "duplicate", "cell", "expired", "owner", "provenance", "consent"} {
		t.Run(scenario, func(t *testing.T) {
			preview, entitlement, _ := previewCapacityFixture(t)
			switch scenario {
			case "missing":
				entitlement.EffectiveAccess = entitlement.EffectiveAccess[:1]
			case "duplicate":
				entitlement.AllowedClusterIds[1] = entitlement.AllowedClusterIds[0]
			case "cell":
				entitlement.EffectiveAccess[0].ControlCellId = ""
			case "expired":
				entitlement.EffectiveAccess[0].AccessExpiresAt = timestamppb.New(time.Now().Add(-time.Second))
			case "owner":
				entitlement.EffectiveAccess[0].OwnerTenantId = "10000000-0000-4000-8000-000000000092"
			case "provenance":
				entitlement.EffectiveAccess[1].ClusterClass = "third_party_marketplace"
			case "consent":
				entitlement.EffectiveAccess[0].MediaConsent = nil
			}
			if _, err := mediaPreviewEntitlement(preview.snapshot.Scope.TenantID, entitlement, time.Now()); status.Code(err) != codes.Unavailable {
				t.Fatalf("invalid entitlement accepted: %v", err)
			}
		})
	}
	preview, entitlement, _ := previewCapacityFixture(t)
	until := time.Now().Add(5 * time.Second)
	entitlement.EffectiveAccess[0].AccessExpiresAt = timestamppb.New(until)
	inventory, err := mediaPreviewEntitlement(preview.snapshot.Scope.TenantID, entitlement, time.Now())
	if err != nil || !inventory.expiresAt.Equal(until) {
		t.Fatalf("access expiry extended: %v", err)
	}
}
