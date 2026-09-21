//go:build schema_verify

package handlers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/google/uuid"
)

func TestPrepaidTopupReversalLedger_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	service := &Service{db: db, logger: logging.NewLogger()}

	// insertTopupAt stores a pending top-up converted to EUR at unitsPerEUR,
	// the way CreateCardTopup records it.
	insertTopupAt := func(t *testing.T, tenantID, provider, checkoutID, currency string, amountCents, eurCents int64, unitsPerEUR, source string) string {
		t.Helper()
		topupID := uuid.NewString()
		if _, err := db.ExecContext(ctx, `
			INSERT INTO purser.pending_topups (id, tenant_id, provider, checkout_id, amount_cents, currency, status, expires_at,
			    original_amount_cents, original_currency, eur_amount_cents, fx_units_per_eur, fx_source, fx_reference_date)
			VALUES ($1, $2, $3, NULLIF($4, ''), $5::bigint, $6::text, 'pending', NOW() + INTERVAL '1 hour',
			    $5::bigint, $6::text, $7, $8::numeric, $9, CURRENT_DATE)
		`, topupID, tenantID, provider, checkoutID, amountCents, currency, eurCents, unitsPerEUR, source); err != nil {
			t.Fatalf("insert pending top-up: %v", err)
		}
		return topupID
	}
	insertTopup := func(t *testing.T, tenantID, provider, checkoutID, currency string, amountCents int64) string {
		t.Helper()
		return insertTopupAt(t, tenantID, provider, checkoutID, currency, amountCents, amountCents, "1", "identity")
	}
	countInt := func(t *testing.T, query string, args ...any) int {
		t.Helper()
		var n int
		if err := db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
			t.Fatalf("count %q: %v", query, err)
		}
		return n
	}
	stripeRefund := func(t *testing.T, chargeID, paymentIntent, refundID, currency string, amountCents int64) error {
		t.Helper()
		charge := map[string]any{
			"id": chargeID, "payment_intent": paymentIntent, "currency": currency,
			"refunds": map[string]any{"data": []map[string]any{{
				"id": refundID, "amount": amountCents, "currency": currency,
				"reason": "requested_by_customer", "status": "succeeded",
			}}},
		}
		raw, err := json.Marshal(charge)
		if err != nil {
			t.Fatal(err)
		}
		payload := StripeWebhookPayload{ID: "evt_" + refundID, Type: "charge.refunded"}
		payload.Data.Object = raw
		return service.handleStripeChargeRefunded(payload)
	}

	t.Run("refunds of a USD top-up debit the stored EUR proportionally", func(t *testing.T) {
		tenantID := seedPrepaidLedgerTenant(t, db)
		// 2500 USD cents at 1.2345 USD per EUR credited 2025 EUR cents. Today's
		// rate differs, so a reconversion at refund time would debit other amounts.
		storeRate(t, db, "USD", time.Now().UTC(), "1.1000")
		topupID := insertTopupAt(t, tenantID, "stripe", "", "USD", 2500, 2025, "1.2345", "ecb")
		pi := "pi_usd_" + topupID
		if err := service.handlePrepaidCheckoutCompleted(ctx, "cs_usd_"+topupID, pi, tenantID, topupID, 2500, "usd", ProviderStripe, true); err != nil {
			t.Fatalf("handlePrepaidCheckoutCompleted: %v", err)
		}
		if got := admissionBalanceCents(t, db, tenantID); got != 2025 {
			t.Fatalf("admission balance after USD top-up = %d, want 2025", got)
		}

		firstRefund := "re_usd_first_" + topupID
		if err := stripeRefund(t, "ch_usd_"+topupID, pi, firstRefund, "usd", 1000); err != nil {
			t.Fatalf("charge.refunded: %v", err)
		}
		if err := stripeRefund(t, "ch_usd_"+topupID, pi, firstRefund, "usd", 1000); err != nil {
			t.Fatalf("replayed charge.refunded: %v", err)
		}
		if got := admissionBalanceCents(t, db, tenantID); got != 1215 {
			t.Fatalf("admission balance after 1000 USD refund = %d, want 1215", got)
		}
		if _, err := service.applyProviderReversal(ctx, providerReversalInput{
			provider: "stripe", reversalType: "refund", providerReversalID: "re_usd_rest_" + topupID,
			providerChargeID: "ch_usd_" + topupID, providerPaymentID: pi,
			amountCents: 1500, currency: "usd", reason: "requested_by_customer",
		}); err != nil {
			t.Fatalf("applyProviderReversal: %v", err)
		}
		if got := admissionBalanceCents(t, db, tenantID); got != 0 {
			t.Fatalf("admission balance after refunding the whole top-up = %d, want 0", got)
		}

		rows, err := db.QueryContext(ctx, `
			SELECT original_amount_cents, original_currency, eur_amount_cents, fx_units_per_eur::text, fx_source
			FROM purser.payment_reversals
			WHERE pending_topup_id = $1 AND status = 'succeeded'
			ORDER BY original_amount_cents
		`, topupID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		type reversal struct {
			original, eur                int64
			originalCurrency, units, src string
		}
		var got []reversal
		for rows.Next() {
			var r reversal
			if err := rows.Scan(&r.original, &r.originalCurrency, &r.eur, &r.units, &r.src); err != nil {
				t.Fatal(err)
			}
			got = append(got, r)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		want := []reversal{
			{original: 1000, eur: 810, originalCurrency: "USD", units: "1.2345000000", src: "ecb"},
			{original: 1500, eur: 1215, originalCurrency: "USD", units: "1.2345000000", src: "ecb"},
		}
		if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("reversal rows = %+v, want %+v", got, want)
		}
		if n := countInt(t, `SELECT COUNT(*) FROM purser.balance_transactions WHERE tenant_id = $1 AND reference_type = 'payment_reversal'`, tenantID); n != 2 {
			t.Fatalf("reversal debit rows = %d, want 2", n)
		}
	})

	t.Run("partial refunds that complete a USD top-up return exactly the credited EUR", func(t *testing.T) {
		tenantID := seedPrepaidLedgerTenant(t, db)
		// 1000 USD cents at 1.1669 USD per EUR credited 857 EUR cents. Refunds of
		// 333, 333 and 334 cents are 285.38, 285.38 and 286.24 EUR cents of it.
		topupID := insertTopupAt(t, tenantID, "stripe", "", "USD", 1000, 857, "1.1669", "ecb")
		pi := "pi_parts_" + topupID
		if err := service.handlePrepaidCheckoutCompleted(ctx, "cs_parts_"+topupID, pi, tenantID, topupID, 1000, "usd", ProviderStripe, true); err != nil {
			t.Fatalf("handlePrepaidCheckoutCompleted: %v", err)
		}
		for index, amount := range []int64{333, 333, 334} {
			if _, err := service.applyProviderReversal(ctx, providerReversalInput{
				provider: "stripe", reversalType: "refund", providerReversalID: "re_parts_" + string(rune('a'+index)) + "_" + topupID,
				providerChargeID: "ch_parts_" + topupID, providerPaymentID: pi,
				amountCents: amount, currency: "usd", reason: "requested_by_customer",
			}); err != nil {
				t.Fatalf("applyProviderReversal %d: %v", index, err)
			}
		}
		var reversedEUR int64
		if err := db.QueryRowContext(ctx, `
			SELECT COALESCE(SUM(eur_amount_cents), 0) FROM purser.payment_reversals
			WHERE pending_topup_id = $1 AND status = 'succeeded'
		`, topupID).Scan(&reversedEUR); err != nil {
			t.Fatal(err)
		}
		if got := admissionBalanceCents(t, db, tenantID); got != 0 || reversedEUR != 857 {
			t.Fatalf("after refunding the whole top-up in parts: balance %d, reversed EUR %d; want 0 and 857", got, reversedEUR)
		}
	})

	t.Run("refund of a credited top-up debits the ledger balance once with lowercase provider currency", func(t *testing.T) {
		tenantID := seedPrepaidLedgerTenant(t, db)
		topupID := insertTopup(t, tenantID, "stripe", "", "EUR", 2500)
		pi := "pi_credited_" + topupID
		if err := service.handlePrepaidCheckoutCompleted(ctx, "cs_credited_"+topupID, pi, tenantID, topupID, 2500, "eur", ProviderStripe, true); err != nil {
			t.Fatalf("handlePrepaidCheckoutCompleted: %v", err)
		}

		refund := providerReversalInput{
			provider: "stripe", reversalType: "refund", providerReversalID: "re_credited_" + topupID,
			providerChargeID: "ch_credited_" + topupID, providerPaymentID: pi,
			amountCents: 1000, currency: "eur", reason: "requested_by_customer",
		}
		applied, err := service.applyProviderReversal(ctx, refund)
		if err != nil {
			t.Fatalf("applyProviderReversal: %v", err)
		}
		if !applied {
			t.Fatal("first refund delivery was not applied")
		}
		applied, err = service.applyProviderReversal(ctx, refund)
		if err != nil {
			t.Fatalf("replayed applyProviderReversal: %v", err)
		}
		if applied {
			t.Fatal("replayed refund was applied twice")
		}

		if got := admissionBalanceCents(t, db, tenantID); got != 1500 {
			t.Fatalf("admission balance = %d, want 1500", got)
		}
		if n := countInt(t, `SELECT COUNT(*) FROM purser.balance_transactions WHERE tenant_id = $1 AND reference_type = 'payment_reversal' AND amount_cents = -1000`, tenantID); n != 1 {
			t.Fatalf("refund debit rows = %d, want 1", n)
		}
		if n := countInt(t, `SELECT COUNT(*) FROM purser.prepaid_balances WHERE tenant_id = $1 AND currency <> $2`, tenantID, billing.LedgerCurrency); n != 0 {
			t.Fatalf("refund created %d non-ledger balance rows", n)
		}
		if n := countInt(t, `SELECT COUNT(*) FROM purser.payment_reversals WHERE provider_reversal_id = $1 AND currency = 'EUR' AND status = 'succeeded'`, refund.providerReversalID); n != 1 {
			t.Fatalf("succeeded EUR reversal rows = %d, want 1", n)
		}
	})

	t.Run("refund observed before settlement blocks the later credit", func(t *testing.T) {
		tenantID := seedPrepaidLedgerTenant(t, db)
		paymentID := "tr_full_" + uuid.NewString()[:8]
		topupID := insertTopup(t, tenantID, "mollie", paymentID, "EUR", 2500)

		if _, err := service.applyProviderReversal(ctx, providerReversalInput{
			provider: "mollie", reversalType: "refund", providerReversalID: "mollie-refund:" + paymentID + ":2500",
			providerChargeID: paymentID, providerPaymentID: paymentID, amountCents: 2500, currency: "EUR", reason: "refund",
		}); err != nil {
			t.Fatalf("applyProviderReversal: %v", err)
		}
		if err := service.handlePrepaidCheckoutCompleted(ctx, paymentID, paymentID, tenantID, topupID, 2500, "EUR", ProviderMollie, true); err != nil {
			t.Fatalf("handlePrepaidCheckoutCompleted: %v", err)
		}

		if n := countInt(t, `SELECT COUNT(*) FROM purser.balance_transactions WHERE tenant_id = $1`, tenantID); n != 0 {
			t.Fatalf("refunded top-up wrote %d balance transactions, want 0", n)
		}
		if got := admissionBalanceCents(t, db, tenantID); got != 0 {
			t.Fatalf("admission balance = %d, want 0", got)
		}
		if n := countInt(t, `SELECT COUNT(*) FROM purser.payment_reversals WHERE pending_topup_id = $1 AND status = 'needs_review'`, topupID); n != 0 {
			t.Fatalf("fully refunded top-up left %d review holds", n)
		}
	})

	t.Run("partial refund observed before settlement holds the remainder for review", func(t *testing.T) {
		tenantID := seedPrepaidLedgerTenant(t, db)
		paymentID := "tr_part_" + uuid.NewString()[:8]
		topupID := insertTopup(t, tenantID, "mollie", paymentID, "EUR", 2500)

		if _, err := service.applyProviderReversal(ctx, providerReversalInput{
			provider: "mollie", reversalType: "refund", providerReversalID: "mollie-refund:" + paymentID + ":1000",
			providerChargeID: paymentID, providerPaymentID: paymentID, amountCents: 1000, currency: "EUR", reason: "refund",
		}); err != nil {
			t.Fatalf("applyProviderReversal: %v", err)
		}
		if err := service.handlePrepaidCheckoutCompleted(ctx, paymentID, paymentID, tenantID, topupID, 2500, "EUR", ProviderMollie, true); err != nil {
			t.Fatalf("handlePrepaidCheckoutCompleted: %v", err)
		}

		if n := countInt(t, `SELECT COUNT(*) FROM purser.balance_transactions WHERE tenant_id = $1`, tenantID); n != 0 {
			t.Fatalf("partially refunded top-up wrote %d balance transactions, want 0", n)
		}
		if n := countInt(t, `SELECT COUNT(*) FROM purser.payment_reversals WHERE pending_topup_id = $1 AND status = 'needs_review' AND operator_review_required`, topupID); n != 1 {
			t.Fatalf("partially refunded top-up review holds = %d, want 1", n)
		}
	})
}
