package heartbeat

import (
	"bytes"
	"context"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/periscope"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

const scopeTestServiceToken = "heartbeat-scope-test-token"

type periscopeScopeCall struct {
	method         string
	requestTenant  string
	metadataTenant string
	contextTenant  string
}

type periscopeScopeRecorder struct {
	mu    sync.Mutex
	calls []periscopeScopeCall
}

func (r *periscopeScopeRecorder) record(ctx context.Context, method, requestTenant string) {
	var mdTenant string
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if v := md.Get("x-tenant-id"); len(v) > 0 {
			mdTenant = v[0]
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, periscopeScopeCall{
		method:         method,
		requestTenant:  requestTenant,
		metadataTenant: mdTenant,
		contextTenant:  middleware.GetTenantID(ctx),
	})
}

type scopePlatformServer struct {
	periscopepb.UnimplementedPlatformAnalyticsServiceServer
	rec *periscopeScopeRecorder
}

func (s *scopePlatformServer) GetPlatformOverview(ctx context.Context, req *periscopepb.GetPlatformOverviewRequest) (*periscopepb.GetPlatformOverviewResponse, error) {
	s.rec.record(ctx, "GetPlatformOverview", req.GetTenantId())
	return &periscopepb.GetPlatformOverviewResponse{TenantId: req.GetTenantId(), ActiveStreams: 2}, nil
}

type scopeAggregatedServer struct {
	periscopepb.UnimplementedAggregatedAnalyticsServiceServer
	rec *periscopeScopeRecorder
}

func (s *scopeAggregatedServer) GetStreamHealthSummary(ctx context.Context, req *periscopepb.GetStreamHealthSummaryRequest) (*periscopepb.GetStreamHealthSummaryResponse, error) {
	s.rec.record(ctx, "GetStreamHealthSummary", req.GetTenantId())
	return &periscopepb.GetStreamHealthSummaryResponse{Summary: &periscopepb.StreamHealthSummary{SampleCount: 1}}, nil
}

func (s *scopeAggregatedServer) GetClientQoeSummary(ctx context.Context, req *periscopepb.GetClientQoeSummaryRequest) (*periscopepb.GetClientQoeSummaryResponse, error) {
	s.rec.record(ctx, "GetClientQoeSummary", req.GetTenantId())
	return &periscopepb.GetClientQoeSummaryResponse{Summary: &periscopepb.ClientQoeSummary{}}, nil
}

// startScopedPeriscope serves the Periscope RPCs the heartbeat uses behind
// the same auth interceptor configuration Periscope runs with (service token,
// metadata policy deny), and returns a real Periscope client on the service
// token plus the server's log output.
func startScopedPeriscope(t *testing.T) (*periscope.GRPCClient, *periscopeScopeRecorder, *bytes.Buffer) {
	t.Helper()
	serverLogs := &bytes.Buffer{}
	serverLogger := logging.NewLoggerWithService("periscope-query-test")
	serverLogger.SetOutput(serverLogs)

	rec := &periscopeScopeRecorder{}
	srv := grpc.NewServer(grpc.UnaryInterceptor(middleware.GRPCAuthInterceptor(middleware.GRPCAuthConfig{
		ServiceToken:         scopeTestServiceToken,
		JWTSecret:            []byte("heartbeat-scope-test-jwt-secret"),
		DelegatedJWTAudience: "periscope",
		MetadataPolicy:       middleware.MetadataPolicyDeny,
		Logger:               serverLogger,
	})))
	periscopepb.RegisterPlatformAnalyticsServiceServer(srv, &scopePlatformServer{rec: rec})
	periscopepb.RegisterAggregatedAnalyticsServiceServer(srv, &scopeAggregatedServer{rec: rec})

	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	client, err := periscope.NewGRPCClient(periscope.GRPCConfig{
		GRPCAddr:      lis.Addr().String(),
		Logger:        testLogger(),
		ServiceToken:  scopeTestServiceToken,
		AllowInsecure: true,
	})
	if err != nil {
		t.Fatalf("periscope client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client, rec, serverLogs
}

// TestHeartbeatPeriscopeReadsScopeTenantByRequestOnly drives the heartbeat's
// Periscope reads through the real client and Periscope's auth interceptor.
// Each call must name the tenant in the request and carry no service-token
// tenant metadata, so Periscope neither denies nor applies injected identity.
func TestHeartbeatPeriscopeReadsScopeTenantByRequestOnly(t *testing.T) {
	client, rec, serverLogs := startScopedPeriscope(t)
	agent := NewAgent(AgentConfig{Logger: testLogger(), Periscope: client})

	const tenantID = "0dd30dec-a860-4d69-9f46-fc95251a556c"
	active, err := agent.countActiveStreams(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("countActiveStreams: %v", err)
	}
	if active != 2 {
		t.Fatalf("active streams = %d, want 2", active)
	}
	tm := tenantMonitoring{TenantID: tenantID, TenantWideEnabled: true, Monitored: []string{"s"}, AllStreamCount: 1}
	if _, err := agent.loadSnapshot(context.Background(), tm); err != nil {
		t.Fatalf("loadSnapshot: %v", err)
	}

	rec.mu.Lock()
	calls := append([]periscopeScopeCall(nil), rec.calls...)
	rec.mu.Unlock()
	seen := map[string]bool{}
	for _, c := range calls {
		seen[c.method] = true
		if c.requestTenant != tenantID {
			t.Errorf("%s request tenant_id = %q, want %q", c.method, c.requestTenant, tenantID)
		}
		if c.metadataTenant != "" {
			t.Errorf("%s carried x-tenant-id metadata %q on a service-token call", c.method, c.metadataTenant)
		}
		if c.contextTenant != "" {
			t.Errorf("%s server context tenant = %q, want none for a service-token call", c.method, c.contextTenant)
		}
	}
	for _, m := range []string{"GetPlatformOverview", "GetStreamHealthSummary", "GetClientQoeSummary"} {
		if !seen[m] {
			t.Errorf("heartbeat did not call %s; calls=%+v", m, calls)
		}
	}
	if strings.Contains(serverLogs.String(), "metadata injection denied") {
		t.Fatalf("Periscope logged a metadata injection denial:\n%s", serverLogs.String())
	}
}

type ctxTenantBillingClient struct {
	ctxTenant string
}

func (c *ctxTenantBillingClient) GetBillingStatus(ctx context.Context, _ string) (*purserpb.BillingStatusResponse, error) {
	c.ctxTenant = ctxkeys.GetTenantID(ctx)
	return &purserpb.BillingStatusResponse{Tier: &purserpb.BillingTier{TierLevel: 3}}, nil
}

// TestTierEntitledNamesTenantByRequestOnly pins that the heartbeat's Purser
// read does not put a tenant identity on the call context, which the shared
// client interceptors would forward as service-token x-tenant-id metadata.
func TestTierEntitledNamesTenantByRequestOnly(t *testing.T) {
	billing := &ctxTenantBillingClient{}
	agent := NewAgent(AgentConfig{Logger: testLogger(), Purser: billing})
	agent.requiredTierLevel = 2
	if !agent.tierEntitled(context.Background(), "tenant-a") {
		t.Fatal("tier 3 tenant should be entitled at required level 2")
	}
	if billing.ctxTenant != "" {
		t.Fatalf("billing call context carried tenant %q", billing.ctxTenant)
	}
}
