//go:build schema_verify

package grpc

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func testLateRetainedProviderGrantMonths(t *testing.T, db *sql.DB, server *PurserServer, actor string) {
	t.Helper()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	tier := uuid.NewString()
	now := time.Now().UTC()
	start := time.Date(now.Year(), now.Month()-2, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	exec(`INSERT INTO purser.billing_tiers(id,tier_name,display_name,tier_level,base_price,currency,metering_enabled) VALUES($1,'retained-allowance','Retained monthly allowance',3,79,'EUR',true)`, tier)
	exec(`INSERT INTO purser.tier_pricing_rules(tier_id,meter,model,currency,included_quantity,unit_price,config) VALUES($1,'egress_gb','tiered_graduated','EUR',1000,0.02,'{}')`, tier)
	exec(`INSERT INTO purser.metering_sources(source_id,region,active_from,required) VALUES('retained-grant-months','',$1,true)`, start)
	exec(`INSERT INTO purser.metering_windows(source_id,period_start,period_end,complete) SELECT 'retained-grant-months',at,at+INTERVAL '5 minutes',true FROM generate_series($1::timestamptz,NOW()+INTERVAL '1 hour',INTERVAL '5 minutes') at`, start)
	testRetainedGrantAnniversaryFence(t, db, server, actor, tier, start, end)
	for _, provider := range []string{"stripe", "mollie"} {
		for _, change := range []string{"change", "end"} {
			t.Run(provider+" late grant "+change+" preserves monthly allowances", func(t *testing.T) {
				tenant := uuid.NewString()
				exec(`INSERT INTO purser.tenant_subscriptions(tenant_id,tier_id,status,billing_model,presentment_currency,billing_period_start,billing_period_end,free_fallback_at,free_fallback_reason,free_fallback_notified_at,free_fallback_provider_ended_at,stripe_subscription_id,stripe_subscription_status,mollie_subscription_id) VALUES($1,$2,'pending_cancellation','postpaid','EUR',$3::timestamptz AT TIME ZONE 'UTC',$4::timestamptz AT TIME ZONE 'UTC',$4::timestamptz,'cancelled',NOW(),NOW(),CASE WHEN $5='stripe' THEN 'ended-allowance-stripe' END,CASE WHEN $5='stripe' THEN 'canceled' END,CASE WHEN $5='mollie' THEN 'ended-allowance-mollie' END)`, tenant, tier, start, end, provider)
				exec(`INSERT INTO purser.subscription_operator_grants(subscription_id,assigned_tier_id,collection,waive_usage,reason,granted_by,granted_at,expires_at) SELECT id,tier_id,'invoice',false,'invoice collection',$2::uuid,$3,NOW()+INTERVAL '1 day' FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant, actor, start.Add(-time.Hour))
				for i, at := range []time.Time{start.Add(time.Hour), end.Add(time.Hour)} {
					exec(`INSERT INTO purser.usage_records(tenant_id,cluster_id,usage_type,unit,dimensions,dimension_key,source_id,report_id,usage_value,usage_details,period_start,period_end,granularity,value_kind,created_at) VALUES($1,'','egress_gb','gibibyte','{}',$2,'retained-grant-months',$3,1500,'{}',$4,$4::timestamptz+INTERVAL '5 minutes','minute_5','delta',$4)`, tenant, fmt.Sprintf("%x", sha256.Sum256([]byte("{}"))), strings.Repeat(fmt.Sprintf("%x", i+1), 64), at)
				}
				var err error
				if change == "change" {
					_, err = server.AdminSetBillingGrant(operatorAssignCtx(actor), &purserpb.AdminSetBillingGrantRequest{TenantId: tenant, TierName: "retained-allowance", Collection: "invoice", WaiveUsage: true, Reason: "usage waiver starts now", ExpiresAt: timestamppb.New(time.Now().Add(48 * time.Hour))})
				} else {
					err = server.endOperatorGrant(operatorAssignCtx(actor), tenant, "invoice arrangement ends", false)
				}
				if err != nil {
					t.Fatal(err)
				}
				var count, fullMonths int
				var base, usage string
				if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*),COUNT(*) FILTER(WHERE (period_start=$2 AND period_end=$3) OR (period_start=$3 AND period_end=$4)),COALESCE(SUM(base_amount),0)::text,COALESCE(SUM(metered_amount),0)::text FROM purser.billing_invoices WHERE tenant_id=$1`, tenant, start, end, end.AddDate(0, 1, 0)).Scan(&count, &fullMonths, &base, &usage); err != nil {
					t.Fatal(err)
				}
				if count < 2 || fullMonths != 2 || base != "0.00" || usage != "20.00" {
					t.Fatalf("grant boundary merged monthly allowances: invoices=%d full months=%d base=%s usage=%s; want two full months, zero provider base, and 20.00 usage", count, fullMonths, base, usage)
				}
				var retained bool
				if err := db.QueryRowContext(t.Context(), `SELECT status='pending_cancellation' AND free_fallback_at=$2 AND free_fallback_provider_ended_at IS NOT NULL AND COALESCE(stripe_subscription_id,mollie_subscription_id) IS NOT NULL FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant, end).Scan(&retained); err != nil || !retained {
					t.Fatalf("grant pricing boundary undid cancellation: retained=%v err=%v", retained, err)
				}
			})
		}
	}
}

func testRetainedGrantAnniversaryFence(t *testing.T, db *sql.DB, server *PurserServer, actor, tier string, start, end time.Time) {
	t.Helper()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, provider := range []string{"stripe", "mollie"} {
		for _, anniversary := range []bool{true, false} {
			name, cutoff := "anniversary", end
			if !anniversary {
				name, cutoff = "midmonth", start.Add(14*24*time.Hour)
			}
			t.Run(provider+" retained grant "+name+" allowance fence", func(t *testing.T) {
				tenant := uuid.NewString()
				exec(`INSERT INTO purser.tenant_subscriptions(tenant_id,tier_id,status,billing_model,presentment_currency,billing_period_start,billing_period_end,free_fallback_at,free_fallback_reason,free_fallback_notified_at,free_fallback_provider_ended_at,stripe_subscription_id,stripe_subscription_status,mollie_subscription_id) VALUES($1,$2,'pending_cancellation','postpaid','EUR',$3::timestamptz AT TIME ZONE 'UTC',$4::timestamptz AT TIME ZONE 'UTC',$4::timestamptz,'cancelled',NOW(),NOW(),CASE WHEN $5='stripe' THEN 'ended-calendar-stripe' END,CASE WHEN $5='stripe' THEN 'canceled' END,CASE WHEN $5='mollie' THEN 'ended-calendar-mollie' END)`, tenant, tier, start, end, provider)
				exec(`INSERT INTO purser.subscription_operator_grants(subscription_id,assigned_tier_id,collection,waive_usage,reason,granted_by,granted_at,expires_at) SELECT id,tier_id,'invoice',false,'calendar arrangement',$2::uuid,$3,$4 FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant, actor, start.Add(-time.Hour), cutoff)
				first, second := 1000, 1000
				secondAt, secondReceived := start.Add(20*24*time.Hour), start.Add(20*24*time.Hour)
				if !anniversary {
					second, secondAt = 2000, start.Add(2*time.Hour)
				}
				for i, usage := range []struct {
					at, received time.Time
					quantity     int
				}{{start.Add(time.Hour), start.Add(time.Hour), first}, {secondAt, secondReceived, second}, {end.Add(time.Hour), end.Add(time.Hour), 1500}} {
					exec(`INSERT INTO purser.usage_records(tenant_id,cluster_id,usage_type,unit,dimensions,dimension_key,source_id,report_id,usage_value,usage_details,period_start,period_end,granularity,value_kind,created_at) VALUES($1,'','egress_gb','gibibyte','{}',$2,'retained-grant-months',$3,$4,'{}',$5,$5::timestamptz+INTERVAL '5 minutes','minute_5','delta',$6)`, tenant, fmt.Sprintf("%x", sha256.Sum256([]byte("{}"))), strings.Repeat(fmt.Sprintf("%x", i+1), 64), usage.quantity, usage.at, usage.received)
				}
				if err := server.endOperatorGrant(operatorAssignCtx(actor), tenant, "calendar arrangement expired", true); err != nil {
					t.Fatal(err)
				}
				var split, retained bool
				if err := db.QueryRowContext(t.Context(), `SELECT COALESCE((usage_details->>'closes_postpaid_phase')::boolean,false) FROM purser.billing_invoices WHERE tenant_id=$1 AND period_start=$2 AND period_end=$3`, tenant, start, cutoff).Scan(&split); err != nil {
					t.Fatal(err)
				}
				if err := db.QueryRowContext(t.Context(), `SELECT status='pending_cancellation' AND free_fallback_at=$2 AND free_fallback_provider_ended_at IS NOT NULL AND COALESCE(stripe_subscription_id,mollie_subscription_id) IS NOT NULL FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant, end).Scan(&retained); err != nil || !retained {
					t.Fatalf("calendar grant ending discarded cancellation: %v %v", retained, err)
				}
				if _, err := server.AdminAssignTier(operatorAssignCtx(actor), &purserpb.AdminAssignTierRequest{TenantId: tenant, TierName: "retained-allowance", Reason: "explicitly restore invoicing"}); err != nil {
					t.Fatal(err)
				}
				var base, metered string
				if err := db.QueryRowContext(t.Context(), `SELECT SUM(base_amount)::text,SUM(metered_amount)::text FROM purser.billing_invoices WHERE tenant_id=$1`, tenant).Scan(&base, &metered); err != nil {
					t.Fatal(err)
				}
				if split == anniversary || base != "0.00" || metered != "30.00" {
					t.Fatalf("calendar allowance fence wrong: anniversary=%v split=%v base=%s usage=%s; want split=%v, zero native base and 30.00 usage", anniversary, split, base, metered, !anniversary)
				}
			})
		}
	}
}
