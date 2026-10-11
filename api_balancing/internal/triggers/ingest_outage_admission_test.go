package triggers

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/federation"
	"frameworks/api_balancing/internal/ingesterrors"
	localauthority "frameworks/api_balancing/internal/mediaauthority"
	"frameworks/api_balancing/internal/state"
	"github.com/DATA-DOG/go-sqlmock"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type placementStreamContextDown struct{ reads *int }

func (down placementStreamContextDown) ResolveStreamContext(context.Context, string, string, string, string) (*commodorepb.ResolveStreamContextResponse, error) {
	*down.reads++
	return nil, status.Error(codes.Unavailable, "commodore is down")
}

// outagePlacementPair is the placement view of the local authority fixture:
// the same tenant, stream and signed outage owner cluster-a in cell-a.
func outagePlacementPair(now time.Time) localauthority.PlacementPair {
	return localauthority.PlacementPair{
		Tenant: localauthority.TenantSnapshot{Version: 8, Ready: true, IngestReady: true, ValidUntil: now.Add(time.Hour), Authority: &mediapb.TenantAuthority{
			SchemaVersion: sharedauthority.PlacementSchemaVersion, TenantId: "10000000-0000-0000-0000-000000000001",
			Lifecycle: mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE, BillingDecision: mediapb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW,
			MediaPlacement: &pb.PolicySet{Revision: 1},
			EffectiveClusterGrants: []*mediapb.TenantClusterGrant{{ClusterId: "cluster-a", ControlCellId: "cell-a", ClusterClass: "platform_official",
				OwnerTenantId: "platform", SubscriptionStatus: "active", MediaConsent: &pb.CapacityConsent{AllowServe: true, AllowIngest: true}}},
		}},
		Object: localauthority.MediaObjectSnapshot{Version: 4, AuthorityID: sharedauthority.LiveStreamAuthorityID("30000000-0000-0000-0000-000000000001"),
			Ready: true, IngestReady: true, ValidUntil: now.Add(time.Hour), Authority: &mediapb.MediaObjectAuthority{
				SchemaVersion: sharedauthority.PlacementSchemaVersion, TenantId: "10000000-0000-0000-0000-000000000001", InternalName: "stream-internal",
				Lifecycle: mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE, ObjectKind: mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM,
				MediaPlacement: &pb.PolicySet{Revision: 1}, PlacementTenantRevision: 1,
				Object: &mediapb.MediaObjectAuthority_LiveStream{LiveStream: &mediapb.LiveStreamAuthority{
					StreamId: "30000000-0000-0000-0000-000000000001", IngestMode: "push", OutageIngestClusterId: "cluster-a"}}}},
	}
}

// With Commodore unreachable, a publisher on the signed outage owner cluster is
// admitted by the real placement gate. PUSH_REWRITE hands the verified outage
// owner to admission, so the gate does not ask Commodore for ownership again.
func TestPushRewrite_CommodoreDown_OutageOwnerAdmits(t *testing.T) {
	installIngestSessionMintMockWithStream(t)
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	sm.SetNodeInfo("edge-node-1", "http://edge.example/view", true, nil, nil, "", "", nil)
	sm.SetNodeConnectionInfo(context.Background(), "edge-node-1", "edge-node-1:18090", "", "cluster-a", nil)
	previousRegistry := control.StreamRegistryInstance
	control.SetStreamRegistry(control.NewStreamRegistry(nil, "cluster-a", time.Minute))
	t.Cleanup(func() { control.SetStreamRegistry(previousRegistry) })

	p, mock, closeDB, tenantBytes, objectBytes := localAuthorityFixture(t)
	defer closeDB()
	validUntil := time.Now().Add(time.Hour)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT authority.payload, authority.payload_sha256, authority.refresh_after, authority.valid_until,")).
		WithArgs(sharedauthority.PublishingCredentialDigest("sk_local")).
		WillReturnRows(sqlmock.NewRows([]string{"payload", "payload_sha256", "refresh_after", "valid_until", "authority_id", "authority_version", "local_ingest_ready", "withheld_by_tenant_revival", "tenant_authority_version"}).
			AddRow(objectBytes, localPayloadDigest(objectBytes), time.Now().Add(time.Minute), validUntil, sharedauthority.LiveStreamAuthorityID("30000000-0000-0000-0000-000000000001"), int64(4), true, false, int64(8)))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT authority.payload, authority.payload_sha256, authority.refresh_after, authority.valid_until,")).
		WillReturnRows(sqlmock.NewRows([]string{"payload", "payload_sha256", "refresh_after", "valid_until", "authority_version", "local_read_ready", "local_ingest_ready", "local_source_ready"}).
			AddRow(tenantBytes, localPayloadDigest(tenantBytes), time.Now().Add(time.Minute), validUntil, int64(8), false, true, false))

	pair := outagePlacementPair(time.Now())
	commodoreReads := 0
	gate := &federation.PlacementPolicyGate{CellID: "cell-a", Authority: viewerPlacementPairReader{pair: pair},
		IngestFence: &federation.ConnectedPlacementIngestFence{Client: placementStreamContextDown{reads: &commodoreReads}},
		Router: balancer.PlacementRouter{Observe: func(_ context.Context, cell balancer.PlacementCell, _ balancer.PlacementRouteRequest) (balancer.PlacementCellObservation, error) {
			observed := time.Now()
			return balancer.PlacementCellObservation{Complete: true, ObservedAt: observed, ExpiresAt: observed.Add(10 * time.Second), Candidates: []placement.Candidate{{
				TenantID: "10000000-0000-0000-0000-000000000001", ClusterID: "cluster-a", NodeID: "edge-node-1", OwnerTenantID: "platform", Official: true,
				AllowedVerbs: []placement.Verb{placement.Ingest}, ObservedAt: observed, ExpiresAt: observed.Add(10 * time.Second),
				Capacity: placement.CapacityAvailable, BWLimit: 1000, BWAvailable: 900, RAMMax: 100, RAMUsed: 10}}}, nil
		}}}
	p.SetIngestPlacementAdmission((&IngestPlacementAdapter{Authority: viewerPlacementPairReader{pair: pair}, Gate: gate}).AdmitPublisher)

	trigger := &ipcpb.MistTrigger{NodeId: "edge-node-1", TriggerPayload: &ipcpb.MistTrigger_PushRewrite{
		PushRewrite: &ipcpb.PushRewriteTrigger{Pid: 4242, TriggerUuid: "outage-owner-trigger", TriggerUnixMillis: 1, StreamName: "sk_local",
			PushUrl: "rtmp://example/live/sk_local", ObservedConnector: "RTMP"},
	}}
	streamName, blocking, err := p.handlePushRewrite(trigger)
	if err != nil || blocking || streamName != "live+stream-internal" {
		code := ipcpb.IngestErrorCode_INGEST_ERROR_NONE
		if ingestErr, ok := errors.AsType[*ingesterrors.IngestError](err); ok {
			code = ingestErr.Code
		}
		t.Fatalf("outage-owner PUSH_REWRITE = stream=%q blocking=%v code=%s err=%v", streamName, blocking, code, err)
	}
	if trigger.GetClusterId() != "cluster-a" {
		t.Fatalf("admitted on %q, want the signed outage owner cluster-a", trigger.GetClusterId())
	}
	if commodoreReads != 0 {
		t.Fatalf("admission asked Commodore for ownership %d times while holding the verified outage owner", commodoreReads)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
