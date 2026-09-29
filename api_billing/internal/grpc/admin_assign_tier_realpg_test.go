//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"strings"
	"testing"

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
	db := startPurserTransitionRealPG(t)
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
		if _, err := db.ExecContext(ctx, `UPDATE purser.tenant_subscriptions SET pending_tier_id = $2, pending_effective_at = NOW() + INTERVAL '1 day', pending_reason = 'downgrade' WHERE tenant_id = $1`, tenantID, tiers["free"]); err != nil {
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
