//go:build schema_verify

package handlers

import (
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"github.com/google/uuid"
)

func TestAdvanceActivationStartsOneCappedCalendarMonth_RealPG(t *testing.T) {
	for _, provider := range []string{"stripe", "mollie"} {
		t.Run(provider, func(t *testing.T) {
			db := startProviderReservationDatabase(t)
			at := time.Date(2026, time.January, 31, 18, 23, 0, 0, time.UTC)
			tenant := newPhaseTenant(t, db, "prepaid", at.AddDate(0, 0, -15), at.AddDate(0, 0, 15), 0)
			if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET presentment_currency='USD' WHERE tenant_id=$1`, tenant.id); err != nil {
				t.Fatal(err)
			}
			receiptID := uuid.NewString()
			purpose := "tenant_subscription_checkout"
			if provider == "mollie" {
				purpose = "mollie_first_payment"
			}
			if _, err := db.Exec(`INSERT INTO purser.payment_provider_intents(id,tenant_id,provider,purpose,status,currency,amount_cents,idempotency_key,expires_at) VALUES($1::uuid,$2,$3,$4,'provider_open','USD',0,$1::text,NOW()+INTERVAL '1 hour')`, receiptID, tenant.id, provider, purpose); err != nil {
				t.Fatal(err)
			}
			if err := tenant.svc.activateAdvanceBilledTenant(t.Context(), tenant.id, tenant.supporterID, provider, "customer", at, receiptID); err != nil {
				t.Fatal(err)
			}
			receipt, readErr := purserdb.New(db).GetProviderCheckoutReservation(t.Context(), purserdb.GetProviderCheckoutReservationParams{TenantID: tenant.id, IntentID: receiptID})
			if readErr != nil || receipt.Status != "succeeded" || !receipt.ActivatedAt.Valid {
				t.Fatalf("activation did not consume mandate/setup receipt: %+v %v", receipt, readErr)
			}
			var start, end time.Time
			if err := db.QueryRow(`SELECT billing_period_start,billing_period_end FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant.id).Scan(&start, &end); err != nil {
				t.Fatal(err)
			}
			want := time.Date(2026, time.February, 28, 18, 23, 0, 0, time.UTC)
			if !start.Equal(at) || !end.Equal(want) {
				t.Fatalf("%s activation calendar period=[%s,%s); want [%s,%s)", provider, start, end, at, want)
			}
		})
	}
}
