package triggers

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/federation"
	localauthority "frameworks/api_balancing/internal/mediaauthority"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
)

func TestUserNewPlacementPrecedesViewerSideEffects(t *testing.T) {
	for _, scenario := range []string{"allow", "deny", "metadata query", "unavailable", "expired", "wrong node", "wrong tenant", "wrong verb", "wrong protocol", "missing digest", "too long", "missing tenant authority", "missing object authority", "negative authority"} {
		t.Run(scenario, func(t *testing.T) {
			resetStateTrigHandlers(t)
			p := minimalProcessorTrigHandlers(t)
			p.streamCache.Set("tenant:stream", streamContext{TenantID: "tenant", MaxViewers: 1, RequiresAuthKnown: true}, time.Minute)
			calls := 0
			p.SetViewerPlacementAdmission(func(ctx context.Context, connection ViewerPlacementConnection) (federation.PlacementAdmissionDecision, error) {
				calls++
				if connection != (ViewerPlacementConnection{TenantID: "tenant", InternalName: "stream", ClusterID: "test-cluster", NodeID: "node", Connector: "HLS", ClientAddress: "203.0.113.1"}) {
					t.Fatalf("incorrect trusted connection: %+v", connection)
				}
				if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 3*time.Second {
					t.Fatal("unbounded viewer placement call")
				}
				if state.DefaultTenantCapacity().CountViewers("tenant") != 0 {
					t.Fatal("viewer capacity mutated before placement")
				}
				decision := federation.PlacementAdmissionDecision{TenantID: "tenant", ObjectID: "live_stream:stream", InternalName: "stream", ClusterID: "test-cluster", NodeID: "node", Protocol: "hls",
					SourceGeneration: "source", PolicyDigest: strings.Repeat("a", 64), PolicyRevision: 3, ParentRevision: 2, Verb: placement.Serve, ExpiresAt: time.Now().Add(10 * time.Second),
					TenantAuthorityVersion: 12, ObjectAuthorityVersion: 13}
				switch scenario {
				case "missing tenant authority":
					decision.TenantAuthorityVersion = 0
				case "missing object authority":
					decision.ObjectAuthorityVersion = 0
				case "negative authority":
					decision.TenantAuthorityVersion, decision.ObjectAuthorityVersion = -1, -1
				case "deny", "metadata query":
					return decision, errors.New("policy denies destination")
				case "expired":
					decision.ExpiresAt = time.Now().Add(-time.Second)
				case "wrong node":
					decision.NodeID = "other"
				case "wrong tenant":
					decision.TenantID = "other"
				case "wrong verb":
					decision.Verb = placement.Ingest
				case "wrong protocol":
					decision.Protocol = "webrtc"
				case "missing digest":
					decision.PolicyDigest = ""
				case "too long":
					decision.ExpiresAt = time.Now().Add(time.Minute)
				}
				return decision, nil
			})
			if scenario == "unavailable" {
				p.SetViewerPlacementAdmission(nil)
			}
			trigger := &ipcpb.MistTrigger{NodeId: "node", TenantId: ptrTrigHandlers("tenant"), ClusterId: ptrTrigHandlers("test-cluster"), TriggerPayload: &ipcpb.MistTrigger_ViewerConnect{
				ViewerConnect: &ipcpb.ViewerConnectTrigger{StreamName: "live+stream", Connector: "HLS", Host: "203.0.113.1", SessionId: "session", RequestUrl: "https://edge/hls/stream/index.m3u8?policy_revision=999"}}}
			if scenario == "metadata query" {
				trigger.GetViewerConnect().RequestUrl += "&poster=.jpg&metaeverywhere=1"
			}
			decision, abort, err := p.handleUserNew(trigger)
			if err != nil || abort {
				t.Fatalf("unexpected trigger error: %v", err)
			}
			want, count := "false", 0
			if scenario == "allow" {
				want, count = "true", 1
			}
			if decision != want || state.DefaultTenantCapacity().CountViewers("tenant") != count {
				t.Fatalf("incorrect admission/capacity: %s count=%d", decision, state.DefaultTenantCapacity().CountViewers("tenant"))
			}
			if (calls == 0) != (scenario == "unavailable") {
				t.Fatalf("public marker skipped placement: calls=%d", calls)
			}
			if want == "false" && trigger.GetViewerConnect().NodeId != nil {
				t.Fatal("denied viewer reached enrichment side effects")
			}
		})
	}
}

type viewerPlacementPairReader struct{ pair localauthority.PlacementPair }

func (r viewerPlacementPairReader) Placement(context.Context, string, string, string) (localauthority.PlacementPair, error) {
	return r.pair, nil
}

func (r viewerPlacementPairReader) PlacementForInternalName(context.Context, string, string) (localauthority.PlacementPair, error) {
	return r.pair, nil
}

type viewerPlacementRegistry struct{ entry control.StreamEntry }

func (r viewerPlacementRegistry) SourceSnapshot(_ context.Context, tenant, internal string) (control.StreamEntry, bool, error) {
	return r.entry, r.entry.TenantID == tenant && r.entry.InternalName == internal, nil
}

func TestUserNewUsesGlobalPlacementGate(t *testing.T) {
	for _, preferred := range []string{"available", "empty", "unreachable"} {
		t.Run(preferred, func(t *testing.T) {
			resetStateTrigHandlers(t)
			p := minimalProcessorTrigHandlers(t)
			p.streamCache.Set("tenant:stream", streamContext{TenantID: "tenant", MaxViewers: 1, RequiresAuthKnown: true}, time.Minute)
			now := time.Now()
			pair := localauthority.PlacementPair{
				Tenant: localauthority.TenantSnapshot{Version: 1, Ready: true, ValidUntil: now.Add(20 * time.Second), Authority: &mediapb.TenantAuthority{
					SchemaVersion: 2, TenantId: "tenant", Lifecycle: mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE, BillingDecision: mediapb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW,
					MediaPlacement: &pb.PolicySet{Revision: 1, Serve: &pb.Rules{SchemaVersion: 1, Preferences: &pb.Preferences{Groups: []*pb.Group{
						{Id: "own", Match: &pb.Selector{ClusterIds: []string{"eu"}}, Spillover: pb.Spillover_SPILLOVER_CAPACITY_ONLY},
						{Id: "fallback", Match: &pb.Selector{ClusterIds: []string{"test-cluster"}}},
					}}}},
					EffectiveClusterGrants: []*mediapb.TenantClusterGrant{
						{ClusterId: "eu", ControlCellId: "eu-cell", ClusterClass: "tenant_private", OwnerTenantId: "tenant", SubscriptionStatus: "active", MediaConsent: &pb.CapacityConsent{AllowServe: true, AllowIngest: true}},
						{ClusterId: "test-cluster", ControlCellId: "us-cell", ClusterClass: "platform_official", OwnerTenantId: "platform", SubscriptionStatus: "active", MediaConsent: &pb.CapacityConsent{AllowServe: true}},
					}}},
				Object: localauthority.MediaObjectSnapshot{Version: 1, AuthorityID: "live_stream:stream", Ready: true, ValidUntil: now.Add(20 * time.Second), Authority: &mediapb.MediaObjectAuthority{
					SchemaVersion: 2, TenantId: "tenant", InternalName: "stream", Lifecycle: mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
					ObjectKind: mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM, MediaPlacement: &pb.PolicySet{}, PlacementTenantRevision: 1,
					Object: &mediapb.MediaObjectAuthority_LiveStream{LiveStream: &mediapb.LiveStreamAuthority{StreamId: "stream", IngestMode: "push"}}}},
			}
			gate := &federation.PlacementPolicyGate{CellID: "us-cell", Authority: viewerPlacementPairReader{pair: pair}, Router: balancer.PlacementRouter{
				Observe: func(_ context.Context, cell balancer.PlacementCell, _ balancer.PlacementRouteRequest) (balancer.PlacementCellObservation, error) {
					observed := time.Now()
					result := balancer.PlacementCellObservation{Complete: true, ObservedAt: observed, ExpiresAt: observed.Add(10 * time.Second)}
					if cell.ID == "eu-cell" {
						if preferred == "unreachable" {
							return result, errors.New("preferred cell unreachable")
						}
						if preferred == "empty" {
							return result, nil
						}
					}
					result.Candidates = []placement.Candidate{{TenantID: "tenant", ClusterID: cell.ClusterIDs[0], NodeID: "node", OwnerTenantID: "tenant", AllowedVerbs: []placement.Verb{placement.Serve},
						ObservedAt: observed, ExpiresAt: result.ExpiresAt, Capacity: placement.CapacityAvailable, BWAvailable: 100, BWLimit: 1000, RAMUsed: 1, RAMMax: 100, Presence: placement.Present, SourceFeasible: true}}
					return result, nil
				}}}
			source := &federation.LivePushPlacementPaths{CellID: "us-cell", Snapshot: func() *state.BalancerSnapshot { return &state.BalancerSnapshot{} }, Registry: viewerPlacementRegistry{
				entry: control.StreamEntry{TenantID: "tenant", InternalName: "stream", Locations: map[string]control.Location{
					"eu-cell": {IsLiveNow: true, AdTimestamp: now.Unix(), EdgeCandidates: []control.EdgeCandidate{{NodeID: "publisher", ClusterID: "eu", IsOrigin: true,
						BufferState: "FULL", Playable: true, SourceGeneration: "source", SourceRevision: 1, SourceObservedAt: now.Unix()}}},
				}},
			}}
			adapter := &ViewerPlacementAdapter{Authority: viewerPlacementPairReader{pair: pair}, Source: source, Gate: gate}
			p.SetViewerPlacementAdmission(adapter.AdmitViewer)
			trigger := &ipcpb.MistTrigger{NodeId: "node", TenantId: ptrTrigHandlers("tenant"), ClusterId: ptrTrigHandlers("test-cluster"), TriggerPayload: &ipcpb.MistTrigger_ViewerConnect{
				ViewerConnect: &ipcpb.ViewerConnectTrigger{StreamName: "live+stream", Connector: "HLS", Host: "203.0.113.1", SessionId: "session", RequestUrl: "https://edge/hls/stream/index.m3u8"}}}
			decision, _, err := p.handleUserNew(trigger)
			if err != nil || (decision == "true") != (preferred == "empty") || (state.DefaultTenantCapacity().CountViewers("tenant") == 1) != (preferred == "empty") {
				t.Fatalf("USER_NEW bypassed global policy: %s %v", decision, err)
			}
		})
	}
}
