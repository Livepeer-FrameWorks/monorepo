//go:build schema_verify

package grpc

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/appconfig/appconfigtest"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func testEndedProviderAssignmentBilling(t *testing.T, db *sql.DB, server *PurserServer, operatorID string) {
	t.Helper()
	appconfigtest.Set(t, "WAIVE_USAGE_CHARGES", "false")
	ender := &fallbackLifecycleProvider{call: func(context.Context, billing.SubscriptionEndRequest) (billing.SubscriptionEndResult, error) {
		return billing.SubscriptionEndResult{}, nil
	}}
	server = &PurserServer{db: db, logger: server.logger, billing: server.billing, tierReconciler: &assignTierReconciler{}, commodoreClient: &recordingCommodoreCache{}, stripeClient: ender, mollieClient: &fallbackMollieEnder{end: ender}}
	ctx := context.Background()
	exec := func(t *testing.T, query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	paidTier := uuid.NewString()
	exec(t, `INSERT INTO purser.billing_tiers(id,tier_name,display_name,tier_level,base_price,currency,metering_enabled) VALUES($1,'ended-paid','Ended paid',3,79,'EUR',true)`, paidTier)
	exec(t, `INSERT INTO purser.tier_pricing_rules(tier_id,meter,model,currency,included_quantity,unit_price,config) VALUES($1,'egress_gb','all_usage','EUR',0,0.02,'{}')`, paidTier)
	start := time.Now().UTC().Truncate(time.Hour).AddDate(0, -2, 0)
	end := start.AddDate(0, 1, 0)
	exec(t, `INSERT INTO purser.metering_sources(source_id,region,active_from,required) VALUES('ended-provider','',$1,true)`, start)
	exec(t, `INSERT INTO purser.metering_windows(source_id,period_start,period_end,complete) SELECT 'ended-provider',at,at+INTERVAL '5 minutes',true FROM generate_series($1::timestamptz,NOW()+INTERVAL '1 hour',INTERVAL '5 minutes') at`, start)
	testLateRetainedProviderGrantMonths(t, db, server, operatorID)
	for _, provider := range []string{"stripe", "mollie"} {
		for _, action := range []string{"assign", "assign after metering repair", "assign without deadline", "assign under waiver", "repeat waiver", "renew waiver", "expire waiver", "revoke waiver"} {
			t.Run(provider+" ended paid term "+action, func(t *testing.T) {
				tenant := uuid.NewString()
				exec(t, `INSERT INTO purser.tenant_subscriptions(tenant_id,tier_id,status,billing_model,presentment_currency,billing_period_start,billing_period_end,free_fallback_at,free_fallback_reason,free_fallback_notified_at,free_fallback_provider_ended_at,stripe_subscription_id,stripe_subscription_status,mollie_subscription_id) VALUES($1,$2,'pending_cancellation','postpaid','EUR',$3::timestamptz AT TIME ZONE 'UTC',$4::timestamptz AT TIME ZONE 'UTC',$4::timestamptz,'cancelled',NOW(),NOW(),CASE WHEN $5='stripe' THEN 'ended-stripe' END,CASE WHEN $5='stripe' THEN 'canceled' END,CASE WHEN $5='mollie' THEN 'ended-mollie' END)`, tenant, paidTier, start, end, provider)
				for i, at := range []time.Time{start.Add(time.Hour), end.Add(time.Hour)} {
					exec(t, `INSERT INTO purser.usage_records(tenant_id,cluster_id,usage_type,unit,dimensions,dimension_key,source_id,report_id,usage_value,usage_details,period_start,period_end,granularity,value_kind,created_at) VALUES($1,'','egress_gb','gibibyte','{}',$2,'ended-provider',$3,5000,'{}',$4,$4::timestamptz+INTERVAL '5 minutes','minute_5','delta',$4)`, tenant, fmt.Sprintf("%x", sha256.Sum256([]byte("{}"))), strings.Repeat(fmt.Sprintf("%x", i+1), 64), at)
				}
				if strings.HasPrefix(action, "assign") {
					if action == "assign under waiver" {
						exec(t, `INSERT INTO purser.subscription_operator_grants(subscription_id,assigned_tier_id,collection,waive_usage,reason,granted_by,expires_at) SELECT id,tier_id,'invoice',true,'current waiver',$2::uuid,NOW()+INTERVAL '1 day' FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant, operatorID)
						snapshot := func() string {
							t.Helper()
							var value string
							if err := db.QueryRowContext(ctx, `SELECT jsonb_build_object('subscription',to_jsonb(subscription),'grant',(SELECT to_jsonb(grant_row) FROM purser.subscription_operator_grants grant_row WHERE grant_row.subscription_id=subscription.id),'invoices',(SELECT COUNT(*) FROM purser.billing_invoices WHERE tenant_id=$1),'events',(SELECT COUNT(*) FROM purser.domain_event_outbox WHERE tenant_id=$1))::text FROM purser.tenant_subscriptions subscription WHERE tenant_id=$1`, tenant).Scan(&value); err != nil {
								t.Fatal(err)
							}
							return value
						}
						before := snapshot()
						_, err := server.AdminAssignTier(operatorAssignCtx(operatorID), &purserpb.AdminAssignTierRequest{TenantId: tenant, TierName: "ended-paid", Reason: "restore billable service"})
						if status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "end or explicitly reassign") || snapshot() != before {
							t.Fatalf("same-tier abort changed an outstanding grant/cancellation: %v", err)
						}
						return
					}
					if action == "assign without deadline" {
						exec(t, `UPDATE purser.tenant_subscriptions SET free_fallback_at=NULL,free_fallback_reason=NULL WHERE tenant_id=$1`, tenant)
						_, err := server.AdminAssignTier(operatorAssignCtx(operatorID), &purserpb.AdminAssignTierRequest{TenantId: tenant, TierName: "ended-paid", Reason: "restore billable service"})
						if status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "recorded fallback deadline") {
							t.Fatalf("missing retained cutoff allowed partial billing progress: %v", err)
						}
						var untouched bool
						if err := db.QueryRowContext(ctx, `SELECT status='pending_cancellation' AND COALESCE(stripe_subscription_id,mollie_subscription_id) IS NOT NULL AND NOT EXISTS(SELECT 1 FROM purser.billing_invoices WHERE tenant_id=$1) FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant).Scan(&untouched); err != nil || !untouched {
							t.Fatalf("missing cutoff mutated native billing evidence: %v %v", untouched, err)
						}
						return
					}
					if action == "assign after metering repair" {
						exec(t, `UPDATE purser.metering_windows SET complete=false WHERE source_id='ended-provider' AND period_start=$1`, end.Add(10*time.Hour))
						_, err := server.AdminAssignTier(operatorAssignCtx(operatorID), &purserpb.AdminAssignTierRequest{TenantId: tenant, TierName: "ended-paid", Reason: "restore billable service"})
						if err == nil || !strings.Contains(err.Error(), "metering") {
							t.Fatalf("incomplete second month did not hold assignment: %v", err)
						}
						var closed int
						var retained bool
						if err := db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM purser.billing_invoices WHERE tenant_id=$1),status='pending_cancellation' AND free_fallback_at=$2 AND free_fallback_provider_ended_at IS NOT NULL AND COALESCE(stripe_subscription_id,mollie_subscription_id) IS NOT NULL FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant, end).Scan(&closed, &retained); err != nil || closed != 1 || !retained {
							t.Fatalf("monthly progress lost cancellation evidence: closed=%d retained=%v err=%v", closed, retained, err)
						}
						exec(t, `UPDATE purser.metering_windows SET complete=true WHERE source_id='ended-provider' AND period_start=$1`, end.Add(10*time.Hour))
					}
					if _, err := server.AdminAssignTier(operatorAssignCtx(operatorID), &purserpb.AdminAssignTierRequest{TenantId: tenant, TierName: "ended-paid", Reason: "restore billable service"}); err != nil {
						t.Fatal(err)
					}
					var invoices int
					var base, metered string
					if err := db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(base_amount),0)::text,COALESCE(SUM(metered_amount),0)::text FROM purser.billing_invoices WHERE tenant_id=$1`, tenant).Scan(&invoices, &base, &metered); err != nil {
						t.Fatal(err)
					}
					if invoices < 2 || base != "0.00" || metered != "200.00" {
						t.Fatalf("ended native periods were detached before finalization: invoices=%d base=%s usage=%s; want separate months, zero base and 200.00 usage", invoices, base, metered)
					}
					var oldPeriods int
					if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM purser.billing_invoices WHERE tenant_id=$1 AND period_start=$2 AND period_end=$3`, tenant, start, end).Scan(&oldPeriods); err != nil || oldPeriods != 1 {
						t.Fatalf("first monthly allowance was merged: invoices=%d err=%v", oldPeriods, err)
					}
					return
				}
				expires := time.Now().UTC().Add(-time.Hour)
				if action == "repeat waiver" || action == "renew waiver" {
					expires = time.Now().UTC().Truncate(time.Microsecond).Add(24 * time.Hour)
				}
				exec(t, `INSERT INTO purser.subscription_operator_grants(subscription_id,assigned_tier_id,collection,waive_usage,reason,granted_by,granted_at,expires_at) SELECT id,tier_id,'invoice',true,'usage waiver',$2::uuid,$3,$4 FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant, operatorID, start.Add(-time.Hour), expires)
				if action == "repeat waiver" || action == "renew waiver" {
					if action == "renew waiver" {
						expires = expires.Add(24 * time.Hour)
					}
					if _, err := server.AdminSetBillingGrant(operatorAssignCtx(operatorID), &purserpb.AdminSetBillingGrantRequest{TenantId: tenant, TierName: "ended-paid", WaiveUsage: true, Collection: "invoice", Reason: "usage waiver", ExpiresAt: timestamppb.New(expires)}); err != nil {
						t.Fatal(err)
					}
					var unchanged bool
					if err := db.QueryRowContext(ctx, `SELECT status='pending_cancellation' AND free_fallback_at=$2 AND free_fallback_provider_ended_at IS NOT NULL AND COALESCE(stripe_subscription_id,mollie_subscription_id) IS NOT NULL AND NOT EXISTS(SELECT 1 FROM purser.billing_invoices WHERE tenant_id=$1) FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant, end).Scan(&unchanged); err != nil || !unchanged {
						t.Fatalf("repeating/renewing a waiver undid cancellation or closed its period: unchanged=%v err=%v", unchanged, err)
					}
					return
				}
				if err := server.endOperatorGrant(operatorAssignCtx(operatorID), tenant, "waiver ended", action == "expire waiver"); err != nil {
					t.Fatal(err)
				}
				var state string
				var fallback, ended sql.NullTime
				if err := db.QueryRowContext(ctx, `SELECT status,free_fallback_at,free_fallback_provider_ended_at FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant).Scan(&state, &fallback, &ended); err != nil {
					t.Fatal(err)
				}
				if state != "pending_cancellation" || !fallback.Valid || !ended.Valid {
					t.Fatalf("ending waiver reactivated a cancelled customer: state=%s fallback=%v ended=%v", state, fallback, ended)
				}
				for i := 0; i < 4; i++ {
					exec(t, `UPDATE purser.tenant_subscriptions SET free_fallback_attempted_at=NULL WHERE tenant_id=$1`, tenant)
					if err := server.moveTenantToFree(ctx, tenant); err != nil {
						t.Fatal(err)
					}
				}
				var tier string
				var charge string
				if err := db.QueryRowContext(ctx, `SELECT tier.tier_name,subscription.status,COALESCE((SELECT SUM(amount) FROM purser.billing_invoices WHERE tenant_id=$1),0)::text FROM purser.tenant_subscriptions subscription JOIN purser.billing_tiers tier ON tier.id=subscription.tier_id WHERE subscription.tenant_id=$1`, tenant).Scan(&tier, &state, &charge); err != nil {
					t.Fatal(err)
				}
				if tier != "free" || state != "active" || charge != "0.00" {
					t.Fatalf("cancelled tenant did not reach Free without new charges: tier=%s state=%s charges=%s", tier, state, charge)
				}
			})
		}
	}
}
