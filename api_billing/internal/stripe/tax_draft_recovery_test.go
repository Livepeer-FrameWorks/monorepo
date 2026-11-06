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
	paymentState            string
	rateID                  string
	tax                     *taxBackend
}

func (b *heldDraftBackend) CallRaw(method, path, key string, body []byte, p *api.Params, out api.LastResponseSetter) error {
	if list, ok := out.(*api.InvoiceList); ok {
		list.Data = []*api.Invoice{b.inv}
		return nil
	}
	if b.tax != nil {
		return b.tax.CallRaw(method, path, key, body, p, out)
	}
	return b.fakeBackend.CallRaw(method, path, key, body, p, out)
}

func (b *heldDraftBackend) Call(method, path, key string, params api.ParamsContainer, out api.LastResponseSetter) error {
	if _, ok := out.(*api.Invoice); !ok && b.tax != nil {
		return b.tax.Call(method, path, key, params, out)
	}
	if method != http.MethodPost {
		return errors.New("unexpected non-mutating invoice request")
	}
	b.writes++
	stage := ""
	rateID := b.rateID
	if rateID == "" {
		rateID = "vat21"
	}
	switch p := params.(type) {
	case *api.InvoiceParams:
		stage = "update"
		if api.BoolValue(p.AutoAdvance) && len(p.DefaultTaxRates) == 0 {
			stage = "advance"
			if p.Metadata != nil || p.AutomaticTax != nil || p.Footer != nil {
				return errors.New("finalized invoice tax facts were rewritten")
			}
			b.inv.AutoAdvance = true
			break
		}
		if api.BoolValue(p.AutoAdvance) || len(p.DefaultTaxRates) != 1 || api.StringValue(p.DefaultTaxRates[0]) != rateID {
			return errors.New("recovery enabled automatic collection or lost current exclusive rate")
		}
		b.inv.Metadata = p.Metadata
	case *api.InvoiceUpdateLinesParams:
		stage = "lines"
		if len(p.Lines) != 1 || api.StringValue(p.Lines[0].ID) != "base" || len(p.Lines[0].TaxRates) != 1 || api.StringValue(p.Lines[0].TaxRates[0]) != rateID || p.Lines[0].Amount != nil {
			return errors.New("recovery changed original net or omitted explicit line rate")
		}
		b.inv.Total = 12100
	case *api.InvoiceFinalizeInvoiceParams:
		stage = "finalize"
		if b.inv.Total != 12100 || b.inv.Metadata["frameworks_tax_profile"] == "" {
			return errors.New("recovery finalized without retained tax setup")
		}
		b.finalized++
		b.inv.Status = api.InvoiceStatusOpen
		b.inv.AutoAdvance = api.BoolValue(p.AutoAdvance)
	case *api.InvoicePayParams:
		stage = "pay"
		if b.inv.Status != api.InvoiceStatusOpen || b.inv.Total != 12100 {
			return errors.New("recovery collected wrong invoice or gross")
		}
		b.paid++
		if b.paymentState == "declined" {
			return &api.Error{HTTPStatusCode: 402, Msg: "card_declined"}
		}
		if b.paymentState != "" {
			b.inv.Status = api.InvoiceStatusOpen
			*out.(*api.Invoice) = *b.inv
			return nil
		}
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

func TestTaxHoldReleaseDoesNotWaitForPayment(t *testing.T) {
	for _, state := range []string{"declined", "requires_action", "processing"} {
		t.Run(state, func(t *testing.T) {
			sub, inv := heldTaxFixture()
			b := &heldDraftBackend{inv: inv, paymentState: state, rateID: "txr_exclusive", tax: &taxBackend{t: t, owner: "tenant", currentSub: sub}}
			installBackend(t, b)
			if _, err := (&Client{}).ReconcileSubscriptionTax(t.Context(), "tenant", "customer", "sub", assessedTaxProfile()); err != nil {
				t.Fatalf("valid tax recovery was held by payment outcome: %v", err)
			}
			if b.paid != 0 || !inv.AutoAdvance || inv.Status != api.InvoiceStatusOpen {
				t.Fatalf("recovery did not hand unpaid invoice to Stripe dunning: paidCalls%d autoAdvance%v status%s", b.paid, inv.AutoAdvance, inv.Status)
			}
			resumed := false
			for _, update := range b.tax.subUpdates {
				for _, field := range update.UnsetFields {
					resumed = resumed || field == api.SubscriptionParamsUnsetFieldPauseCollection
				}
			}
			if !resumed {
				t.Fatal("valid tax profile retained its owned pause while payment was pending")
			}
		})
	}
}

func TestTaxHoldInvoiceFailureDoesNotPauseCorrectFutureRenewals(t *testing.T) {
	for _, failure := range []string{"invoice API", "unsupported adjustment"} {
		t.Run(failure, func(t *testing.T) {
			sub, inv := heldTaxFixture()
			b := &heldDraftBackend{inv: inv, rateID: "txr_exclusive", tax: &taxBackend{t: t, owner: "tenant", currentSub: sub}}
			if failure == "invoice API" {
				b.failAfter = "update"
			} else {
				inv.Lines.Data[0].Amount = -100
			}
			installBackend(t, b)
			if report, err := (&Client{}).ReconcileSubscriptionTax(t.Context(), "tenant", "customer", "sub", assessedTaxProfile()); err == nil && (report == nil || len(report.Reviews) == 0) {
				t.Fatal("past invoice failure was silently resolved")
			}
			resumed := false
			if b.tax.updatedSub != nil {
				for _, f := range b.tax.updatedSub.UnsetFields {
					resumed = resumed || f == api.SubscriptionParamsUnsetFieldPauseCollection
				}
			}
			if !resumed {
				t.Fatal("past invoice failure kept future tax-correct renewals paused")
			}
			if b.paid != 0 {
				t.Fatal("tax recovery performed a synchronous charge")
			}
		})
	}
}

func TestTaxHoldRecoveryIgnoresOtherInvoices(t *testing.T) {
	for _, state := range []api.InvoiceStatus{api.InvoiceStatusDraft, api.InvoiceStatusOpen} {
		for _, kind := range []string{"unrelated", "manual", "before hold"} {
			t.Run(string(state)+"/"+kind, func(t *testing.T) {
				sub, inv := heldTaxFixture()
				inv.Status = state
				switch kind {
				case "unrelated":
					inv.Parent.SubscriptionDetails.Metadata = map[string]string{}
				case "manual":
					inv.BillingReason = api.InvoiceBillingReasonManual
					inv.Parent = nil
				case "before hold":
					inv.Created = 99
				}
				b := &heldDraftBackend{inv: inv}
				installBackend(t, b)
				if report, err := recoverStripeTaxInvoices(t.Context(), sub, assessedTaxProfile(), "vat21"); err != nil || len(report.Reviews) != 0 {
					t.Fatalf("unrelated invoice blocked owned tax recovery: %v", err)
				}
				if b.writes != 0 {
					t.Fatal("recovery changed an unrelated invoice")
				}
			})
		}
	}
}

func TestTaxPreflightLeavesPendingInvoiceWorkToBillingWorker(t *testing.T) {
	sub, inv := heldTaxFixture()
	inv.Lines.Data[0].Amount = -100
	b := &taxBackend{t: t, owner: "tenant", currentSub: sub, drafts: []*api.Invoice{inv}}
	installBackend(t, b)
	if err := (&Client{}).SyncSubscriptionTax(t.Context(), "tenant", "customer", "sub", assessedTaxProfile()); err != nil {
		t.Fatal(err)
	}
	if b.invoiceListCalls != 0 {
		t.Fatal("interactive tax preflight scanned or repaired old invoice debt")
	}
	if b.updatedSub == nil || b.updatedSub.Metadata["frameworks_tax_recovery_pending"] != "true" || b.updatedSub.Metadata["frameworks_tax_hold_started"] != "" {
		t.Fatal("resumed tax preflight lost pending worker repair or retained active hold")
	}
}

func TestLaterTaxHoldStartsFreshEpisodeWithoutLosingPendingRepair(t *testing.T) {
	sub, _ := heldTaxFixture()
	sub.Metadata["frameworks_tax_review"] = ""
	sub.Metadata["frameworks_tax_recovery_pending"] = "true"
	sub.PauseCollection = nil
	b := &taxBackend{t: t, owner: "tenant", currentSub: sub}
	installBackend(t, b)
	if err := (&Client{}).HoldSubscriptionTaxReview(t.Context(), "tenant", "customer", "sub"); err != nil {
		t.Fatal(err)
	}
	if b.updatedSub == nil || b.updatedSub.Metadata["frameworks_tax_hold_started"] == "100" || b.updatedSub.Metadata["frameworks_tax_hold_started"] == "" {
		t.Fatal("new tax hold reused the old episode timestamp")
	}
	if _, cleared := b.updatedSub.Metadata["frameworks_tax_recovery_pending"]; cleared {
		t.Fatal("new hold erased older unfinished invoice repair")
	}
}

func TestOlderOwnedInvoiceRepairSurvivesANewerHoldEpisode(t *testing.T) {
	sub, inv := heldTaxFixture()
	sub.Metadata["frameworks_tax_hold_started"] = "200"
	b := &heldDraftBackend{inv: inv}
	installBackend(t, b)
	if _, err := recoverStripeTaxInvoices(t.Context(), sub, assessedTaxProfile(), "vat21"); err != nil {
		t.Fatal(err)
	}
	if inv.Status != api.InvoiceStatusOpen || !inv.AutoAdvance || b.finalized != 1 || b.paid != 0 {
		t.Fatal("older unfinished tax work was lost when a later hold began")
	}
}

func TestOwnedNonRenewalHeldDraftRequiresExplicitReview(t *testing.T) {
	for _, reason := range []api.InvoiceBillingReason{api.InvoiceBillingReasonSubscriptionCreate, api.InvoiceBillingReasonSubscriptionUpdate, api.InvoiceBillingReasonManual} {
		t.Run(string(reason), func(t *testing.T) {
			sub, inv := heldTaxFixture()
			inv.BillingReason = reason
			b := &heldDraftBackend{inv: inv}
			installBackend(t, b)
			var review *billingtax.ReviewError
			if err := recoverStripeTaxInvoice(t.Context(), sub, inv, assessedTaxProfile(), "vat21"); !errors.As(err, &review) {
				t.Fatalf("owned non-renewal invoice disappeared without review: %v", err)
			}
			if b.writes != 0 {
				t.Fatal("unsupported owned supply was speculatively repriced")
			}
		})
	}
}

func TestRecoveredOpenInvoicePreservesOperatorDisabledAutoAdvance(t *testing.T) {
	sub, inv := heldTaxFixture()
	inv.Status = api.InvoiceStatusOpen
	inv.Metadata = map[string]string{"frameworks_tax_recovered_hold": "true", "frameworks_tax_profile": assessedTaxProfile().Fingerprint()}
	b := &heldDraftBackend{inv: inv}
	installBackend(t, b)
	if err := recoverStripeTaxInvoice(t.Context(), sub, inv, assessedTaxProfile(), "vat21"); err != nil {
		t.Fatal(err)
	}
	if b.writes != 0 || inv.AutoAdvance {
		t.Fatal("recovery overrode operator-disabled automatic collection")
	}
}

func TestNilHeldInvoiceLineRequiresReview(t *testing.T) {
	sub, inv := heldTaxFixture()
	inv.Lines.Data[0] = nil
	b := &heldDraftBackend{inv: inv}
	installBackend(t, b)
	var review *billingtax.ReviewError
	if err := recoverStripeTaxInvoice(t.Context(), sub, inv, assessedTaxProfile(), "vat21"); !errors.As(err, &review) {
		t.Fatalf("nil provider line was not held for review: %v", err)
	}
	if b.writes != 0 {
		t.Fatal("invalid provider invoice was modified")
	}
}

func heldTaxFixture() (*api.Subscription, *api.Invoice) {
	meta := map[string]string{"tenant_id": "tenant", "frameworks_tax_review": "true", "frameworks_tax_pause_owned": "true", "frameworks_tax_hold_started": "100"}
	sub := &api.Subscription{ID: "sub", Customer: &api.Customer{ID: "customer"}, Metadata: meta, PauseCollection: &api.SubscriptionPauseCollection{Behavior: api.SubscriptionPauseCollectionBehaviorKeepAsDraft}}
	snapshot := map[string]string{}
	for key, value := range meta {
		snapshot[key] = value
	}
	inv := &api.Invoice{ID: "same-held-invoice", Customer: sub.Customer, Created: 101, Currency: api.CurrencyEUR, Status: api.InvoiceStatusDraft, BillingReason: api.InvoiceBillingReasonSubscriptionCycle, Parent: &api.InvoiceParent{SubscriptionDetails: &api.InvoiceParentSubscriptionDetails{Metadata: snapshot, Subscription: &api.Subscription{ID: sub.ID}}}, Lines: &api.InvoiceLineItemList{Data: []*api.InvoiceLineItem{{ID: "base", Amount: 10000, Currency: api.CurrencyEUR, Parent: &api.InvoiceLineItemParent{SubscriptionItemDetails: &api.InvoiceLineItemParentSubscriptionItemDetails{Subscription: "sub", SubscriptionItem: "item"}}}}}}
	return sub, inv
}

func TestOwnedTaxHoldDraftRecoversSameInvoiceAfterEveryLostResponse(t *testing.T) {
	for _, stage := range []string{"update", "lines", "finalize"} {
		t.Run(stage, func(t *testing.T) {
			sub, inv := heldTaxFixture()
			b := &heldDraftBackend{inv: inv, failAfter: stage}
			installBackend(t, b)
			if _, err := recoverStripeTaxInvoices(t.Context(), sub, assessedTaxProfile(), "vat21"); err == nil {
				t.Fatal("lost committed response not exercised")
			}
			if _, err := recoverStripeTaxInvoices(t.Context(), sub, assessedTaxProfile(), "vat21"); err != nil {
				t.Fatal(err)
			}
			wantFinalized := 1
			if inv.ID != "same-held-invoice" || inv.Status != api.InvoiceStatusOpen || !inv.AutoAdvance || inv.Lines.Data[0].Amount != 10000 || inv.Total != 12100 || b.finalized != wantFinalized || b.paid != 0 {
				t.Fatalf("recovery changed invoice/credit/payment: %+v finalized%d paid%d", inv, b.finalized, b.paid)
			}
		})
	}
}

func TestTaxHoldRecoveryRefusesOperatorChangedPauseAndUnsupportedOwnedInvoices(t *testing.T) {
	for _, mode := range []string{"operator pause", "unrelated credit", "missing finalized proof"} {
		t.Run(mode, func(t *testing.T) {
			sub, inv := heldTaxFixture()
			switch mode {
			case "operator pause":
				sub.PauseCollection = &api.SubscriptionPauseCollection{Behavior: api.SubscriptionPauseCollectionBehaviorVoid}
			case "unrelated credit":
				inv.Lines.Data[0].Amount = -100
			case "missing finalized proof":
				inv.Status = api.InvoiceStatusOpen
			}
			b := &heldDraftBackend{inv: inv}
			installBackend(t, b)
			err := recoverStripeTaxInvoice(t.Context(), sub, inv, assessedTaxProfile(), "vat21")
			var review *billingtax.ReviewError
			if !errors.As(err, &review) || !strings.Contains(review.Code, "held_invoice") || b.writes != 0 {
				t.Fatalf("unproven invoice was collected instead of reviewed: writes%d err%v", b.writes, err)
			}
		})
	}
}
