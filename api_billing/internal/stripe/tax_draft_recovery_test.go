package stripe

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	billingtax "frameworks/api_billing/internal/tax"
	api "github.com/stripe/stripe-go/v85"
)

type heldDraftBackend struct {
	fakeBackend
	inv                     *api.Invoice
	failAfter               string
	writes, finalized, paid int
}

func (b *heldDraftBackend) CallRaw(method, path, key string, body []byte, p *api.Params, out api.LastResponseSetter) error {
	if list, ok := out.(*api.InvoiceList); ok {
		list.Data = []*api.Invoice{b.inv}
		return nil
	}
	return b.fakeBackend.CallRaw(method, path, key, body, p, out)
}

func (b *heldDraftBackend) Call(method, path, key string, params api.ParamsContainer, out api.LastResponseSetter) error {
	if method != http.MethodPost {
		return errors.New("unexpected non-mutating invoice request")
	}
	b.writes++
	stage := ""
	switch p := params.(type) {
	case *api.InvoiceParams:
		stage = "update"
		if api.BoolValue(p.AutoAdvance) || len(p.DefaultTaxRates) != 1 || api.StringValue(p.DefaultTaxRates[0]) != "vat21" {
			return errors.New("recovery enabled automatic collection or lost current exclusive rate")
		}
		b.inv.Metadata = p.Metadata
	case *api.InvoiceUpdateLinesParams:
		stage = "lines"
		if len(p.Lines) != 1 || api.StringValue(p.Lines[0].ID) != "base" || len(p.Lines[0].TaxRates) != 1 || api.StringValue(p.Lines[0].TaxRates[0]) != "vat21" || p.Lines[0].Amount != nil {
			return errors.New("recovery changed original net or omitted explicit line rate")
		}
		b.inv.Total = 12100
	case *api.InvoiceFinalizeInvoiceParams:
		stage = "finalize"
		if api.BoolValue(p.AutoAdvance) || b.inv.Total != 12100 || b.inv.Metadata["frameworks_tax_profile"] == "" {
			return errors.New("recovery finalized without retained tax setup")
		}
		b.finalized++
		b.inv.Status = api.InvoiceStatusOpen
	case *api.InvoicePayParams:
		stage = "pay"
		if b.inv.Status != api.InvoiceStatusOpen || b.inv.Total != 12100 {
			return errors.New("recovery collected wrong invoice or gross")
		}
		b.paid++
		b.inv.Status = api.InvoiceStatusPaid
	default:
		return errors.New("unexpected invoice recovery mutation " + path)
	}
	*out.(*api.Invoice) = *b.inv
	if b.failAfter == stage {
		b.failAfter = ""
		return errors.New("provider committed but response lost")
	}
	return nil
}

func heldTaxFixture() (*api.Subscription, *api.Invoice) {
	meta := map[string]string{"tenant_id": "tenant", "frameworks_tax_review": "true", "frameworks_tax_pause_owned": "true", "frameworks_tax_hold_started": "100"}
	sub := &api.Subscription{ID: "sub", Customer: &api.Customer{ID: "customer"}, Metadata: meta, PauseCollection: &api.SubscriptionPauseCollection{Behavior: api.SubscriptionPauseCollectionBehaviorKeepAsDraft}}
	inv := &api.Invoice{ID: "same-held-invoice", Customer: sub.Customer, Created: 101, Currency: api.CurrencyEUR, Status: api.InvoiceStatusDraft, BillingReason: api.InvoiceBillingReasonSubscriptionCycle, Parent: &api.InvoiceParent{SubscriptionDetails: &api.InvoiceParentSubscriptionDetails{Metadata: meta}}, Lines: &api.InvoiceLineItemList{Data: []*api.InvoiceLineItem{{ID: "base", Amount: 10000, Currency: api.CurrencyEUR, Parent: &api.InvoiceLineItemParent{SubscriptionItemDetails: &api.InvoiceLineItemParentSubscriptionItemDetails{Subscription: "sub", SubscriptionItem: "item"}}}}}}
	return sub, inv
}

func TestOwnedTaxHoldDraftRecoversSameInvoiceAfterEveryLostResponse(t *testing.T) {
	for _, stage := range []string{"update", "lines", "finalize", "pay"} {
		t.Run(stage, func(t *testing.T) {
			sub, inv := heldTaxFixture()
			b := &heldDraftBackend{inv: inv, failAfter: stage}
			installBackend(t, b)
			if err := recoverStripeTaxInvoices(t.Context(), sub, assessedTaxProfile(), "vat21"); err == nil {
				t.Fatal("lost committed response not exercised")
			}
			if err := recoverStripeTaxInvoices(t.Context(), sub, assessedTaxProfile(), "vat21"); err != nil {
				t.Fatal(err)
			}
			if inv.ID != "same-held-invoice" || inv.Status != api.InvoiceStatusPaid || inv.Lines.Data[0].Amount != 10000 || inv.Total != 12100 || b.finalized != 1 || b.paid != 1 {
				t.Fatalf("recovery changed invoice/credit/payment: %+v finalized%d paid%d", inv, b.finalized, b.paid)
			}
		})
	}
}

func TestTaxHoldRecoveryRefusesUnownedAdjustedAndChangedFinalizedInvoices(t *testing.T) {
	for _, mode := range []string{"operator pause", "missing ownership", "unrelated credit", "changed finalized terms"} {
		t.Run(mode, func(t *testing.T) {
			sub, inv := heldTaxFixture()
			switch mode {
			case "operator pause":
				sub.PauseCollection = &api.SubscriptionPauseCollection{Behavior: api.SubscriptionPauseCollectionBehaviorVoid}
			case "missing ownership":
				inv.Parent.SubscriptionDetails.Metadata = map[string]string{}
			case "unrelated credit":
				inv.Lines.Data[0].Amount = -100
			case "changed finalized terms":
				inv.Status = api.InvoiceStatusOpen
				inv.Metadata = map[string]string{"frameworks_tax_recovered_hold": "true", "frameworks_tax_profile": "different-legal-terms"}
			}
			b := &heldDraftBackend{inv: inv}
			installBackend(t, b)
			err := recoverStripeTaxInvoices(t.Context(), sub, assessedTaxProfile(), "vat21")
			var review *billingtax.ReviewError
			if !errors.As(err, &review) || !strings.Contains(review.Code, "held_invoice") || b.writes != 0 {
				t.Fatalf("unproven invoice was collected instead of reviewed: writes%d err%v", b.writes, err)
			}
		})
	}
}
