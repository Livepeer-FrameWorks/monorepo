//go:build schema_verify

package grpc

import (
	"context"
	"testing"
	"time"

	billingpkg "frameworks/api_billing/internal/billing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// An operator grant decides a postpaid tenant's collection readiness and
// base fee on the real schema: a base-fee waiver alone still needs a way to
// collect usage, manual invoicing or owing nothing does not, an expired grant
// stops applying, and a Stripe subscription blocks a base-fee override.
func TestOperatorBillingGrant_RealPG(t *testing.T) { //nolint:funlen // One database carries the whole grant matrix.
	db := startPurserTransitionRealPG(t)
	ctx := context.Background()
	supporterID := uuid.NewString()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.billing_tiers (id, tier_name, display_name, tier_level, base_price, currency, metering_enabled, is_active)
		VALUES ($1, 'supporter', 'Supporter', 2, 25.00, 'EUR', true, true)
	`, supporterID); err != nil {
		t.Fatal(err)
	}
	server := &PurserServer{db: db, logger: logging.NewLogger(), tierReconciler: &assignTierReconciler{}, commodoreClient: &recordingCommodoreCache{}}
	opCtx := operatorAssignCtx("7a000000-0000-4000-8000-000000000002")

	seed := func(t *testing.T, paymentMethod, stripeCustomer, stripeSub string) string {
		t.Helper()
		tenantID := uuid.NewString()
		if _, err := db.ExecContext(ctx, `
			INSERT INTO purser.tenant_subscriptions (id, tenant_id, tier_id, billing_model, status, payment_method, stripe_customer_id, stripe_subscription_id)
			VALUES ($1, $2, $3, 'postpaid', 'active', NULLIF($4, ''), NULLIF($5, ''), NULLIF($6, ''))
		`, uuid.NewString(), tenantID, supporterID, paymentMethod, stripeCustomer, stripeSub); err != nil {
			t.Fatal(err)
		}
		return tenantID
	}
	admission := func(t *testing.T, tenantID string) (bool, string) {
		t.Helper()
		resp, err := server.GetTenantAdmissionStatus(ctx, &purserpb.GetTenantAdmissionStatusRequest{TenantId: tenantID})
		if err != nil {
			t.Fatalf("GetTenantAdmissionStatus: %v", err)
		}
		return resp.GetCollectionReady(), resp.GetCollectionProvider()
	}
	zero := "0"
	grant := func(t *testing.T, req *purserpb.AdminSetBillingGrantRequest) *purserpb.AdminBillingGrantResponse {
		t.Helper()
		resp, err := server.AdminSetBillingGrant(opCtx, req)
		if err != nil {
			t.Fatalf("AdminSetBillingGrant: %v", err)
		}
		return resp
	}

	t.Run("paid postpaid tier without collection is not ready", func(t *testing.T) {
		tenantID := seed(t, "", "", "")
		if ready, _ := admission(t, tenantID); ready {
			t.Fatal("a supporter tenant with no payment setup must not be collection-ready")
		}
	})

	t.Run("a saved Stripe card is collection", func(t *testing.T) {
		tenantID := seed(t, "stripe", "cus_saved", "")
		if ready, provider := admission(t, tenantID); !ready || provider != "stripe" {
			t.Fatalf("card on file: ready=%v provider=%q, want stripe", ready, provider)
		}
	})

	t.Run("waived base fee alone still needs collection for usage", func(t *testing.T) {
		tenantID := seed(t, "", "", "")
		resp := grant(t, &purserpb.AdminSetBillingGrantRequest{TenantId: tenantID, BasePrice: &zero, Reason: "beta supporter"})
		if resp.GetCollectionReady() {
			t.Fatal("usage is still owed; the tenant must add a card or be invoiced by hand")
		}
		tier, err := billingpkg.LoadEffectiveTier(ctx, db, tenantID)
		if err != nil {
			t.Fatal(err)
		}
		if !tier.BasePrice.IsZero() || !tier.BasePriceGranted || tier.UsageWaived {
			t.Fatalf("effective tier = base %s granted %v waived %v", tier.BasePrice, tier.BasePriceGranted, tier.UsageWaived)
		}
		var payload []byte
		if err := db.QueryRowContext(ctx, `
			SELECT payload FROM purser.domain_event_outbox
			WHERE tenant_id = $1::uuid AND event_type = 'billing.subscription_updated'
		`, tenantID).Scan(&payload); err != nil {
			t.Fatalf("subscription_updated: %v", err)
		}
		var ev internalv1.SubscriptionUpdated
		if err := proto.Unmarshal(payload, &ev); err != nil {
			t.Fatal(err)
		}
		if len(ev.GetChangedFields()) != 1 || ev.GetChangedFields()[0] != "operator_grant" || ev.GetReason() != "beta supporter" {
			t.Fatalf("event = %+v", &ev)
		}
	})

	t.Run("manual invoicing is collection", func(t *testing.T) {
		tenantID := seed(t, "", "", "")
		resp := grant(t, &purserpb.AdminSetBillingGrantRequest{TenantId: tenantID, BasePrice: &zero, Collection: "invoice", Reason: "house account"})
		if !resp.GetCollectionReady() || resp.GetCollectionProvider() != "operator" {
			t.Fatalf("invoice grant: ready=%v provider=%q", resp.GetCollectionReady(), resp.GetCollectionProvider())
		}
	})

	t.Run("owing nothing needs no collection, until the grant expires", func(t *testing.T) {
		tenantID := seed(t, "", "", "")
		grant(t, &purserpb.AdminSetBillingGrantRequest{
			TenantId: tenantID, BasePrice: &zero, WaiveUsage: true, Reason: "staging test",
			ExpiresAt: timestamppb.New(time.Now().Add(time.Hour)),
		})
		if ready, provider := admission(t, tenantID); !ready || provider != "operator" {
			t.Fatalf("comp: ready=%v provider=%q", ready, provider)
		}
		if _, err := db.ExecContext(ctx, `
			UPDATE purser.subscription_operator_grants g SET expires_at = NOW() - INTERVAL '1 minute'
			FROM purser.tenant_subscriptions ts WHERE ts.id = g.subscription_id AND ts.tenant_id = $1
		`, tenantID); err != nil {
			t.Fatal(err)
		}
		if ready, _ := admission(t, tenantID); ready {
			t.Fatal("an expired grant must stop standing in for collection")
		}
		tier, err := billingpkg.LoadEffectiveTier(ctx, db, tenantID)
		if err != nil {
			t.Fatal(err)
		}
		if tier.BasePrice.String() != "25" || tier.BasePriceGranted || tier.UsageWaived {
			t.Fatalf("expired grant still applies: base %s waived %v", tier.BasePrice, tier.UsageWaived)
		}
		shown, err := server.AdminGetBillingGrant(opCtx, &purserpb.AdminGetBillingGrantRequest{TenantId: tenantID})
		if err != nil {
			t.Fatal(err)
		}
		if shown.GetGrant() == nil || shown.GetGrant().GetActive() {
			t.Fatalf("an expired grant stays on record as inactive: %+v", shown.GetGrant())
		}
		// Showing a grant reports the tier it applies to; nothing was assigned.
		if tier := shown.GetTier(); tier.GetTierName() != "supporter" || tier.GetTierId() != supporterID ||
			tier.GetTierLevel() != 2 || tier.GetBillingModel() != "postpaid" || tier.GetSubscriptionId() == "" || shown.GetAssignment() != nil {
			t.Fatalf("shown tier = %+v assignment = %+v, want the supporter postpaid subscription and no assignment", tier, shown.GetAssignment())
		}
	})

	t.Run("a Stripe subscription blocks a base-fee override", func(t *testing.T) {
		tenantID := seed(t, "stripe", "cus_sub", "sub_live")
		_, err := server.AdminSetBillingGrant(opCtx, &purserpb.AdminSetBillingGrantRequest{TenantId: tenantID, BasePrice: &zero, Reason: "x"})
		if status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("err = %v, want FailedPrecondition", err)
		}
		// Waiving usage does not touch the base fee, so it is allowed.
		grant(t, &purserpb.AdminSetBillingGrantRequest{TenantId: tenantID, WaiveUsage: true, Reason: "usage comp"})
	})

	t.Run("a grant with a tier reports the assignment and the tier", func(t *testing.T) {
		tenantID := seed(t, "", "", "")
		resp := grant(t, &purserpb.AdminSetBillingGrantRequest{TenantId: tenantID, TierName: "supporter", Collection: "invoice", Reason: "house account"})
		if resp.GetAssignment().GetTierName() != "supporter" || resp.GetAssignment().GetChanged() || resp.GetTier().GetTierName() != "supporter" {
			t.Fatalf("assignment = %+v tier = %+v, want an unchanged supporter assignment and the supporter tier", resp.GetAssignment(), resp.GetTier())
		}
	})

	t.Run("revoke removes the grant", func(t *testing.T) {
		tenantID := seed(t, "", "", "")
		grant(t, &purserpb.AdminSetBillingGrantRequest{TenantId: tenantID, Collection: "invoice", Reason: "house account"})
		resp, err := server.AdminRevokeBillingGrant(opCtx, &purserpb.AdminRevokeBillingGrantRequest{TenantId: tenantID, Reason: "ended"})
		if err != nil {
			t.Fatalf("revoke: %v", err)
		}
		if resp.GetGrant() != nil || resp.GetCollectionReady() {
			t.Fatalf("after revoke: grant=%v ready=%v", resp.GetGrant(), resp.GetCollectionReady())
		}
		if resp.GetTier().GetTierName() != "supporter" || resp.GetTier().GetBillingModel() != "postpaid" {
			t.Fatalf("after revoke: tier = %+v, want the supporter postpaid tier the tenant stays on", resp.GetTier())
		}
		if _, err := server.AdminRevokeBillingGrant(opCtx, &purserpb.AdminRevokeBillingGrantRequest{TenantId: tenantID, Reason: "again"}); status.Code(err) != codes.NotFound {
			t.Fatalf("second revoke err = %v, want NotFound", err)
		}
	})

	t.Run("a recorded bank transfer pays the invoice", func(t *testing.T) {
		tenantID := seed(t, "", "", "")
		invoiceID := uuid.NewString()
		if _, err := db.ExecContext(ctx, `
			INSERT INTO purser.billing_invoices (
				id, tenant_id, status, currency, amount, due_date,
				presentment_amount_cents, presentment_currency, presentment_units_per_eur,
				presentment_reference_date, finalized_at
			) VALUES ($1, $2, 'overdue', 'EUR', 12.50, NOW() - INTERVAL '1 day', 1250, 'EUR', 1, CURRENT_DATE, NOW() - INTERVAL '20 days')
		`, invoiceID, tenantID); err != nil {
			t.Fatal(err)
		}
		resp, err := server.AdminRecordInvoicePayment(opCtx, &purserpb.AdminRecordInvoicePaymentRequest{
			TenantId: tenantID, InvoiceId: invoiceID, Reference: "transfer 2026-10-03", Reason: "bank transfer received",
		})
		if err != nil {
			t.Fatalf("AdminRecordInvoicePayment: %v", err)
		}
		if resp.GetAmount() != "12.50" || resp.GetCurrency() != "EUR" || resp.GetInvoiceStatus() != "paid" {
			t.Fatalf("response = %+v", resp)
		}
		var invoiceStatus, method, paymentStatus, txID string
		if err := db.QueryRowContext(ctx, `
			SELECT i.status, p.method, p.status, p.tx_id
			FROM purser.billing_invoices i JOIN purser.billing_payments p ON p.invoice_id = i.id
			WHERE i.id = $1
		`, invoiceID).Scan(&invoiceStatus, &method, &paymentStatus, &txID); err != nil {
			t.Fatal(err)
		}
		if invoiceStatus != "paid" || method != "bank_transfer" || paymentStatus != "confirmed" || txID != "transfer 2026-10-03" {
			t.Fatalf("invoice %s, payment %s/%s/%s", invoiceStatus, method, paymentStatus, txID)
		}
		if _, err := server.AdminRecordInvoicePayment(opCtx, &purserpb.AdminRecordInvoicePaymentRequest{
			TenantId: tenantID, InvoiceId: invoiceID, Reference: "again", Reason: "again",
		}); status.Code(err) != codes.NotFound {
			t.Fatalf("a paid invoice takes no second payment: err = %v", err)
		}
	})
}
