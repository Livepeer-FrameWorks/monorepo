package preflight

import (
	"context"
	"errors"
	"testing"

	"frameworks/api_gateway/internal/clients/clientstest"

	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
)

// solventBalance returns a FakePurser whose only stubbed call is a positive
// prepaid balance — enough for GetCapabilities' balance gate to pass.
func solventBalance() *clientstest.FakePurser {
	return &clientstest.FakePurser{
		GetPrepaidBalanceFn: func(context.Context, string, string) (*purserpb.PrepaidBalance, error) {
			return &purserpb.PrepaidBalance{BalanceCents: 500}, nil
		},
	}
}

func TestGetCapabilities_Solvent_AllEnabled(t *testing.T) {
	c := checkerWith(solventBalance())
	caps := c.GetCapabilities(ctxTenant("t1"))
	for _, k := range []string{"create_stream", "update_stream", "delete_stream", "create_clip", "start_dvr", "create_vod_upload", "complete_vod_upload", "delete_vod_asset"} {
		if !caps[k] {
			t.Fatalf("solvent tenant should have %q enabled", k)
		}
	}
}

func TestGetCapabilities_Broke_BillableDisabled_ReadsEnabled(t *testing.T) {
	c := checkerWith(&clientstest.FakePurser{
		GetPrepaidBalanceFn: func(context.Context, string, string) (*purserpb.PrepaidBalance, error) {
			return &purserpb.PrepaidBalance{BalanceCents: 0}, nil
		},
		GetPaymentRequirementsFn: func(context.Context, string, string) (*purserpb.PaymentRequirements, error) {
			return &purserpb.PaymentRequirements{}, nil
		},
	})
	caps := c.GetCapabilities(ctxTenant("t1"))
	for _, k := range []string{"create_stream", "update_stream", "delete_stream", "create_clip", "start_dvr", "create_vod_upload", "complete_vod_upload", "delete_vod_asset"} {
		if caps[k] {
			t.Fatalf("broke tenant must not have billable cap %q: %+v", k, caps)
		}
	}
	// Free reads and the recovery tools stay available even with zero balance.
	for _, k := range []string{"read_streams", "read_analytics", "read_billing", "read_vod", "topup_balance", "update_billing_details", "resolve_playback_endpoint", "validate_stream_key"} {
		if !caps[k] {
			t.Fatalf("free capability %q should stay enabled", k)
		}
	}
}

// CheckRateLimit is currently a placeholder that never blocks; pin that contract.
func TestCheckRateLimit_NeverBlocks(t *testing.T) {
	c := checkerWith(&clientstest.FakePurser{})
	b, err := c.CheckRateLimit(context.Background())
	if b != nil || err != nil {
		t.Fatalf("CheckRateLimit → (%v,%v), want (nil,nil)", b, err)
	}
}

// CheckBalance must hard-error (not panic, not block) when no tenant is present.
func TestCheckBalance_NoTenant_Errors(t *testing.T) {
	c := checkerWith(&clientstest.FakePurser{})
	if _, err := c.CheckBalance(context.Background()); err == nil {
		t.Fatal("expected error for missing tenant")
	}
}

// Balance fetch fails AND billing model is unknown → surface a hard error rather
// than silently passing or blocking.
func TestCheckBalance_UnknownModel_Errors(t *testing.T) {
	c := checkerWith(&clientstest.FakePurser{
		GetPrepaidBalanceFn: func(context.Context, string, string) (*purserpb.PrepaidBalance, error) {
			return nil, errors.New("no balance row")
		},
		GetTenantBillingStatusFn: func(context.Context, string) (*purserpb.GetTenantBillingStatusResponse, error) {
			return nil, errors.New("status unavailable")
		},
	})
	b, err := c.CheckBalance(ctxTenant("t1"))
	if err == nil {
		t.Fatal("expected error when balance and billing status both fail")
	}
	if b != nil {
		t.Fatalf("expected nil blocker on hard error, got %+v", b)
	}
}

// A hard balance error must propagate through RequireBalance unchanged (NOT as a
// PreflightError) so callers can distinguish "broke" from "backend broken".
func TestRequireBalance_HardErrorPropagates(t *testing.T) {
	c := checkerWith(&clientstest.FakePurser{})
	err := c.RequireBalance(context.Background())
	if err == nil {
		t.Fatal("expected hard error")
	}
	if _, ok := IsPreflightError(err); ok {
		t.Fatal("hard backend error should not be a PreflightError")
	}
}

func TestRequireBillingDetails_HardErrorPropagates(t *testing.T) {
	c := checkerWith(&clientstest.FakePurser{})
	err := c.RequireBillingDetails(context.Background())
	if err == nil {
		t.Fatal("expected hard error")
	}
	if _, ok := IsPreflightError(err); ok {
		t.Fatal("hard backend error should not be a PreflightError")
	}
}

func TestRequireBillingDetails_ClearReturnsNil(t *testing.T) {
	c := checkerWith(&clientstest.FakePurser{
		GetBillingDetailsFn: func(context.Context, string) (*purserpb.BillingDetails, error) {
			return &purserpb.BillingDetails{IsComplete: true}, nil
		},
	})
	if err := c.RequireBillingDetails(ctxTenant("t1")); err != nil {
		t.Fatalf("complete billing should pass, got %v", err)
	}
}

// In GetBlockers, a balance check that hard-errors (complete billing, but balance
// + status both unavailable) is logged and swallowed — the tenant is not falsely
// blocked, and no blocker is appended.
func TestGetBlockers_BalanceCheckError_Swallowed(t *testing.T) {
	c := checkerWith(&clientstest.FakePurser{
		GetBillingDetailsFn: func(context.Context, string) (*purserpb.BillingDetails, error) {
			return &purserpb.BillingDetails{IsComplete: true}, nil
		},
		GetPrepaidBalanceFn: func(context.Context, string, string) (*purserpb.PrepaidBalance, error) {
			return nil, errors.New("no balance row")
		},
		GetTenantBillingStatusFn: func(context.Context, string) (*purserpb.GetTenantBillingStatusResponse, error) {
			return nil, errors.New("status unavailable")
		},
	})
	bs, err := c.GetBlockers(ctxTenant("t1"))
	if err != nil {
		t.Fatalf("GetBlockers should not surface the swallowed balance error: %v", err)
	}
	if len(bs) != 0 {
		t.Fatalf("balance-check error must not produce a blocker, got %+v", bs)
	}
}
