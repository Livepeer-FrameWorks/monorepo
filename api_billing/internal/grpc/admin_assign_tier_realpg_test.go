//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type assignTierReconcile struct {
	level int32
	name  string
}

type assignTierReconciler struct {
	calls []assignTierReconcile
}

func (r *assignTierReconciler) OfficialClusterIDs(context.Context) (map[string]bool, error) {
	return map[string]bool{}, nil
}

func (r *assignTierReconciler) Reconcile(_ context.Context, _ string, level int32, name string) ([]string, string, error) {
	r.calls = append(r.calls, assignTierReconcile{level: level, name: name})
	return []string{"cluster-" + name}, "cluster-" + name, nil
}

func (r *assignTierReconciler) RevokeDNSEntitlements(context.Context, string) error { return nil }

func operatorAssignCtx(userID string) context.Context {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "jwt")
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, userID)
	return context.WithValue(ctx, ctxkeys.KeyPlatformOperator, true)
}

func TestAdminAssignTier_RealPG(t *testing.T) { //nolint:funlen // One database carries the whole assignment matrix.
	db := startOperatorGrantDatabase(t)
	ctx := context.Background()
	tiers := map[string]string{}
	for _, tier := range []struct {
		name            string
		level           int
		defaultPrepaid  bool
		defaultPostpaid bool
		active          bool
	}{
		{"payg", 0, true, false, true},
		{"free", 1, false, true, true},
		{"production", 4, false, false, true},
		{"retired", 3, false, false, false},
	} {
		id := uuid.NewString()
		tiers[tier.name] = id
		if _, err := db.ExecContext(ctx, `
			INSERT INTO purser.billing_tiers (
				id, tier_name, display_name, tier_level, is_default_prepaid, is_default_postpaid, is_active
			) VALUES ($1,$2,$2,$3,$4,$5,$6)
		`, id, tier.name, tier.level, tier.defaultPrepaid, tier.defaultPostpaid, tier.active); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.tier_entitlements (tier_id, key, value)
		VALUES ($1, 'custom_subdomain_enabled', 'true'::jsonb)
	`, tiers["production"]); err != nil {
		t.Fatal(err)
	}

	reconciler := &assignTierReconciler{}
	cache := &recordingCommodoreCache{}
	server := &PurserServer{db: db, logger: logging.NewLogger(), tierReconciler: reconciler, commodoreClient: cache}
	const operatorID = "7a000000-0000-4000-8000-000000000001"

	seed := func(t *testing.T, tier, model, subStatus string) string {
		t.Helper()
		tenantID := uuid.NewString()
		if _, err := db.ExecContext(ctx, `
			INSERT INTO purser.tenant_subscriptions (id, tenant_id, tier_id, billing_model, status)
			VALUES ($1,$2,$3,$4,$5)
		`, uuid.NewString(), tenantID, tiers[tier], model, subStatus); err != nil {
			t.Fatal(err)
		}
		return tenantID
	}
	subscription := func(t *testing.T, tenantID string) (tierID, model, subStatus string, pending bool) {
		t.Helper()
		if err := db.QueryRowContext(ctx, `
			SELECT tier_id::text, billing_model, status, pending_tier_id IS NOT NULL
			FROM purser.tenant_subscriptions WHERE tenant_id = $1
		`, tenantID).Scan(&tierID, &model, &subStatus, &pending); err != nil {
			t.Fatal(err)
		}
		return tierID, model, subStatus, pending
	}
	for _, before := range []string{"past_due", "pending_cancellation"} {
		t.Run("operator assignment restores invoicing from "+before, func(t *testing.T) {
			tenant := seed(t, "production", "postpaid", before)
			if _, err := db.ExecContext(ctx, `UPDATE purser.tenant_subscriptions SET billing_period_start=NOW()-INTERVAL '1 month',billing_period_end=NOW()-INTERVAL '1 hour',free_fallback_at=NOW()+INTERVAL '7 days',free_fallback_reason='payment_failed' WHERE tenant_id=$1`, tenant); err != nil {
				t.Fatal(err)
			}
			if before == "pending_cancellation" {
				if _, err := db.ExecContext(ctx, `UPDATE purser.tenant_subscriptions SET free_fallback_at=NOW()-INTERVAL '1 hour',free_fallback_reason='cancelled',stripe_subscription_id='ended-subscription',stripe_subscription_status='canceled',stripe_current_period_end=NOW()-INTERVAL '1 hour',mollie_subscription_id='ended-mollie',mollie_next_payment_date=CURRENT_DATE,free_fallback_provider_ended_at=NOW() WHERE tenant_id=$1`, tenant); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := server.AdminAssignTier(operatorAssignCtx(operatorID), &purserpb.AdminAssignTierRequest{TenantId: tenant, TierName: "production", Reason: "operator corrected the billing arrangement"}); err != nil {
				t.Fatal(err)
			}
			var scheduled, ended sql.NullTime
			var state string
			var stripe, mollie sql.NullString
			if err := db.QueryRowContext(ctx, `SELECT status,free_fallback_at,free_fallback_provider_ended_at,stripe_subscription_id,mollie_subscription_id FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant).Scan(&state, &scheduled, &ended, &stripe, &mollie); err != nil {
				t.Fatal(err)
			}
			if state != "active" || scheduled.Valid || ended.Valid || stripe.Valid || mollie.Valid {
				t.Fatalf("abort left unbillable subscription: status=%s scheduled=%v ended=%v stripe=%v mollie=%v", state, scheduled, ended, stripe, mollie)
			}
			due, err := purserdb.New(db).ListSubscriptionsDueForInvoice(ctx, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, row := range due {
				if row.TenantID == tenant {
					found = true
				}
			}
			if !found {
				t.Fatal("aborted fallback is absent from the production invoice job")
			}
		})
	}
	for _, provider := range []string{"stripe", "mollie"} {
		for _, action := range []string{"set-tier", "grant set", "grant revoke"} {
			t.Run("open ended-provider paid term refuses "+provider+" "+action, func(t *testing.T) {
				tenant := seed(t, "production", "postpaid", "pending_cancellation")
				if _, err := db.ExecContext(ctx, `UPDATE purser.tenant_subscriptions SET presentment_currency='EUR' WHERE tenant_id=$1`, tenant); err != nil {
					t.Fatal(err)
				}
				if _, err := db.ExecContext(ctx, `UPDATE purser.tenant_subscriptions SET billing_period_start=NOW()-INTERVAL '10 days',billing_period_end=CASE WHEN $2='mollie' THEN NOW()-INTERVAL '1 hour' ELSE NOW()+INTERVAL '20 days' END,free_fallback_at=NOW()+INTERVAL '20 days',free_fallback_reason='cancelled',free_fallback_provider_ended_at=NOW(),stripe_subscription_id=CASE WHEN $2='stripe' THEN 'ended-stripe' END,stripe_customer_id=CASE WHEN $2='stripe' THEN 'customer' END,stripe_subscription_status=CASE WHEN $2='stripe' THEN 'canceled' END,mollie_subscription_id=CASE WHEN $2='mollie' THEN 'ended-mollie' END WHERE tenant_id=$1`, tenant, provider); err != nil {
					t.Fatal(err)
				}
				if action == "grant revoke" {
					if _, err := db.ExecContext(ctx, `INSERT INTO purser.subscription_operator_grants(subscription_id,assigned_tier_id,collection,waive_usage,reason,granted_by) SELECT id,tier_id,'invoice',true,'existing arrangement',$2::uuid FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant, operatorID); err != nil {
						t.Fatal(err)
					}
				}
				snapshot := func() string {
					t.Helper()
					var value string
					if err := db.QueryRowContext(ctx, `SELECT jsonb_build_object('subscription',to_jsonb(subscription),'grant',(SELECT to_jsonb(grant_row) FROM purser.subscription_operator_grants grant_row WHERE grant_row.subscription_id=subscription.id),'history',(SELECT COUNT(*) FROM purser.subscription_operator_grant_history WHERE tenant_id=subscription.tenant_id),'invoices',(SELECT COUNT(*) FROM purser.billing_invoices WHERE tenant_id=subscription.tenant_id),'events',(SELECT COUNT(*) FROM purser.domain_event_outbox WHERE tenant_id=subscription.tenant_id))::text FROM purser.tenant_subscriptions subscription WHERE tenant_id=$1`, tenant).Scan(&value); err != nil {
						t.Fatal(err)
					}
					return value
				}
				before := snapshot()
				var err error
				switch action {
				case "set-tier":
					_, err = server.AdminAssignTier(operatorAssignCtx(operatorID), &purserpb.AdminAssignTierRequest{TenantId: tenant, TierName: "production", Reason: "retain paid service"})
				case "grant set":
					_, err = server.AdminSetBillingGrant(operatorAssignCtx(operatorID), &purserpb.AdminSetBillingGrantRequest{TenantId: tenant, TierName: "production", WaiveUsage: true, Collection: "invoice", Reason: "retain paid service"})
				case "grant revoke":
					_, err = server.AdminRevokeBillingGrant(operatorAssignCtx(operatorID), &purserpb.AdminRevokeBillingGrantRequest{TenantId: tenant, TierName: "production", Reason: "retain paid service"})
				}
				if status.Code(err) != codes.FailedPrecondition {
					t.Fatalf("open paid term lost fee evidence through %s: %v", action, err)
				}
				if !strings.Contains(err.Error(), "paid term") {
					t.Fatalf("paid term refusal was not actionable: %v", err)
				}
				if after := snapshot(); after != before {
					t.Fatalf("refused %s changed subscription/grant/invoice/event state: before=%s after=%s", action, before, after)
				}
			})
		}
	}
	t.Run("automatic same-tier waiver expiry preserves the native paid term", func(t *testing.T) {
		tenant := seed(t, "production", "postpaid", "pending_cancellation")
		if _, err := db.ExecContext(ctx, `UPDATE purser.tenant_subscriptions SET presentment_currency='EUR' WHERE tenant_id=$1`, tenant); err != nil {
			t.Fatal(err)
		}
		expires := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
		if _, err := db.ExecContext(ctx, `UPDATE purser.tenant_subscriptions SET billing_period_start=NOW()-INTERVAL '10 days',billing_period_end=NOW()+INTERVAL '20 days',free_fallback_at=NOW()+INTERVAL '20 days',free_fallback_reason='cancelled',free_fallback_provider_ended_at=NOW(),stripe_subscription_id='retained-paid',stripe_customer_id='customer',stripe_subscription_status='canceled' WHERE tenant_id=$1`, tenant); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO purser.subscription_operator_grants(subscription_id,assigned_tier_id,collection,waive_usage,reason,granted_by,granted_at,expires_at) SELECT id,tier_id,'invoice',true,'expiring usage waiver',$2::uuid,NOW()-INTERVAL '2 days',$3 FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant, operatorID, expires); err != nil {
			t.Fatal(err)
		}
		ownership := func() string {
			t.Helper()
			var value string
			if err := db.QueryRowContext(ctx, `SELECT jsonb_build_object('tier',tier_id,'status',status,'stripe',stripe_subscription_id,'mollie',mollie_subscription_id,'ended',free_fallback_provider_ended_at,'fallback',free_fallback_at,'reason',free_fallback_reason)::text FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant).Scan(&value); err != nil {
				t.Fatal(err)
			}
			return value
		}
		before := ownership()
		if err := server.endOperatorGrant(operatorAssignCtx(operatorID), tenant, "grant expired", true); err != nil {
			t.Fatalf("paid term deferred an expired usage waiver: %v", err)
		}
		if _, err := purserdb.New(db).GetSubscriptionOperatorGrant(ctx, tenant); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("expired waiver still applies: %v", err)
		}
		var ended time.Time
		if err := db.QueryRowContext(ctx, `SELECT ended_at FROM purser.subscription_operator_grant_history WHERE tenant_id=$1`, tenant).Scan(&ended); err != nil || !ended.Equal(expires) {
			t.Fatalf("waiver did not end at its deadline: %v %v", ended, err)
		}
		if after := ownership(); after != before {
			t.Fatalf("waiver expiry discarded paid native ownership: before=%s after=%s", before, after)
		}
	})
	t.Run("a live native subscription cannot lose its fallback obligation", func(t *testing.T) {
		tenant := seed(t, "production", "postpaid", "past_due")
		if _, err := db.ExecContext(ctx, `UPDATE purser.tenant_subscriptions SET stripe_subscription_id='unpaid-but-live',free_fallback_at=NOW()+INTERVAL '7 days',free_fallback_reason='payment_failed' WHERE tenant_id=$1`, tenant); err != nil {
			t.Fatal(err)
		}
		_, err := server.AdminAssignTier(operatorAssignCtx(operatorID), &purserpb.AdminAssignTierRequest{TenantId: tenant, TierName: "production", Reason: "operator tries to abort collection"})
		if status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("live collector lost its fallback: %v", err)
		}
		var scheduled sql.NullTime
		if err := db.QueryRowContext(ctx, `SELECT free_fallback_at FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant).Scan(&scheduled); err != nil || !scheduled.Valid {
			t.Fatalf("live collector lost durable shutdown: %v %v", scheduled, err)
		}
	})
	for _, target := range []string{"free", "production"} {
		t.Run("provider stop refuses assignment to "+target, func(t *testing.T) {
			tenant := seed(t, "free", "postpaid", "active")
			if _, err := db.ExecContext(ctx, `UPDATE purser.tenant_subscriptions SET free_fallback_at=NOW(),free_fallback_reason='payment_failed',free_fallback_provider_stopping=TRUE WHERE tenant_id=$1`, tenant); err != nil {
				t.Fatal(err)
			}
			_, err := server.AdminAssignTier(operatorAssignCtx(operatorID), &purserpb.AdminAssignTierRequest{TenantId: tenant, TierName: target, Reason: "operator retries assignment"})
			if status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("in-flight provider stop did not return a precondition: %v", err)
			}
		})
	}
	for _, phase := range []string{"requested", "pricing_applied"} {
		t.Run("operator assignment preserves upgrade "+phase, func(t *testing.T) {
			tenant := seed(t, "production", "postpaid", "active")
			if _, err := db.ExecContext(ctx, `INSERT INTO purser.subscription_tier_upgrades(id,tenant_id,old_tier_id,tier_id,actor_id,old_period_start,old_period_end,requested_at,provider,collection_provider,customer_id,provider_subscription_id,stripe_price_id,old_base_cents,base_cents,state)
VALUES($1,$2,$3,$4,'customer',NOW()-INTERVAL '1 day',NOW()+INTERVAL '29 days',NOW(),'invoice','invoice','','','',0,2000,$5)`, uuid.NewString(), tenant, tiers["free"], tiers["production"], phase); err != nil {
				t.Fatal(err)
			}
			if phase == "requested" {
				if _, err := db.ExecContext(ctx, `UPDATE purser.tenant_subscriptions SET pending_reason='tier_upgrade',pending_tier_id=tier_id WHERE tenant_id=$1`, tenant); err != nil {
					t.Fatal(err)
				}
			}
			for _, target := range []string{"production", "free"} {
				_, err := server.AdminAssignTier(operatorAssignCtx(operatorID), &purserpb.AdminAssignTierRequest{TenantId: tenant, TierName: target, Reason: "operator correction during collection"})
				if status.Code(err) != codes.FailedPrecondition {
					t.Fatalf("%s upgrade was repriced by %s: %v", phase, target, err)
				}
			}
		})
	}
	updatedEvent := func(t *testing.T, tenantID string) (*internalv1.SubscriptionUpdated, string) {
		t.Helper()
		var payload []byte
		var actor sql.NullString
		if err := db.QueryRowContext(ctx, `
			SELECT payload, actor_user_id::text FROM purser.domain_event_outbox
			WHERE tenant_id = $1::uuid AND event_type = 'billing.subscription_updated'
		`, tenantID).Scan(&payload, &actor); err != nil {
			t.Fatalf("load subscription_updated event: %v", err)
		}
		var ev internalv1.SubscriptionUpdated
		if err := proto.Unmarshal(payload, &ev); err != nil {
			t.Fatal(err)
		}
		return &ev, actor.String
	}

	t.Run("postpaid paid tier without payment collection", func(t *testing.T) {
		reconciler.calls = nil
		tenantID := seed(t, "free", "postpaid", "active")
		if _, err := db.ExecContext(ctx, `DELETE FROM purser.media_authority_refresh_outbox WHERE tenant_id = $1`, tenantID); err != nil {
			t.Fatal(err)
		}
		resp, err := server.AdminAssignTier(operatorAssignCtx(operatorID), &purserpb.AdminAssignTierRequest{
			TenantId: tenantID, TierName: "production", Reason: "staging storage test",
		})
		if err != nil {
			t.Fatalf("AdminAssignTier: %v", err)
		}
		if !resp.GetChanged() || resp.GetTierName() != "production" || resp.GetBillingModel() != "postpaid" ||
			resp.GetPreviousTierName() != "free" || resp.GetPreviousBillingModel() != "postpaid" || resp.GetTierLevel() != 4 {
			t.Fatalf("response = %+v", resp)
		}
		if tierID, model, _, _ := subscription(t, tenantID); tierID != tiers["production"] || model != "postpaid" {
			t.Fatalf("subscription tier/model = %s/%s", tierID, model)
		}
		if len(reconciler.calls) != 1 || reconciler.calls[0] != (assignTierReconcile{level: 4, name: "production"}) {
			t.Fatalf("cluster access reconciled with %+v, want production level 4", reconciler.calls)
		}
		if resp.GetPrimaryClusterId() != "cluster-production" || cache.tenantID != tenantID || cache.reason != "tier_changed" {
			t.Fatalf("primary %q, cache invalidation %q/%q", resp.GetPrimaryClusterId(), cache.tenantID, cache.reason)
		}
		var subdomain bool
		if err := db.QueryRowContext(ctx, `
			SELECT (e.value)::text::boolean FROM purser.tenant_subscriptions s
			JOIN purser.tier_entitlements e ON e.tier_id = s.tier_id AND e.key = 'custom_subdomain_enabled'
			WHERE s.tenant_id = $1
		`, tenantID).Scan(&subdomain); err != nil || !subdomain {
			t.Fatalf("production entitlement after assignment = %v, err %v", subdomain, err)
		}
		if n := purserCount(t, db, `SELECT COUNT(*) FROM purser.media_authority_refresh_outbox WHERE tenant_id = $1::uuid`, tenantID); n != 1 {
			t.Fatalf("media authority refresh obligations = %d, want 1", n)
		}
		ev, actor := updatedEvent(t, tenantID)
		if ev.GetReason() != "staging storage test" || ev.GetTierId() != tiers["production"] || actor != operatorID {
			t.Fatalf("event = %+v actor %q", ev, actor)
		}
		if n := purserCount(t, db, `SELECT COUNT(*) FROM purser.billing_event_outbox WHERE tenant_id = $1::uuid AND event_type = 'subscription_updated' AND user_id = $2`, tenantID, operatorID); n != 1 {
			t.Fatalf("legacy subscription_updated rows naming the operator = %d, want 1", n)
		}
	})

	t.Run("prepaid tenant moves to postpaid and keeps its balance", func(t *testing.T) {
		tenantID := seed(t, "payg", "prepaid", "suspended")
		if _, err := db.ExecContext(ctx, `INSERT INTO purser.prepaid_balances (tenant_id, balance_cents, currency) VALUES ($1, 2500, 'EUR')`, tenantID); err != nil {
			t.Fatal(err)
		}
		resp, err := server.AdminAssignTier(operatorAssignCtx(operatorID), &purserpb.AdminAssignTierRequest{
			TenantId: tenantID, TierName: "production", Reason: "comped partner",
		})
		if err != nil {
			t.Fatalf("AdminAssignTier: %v", err)
		}
		tierID, model, subStatus, _ := subscription(t, tenantID)
		if tierID != tiers["production"] || model != "postpaid" || subStatus != "active" || resp.GetPreviousBillingModel() != "prepaid" {
			t.Fatalf("tier/model/status = %s/%s/%s, response %+v", tierID, model, subStatus, resp)
		}
		if n := purserCount(t, db, `SELECT balance_cents FROM purser.prepaid_balances WHERE tenant_id = $1::uuid`, tenantID); n != 2500 {
			t.Fatalf("balance = %d, want 2500 carried as credit", n)
		}
		ev, _ := updatedEvent(t, tenantID)
		if got := ev.GetChangedFields(); len(got) != 3 || got[0] != "tier_id" || got[1] != "billing_model" || got[2] != "status" {
			t.Fatalf("changed fields = %v", got)
		}
	})

	t.Run("postpaid tenant moves to payg prepaid and gets a balance", func(t *testing.T) {
		tenantID := seed(t, "production", "postpaid", "active")
		// The switch finalizes the postpaid phase's invoice, presented in EUR.
		if _, err := db.ExecContext(ctx, `UPDATE purser.tenant_subscriptions SET presentment_currency = 'EUR', pending_tier_id = $2, pending_effective_at = NOW() + INTERVAL '1 day', pending_reason = 'downgrade' WHERE tenant_id = $1`, tenantID, tiers["free"]); err != nil {
			t.Fatal(err)
		}
		resp, err := server.AdminAssignTier(operatorAssignCtx(operatorID), &purserpb.AdminAssignTierRequest{
			TenantId: tenantID, TierName: "payg", BillingModel: "prepaid", Reason: "back to pay as you go",
		})
		if err != nil {
			t.Fatalf("AdminAssignTier: %v", err)
		}
		tierID, model, _, pending := subscription(t, tenantID)
		if tierID != tiers["payg"] || model != "prepaid" || pending || resp.GetBillingModel() != "prepaid" {
			t.Fatalf("tier/model/pending = %s/%s/%v", tierID, model, pending)
		}
		if n := purserCount(t, db, `SELECT COUNT(*) FROM purser.prepaid_balances WHERE tenant_id = $1::uuid AND currency = 'EUR'`, tenantID); n != 1 {
			t.Fatalf("prepaid balance rows = %d, want 1", n)
		}
	})

	t.Run("moving back to prepaid returns the credit an open draft holds", func(t *testing.T) {
		tenantID := seed(t, "production", "postpaid", "active")
		periodStart := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
		// The tenant's 10.00 balance went to its September draft as credit.
		for _, statement := range []string{
			`UPDATE purser.tenant_subscriptions SET presentment_currency = 'EUR' WHERE tenant_id = $1`,
			`INSERT INTO purser.prepaid_balances (tenant_id, balance_cents, currency) VALUES ($1, 0, 'EUR')`,
			`INSERT INTO purser.balance_transactions (tenant_id, amount_cents, balance_after_cents, transaction_type, description, reference_id, reference_type)
				VALUES ($1, -1000, 0, 'credit', 'Invoice credit: 2026-09', gen_random_uuid(), 'invoice_credit')`,
			`INSERT INTO purser.billing_invoices (tenant_id, amount, currency, status, due_date, base_amount, metered_amount, prepaid_credit_applied, period_start, period_end)
				VALUES ($1, 69.00, 'EUR', 'draft', $2::timestamptz + INTERVAL '1 month 14 days', 79.00, 0, 10.00, $2, $2::timestamptz + INTERVAL '1 month')`,
		} {
			args := []any{tenantID}
			if strings.Contains(statement, "billing_invoices") {
				args = append(args, periodStart)
			}
			if _, err := db.ExecContext(ctx, statement, args...); err != nil {
				t.Fatalf("seed: %v\n%s", err, statement)
			}
		}
		if _, err := server.AdminAssignTier(operatorAssignCtx(operatorID), &purserpb.AdminAssignTierRequest{
			TenantId: tenantID, TierName: "payg", Reason: "back to pay as you go",
		}); err != nil {
			t.Fatalf("AdminAssignTier: %v", err)
		}
		if n := purserCount(t, db, `SELECT balance_cents FROM purser.prepaid_balances WHERE tenant_id = $1::uuid`, tenantID); n != 1000 {
			t.Fatalf("balance = %d, want the 1000 cents the draft held", n)
		}
		var amount, credit string
		if err := db.QueryRowContext(ctx, `SELECT amount::text, prepaid_credit_applied::text FROM purser.billing_invoices WHERE tenant_id = $1::uuid AND period_start = $2`, tenantID, periodStart).Scan(&amount, &credit); err != nil {
			t.Fatal(err)
		}
		if amount != "79.00" || credit != "0.00" {
			t.Fatalf("draft amount %s credit %s, want 79.00 with no credit", amount, credit)
		}
	})

	t.Run("tenant without a subscription gets one on the assigned tier", func(t *testing.T) {
		tenantID := uuid.NewString()
		resp, err := server.AdminAssignTier(operatorAssignCtx(operatorID), &purserpb.AdminAssignTierRequest{
			TenantId: tenantID, TierName: "production", Reason: "comped account",
		})
		if err != nil {
			t.Fatalf("AdminAssignTier: %v", err)
		}
		if tierID, model, subStatus, _ := subscription(t, tenantID); tierID != tiers["production"] || model != "postpaid" || subStatus != "active" {
			t.Fatalf("created subscription = %s/%s/%s", tierID, model, subStatus)
		}
		if !resp.GetChanged() || resp.GetPreviousTierName() != "" {
			t.Fatalf("response = %+v", resp)
		}
	})

	t.Run("reassigning the current tier writes nothing", func(t *testing.T) {
		reconciler.calls = nil
		tenantID := seed(t, "production", "postpaid", "active")
		resp, err := server.AdminAssignTier(operatorAssignCtx(operatorID), &purserpb.AdminAssignTierRequest{
			TenantId: tenantID, TierName: "production", Reason: "no-op",
		})
		if err != nil {
			t.Fatalf("AdminAssignTier: %v", err)
		}
		if resp.GetChanged() || len(reconciler.calls) != 1 {
			t.Fatalf("changed %v, reconcile calls %v", resp.GetChanged(), reconciler.calls)
		}
		if n := purserCount(t, db, `SELECT COUNT(*) FROM purser.domain_event_outbox WHERE tenant_id = $1::uuid`, tenantID); n != 0 {
			t.Fatalf("unchanged assignment recorded %d events", n)
		}
	})

	for _, tc := range []struct {
		name  string
		tier  string
		model string
		start string
		code  codes.Code
		msg   string
	}{
		{"unknown tier", "platinum", "", "active", codes.NotFound, `billing tier "platinum" does not exist`},
		{"inactive tier", "retired", "", "active", codes.FailedPrecondition, `billing tier "retired" is inactive`},
		{"payg is prepaid only", "payg", "postpaid", "active", codes.FailedPrecondition, "runs prepaid"},
		{"paid tier is postpaid only", "production", "prepaid", "active", codes.FailedPrecondition, "runs postpaid"},
		{"closed account", "production", "", "cancelled", codes.FailedPrecondition, "cancelled"},
	} {
		t.Run("refuses "+tc.name, func(t *testing.T) {
			tenantID := seed(t, "free", "postpaid", tc.start)
			_, err := server.AdminAssignTier(operatorAssignCtx(operatorID), &purserpb.AdminAssignTierRequest{
				TenantId: tenantID, TierName: tc.tier, BillingModel: tc.model, Reason: "refusal",
			})
			if status.Code(err) != tc.code || !strings.Contains(status.Convert(err).Message(), tc.msg) {
				t.Fatalf("err = %v, want %v containing %q", err, tc.code, tc.msg)
			}
			if tierID, model, _, _ := subscription(t, tenantID); tierID != tiers["free"] || model != "postpaid" {
				t.Fatalf("refused assignment changed the subscription to %s/%s", tierID, model)
			}
			if n := purserCount(t, db, `SELECT COUNT(*) FROM purser.domain_event_outbox WHERE tenant_id = $1::uuid`, tenantID); n != 0 {
				t.Fatalf("refused assignment recorded %d events", n)
			}
		})
	}
}
