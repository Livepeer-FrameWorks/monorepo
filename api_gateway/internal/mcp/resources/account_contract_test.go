package resources

import (
	"context"
	"testing"

	"frameworks/api_gateway/internal/clients/clientstest"
	"frameworks/api_gateway/internal/mcp/preflight"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
)

func accountTenantCtx() context.Context {
	return context.WithValue(context.Background(), ctxkeys.KeyTenantID, "tenant-1")
}

func TestAccountStatusTreatsZeroBalanceAsRatedBlockerNotBrokenAccount(t *testing.T) {
	purser := &clientstest.FakePurser{
		GetTenantBillingStatusFn: func(context.Context, string) (*purserpb.GetTenantBillingStatusResponse, error) {
			return &purserpb.GetTenantBillingStatusResponse{BillingModel: "prepaid"}, nil
		},
		GetPrepaidBalanceFn: func(context.Context, string, string) (*purserpb.PrepaidBalance, error) {
			return &purserpb.PrepaidBalance{}, nil
		},
		GetPaymentRequirementsFn: func(context.Context, string, string) (*purserpb.PaymentRequirements, error) {
			return &purserpb.PaymentRequirements{}, nil
		},
		GetBillingDetailsFn: func(context.Context, string) (*purserpb.BillingDetails, error) {
			return &purserpb.BillingDetails{}, nil
		},
	}
	serviceClients := clientstest.Clients(clientstest.WithPurser(purser))
	result, err := handleAccountStatus(accountTenantCtx(), serviceClients, preflight.NewChecker(serviceClients, clientstest.DiscardLogger()), clientstest.DiscardLogger())
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
	if !status.Capabilities["create_stream"] || status.Capabilities["create_clip"] {
		t.Fatalf("capabilities do not distinguish control/rated: %+v", status.Capabilities)
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
	}
	serviceClients := clientstest.Clients(clientstest.WithPurser(purser))
	result, err := handleAccountStatus(accountTenantCtx(), serviceClients, preflight.NewChecker(serviceClients, clientstest.DiscardLogger()), clientstest.DiscardLogger())
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
