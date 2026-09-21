//go:build schema_verify

package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

type tierChangeFixture struct {
	db       *sql.DB
	stripe   *providerFixture
	service  *Service
	jobs     *JobManager
	tenantID string
	start    time.Time
	end      time.Time
}

// firstBaseFee is how the charge of the fixture's first base-fee invoice ends.
type firstBaseFee int

const (
	// firstBaseFeePaid confirms the charge, so the invoice is paid in full.
	firstBaseFeePaid firstBaseFee = iota
	// firstBaseFeeDeclined declines the charge, so the invoice stays payable.
	firstBaseFeeDeclined
	// firstBaseFeeInFlight leaves the charge processing at the provider.
	firstBaseFeeInFlight
)

// newTierChangeFixture seeds a USD tenant billed in advance on fromBase EUR,
// halfway through a 30-day period whose base-fee invoice and usage draft
// exist, with Stripe served by an in-process fixture. first decides whether
// that base-fee invoice was paid before the change.
func newTierChangeFixture(t *testing.T, fromBase string, first firstBaseFee) *tierChangeFixture {
	t.Helper()
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	stripe, stripeClient := installStripeFixture(t)
	stripe.route(http.MethodGet, "/v1/setup_intents/seti_change", func(recordedProviderRequest) (int, any) {
		return http.StatusOK, map[string]any{"id": "seti_change", "object": "setup_intent", "status": "succeeded", "payment_method": "pm_change"}
	})
	stripe.route(http.MethodPost, "/v1/customers/cus_change", func(recordedProviderRequest) (int, any) {
		return http.StatusOK, map[string]any{"id": "cus_change", "object": "customer"}
	})
	stripe.route(http.MethodGet, "/v1/customers/cus_change", func(recordedProviderRequest) (int, any) {
		return http.StatusOK, map[string]any{
			"id": "cus_change", "object": "customer",
			"invoice_settings": map[string]any{"default_payment_method": map[string]any{"id": "pm_change", "object": "payment_method"}},
		}
	})
	declineNext := first == firstBaseFeeDeclined
	stripe.route(http.MethodPost, "/v1/payment_intents", func(request recordedProviderRequest) (int, any) {
		if declineNext {
			declineNext = false
			return http.StatusPaymentRequired, map[string]any{"error": map[string]any{
				"type": "card_error", "code": "card_declined", "message": "Your card was declined.",
			}}
		}
		return http.StatusOK, map[string]any{"id": "pi_" + request.form.Get("metadata[billing_payment_id]"), "object": "payment_intent", "status": "processing"}
	})

	service := &Service{db: db, logger: logging.NewLogger(), stripeClient: stripeClient}
	jobs := &JobManager{db: db, logger: logging.NewLogger(), billing: service}
	wireAdvanceBilling(service, jobs)

	tierID := insertTierChangeTier(t, db, fromBase)
	now := time.Now().UTC()
	start := now.Add(-15 * 24 * time.Hour).Truncate(time.Second)
	end := start.Add(30 * 24 * time.Hour)
	tenantID := uuid.NewString()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.tenant_subscriptions (
			tenant_id, tier_id, status, billing_model, billing_email, payment_method, stripe_customer_id,
			billing_period_start, billing_period_end, next_billing_date, presentment_currency
		) VALUES ($1, $2, 'active', 'postpaid', 'billing@example.com', 'stripe', 'cus_change', $3, $4, $4, 'USD')
	`, tenantID, tierID, start, end); err != nil {
		t.Fatal(err)
	}
	storeRate(t, db, "USD", now, "1.2500")
	chargeErr := jobs.chargeAdvanceBaseFee(ctx, tenantID, now)
	if first == firstBaseFeePaid && chargeErr != nil {
		t.Fatalf("charge the current period's base fee: %v", chargeErr)
	}
	if first == firstBaseFeeDeclined && chargeErr == nil {
		t.Fatal("the declined base-fee charge reported no error")
	}
	if err := jobs.updateInvoiceDraft(ctx, tenantID); err != nil {
		t.Fatalf("write the current period's usage draft: %v", err)
	}
	f := &tierChangeFixture{db: db, stripe: stripe, service: service, jobs: jobs, tenantID: tenantID, start: start, end: end}
	if first == firstBaseFeePaid {
		f.confirmFirstBaseFeePayment(t)
	}
	if status := f.firstBaseFee(t).status; (first == firstBaseFeePaid) != (status == "paid") {
		t.Fatalf("first base-fee invoice status = %s before the change", status)
	}
	return f
}

// confirmFirstBaseFeePayment delivers the Stripe success of the first
// base-fee invoice's off-session charge.
func (f *tierChangeFixture) confirmFirstBaseFeePayment(t *testing.T) {
	t.Helper()
	first := f.firstBaseFee(t)
	var txID string
	if err := f.db.QueryRowContext(context.Background(), `
		SELECT tx_id FROM purser.billing_payments WHERE invoice_id = $1::uuid AND status = 'pending'
	`, first.id).Scan(&txID); err != nil {
		t.Fatalf("first base-fee payment: %v", err)
	}
	updated, err := f.service.updateInvoicePaymentStatus("stripe", txID, first.id, "confirmed", nil, providerSettlementEvidence{
		TenantID: f.tenantID, AmountCents: first.presentmentCents, Currency: "usd",
	})
	if err != nil || !updated {
		t.Fatalf("confirm first base-fee payment = %v, %v", updated, err)
	}
}

type baseFeeInvoiceState struct {
	id, status, amount, reductionLine string
	presentmentCents                  int64
}

// firstBaseFee reads the base-fee invoice of the period the fixture started in.
func (f *tierChangeFixture) firstBaseFee(t *testing.T) baseFeeInvoiceState {
	t.Helper()
	var s baseFeeInvoiceState
	if err := f.db.QueryRowContext(context.Background(), `
		SELECT id::text, status, amount::text, presentment_amount_cents,
		       COALESCE((SELECT SUM(line.amount)::numeric(20,2)::text FROM purser.invoice_line_items line
		                 WHERE line.invoice_id = invoice.id AND line.amount < 0), '0.00')
		FROM purser.billing_invoices invoice
		WHERE tenant_id = $1 AND base_fee_period_start = $2
	`, f.tenantID, f.start).Scan(&s.id, &s.status, &s.amount, &s.presentmentCents, &s.reductionLine); err != nil {
		t.Fatalf("first base-fee invoice: %v", err)
	}
	return s
}

func insertTierChangeTier(t *testing.T, db *sql.DB, base string) string {
	t.Helper()
	tierID := uuid.NewString()
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO purser.billing_tiers (id, tier_name, display_name, base_price, currency, metering_enabled)
		VALUES ($1, $2, 'Tier change', $3::numeric, 'EUR', false)
	`, tierID, "tier-change-"+tierID, base); err != nil {
		t.Fatal(err)
	}
	return tierID
}

// changeTier completes a Stripe setup checkout for tierID, the path a
// USD or GBP tenant takes to change tier.
func (f *tierChangeFixture) changeTier(t *testing.T, tierID string) {
	t.Helper()
	session, err := json.Marshal(map[string]any{
		"id": "cs_change_" + tierID, "customer": "cus_change", "setup_intent": "seti_change", "mode": "setup",
		"metadata": map[string]string{"purpose": string(PurposeSubscriptionSetup), "tenant_id": f.tenantID, "tier_id": tierID, "reference_id": tierID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.DispatchStripeCheckoutCompleted(context.Background(), session); err != nil {
		t.Fatalf("setup checkout completion: %v", err)
	}
}

type tierChangeState struct {
	changeAt                            time.Time
	usageStatus                         string
	usageStart, usageEnd                time.Time
	usageInvoices                       int
	baseFeeInvoices                     int
	newBaseAmount, newAmount            string
	newStatus                           string
	newPresentmentCents                 int64
	creditLineAmount                    string
	charges                             int
	lastChargeAmount, lastChargeCurrent string
	balanceCents                        int64
	balanceTransactions                 int
}

func (f *tierChangeFixture) state(t *testing.T) tierChangeState {
	t.Helper()
	ctx := context.Background()
	var s tierChangeState
	if err := f.db.QueryRowContext(ctx, `SELECT billing_period_start FROM purser.tenant_subscriptions WHERE tenant_id = $1`, f.tenantID).Scan(&s.changeAt); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRowContext(ctx, `
		SELECT status, period_start, period_end FROM purser.billing_invoices
		WHERE tenant_id = $1 AND base_fee_period_start IS NULL AND period_start = $2
	`, f.tenantID, f.start).Scan(&s.usageStatus, &s.usageStart, &s.usageEnd); err != nil {
		t.Fatalf("old period usage invoice: %v", err)
	}
	if err := f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM purser.billing_invoices WHERE tenant_id = $1 AND base_fee_period_start IS NULL`, f.tenantID).Scan(&s.usageInvoices); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM purser.billing_invoices WHERE tenant_id = $1 AND base_fee_period_start IS NOT NULL`, f.tenantID).Scan(&s.baseFeeInvoices); err != nil {
		t.Fatal(err)
	}
	var invoiceID string
	err := f.db.QueryRowContext(ctx, `
		SELECT id::text, base_amount::text, amount::text, status, COALESCE(presentment_amount_cents, -1)
		FROM purser.billing_invoices WHERE tenant_id = $1 AND base_fee_period_start = $2
	`, f.tenantID, s.changeAt).Scan(&invoiceID, &s.newBaseAmount, &s.newAmount, &s.newStatus, &s.newPresentmentCents)
	if err != nil && err != sql.ErrNoRows {
		t.Fatal(err)
	}
	if invoiceID != "" {
		if err := f.db.QueryRowContext(ctx, `
			SELECT COALESCE(SUM(amount), 0)::numeric(20,2)::text FROM purser.invoice_line_items
			WHERE invoice_id = $1 AND amount < 0
		`, invoiceID).Scan(&s.creditLineAmount); err != nil {
			t.Fatal(err)
		}
		if err := f.db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM purser.balance_transactions WHERE tenant_id = $1 AND reference_id = $2::uuid
		`, f.tenantID, invoiceID).Scan(&s.balanceTransactions); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(balance_cents), 0) FROM purser.prepaid_balances WHERE tenant_id = $1 AND currency = 'EUR'
	`, f.tenantID).Scan(&s.balanceCents); err != nil {
		t.Fatal(err)
	}
	charges := f.stripe.matching(http.MethodPost, "/v1/payment_intents")
	s.charges = len(charges)
	if len(charges) > 0 {
		s.lastChargeAmount = charges[len(charges)-1].form.Get("amount")
		s.lastChargeCurrent = charges[len(charges)-1].form.Get("currency")
	}
	return s
}

// proratedCreditCents is oldBaseCents times the share of the old period left
// at the change, rounded half away from zero.
func proratedCreditCents(oldBaseCents int64, start, end, changeAt time.Time) int64 {
	remaining := decimal.NewFromInt(end.Sub(changeAt).Microseconds())
	period := decimal.NewFromInt(end.Sub(start).Microseconds())
	return decimal.NewFromInt(oldBaseCents).Mul(remaining).Div(period).Round(0).IntPart()
}

func TestAdvanceBilledTierUpgradeClosesThePeriodAndProratesTheBaseFee_RealPG(t *testing.T) { //nolint:funlen // One tenant is followed through the change and its replay.
	f := newTierChangeFixture(t, "20.00", firstBaseFeePaid)
	upgradeTier := insertTierChangeTier(t, f.db, "50.00")
	if charges := len(f.stripe.matching(http.MethodPost, "/v1/payment_intents")); charges != 1 {
		t.Fatalf("setup charged %d times, want the current base fee once", charges)
	}

	f.changeTier(t, upgradeTier)
	after := f.state(t)

	if after.usageStatus == "draft" || after.usageStart.Sub(f.start).Abs() > time.Millisecond || after.usageEnd.Sub(after.changeAt).Abs() > time.Millisecond {
		t.Errorf("old period usage invoice = %s for [%s, %s), want finalized for [%s, %s)",
			after.usageStatus, after.usageStart, after.usageEnd, f.start, after.changeAt)
	}
	credit := proratedCreditCents(2000, f.start, f.end, after.changeAt)
	wantNet := decimal.New(5000-credit, -2).StringFixed(2)
	wantPresentment := decimal.NewFromInt(5000 - credit).Mul(decimal.RequireFromString("1.25")).Round(0).IntPart()
	if after.newBaseAmount != "50.00" || after.newAmount != wantNet || after.creditLineAmount != decimal.New(-credit, -2).StringFixed(2) {
		t.Errorf("new base-fee invoice base %s amount %s credit line %s, want base 50.00 amount %s credit -%d cents",
			after.newBaseAmount, after.newAmount, after.creditLineAmount, wantNet, credit)
	}
	if after.newPresentmentCents != wantPresentment || after.charges != 2 || after.lastChargeAmount != decimal.NewFromInt(wantPresentment).String() || after.lastChargeCurrent != "usd" {
		t.Errorf("new base-fee presentment %d, charges %d (last %s %s), want %d usd charged once",
			after.newPresentmentCents, after.charges, after.lastChargeAmount, after.lastChargeCurrent, wantPresentment)
	}
	if t.Failed() {
		return
	}

	f.changeTier(t, upgradeTier)
	replay := f.state(t)
	if replay.changeAt != after.changeAt || replay.charges != after.charges || replay.baseFeeInvoices != 2 || replay.usageInvoices != after.usageInvoices || replay.newAmount != after.newAmount {
		t.Fatalf("replayed activation changed state: before %+v after %+v", after, replay)
	}
}

func TestAdvanceBilledTierDowngradeCreditsTheExcessToThePrepaidBalance_RealPG(t *testing.T) {
	f := newTierChangeFixture(t, "50.00", firstBaseFeePaid)
	downgradeTier := insertTierChangeTier(t, f.db, "20.00")

	f.changeTier(t, downgradeTier)
	after := f.state(t)

	if after.usageStatus == "draft" || after.usageEnd.Sub(after.changeAt).Abs() > time.Millisecond {
		t.Errorf("old period usage invoice = %s ending %s, want finalized ending at the change %s", after.usageStatus, after.usageEnd, after.changeAt)
	}
	credit := proratedCreditCents(5000, f.start, f.end, after.changeAt)
	excess := credit - 2000
	if excess <= 0 {
		t.Fatalf("fixture credit %d does not exceed the new base fee", credit)
	}
	if after.newAmount != "0.00" || after.newStatus != "paid" || after.newPresentmentCents != 0 || after.charges != 1 {
		t.Errorf("new base-fee invoice amount %s status %s presentment %d, charges %d; want a paid zero invoice and no new charge",
			after.newAmount, after.newStatus, after.newPresentmentCents, after.charges)
	}
	if after.balanceCents != excess || after.balanceTransactions != 1 {
		t.Errorf("prepaid balance %d with %d invoice transactions, want %d from one transaction", after.balanceCents, after.balanceTransactions, excess)
	}
	if t.Failed() {
		return
	}

	f.changeTier(t, downgradeTier)
	replay := f.state(t)
	if replay.balanceCents != excess || replay.balanceTransactions != 1 || replay.charges != 1 || replay.baseFeeInvoices != 2 || replay.changeAt != after.changeAt {
		t.Fatalf("replayed downgrade changed state: before %+v after %+v", after, replay)
	}
}

// An unpaid base-fee invoice earns no credit: the tier change reduces it to the
// share of its period the tenant used, presented at the rate it was issued at,
// and the new base fee is charged in full.
func TestAdvanceBilledTierChangeReducesAnUnpaidPreviousBaseFeeInvoice_RealPG(t *testing.T) { //nolint:funlen // Upgrade and downgrade share the assertions.
	for _, tc := range []struct {
		name               string
		fromBase, toBase   string
		fromCents, toCents int64
	}{
		{name: "upgrade", fromBase: "20.00", toBase: "50.00", fromCents: 2000, toCents: 5000},
		{name: "downgrade", fromBase: "50.00", toBase: "20.00", fromCents: 5000, toCents: 2000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTierChangeFixture(t, tc.fromBase, firstBaseFeeDeclined)
			newTier := insertTierChangeTier(t, f.db, tc.toBase)

			f.changeTier(t, newTier)
			after := f.state(t)
			first := f.firstBaseFee(t)

			unused := proratedCreditCents(tc.fromCents, f.start, f.end, after.changeAt)
			usedCents := tc.fromCents - unused
			wantFirstPresentment := decimal.NewFromInt(usedCents).Mul(decimal.RequireFromString("1.25")).Round(0).IntPart()
			if first.status == "paid" || first.amount != decimal.New(usedCents, -2).StringFixed(2) ||
				first.presentmentCents != wantFirstPresentment || first.reductionLine != decimal.New(-unused, -2).StringFixed(2) {
				t.Errorf("unpaid previous base-fee invoice = %+v, want amount %s presented %d with a -%d cent reduction line, still payable",
					first, decimal.New(usedCents, -2).StringFixed(2), wantFirstPresentment, unused)
			}
			wantNew := decimal.New(tc.toCents, -2).StringFixed(2)
			wantNewPresentment := decimal.NewFromInt(tc.toCents).Mul(decimal.RequireFromString("1.25")).Round(0).IntPart()
			if after.newBaseAmount != wantNew || after.newAmount != wantNew || after.creditLineAmount != "0.00" || after.newStatus == "paid" {
				t.Errorf("new base-fee invoice base %s amount %s credit line %s status %s, want the full %s without credit",
					after.newBaseAmount, after.newAmount, after.creditLineAmount, after.newStatus, wantNew)
			}
			if after.newPresentmentCents != wantNewPresentment || after.charges != 2 || after.lastChargeAmount != decimal.NewFromInt(wantNewPresentment).String() {
				t.Errorf("new base-fee presentment %d, charges %d (last %s), want %d charged after the declined first charge",
					after.newPresentmentCents, after.charges, after.lastChargeAmount, wantNewPresentment)
			}
			if after.balanceCents != 0 || after.balanceTransactions != 0 {
				t.Errorf("prepaid balance %d with %d invoice transactions, want no credit from an unpaid invoice", after.balanceCents, after.balanceTransactions)
			}
			if t.Failed() {
				return
			}

			f.changeTier(t, newTier)
			replay := f.state(t)
			replayFirst := f.firstBaseFee(t)
			if replayFirst != first || replay.newAmount != after.newAmount || replay.charges != after.charges || replay.baseFeeInvoices != 2 {
				t.Fatalf("replayed change moved state: first %+v -> %+v, new %+v -> %+v", first, replayFirst, after, replay)
			}
		})
	}
}

// While a charge of the previous base-fee invoice is still processing, what it
// owes is unknown: the new base-fee invoice waits and is created by the
// invoice job once the charge resolves.
func TestAdvanceBilledTierChangeWaitsForAPendingPreviousBaseFeeCharge_RealPG(t *testing.T) {
	f := newTierChangeFixture(t, "20.00", firstBaseFeeInFlight)
	upgradeTier := insertTierChangeTier(t, f.db, "50.00")

	f.changeTier(t, upgradeTier)
	waiting := f.state(t)
	if waiting.changeAt.Equal(f.start) || waiting.baseFeeInvoices != 1 || waiting.charges != 1 {
		t.Fatalf("while the previous charge is pending: period start %s, %d base-fee invoices, %d charges; want the new period with no base-fee invoice yet",
			waiting.changeAt, waiting.baseFeeInvoices, waiting.charges)
	}

	f.confirmFirstBaseFeePayment(t)
	f.jobs.chargeDueAdvanceBaseFees(context.Background(), time.Now())
	after := f.state(t)
	credit := proratedCreditCents(2000, f.start, f.end, after.changeAt)
	if after.baseFeeInvoices != 2 || after.newAmount != decimal.New(5000-credit, -2).StringFixed(2) || after.charges != 2 {
		t.Fatalf("after the previous charge was paid: %d base-fee invoices, new amount %s, %d charges; want amount %s charged",
			after.baseFeeInvoices, after.newAmount, after.charges, decimal.New(5000-credit, -2).StringFixed(2))
	}
}

func TestExpiredCheckoutUnblocksPreviousBaseFee_RealPG(t *testing.T) {
	f := newTierChangeFixture(t, "20.00", firstBaseFeeInFlight)
	ctx := context.Background()
	first := f.firstBaseFee(t)
	if _, err := f.db.ExecContext(ctx, `UPDATE purser.billing_payments SET tx_id = 'cs_expired' WHERE invoice_id = $1 AND status = 'pending'`, first.id); err != nil {
		t.Fatal(err)
	}
	f.changeTier(t, insertTierChangeTier(t, f.db, "50.00"))
	if f.state(t).baseFeeInvoices != 1 {
		t.Fatal("unresolved payment must block repricing")
	}
	var event StripeWebhookPayload
	event.Data.Object, _ = json.Marshal(map[string]any{
		"id": "cs_expired", "metadata": map[string]string{
			"purpose": string(PurposeInvoice), "tenant_id": f.tenantID, "reference_id": first.id,
		},
	})
	for range 2 {
		if err := f.service.handleStripeCheckoutExpired(event); err != nil {
			t.Fatal(err)
		}
	}
	f.jobs.chargeDueAdvanceBaseFees(ctx, time.Now())
	if got := f.state(t); got.baseFeeInvoices != 2 || got.charges != 2 {
		t.Fatalf("expiry did not unblock advance billing: %+v", got)
	}
	if f.firstBaseFee(t).amount == "20.00" {
		t.Fatal("unused previous base fee was not reduced")
	}

	t.Run("failure cannot overwrite a concurrent confirmation", func(t *testing.T) {
		invoiceID, txIDs := seedHalfPaidInvoice(t, f.db)
		held, err := f.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = held.Rollback() }()
		if _, err = held.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text, 0))`, invoiceID); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			_, updateErr := f.service.updateInvoicePaymentStatus("stripe", txIDs[0], invoiceID, "failed", nil, providerSettlementEvidence{})
			done <- updateErr
		}()
		deadline := time.Now().Add(10 * time.Second)
		for {
			var waiting bool
			if err = f.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_locks WHERE locktype = 'advisory' AND NOT granted)`).Scan(&waiting); err != nil {
				t.Fatal(err)
			}
			if waiting {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("failure handler never waited for the invoice lock")
			}
			time.Sleep(10 * time.Millisecond)
		}
		if _, err = held.ExecContext(ctx, `UPDATE purser.billing_payments SET status = 'confirmed', confirmed_at = NOW() WHERE tx_id = $1`, txIDs[0]); err != nil {
			t.Fatal(err)
		}
		if err = held.Commit(); err != nil {
			t.Fatal(err)
		}
		select {
		case err = <-done:
			if err == nil {
				t.Fatal("stale failure accepted after confirmation")
			}
		case <-time.After(10 * time.Second):
			t.Fatal("failure handler did not finish")
		}
		var status string
		if err = f.db.QueryRowContext(ctx, `SELECT status FROM purser.billing_payments WHERE tx_id = $1`, txIDs[0]).Scan(&status); err != nil || status != "confirmed" {
			t.Fatalf("confirmed payment was overwritten: %s, %v", status, err)
		}
	})
}
