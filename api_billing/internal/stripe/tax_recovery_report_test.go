package stripe

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	api "github.com/stripe/stripe-go/v85"
)

type recoveryReportBackend struct {
	taxBackend
	inv           *api.Invoice
	allLines      []*api.InvoiceLineItem
	invoiceWrites int
}

func (b *recoveryReportBackend) CallRaw(method, path, key string, body []byte, p *api.Params, out api.LastResponseSetter) error {
	if list, ok := out.(*api.InvoiceLineItemList); ok {
		list.Data = b.allLines
		return nil
	}
	return b.taxBackend.CallRaw(method, path, key, body, p, out)
}

func (b *recoveryReportBackend) Call(method, path, key string, params api.ParamsContainer, out api.LastResponseSetter) error {
	if inv, ok := out.(*api.Invoice); ok {
		if method == http.MethodGet {
			*inv = *b.inv
			return nil
		}
		p, ok := params.(*api.InvoiceParams)
		if !ok || p.Metadata[taxTerminalCode] == "" || p.Metadata[taxTerminalInput] == "" || p.AutoAdvance != nil || p.DefaultTaxRates != nil || p.AutomaticTax != nil || p.Footer != nil {
			return errors.New("terminal review acknowledgement changed provider invoice money or collection")
		}
		metadata := map[string]string{}
		for k, v := range b.inv.Metadata {
			metadata[k] = v
		}
		for k, v := range p.Metadata {
			metadata[k] = v
		}
		b.inv.Metadata = metadata
		b.invoiceWrites++
		*inv = *b.inv
		return nil
	}
	if err := b.taxBackend.Call(method, path, key, params, out); err != nil {
		return err
	}
	if p, ok := params.(*api.SubscriptionParams); ok && method == http.MethodPost {
		metadata := map[string]string{}
		for k, v := range b.currentSub.Metadata {
			metadata[k] = v
		}
		for k, v := range p.Metadata {
			if v == "" {
				delete(metadata, k)
			} else {
				metadata[k] = v
			}
		}
		b.currentSub.Metadata = metadata
	}
	return nil
}

func terminalReviewFixture(t *testing.T) (*Client, *recoveryReportBackend, TaxProfile) {
	t.Helper()
	sub, inv := heldTaxFixture()
	p := assessedTaxProfile()
	sub.Metadata = map[string]string{"tenant_id": "tenant", "frameworks_tax_profile": p.Fingerprint(), "frameworks_tax_recovery_pending": "true"}
	sub.PauseCollection = nil
	sub.DefaultTaxRates = []*api.TaxRate{{ID: "txr_exclusive"}}
	sub.AutomaticTax = &api.SubscriptionAutomaticTax{Enabled: false}
	inv.BillingReason = api.InvoiceBillingReasonSubscriptionUpdate
	b := &recoveryReportBackend{taxBackend: taxBackend{t: t, owner: "tenant", currentSub: sub, drafts: []*api.Invoice{inv}}, inv: inv}
	installBackend(t, b)
	return &Client{}, b, p
}

func TestTerminalInvoiceReviewStopsUnchangedAutomaticRescansAfterAcknowledgement(t *testing.T) {
	c, b, p := terminalReviewFixture(t)
	report, err := c.ReconcileSubscriptionTax(t.Context(), "tenant", "customer", "sub", p)
	if err != nil || len(report.Reviews) != 1 || report.RetryPending {
		t.Fatalf("irreparable invoice did not produce one terminal review: %+v %v", report, err)
	}
	if b.invoiceWrites != 0 || b.inv.Metadata[taxTerminalInput] != "" {
		t.Fatal("provider terminal marker preceded durable-review acknowledgement")
	}
	retainedReview := report.Reviews[0]
	if err = c.AckTaxRecoveryReport(t.Context(), report); err != nil {
		t.Fatal(err)
	}
	if b.inv.Metadata[taxTerminalInput] != retainedReview.InputFingerprint || b.currentSub.Metadata["frameworks_tax_recovery_pending"] != "" {
		t.Fatal("acknowledged terminal review remained automatic work")
	}
	reads, writes := b.invoiceListCalls, b.invoiceWrites
	clean, err := c.ReconcileSubscriptionTax(t.Context(), "tenant", "customer", "sub", p)
	if err != nil || clean.RecoveryPending || b.invoiceListCalls != reads || b.invoiceWrites != writes {
		t.Fatalf("unchanged terminal invoice caused repeated automatic scans: %+v %v", clean, err)
	}
	b.currentSub.Metadata["frameworks_tax_recovery_pending"] = "true"
	unchanged, err := c.ReconcileSubscriptionTax(t.Context(), "tenant", "customer", "sub", p)
	if err != nil || len(unchanged.Reviews) != 0 {
		t.Fatalf("same reviewed inputs reopened during another recovery episode: %+v %v", unchanged, err)
	}
	b.inv.Lines.Data[0].Amount++
	changed, err := c.ReconcileSubscriptionTax(t.Context(), "tenant", "customer", "sub", p)
	if err != nil || len(changed.Reviews) != 1 || changed.Reviews[0].InputFingerprint == retainedReview.InputFingerprint {
		t.Fatalf("new provider facts were permanently hidden by an old marker: %+v %v", changed, err)
	}
}

func TestTaxReviewEvidenceAndAckUseCompletePaginatedInputs(t *testing.T) {
	for _, changedPage := range []bool{false, true} {
		name := "unchanged page two"
		if changedPage {
			name = "changed page two"
		}
		t.Run(name, func(t *testing.T) {
			c, b, p := terminalReviewFixture(t)
			first := b.inv.Lines.Data[0]
			second := *first
			second.ID = "page-two"
			second.Amount = -100
			b.allLines = []*api.InvoiceLineItem{first, &second}
			b.inv.Lines = &api.InvoiceLineItemList{ListMeta: api.ListMeta{HasMore: true}, Data: []*api.InvoiceLineItem{first}}
			report, err := c.ReconcileSubscriptionTax(t.Context(), "tenant", "customer", "sub", p)
			if err != nil || len(report.Reviews) != 1 || !strings.Contains(string(report.Reviews[0].Evidence), "page-two") {
				t.Fatalf("terminal review omitted paginated financial input: %+v %v", report, err)
			}
			b.inv.Lines = &api.InvoiceLineItemList{ListMeta: api.ListMeta{HasMore: true}, Data: []*api.InvoiceLineItem{first}}
			if changedPage {
				second.Amount = -200
			}
			err = c.AckTaxRecoveryReport(t.Context(), report)
			if changedPage {
				if err == nil || b.invoiceWrites != 0 || b.currentSub.Metadata["frameworks_tax_recovery_pending"] != "true" {
					t.Fatal("changed page-two facts received a stale terminal acknowledgement")
				}
			} else if err != nil || b.invoiceWrites != 1 {
				t.Fatalf("unchanged full input could not be acknowledged: %v", err)
			}
		})
	}
}

func TestTaxRecoveryAckDoesNotClearPendingAfterCurrentProfileChanges(t *testing.T) {
	c, b, p := terminalReviewFixture(t)
	report := &TaxRecoveryReport{TenantID: "tenant", CustomerID: "customer", SubscriptionID: "sub", ProfileSHA: p.Fingerprint(), RecoveryPending: true}
	b.currentSub.Metadata["frameworks_tax_profile"] = "new-current-profile"
	if err := c.AckTaxRecoveryReport(t.Context(), report); err == nil {
		t.Fatal("stale profile acknowledgement cleared newer recovery work")
	}
	if b.currentSub.Metadata["frameworks_tax_recovery_pending"] != "true" {
		t.Fatal("newer profile pending work was lost")
	}
}

func TestTaxReviewFingerprintIncludesDiscountPresence(t *testing.T) {
	_, inv := heldTaxFixture()
	_, first, err := taxRecoveryInvoiceInputs(inv, assessedTaxProfile().Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	inv.Discounts = []*api.Discount{nil}
	_, changed, err := taxRecoveryInvoiceInputs(inv, assessedTaxProfile().Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	if first == changed {
		t.Fatal("classification-changing discount presence was omitted from review inputs")
	}
}
