//go:build schema_verify

package grpc

import (
	"context"
	"testing"
	"time"

	"frameworks/api_billing/internal/fx"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	internalv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/proto"
)

// An on-session payment of a USD invoice records the EUR the invoice was issued
// for at its presentment rate, whatever today's rate is, and the payment that
// completes the invoice takes the EUR the earlier payment left.
func TestOnSessionInvoicePaymentRecordsTheInvoiceLockedEUR_RealPG(t *testing.T) { //nolint:funlen // One invoice is followed from its first payment to the event.
	configureCardOnlyPayments(t)
	db := startPurserTransitionRealPG(t)
	ctx := context.Background()
	now := time.Now().UTC()
	presentedOn := now.AddDate(0, 0, -1)
	storeECBRates(t, db, presentedOn, map[string]string{"USD": "1.2500"})
	storeECBRates(t, db, now, map[string]string{"USD": "1.1000"})

	tierID := uuid.NewString()
	tenantID := uuid.NewString()
	invoiceID := uuid.NewString()
	if _, err := db.ExecContext(ctx, `INSERT INTO purser.billing_tiers (id, tier_name, display_name) VALUES ($1, $2, 'Invoice FX contract')`,
		tierID, "invoice-fx-"+tierID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.tenant_subscriptions (tenant_id, tier_id, status, billing_model, presentment_currency)
		VALUES ($1, $2, 'active', 'postpaid', 'USD')
	`, tenantID, tierID); err != nil {
		t.Fatal(err)
	}
	// 10.03 EUR presented as 1254 USD cents at 1.25. Its two halves of 627 USD
	// cents are 501.5 EUR cents each.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.billing_invoices (
			id, tenant_id, status, currency, amount, due_date,
			presentment_amount_cents, presentment_currency, presentment_units_per_eur,
			presentment_reference_date, finalized_at
		) VALUES ($1, $2, 'pending', 'EUR', 10.03, NOW() + INTERVAL '14 days', 1254, 'USD', 1.25, $3, NOW() - INTERVAL '1 day')
	`, invoiceID, tenantID, fx.Date(presentedOn)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.billing_payments (
			invoice_id, method, amount, currency, status, tx_id, confirmed_at,
			original_amount_cents, original_currency, eur_amount_cents, fx_units_per_eur, fx_source, fx_reference_date
		) VALUES ($1, 'card', 6.27, 'USD', 'confirmed', 'pi_first_half', NOW(), 627, 'USD', 502, 1.25, 'ecb', $2)
	`, invoiceID, fx.Date(presentedOn)); err != nil {
		t.Fatal(err)
	}

	server := &PurserServer{
		db: db, logger: logging.NewLogger(),
		invoiceCardCheckout: func(context.Context, string, string, string, decimal.Decimal, string, string) (string, string, error) {
			return "https://checkout.example.test/invoice-fx", "cs_invoice_fx", nil
		},
	}
	tenantCtx := context.WithValue(context.WithValue(ctx, ctxkeys.KeyTenantID, tenantID), ctxkeys.KeyUserID, uuid.NewString())
	response, err := server.CreatePayment(tenantCtx, &purserpb.PaymentRequest{InvoiceId: invoiceID, Method: "card"})
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	conversion := response.GetFx()
	if response.GetCurrency() != "USD" || response.GetAmount() != 6.27 || conversion.GetEurAmountCents() != 501 ||
		conversion.GetUnitsPerEur() != "1.25" || conversion.GetReferenceDate() != fx.Date(presentedOn).Format(time.DateOnly) {
		t.Errorf("payment response = %s %.2f with conversion %+v, want USD 6.27 as 501 EUR cents at 1.25 of %s",
			response.GetCurrency(), response.GetAmount(), conversion, fx.Date(presentedOn).Format(time.DateOnly))
	}
	var storedEUR int64
	if err := db.QueryRowContext(ctx, `SELECT eur_amount_cents FROM purser.billing_payments WHERE id = $1::uuid`, response.GetId()).Scan(&storedEUR); err != nil {
		t.Fatal(err)
	}
	var payload []byte
	if err := db.QueryRowContext(ctx, `
		SELECT payload FROM purser.domain_event_outbox WHERE aggregate_id = $1 AND event_type = 'billing.payment_created'
	`, response.GetId()).Scan(&payload); err != nil {
		t.Fatalf("payment_created event: %v", err)
	}
	created := &internalv1.PaymentCreated{}
	if err := proto.Unmarshal(payload, created); err != nil {
		t.Fatal(err)
	}
	if storedEUR != 501 || created.GetAmount().GetAmountMinor() != 501 || created.GetAmount().GetCurrency() != "EUR" {
		t.Errorf("stored payment EUR %d, payment_created %v; want 501 EUR cents in both", storedEUR, created.GetAmount())
	}
}
