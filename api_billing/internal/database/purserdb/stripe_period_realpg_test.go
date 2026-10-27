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
	tenant, tier := uuid.NewString(), uuid.NewString()
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	boundary, end := start.Add(15*24*time.Hour), start.AddDate(0, 1, 0)
	if _, err := db.ExecContext(ctx, `INSERT INTO purser.billing_tiers(id,tier_name,display_name,tier_level,base_price) VALUES($1::text::uuid,$1::text,'Sync contract',1,100)`, tier); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO purser.tenant_subscriptions(tenant_id,tier_id,status,billing_model,billing_period_start,billing_period_end,next_billing_date,stripe_current_period_end) VALUES($1,$2,'active','postpaid',$3::timestamp,$4::timestamptz,$4::timestamptz,$4::timestamptz)`, tenant, tier, boundary, end); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct{ start, end, wantStart, wantEnd time.Time }{
		{start, end, boundary, end},
		{end, end.AddDate(0, 1, 0), end, end.AddDate(0, 1, 0)},
		{start, end, end, end.AddDate(0, 1, 0)},
	} {
		if err := New(db).UpdateTenantStripeSubscriptionSync(ctx, UpdateTenantStripeSubscriptionSyncParams{TenantID: tenant, Status: sql.NullString{String: "active", Valid: true}, PeriodStart: sql.NullTime{Time: step.start, Valid: true}, PeriodEnd: sql.NullTime{Time: step.end, Valid: true}}); err != nil {
			t.Fatal(err)
		}
		var gotStart, gotEnd, next, providerEnd time.Time
		if err := db.QueryRowContext(ctx, `SELECT billing_period_start,billing_period_end,next_billing_date,stripe_current_period_end FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant).Scan(&gotStart, &gotEnd, &next, &providerEnd); err != nil {
			t.Fatal(err)
		}
		if !gotStart.Equal(step.wantStart) || !gotEnd.Equal(step.wantEnd) || !next.Equal(step.wantEnd) || !providerEnd.Equal(step.wantEnd) {
			t.Fatalf("Stripe sync period=%s..%s next=%s provider=%s, want %s..%s", gotStart, gotEnd, next, providerEnd, step.wantStart, step.wantEnd)
		}
	}
}
