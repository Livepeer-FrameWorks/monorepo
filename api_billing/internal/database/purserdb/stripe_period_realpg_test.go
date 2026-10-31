//go:build schema_verify

package purserdb

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
)

func assertStripeSyncPreservesBillingPhase(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	for _, writer := range []string{"sync", "webhook", "checkout", "mollie"} {
		t.Run(writer, func(t *testing.T) {
			tenant, tier := uuid.NewString(), uuid.NewString()
			start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
			boundary, end := start.Add(15*24*time.Hour), start.AddDate(0, 1, 0)
			if _, err := db.ExecContext(ctx, `INSERT INTO purser.billing_tiers(id,tier_name,display_name,tier_level,base_price) VALUES($1::text::uuid,$1::text,'Sync contract',1,100)`, tier); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, `INSERT INTO purser.tenant_subscriptions(tenant_id,tier_id,status,billing_model,billing_period_start,billing_period_end,next_billing_date,stripe_current_period_end) VALUES($1,$2,'active','postpaid',$3::timestamp,$4::timestamptz,$4::timestamptz,$4::timestamptz)`, tenant, tier, boundary, end); err != nil {
				t.Fatal(err)
			}

			if _, err := db.ExecContext(ctx, `UPDATE purser.tenant_subscriptions SET stripe_subscription_id=$2 WHERE tenant_id=$1`, tenant, "stripe-period-"+tenant); err != nil {
				t.Fatal(err)
			}
			for _, step := range []struct{ start, end, wantStart, wantEnd time.Time }{
				{start, end, boundary, end},
				{start, end.Add(24 * time.Hour), boundary, end.Add(24 * time.Hour)},
				{end, end.AddDate(0, 1, 0), end, end.AddDate(0, 1, 0)},
				{end, end.AddDate(0, 1, -1), end, end.AddDate(0, 1, -1)},
				{start, end, end, end.AddDate(0, 1, -1)},
			} {
				var err error
				periodStart, periodEnd := sql.NullTime{Time: step.start, Valid: true}, sql.NullTime{Time: step.end, Valid: true}
				switch writer {
				case "sync":
					err = New(db).UpdateTenantStripeSubscriptionSync(ctx, UpdateTenantStripeSubscriptionSyncParams{TenantID: tenant, SubscriptionID: "stripe-period-" + tenant, Status: sql.NullString{String: "active", Valid: true}, PeriodStart: periodStart, PeriodEnd: periodEnd})
				case "webhook":
					_, err = New(db).UpdateTenantStripeSubscriptionStatus(ctx, UpdateTenantStripeSubscriptionStatusParams{TenantID: tenant, SubscriptionID: "stripe-period-" + tenant, StripeStatus: sql.NullString{String: "active", Valid: true}, Status: "active", PeriodStart: periodStart, PeriodEnd: periodEnd})
				case "mollie":
					_, err = New(db).ActivateMollieTenantSubscription(ctx, ActivateMollieTenantSubscriptionParams{TenantID: tenant, TierID: tier, SubscriptionID: sql.NullString{String: "mollie-sub", Valid: true}, PeriodStart: periodStart, PeriodEnd: periodEnd, NextPaymentDate: step.end})
				case "checkout":
					_, err = New(db).ActivateTenantSubscriptionFromStripe(ctx, ActivateTenantSubscriptionFromStripeParams{TenantID: tenant, TierID: tier, PeriodStart: periodStart, PeriodEnd: periodEnd})
				}
				if err != nil {
					t.Fatal(err)
				}
				var gotStart, gotEnd, next, providerEnd time.Time
				if writer == "mollie" {
					var date time.Time
					if readErr := db.QueryRowContext(ctx, `SELECT mollie_next_payment_date FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant).Scan(&date); readErr != nil {
						t.Fatal(readErr)
					}
					if !date.Equal(step.wantEnd) {
						t.Fatalf("Mollie payment date = %s, want %s", date, step.wantEnd)
					}
				}
				if err := db.QueryRowContext(ctx, `SELECT billing_period_start,billing_period_end,next_billing_date,stripe_current_period_end FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant).Scan(&gotStart, &gotEnd, &next, &providerEnd); err != nil {
					t.Fatal(err)
				}
				if !gotStart.Equal(step.wantStart) || !gotEnd.Equal(step.wantEnd) || !next.Equal(step.wantEnd) || (writer != "mollie" && !providerEnd.Equal(step.wantEnd)) {
					t.Fatalf("Stripe sync period=%s..%s next=%s provider=%s, want %s..%s", gotStart, gotEnd, next, providerEnd, step.wantStart, step.wantEnd)
				}
			}
		})
	}
}
