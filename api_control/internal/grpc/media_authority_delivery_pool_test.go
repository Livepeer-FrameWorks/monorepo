package grpc

import (
	"context"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	foghornclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// deliveryPoolFake is a Foghorn that refuses every signed authority with the
// configured code, standing in for a replica whose address now belongs to a
// Foghorn of another cell (wrong audience) or one that lost the trust set.
type deliveryPoolFake struct {
	quartermasterpb.UnimplementedBootstrapServiceServer
	foghornpb.UnimplementedMediaAuthorityControlServiceServer
	host   string
	port   int32
	code   codes.Code
	applys atomic.Int32
}

func (f *deliveryPoolFake) DiscoverServices(_ context.Context, req *quartermasterpb.ServiceDiscoveryRequest) (*quartermasterpb.ServiceDiscoveryResponse, error) {
	return &quartermasterpb.ServiceDiscoveryResponse{Instances: []*quartermasterpb.ServiceInstance{{ClusterId: req.GetClusterId(), Status: "running", HealthStatus: "healthy", Host: &f.host, Port: &f.port}}}, nil
}

func (f *deliveryPoolFake) ApplyMediaAuthority(context.Context, *foghornpb.ApplyMediaAuthorityRequest) (*foghornpb.ApplyMediaAuthorityResponse, error) {
	f.applys.Add(1)
	return nil, status.Error(f.code, "refused by fake cell")
}

func newDeliveryPoolServer(t *testing.T, code codes.Code) (*CommodoreServer, *deliveryPoolFake) {
	t.Helper()
	fake := &deliveryPoolFake{code: code}
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
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
	qm, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{GRPCAddr: listener.Addr().String(), AllowInsecure: true, Logger: logging.NewLogger(), Timeout: 5 * time.Second, ServiceToken: "commodore-service-token", PreferServiceToken: true})
	if err != nil {
		t.Fatal(err)
	}
	pool := foghornclient.NewPool(foghornclient.PoolConfig{Logger: logging.NewLogger(), AllowInsecure: true, ServiceToken: "commodore-service-token"})
	t.Cleanup(func() { _ = qm.Close(); _ = pool.Close() })
	return &CommodoreServer{logger: logging.NewLogger(), quartermasterClient: qm, foghornPool: pool, foghornCandidateNext: make(map[string]int)}, fake
}

func TestApplyMediaAuthorityDelivery_PermissionDeniedEvictsPooledCell(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	server, fake := newDeliveryPoolServer(t, codes.PermissionDenied)
	row := commodoredb.ClaimMediaAuthorityDeliveriesRow{AuthorityKind: "tenant", AuthorityID: "tenant-1", AuthorityVersion: 3, CellID: "cell-b"}
	err := server.applyMediaAuthorityDelivery(ctx, row, &mediapb.SignedAuthorityEnvelope{})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("delivery error = %v, want PermissionDenied", err)
	}
	if fake.applys.Load() != 1 {
		t.Fatalf("apply calls = %d, want 1", fake.applys.Load())
	}
	if _, cached := server.foghornPool.Get("cell-b"); cached {
		t.Fatal("pooled connection for the refusing cell survived; the next delivery would reuse the misrouted address")
	}
}

func TestApplyMediaAuthorityDelivery_TransientErrorKeepsPooledCell(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	server, _ := newDeliveryPoolServer(t, codes.Unavailable)
	row := commodoredb.ClaimMediaAuthorityDeliveriesRow{AuthorityKind: "tenant", AuthorityID: "tenant-1", AuthorityVersion: 3, CellID: "cell-b"}
	if err := server.applyMediaAuthorityDelivery(ctx, row, &mediapb.SignedAuthorityEnvelope{}); status.Code(err) != codes.Unavailable {
		t.Fatalf("delivery error = %v, want Unavailable", err)
	}
	if _, cached := server.foghornPool.Get("cell-b"); !cached {
		t.Fatal("a transient failure must not churn the cell's pooled connection")
	}
}
