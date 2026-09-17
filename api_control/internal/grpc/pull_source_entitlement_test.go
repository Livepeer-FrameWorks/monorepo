package grpc

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"frameworks/api_control/internal/placementpolicy"
	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"

	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type pullSourceQuartermasterFake struct {
	quartermasterpb.UnimplementedClusterServiceServer

	entitlementByTenant map[string][]*clusterpeerpb.TenantClusterPeer
	requestedTenant     []string
}

func requireCommodoreServiceCredential(ctx context.Context) error {
	md, _ := metadata.FromIncomingContext(ctx)
	if got := md.Get("authorization"); len(got) != 1 || got[0] != "Bearer commodore-service-token" {
		return status.Errorf(codes.Unauthenticated, "unexpected authorization metadata: %v", got)
	}
	if len(md.Get("x-user-id")) != 0 || len(md.Get("x-tenant-id")) != 0 {
		return status.Error(codes.Unauthenticated, "caller identity leaked onto service request")
	}
	return nil
}

func (f *pullSourceQuartermasterFake) GetTenantEntitlement(ctx context.Context, req *quartermasterpb.GetTenantEntitlementRequest) (*quartermasterpb.GetTenantEntitlementResponse, error) {
	if err := requireCommodoreServiceCredential(ctx); err != nil {
		return nil, err
	}
	f.requestedTenant = append(f.requestedTenant, req.GetTenantId())
	return &quartermasterpb.GetTenantEntitlementResponse{EffectiveAccess: f.entitlementByTenant[req.GetTenantId()]}, nil
}

func startPullSourceQuartermasterFake(t *testing.T, fake *pullSourceQuartermasterFake) *qmclient.GRPCClient {
	t.Helper()
	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	quartermasterpb.RegisterClusterServiceServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()

	client, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{
		GRPCAddr:           lis.Addr().String(),
		AllowInsecure:      true,
		Logger:             logging.NewLogger(),
		Timeout:            5 * time.Second,
		ServiceToken:       "commodore-service-token",
		PreferServiceToken: true,
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

func restrictedLocation(clusters ...string) *commodorepb.StreamSourceLocation {
	location := &commodorepb.StreamSourceLocation{Mode: commodorepb.SourceLocationMode_SOURCE_LOCATION_MODE_RESTRICTED}
	for _, cluster := range clusters {
		location.Clusters = append(location.Clusters, &commodorepb.SourceLocationCluster{ClusterId: cluster})
	}
	return location
}

func TestStreamPlacementUsesOnlyTenantEntitledClusters(t *testing.T) {
	fake := &pullSourceQuartermasterFake{
		entitlementByTenant: map[string][]*clusterpeerpb.TenantClusterPeer{
			"tenant-1": {
				{ClusterId: "entitled", ClusterType: "edge", AllowPrivatePullSources: true},
				{ClusterId: "disabled", ClusterType: "edge", AllowPrivatePullSources: false},
			},
		},
	}
	server := &CommodoreServer{
		logger:              logrus.New(),
		quartermasterClient: startPullSourceQuartermasterFake(t, fake),
	}
	ctx := context.Background()

	plan, err := server.prepareStreamPlacement(ctx, "tenant-1", restrictedLocation("entitled"), nil, true)
	if err != nil || !plan.write || strings.Join(plan.location.ClusterIDs(), ",") != "entitled" || !plan.consented["entitled"] || plan.consented["disabled"] {
		t.Fatalf("entitled private source plan = %+v, err %v", plan, err)
	}

	_, err = server.prepareStreamPlacement(ctx, "tenant-1", restrictedLocation("foreign"), nil, false)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("foreign cluster status = %v, want InvalidArgument", status.Code(err))
	}
	if message := status.Convert(err).Message(); !strings.Contains(message, "not eligible") || strings.Contains(message, "allow_private_pull_sources") || strings.Contains(message, "registered media") {
		t.Fatalf("foreign cluster denial = %q", message)
	}

	legacy, err := server.prepareStreamPlacement(ctx, "tenant-1", nil, &commodorepb.PullSourceAllowedClustersInput{ClusterIds: []string{"disabled", "entitled", "entitled"}}, false)
	if err != nil || !legacy.write || strings.Join(legacy.location.ClusterIDs(), ",") != "disabled,entitled" {
		t.Fatalf("legacy pin input plan = %+v, err %v", legacy, err)
	}
	if _, bothErr := server.prepareStreamPlacement(ctx, "tenant-1", restrictedLocation("entitled"), &commodorepb.PullSourceAllowedClustersInput{}, false); status.Code(bothErr) != codes.InvalidArgument {
		t.Fatalf("both inputs accepted: %v", bothErr)
	}
	custom := &commodorepb.StreamSourceLocation{Mode: commodorepb.SourceLocationMode_SOURCE_LOCATION_MODE_CUSTOM}
	if _, customErr := server.prepareStreamPlacement(ctx, "tenant-1", custom, nil, false); status.Code(customErr) != codes.InvalidArgument {
		t.Fatalf("CUSTOM input accepted: %v", customErr)
	}

	untouched, err := server.prepareStreamPlacement(ctx, "tenant-1", nil, nil, false)
	if err != nil || untouched.needsApply() {
		t.Fatalf("public source without location needs placement work: %+v %v", untouched, err)
	}
	if len(fake.requestedTenant) != 3 {
		t.Fatalf("entitlement calls = %d, want one per validated location", len(fake.requestedTenant))
	}
	for _, tenantID := range fake.requestedTenant {
		if tenantID != "tenant-1" {
			t.Fatalf("tenant-bound lookup used %q", tenantID)
		}
	}
}

func TestLegacyPinColumnIsNeverNil(t *testing.T) {
	if pins := legacyPinColumn(placementpolicy.SourceLocation{Mode: placementpolicy.SourceLocationAny}); pins == nil || len(pins) != 0 {
		t.Fatalf("unrestricted pin column = %#v, want empty non-nil list", pins)
	}
	if pins := legacyPinColumn(placementpolicy.SourceLocation{}); pins == nil {
		t.Fatal("unwritten location produced a nil pin column")
	}
	restricted := placementpolicy.SourceLocation{Mode: placementpolicy.SourceLocationRestricted, Clusters: []placementpolicy.SourceLocationCluster{{ClusterID: "b"}, {ClusterID: "a"}}}
	if pins := legacyPinColumn(restricted); strings.Join(pins, ",") != "a,b" {
		t.Fatalf("restricted pin column = %v", pins)
	}
}

func TestPrivateSourceRequiresConsentedBoundOverEffectivePolicy(t *testing.T) {
	consented := map[string]bool{"lan-a": true, "lan-b": true}
	allow := func(clusters ...string) *placementpb.Rules {
		rules := &placementpb.Rules{SchemaVersion: 1, Constraints: &placementpb.Constraints{Allow: &placementpb.SelectorSet{}}}
		for _, cluster := range clusters {
			rules.Constraints.Allow.Any = append(rules.Constraints.Allow.Any, &placementpb.Selector{ClusterIds: []string{cluster}})
		}
		return rules
	}
	for name, tc := range map[string]struct {
		tenant, stream *placementpb.PolicySet
		want           bool
	}{
		"no rules":                         {nil, nil, false},
		"stream bounded to consent":        {nil, &placementpb.PolicySet{Revision: 1, Ingest: allow("lan-a", "lan-b")}, true},
		"stream names unconsented cluster": {nil, &placementpb.PolicySet{Revision: 1, Ingest: allow("lan-a", "public")}, false},
		"tenant layer bounds the stream":   {&placementpb.PolicySet{Revision: 1, Ingest: allow("lan-b")}, &placementpb.PolicySet{Revision: 1}, true},
		"region-only allow is unbounded": {nil, &placementpb.PolicySet{Revision: 1, Ingest: &placementpb.Rules{SchemaVersion: 1, Constraints: &placementpb.Constraints{
			Allow: &placementpb.SelectorSet{Any: []*placementpb.Selector{{Regions: []string{"eu"}}}},
		}}}, false},
		"node allow inside consented cluster": {nil, &placementpb.PolicySet{Revision: 1, Ingest: &placementpb.Rules{SchemaVersion: 1, Constraints: &placementpb.Constraints{
			Allow: &placementpb.SelectorSet{Any: []*placementpb.Selector{{ClusterIds: []string{"lan-a"}, NodeIds: []string{"camera-gw"}}}},
		}}}, true},
	} {
		t.Run(name, func(t *testing.T) {
			policy, err := placement.CompilePolicySets(tc.tenant, tc.stream, placement.Ingest)
			if err != nil {
				t.Fatal(err)
			}
			if got := placementpolicy.PrivateSourceBounded(policy, consented); got != tc.want {
				t.Fatalf("bounded = %t, want %t", got, tc.want)
			}
		})
	}
}
