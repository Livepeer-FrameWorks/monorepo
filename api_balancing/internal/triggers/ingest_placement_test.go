package triggers

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/federation"
	localauthority "frameworks/api_balancing/internal/mediaauthority"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
)

type ingestFenceFunc func(context.Context, federation.PlacementIngestIdentity) (string, error)

func (f ingestFenceFunc) ActiveIngestCluster(ctx context.Context, identity federation.PlacementIngestIdentity) (string, error) {
	return f(ctx, identity)
}

func TestConfigureLiveIngestPlacementAdmissionUsesPublicRuntime(t *testing.T) {
	for _, missing := range []string{"none", "runtime", "gate", "gate-cell", "authority", "fence", "router", "internal-name-lookup", "already-installed"} {
		t.Run(missing, func(t *testing.T) {
			processor := newTestProcessor(t)
			observe := func(context.Context, balancer.PlacementCell, balancer.PlacementRouteRequest) (balancer.PlacementCellObservation, error) {
				return balancer.PlacementCellObservation{}, errors.New("unused")
			}
			gate := &federation.PlacementPolicyGate{CellID: "cell", Authority: viewerPlacementPairReader{}, IngestFence: ingestFenceFunc(func(context.Context, federation.PlacementIngestIdentity) (string, error) { return "", nil }),
				Router: balancer.PlacementRouter{Observe: observe}}
			runtime := &federation.LivePublicPlacementRuntime{Gate: gate, Source: &federation.MediaPlacementPaths{Push: &federation.LivePushPlacementPaths{CellID: "cell"}}}
			switch missing {
			case "runtime":
				runtime = nil
			case "gate":
				runtime.Gate = nil
			case "gate-cell":
				gate.CellID = ""
			case "authority":
				gate.Authority = nil
			case "fence":
				gate.IngestFence = nil
			case "router":
				gate.Router.Observe = nil
			case "internal-name-lookup":
				gate.Authority = placementOnlyReader{}
			case "already-installed":
				processor.SetIngestPlacementAdmission(func(context.Context, IngestPlacementConnection) (federation.PlacementAdmissionDecision, error) {
					return federation.PlacementAdmissionDecision{}, errors.New("existing")
				})
			}
			err := processor.ConfigureLiveIngestPlacementAdmission(runtime, nil, nil)
			if missing == "none" {
				if err != nil || !processor.ingestPlacementRequired || processor.ingestPlacementAdmission == nil {
					t.Fatalf("startup did not install ingest admission: %v", err)
				}
				return
			}
			if err == nil || (missing != "already-installed" && (processor.ingestPlacementRequired || processor.ingestPlacementAdmission != nil)) {
				t.Fatalf("incomplete startup installed ingest admission: %v", err)
			}
		})
	}
}

func TestCheckIngestPlacementRequiresBoundDecision(t *testing.T) {
	for _, scenario := range []string{"never-installed", "allow", "unavailable", "missing-connector", "unsupported-connector", "wrong-protocol", "wrong-verb", "wrong-node", "wrong-tenant", "expired", "too-long", "missing-digest", "denied"} {
		t.Run(scenario, func(t *testing.T) {
			p := newTestProcessor(t)
			connector := "TSSRT"
			calls := 0
			if scenario != "never-installed" {
				p.SetIngestPlacementAdmission(func(_ context.Context, connection IngestPlacementConnection) (federation.PlacementAdmissionDecision, error) {
					calls++
					if scenario == "denied" {
						return federation.PlacementAdmissionDecision{}, errors.New("policy denied")
					}
					decision := federation.PlacementAdmissionDecision{TenantID: connection.TenantID, ObjectID: "live_stream:stream", InternalName: connection.InternalName,
						ClusterID: connection.ClusterID, NodeID: connection.NodeID, Protocol: "srt", Verb: placement.Ingest, PolicyDigest: strings.Repeat("ab", 32),
						PolicyRevision: 1, ParentRevision: 1, TenantAuthorityVersion: 1, ObjectAuthorityVersion: 1, ExpiresAt: time.Now().Add(5 * time.Second)}
					switch scenario {
					case "wrong-protocol":
						decision.Protocol = "rtmp"
					case "wrong-verb":
						decision.Verb = placement.Serve
					case "wrong-node":
						decision.NodeID = "other"
					case "wrong-tenant":
						decision.TenantID = "other"
					case "expired":
						decision.ExpiresAt = time.Now().Add(-time.Second)
					case "too-long":
						decision.ExpiresAt = time.Now().Add(time.Minute)
					case "missing-digest":
						decision.PolicyDigest = ""
					}
					return decision, nil
				})
			}
			switch scenario {
			case "unavailable":
				p.SetIngestPlacementAdmission(nil)
			case "missing-connector":
				connector = ""
			case "unsupported-connector":
				connector = "DTSC"
			}
			decision, err := p.checkIngestPlacement(context.Background(), "tenant", "stream", "cluster", "node", connector, "203.0.113.5")
			switch scenario {
			case "never-installed":
				// Publisher admission has no unenforced mode: without an
				// installed adapter the claim is denied, never waved through.
				if err == nil || decision != nil || calls != 0 {
					t.Fatalf("uninstalled admission admitted a publisher: %v %v", decision, err)
				}
			case "allow":
				if err != nil || decision == nil || decision.Protocol != "srt" || calls != 1 {
					t.Fatalf("bound decision rejected: %v %v", decision, err)
				}
			case "missing-connector", "unsupported-connector", "unavailable":
				if err == nil || calls != 0 {
					t.Fatalf("%s reached the adapter: %v calls=%d", scenario, err, calls)
				}
			default:
				if err == nil || decision != nil {
					t.Fatalf("%s admitted: %v", scenario, decision)
				}
			}
		})
	}
}

func TestPushRewriteDeniedByPlacementReleasesItsClaim(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(map[bool]string{false: "denied", true: "allowed-then-rejected-later"}[allowed], func(t *testing.T) {
			sm := state.ResetDefaultManagerForTests()
			t.Cleanup(func() { state.ResetDefaultManagerForTests() })
			installRegistryForTest(t)
			mock := installControlDBForTest(t)
			p, fake := processorWithPlacementCommodore(t)
			sm.SetNodeConnectionInfo(context.Background(), "node-A", "node-A:18090", "", "demo-media", nil)
			mock.ExpectQuery(`FROM foghorn\.ingest_sessions`).WillReturnError(sql.ErrNoRows)
			claimAcquired.Store(true)
			t.Cleanup(func() { claimAcquired.Store(false) })
			var calls atomic.Int32
			var seen IngestPlacementConnection
			p.SetIngestPlacementAdmission(func(_ context.Context, connection IngestPlacementConnection) (federation.PlacementAdmissionDecision, error) {
				calls.Add(1)
				seen = connection
				if !allowed {
					return federation.PlacementAdmissionDecision{}, errors.New("private-only policy has no capacity here")
				}
				return federation.PlacementAdmissionDecision{TenantID: connection.TenantID, ObjectID: "live_stream:stream-1", InternalName: connection.InternalName,
					ClusterID: connection.ClusterID, NodeID: connection.NodeID, Protocol: "rtmp", Verb: placement.Ingest, PolicyDigest: strings.Repeat("cd", 32),
					PolicyRevision: 1, ParentRevision: 1, TenantAuthorityVersion: 1, ObjectAuthorityVersion: 1, ExpiresAt: time.Now().Add(5 * time.Second)}, nil
			})
			_, _, err := p.handlePushRewrite(&ipcpb.MistTrigger{
				NodeId: "node-A",
				TriggerPayload: &ipcpb.MistTrigger_PushRewrite{PushRewrite: &ipcpb.PushRewriteTrigger{
					Pid: 4242, TriggerUuid: "uuid-placement", TriggerUnixMillis: 1,
					StreamName: "sk-abc", Hostname: "127.0.0.1", PushUrl: "srt://spoofed/app", ObservedConnector: "RTMP",
				}},
			})
			if err == nil {
				t.Fatal("publisher was admitted")
			}
			if calls.Load() != 1 || seen.TenantID != "tenant-1" || seen.InternalName != "internal-1" || seen.ClusterID != "demo-media" || seen.NodeID != "node-A" || seen.Connector != "RTMP" || seen.PublisherAddress != "127.0.0.1" {
				t.Fatalf("placement admission saw the wrong connection: calls=%d %+v", calls.Load(), seen)
			}
			if mentions := strings.Contains(err.Error(), "placement policy"); mentions == allowed {
				t.Fatalf("allowed=%t but error = %v", allowed, err)
			}
			reqs := fake.requests()
			if len(reqs) != 1 || len(reqs[0].GetRelease()) != 1 || reqs[0].GetClusterId() != "demo-media" {
				t.Fatalf("rejected publish did not release its claim exactly once: %+v", reqs)
			}
		})
	}
}

func TestIngestPlacementAdapterAdmitsFromObservedConnectorOnly(t *testing.T) {
	now := time.Now()
	pair := localauthority.PlacementPair{
		Tenant: localauthority.TenantSnapshot{Version: 1, Ready: true, IngestReady: true, ValidUntil: now.Add(20 * time.Second), Authority: &mediapb.TenantAuthority{
			SchemaVersion: 2, TenantId: "tenant", Lifecycle: mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE, BillingDecision: mediapb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW,
			MediaPlacement: &pb.PolicySet{Revision: 1, Ingest: &pb.Rules{SchemaVersion: 1, Preferences: &pb.Preferences{Groups: []*pb.Group{
				{Id: "own", Match: &pb.Selector{ClusterIds: []string{"eu"}}, Spillover: pb.Spillover_SPILLOVER_CAPACITY_ONLY},
				{Id: "fallback", Match: &pb.Selector{ClusterIds: []string{"test-cluster"}}},
			}}}},
			EffectiveClusterGrants: []*mediapb.TenantClusterGrant{
				{ClusterId: "eu", ControlCellId: "eu-cell", ClusterClass: "tenant_private", OwnerTenantId: "tenant", SubscriptionStatus: "active", MediaConsent: &pb.CapacityConsent{AllowServe: true, AllowIngest: true}},
				{ClusterId: "test-cluster", ControlCellId: "us-cell", ClusterClass: "platform_official", OwnerTenantId: "platform", SubscriptionStatus: "active", MediaConsent: &pb.CapacityConsent{AllowServe: true, AllowIngest: true}},
			}}},
		Object: localauthority.MediaObjectSnapshot{Version: 1, AuthorityID: "live_stream:stream", Ready: true, IngestReady: true, ValidUntil: now.Add(20 * time.Second), Authority: &mediapb.MediaObjectAuthority{
			SchemaVersion: 2, TenantId: "tenant", InternalName: "stream", Lifecycle: mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
			ObjectKind: mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM, MediaPlacement: &pb.PolicySet{}, PlacementTenantRevision: 1,
			Object: &mediapb.MediaObjectAuthority_LiveStream{LiveStream: &mediapb.LiveStreamAuthority{StreamId: "stream", IngestMode: "push"}}}},
	}
	for _, scenario := range []struct {
		name, connector, cluster string
		preferredEmpty           bool
		wantAllowed              bool
	}{
		{"observed rtmp on preferred cluster", "RTMP", "eu", false, true},
		{"observed whip on preferred cluster", "WebRTC", "eu", false, true},
		{"observed srt spills only when preferred is empty", "TSSRT", "test-cluster", true, true},
		{"observed srt refused while preferred has capacity", "TSSRT", "test-cluster", false, false},
		{"node-to-node connector is not a publisher", "DTSC", "eu", false, false},
		{"missing attestation", "", "eu", false, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			cellID := map[string]string{"eu": "eu-cell", "test-cluster": "us-cell"}[scenario.cluster]
			gate := &federation.PlacementPolicyGate{CellID: cellID, Authority: viewerPlacementPairReader{pair: pair},
				IngestFence: ingestFenceFunc(func(context.Context, federation.PlacementIngestIdentity) (string, error) { return "", nil }),
				Router: balancer.PlacementRouter{Observe: func(_ context.Context, cell balancer.PlacementCell, _ balancer.PlacementRouteRequest) (balancer.PlacementCellObservation, error) {
					observed := time.Now()
					result := balancer.PlacementCellObservation{Complete: true, ObservedAt: observed, ExpiresAt: observed.Add(10 * time.Second)}
					if cell.ID == "eu-cell" && scenario.preferredEmpty {
						return result, nil
					}
					result.Candidates = []placement.Candidate{{TenantID: "tenant", ClusterID: cell.ClusterIDs[0], NodeID: "node", OwnerTenantID: "tenant", AllowedVerbs: []placement.Verb{placement.Ingest, placement.Serve},
						ObservedAt: observed, ExpiresAt: result.ExpiresAt, Capacity: placement.CapacityAvailable, BWAvailable: 100, BWLimit: 1000, RAMUsed: 1, RAMMax: 100, Presence: placement.Present, SourceFeasible: true}}
					return result, nil
				}}}
			adapter := &IngestPlacementAdapter{Authority: viewerPlacementPairReader{pair: pair}, Gate: gate}
			decision, err := adapter.AdmitPublisher(context.Background(), IngestPlacementConnection{TenantID: "tenant", InternalName: "stream", ClusterID: scenario.cluster, NodeID: "node", Connector: scenario.connector, PublisherAddress: "203.0.113.9"})
			if (err == nil) != scenario.wantAllowed {
				t.Fatalf("allowed=%t err=%v", err == nil, err)
			}
			if scenario.wantAllowed && (decision.Verb != placement.Ingest || decision.ClusterID != scenario.cluster || decision.NodeID != "node") {
				t.Fatalf("decision not bound to the publisher: %+v", decision)
			}
		})
	}
}
