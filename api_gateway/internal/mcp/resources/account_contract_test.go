package resources

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"testing"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/clients/clientstest"
	"frameworks/api_gateway/internal/mcp/preflight"
	"frameworks/api_gateway/internal/resolvers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/platformfeatures"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/version"
)

// accountTenantCtx is an interactive session of a tenant owner, which may
// read the tenant's capabilities.
func accountTenantCtx() context.Context {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "tenant-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, "user-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyAuthType, "jwt")
	return context.WithValue(ctx, ctxkeys.KeyRole, "owner")
}

// accountResolver wires the capability sources account://status reads alongside
// the billing fakes: the tier cap, the tier's processing flag, and the tenant's
// entitled clusters.
func accountResolver(serviceClients *clients.ServiceClients) *resolvers.Resolver {
	serviceClients.Quartermaster = accountQuartermaster{&clientstest.FakeQuartermaster{
		GetTenantClusterCapabilitiesFn: func(context.Context, string) (*quartermasterpb.GetTenantClusterCapabilitiesResponse, error) {
			return &quartermasterpb.GetTenantClusterCapabilitiesResponse{
				CustomSubdomain: true,
				Clusters: []*quartermasterpb.TenantClusterCapability{{
					ClusterId:   "cluster-eu",
					ClusterName: "EU",
					Role:        "preferred",
					AccessLevel: "shared",
					Media:       &quartermasterpb.ClusterMediaCapabilities{Ingest: true, Playback: true},
				}},
			}, nil
		},
	}}
	return &resolvers.Resolver{Clients: serviceClients, Logger: clientstest.DiscardLogger()}
}

// accountQuartermaster answers the rate-limit lookup account://status makes
// for a caller with a user.
type accountQuartermaster struct {
	*clientstest.FakeQuartermaster
}

func (accountQuartermaster) ValidateTenant(context.Context, string, string) (*quartermasterpb.ValidateTenantResponse, error) {
	return &quartermasterpb.ValidateTenantResponse{Valid: true, RateLimitPerMinute: 60}, nil
}

func TestAccountStatusTreatsZeroBalanceAsRatedBlockerNotBrokenAccount(t *testing.T) {
	purser := &clientstest.FakePurser{
		GetTenantBillingStatusFn: func(context.Context, string) (*purserpb.GetTenantBillingStatusResponse, error) {
			return &purserpb.GetTenantBillingStatusResponse{BillingModel: "prepaid"}, nil
		},
		GetPrepaidBalanceFn: func(context.Context, string) (*purserpb.PrepaidBalance, error) {
			return &purserpb.PrepaidBalance{}, nil
		},
		GetPaymentRequirementsFn: func(context.Context, string, string) (*purserpb.PaymentRequirements, error) {
			return &purserpb.PaymentRequirements{}, nil
		},
		GetBillingDetailsFn: func(context.Context, string) (*purserpb.BillingDetails, error) {
			return &purserpb.BillingDetails{}, nil
		},
		GetSubscriptionFn: func(context.Context, string) (*purserpb.GetSubscriptionResponse, error) {
			return &purserpb.GetSubscriptionResponse{Subscription: &purserpb.TenantSubscription{TierId: "tier-free"}}, nil
		},
		GetBillingTierFn: func(context.Context, string) (*purserpb.BillingTier, error) {
			return &purserpb.BillingTier{Id: "tier-free", Features: &purserpb.BillingFeatures{}}, nil
		},
	}
	serviceClients := clientstest.Clients(clientstest.WithPurser(purser))
	resolver := accountResolver(serviceClients)
	result, err := handleAccountStatus(accountTenantCtx(), serviceClients, resolver, preflight.NewChecker(serviceClients, clientstest.DiscardLogger()), clientstest.DiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	status := decodeResource[AccountStatus](t, result.Contents[0].Text)
	if !status.AccountReady || status.RatedWorkReady {
		t.Fatalf("readiness = account:%t rated:%t, want true/false", status.AccountReady, status.RatedWorkReady)
	}
	if len(status.Blockers) != 1 || status.Blockers[0].Code != "INSUFFICIENT_BALANCE" {
		t.Fatalf("blockers = %+v", status.Blockers)
	}
	if !status.ToolAccess["create_stream"] || status.ToolAccess["create_clip"] {
		t.Fatalf("tool access does not distinguish control/rated: %+v", status.ToolAccess)
	}
	if len(status.NextActions) != 2 || status.NextActions[0] != "top_up_prepaid_credit" {
		t.Fatalf("next actions = %+v", status.NextActions)
	}
}

func TestAccountStatusAllowsFreeRatedPreflightWithoutProvider(t *testing.T) {
	purser := &clientstest.FakePurser{
		GetTenantBillingStatusFn: func(context.Context, string) (*purserpb.GetTenantBillingStatusResponse, error) {
			return &purserpb.GetTenantBillingStatusResponse{BillingModel: "postpaid", TierName: "free"}, nil
		},
		GetBillingDetailsFn: func(context.Context, string) (*purserpb.BillingDetails, error) {
			return &purserpb.BillingDetails{}, nil
		},
		GetSubscriptionFn: func(context.Context, string) (*purserpb.GetSubscriptionResponse, error) {
			return &purserpb.GetSubscriptionResponse{Subscription: &purserpb.TenantSubscription{TierId: "tier-free"}}, nil
		},
		GetBillingTierFn: func(context.Context, string) (*purserpb.BillingTier, error) {
			return &purserpb.BillingTier{Id: "tier-free", Features: &purserpb.BillingFeatures{}}, nil
		},
	}
	serviceClients := clientstest.Clients(clientstest.WithPurser(purser))
	resolver := accountResolver(serviceClients)
	result, err := handleAccountStatus(accountTenantCtx(), serviceClients, resolver, preflight.NewChecker(serviceClients, clientstest.DiscardLogger()), clientstest.DiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	status := decodeResource[AccountStatus](t, result.Contents[0].Text)
	if !status.AccountReady || !status.RatedWorkReady || len(status.Blockers) != 0 {
		t.Fatalf("Free account status = %+v", status)
	}
	if status.Billing.TierName != "free" || status.Billing.CollectionReady {
		t.Fatalf("Free billing status = %+v", status.Billing)
	}
}

// account://status carries what the server supports and which platform gates
// are open, next to the per-tool access map, so an agent reads both from one
// resource without guessing either.
func TestAccountStatusReportsServerInfoAndEnforcedCapabilities(t *testing.T) {
	maxDays := int32(30)
	purser := &clientstest.FakePurser{
		GetTenantBillingStatusFn: func(context.Context, string) (*purserpb.GetTenantBillingStatusResponse, error) {
			return &purserpb.GetTenantBillingStatusResponse{BillingModel: "postpaid", TierName: "free", RecordingRetentionDays: maxDays}, nil
		},
		GetBillingDetailsFn: func(context.Context, string) (*purserpb.BillingDetails, error) {
			return &purserpb.BillingDetails{}, nil
		},
		GetSubscriptionFn: func(context.Context, string) (*purserpb.GetSubscriptionResponse, error) {
			return &purserpb.GetSubscriptionResponse{Subscription: &purserpb.TenantSubscription{TierId: "tier-free"}}, nil
		},
		GetBillingTierFn: func(context.Context, string) (*purserpb.BillingTier, error) {
			return &purserpb.BillingTier{Id: "tier-free", Features: &purserpb.BillingFeatures{ProcessingCustomizable: true}}, nil
		},
	}
	serviceClients := clientstest.Clients(clientstest.WithPurser(purser))
	resolver := accountResolver(serviceClients)
	result, err := handleAccountStatus(accountTenantCtx(), serviceClients, resolver, preflight.NewChecker(serviceClients, clientstest.DiscardLogger()), clientstest.DiscardLogger())
	if err != nil {
		t.Fatal(err)
	}

	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal([]byte(result.Contents[0].Text), &raw); err != nil {
		t.Fatalf("decode account status: %v", err)
	}
	if _, ok := raw["tool_access"]; !ok {
		t.Errorf("account status has no tool_access key: %v", keysOf(raw))
	}
	if _, ok := raw["server_info"]; !ok {
		t.Errorf("account status has no server_info key: %v", keysOf(raw))
	}

	status := decodeResource[AccountStatus](t, result.Contents[0].Text)
	if status.ServerInfo.Version != version.Version || len(status.ServerInfo.Features) != len(platformfeatures.Shipped()) {
		t.Errorf("server info = %+v, want this build's release and shipped features", status.ServerInfo)
	}
	if status.Capabilities == nil || status.Capabilities.Tenant == nil {
		t.Fatalf("capabilities = %+v, want the enforced gates", status.Capabilities)
	}
	tenant := status.Capabilities.Tenant
	if !tenant.RecordingRetentionCapped || tenant.RecordingRetentionMaxDays == nil || *tenant.RecordingRetentionMaxDays != int(maxDays) {
		t.Errorf("retention gate = %+v, want capped at %d days", tenant, maxDays)
	}
	if !tenant.ProcessingCustomizable || !tenant.CustomSubdomain || tenant.CustomDomain {
		t.Errorf("tenant gates = %+v, want the tier and Quartermaster answers", tenant)
	}
	if len(status.Capabilities.Clusters) != 1 || !status.Capabilities.Clusters[0].Ingest || status.Capabilities.Clusters[0].Storage {
		t.Errorf("cluster gates = %+v, want Quartermaster's verbs unchanged", status.Capabilities.Clusters)
	}
	if len(status.Capabilities.Unavailable) != 0 {
		t.Errorf("healthy sources reported as unavailable: %v", status.Capabilities.Unavailable)
	}
}

// When a capability source is down the section is omitted and named, never
// filled with a default that would read as a closed gate.
func TestAccountStatusNamesUnreadableCapabilitySectionsInsteadOfGuessing(t *testing.T) {
	purser := &clientstest.FakePurser{
		GetTenantBillingStatusFn: func(context.Context, string) (*purserpb.GetTenantBillingStatusResponse, error) {
			return &purserpb.GetTenantBillingStatusResponse{BillingModel: "postpaid", TierName: "free"}, nil
		},
		GetBillingDetailsFn: func(context.Context, string) (*purserpb.BillingDetails, error) {
			return &purserpb.BillingDetails{}, nil
		},
		GetSubscriptionFn: func(context.Context, string) (*purserpb.GetSubscriptionResponse, error) {
			return &purserpb.GetSubscriptionResponse{Subscription: &purserpb.TenantSubscription{TierId: "tier-free"}}, nil
		},
		GetBillingTierFn: func(context.Context, string) (*purserpb.BillingTier, error) {
			return &purserpb.BillingTier{Id: "tier-free", Features: &purserpb.BillingFeatures{}}, nil
		},
	}
	serviceClients := clientstest.Clients(clientstest.WithPurser(purser))
	serviceClients.Quartermaster = accountQuartermaster{&clientstest.FakeQuartermaster{
		GetTenantClusterCapabilitiesFn: func(context.Context, string) (*quartermasterpb.GetTenantClusterCapabilitiesResponse, error) {
			return nil, errors.New("quartermaster unavailable")
		},
	}}
	resolver := &resolvers.Resolver{Clients: serviceClients, Logger: clientstest.DiscardLogger()}

	result, err := handleAccountStatus(accountTenantCtx(), serviceClients, resolver, preflight.NewChecker(serviceClients, clientstest.DiscardLogger()), clientstest.DiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	status := decodeResource[AccountStatus](t, result.Contents[0].Text)
	if status.Capabilities == nil {
		t.Fatal("capabilities missing entirely; want the readable part plus the unavailable sections")
	}
	if status.Capabilities.Tenant != nil || len(status.Capabilities.Clusters) != 0 {
		t.Errorf("unreadable sections were filled in: %+v", status.Capabilities)
	}
	if len(status.Capabilities.Unavailable) != 2 {
		t.Errorf("unavailable = %v, want both the tenant and cluster sections named", status.Capabilities.Unavailable)
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
