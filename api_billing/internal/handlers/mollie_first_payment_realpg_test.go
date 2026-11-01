//go:build schema_verify

package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/outbox"
	"github.com/google/uuid"
)

// A Mollie first payment is activated by the presentment currency it was
// created under: an EUR base-fee payment never also starts advance billing,
// and a zero-amount mandate payment always does. While it is pending, the
// tenant's presentment currency cannot change.
func TestMollieFirstPaymentUsesThePresentmentCurrencyItWasCreatedUnder_RealPG(t *testing.T) { //nolint:funlen // Both currency directions share the webhook fixture.
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	fixture, mollieClient := installMollieFixture(t)
	service := &Service{db: db, logger: logging.NewLogger(), mollieClient: mollieClient}
	service.chargeAdvanceBaseFee = func(ctx context.Context, tenantID string) error {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM purser.mollie_mandates WHERE tenant_id=$1 AND status='valid'`, tenantID).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			t.Errorf("advance charge ran without its committed mandate: %d", count)
		}
		return nil
	}

	tierID := insertTierChangeTier(t, db, "20.00")

	cases := []struct {
		name                  string
		createdUnder, changed string
		amount                string
		wantActivated         bool
	}{
		{name: "EUR base-fee payment after a change to USD", createdUnder: "EUR", changed: "USD", amount: "20.00", wantActivated: false},
		{name: "USD mandate payment after a change to EUR", createdUnder: "USD", changed: "EUR", amount: "0.00", wantActivated: true},
	}
	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tenantID := uuid.NewString()
			paymentID := fmt.Sprintf("tr_first_%d", index)
			customerID := fmt.Sprintf("cst_first_%d", index)
			mandateID := fmt.Sprintf("mdt_first_%d", index)
			if _, err := db.ExecContext(ctx, `
				INSERT INTO purser.tenant_subscriptions (tenant_id, tier_id, status, billing_model, presentment_currency)
				VALUES ($1, $2, 'active', 'prepaid', $3)
			`, tenantID, tierID, tc.createdUnder); err != nil {
				t.Fatal(err)
			}
			// CreateFirstPayment records the intent in the currency the payment is
			// created in and names the Mollie payment on it.
			queries := purserdb.New(db)
			intentID, err := queries.UpsertMollieFirstPaymentIntent(ctx, purserdb.UpsertMollieFirstPaymentIntentParams{
				TenantID: tenantID, TierID: tierID, Currency: tc.createdUnder,
				AmountCents:    map[string]int64{"20.00": 2000, "0.00": 0}[tc.amount],
				IdempotencyKey: "first-payment-contract:" + tenantID,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := queries.SetProviderIntentPaymentOpen(ctx, purserdb.SetProviderIntentPaymentOpenParams{TenantID: tenantID,
				PaymentID: sql.NullString{String: paymentID, Valid: true}, IntentID: intentID,
			}); err != nil {
				t.Fatal(err)
			}

			_, lockErr := db.ExecContext(ctx, `UPDATE purser.tenant_subscriptions SET presentment_currency = $2 WHERE tenant_id = $1`, tenantID, tc.changed)
			if lockErr == nil || !strings.Contains(lockErr.Error(), "PRESENTMENT_CURRENCY_LOCKED") {
				t.Errorf("presentment currency change with a pending first payment = %v, want PRESENTMENT_CURRENCY_LOCKED", lockErr)
			}

			// The lock covers the time Mollie keeps a first payment payable; a
			// webhook later than that finds the currency changed.
			if _, err := db.ExecContext(ctx, `
				UPDATE purser.payment_provider_intents SET created_at = NOW() - INTERVAL '2 days', updated_at = NOW() - INTERVAL '2 days'
				WHERE id = $1::uuid
			`, intentID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, `UPDATE purser.tenant_subscriptions SET presentment_currency = $2 WHERE tenant_id = $1`, tenantID, tc.changed); err != nil {
				t.Fatalf("presentment currency change after the payment window: %v", err)
			}

			fixture.route(http.MethodGet, "/v2/payments/"+paymentID, func(recordedProviderRequest) (int, any) {
				return http.StatusOK, map[string]any{
					"resource": "payment", "id": paymentID, "status": "paid", "sequenceType": "first",
					"customerId": customerID, "mandateId": mandateID,
					"amount":    map[string]string{"currency": tc.createdUnder, "value": tc.amount},
					"createdAt": time.Now().Add(-49 * time.Hour).UTC().Format(time.RFC3339),
					"metadata":  map[string]string{"tenant_id": tenantID, "tier_id": tierID, "payment_type": "first_payment"},
				}
			})
			fixture.route(http.MethodGet, "/v2/customers/"+customerID+"/mandates/"+mandateID, func(recordedProviderRequest) (int, any) {
				return http.StatusOK, map[string]any{
					"resource": "mandate", "id": mandateID, "status": "valid", "method": "creditcard",
					"createdAt": time.Now().Add(-49 * time.Hour).UTC().Format(time.RFC3339),
				}
			})
			if _, err := service.handleMolliePaymentWebhook(ctx, paymentID, []byte("id="+paymentID)); err != nil {
				t.Fatalf("first payment webhook: %v", err)
			}

			var paymentMethod sql.NullString
			var periodStart sql.NullTime
			if err := db.QueryRowContext(ctx, `
				SELECT payment_method, billing_period_start FROM purser.tenant_subscriptions WHERE tenant_id = $1
			`, tenantID).Scan(&paymentMethod, &periodStart); err != nil {
				t.Fatal(err)
			}
			activated := paymentMethod.String == "mollie" && periodStart.Valid
			if activated != tc.wantActivated {
				t.Errorf("advance billing activated = %v (payment method %q), want %v for a payment created in %s",
					activated, paymentMethod.String, tc.wantActivated, tc.createdUnder)
			}
		})
	}
}

func TestMollieFirstPaymentRecoveryWinsDuringMandateFetch_RealPG(t *testing.T) {
	db := startProviderReservationDatabase(t)
	ctx := t.Context()
	fixture, client := installMollieFixture(t)
	tenant := uuid.NewString()
	tier := insertTierChangeTier(t, db, "20.00")
	q := purserdb.New(db)
	if _, err := db.Exec(`INSERT INTO purser.tenant_subscriptions(tenant_id,tier_id,status,billing_model)VALUES($1,$2,'active','prepaid')`, tenant, tier); err != nil {
		t.Fatal(err)
	}
	id, err := q.UpsertMollieFirstPaymentIntent(ctx, purserdb.UpsertMollieFirstPaymentIntentParams{TenantID: tenant, TierID: tier, Currency: "EUR", IdempotencyKey: tenant, AmountCents: 2000})
	if err != nil {
		t.Fatal(err)
	}
	if err = q.SetProviderIntentPaymentOpen(ctx, purserdb.SetProviderIntentPaymentOpenParams{TenantID: tenant, IntentID: id, PaymentID: sql.NullString{String: "tr_race", Valid: true}}); err != nil {
		t.Fatal(err)
	}
	paidAt := time.Now().UTC().Truncate(time.Second)
	fixture.route(http.MethodGet, "/v2/payments/tr_race", func(recordedProviderRequest) (int, any) {
		return 200, map[string]any{"id": "tr_race", "status": "paid", "paidAt": paidAt.Format(time.RFC3339), "sequenceType": "first", "customerId": "customer", "mandateId": "mandate", "amount": map[string]string{"currency": "EUR", "value": "20.00"}, "metadata": map[string]string{"tenant_id": tenant, "payment_type": "first_payment", "checkout_reservation_id": id}}
	})
	fixture.route(http.MethodGet, "/v2/customers/customer/mandates/mandate", func(recordedProviderRequest) (int, any) {
		if err := q.FinishProviderCheckoutIntent(ctx, purserdb.FinishProviderCheckoutIntentParams{TenantID: tenant, IntentID: id, Status: "expired"}); err != nil {
			return 500, map[string]string{"detail": err.Error()}
		}
		return 200, map[string]any{"id": "mandate", "status": "valid", "method": "creditcard", "createdAt": time.Now().UTC().Format(time.RFC3339)}
	})
	svc := &Service{db: db, logger: logging.NewLogger(), mollieClient: client}
	checkMandates := func() {
		t.Helper()
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM purser.mollie_mandates WHERE tenant_id=$1`, tenant).Scan(&count); err != nil || count != 0 {
			t.Fatalf("expired first payment installed a mandate: count=%d err=%v", count, err)
		}
	}
	body := []byte("id=tr_race")
	if ok, _, status := svc.processMollieWebhookPayload(ctx, "tr_race", body); ok || status != 500 {
		t.Fatalf("expiry during mandate fetch must retry, got ok=%v status=%d", ok, status)
	}
	checkMandates()
	var webhookStatus string
	if err := db.QueryRowContext(ctx, `SELECT status FROM purser.webhook_events WHERE provider='mollie'`).Scan(&webhookStatus); err != nil || webhookStatus != "failed_retryable" {
		t.Fatalf("failed callback lost retry ownership: status=%s err=%v", webhookStatus, err)
	}
	fixture.route(http.MethodPost, "/v2/payments/tr_race/refunds", func(recordedProviderRequest) (int, any) {
		return 201, map[string]any{"id": "refund", "status": "pending"}
	})
	if ok, _, status := svc.processMollieWebhookPayload(ctx, "tr_race", body); !ok || status != 200 {
		t.Fatalf("redelivery did not hand the expired payment to recovery: ok=%v status=%d", ok, status)
	}
	var recoveryStatus string
	if err := db.QueryRowContext(ctx, `SELECT status FROM purser.provider_webhook_inbox WHERE provider='checkout_recovery' AND headers->>'tenant_id'=$1 AND event_key='checkout-expiry:'||$2`, tenant, id).Scan(&recoveryStatus); err != nil || recoveryStatus != "pending" {
		t.Fatalf("acknowledged callback has no durable refund owner: status=%s err=%v", recoveryStatus, err)
	}
	if len(fixture.matching("POST", "/v2/payments/tr_race/refunds")) != 0 || len(fixture.matching("GET", "/v2/customers/customer/mandates/mandate")) != 1 {
		t.Fatal("redelivery refunded inline or repeated mandate activation")
	}
	worker := outbox.Worker[providerWebhookPayload]{Config: providerWebhookWorkerConfig(), Store: &providerWebhookInboxStore{db: db}, Dispatcher: &providerWebhookDispatcher{service: svc}, Logger: logging.NewLogger()}
	worker.ProcessBatch(ctx)
	receipt, err := q.GetProviderCheckoutReservation(ctx, purserdb.GetProviderCheckoutReservationParams{TenantID: tenant, IntentID: id})
	if err != nil || !receipt.CompensationCurrent || receipt.RecoveryOutcome.String != "refund_requested" || len(fixture.matching("POST", "/v2/payments/tr_race/refunds")) != 1 {
		t.Fatalf("inbox did not own exactly one refund: receipt=%+v err=%v", receipt, err)
	}
	if ok, _, status := svc.processMollieWebhookPayload(ctx, "tr_race", body); !ok || status != 200 {
		t.Fatalf("duplicate callback: ok=%v status=%d", ok, status)
	}
	checkMandates()
	if len(fixture.matching("POST", "/v2/payments/tr_race/refunds")) != 1 {
		t.Fatal("duplicate callback repeated refund")
	}
	if err := db.QueryRowContext(ctx, `SELECT status FROM purser.webhook_events WHERE provider='mollie'`).Scan(&webhookStatus); err != nil || webhookStatus != "processed" {
		t.Fatalf("redelivery not processed: %s %v", webhookStatus, err)
	}
}
