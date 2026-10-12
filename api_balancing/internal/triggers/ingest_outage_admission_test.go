package triggers

import (
	"context"
	"errors"
	"net"
	"regexp"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/federation"
	"frameworks/api_balancing/internal/ingesterrors"
	localauthority "frameworks/api_balancing/internal/mediaauthority"
	"frameworks/api_balancing/internal/state"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
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
	assertOutageOwnerPushRewriteAdmitted(t, nil)
}

// hangingCommodore accepts connections and never answers the calls it hangs:
// a Commodore whose process is stuck, or a path that drops packets, rather
// than one that refuses connections at once.
type hangingCommodore struct {
	commodorepb.UnimplementedInternalServiceServer
	// answerValidation answers the non-claiming identity validation and hangs
	// only the claim.
	answerValidation bool
	calls            atomic.Int32
}

func (c *hangingCommodore) ValidateStreamKey(ctx context.Context, req *commodorepb.ValidateStreamKeyRequest) (*commodorepb.ValidateStreamKeyResponse, error) {
	c.calls.Add(1)
	if c.answerValidation && req.GetClusterId() == "" {
		return &commodorepb.ValidateStreamKeyResponse{Valid: true, TenantId: "10000000-0000-0000-0000-000000000001", UserId: "20000000-0000-0000-0000-000000000001",
			StreamId: "30000000-0000-0000-0000-000000000001", InternalName: "stream-internal"}, nil
	}
	<-ctx.Done()
	return nil, status.FromContextError(ctx.Err()).Err()
}

func hangingCommodoreClient(t *testing.T, server *hangingCommodore) *commodore.GRPCClient {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	grpcServer := grpc.NewServer()
	commodorepb.RegisterInternalServiceServer(grpcServer, server)
	go func() { _ = grpcServer.Serve(listener) }()
	client, err := commodore.NewGRPCClient(commodore.GRPCConfig{GRPCAddr: listener.Addr().String(), Logger: logging.Logger(logrus.New()),
		AllowInsecure: true, ServiceToken: "test-commodore-service-token"})
	if err != nil {
		t.Fatalf("commodore client: %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		grpcServer.Stop()
	})
	return client
}

// A Commodore that accepts the connection and never answers costs the
// admission one bounded attempt per call, not the whole PUSH_REWRITE budget:
// the signed outage owner is still admitted with budget left for the cell's
// own session lookup and mint.
func TestPushRewrite_CommodoreHangs_OutageOwnerAdmittedWithinBudget(t *testing.T) {
	for _, tc := range []struct {
		name             string
		answerValidation bool
	}{
		{name: "validation hangs"},
		{name: "claim hangs", answerValidation: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := &hangingCommodore{answerValidation: tc.answerValidation}
			client := hangingCommodoreClient(t, server)
			elapsed := assertOutageOwnerPushRewriteAdmitted(t, func(p *Processor) { p.commodoreClient = client })
			if elapsed > 2500*time.Millisecond {
				t.Fatalf("admission took %s of its 3s budget waiting on Commodore", elapsed)
			}
			if server.calls.Load() == 0 {
				t.Fatal("admission never asked Commodore; the test does not exercise the hanging call")
			}
		})
	}
}

// Once Commodore is found unreachable, following admissions in the backoff
// window do not ask it again.
func TestPushRewrite_CommodoreOutageBackoffSkipsAdmissionCalls(t *testing.T) {
	server := &hangingCommodore{}
	client := hangingCommodoreClient(t, server)
	backoff := &federation.CentralOutageBackoff{Service: "commodore"}
	backoff.RecordUnreachable(status.Error(codes.Unavailable, "commodore is down"))
	elapsed := assertOutageOwnerPushRewriteAdmitted(t, func(p *Processor) {
		p.commodoreClient = client
		p.SetCommodoreOutageBackoff(backoff)
	})
	if calls := server.calls.Load(); calls != 0 {
		t.Fatalf("admission asked Commodore %d times inside the outage backoff", calls)
	}
	if elapsed > time.Second {
		t.Fatalf("admission inside the outage backoff took %s", elapsed)
	}
}

// assertOutageOwnerPushRewriteAdmitted returns how long the PUSH_REWRITE
// itself took, excluding fixture setup.
func assertOutageOwnerPushRewriteAdmitted(t *testing.T, configure func(*Processor)) time.Duration {
	t.Helper()
	elapsed, _ := admitOutageOwnerPushRewrite(t, "", configure)
	return elapsed
}

// admitOutageOwnerPushRewrite is assertOutageOwnerPushRewriteAdmitted for a
// stream whose signed authority carries processesJSON ("" keeps the fixture's),
// returning the processor so the admitted processing config can be read.
func admitOutageOwnerPushRewrite(t *testing.T, processesJSON string, configure func(*Processor)) (time.Duration, *Processor) {
	t.Helper()
	installIngestSessionMintMockWithStream(t)
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	sm.SetNodeInfo("edge-node-1", "http://edge.example/view", true, nil, nil, "", "", nil)
	sm.SetNodeConnectionInfo(context.Background(), "edge-node-1", "127.0.0.1:18090", "", "cluster-a", nil)
	previousRegistry := control.StreamRegistryInstance
	control.SetStreamRegistry(control.NewStreamRegistry(nil, "cluster-a", time.Minute))
	t.Cleanup(func() { control.SetStreamRegistry(previousRegistry) })

	p, mock, closeDB, tenantBytes, objectBytes := localAuthorityFixture(t)
	defer closeDB()
	if processesJSON != "" {
		object := &mediapb.MediaObjectAuthority{}
		if err := proto.Unmarshal(objectBytes, object); err != nil {
			t.Fatal(err)
		}
		object.GetLiveStream().ProcessesJson = processesJSON
		var err error
		if objectBytes, err = (proto.MarshalOptions{Deterministic: true}).Marshal(object); err != nil {
			t.Fatal(err)
		}
	}
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
	if configure != nil {
		configure(p)
	}

	trigger := &ipcpb.MistTrigger{NodeId: "edge-node-1", TriggerPayload: &ipcpb.MistTrigger_PushRewrite{
		PushRewrite: &ipcpb.PushRewriteTrigger{Pid: 4242, TriggerUuid: "outage-owner-trigger", TriggerUnixMillis: 1, StreamName: "sk_local",
			PushUrl: "rtmp://example/live/sk_local", ObservedConnector: "RTMP"},
	}}
	started := time.Now()
	streamName, blocking, err := p.handlePushRewrite(trigger)
	elapsed := time.Since(started)
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
	return elapsed, p
}
