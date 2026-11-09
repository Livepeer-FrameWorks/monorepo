//go:build schema_verify

package handlers

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"frameworks/api_billing/internal/appconfig/appconfigtest"
	"frameworks/api_billing/internal/pricing"
	"frameworks/api_billing/internal/rating"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func TestPostpaidTaxReviewHoldsAndResumesSameInvoice_RealPG(t *testing.T) {
	t.Run("incomplete_profile", func(t *testing.T) { testPostpaidTaxReviewHoldsAndResumesSameInvoice(t, false) })
	t.Run("retained_VAT_conflicts_with_country", func(t *testing.T) { testPostpaidTaxReviewHoldsAndResumesSameInvoice(t, true) })
}

func testPostpaidTaxReviewHoldsAndResumesSameInvoice(t *testing.T, malformedVAT bool) {
	db := startPurserUsageRealPG(t)
	for key, value := range map[string]string{"SUPPLIER_NAME": "DDVTech B.V.", "SUPPLIER_ADDRESS": "Leiden NL", "SUPPLIER_VAT_NUMBER": "NL853319145B01", "SUPPLIER_REGISTRATION_NUMBER": "59104694", "SUPPLIER_COUNTRY": "NL"} {
		appconfigtest.Set(t, key, value)
	}
	ctx := context.Background()
	tenant := seedPrepaidLedgerTenant(t, db)
	if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET billing_model='postpaid',billing_name=NULL WHERE tenant_id=$1::uuid`, tenant); err != nil {
		t.Fatal(err)
	}
	if malformedVAT {
		if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET billing_name='Buyer',billing_email='buyer@example.test',billing_customer_type='business',tax_id='NL853319145B01',billing_address='{"street":"Main1","city":"Berlin","postal_code":"10115","country":"DE"}' WHERE tenant_id=$1::uuid`, tenant); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	amount := decimal.NewFromInt(100)
	run := &postpaidInvoiceRun{tenantID: tenant, currency: "EUR", presentmentCurrency: "EUR", periodStart: start, periodEnd: start.Add(time.Hour), invoiceID: uuid.NewString(), billingEmail: "buyer@example.test", usageDetails: map[string]any{}, phase: usagePhase{start: start, end: start.Add(time.Hour)}, rating: &clusterRatingResult{TotalAmount: amount, BaseAmount: amount, BaseLine: pricedLine{PricingSource: pricing.SourceTier, LineItem: rating.LineItem{LineKey: "base_subscription", Description: "Support subscription", Currency: "EUR", Amount: amount, UnitPrice: amount, Quantity: decimal.NewFromInt(1), BillableQuantity: decimal.NewFromInt(1)}}}}
	var result postpaidInvoiceResult
	write := func() error {
		return database.WithRetryablePostgresTx(ctx, db, nil, func(tx *sql.Tx) error { var err error; result, err = writePostpaidInvoiceTx(ctx, tx, run); return err })
	}
	if err := write(); err != nil {
		t.Fatalf("tax review disappeared instead of committing a held invoice: %v", err)
	}
	if result.status != "manual_review" {
		t.Fatalf("tax review status = %s", result.status)
	}
	var status string
	var issued bool
	var credit, anomalies, events int64
	if err := db.QueryRow(`SELECT status,finalized_at IS NOT NULL,round(prepaid_credit_applied*100)::bigint FROM purser.billing_invoices WHERE tenant_id=$1::uuid AND id=$2::uuid`, tenant, result.invoiceID).Scan(&status, &issued, &credit); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1::uuid AND kind='invoice_tax_review' AND reference_id=$2 AND status='open'`, tenant, result.invoiceID).Scan(&anomalies); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM purser.domain_event_outbox WHERE tenant_id=$1::uuid`, tenant).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if status != "manual_review" || issued || credit != 0 || anomalies != 1 || events != 0 {
		t.Fatalf("unsafe held invoice: status=%s issued=%v credit=%d anomalies=%d events=%d", status, issued, credit, anomalies, events)
	}
	run.draftInvoiceID = result.invoiceID
	if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET billing_name='Buyer',billing_email='buyer@example.test',billing_customer_type='consumer',tax_id=NULL,billing_address='{"street":"Main1","city":"Leiden","postal_code":"2332ED","country":"NL"}' WHERE tenant_id=$1::uuid`, tenant); err != nil {
		t.Fatal(err)
	}
	if err := write(); err != nil {
		t.Fatal(err)
	}
	if result.status == "manual_review" {
		t.Fatal("corrected billing details left invoice stuck")
	}
	if err := db.QueryRow(`SELECT count(*) FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1::uuid AND kind='invoice_tax_review' AND reference_id=$2 AND status='open'`, tenant, result.invoiceID).Scan(&anomalies); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM purser.billing_invoices WHERE tenant_id=$1::uuid AND period_start=$2`, tenant, start).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 || anomalies != 0 {
		t.Fatalf("review did not converge: invoices=%d unresolved=%d", count, anomalies)
	}
}

func TestPostpaidTaxReviewCannotAdvanceBillingPhase_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	start := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Microsecond)
	tenant := newPhaseTenant(t, db, "postpaid", start, start.AddDate(0, 1, 0), 1000)
	if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET billing_name=NULL WHERE tenant_id=$1::uuid`, tenant.id); err != nil {
		t.Fatal(err)
	}
	closing, err := PreparePostpaidPhaseClose(t.Context(), db, logging.NewLogger(), tenant.svc, tenant.id)
	if err != nil {
		t.Fatal(err)
	}
	closed, err := commitSwitchToPrepaid(t.Context(), db, closing, tenant.id, tenant.paygID)
	if err == nil || closed {
		t.Fatalf("tax-held phase changed arrangement: closed=%v err=%v", closed, err)
	}
	var model string
	var actualStart time.Time
	if err = db.QueryRow(`SELECT billing_model,billing_period_start FROM purser.tenant_subscriptions WHERE tenant_id=$1::uuid`, tenant.id).Scan(&model, &actualStart); err != nil {
		t.Fatal(err)
	}
	if model != "postpaid" || !actualStart.Equal(start) {
		t.Fatalf("held tax phase advanced: model=%s start=%s", model, actualStart)
	}
	var reviews int
	if err = db.QueryRow(`SELECT count(*) FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1::uuid AND kind='invoice_tax_review' AND reference_type='billing_phase' AND status='open'`, tenant.id).Scan(&reviews); err != nil {
		t.Fatal(err)
	}
	if reviews != 1 {
		t.Fatalf("tax-held phase has %d durable reviews after arrangement rollback, want 1", reviews)
	}
	if _, err = db.Exec(`UPDATE purser.tenant_subscriptions SET billing_name='Corrected Buyer' WHERE tenant_id=$1::uuid`, tenant.id); err != nil {
		t.Fatal(err)
	}
	closing, err = PreparePostpaidPhaseClose(t.Context(), db, logging.NewLogger(), tenant.svc, tenant.id)
	if err != nil {
		t.Fatal(err)
	}
	if closed, err = commitSwitchToPrepaid(t.Context(), db, closing, tenant.id, tenant.paygID); err != nil || !closed {
		t.Fatalf("corrected tax phase failed: closed=%v err=%v", closed, err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1::uuid AND kind='invoice_tax_review' AND reference_type='billing_phase' AND status='open'`, tenant.id).Scan(&reviews); err != nil {
		t.Fatal(err)
	}
	if reviews != 0 {
		t.Fatalf("corrected tax phase left %d unresolved reviews", reviews)
	}
}

func TestPostpaidPricingReviewIsNotReportedAsTaxReview_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	start := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Microsecond)
	tenant := newPhaseTenant(t, db, "postpaid", start, start.AddDate(0, 1, 0), 1000)
	closing, err := PreparePostpaidPhaseClose(t.Context(), db, logging.NewLogger(), tenant.svc, tenant.id)
	if err != nil {
		t.Fatal(err)
	}
	closing.run.rating.ManualReviewReasons = []string{"pricing attribution needs review"}
	closed, err := commitSwitchToPrepaid(t.Context(), db, closing, tenant.id, tenant.paygID)
	if err != nil || !closed {
		t.Fatalf("pricing review was incorrectly rejected as tax hold: closed=%v err=%v", closed, err)
	}
}
