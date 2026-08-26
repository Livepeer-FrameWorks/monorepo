package grpc

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	purserclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/purser"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"

	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// purserEntitlementFake serves the two lookups entitlement resolution makes.
// Either can be made to fail to model a transient Purser outage.
type purserEntitlementFake struct {
	purserpb.UnimplementedSubscriptionServiceServer
	purserpb.UnimplementedBillingServiceServer

	subscription func(context.Context, *purserpb.GetSubscriptionRequest) (*purserpb.GetSubscriptionResponse, error)
	tier         func(context.Context, *purserpb.GetBillingTierRequest) (*purserpb.BillingTier, error)
	admission    func(context.Context, *purserpb.GetTenantAdmissionStatusRequest) (*purserpb.GetTenantAdmissionStatusResponse, error)

	subscriptionCalls atomic.Int64
	tierCalls         atomic.Int64
	admissionCalls    atomic.Int64
	// billingStatus answers the separate lookup ResolveStreamContext makes for
	// suspension and balance. Default is an active postpaid tenant.
	billingStatus func(context.Context, *purserpb.GetTenantBillingStatusRequest) (*purserpb.GetTenantBillingStatusResponse, error)
}

func (f *purserEntitlementFake) GetTenantBillingStatus(ctx context.Context, req *purserpb.GetTenantBillingStatusRequest) (*purserpb.GetTenantBillingStatusResponse, error) {
	if f.billingStatus != nil {
		return f.billingStatus(ctx, req)
	}
	return &purserpb.GetTenantBillingStatusResponse{BillingModel: "postpaid"}, nil
}

func (f *purserEntitlementFake) GetSubscription(ctx context.Context, req *purserpb.GetSubscriptionRequest) (*purserpb.GetSubscriptionResponse, error) {
	f.subscriptionCalls.Add(1)
	if f.subscription != nil {
		return f.subscription(ctx, req)
	}
	return &purserpb.GetSubscriptionResponse{}, nil
}

func (f *purserEntitlementFake) GetBillingTier(ctx context.Context, req *purserpb.GetBillingTierRequest) (*purserpb.BillingTier, error) {
	f.tierCalls.Add(1)
	if f.tier != nil {
		return f.tier(ctx, req)
	}
	return &purserpb.BillingTier{}, nil
}

func (f *purserEntitlementFake) GetTenantAdmissionStatus(ctx context.Context, req *purserpb.GetTenantAdmissionStatusRequest) (*purserpb.GetTenantAdmissionStatusResponse, error) {
	f.admissionCalls.Add(1)
	if f.admission != nil {
		return f.admission(ctx, req)
	}
	return &purserpb.GetTenantAdmissionStatusResponse{}, nil
}

func startPurserEntitlementFake(t *testing.T, fake *purserEntitlementFake) *purserclient.GRPCClient {
	t.Helper()

	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	purserpb.RegisterSubscriptionServiceServer(srv, fake)
	purserpb.RegisterBillingServiceServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()

	client, err := purserclient.NewGRPCClient(purserclient.GRPCConfig{
		GRPCAddr:      lis.Addr().String(),
		AllowInsecure: true,
		Logger:        logging.NewLogger(),
		Timeout:       5 * time.Second,
	})
	if err != nil {
		srv.Stop()
		_ = lis.Close()
		t.Fatalf("purser client: %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		srv.Stop()
		_ = lis.Close()
	})
	return client
}

// A Purser outage must not be reported as "this tenant is on the free tier".
// Doing so would demote a paying tenant's cluster classes, and because the
// caller caches the resulting route, the demotion would persist for the cache
// TTL and surface as CLUSTER_NOT_ENTITLED — a permanent-looking denial produced
// by a transient failure.
func TestAllowedClusterClassesPropagatesPurserFailure(t *testing.T) {
	fake := &purserEntitlementFake{
		admission: func(context.Context, *purserpb.GetTenantAdmissionStatusRequest) (*purserpb.GetTenantAdmissionStatusResponse, error) {
			return nil, status.Error(codes.Unavailable, "purser down")
		},
	}
	s := &CommodoreServer{logger: logrus.New(), purserClient: startPurserEntitlementFake(t, fake)}

	classes, err := s.allowedClusterClassesForTenant(context.Background(), "tenant-1")
	if err == nil {
		t.Fatalf("a Purser failure resolved to classes %v instead of an error", classes)
	}
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("want Unavailable so the caller fails closed as transient, got %v", err)
	}
	if classes != nil {
		t.Errorf("classes returned alongside the error: %v", classes)
	}
	if fake.admissionCalls.Load() != 1 || fake.subscriptionCalls.Load() != 0 || fake.tierCalls.Load() != 0 {
		t.Fatalf("classification calls admission/subscription/tier = %d/%d/%d, want 1/0/0", fake.admissionCalls.Load(), fake.subscriptionCalls.Load(), fake.tierCalls.Load())
	}
}

// A tenant that genuinely has no subscription is the free tier — that is an
// answer, not a failure, and must not be turned into an error.
func TestAllowedClusterClassesTreatsNoSubscriptionAsFreeTier(t *testing.T) {
	s := &CommodoreServer{
		logger:       logrus.New(),
		purserClient: startPurserEntitlementFake(t, &purserEntitlementFake{}),
	}

	classes, err := s.allowedClusterClassesForTenant(context.Background(), "tenant-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := classes["platform_official"]; !ok {
		t.Fatalf("free tier should keep platform_official, got %v", classes)
	}
	if _, ok := classes["tenant_private"]; ok {
		t.Errorf("free tier should not include tenant_private: %v", classes)
	}
}

// A paid tier still widens the class set.
func TestAllowedClusterClassesGrantsPaidTierClasses(t *testing.T) {
	s := &CommodoreServer{
		logger: logrus.New(),
		purserClient: startPurserEntitlementFake(t, &purserEntitlementFake{
			admission: func(context.Context, *purserpb.GetTenantAdmissionStatusRequest) (*purserpb.GetTenantAdmissionStatusResponse, error) {
				return &purserpb.GetTenantAdmissionStatusResponse{TierLevel: 4}, nil
			},
		}),
	}

	classes, err := s.allowedClusterClassesForTenant(context.Background(), "tenant-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{"platform_official", "third_party_marketplace", "tenant_private"} {
		if _, ok := classes[want]; !ok {
			t.Errorf("paid tier missing %q: %v", want, classes)
		}
	}
}

// Purser not being wired up at all is a deployment shape (dev stacks), not a
// failed lookup, so it resolves to the free tier rather than erroring.
func TestAllowedClusterClassesWithoutPurserIsFreeTier(t *testing.T) {
	s := &CommodoreServer{logger: logrus.New()}

	classes, err := s.allowedClusterClassesForTenant(context.Background(), "tenant-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := classes["platform_official"]; !ok {
		t.Fatalf("want the free class set, got %v", classes)
	}
}

// Peer health and plan entitlement decide whether a publish is admitted, so
// they must not be served at route-cache age: a degraded cluster would stay
// admissible, and a recovered one rejected, for minutes.
func TestAdmissionRouteRefreshesStalePeerState(t *testing.T) {
	s := &CommodoreServer{
		logger:        logrus.New(),
		routeCache:    make(map[string]*clusterRoute),
		routeCacheTTL: 5 * time.Minute,
		// quartermasterClient nil: a refresh attempt must surface, not be skipped.
	}

	// A cached route whose static parts are current but whose admission facts
	// have aged past the admission window.
	s.routeCache["tenant-1"] = &clusterRoute{
		clusterID:           "cluster-1",
		foghornAddr:         "foghorn:50051",
		resolvedAt:          time.Now(),
		admissionResolvedAt: time.Now().Add(-(admissionRouteFreshness + time.Second)),
	}

	if _, err := s.resolveAdmissionRouteForTenant(context.Background(), "tenant-1"); err == nil {
		t.Fatal("stale admission state was served instead of refreshed")
	}

	// Routing itself is unaffected: the static route is still usable.
	if _, err := s.resolveClusterRouteForTenant(context.Background(), "tenant-1"); err != nil {
		t.Fatalf("static route resolution should still succeed from cache: %v", err)
	}
}

// Within the window the cached peer state is reused, so admission does not
// issue a Quartermaster call per request.
func TestAdmissionRouteReusesFreshPeerState(t *testing.T) {
	s := &CommodoreServer{
		logger:        logrus.New(),
		routeCache:    make(map[string]*clusterRoute),
		routeCacheTTL: 5 * time.Minute,
	}
	s.routeCache["tenant-1"] = &clusterRoute{
		clusterID:           "cluster-1",
		foghornAddr:         "foghorn:50051",
		resolvedAt:          time.Now(),
		admissionResolvedAt: time.Now(),
	}

	route, err := s.resolveAdmissionRouteForTenant(context.Background(), "tenant-1")
	if err != nil {
		t.Fatalf("fresh admission state should be reused without a refresh: %v", err)
	}
	if route.clusterID != "cluster-1" {
		t.Fatalf("unexpected route: %+v", route)
	}
}

// Other routing paths deliberately evict a route after a failed dial. A refresh
// that started before that eviction must not reinsert the entry it was built
// from, or it would silently undo the invalidation.
func TestAdmissionRefreshDoesNotResurrectEvictedRoute(t *testing.T) {
	s := &CommodoreServer{
		logger:        logrus.New(),
		routeCache:    make(map[string]*clusterRoute),
		routeCacheTTL: 5 * time.Minute,
		purserClient:  startPurserEntitlementFake(t, &purserEntitlementFake{}),
	}

	stale := &clusterRoute{
		clusterID:           "cluster-1",
		foghornAddr:         "foghorn:50051",
		resolvedAt:          time.Now(),
		admissionResolvedAt: time.Now().Add(-(admissionRouteFreshness + time.Second)),
	}
	refreshed := *stale
	refreshed.admissionResolvedAt = time.Now()

	// The entry was evicted while this refresh was in flight.
	s.routeCacheMu.Lock()
	delete(s.routeCache, "tenant-1")
	s.routeCacheMu.Unlock()

	s.installRefreshedAdmissionRoute("tenant-1", stale, &refreshed)

	s.routeCacheMu.RLock()
	_, exists := s.routeCache["tenant-1"]
	s.routeCacheMu.RUnlock()
	if exists {
		t.Fatal("refresh reinserted a route that had been deliberately evicted")
	}
}

// A refresh that finished second must not overwrite the fresher state that
// landed first, nor stamp its own older read as current.
func TestAdmissionRefreshDoesNotOverwriteNewerEntry(t *testing.T) {
	s := &CommodoreServer{
		logger:        logrus.New(),
		routeCache:    make(map[string]*clusterRoute),
		routeCacheTTL: 5 * time.Minute,
	}

	base := &clusterRoute{clusterID: "cluster-1", admissionResolvedAt: time.Now().Add(-time.Minute)}
	newer := &clusterRoute{clusterID: "cluster-1", admissionResolvedAt: time.Now()}
	s.routeCache["tenant-1"] = newer

	slowResult := *base
	slowResult.admissionResolvedAt = time.Now().Add(-30 * time.Second)
	s.installRefreshedAdmissionRoute("tenant-1", base, &slowResult)

	s.routeCacheMu.RLock()
	got := s.routeCache["tenant-1"]
	s.routeCacheMu.RUnlock()
	if got != newer {
		t.Fatal("a slower refresh overwrote the fresher cached admission state")
	}
}

// When the entry was replaced while a refresh was in flight, the replacement is
// what the caller gets — including when its completion stamp is the older one.
// These timestamps record when a lookup finished, not when Quartermaster and
// Purser produced the snapshot, so a slow reader of stale state can finish last
// and still look newer. Deciding by stamp lets that reader's peer set be the one
// admission uses.
func TestAdmissionRefreshYieldsToReplacementRegardlessOfStamp(t *testing.T) {
	s := &CommodoreServer{
		logger:        logrus.New(),
		routeCache:    make(map[string]*clusterRoute),
		routeCacheTTL: 5 * time.Minute,
	}

	from := &clusterRoute{clusterID: "cluster-1", admissionResolvedAt: time.Now().Add(-time.Minute)}
	// A replacement landed first. Its stamp is older only because this refresh
	// took longer to complete.
	replacement := &clusterRoute{clusterID: "cluster-1", admissionResolvedAt: time.Now().Add(-time.Second)}
	s.routeCache["tenant-1"] = replacement

	late := *from
	late.admissionResolvedAt = time.Now()

	got := s.installRefreshedAdmissionRoute("tenant-1", from, &late)
	if got != replacement {
		t.Fatal("a late refresh's own result was served over the entry that replaced it")
	}

	s.routeCacheMu.RLock()
	cached := s.routeCache["tenant-1"]
	s.routeCacheMu.RUnlock()
	if cached != replacement {
		t.Fatal("a late refresh overwrote the entry that replaced it")
	}
}
