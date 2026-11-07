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
	inv               *api.Invoice
	allLines          []*api.InvoiceLineItem
	invoiceWrites     int
	repairs           *heldDraftBackend
	subscriptionReads int
	afterInvoiceAck   func()
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
		if b.repairs != nil && (!ok || p.Metadata[taxTerminalCode] == "") {
			return b.repairs.Call(method, path, key, params, out)
		}
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
		if b.afterInvoiceAck != nil {
			b.afterInvoiceAck()
		}
		*inv = *b.inv
		return nil
	}
	if _, ok := out.(*api.Subscription); ok && method == http.MethodGet {
		b.subscriptionReads++
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

func TestTaxRecoveryAckWithoutInvoiceWritesReadsSubscriptionOnce(t *testing.T) {
	c, b, p := terminalReviewFixture(t)
	b.inv.Status = api.InvoiceStatusOpen
	b.inv.Metadata = map[string]string{"frameworks_tax_recovered_hold": "true", "frameworks_tax_profile": p.Fingerprint()}
	report, err := c.ReconcileSubscriptionTax(t.Context(), "tenant", "customer", "sub", p)
	if err != nil || len(report.Reviews) != 0 {
		t.Fatalf("unexpected recovered invoice review: %+v %v", report, err)
	}
	before := b.subscriptionReads
	if err = c.AckTaxRecoveryReport(t.Context(), report); err != nil {
		t.Fatal(err)
	}
	if reads := b.subscriptionReads - before; reads != 1 {
		t.Fatalf("ordinary recovery acknowledgement fetched subscription %d times, want1", reads)
	}
}

func TestTaxRecoveryAckKeepsPostWriteFenceForChangedCollectionContext(t *testing.T) {
	c, b, p := terminalReviewFixture(t)
	report, err := c.ReconcileSubscriptionTax(t.Context(), "tenant", "customer", "sub", p)
	if err != nil || len(report.Reviews) != 1 {
		t.Fatalf("missing review: %+v %v", report, err)
	}
	b.afterInvoiceAck = func() {
		b.currentSub.PauseCollection = &api.SubscriptionPauseCollection{Behavior: api.SubscriptionPauseCollectionBehaviorKeepAsDraft}
	}
	if err = c.AckTaxRecoveryReport(t.Context(), report); err == nil || b.currentSub.Metadata["frameworks_tax_recovery_pending"] != "true" {
		t.Fatalf("invoice acknowledgement cleared newer subscription collection work: %v", err)
	}
}

func TestTerminalDraftResumesAfterSubscriptionPauseChanges(t *testing.T) {
	c, b, p := terminalReviewFixture(t)
	b.inv.BillingReason = api.InvoiceBillingReasonSubscriptionCycle
	b.currentSub.PauseCollection = &api.SubscriptionPauseCollection{Behavior: api.SubscriptionPauseCollectionBehaviorKeepAsDraft}
	b.repairs = &heldDraftBackend{inv: b.inv, rateID: "txr_exclusive"}
	report, err := c.ReconcileSubscriptionTax(t.Context(), "tenant", "customer", "sub", p)
	if err != nil || len(report.Reviews) != 1 || report.Reviews[0].Code != "stripe_held_invoice_operator_pause" {
		t.Fatalf("operator-held draft did not retain a review: %+v %v", report, err)
	}
	if err = c.AckTaxRecoveryReport(t.Context(), report); err != nil {
		t.Fatal(err)
	}
	reads := b.invoiceListCalls
	b.currentSub.PauseCollection = nil
	resumed, err := c.ReconcileSubscriptionTax(t.Context(), "tenant", "customer", "sub", p)
	if err != nil || !resumed.RecoveryPending || len(resumed.Reviews) != 0 || b.invoiceListCalls <= reads || b.inv.Status != api.InvoiceStatusOpen || !b.inv.AutoAdvance {
		t.Fatalf("changed subscription pause permanently hid the repairable draft: report=%+v err=%v reads=%d->%d status=%s autoAdvance=%v", resumed, err, reads, b.invoiceListCalls, b.inv.Status, b.inv.AutoAdvance)
	}
}

func TestLegacyAcknowledgedDraftBootstrapsRecoveryContextOnce(t *testing.T) {
	c, b, p := terminalReviewFixture(t)
	delete(b.currentSub.Metadata, "frameworks_tax_recovery_pending")
	b.inv.BillingReason = api.InvoiceBillingReasonSubscriptionCycle
	b.inv.Metadata = map[string]string{taxTerminalCode: "stripe_held_invoice_operator_pause", taxTerminalInput: strings.Repeat("a", 64)}
	b.repairs = &heldDraftBackend{inv: b.inv, rateID: "txr_exclusive"}
	report, err := c.ReconcileSubscriptionTax(t.Context(), "tenant", "customer", "sub", p)
	if err != nil || !report.RecoveryPending || len(report.Reviews) != 0 || b.inv.Status != api.InvoiceStatusOpen || !b.inv.AutoAdvance {
		t.Fatalf("legacy acknowledged draft without subscription markers remained stranded: report=%+v err=%v status=%s", report, err, b.inv.Status)
	}
	if err = c.AckTaxRecoveryReport(t.Context(), report); err != nil {
		t.Fatal(err)
	}
	if b.currentSub.Metadata[taxRecoveryContext] == "" {
		t.Fatal("finished legacy discovery did not retain its one-time context stamp")
	}
	reads := b.invoiceListCalls
	clean, err := c.ReconcileSubscriptionTax(t.Context(), "tenant", "customer", "sub", p)
	if err != nil || clean.RecoveryPending || len(clean.Reviews) != 0 || b.invoiceListCalls != reads {
		t.Fatalf("completed legacy discovery repeatedly scanned finalized debt: %+v err=%v reads=%d->%d", clean, err, reads, b.invoiceListCalls)
	}
	if err = c.SyncSubscriptionTax(t.Context(), "tenant", "customer", "sub", p); err != nil || b.invoiceListCalls != reads {
		t.Fatalf("future-only sync inspected historical invoices: %v", err)
	}
}

func TestLegacyContextDiscoveryDoesNotReviewFinalizedInvoices(t *testing.T) {
	c, b, p := terminalReviewFixture(t)
	delete(b.currentSub.Metadata, "frameworks_tax_recovery_pending")
	b.inv.Status = api.InvoiceStatusOpen
	b.inv.Metadata = map[string]string{"frameworks_tax_recovered_hold": "true", "frameworks_tax_profile": p.Fingerprint()}
	report, err := c.ReconcileSubscriptionTax(t.Context(), "tenant", "customer", "sub", p)
	if err != nil || !report.RecoveryPending || len(report.Reviews) != 0 || b.invoiceWrites != 0 {
		t.Fatalf("legacy discovery did not complete its read-only finalized-debt control: %+v err=%v writes=%d", report, err, b.invoiceWrites)
	}
	if err = c.AckTaxRecoveryReport(t.Context(), report); err != nil {
		t.Fatal(err)
	}
	reads := b.invoiceListCalls
	clean, err := c.ReconcileSubscriptionTax(t.Context(), "tenant", "customer", "sub", p)
	if err != nil || clean.RecoveryPending || b.invoiceListCalls != reads || b.invoiceWrites != 0 {
		t.Fatalf("clean legacy subscription repeatedly inspected finalized debt: %+v %v", clean, err)
	}
}

func TestFinalizedInvoicesAreNotReopenedByCurrentTaxProfile(t *testing.T) {
	c, b, p := terminalReviewFixture(t)
	b.inv.Status = api.InvoiceStatusOpen
	b.inv.BillingReason = api.InvoiceBillingReasonSubscriptionCycle
	b.inv.Metadata = map[string]string{"frameworks_tax_recovered_hold": "true", "frameworks_tax_profile": p.Fingerprint()}
	p.RateBPS = 0
	report, err := c.ReconcileSubscriptionTax(t.Context(), "tenant", "customer", "sub", p)
	if err != nil || len(report.Reviews) != 0 || b.invoiceWrites != 0 {
		t.Fatalf("current profile reopened finalized invoice facts: %+v %v writes=%d", report, err, b.invoiceWrites)
	}
}

func TestExternallyFinalizedOwnedInvoiceRequiresFrozenTaxReview(t *testing.T) {
	c, b, p := terminalReviewFixture(t)
	b.inv.Status = api.InvoiceStatusOpen
	b.inv.BillingReason = api.InvoiceBillingReasonSubscriptionCycle
	b.inv.Total, b.inv.TotalExcludingTax = 10000, 10000
	report, err := c.ReconcileSubscriptionTax(t.Context(), "tenant", "customer", "sub", p)
	if err != nil || len(report.Reviews) != 1 || report.Reviews[0].Code != "stripe_held_invoice_missing_frozen_tax" {
		t.Fatalf("externally finalized owned invoice escaped review: %+v %v", report, err)
	}
	if err = c.AckTaxRecoveryReport(t.Context(), report); err != nil {
		t.Fatal(err)
	}
	if b.inv.Metadata[taxTerminalCode] != "stripe_held_invoice_missing_frozen_tax" || b.inv.Total != 10000 || b.inv.AutoAdvance || b.inv.Status != api.InvoiceStatusOpen {
		t.Fatal("open invoice review changed original money or collection")
	}
	p.RateBPS = 0
	b.currentSub.Metadata["frameworks_tax_recovery_pending"] = "true"
	again, err := c.ReconcileSubscriptionTax(t.Context(), "tenant", "customer", "sub", p)
	if err != nil || len(again.Reviews) != 0 {
		t.Fatalf("current rate reopened the unchanged finalized-invoice review: %+v %v", again, err)
	}
}

func TestTaxRecoveryAckRejectsChangedCustomerBeforeInvoiceWrite(t *testing.T) {
	c, b, p := terminalReviewFixture(t)
	report, err := c.ReconcileSubscriptionTax(t.Context(), "tenant", "customer", "sub", p)
	if err != nil || len(report.Reviews) != 1 {
		t.Fatalf("missing original review: %+v %v", report, err)
	}
	b.currentSub.Customer = &api.Customer{ID: "other-customer"}
	if err = c.AckTaxRecoveryReport(t.Context(), report); err == nil || b.invoiceWrites != 0 {
		t.Fatalf("changed subscription customer received an invoice acknowledgement: err=%v writes=%d", err, b.invoiceWrites)
	}
}

func TestTaxRecoveryDoesNotClaimAnotherCustomersInvoice(t *testing.T) {
	_, b, p := terminalReviewFixture(t)
	b.inv.Customer = &api.Customer{ID: "other-customer"}
	report, err := recoverStripeTaxInvoices(t.Context(), b.currentSub, p, "txr_exclusive")
	if err != nil || len(report.Reviews) != 0 || b.invoiceWrites != 0 {
		t.Fatalf("subscription metadata claimed another customer's invoice: %+v %v", report, err)
	}
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
	sub, _ := heldTaxFixture()
	_, first, err := taxRecoveryInvoiceInputs(inv, sub, assessedTaxProfile().Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	inv.Discounts = []*api.Discount{nil}
	_, changed, err := taxRecoveryInvoiceInputs(inv, sub, assessedTaxProfile().Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	if first == changed {
		t.Fatal("classification-changing discount presence was omitted from review inputs")
	}
}
