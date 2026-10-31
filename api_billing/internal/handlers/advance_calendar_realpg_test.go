//go:build schema_verify

package handlers

import (
	"testing"
	"time"
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
			if err := tenant.svc.activateAdvanceBilledTenant(t.Context(), tenant.id, tenant.supporterID, provider, "customer", at, ""); err != nil {
				t.Fatal(err)
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
