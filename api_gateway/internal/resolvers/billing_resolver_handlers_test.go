package resolvers

import (
	"context"
	"errors"
	"testing"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients/clientstest"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
)

func purserResolver(p *clientstest.FakePurser) *Resolver {
	email := "verified@example.com"
	return &Resolver{
		Clients: clientstest.Clients(
			clientstest.WithPurser(p),
			clientstest.WithCommodore(&clientstest.FakeCommodore{
				GetMeFn: func(context.Context) (*commodorepb.User, error) {
					return &commodorepb.User{Email: &email, IsVerified: true}, nil
				},
			}),
		),
		Logger: clientstest.DiscardLogger(),
	}
}

func TestDoGetBillingTiers(t *testing.T) {
	r := purserResolver(&clientstest.FakePurser{
		GetBillingTiersFn: func(_ context.Context, includeInactive bool, _ *commonpb.CursorPaginationRequest) (*purserpb.GetBillingTiersResponse, error) {
			if includeInactive {
				t.Error("resolver should request active tiers only")
			}
			return &purserpb.GetBillingTiersResponse{Tiers: []*purserpb.BillingTier{{Id: "tier-1"}, {Id: "tier-2"}}}, nil
		},
	})
	tiers, err := r.DoGetBillingTiers(clientstest.AuthedCtx("t1"))
	if err != nil || len(tiers) != 2 || tiers[0].Id != "tier-1" {
		t.Fatalf("DoGetBillingTiers = (%+v, %v)", tiers, err)
	}

	failing := purserResolver(&clientstest.FakePurser{
		GetBillingTiersFn: func(context.Context, bool, *commonpb.CursorPaginationRequest) (*purserpb.GetBillingTiersResponse, error) {
			return nil, errors.New("purser down")
		},
	})
	if _, err := failing.DoGetBillingTiers(clientstest.AuthedCtx("t1")); err == nil {
		t.Fatal("DoGetBillingTiers should surface backend error")
	}
}

func TestDoGetInvoices(t *testing.T) {
	p := &clientstest.FakePurser{
		ListInvoicesFn: func(_ context.Context, tenantID string, _ *string, _ *commonpb.CursorPaginationRequest) (*purserpb.ListInvoicesResponse, error) {
			if tenantID != "t1" {
				t.Errorf("tenant not forwarded: %q", tenantID)
			}
			return &purserpb.ListInvoicesResponse{Invoices: []*purserpb.Invoice{{Id: "inv-1"}}}, nil
		},
	}
	r := purserResolver(p)
	invoices, err := r.DoGetInvoices(clientstest.AuthedCtx("t1"))
	if err != nil || len(invoices) != 1 || invoices[0].Id != "inv-1" {
		t.Fatalf("DoGetInvoices = (%+v, %v)", invoices, err)
	}

	// No tenant in context → error, and the backend is never consulted.
	guard := &clientstest.FakePurser{} // ListInvoices unstubbed → panics if reached
	rGuard := purserResolver(guard)
	if _, err := rGuard.DoGetInvoices(context.Background()); err == nil {
		t.Fatal("missing tenant should error")
	}
	if guard.Calls != 0 {
		t.Fatalf("backend consulted without tenant: %d calls", guard.Calls)
	}
}

func TestDoGetInvoice(t *testing.T) {
	// Found.
	r := purserResolver(&clientstest.FakePurser{
		GetInvoiceFn: func(_ context.Context, id string) (*purserpb.GetInvoiceResponse, error) {
			return &purserpb.GetInvoiceResponse{Invoice: &purserpb.Invoice{Id: id}}, nil
		},
	})
	inv, err := r.DoGetInvoice(clientstest.AuthedCtx("t1"), "inv-9")
	if err != nil || inv.Id != "inv-9" {
		t.Fatalf("DoGetInvoice = (%+v, %v)", inv, err)
	}

	// Nil invoice in a successful response → not-found error (not a nil deref).
	rNil := purserResolver(&clientstest.FakePurser{
		GetInvoiceFn: func(context.Context, string) (*purserpb.GetInvoiceResponse, error) {
			return &purserpb.GetInvoiceResponse{Invoice: nil}, nil
		},
	})
	if _, err := rNil.DoGetInvoice(clientstest.AuthedCtx("t1"), "ghost"); err == nil {
		t.Fatal("nil invoice should be a not-found error")
	}
}

// DoGetBillingStatus normalizes a sparse Purser response: it backfills the
// tenant ID from context and defaults an empty status to "active" so downstream
// never sees a blank billing state.
func TestDoGetBillingStatus_Normalizes(t *testing.T) {
	r := purserResolver(&clientstest.FakePurser{
		GetBillingStatusFn: func(context.Context, string) (*purserpb.BillingStatusResponse, error) {
			return &purserpb.BillingStatusResponse{}, nil // empty TenantId + BillingStatus
		},
	})
	status, err := r.DoGetBillingStatus(clientstest.AuthedCtx("t1"))
	if err != nil {
		t.Fatal(err)
	}
	if status.TenantId != "t1" {
		t.Errorf("tenant not backfilled: %q", status.TenantId)
	}
	if status.BillingStatus != "active" {
		t.Errorf("empty status not defaulted to active: %q", status.BillingStatus)
	}

	// Missing tenant → error before any backend call.
	guard := &clientstest.FakePurser{}
	if _, err := purserResolver(guard).DoGetBillingStatus(context.Background()); err == nil {
		t.Fatal("missing tenant should error")
	}
	if guard.Calls != 0 {
		t.Fatalf("backend consulted without tenant: %d calls", guard.Calls)
	}
}

func TestDoPromoteToPaidRequiresVerifiedEmail(t *testing.T) {
	email := "unverified@example.com"
	purser := &clientstest.FakePurser{
		PromoteToPaidFn: func(context.Context, string, string) (*purserpb.PromoteToPaidResponse, error) {
			t.Fatal("Purser must not be called for an unverified wallet account")
			return nil, nil
		},
	}
	r := &Resolver{
		Clients: clientstest.Clients(
			clientstest.WithPurser(purser),
			clientstest.WithCommodore(&clientstest.FakeCommodore{
				GetMeFn: func(context.Context) (*commodorepb.User, error) {
					return &commodorepb.User{Email: &email, IsVerified: false}, nil
				},
			}),
		),
		Logger: clientstest.DiscardLogger(),
	}

	result, err := r.DoPromoteToPaid(clientstest.AuthedCtx("t1"), "free-tier")
	if err != nil {
		t.Fatal(err)
	}
	validation, ok := result.(*model.ValidationError)
	if !ok || validation.Code == nil || *validation.Code != "VERIFIED_EMAIL_REQUIRED" {
		t.Fatalf("result=%#v, want VERIFIED_EMAIL_REQUIRED", result)
	}
	if purser.Calls != 0 {
		t.Fatalf("Purser called %d times for an unverified account", purser.Calls)
	}
}
