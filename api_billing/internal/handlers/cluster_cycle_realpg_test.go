//go:build schema_verify

package handlers

import (
	"testing"
	"time"

	"frameworks/api_billing/internal/appconfig/appconfigtest"
	billingpkg "frameworks/api_billing/internal/billing"
	"frameworks/api_billing/internal/database/purserdb"
	"github.com/google/uuid"
)

func TestClusterMonthlyFeeKeepsSplitCycle_RealPG(t *testing.T) {
	appconfigtest.Set(t, "WAIVE_USAGE_CHARGES", "false")
	cases := []struct {
		name                    string
		dates                   []string
		activeFrom, activeUntil string
		want                    []int64
	}{
		{"October model switches", []string{"2026-10-21", "2026-10-25", "2026-10-31", "2026-11-21"}, "2026-10-21", "", []int64{387, 581, 2032}},
		{"activity inside split cycle", []string{"2026-10-21", "2026-10-25", "2026-10-31", "2026-11-21"}, "2026-10-24", "2026-11-10", []int64{97, 580, 968}},
		{"fresh January month", []string{"2027-01-31", "2027-02-10", "2027-02-28"}, "2027-01-31", "", []int64{1071, 1929}},
		{"fresh leap January month", []string{"2028-01-31", "2028-02-10", "2028-02-29"}, "2028-01-31", "", []int64{1034, 1966}},
	}
	parse := func(s string) time.Time {
		t.Helper()
		at, err := time.Parse(time.DateOnly, s)
		if err != nil {
			t.Fatal(err)
		}
		return at
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := startPurserUsageRealPG(t)
			start, end := parse(tc.dates[0]), parse(tc.dates[len(tc.dates)-1])
			tenant := newPhaseTenant(t, db, "postpaid", start, end, 0)
			cluster, owner := "cluster-cycle-"+uuid.NewString(), uuid.NewString()
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := db.Exec(query, args...); err != nil {
					t.Fatal(err)
				}
			}
			exec(`INSERT INTO purser.cluster_pricing_history(cluster_id,pricing_model,base_price,currency,effective_from) VALUES($1,'monthly',30,'EUR','2020-01-01')`, cluster)
			exec(`INSERT INTO purser.platform_fee_policy(id,cluster_kind,cluster_owner_tenant_id,pricing_source,fee_basis_points,effective_from) VALUES($1,'third_party_marketplace',$2,'cluster_monthly',1000,'2020-01-01')`, uuid.NewString(), owner)
			status := "active"
			var until any
			if tc.activeUntil != "" {
				status = "cancelled"
				until = parse(tc.activeUntil)
			}
			exec(`INSERT INTO purser.cluster_subscriptions(tenant_id,cluster_id,status,activated_at,created_at,cancelled_at) VALUES($1,$2,$3,$4,$4,$5)`, tenant.id, cluster, status, parse(tc.activeFrom), until)
			tenant.svc.qmClient = statementQuartermaster{marketplaceClusterResolver{clusterID: cluster, ownerTenantID: owner}}
			subscription, err := purserdb.New(db).GetSubscriptionForPhaseClose(t.Context(), tenant.id)
			if err != nil {
				t.Fatal(err)
			}
			tier, err := billingpkg.LoadSubscriptionEffectiveTier(t.Context(), db, tenant.id)
			if err != nil {
				t.Fatal(err)
			}
			for i, want := range tc.want {
				phase := usagePhase{start: parse(tc.dates[i]), end: parse(tc.dates[i+1]), chainFrom: start, periodEnd: end}
				var result *clusterRatingResult
				if i == 0 {
					run, rateErr := tenant.jobs.ratePostpaidInvoice(t.Context(), purserdb.ListSubscriptionsDueForInvoiceRow(subscription), tier, phase, "", true)
					if rateErr != nil {
						t.Fatal(rateErr)
					}
					result = run.rating
				} else {
					run, rateErr := tenant.jobs.ratePrepaidStatement(t.Context(), tier, tenant.id, "billing@example.com", phase, false, i < len(tc.want)-1)
					if rateErr != nil {
						t.Fatal(rateErr)
					}
					result = run.rating
				}
				var got int64
				for _, line := range result.UsageLines {
					if line.ClusterID != nil && *line.ClusterID == cluster && line.PricingSource == "cluster_monthly" {
						got += line.Amount.Shift(2).IntPart()
					}
				}
				if got != want {
					t.Errorf("phase %s..%s monthly cluster fee=%d cents want %d; retain cycle %s..%s", phase.start, phase.end, got, want, start, end)
				}
			}
		})
	}
}
