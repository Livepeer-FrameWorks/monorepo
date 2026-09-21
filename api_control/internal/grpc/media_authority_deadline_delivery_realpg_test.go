//go:build schema_verify

package grpc

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"fmt"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	foghornclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type deadlineDeliveryTransport struct {
	quartermasterpb.UnimplementedBootstrapServiceServer
	foghornpb.UnimplementedMediaAuthorityControlServiceServer
	host                                              string
	port                                              int32
	trust                                             sharedauthority.TrustSet
	legacyStarted                                     chan struct{}
	shortStarted                                      chan struct{}
	legacyOnce, shortOnce                             sync.Once
	legacyActive, shortActive, shortCalls, violations atomic.Int32
}

func (f *deadlineDeliveryTransport) DiscoverServices(ctx context.Context, req *quartermasterpb.ServiceDiscoveryRequest) (*quartermasterpb.ServiceDiscoveryResponse, error) {
	if err := requireCommodoreServiceCredential(ctx); err != nil {
		return nil, err
	}
	return &quartermasterpb.ServiceDiscoveryResponse{Instances: []*quartermasterpb.ServiceInstance{{ClusterId: req.GetClusterId(), Status: "running", HealthStatus: "healthy", Host: &f.host, Port: &f.port}}}, nil
}

func (f *deadlineDeliveryTransport) ApplyMediaAuthority(ctx context.Context, req *foghornpb.ApplyMediaAuthorityRequest) (*foghornpb.ApplyMediaAuthorityResponse, error) {
	if err := requireCommodoreServiceCredential(ctx); err != nil {
		return nil, err
	}
	envelope := req.GetAuthority().GetEnvelope()
	if _, err := sharedauthority.Verify(req.GetAuthority(), f.trust, envelope.GetAudienceCellId(), time.Now()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if envelope.GetSchemaVersion() == 1 {
		f.legacyActive.Add(1)
		defer f.legacyActive.Add(-1)
		f.legacyOnce.Do(func() { close(f.legacyStarted) })
		<-ctx.Done()
		return nil, ctx.Err()
	}
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > mediaAuthorityDeadlineDeliveryTimeout+10*time.Millisecond {
		f.violations.Add(1)
	}
	if envelope.GetAudienceCellId() == "slow" {
		if f.shortActive.Add(1) != 1 {
			f.violations.Add(1)
		}
		defer f.shortActive.Add(-1)
		f.shortCalls.Add(1)
		f.shortOnce.Do(func() { close(f.shortStarted) })
		<-ctx.Done()
		return nil, ctx.Err()
	}
	outcome := foghornpb.MediaAuthorityApplyOutcome_MEDIA_AUTHORITY_APPLY_OUTCOME_APPLIED
	if envelope.GetAuthorityId() == "81000000-0000-4000-8000-000000000014" {
		outcome = foghornpb.MediaAuthorityApplyOutcome_MEDIA_AUTHORITY_APPLY_OUTCOME_DUPLICATE
	}
	if envelope.GetAudienceCellId() == "unknown-outcome" {
		outcome = foghornpb.MediaAuthorityApplyOutcome(999)
	}
	return &foghornpb.ApplyMediaAuthorityResponse{Outcome: outcome, AuthorityKind: envelope.GetKind(), AuthorityId: envelope.GetAuthorityId(), AuthorityVersion: envelope.GetAuthorityVersion()}, nil
}

func TestMediaPlacementDeadlineDelivery_RealPG(t *testing.T) {
	testMediaPlacementDeadlineDelivery(t, startCommodoreRealPG(t))
}

func TestMediaPlacementDeadlineDelivery_RealYugabyte(t *testing.T) {
	testMediaPlacementDeadlineDelivery(t, startPlacementDeliveryYugabyte(t, "placement_delivery"))
}

func testMediaPlacementDeadlineDelivery(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	fake := &deadlineDeliveryTransport{trust: sharedauthority.TrustSet{"delivery-test": public}, legacyStarted: make(chan struct{}), shortStarted: make(chan struct{})}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	host, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	fake.host, fake.port = host, int32(portNumber)
	rpcServer := grpc.NewServer()
	quartermasterpb.RegisterBootstrapServiceServer(rpcServer, fake)
	foghornpb.RegisterMediaAuthorityControlServiceServer(rpcServer, fake)
	go func() { _ = rpcServer.Serve(listener) }()
	t.Cleanup(func() { rpcServer.Stop(); _ = listener.Close() })
	qm, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{GRPCAddr: listener.Addr().String(), AllowInsecure: true, Logger: logging.NewLogger(), Timeout: 10 * time.Second, ServiceToken: "commodore-service-token", PreferServiceToken: true})
	if err != nil {
		t.Fatal(err)
	}
	pool := foghornclient.NewPool(foghornclient.PoolConfig{Logger: logging.NewLogger(), AllowInsecure: true, ServiceToken: "commodore-service-token"})
	t.Cleanup(func() { _ = qm.Close(); _ = pool.Close() })
	server := &CommodoreServer{db: db, logger: logging.NewLogger(), quartermasterClient: qm, foghornPool: pool, foghornCandidateNext: make(map[string]int)}
	q := commodoredb.New(db)
	seed := func(index int, schema uint32, cell string, lifetime time.Duration) {
		t.Helper()
		id := fmt.Sprintf("81000000-0000-4000-8000-%012d", index)
		payload := &mediapb.TenantAuthority{SchemaVersion: schema, TenantId: id, Lifecycle: mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE, BillingDecision: mediapb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW, BillingModel: mediapb.TenantBillingModel_TENANT_BILLING_MODEL_POSTPAID}
		if schema == 2 {
			payload.MediaPlacement = &pb.PolicySet{}
		}
		now := time.Now().UTC()
		envelope, err := sharedauthority.NewEnvelope(mediapb.AuthorityKind_AUTHORITY_KIND_TENANT, id, 1, now, now.Add(lifetime/2), now.Add(lifetime), "delivery-test", cell, payload, []*mediapb.AuthoritySourceRevision{{Service: "quartermaster", Revision: "delivery-fixture"}})
		if err != nil {
			t.Fatal(err)
		}
		signed, err := sharedauthority.Sign(envelope, private)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := proto.Marshal(signed)
		if err != nil {
			t.Fatal(err)
		}
		if err := q.InsertMediaAuthorityVersion(ctx, commodoredb.InsertMediaAuthorityVersionParams{AuthorityKind: "tenant", AuthorityID: id, AuthorityVersion: 1, PayloadSchemaVersion: int32(schema), Payload: envelope.Payload, PayloadSha256: envelope.PayloadSha256, SourceRevisions: []byte("[]"), IssuedAt: now, RefreshAfter: now.Add(lifetime / 2), ValidUntil: now.Add(lifetime)}); err != nil {
			t.Fatal(err)
		}
		if _, err := q.UpsertCurrentMediaAuthority(ctx, commodoredb.UpsertCurrentMediaAuthorityParams{AuthorityKind: "tenant", AuthorityID: id, AuthorityVersion: 1}); err != nil {
			t.Fatal(err)
		}
		if _, err := q.EnqueueMediaAuthorityDelivery(ctx, commodoredb.EnqueueMediaAuthorityDeliveryParams{AuthorityKind: "tenant", AuthorityID: id, AuthorityVersion: 1, CellID: cell, SignedEnvelope: encoded,
			ShortLease: mediaAuthorityShortLease(uint32(schema), now, now.Add(lifetime))}); err != nil {
			t.Fatal(err)
		}
	}
	seed(1, 1, "slow", time.Hour)
	for i := 2; i < 12; i++ {
		seed(i, 2, "slow", 30*time.Second)
	}
	for i := 12; i < 15; i++ {
		seed(i, 2, "healthy", 30*time.Second)
	}
	seed(15, 2, "unknown-outcome", 30*time.Second)
	var workers sync.WaitGroup
	t.Cleanup(func() { cancel(); workers.Wait() })
	workers.Add(1)
	go func() { defer workers.Done(); server.processMediaAuthorityDeliveryBatch(ctx) }()
	select {
	case <-fake.legacyStarted:
	case <-ctx.Done():
		t.Fatal("ordinary delivery did not enter transport")
	}
	workers.Add(1)
	go func() { defer workers.Done(); server.processMediaAuthorityDeadlineDeliveryBatch(ctx) }()
	select {
	case <-fake.shortStarted:
	case <-ctx.Done():
		t.Fatal("long delivery prevented short-lease delivery to the same cell")
	}
	progressCtx, stopProgress := context.WithTimeout(ctx, 3*time.Second)
	defer stopProgress()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var acknowledged, refused int
		if err := db.QueryRowContext(progressCtx, "SELECT count(*) FILTER (WHERE cell_id='healthy' AND status='acknowledged'), count(*) FILTER (WHERE cell_id='unknown-outcome' AND status='pending' AND attempts>0) FROM commodore.media_authority_deliveries").Scan(&acknowledged, &refused); err != nil {
			t.Fatal(err)
		}
		if acknowledged == 3 && refused == 1 {
			break
		}
		select {
		case <-tick.C:
		case <-progressCtx.Done():
			t.Fatal("slow cell or batch barrier stalled healthy-cell queue draining")
		}
	}
	if fake.legacyActive.Load() != 1 || fake.shortActive.Load() != 1 || fake.shortCalls.Load() != 1 || fake.violations.Load() != 0 {
		t.Fatalf("cell isolation/deadline violation: legacy=%d short=%d calls=%d violations=%d", fake.legacyActive.Load(), fake.shortActive.Load(), fake.shortCalls.Load(), fake.violations.Load())
	}
	var distributed int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM commodore.media_authority_distribution WHERE cell_id='healthy' AND highest_acknowledged_version=1").Scan(&distributed); err != nil || distributed != 3 {
		t.Fatalf("healthy acknowledgments lost distribution state: %d %v", distributed, err)
	}
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM commodore.media_authority_distribution WHERE cell_id='unknown-outcome'").Scan(&distributed); err != nil || distributed != 0 {
		t.Fatalf("unknown outcome advanced distribution: %d %v", distributed, err)
	}
	cancel()
	done := make(chan struct{})
	go func() { workers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("delivery workers did not settle after cancellation")
	}
}
