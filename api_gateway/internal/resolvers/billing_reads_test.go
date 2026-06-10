package resolvers

import (
	"context"
	"testing"

	"frameworks/api_gateway/internal/clients/clientstest"

	"frameworks/api_gateway/graph/model"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestDoGetBillingDetails(t *testing.T) {
	r := purserResolver(&clientstest.FakePurser{
		GetBillingDetailsFn: func(_ context.Context, tenantID string) (*purserpb.BillingDetails, error) {
			return &purserpb.BillingDetails{TenantId: tenantID, IsComplete: true}, nil
		},
	})
	got, err := r.DoGetBillingDetails(clientstest.AuthedCtx("t1"))
	if err != nil || got == nil || !got.IsComplete {
		t.Fatalf("DoGetBillingDetails = (%+v, %v)", got, err)
	}

	guard := &clientstest.FakePurser{}
	if _, err := purserResolver(guard).DoGetBillingDetails(context.Background()); err == nil {
		t.Fatal("missing tenant should error")
	}
	if guard.Calls != 0 {
		t.Fatalf("backend consulted without tenant: %d calls", guard.Calls)
	}
}

// DoGetInvoicePreview asks Purser for the single most-recent DRAFT invoice and
// returns it (or nil when none exists yet).
func TestDoGetInvoicePreview(t *testing.T) {
	r := purserResolver(&clientstest.FakePurser{
		ListInvoicesFn: func(_ context.Context, _ string, statusFilter *string, p *commonpb.CursorPaginationRequest) (*purserpb.ListInvoicesResponse, error) {
			if statusFilter == nil || *statusFilter != "draft" {
				t.Errorf("preview must filter to draft invoices, got %v", statusFilter)
			}
			if p == nil || p.First != 1 {
				t.Errorf("preview must request a single invoice, got %+v", p)
			}
			return &purserpb.ListInvoicesResponse{Invoices: []*purserpb.Invoice{{Id: "draft-1"}}}, nil
		},
	})
	inv, err := r.DoGetInvoicePreview(clientstest.AuthedCtx("t1"))
	if err != nil || inv == nil || inv.Id != "draft-1" {
		t.Fatalf("DoGetInvoicePreview = (%+v, %v)", inv, err)
	}

	// No draft invoice yet → (nil, nil), not an error.
	rEmpty := purserResolver(&clientstest.FakePurser{
		ListInvoicesFn: func(context.Context, string, *string, *commonpb.CursorPaginationRequest) (*purserpb.ListInvoicesResponse, error) {
			return &purserpb.ListInvoicesResponse{}, nil
		},
	})
	inv, err = rEmpty.DoGetInvoicePreview(clientstest.AuthedCtx("t1"))
	if err != nil || inv != nil {
		t.Fatalf("empty preview should be (nil,nil), got (%+v, %v)", inv, err)
	}
}

// DoGetPrepaidBalance: NotFound is an expected "no prepaid wallet" state and must
// map to (nil, nil), NOT a propagated error. Other gRPC errors propagate.
func TestDoGetPrepaidBalance_NotFoundIsNotError(t *testing.T) {
	r := purserResolver(&clientstest.FakePurser{
		GetPrepaidBalanceFn: func(context.Context, string, string) (*purserpb.PrepaidBalance, error) {
			return nil, status.Error(codes.NotFound, "no balance")
		},
	})
	got, err := r.DoGetPrepaidBalance(clientstest.AuthedCtx("t1"), nil)
	if err != nil {
		t.Fatalf("NotFound should not be an error: %v", err)
	}
	if got != nil {
		t.Fatalf("NotFound should yield nil balance, got %+v", got)
	}

	rErr := purserResolver(&clientstest.FakePurser{
		GetPrepaidBalanceFn: func(context.Context, string, string) (*purserpb.PrepaidBalance, error) {
			return nil, status.Error(codes.Internal, "boom")
		},
	})
	if _, err := rErr.DoGetPrepaidBalance(clientstest.AuthedCtx("t1"), nil); err == nil {
		t.Fatal("non-NotFound error should propagate")
	}
}

// The currency override is forwarded to Purser; fields map straight through.
func TestDoGetPrepaidBalance_CurrencyOverrideAndMapping(t *testing.T) {
	var gotCurrency string
	r := purserResolver(&clientstest.FakePurser{
		GetPrepaidBalanceFn: func(_ context.Context, _ string, currency string) (*purserpb.PrepaidBalance, error) {
			gotCurrency = currency
			return &purserpb.PrepaidBalance{Id: "b1", BalanceCents: 100, Currency: currency, IsLowBalance: true}, nil
		},
	})
	usd := "USD"
	got, err := r.DoGetPrepaidBalance(clientstest.AuthedCtx("t1"), &usd)
	if err != nil || got == nil {
		t.Fatalf("DoGetPrepaidBalance = (%+v, %v)", got, err)
	}
	if got.BalanceCents != 100 || got.Currency != "USD" || !got.IsLowBalance {
		t.Errorf("balance fields not mapped: %+v", got)
	}
	if gotCurrency != "USD" {
		t.Errorf("currency override not forwarded: %q", gotCurrency)
	}
}

// DoUpdateBillingDetails maps the GraphQL input into the Purser proto request,
// including the optional Address.State pointer, and forwards the tenant from ctx.
func TestDoUpdateBillingDetails(t *testing.T) {
	var gotReq *purserpb.UpdateBillingDetailsRequest
	r := purserResolver(&clientstest.FakePurser{
		UpdateBillingDetailsFn: func(_ context.Context, req *purserpb.UpdateBillingDetailsRequest) (*purserpb.BillingDetails, error) {
			gotReq = req
			return &purserpb.BillingDetails{TenantId: req.TenantId, IsComplete: true}, nil
		},
	})
	email := "ops@example.com"
	state := "Berlin"
	got, err := r.DoUpdateBillingDetails(clientstest.AuthedCtx("tenant-9"), model.UpdateBillingDetailsInput{
		Email: &email,
		Address: &model.BillingAddressInput{
			Street: "1 Main", City: "Berlin", PostalCode: "10115", Country: "DE", State: &state,
		},
	})
	if err != nil || got == nil || !got.IsComplete {
		t.Fatalf("DoUpdateBillingDetails = (%+v, %v)", got, err)
	}
	if gotReq.TenantId != "tenant-9" {
		t.Errorf("tenant not forwarded: %q", gotReq.TenantId)
	}
	if gotReq.Email == nil || *gotReq.Email != "ops@example.com" {
		t.Errorf("email not mapped: %v", gotReq.Email)
	}
	if gotReq.Address == nil || gotReq.Address.State != "Berlin" || gotReq.Address.Country != "DE" {
		t.Errorf("address not mapped (incl. optional state): %+v", gotReq.Address)
	}

	guard := &clientstest.FakePurser{}
	if _, err := purserResolver(guard).DoUpdateBillingDetails(context.Background(), model.UpdateBillingDetailsInput{}); err == nil {
		t.Fatal("missing tenant should error")
	}
	if guard.Calls != 0 {
		t.Fatalf("backend consulted without tenant: %d calls", guard.Calls)
	}
}
