package resolvers

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/clients/clientstest"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/periscope"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/tenants"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// operatorCtx carries the platform_operator grant: passes RequirePlatformOperator.
func operatorCtx() context.Context {
	ctx := clientstest.AuthedCtx(tenants.SystemTenantID.String())
	ctx = context.WithValue(ctx, ctxkeys.KeyRole, "owner")
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, "operator-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyPlatformOperator, true)
	return ctx
}

func platformResolverWith(opts ...func(*clients.ServiceClients)) *Resolver {
	return &Resolver{
		Clients: clientstest.Clients(opts...),
		Logger:  clientstest.DiscardLogger(),
	}
}

// requireStrippedIdentity asserts the ctx a fake receives carries no caller
// identity: the client interceptor must fall back to the service token and
// downstream must classify the call as a service call.
func requireStrippedIdentity(t *testing.T, ctx context.Context) {
	t.Helper()
	if got := ctxkeys.GetTenantID(ctx); got != "" {
		t.Fatalf("identity leak: tenant_id %q reached the client ctx", got)
	}
	if got := ctxkeys.GetUserID(ctx); got != "" {
		t.Fatalf("identity leak: user_id %q reached the client ctx", got)
	}
	if got := ctxkeys.GetJWTToken(ctx); got != "" {
		t.Fatalf("identity leak: jwt reached the client ctx")
	}
}

func TestPlatformGateRejectsNonOperators(t *testing.T) {
	cases := []struct {
		name string
		ctx  context.Context
	}{
		{"regular tenant owner", func() context.Context {
			ctx := clientstest.AuthedCtx("5eed517e-ba5e-da7a-517e-ba5eda7a0001")
			return context.WithValue(ctx, ctxkeys.KeyRole, "owner")
		}()},
		{"system tenant member", func() context.Context {
			ctx := clientstest.AuthedCtx(tenants.SystemTenantID.String())
			return context.WithValue(ctx, ctxkeys.KeyRole, "member")
		}()},
		{"anonymous", context.Background()},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ps := &clientstest.FakePeriscope{}
			qm := &clientstest.FakeQuartermaster{}
			pu := &clientstest.FakePurser{}
			r := platformResolverWith(clientstest.WithPeriscope(ps), clientstest.WithQuartermaster(qm), clientstest.WithPurser(pu))

			if _, err := r.DoPlatformTenants(tc.ctx, nil, nil); err == nil {
				t.Fatal("DoPlatformTenants must reject non-operators")
			}
			if _, err := r.DoPlatformTenant(tc.ctx, "tenant-x"); err == nil {
				t.Fatal("DoPlatformTenant must reject non-operators")
			}
			if _, err := r.DoPlatformClusters(tc.ctx); err == nil {
				t.Fatal("DoPlatformClusters must reject non-operators")
			}
			if ps.Calls+qm.Calls+pu.Calls != 0 {
				t.Fatalf("backends consulted despite rejected gate: %d calls", ps.Calls+qm.Calls+pu.Calls)
			}
		})
	}
}

func TestDoPlatformTenantsJoinsActivityIdentityBilling(t *testing.T) {
	day := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
	ps := &clientstest.FakePeriscope{
		ListTenantActivityFn: func(ctx context.Context, _ *periscope.TimeRangeOpts, _ []string, _ int32) (*periscopepb.ListTenantActivityResponse, error) {
			requireStrippedIdentity(t, ctx)
			return &periscopepb.ListTenantActivityResponse{Tenants: []*periscopepb.TenantActivity{
				{TenantId: "tenant-busy", ViewerHours: 120.5, IngestHours: 40, LiveStreams: 2, UniqueViewers: 77, LastStreamAt: timestamppb.New(day)},
				{TenantId: "tenant-quiet", ApiRequests: 50},
			}}, nil
		},
	}
	qm := &clientstest.FakeQuartermaster{
		ListTenantsFn: func(ctx context.Context, _ *commonpb.CursorPaginationRequest) (*quartermasterpb.ListTenantsResponse, error) {
			requireStrippedIdentity(t, ctx)
			return &quartermasterpb.ListTenantsResponse{Tenants: []*quartermasterpb.Tenant{
				{Id: "tenant-busy", Name: "Busy Org", DeploymentTier: "pro"},
				{Id: "tenant-dormant", Name: "Dormant Org", DeploymentTier: "free"},
			}}, nil
		},
	}
	pu := &clientstest.FakePurser{
		ListTenantBillingSnapshotsFn: func(ctx context.Context, tenantIDs []string, limit int32) (*purserpb.ListTenantBillingSnapshotsResponse, error) {
			requireStrippedIdentity(t, ctx)
			// The filter must cover the exact rendered set: activity tenants
			// (busy, quiet) PLUS listed-but-dormant tenants — a server-side
			// cap on an unfiltered fetch would silently null out billing.
			got := map[string]bool{}
			for _, id := range tenantIDs {
				got[id] = true
			}
			if len(tenantIDs) != 3 || !got["tenant-busy"] || !got["tenant-quiet"] || !got["tenant-dormant"] {
				t.Errorf("billing filter must be the rendered tenant set, got %v", tenantIDs)
			}
			if limit != int32(len(tenantIDs)) {
				t.Errorf("billing limit must match the filter size, got %d", limit)
			}
			return &purserpb.ListTenantBillingSnapshotsResponse{Snapshots: []*purserpb.TenantBillingSnapshot{
				{TenantId: "tenant-busy", BillingModel: "postpaid", Status: "active", TierName: "pro", OutstandingAmount: 12.5},
				{TenantId: "tenant-dormant", BillingModel: "prepaid", Status: "active", TierName: "free", PrepaidBalanceCents: 950},
			}}, nil
		},
	}
	r := platformResolverWith(clientstest.WithPeriscope(ps), clientstest.WithQuartermaster(qm), clientstest.WithPurser(pu))

	idx, err := r.DoPlatformTenants(operatorCtx(), nil, nil)
	if err != nil {
		t.Fatalf("DoPlatformTenants: %v", err)
	}
	if len(idx.Rows) != 3 {
		t.Fatalf("expected 2 activity rows + 1 dormant tenant, got %d", len(idx.Rows))
	}

	busy := idx.Rows[0]
	if busy.TenantID != "tenant-busy" || busy.Tenant == nil || busy.Tenant.Name != "Busy Org" {
		t.Fatalf("busy row identity mismatch: %+v", busy)
	}
	if busy.Activity.ViewerHours != 120.5 || busy.Activity.LiveStreams != 2 || busy.Activity.UniqueViewers != 77 {
		t.Fatalf("busy row activity mismatch: %+v", busy.Activity)
	}
	if busy.Activity.LastStreamAt == nil || !busy.Activity.LastStreamAt.Equal(day) {
		t.Fatalf("busy row lastStreamAt mismatch: %+v", busy.Activity.LastStreamAt)
	}
	if busy.Billing == nil || busy.Billing.OutstandingAmount != 12.5 {
		t.Fatalf("busy row billing mismatch: %+v", busy.Billing)
	}

	quiet := idx.Rows[1]
	if quiet.TenantID != "tenant-quiet" {
		t.Fatalf("expected tenant-quiet second, got %s", quiet.TenantID)
	}
	if quiet.Tenant != nil {
		t.Fatalf("tenant-quiet has no quartermaster row; identity must be null, got %+v", quiet.Tenant)
	}
	if quiet.Billing != nil {
		t.Fatalf("tenant-quiet has no snapshot; billing must be null, got %+v", quiet.Billing)
	}

	dormant := idx.Rows[2]
	if dormant.TenantID != "tenant-dormant" || dormant.Activity == nil || dormant.Activity.ViewerHours != 0 {
		t.Fatalf("dormant tenant must be appended with zero activity: %+v", dormant)
	}
	// Regression: dormant (no-activity) tenants must still surface their
	// billing snapshot — paid-but-idle is exactly what an operator looks for.
	if dormant.Billing == nil || dormant.Billing.PrepaidBalanceCents != 950 {
		t.Fatalf("dormant tenant lost its billing snapshot: %+v", dormant.Billing)
	}
}

func TestDoPlatformTenantsPartialTolerance(t *testing.T) {
	ps := &clientstest.FakePeriscope{
		ListTenantActivityFn: func(context.Context, *periscope.TimeRangeOpts, []string, int32) (*periscopepb.ListTenantActivityResponse, error) {
			return &periscopepb.ListTenantActivityResponse{Tenants: []*periscopepb.TenantActivity{{TenantId: "tenant-a"}}}, nil
		},
	}
	qm := &clientstest.FakeQuartermaster{
		ListTenantsFn: func(context.Context, *commonpb.CursorPaginationRequest) (*quartermasterpb.ListTenantsResponse, error) {
			return nil, context.DeadlineExceeded
		},
	}
	pu := &clientstest.FakePurser{
		ListTenantBillingSnapshotsFn: func(context.Context, []string, int32) (*purserpb.ListTenantBillingSnapshotsResponse, error) {
			return nil, context.DeadlineExceeded
		},
	}
	r := platformResolverWith(clientstest.WithPeriscope(ps), clientstest.WithQuartermaster(qm), clientstest.WithPurser(pu))

	idx, err := r.DoPlatformTenants(operatorCtx(), nil, nil)
	if err != nil {
		t.Fatalf("identity/billing failures must degrade, not fail: %v", err)
	}
	if len(idx.Rows) != 1 || idx.Rows[0].Tenant != nil || idx.Rows[0].Billing != nil {
		t.Fatalf("expected degraded row with null identity/billing: %+v", idx.Rows)
	}

	// Activity failing is fatal — it's the spine of the index.
	psDown := &clientstest.FakePeriscope{
		ListTenantActivityFn: func(context.Context, *periscope.TimeRangeOpts, []string, int32) (*periscopepb.ListTenantActivityResponse, error) {
			return nil, context.DeadlineExceeded
		},
	}
	rDown := platformResolverWith(clientstest.WithPeriscope(psDown), clientstest.WithQuartermaster(qm), clientstest.WithPurser(pu))
	if _, err := rDown.DoPlatformTenants(operatorCtx(), nil, nil); err == nil {
		t.Fatal("activity failure must surface as an error")
	}
}

// The billing tab reuses the tenant-scoped resolvers by impersonating the
// target tenant at the data layer: the client must see ctx tenant == target
// (not the operator's system tenant) and no user identity.
func TestDoPlatformTenantInvoicesImpersonatesTarget(t *testing.T) {
	const target = "tenant-target"
	pu := &clientstest.FakePurser{
		ListInvoicesFn: func(ctx context.Context, tenantID string, _ *string, _ *commonpb.CursorPaginationRequest) (*purserpb.ListInvoicesResponse, error) {
			if tenantID != target {
				t.Errorf("expected explicit target tenant, got %q", tenantID)
			}
			if got := ctxkeys.GetTenantID(ctx); got != target {
				t.Errorf("impersonated ctx tenant = %q, want %q", got, target)
			}
			if got := ctxkeys.GetUserID(ctx); got != "" {
				t.Errorf("operator user identity leaked: %q", got)
			}
			if got := ctxkeys.GetJWTToken(ctx); got != "" {
				t.Errorf("operator jwt leaked")
			}
			return &purserpb.ListInvoicesResponse{Invoices: []*purserpb.Invoice{{Id: "inv-1", TenantId: target}}}, nil
		},
	}
	r := platformResolverWith(clientstest.WithPurser(pu))

	conn, err := r.DoPlatformTenantInvoices(operatorCtx(), target, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("DoPlatformTenantInvoices: %v", err)
	}
	if len(conn.Edges) != 1 || conn.Edges[0].Node.Id != "inv-1" {
		t.Fatalf("invoice mapping mismatch: %+v", conn)
	}
}

// Detail reads must pass a tenant filter to the activity RPC: filtering the
// ranked top-N list client-side reports zero activity for any tenant
// outside the top N.
func TestDoPlatformTenantActivityFiltersToTenant(t *testing.T) {
	const target = "tenant-small"
	ps := &clientstest.FakePeriscope{
		ListTenantActivityFn: func(ctx context.Context, _ *periscope.TimeRangeOpts, tenantIDs []string, _ int32) (*periscopepb.ListTenantActivityResponse, error) {
			requireStrippedIdentity(t, ctx)
			if len(tenantIDs) != 1 || tenantIDs[0] != target {
				t.Errorf("detail activity must filter to the target tenant, got %v", tenantIDs)
			}
			return &periscopepb.ListTenantActivityResponse{Tenants: []*periscopepb.TenantActivity{
				{TenantId: target, IngestHours: 0.5},
			}}, nil
		},
	}
	r := platformResolverWith(clientstest.WithPeriscope(ps))

	activity, err := r.DoPlatformTenantActivity(operatorCtx(), target, nil)
	if err != nil {
		t.Fatalf("DoPlatformTenantActivity: %v", err)
	}
	if activity.IngestHours != 0.5 {
		t.Fatalf("activity mismatch: %+v", activity)
	}
}

// The overview tab reuses DoGetPlatformOverview, which runs the gateway's
// RequirePermission BEFORE its backend call. The impersonated ctx must keep
// the operator's auth type (gateway-local) while stripping the wire identity
// — stripping auth type turned an authorized operator into "unauthenticated".
func TestDoPlatformTenantOverviewPassesGatewayPermission(t *testing.T) {
	const target = "tenant-target"
	ps := &clientstest.FakePeriscope{
		GetPlatformOverviewFn: func(ctx context.Context, tenantID string, _ *periscope.TimeRangeOpts) (*periscopepb.GetPlatformOverviewResponse, error) {
			if tenantID != target {
				t.Errorf("expected impersonated tenant %q, got %q", target, tenantID)
			}
			if got := ctxkeys.GetUserID(ctx); got != "" {
				t.Errorf("operator user identity leaked: %q", got)
			}
			if got := ctxkeys.GetJWTToken(ctx); got != "" {
				t.Errorf("operator jwt leaked")
			}
			return &periscopepb.GetPlatformOverviewResponse{TenantId: target, TotalStreams: 4}, nil
		},
	}
	r := platformResolverWith(clientstest.WithPeriscope(ps))

	overview, err := r.DoPlatformTenantOverview(operatorCtx(), target, nil)
	if err != nil {
		t.Fatalf("DoPlatformTenantOverview: %v", err)
	}
	if overview == nil || overview.TotalStreams != 4 {
		t.Fatalf("overview mismatch: %+v", overview)
	}
}

func TestDoPlatformClustersJoinsLiveStatsAndTenants(t *testing.T) {
	dbURL := "postgres://user:secret@db.internal/quartermaster"
	qm := &clientstest.FakeQuartermaster{
		ListClustersFn: func(ctx context.Context, _ *commonpb.CursorPaginationRequest) (*quartermasterpb.ListClustersResponse, error) {
			requireStrippedIdentity(t, ctx)
			return &quartermasterpb.ListClustersResponse{Clusters: []*quartermasterpb.InfrastructureCluster{
				{Id: "row-1", ClusterId: "cluster-eu", ClusterName: "EU", DatabaseUrl: &dbURL, KafkaBrokers: []string{"broker:9092"}},
				{Id: "row-2", ClusterId: "cluster-us", ClusterName: "US"},
			}}, nil
		},
		GetTenantsByClusterFn: func(ctx context.Context, clusterID string, _ *commonpb.CursorPaginationRequest) (*quartermasterpb.GetTenantsByClusterResponse, error) {
			if clusterID == "cluster-us" {
				return nil, context.DeadlineExceeded // per-cluster failure tolerated
			}
			return &quartermasterpb.GetTenantsByClusterResponse{
				ClusterId:  clusterID,
				Tenants:    []*quartermasterpb.Tenant{{Id: "tenant-a", Name: "A"}},
				Pagination: &commonpb.CursorPaginationResponse{TotalCount: 7},
			}, nil
		},
	}
	ps := &clientstest.FakePeriscope{
		GetNetworkLiveStatsFn: func(ctx context.Context) (*periscopepb.GetNetworkLiveStatsResponse, error) {
			requireStrippedIdentity(t, ctx)
			return &periscopepb.GetNetworkLiveStatsResponse{Clusters: []*periscopepb.NetworkClusterLiveStats{
				{ClusterId: "cluster-eu", ActiveStreams: 3, CurrentViewers: 42},
			}}, nil
		},
	}
	r := platformResolverWith(clientstest.WithQuartermaster(qm), clientstest.WithPeriscope(ps))

	rows, err := r.DoPlatformClusters(operatorCtx())
	if err != nil {
		t.Fatalf("DoPlatformClusters: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 cluster rows, got %d", len(rows))
	}
	eu := rows[0]
	if eu.LiveStats == nil || eu.LiveStats.ActiveStreams != 3 || eu.LiveStats.CurrentViewers != 42 {
		t.Fatalf("eu live stats mismatch: %+v", eu.LiveStats)
	}
	// Credentials-shaped connectivity fields must never leave the pivot.
	if eu.Cluster.DatabaseUrl != nil || eu.Cluster.PeriscopeUrl != nil || len(eu.Cluster.KafkaBrokers) != 0 {
		t.Fatalf("pivot cluster must be sanitized: %+v", eu.Cluster)
	}
	if eu.TenantCount != 7 || len(eu.Tenants) != 1 {
		t.Fatalf("eu tenants mismatch: count=%d tenants=%d", eu.TenantCount, len(eu.Tenants))
	}
	us := rows[1]
	if us.LiveStats != nil {
		t.Fatalf("us has no live stats; must be null, got %+v", us.LiveStats)
	}
	if len(us.Tenants) != 0 || us.TenantCount != 0 {
		t.Fatalf("us tenants-by-cluster failed; must degrade to empty: %+v", us)
	}
}

func TestPlatformAuditLogging(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.NewLogger()
	logger.SetOutput(&buf)

	ps := &clientstest.FakePeriscope{
		ListTenantActivityFn: func(context.Context, *periscope.TimeRangeOpts, []string, int32) (*periscopepb.ListTenantActivityResponse, error) {
			return &periscopepb.ListTenantActivityResponse{}, nil
		},
	}
	qm := &clientstest.FakeQuartermaster{
		ListTenantsFn: func(context.Context, *commonpb.CursorPaginationRequest) (*quartermasterpb.ListTenantsResponse, error) {
			return &quartermasterpb.ListTenantsResponse{}, nil
		},
	}
	pu := &clientstest.FakePurser{
		ListTenantBillingSnapshotsFn: func(context.Context, []string, int32) (*purserpb.ListTenantBillingSnapshotsResponse, error) {
			return &purserpb.ListTenantBillingSnapshotsResponse{}, nil
		},
	}
	r := &Resolver{Clients: clientstest.Clients(clientstest.WithPeriscope(ps), clientstest.WithQuartermaster(qm), clientstest.WithPurser(pu)), Logger: logger}

	if _, err := r.DoPlatformTenants(operatorCtx(), nil, nil); err != nil {
		t.Fatalf("DoPlatformTenants: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "platform_admin_read") || !strings.Contains(out, "operator-1") || !strings.Contains(out, "allowed") {
		t.Fatalf("allowed read must be audited with the operator identity, got: %s", out)
	}

	buf.Reset()
	denied := context.WithValue(clientstest.AuthedCtx("random-tenant"), ctxkeys.KeyRole, "owner")
	if _, err := r.DoPlatformTenants(denied, nil, nil); err == nil {
		t.Fatal("expected gate rejection")
	}
	if out := buf.String(); !strings.Contains(out, "platform_admin_read") || !strings.Contains(out, "denied") {
		t.Fatalf("denied read must be audited, got: %s", out)
	}
}

// Demo mode must short-circuit before the gate and never touch clients —
// the schema sweep runs with empty ServiceClients.
func TestPlatformDemoModeShortCircuits(t *testing.T) {
	r := platformResolverWith() // no fakes: any client call panics
	ctx := context.WithValue(context.Background(), ctxkeys.KeyDemoMode, true)

	idx, err := r.DoPlatformTenants(ctx, nil, nil)
	if err != nil || len(idx.Rows) == 0 {
		t.Fatalf("demo tenants = (%+v, %v)", idx, err)
	}
	detail, err := r.DoPlatformTenant(ctx, "anything")
	if err != nil || detail == nil {
		t.Fatalf("demo tenant detail = (%+v, %v)", detail, err)
	}
	clusters, err := r.DoPlatformClusters(ctx)
	if err != nil || len(clusters) == 0 {
		t.Fatalf("demo clusters = (%+v, %v)", clusters, err)
	}
	content, err := r.DoPlatformTenantContent(ctx, "anything")
	if err != nil || content == nil {
		t.Fatalf("demo content = (%+v, %v)", content, err)
	}
	snapshot, err := r.DoPlatformTenantBillingSnapshot(ctx, "anything")
	if err != nil || snapshot == nil {
		t.Fatalf("demo snapshot = (%+v, %v)", snapshot, err)
	}
	activity, err := r.DoPlatformTenantActivity(ctx, "anything", nil)
	if err != nil || activity == nil {
		t.Fatalf("demo activity = (%+v, %v)", activity, err)
	}
}
