//go:build schema_verify

package grpc

import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"frameworks/api_billing/internal/handlers"
	billingstripe "frameworks/api_billing/internal/stripe"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	internalv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type meteringHeldUpgrade struct {
	db                       *sql.DB
	server                   *PurserServer
	tenant, newTier, upgrade string
	anchor                   time.Time
}

// newMeteringHeldUpgrade seeds a paid Stripe purchase whose old phase has no
// complete metering, so its billing boundary refuses.
func newMeteringHeldUpgrade(t *testing.T) *meteringHeldUpgrade {
	t.Helper()
	db := startOperatorGrantDatabase(t)
	ctx := t.Context()
	tenant, oldTier, newTier, upgrade := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	start := time.Now().UTC().Add(-10 * 24 * time.Hour).Truncate(time.Second)
	anchor := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("%v\n%s", err, query)
		}
	}
	exec(`INSERT INTO purser.billing_tiers (id,tier_name,display_name,tier_level,base_price,currency,metering_enabled,stripe_price_id_monthly) VALUES
		($1::text::uuid,$1::text,'Old',2,20,'EUR',true,'old-price'),($2::text::uuid,$2::text,'New',3,50,'EUR',true,'new-price')`, oldTier, newTier)
	exec(`INSERT INTO purser.tenant_subscriptions (tenant_id,tier_id,status,billing_model,payment_method,stripe_customer_id,stripe_subscription_id,billing_email,presentment_currency,
		billing_period_start,billing_period_end,next_billing_date,pending_tier_id,pending_effective_at,pending_reason)
		VALUES ($1,$2,'active','postpaid','stripe','customer','subscription','billing@example.com','EUR',$3,$4,$4,$5,$6,'tier_upgrade')`, tenant, oldTier, start, start.AddDate(0, 1, 0), newTier, anchor)
	receipt, err := json.Marshal(billing.TierUpgradeReceipt{Applied: true, PeriodStart: anchor, PeriodEnd: anchor.AddDate(0, 1, 0), PaidReceiptID: "upgrade-invoice"})
	if err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO purser.subscription_tier_upgrades (id,tenant_id,old_tier_id,tier_id,actor_id,old_period_start,old_period_end,requested_at,provider,collection_provider,customer_id,provider_subscription_id,stripe_price_id,old_base_cents,base_cents,state,provider_receipt)
		VALUES ($1,$2,$3,$4,'customer',$5,$6,$7,'stripe','stripe','customer','subscription','new-price',2000,5000,'provider_applied',$8)`, upgrade, tenant, oldTier, newTier, start, start.AddDate(0, 1, 0), anchor, receipt)
	logger := logging.NewLogger()
	service := handlers.NewService(db, logger, nil, nil, nil, billingstripe.NewClient(billingstripe.Config{SecretKey: "sk_test_fixture", Logger: logger}), nil)
	return &meteringHeldUpgrade{db: db, tenant: tenant, newTier: newTier, upgrade: upgrade, anchor: anchor,
		server: &PurserServer{db: db, logger: logger, billing: service, tierReconciler: &assignTierReconciler{}, commodoreClient: &recordingCommodoreCache{}}}
}

// Metering holds a paid purchase until complete. Plain recovery keeps waiting;
// only an explicit, audited waiver closes the phase on recorded usage.
func TestMeteringHoldNeedsAnExplicitWaiver_RealPG(t *testing.T) {
	f := newMeteringHeldUpgrade(t)
	ctx := t.Context()
	request := &purserpb.AdminRecoverTierUpgradeRequest{TenantId: f.tenant, UpgradeId: f.upgrade, Reason: "metering outage reviewed", Confirmed: true}
	result, err := f.server.AdminRecoverTierUpgrade(operatorAssignCtx(uuid.NewString()), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.GetState() != "provider_applied" || result.GetMeteringWaivedBy() != "" {
		t.Fatalf("recovery without a waiver = state %q waived by %q detail %q, want it still held on metering", result.GetState(), result.GetMeteringWaivedBy(), result.GetDetail())
	}
	waiver := uuid.NewString()
	request.WaiveMetering, request.Reason = true, "metering outage cannot be repaired"
	preview := &purserpb.AdminRecoverTierUpgradeRequest{TenantId: f.tenant, UpgradeId: f.upgrade, Reason: request.Reason, WaiveMetering: true}
	if shown, previewErr := f.server.AdminRecoverTierUpgrade(operatorAssignCtx(waiver), preview); previewErr != nil || shown.GetState() != "provider_applied" || shown.GetMeteringWaivedBy() != "" {
		t.Fatalf("preview recorded a waiver: %+v %v", shown, previewErr)
	}
	result, err = f.server.AdminRecoverTierUpgrade(operatorAssignCtx(waiver), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.GetState() != "completed" || result.GetMeteringWaivedBy() != waiver {
		t.Fatalf("waived recovery = state %q waived by %q detail %q, want completed with its waiver", result.GetState(), result.GetMeteringWaivedBy(), result.GetDetail())
	}
	var tier, reason string
	var invoices int
	if err = f.db.QueryRowContext(ctx, `SELECT s.tier_id::text,u.metering_waiver_reason,(SELECT count(*) FROM purser.billing_invoices i WHERE i.tenant_id=s.tenant_id AND i.period_end=$2::timestamptz)
		FROM purser.tenant_subscriptions s JOIN purser.subscription_tier_upgrades u ON u.tenant_id=s.tenant_id WHERE s.tenant_id=$1`, f.tenant, f.anchor).Scan(&tier, &reason, &invoices); err != nil {
		t.Fatal(err)
	}
	if tier != f.newTier || reason != request.Reason || invoices != 1 {
		t.Fatalf("waived upgrade: tier=%s reason=%q closing invoices=%d", tier, reason, invoices)
	}
	rows, err := f.db.QueryContext(ctx, `SELECT payload FROM purser.domain_event_outbox WHERE tenant_id=$1::uuid AND event_type='billing.subscription_updated'`, f.tenant)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	audited := false
	for rows.Next() {
		var payload []byte
		if err = rows.Scan(&payload); err != nil {
			t.Fatal(err)
		}
		event := &internalv1.SubscriptionUpdated{}
		if err = proto.Unmarshal(payload, event); err != nil {
			t.Fatal(err)
		}
		for _, field := range event.GetChangedFields() {
			audited = audited || (field == "tier_upgrade_metering_waived" && event.GetReason() == request.Reason)
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !audited {
		t.Fatal("the metering waiver has no domain event")
	}
}

// A waiver names a purchase the provider already applied, so one requested
// earlier cannot be carried into a later metering hold.
func TestRecoveryOfARequestedUpgradeCannotWaiveMetering_RealPG(t *testing.T) {
	f := newMeteringHeldUpgrade(t)
	if _, err := f.db.ExecContext(t.Context(), `UPDATE purser.subscription_tier_upgrades SET state='requested' WHERE tenant_id=$1`, f.tenant); err != nil {
		t.Fatal(err)
	}
	_, err := f.server.AdminRecoverTierUpgrade(operatorAssignCtx(uuid.NewString()), &purserpb.AdminRecoverTierUpgradeRequest{TenantId: f.tenant, UpgradeId: f.upgrade, Reason: "too early", Confirmed: true, WaiveMetering: true})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("waiver on a requested upgrade = %v, want FailedPrecondition", err)
	}
	var waived, recovery sql.NullTime
	if err = f.db.QueryRowContext(t.Context(), `SELECT metering_waived_at,recovery_requested_at FROM purser.subscription_tier_upgrades WHERE tenant_id=$1`, f.tenant).Scan(&waived, &recovery); err != nil || waived.Valid || recovery.Valid {
		t.Fatalf("refused waiver left waived=%v recovery=%v err=%v", waived, recovery, err)
	}
}
