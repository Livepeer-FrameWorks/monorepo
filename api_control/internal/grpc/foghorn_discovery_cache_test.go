package grpc

import (
	"context"
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
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// discoveryCountingFake is a Quartermaster that counts discovery calls, holding
// each one for hold so concurrent callers overlap, and a Foghorn whose cell is
// down.
type discoveryCountingFake struct {
	quartermasterpb.UnimplementedBootstrapServiceServer
	foghornpb.UnimplementedMediaAuthorityControlServiceServer
	host      string
	port      int32
	hold      time.Duration
	discovers atomic.Int32
}

func (f *discoveryCountingFake) DiscoverServices(_ context.Context, req *quartermasterpb.ServiceDiscoveryRequest) (*quartermasterpb.ServiceDiscoveryResponse, error) {
	f.discovers.Add(1)
	time.Sleep(f.hold)
	return &quartermasterpb.ServiceDiscoveryResponse{Instances: []*quartermasterpb.ServiceInstance{{ClusterId: req.GetClusterId(), Status: "running", HealthStatus: "healthy", Host: &f.host, Port: &f.port}}}, nil
}

func (f *discoveryCountingFake) ApplyMediaAuthority(context.Context, *foghornpb.ApplyMediaAuthorityRequest) (*foghornpb.ApplyMediaAuthorityResponse, error) {
	return nil, status.Error(codes.Unavailable, "cell down")
}

func newDiscoveryCountingServer(t *testing.T, hold time.Duration) (*CommodoreServer, *discoveryCountingFake) {
	t.Helper()
	fake := &discoveryCountingFake{hold: hold}
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

// TestDiscoverFoghornAddrsBrieflySharesConcurrentMisses has the eight delivery
// workers of one cell miss the cache together: they make one Quartermaster
// call between them, not eight.
func TestDiscoverFoghornAddrsBrieflySharesConcurrentMisses(t *testing.T) {
	server, fake := newDiscoveryCountingServer(t, 200*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	results := make([][]string, 8)
	for i := range results {
		wg.Go(func() { results[i] = server.discoverFoghornAddrsBriefly(ctx, "cell-b") })
	}
	wg.Wait()

	if n := fake.discovers.Load(); n != 1 {
		t.Fatalf("Quartermaster discovery calls = %d for 8 concurrent misses, want 1", n)
	}
	for i, addrs := range results {
		if len(addrs) != 1 {
			t.Fatalf("worker %d got %v, want the shared answer", i, addrs)
		}
	}
}

// TestApplyMediaAuthorityFailureBurstKeepsDiscovery sends a burst of
// deliveries to a cell that is down. Each fails, and the retries reuse the
// fresh answer instead of asking Quartermaster again per failure.
func TestApplyMediaAuthorityFailureBurstKeepsDiscovery(t *testing.T) {
	server, fake := newDiscoveryCountingServer(t, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	row := commodoredb.ClaimMediaAuthorityDeliveriesRow{AuthorityKind: "tenant", AuthorityID: "tenant-1", AuthorityVersion: 3, CellID: "cell-b"}

	for range 8 {
		if err := server.applyMediaAuthorityDelivery(ctx, row, &mediapb.SignedAuthorityEnvelope{}); status.Code(err) != codes.Unavailable {
			t.Fatalf("delivery error = %v, want Unavailable", err)
		}
	}
	if n := fake.discovers.Load(); n != 1 {
		t.Fatalf("Quartermaster discovery calls = %d across a burst of 8 failed deliveries, want 1", n)
	}
}
