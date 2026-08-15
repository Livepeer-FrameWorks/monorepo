package grpc

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"

	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
)

// quartermasterRoutingFake serves the two lookups a full route build makes and
// counts the routing calls, which is what the collapse is about.
type quartermasterRoutingFake struct {
	quartermasterpb.UnimplementedTenantServiceServer
	quartermasterpb.UnimplementedBootstrapServiceServer

	routingCalls atomic.Int64
	// gate holds every routing call open until released, so the build is
	// guaranteed to still be in flight when the other callers arrive.
	gate chan struct{}
}

func (f *quartermasterRoutingFake) GetClusterRouting(context.Context, *quartermasterpb.GetClusterRoutingRequest) (*quartermasterpb.ClusterRoutingResponse, error) {
	f.routingCalls.Add(1)
	if f.gate != nil {
		<-f.gate
	}
	addr, slug := "foghorn-1:50051", "cluster-1"
	return &quartermasterpb.ClusterRoutingResponse{
		ClusterId:       "cluster-1",
		FoghornGrpcAddr: &addr,
		ClusterSlug:     &slug,
	}, nil
}

func (f *quartermasterRoutingFake) DiscoverServices(context.Context, *quartermasterpb.ServiceDiscoveryRequest) (*quartermasterpb.ServiceDiscoveryResponse, error) {
	return &quartermasterpb.ServiceDiscoveryResponse{}, nil
}

func startQuartermasterRoutingFake(t *testing.T, fake *quartermasterRoutingFake) *qmclient.GRPCClient {
	t.Helper()

	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	quartermasterpb.RegisterTenantServiceServer(srv, fake)
	quartermasterpb.RegisterBootstrapServiceServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()

	client, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{
		GRPCAddr:      lis.Addr().String(),
		AllowInsecure: true,
		Logger:        logging.NewLogger(),
		Timeout:       5 * time.Second,
	})
	if err != nil {
		srv.Stop()
		_ = lis.Close()
		t.Fatalf("quartermaster client: %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		srv.Stop()
		_ = lis.Close()
	})
	return client
}

// A full route build fans out to Quartermaster, Purser, and one Foghorn
// discovery per cluster. At cache expiry every concurrent request for the
// tenant would repeat all of it, so builds collapse per tenant.
func TestClusterRouteBuildCollapsesConcurrentCallers(t *testing.T) {
	fake := &quartermasterRoutingFake{gate: make(chan struct{})}
	s := &CommodoreServer{
		logger:              logrus.New(),
		routeCache:          make(map[string]*clusterRoute),
		routeCacheTTL:       5 * time.Minute,
		quartermasterClient: startQuartermasterRoutingFake(t, fake),
		purserClient:        startPurserEntitlementFake(t, &purserEntitlementFake{}),
	}

	const callers = 8
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.resolveClusterRouteForTenant(context.Background(), "tenant-1"); err != nil {
				errs <- err
			}
		}()
	}

	// Hold the first routing call until every caller has had a chance to join it.
	waitFor(t, func() bool { return fake.routingCalls.Load() >= 1 })
	time.Sleep(150 * time.Millisecond)
	close(fake.gate)

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("resolve: %v", err)
	}

	if got := fake.routingCalls.Load(); got != 1 {
		t.Fatalf("%d concurrent callers produced %d route builds, want 1", callers, got)
	}
}

// The collapse must not turn into a permanent cache: once the entry expires the
// next caller rebuilds it.
func TestClusterRouteBuildRunsAgainAfterExpiry(t *testing.T) {
	fake := &quartermasterRoutingFake{}
	s := &CommodoreServer{
		logger:              logrus.New(),
		routeCache:          make(map[string]*clusterRoute),
		routeCacheTTL:       50 * time.Millisecond,
		quartermasterClient: startQuartermasterRoutingFake(t, fake),
		purserClient:        startPurserEntitlementFake(t, &purserEntitlementFake{}),
	}

	if _, err := s.resolveClusterRouteForTenant(context.Background(), "tenant-1"); err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	// Within the TTL the cache answers.
	if _, err := s.resolveClusterRouteForTenant(context.Background(), "tenant-1"); err != nil {
		t.Fatalf("cached resolve: %v", err)
	}
	if got := fake.routingCalls.Load(); got != 1 {
		t.Fatalf("cached resolve issued %d route builds, want 1", got)
	}

	time.Sleep(80 * time.Millisecond)
	if _, err := s.resolveClusterRouteForTenant(context.Background(), "tenant-1"); err != nil {
		t.Fatalf("resolve after expiry: %v", err)
	}
	if got := fake.routingCalls.Load(); got != 2 {
		t.Fatalf("expired route was not rebuilt: %d builds", got)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met within deadline")
}
