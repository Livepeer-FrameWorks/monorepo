//go:build schema_verify

package purserdb

import (
	"database/sql"
	"github.com/google/uuid"
	"testing"
)

func assertCheckoutIntentScopeAndFailureFence(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := t.Context()
	q := New(db)
	tenant, wrong, id := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := db.ExecContext(ctx, `INSERT INTO purser.payment_provider_intents(id,tenant_id,provider,purpose,status,currency,idempotency_key,submission_state) VALUES($1::uuid,$2,'stripe','tenant_subscription_checkout','pending','EUR',$1::text,'prepared')`, id, tenant); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.Exec(`DELETE FROM purser.payment_provider_intents WHERE tenant_id=$1`, tenant); err != nil {
			t.Error(err)
		}
	}()
	for _, scope := range []string{wrong, tenant} {
		for _, err := range []error{
			q.SetProviderIntentCustomer(ctx, SetProviderIntentCustomerParams{TenantID: scope, IntentID: id, CustomerID: sql.NullString{String: "customer", Valid: true}}),
			q.SetProviderIntentSessionOpen(ctx, SetProviderIntentSessionOpenParams{TenantID: scope, IntentID: id, SessionID: sql.NullString{String: "session", Valid: true}}),
			q.SetProviderIntentPaymentOpen(ctx, SetProviderIntentPaymentOpenParams{TenantID: scope, IntentID: id, PaymentID: sql.NullString{String: "payment", Valid: true}}),
			q.SetProviderIntentSubscriptionOpen(ctx, SetProviderIntentSubscriptionOpenParams{TenantID: scope, IntentID: id, SubscriptionID: sql.NullString{String: "subscription", Valid: true}}),
			q.SetProviderPaymentIntentFailure(ctx, SetProviderPaymentIntentFailureParams{TenantID: scope, IntentID: id, Status: "terminal_failed", LastError: sql.NullString{String: "lookup failed", Valid: true}}),
		} {
			if err != nil {
				t.Fatal(err)
			}
		}
		row, err := q.GetProviderCheckoutReservation(ctx, GetProviderCheckoutReservationParams{TenantID: tenant, IntentID: id})
		if err != nil {
			t.Fatal(err)
		}
		if scope == wrong {
			if row.Status != "pending" || row.ProviderSessionID.Valid || row.ProviderPaymentID.Valid || row.ProviderSubscriptionID.Valid || row.ProviderCustomerID.Valid {
				t.Fatalf("foreign tenant modified checkout: %+v", row)
			}
		} else if row.Status != "provider_call_failed" || row.ProviderSessionID.String != "session" || row.ProviderPaymentID.String != "payment" || row.ProviderSubscriptionID.String != "subscription" {
			t.Fatalf("payable checkout became terminal or failed to bind: %+v", row)
		}
	}
	// A genuinely unsubmitted reservation can release immediately.
	if _, err := db.ExecContext(ctx, `UPDATE purser.payment_provider_intents SET status='pending',provider_session_id=NULL,provider_payment_id=NULL,provider_subscription_id=NULL WHERE tenant_id=$1`, tenant); err != nil {
		t.Fatal(err)
	}
	if err := q.SetProviderPaymentIntentFailure(ctx, SetProviderPaymentIntentFailureParams{TenantID: tenant, IntentID: id, Status: "terminal_failed"}); err != nil {
		t.Fatal(err)
	}
	row, err := q.GetProviderCheckoutReservation(ctx, GetProviderCheckoutReservationParams{TenantID: tenant, IntentID: id})
	if err != nil || row.Status != "terminal_failed" {
		t.Fatalf("unsubmitted checkout did not settle: %s %v", row.Status, err)
	}
}
