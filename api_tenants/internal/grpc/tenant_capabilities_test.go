package grpc

import (
	"context"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestClusterMediaCapabilitiesNeedConsentAndFreshReport(t *testing.T) {
	allFresh := map[string]bool{edgeIngestServiceType: true, edgeEgressServiceType: true, edgeStorageServiceType: true, edgeProcessingServiceType: true}
	for _, test := range []struct {
		name    string
		consent *placementpb.CapacityConsent
		fresh   map[string]bool
		want    *quartermasterpb.ClusterMediaCapabilities
	}{
		{"full consent and reports", &placementpb.CapacityConsent{AllowIngest: true, AllowServe: true}, allFresh,
			&quartermasterpb.ClusterMediaCapabilities{Ingest: true, Playback: true, Storage: true, Processing: true}},
		{"no fresh reports", &placementpb.CapacityConsent{AllowIngest: true, AllowServe: true}, nil,
			&quartermasterpb.ClusterMediaCapabilities{}},
		{"serve only", &placementpb.CapacityConsent{AllowServe: true}, allFresh,
			&quartermasterpb.ClusterMediaCapabilities{Playback: true, Storage: true, Processing: true}},
		{"no consent verb", &placementpb.CapacityConsent{AllowExternalSource: true}, allFresh,
			&quartermasterpb.ClusterMediaCapabilities{}},
		{"ingest report only", &placementpb.CapacityConsent{AllowIngest: true, AllowServe: true}, map[string]bool{edgeIngestServiceType: true},
			&quartermasterpb.ClusterMediaCapabilities{Ingest: true}},
	} {
		got := clusterMediaCapabilities(test.consent, test.fresh)
		if got.GetIngest() != test.want.GetIngest() || got.GetPlayback() != test.want.GetPlayback() ||
			got.GetStorage() != test.want.GetStorage() || got.GetProcessing() != test.want.GetProcessing() {
			t.Errorf("%s: got %+v, want %+v", test.name, got, test.want)
		}
	}
}

func TestTenantDomainEligibility(t *testing.T) {
	eligible := tenantDomainEligibility{EntitlementsObserved: true, Active: true, CustomSubdomainEnabled: true, CustomDomainEnabled: true, HasActiveCluster: true}
	if !eligible.customSubdomain() || !eligible.customDomain() {
		t.Fatalf("fully eligible tenant = %+v", eligible)
	}
	for name, mutate := range map[string]func(*tenantDomainEligibility){
		"unobserved": func(e *tenantDomainEligibility) { e.EntitlementsObserved = false },
		"inactive":   func(e *tenantDomainEligibility) { e.Active = false },
		"subdomain":  func(e *tenantDomainEligibility) { e.CustomSubdomainEnabled = false },
		"no cluster": func(e *tenantDomainEligibility) { e.HasActiveCluster = false },
	} {
		e := eligible
		mutate(&e)
		if e.customSubdomain() || e.customDomain() {
			t.Errorf("%s: subdomain=%v domain=%v, want both false", name, e.customSubdomain(), e.customDomain())
		}
	}
	domainOff := eligible
	domainOff.CustomDomainEnabled = false
	if !domainOff.customSubdomain() || domainOff.customDomain() {
		t.Errorf("custom domain disabled: subdomain=%v domain=%v", domainOff.customSubdomain(), domainOff.customDomain())
	}
}

func TestAuthorizeTenantCapabilityReader(t *testing.T) {
	const tenant = "11111111-1111-4111-8111-111111111191"
	user := func(tenantID, role string) context.Context {
		return context.WithValue(tenantCtx(tenantID, role), ctxkeys.KeyUserID, "user-1")
	}
	token := func(permissions ...string) context.Context {
		ctx := context.WithValue(user(tenant, "member"), ctxkeys.KeyAuthType, "api_token")
		return context.WithValue(ctx, ctxkeys.KeyPermissions, permissions)
	}
	for _, test := range []struct {
		name string
		ctx  context.Context
		want codes.Code
	}{
		{"service", context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service"), codes.OK},
		{"member", user(tenant, "member"), codes.OK},
		{"viewer", user(tenant, "viewer"), codes.OK},
		{"other tenant owner", user("11111111-1111-4111-8111-111111111192", "owner"), codes.PermissionDenied},
		{"unknown role", user(tenant, "guest"), codes.PermissionDenied},
		{"token with placement read", token("placement:read"), codes.OK},
		{"token without placement read", token("streams:read"), codes.PermissionDenied},
		{"anonymous", context.Background(), codes.Unauthenticated},
	} {
		if got := status.Code(authorizeTenantCapabilityReader(test.ctx, tenant)); got != test.want {
			t.Errorf("%s: code = %v, want %v", test.name, got, test.want)
		}
	}
}
