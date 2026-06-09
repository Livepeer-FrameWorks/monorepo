package preflight

import (
	"context"
	"errors"
	"testing"

	"frameworks/api_gateway/internal/clients/clientstest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
)

func ctxTenant(id string) context.Context {
	return context.WithValue(context.Background(), ctxkeys.KeyTenantID, id)
}

func checkerWith(p *clientstest.FakePurser) *Checker {
	return NewChecker(clientstest.Clients(clientstest.WithPurser(p)), clientstest.DiscardLogger())
}

func TestCheckBillingDetails(t *testing.T) {
	// Purser error → treated as incomplete (a blocker, not a hard error).
	c := checkerWith(&clientstest.FakePurser{
		GetBillingDetailsFn: func(context.Context, string) (*purserpb.BillingDetails, error) {
			return nil, errors.New("not found")
		},
	})
	b, err := c.CheckBillingDetails(ctxTenant("t1"))
	if err != nil || b == nil || b.Code != "BILLING_DETAILS_MISSING" {
		t.Fatalf("missing details → (%v,%v)", b, err)
	}

	// Incomplete details → blocker.
	c = checkerWith(&clientstest.FakePurser{
		GetBillingDetailsFn: func(context.Context, string) (*purserpb.BillingDetails, error) {
			return &purserpb.BillingDetails{IsComplete: false}, nil
		},
	})
	if b, _ := c.CheckBillingDetails(ctxTenant("t1")); b == nil {
		t.Fatal("incomplete details should block")
	}

	// Complete details → no blocker.
	c = checkerWith(&clientstest.FakePurser{
		GetBillingDetailsFn: func(context.Context, string) (*purserpb.BillingDetails, error) {
			return &purserpb.BillingDetails{IsComplete: true}, nil
		},
	})
	if b, err := c.CheckBillingDetails(ctxTenant("t1")); b != nil || err != nil {
		t.Fatalf("complete details → (%v,%v), want (nil,nil)", b, err)
	}

	// No tenant → hard error.
	if _, err := c.CheckBillingDetails(context.Background()); err == nil {
		t.Fatal("missing tenant should error")
	}
}

func TestCheckBalance(t *testing.T) {
	// Positive balance → no blocker.
	c := checkerWith(&clientstest.FakePurser{
		GetPrepaidBalanceFn: func(context.Context, string, string) (*purserpb.PrepaidBalance, error) {
			return &purserpb.PrepaidBalance{BalanceCents: 1000}, nil
		},
	})
	if b, err := c.CheckBalance(ctxTenant("t1")); b != nil || err != nil {
		t.Fatalf("positive balance → (%v,%v)", b, err)
	}

	// Zero balance → INSUFFICIENT_BALANCE blocker, with x402 options attached.
	c = checkerWith(&clientstest.FakePurser{
		GetPrepaidBalanceFn: func(context.Context, string, string) (*purserpb.PrepaidBalance, error) {
			return &purserpb.PrepaidBalance{BalanceCents: 0}, nil
		},
		GetPaymentRequirementsFn: func(context.Context, string, string) (*purserpb.PaymentRequirements, error) {
			return &purserpb.PaymentRequirements{Accepts: []*purserpb.PaymentRequirement{
				{Network: "base", Asset: "USDC", PayTo: "0xabc", Description: "pay"},
			}}, nil
		},
	})
	b, err := c.CheckBalance(ctxTenant("t1"))
	if err != nil || b == nil || b.Code != "INSUFFICIENT_BALANCE" {
		t.Fatalf("zero balance → (%v,%v)", b, err)
	}
	if len(b.X402Accepts) != 1 || b.X402Accepts[0].Network != "base" {
		t.Fatalf("expected x402 accepts attached, got %+v", b.X402Accepts)
	}

	// Balance fetch error + postpaid model → balance check is skipped (no blocker).
	c = checkerWith(&clientstest.FakePurser{
		GetPrepaidBalanceFn: func(context.Context, string, string) (*purserpb.PrepaidBalance, error) {
			return nil, errors.New("no balance row")
		},
		GetTenantBillingStatusFn: func(context.Context, string) (*purserpb.GetTenantBillingStatusResponse, error) {
			return &purserpb.GetTenantBillingStatusResponse{BillingModel: "postpaid"}, nil
		},
	})
	if b, err := c.CheckBalance(ctxTenant("t1")); b != nil || err != nil {
		t.Fatalf("postpaid → (%v,%v), want (nil,nil)", b, err)
	}

	// Balance fetch error + prepaid model → treated as 0 → blocker (no x402 here).
	c = checkerWith(&clientstest.FakePurser{
		GetPrepaidBalanceFn: func(context.Context, string, string) (*purserpb.PrepaidBalance, error) {
			return nil, errors.New("no balance row")
		},
		GetTenantBillingStatusFn: func(context.Context, string) (*purserpb.GetTenantBillingStatusResponse, error) {
			return &purserpb.GetTenantBillingStatusResponse{BillingModel: "prepaid"}, nil
		},
		GetPaymentRequirementsFn: func(context.Context, string, string) (*purserpb.PaymentRequirements, error) {
			return nil, errors.New("x402 unavailable")
		},
	})
	if b, _ := c.CheckBalance(ctxTenant("t1")); b == nil || b.Code != "INSUFFICIENT_BALANCE" {
		t.Fatalf("prepaid w/ missing balance should block, got %v", b)
	}
}

func TestGetBlockers(t *testing.T) {
	// No tenant → single AUTH blocker, short-circuits before any Purser call.
	c := NewChecker(clientstest.Clients(clientstest.WithPurser(&clientstest.FakePurser{})), clientstest.DiscardLogger())
	bs, err := c.GetBlockers(context.Background())
	if err != nil || len(bs) != 1 || bs[0].Code != "AUTHENTICATION_REQUIRED" {
		t.Fatalf("no tenant → %v (%v)", bs, err)
	}

	// Incomplete billing → balance check is skipped (only the billing blocker).
	c = checkerWith(&clientstest.FakePurser{
		GetBillingDetailsFn: func(context.Context, string) (*purserpb.BillingDetails, error) {
			return &purserpb.BillingDetails{IsComplete: false}, nil
		},
	})
	bs, err = c.GetBlockers(ctxTenant("t1"))
	if err != nil || len(bs) != 1 || bs[0].Code != "BILLING_DETAILS_MISSING" {
		t.Fatalf("incomplete billing → %v (%v)", bs, err)
	}

	// Complete billing + sufficient balance → no blockers.
	c = checkerWith(clientstest.SolventPurser())
	if bs, err := c.GetBlockers(ctxTenant("t1")); err != nil || len(bs) != 0 {
		t.Fatalf("solvent → %v (%v), want none", bs, err)
	}
}

func TestRequireBalanceAndBillingWrapBlockers(t *testing.T) {
	c := checkerWith(clientstest.SolventPurser())
	if err := c.RequireBillingAndBalance(ctxTenant("t1")); err != nil {
		t.Fatalf("solvent should pass: %v", err)
	}

	// Insufficient balance → PreflightError.
	c = checkerWith(&clientstest.FakePurser{
		GetBillingDetailsFn: func(context.Context, string) (*purserpb.BillingDetails, error) {
			return &purserpb.BillingDetails{IsComplete: true}, nil
		},
		GetPrepaidBalanceFn: func(context.Context, string, string) (*purserpb.PrepaidBalance, error) {
			return &purserpb.PrepaidBalance{BalanceCents: 0}, nil
		},
		GetPaymentRequirementsFn: func(context.Context, string, string) (*purserpb.PaymentRequirements, error) {
			return nil, errors.New("x402 unavailable")
		},
	})
	err := c.RequireBalance(ctxTenant("t1"))
	if pfe, ok := IsPreflightError(err); !ok || pfe.Blocker.Code != "INSUFFICIENT_BALANCE" {
		t.Fatalf("want preflight error, got %v", err)
	}
}
