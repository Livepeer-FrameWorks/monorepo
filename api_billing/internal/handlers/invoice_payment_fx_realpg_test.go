//go:build schema_verify

package handlers

import (
	"context"
	"database/sql"
	"net/http"
	"testing"
	"time"

	"frameworks/api_billing/internal/fx"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// insertPresentedUSDInvoice stores a pending invoice of eurCents presented as
// presentmentCents USD at unitsPerEUR on referenceDate.
func insertPresentedUSDInvoice(t *testing.T, db *sql.DB, tenantID string, eurCents, presentmentCents int64, unitsPerEUR string, referenceDate time.Time) string {
	t.Helper()
	invoiceID := uuid.NewString()
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO purser.billing_invoices (
			id, tenant_id, status, currency, amount, due_date, period_start, period_end,
			presentment_amount_cents, presentment_currency, presentment_units_per_eur,
			presentment_reference_date, finalized_at
		) VALUES ($1, $2, 'pending', 'EUR', $3::numeric, NOW() + INTERVAL '14 days',
		          NOW() - INTERVAL '31 days', NOW() - INTERVAL '1 day',
		          $4, 'USD', $5::numeric, $6, NOW() - INTERVAL '1 day')
	`, invoiceID, tenantID, decimal.New(eurCents, -2).StringFixed(2), presentmentCents, unitsPerEUR, fx.Date(referenceDate)); err != nil {
		t.Fatalf("insert presented invoice: %v", err)
	}
	return invoiceID
}

type storedPaymentFX struct {
	original, eur        int64
	currency, units, src string
	referenceDate        string
}

func invoicePaymentFX(t *testing.T, db *sql.DB, invoiceID string) []storedPaymentFX {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `
		SELECT original_amount_cents, eur_amount_cents, original_currency, fx_units_per_eur::text, fx_source,
		       fx_reference_date::text
		FROM purser.billing_payments WHERE invoice_id = $1::uuid ORDER BY created_at
	`, invoiceID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []storedPaymentFX
	for rows.Next() {
		var p storedPaymentFX
		if err := rows.Scan(&p.original, &p.eur, &p.currency, &p.units, &p.src, &p.referenceDate); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// An off-session charge of a USD invoice records the EUR the invoice was
// issued for, at the rate it was presented at, whatever today's rate is.
func TestOffSessionInvoicePaymentsRecordTheInvoiceLockedEUR_RealPG(t *testing.T) { //nolint:funlen // Stripe and Mollie share the invoice fixture.
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	now := time.Now().UTC()
	presentedOn := now.AddDate(0, 0, -1)
	storeRate(t, db, "USD", presentedOn, "1.2500")
	storeRate(t, db, "USD", now, "1.1000")

	stripe, stripeClient := installStripeFixture(t)
	stripe.route(http.MethodGet, "/v1/customers/cus_invoice_fx", func(recordedProviderRequest) (int, any) {
		return http.StatusOK, map[string]any{
			"id": "cus_invoice_fx", "object": "customer",
			"invoice_settings": map[string]any{"default_payment_method": map[string]any{"id": "pm_invoice_fx", "object": "payment_method"}},
		}
	})
	stripe.route(http.MethodPost, "/v1/payment_intents", func(request recordedProviderRequest) (int, any) {
		return http.StatusOK, map[string]any{"id": "pi_" + request.form.Get("metadata[billing_payment_id]"), "object": "payment_intent", "status": "processing"}
	})
	mollie, mollieClient := installMollieFixture(t)
	mollie.route(http.MethodPost, "/v2/customers/cst_invoice_fx/payments", func(recordedProviderRequest) (int, any) {
		return http.StatusCreated, map[string]any{"resource": "payment", "id": "tr_invoice_fx", "status": "open"}
	})
	jobs := &JobManager{db: db, logger: logging.NewLogger(), billing: &Service{
		db: db, logger: logging.NewLogger(), stripeClient: stripeClient, mollieClient: mollieClient,
	}}

	tierID := insertTierChangeTier(t, db, "20.00")
	insertTenant := func(paymentMethod string) string {
		tenantID := uuid.NewString()
		if _, err := db.ExecContext(ctx, `
			INSERT INTO purser.tenant_subscriptions (tenant_id, tier_id, status, billing_model, payment_method, stripe_customer_id, presentment_currency)
			VALUES ($1, $2, 'active', 'postpaid', $3, NULLIF($4, ''), 'USD')
		`, tenantID, tierID, paymentMethod, map[string]string{"stripe": "cus_invoice_fx"}[paymentMethod]); err != nil {
			t.Fatal(err)
		}
		return tenantID
	}
	want := storedPaymentFX{original: 2500, eur: 2000, currency: "USD", units: "1.2500000000", src: fx.SourceECB, referenceDate: fx.Date(presentedOn).Format(time.DateOnly)}

	stripeTenant := insertTenant("stripe")
	stripeInvoice := insertPresentedUSDInvoice(t, db, stripeTenant, 2000, 2500, "1.25", presentedOn)
	if err := jobs.chargeStripeOverage(ctx, stripeTenant, stripeInvoice, decimal.RequireFromString("25.00"), "USD"); err != nil {
		t.Fatalf("chargeStripeOverage: %v", err)
	}
	if got := invoicePaymentFX(t, db, stripeInvoice); len(got) != 1 || got[0] != want {
		t.Errorf("Stripe off-session payment FX = %+v, want %+v", got, want)
	}

	mollieTenant := insertTenant("mollie")
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.mollie_customers (tenant_id, mollie_customer_id) VALUES ($1, 'cst_invoice_fx');
	`, mollieTenant); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.mollie_mandates (tenant_id, mollie_customer_id, mollie_mandate_id, status, method)
		VALUES ($1, 'cst_invoice_fx', 'mdt_invoice_fx', 'valid', 'creditcard')
	`, mollieTenant); err != nil {
		t.Fatal(err)
	}
	mollieInvoice := insertPresentedUSDInvoice(t, db, mollieTenant, 2000, 2500, "1.25", presentedOn)
	if err := jobs.chargeMollieOverage(ctx, mollieTenant, mollieInvoice, decimal.RequireFromString("25.00"), "USD"); err != nil {
		t.Fatalf("chargeMollieOverage: %v", err)
	}
	if got := invoicePaymentFX(t, db, mollieInvoice); len(got) != 1 || got[0] != want {
		t.Errorf("Mollie off-session payment FX = %+v, want %+v", got, want)
	}
}
