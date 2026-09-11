package grpc

import (
	"testing"

	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/protobuf/proto"
)

func TestMediaPlacementReviewContextBindsOwnerConsent(t *testing.T) {
	authority := &mediaauthoritypb.TenantAuthority{SchemaVersion: 1, TenantId: "tenant"}
	entitlement := &quartermasterpb.GetTenantEntitlementResponse{
		AllowedClusterIds: []string{"b", "a"},
		EffectiveAccess: []*clusterpeerpb.TenantClusterPeer{
			{ClusterId: "b", MediaConsent: &placementpb.CapacityConsent{Revision: 4, AllowIngest: true, AllowServe: true, AllowExternalSource: true}},
			{ClusterId: "a", MediaConsent: &placementpb.CapacityConsent{AllowIngest: true, AllowServe: true, AllowExternalSource: true}},
		},
	}
	before := proto.CloneOf(entitlement)
	digest, err := mediaPlacementContextDigest(authority, entitlement)
	if err != nil || len(digest) != 64 || !proto.Equal(before, entitlement) {
		t.Fatalf("context snapshot: %s, %v", digest, err)
	}
	for name, mutate := range map[string]func(*quartermasterpb.GetTenantEntitlementResponse){
		"revision": func(v *quartermasterpb.GetTenantEntitlementResponse) { v.EffectiveAccess[0].MediaConsent.Revision++ },
		"ingest": func(v *quartermasterpb.GetTenantEntitlementResponse) {
			v.EffectiveAccess[0].MediaConsent.AllowIngest = false
		},
		"serve": func(v *quartermasterpb.GetTenantEntitlementResponse) {
			v.EffectiveAccess[0].MediaConsent.AllowServe = false
		},
		"external source": func(v *quartermasterpb.GetTenantEntitlementResponse) {
			v.EffectiveAccess[0].MediaConsent.AllowExternalSource = false
		},
		"observation missing": func(v *quartermasterpb.GetTenantEntitlementResponse) { v.EffectiveAccess[1].MediaConsent = nil },
	} {
		changed := proto.CloneOf(entitlement)
		mutate(changed)
		if got, err := mediaPlacementContextDigest(authority, changed); err != nil || got == digest {
			t.Fatalf("%s not review-bound: %s, %v", name, got, err)
		}
	}
	reordered := proto.CloneOf(entitlement)
	reordered.AllowedClusterIds[0], reordered.AllowedClusterIds[1] = reordered.AllowedClusterIds[1], reordered.AllowedClusterIds[0]
	reordered.EffectiveAccess[0], reordered.EffectiveAccess[1] = reordered.EffectiveAccess[1], reordered.EffectiveAccess[0]
	if got, err := mediaPlacementContextDigest(authority, reordered); err != nil || got != digest {
		t.Fatalf("RPC row order changed review: %s, %v", got, err)
	}
}

func TestMediaPlacementReviewWarningConsequences(t *testing.T) {
	denyAll := &placementpb.PolicySet{Serve: &placementpb.Rules{Constraints: &placementpb.Constraints{Deny: []*placementpb.Selector{{}}}}}
	warnings := mediaPlacementWarnings(nil, denyAll)
	assertWarning := func(id string) {
		t.Helper()
		if len(warnings) != 1 || warnings[0].GetId() != id || !warnings[0].GetAcknowledgementRequired() {
			t.Fatalf("expected required warning %s, got %v", id, warnings)
		}
	}
	assertWarning("serve_deny_all")
	if got := mediaPlacementWarnings(denyAll, denyAll); len(got) != 0 {
		t.Fatalf("unchanged rules produced warnings: %v", got)
	}

	restricted := &placementpb.PolicySet{Ingest: &placementpb.Rules{Constraints: &placementpb.Constraints{Allow: &placementpb.SelectorSet{Any: []*placementpb.Selector{{ClusterIds: []string{"owned-cluster"}}}}}}}
	wider := proto.Clone(restricted).(*placementpb.PolicySet)
	wider.Ingest.Constraints.Allow.Any = append(wider.Ingest.Constraints.Allow.Any, &placementpb.Selector{ClusterIds: []string{"official-cluster"}})
	warnings = mediaPlacementWarnings(restricted, wider)
	assertWarning("ingest_restriction_removed")

	fallback := &placementpb.PolicySet{Serve: &placementpb.Rules{Preferences: &placementpb.Preferences{Groups: []*placementpb.Group{{Id: "preferred", Spillover: placementpb.Spillover_SPILLOVER_GEO_HOLE, GeoHoleDistanceKm: 1000}}}}}
	looser := proto.Clone(fallback).(*placementpb.PolicySet)
	looser.Serve.Preferences.Groups[0].GeoHoleDistanceKm = 200
	warnings = mediaPlacementWarnings(fallback, looser)
	assertWarning("serve_fallback_changed")
}
