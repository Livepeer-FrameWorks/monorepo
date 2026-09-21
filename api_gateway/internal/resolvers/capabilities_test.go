package resolvers

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"frameworks/api_gateway/internal/capabilities"
	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/clients/clientstest"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/platformfeatures"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/version"

	"github.com/99designs/gqlgen/graphql"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// capabilitySources are the three downstream readings the capabilities query
// assembles, each swappable so a test can fail exactly one source.
type capabilitySources struct {
	retentionDays          int32
	retentionErr           error
	processingCustomizable bool
	processingErr          error
	clusters               *quartermasterpb.GetTenantClusterCapabilitiesResponse
	clustersErr            error
	// subscriptionCustomFeatures is what the tenant's subscription overrides
	// hold; the processing gate must ignore it and read the tier.
	subscriptionCustomFeatures *purserpb.BillingFeatures
}

func capabilitiesResolver(src *capabilitySources) *Resolver {
	return &Resolver{
		Clients: clientstest.Clients(
			clientstest.WithPurser(&clientstest.FakePurser{
				GetTenantBillingStatusFn: func(context.Context, string) (*purserpb.GetTenantBillingStatusResponse, error) {
					if src.retentionErr != nil {
						return nil, src.retentionErr
					}
					return &purserpb.GetTenantBillingStatusResponse{RecordingRetentionDays: src.retentionDays}, nil
				},
				GetSubscriptionFn: func(context.Context, string) (*purserpb.GetSubscriptionResponse, error) {
					if src.processingErr != nil {
						return nil, src.processingErr
					}
					return &purserpb.GetSubscriptionResponse{Subscription: &purserpb.TenantSubscription{
						TierId:         "tier-x",
						CustomFeatures: src.subscriptionCustomFeatures,
					}}, nil
				},
				GetBillingTierFn: func(_ context.Context, tierID string) (*purserpb.BillingTier, error) {
					if tierID != "tier-x" {
						return nil, errors.New("unexpected tier " + tierID)
					}
					return &purserpb.BillingTier{
						Id:       "tier-x",
						Features: &purserpb.BillingFeatures{ProcessingCustomizable: src.processingCustomizable},
					}, nil
				},
			}),
			clientstest.WithQuartermaster(&clientstest.FakeQuartermaster{
				GetTenantClusterCapabilitiesFn: func(context.Context, string) (*quartermasterpb.GetTenantClusterCapabilitiesResponse, error) {
					if src.clustersErr != nil {
						return nil, src.clustersErr
					}
					return src.clusters, nil
				},
			}),
		),
		Logger: clientstest.DiscardLogger(),
	}
}

func demoClusterCapabilities() *quartermasterpb.GetTenantClusterCapabilitiesResponse {
	return &quartermasterpb.GetTenantClusterCapabilitiesResponse{
		CustomSubdomain: true,
		CustomDomain:    false,
		Clusters: []*quartermasterpb.TenantClusterCapability{{
			ClusterId:   "cluster-eu",
			ClusterName: "EU",
			Role:        "preferred",
			AccessLevel: "dedicated",
			Media: &quartermasterpb.ClusterMediaCapabilities{
				Ingest: true, Playback: true, Storage: false, Processing: false,
			},
		}},
	}
}

// Every gate reads from the service that enforces it: the operator grant from
// the caller's identity, the retention cap and the processing override from
// Purser, and the domain and per-cluster media verbs from Quartermaster.
func TestReadCapabilitiesReportsEveryGateFromItsEnforcer(t *testing.T) {
	src := &capabilitySources{
		retentionDays:          30,
		processingCustomizable: true,
		clusters:               demoClusterCapabilities(),
	}
	r := capabilitiesResolver(src)
	reading, err := r.ReadCapabilities(capabilitiesCtx("tenant-1"))
	if err != nil {
		t.Fatalf("ReadCapabilities: %v", err)
	}
	if reading.Tenant == nil {
		t.Fatalf("tenant section missing: %+v", reading)
	}
	if reading.Tenant.PlatformOperator {
		t.Error("a plain tenant token must not report the platform operator grant")
	}
	if !reading.Tenant.RecordingRetention.Capped || reading.Tenant.RecordingRetention.MaxDays == nil || *reading.Tenant.RecordingRetention.MaxDays != 30 {
		t.Errorf("retention cap = %+v, want capped at 30 days", reading.Tenant.RecordingRetention)
	}
	if !reading.Tenant.ProcessingCustomizable {
		t.Error("processing gate did not follow the tier features")
	}
	if !reading.Tenant.CustomSubdomain || reading.Tenant.CustomDomain {
		t.Errorf("domain gates = subdomain %v, domain %v; want Quartermaster's answer", reading.Tenant.CustomSubdomain, reading.Tenant.CustomDomain)
	}
	if len(reading.Clusters) != 1 {
		t.Fatalf("clusters = %+v, want the one entitled cluster", reading.Clusters)
	}
	media := reading.Clusters[0].GetMedia()
	if !media.GetIngest() || !media.GetPlayback() || media.GetStorage() || media.GetProcessing() {
		t.Errorf("cluster media = %+v, want Quartermaster's verbs unchanged", media)
	}
	if reading.TenantErr != nil || reading.ClustersErr != nil {
		t.Errorf("healthy sources reported errors: %v / %v", reading.TenantErr, reading.ClustersErr)
	}

	// The operator grant belongs to the caller, not the tenant, so the same
	// resolver must re-read it per request instead of serving the cached
	// section's answer to a different identity.
	operatorCtxSameTenant := context.WithValue(capabilitiesCtx("tenant-1"), ctxkeys.KeyPlatformOperator, true)
	operatorReading, err := r.ReadCapabilities(operatorCtxSameTenant)
	if err != nil {
		t.Fatalf("ReadCapabilities as operator: %v", err)
	}
	if !operatorReading.Tenant.PlatformOperator {
		t.Error("platform operator grant not reported for an operator token; a cached section answered for a different caller")
	}
}

// A tier with no cap reports capped=false and no number, so a client cannot
// mistake "uncapped" for a zero-day limit.
func TestReadCapabilitiesReportsUncappedRetentionWithoutAMaximum(t *testing.T) {
	reading, err := capabilitiesResolver(&capabilitySources{clusters: demoClusterCapabilities()}).
		ReadCapabilities(capabilitiesCtx("tenant-1"))
	if err != nil {
		t.Fatalf("ReadCapabilities: %v", err)
	}
	if reading.Tenant.RecordingRetention.Capped || reading.Tenant.RecordingRetention.MaxDays != nil {
		t.Errorf("retention cap = %+v, want uncapped with no maximum", reading.Tenant.RecordingRetention)
	}
}

// Commodore gates tenant process overrides on the tier's features only, so a
// subscription override must not flip the reported gate.
func TestReadCapabilitiesIgnoresSubscriptionCustomFeaturesForProcessing(t *testing.T) {
	src := &capabilitySources{
		processingCustomizable:     false,
		subscriptionCustomFeatures: &purserpb.BillingFeatures{ProcessingCustomizable: true},
		clusters:                   demoClusterCapabilities(),
	}
	reading, err := capabilitiesResolver(src).ReadCapabilities(capabilitiesCtx("tenant-1"))
	if err != nil {
		t.Fatalf("ReadCapabilities: %v", err)
	}
	if reading.Tenant.ProcessingCustomizable {
		t.Error("subscription custom features flipped the processing gate; Commodore reads only the tier")
	}
}

// With no cached reading, a dead source leaves its section null and surfaces the
// error rather than inventing a value. A live source still answers.
func TestReadCapabilitiesLeavesASectionNullWhenItsSourceIsDownWithNoCache(t *testing.T) {
	outage := errors.New("quartermaster unavailable")
	reading, err := capabilitiesResolver(&capabilitySources{retentionDays: 7, clustersErr: outage}).
		ReadCapabilities(capabilitiesCtx("tenant-1"))
	if err != nil {
		t.Fatalf("ReadCapabilities: %v", err)
	}
	if reading.Clusters != nil || !errors.Is(reading.ClustersErr, outage) {
		t.Errorf("clusters = %+v, err = %v; want null with the source error", reading.Clusters, reading.ClustersErr)
	}
	if reading.Tenant != nil || !errors.Is(reading.TenantErr, outage) {
		t.Errorf("tenant = %+v, err = %v; want null because its domain gates come from Quartermaster", reading.Tenant, reading.TenantErr)
	}

	purserOutage := errors.New("purser unavailable")
	partial, err := capabilitiesResolver(&capabilitySources{retentionErr: purserOutage, clusters: demoClusterCapabilities()}).
		ReadCapabilities(capabilitiesCtx("tenant-1"))
	if err != nil {
		t.Fatalf("ReadCapabilities with a dead Purser: %v", err)
	}
	if partial.Tenant != nil || !errors.Is(partial.TenantErr, purserOutage) {
		t.Errorf("tenant = %+v, err = %v; want null with the Purser error", partial.Tenant, partial.TenantErr)
	}
	if len(partial.Clusters) != 1 || partial.ClustersErr != nil {
		t.Errorf("a dead Purser must not take the cluster section down: %+v / %v", partial.Clusters, partial.ClustersErr)
	}
}

// After the cached reading expires and the source is down, the last reading is
// served with the observation time it was read at, so a client can see how old
// the answer is.
func TestReadCapabilitiesServesTheLastReadingWithItsOriginalObservedAt(t *testing.T) {
	src := &capabilitySources{retentionDays: 14, processingCustomizable: true, clusters: demoClusterCapabilities()}
	r := capabilitiesResolver(src)
	start := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
	clock := start
	r.capabilityCache.now = func() time.Time { return clock }
	ctx := capabilitiesCtx("tenant-1")

	first, err := r.ReadCapabilities(ctx)
	if err != nil || first.Tenant == nil {
		t.Fatalf("first read = %+v, %v", first, err)
	}
	if !first.ObservedAt.Equal(start) {
		t.Fatalf("observedAt = %s, want the read time %s", first.ObservedAt, start)
	}

	outage := errors.New("purser unavailable")
	src.retentionErr = outage
	src.clustersErr = outage
	src.processingErr = outage
	clock = start.Add(capabilities.SectionTTL + time.Second)

	stale, err := r.ReadCapabilities(ctx)
	if err != nil {
		t.Fatalf("read during outage: %v", err)
	}
	if stale.Tenant == nil || stale.TenantErr != nil || stale.ClustersErr != nil {
		t.Fatalf("outage after a good read = %+v (tenant err %v, clusters err %v); want the last reading", stale, stale.TenantErr, stale.ClustersErr)
	}
	if !stale.Tenant.RecordingRetention.Capped || *stale.Tenant.RecordingRetention.MaxDays != 14 {
		t.Errorf("stale retention = %+v, want the last reading", stale.Tenant.RecordingRetention)
	}
	if !stale.ObservedAt.Equal(start) {
		t.Errorf("stale observedAt = %s, want the original %s, not the serving time", stale.ObservedAt, start)
	}
}

// The GraphQL shape turns an unreadable section into an explicit null plus an
// error, so "unknown" never reads as "not allowed".
func TestDoGetCapabilitiesReportsUnreadableSectionsAsNullWithAnError(t *testing.T) {
	outage := errors.New("quartermaster unavailable")
	r := capabilitiesResolver(&capabilitySources{retentionDays: 7, clustersErr: outage})
	ctx := graphql.WithResponseContext(capabilitiesCtx("tenant-1"),
		func(_ context.Context, err error) *gqlerror.Error { return gqlerror.WrapPath(nil, err) },
		func(_ context.Context, err any) error { return errors.New("panic") },
	)

	got, err := r.DoGetCapabilities(ctx)
	if err != nil {
		t.Fatalf("DoGetCapabilities: %v", err)
	}
	if got.Tenant != nil || got.Clusters != nil {
		t.Errorf("sections = %+v / %+v, want both null", got.Tenant, got.Clusters)
	}
	errs := graphql.GetErrors(ctx)
	if len(errs) != 2 {
		t.Fatalf("errors = %v, want one per unreadable section", errs)
	}
	for _, e := range errs {
		if !strings.Contains(e.Message, outage.Error()) {
			t.Errorf("error %q does not name the source failure", e.Message)
		}
	}
}

// capabilitiesCtx is an interactive session of a tenant owner.
func capabilitiesCtx(tenantID string) context.Context {
	return context.WithValue(clientstest.AuthedCtx(tenantID), ctxkeys.KeyUserID, "user-1")
}

func apiTokenCtx(tenantID string, permissions ...string) context.Context {
	ctx := context.WithValue(capabilitiesCtx(tenantID), ctxkeys.KeyAuthType, "api_token")
	return context.WithValue(ctx, ctxkeys.KeyPermissions, permissions)
}

// The capabilities query carries Quartermaster's placement reading, so Bridge
// applies Quartermaster's reader rule before it reads any cached section: an
// API token needs placement:read and an interactive caller needs
// media.placement.read on its tenant. A section another caller warmed must
// never reach a caller Quartermaster would refuse.
func TestReadCapabilitiesAppliesThePlacementReadRuleBeforeTheCache(t *testing.T) {
	r := capabilitiesResolver(&capabilitySources{retentionDays: 30, processingCustomizable: true, clusters: demoClusterCapabilities()})
	if reading, err := r.ReadCapabilities(capabilitiesCtx("tenant-1")); err != nil || reading.Tenant == nil {
		t.Fatalf("owner read = %+v, %v", reading, err)
	}

	denied := []struct {
		name string
		ctx  context.Context
		code codes.Code
	}{
		{"API token without placement:read", apiTokenCtx("tenant-1", "streams:read"), codes.PermissionDenied},
		{"session with no tenant role", context.WithValue(capabilitiesCtx("tenant-1"), ctxkeys.KeyRole, ""), codes.PermissionDenied},
		{"session with no user", clientstest.AuthedCtx("tenant-1"), codes.Unauthenticated},
	}
	for _, tc := range denied {
		t.Run(tc.name, func(t *testing.T) {
			reading, err := r.ReadCapabilities(tc.ctx)
			if reading != nil || status.Code(err) != tc.code {
				t.Fatalf("reading = %+v, err = %v; want no reading and %s", reading, err, tc.code)
			}
		})
	}

	if reading, err := r.ReadCapabilities(apiTokenCtx("tenant-1", "placement:read")); err != nil || reading.Tenant == nil {
		t.Fatalf("API token with placement:read = %+v, %v; want the reading", reading, err)
	}
}

// serverInfo is static process state: the release and the shipped feature
// slugs, with no tenant, cluster, commit, or build detail.
func TestServerInfoReportsTheReleaseAndShippedFeaturesOnly(t *testing.T) {
	info := (&Resolver{Clients: &clients.ServiceClients{}, Logger: clientstest.DiscardLogger()}).ServerInfo()
	if info.Version != version.Version {
		t.Errorf("version = %q, want %q", info.Version, version.Version)
	}
	want := platformfeatures.Shipped()
	if len(info.Features) != len(want) {
		t.Fatalf("features = %v, want the shipped registry slugs %v", info.Features, want)
	}
	for i := range want {
		if info.Features[i] != want[i] {
			t.Fatalf("features = %v, want %v", info.Features, want)
		}
	}
	// The player fallback keys off this slug.
	found := false
	for _, slug := range info.Features {
		if slug == "viewer-protocol-selection" {
			found = true
		}
	}
	if !found {
		t.Error("shipped features omit viewer-protocol-selection, which the player probes for")
	}
}
