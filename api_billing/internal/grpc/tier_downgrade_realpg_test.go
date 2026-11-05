//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/handlers"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	internalv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// grpcTierDowngradeCases are the downgrade contracts of this package. The
// Makefile runs them on PostgreSQL by name and on Yugabyte through
// TestTierDowngradeLifecycle_RealYugabyte; selection parity keeps both lists equal.
func grpcTierDowngradeCases() []struct {
	name string
	run  func(*testing.T)
} {
	return []struct {
		name string
		run  func(*testing.T)
	}{
		{"TestChangeBillingTierQueuesProviderStep", TestChangeBillingTierQueuesProviderStep_RealPG},
		{"TestFreeDowngradeKeepsPaidTermThenMoves", TestFreeDowngradeKeepsPaidTermThenMoves_RealPG},
		{"TestFreeDowngradeRepointKeepsSubscriptionLive", TestFreeDowngradeRepointKeepsSubscriptionLive_RealPG},
		{"TestFreeDowngradeProviderFailureRetries", TestFreeDowngradeProviderFailureRetries_RealPG},
		{"TestFreeDowngradeLeavesAPaymentFailureFallback", TestFreeDowngradeLeavesAPaymentFailureFallback_RealPG},
		{"TestFreeStopAfterAConfirmedPriceChange", TestFreeStopAfterAConfirmedPriceChange_RealPG},
		{"TestRepointKeepsAPaymentFailureFallback", TestRepointKeepsAPaymentFailureFallback_RealPG},
		{"TestConfirmedDowngradePastItsEffectiveTimeRefusesChanges", TestConfirmedDowngradePastItsEffectiveTimeRefusesChanges_RealPG},
		{"TestMoveToFreeWritesOffCollectionCarry", TestMoveToFreeWritesOffCollectionCarry_RealPG},
		{"TestTierDowngradeRecoveryRecordsAndReleasesTheHold", TestTierDowngradeRecoveryRecordsAndReleasesTheHold_RealPG},
	}
}

func TestTierDowngradeLifecycle_RealYugabyte(t *testing.T) {
	contracts := grpcTierDowngradeCases()
	if !t.Run("selection parity", func(t *testing.T) {
		selection := os.Getenv("FRAMEWORKS_TIER_DOWNGRADE_TESTS")
		if selection == "" {
			t.Fatal("selection parity requires the Makefile's canonical test list; run make verify-purser-tier-downgrade-test-selection")
		}
		selected := make(map[string]bool)
		for _, name := range strings.Split(selection, "|") {
			selected[name] = true
		}
		for _, contract := range contracts {
			name := contract.name + "_RealPG"
			if !selected[name] {
				t.Fatalf("Yugabyte downgrade case %s is missing from PURSER_TIER_DOWNGRADE_GRPC_REALPG_TESTS", name)
			}
			delete(selected, name)
		}
		if len(selected) != 0 {
			t.Fatalf("Makefile downgrade cases are absent from grpcTierDowngradeCases: %v", selected)
		}
	}) {
		return
	}
	for _, contract := range contracts {
		t.Run(contract.name, contract.run)
	}
}

// downgradeRecoveryReconciler is the tier reconciler the billing job manager needs.
type downgradeRecoveryReconciler struct{ assignTierReconciler }

func (*downgradeRecoveryReconciler) SweepDeploymentTiers(context.Context) (int, error) { return 0, nil }

// An operator's recovery request lists the held downgrade on the first page,
// previews it without writing, refuses a target other than the previewed one,
// and on confirmation records the operator and reason before applying it.
func TestTierDowngradeRecoveryRecordsAndReleasesTheHold_RealPG(t *testing.T) {
	configureRPCTaxSupplier(t)
	db := startOperatorGrantDatabase(t)
	ctx := t.Context()
	tenant, current, target, operatorID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("%v\n%s", err, query)
		}
	}
	effective := time.Now().UTC().Truncate(time.Second).Add(-handlers.HeldDowngradeGrace - time.Hour)
	periodStart := effective.Add(-30 * 24 * time.Hour)
	exec(`INSERT INTO purser.billing_tiers (id,tier_name,display_name,tier_level,base_price,currency,metering_enabled,is_active) VALUES
		($1::text::uuid,$1::text,'Current',3,0,'EUR',false,true),($2::text::uuid,$2::text,'Target',2,0,'EUR',false,true)`, current, target)
	exec(`INSERT INTO purser.tenant_subscriptions (billing_customer_type,billing_name,billing_company,billing_address,tenant_id,tier_id,status,billing_model,billing_email,presentment_currency,
		billing_period_start,billing_period_end,next_billing_date,pending_tier_id,pending_reason,pending_effective_at,pending_provider_tier_id)
		VALUES ('business','Fixture Buyer','Fixture Ltd','{"street":"1 Main Street","city":"Toronto","postal_code":"M5V1J1","country":"CA"}'::jsonb,$1,$2,'active','postpaid','billing@example.com','EUR',$4,$5,$5,$3,'downgrade',$6,$3)`, tenant, current, target, periodStart, effective, effective)
	exec(`INSERT INTO purser.billing_invoices (tenant_id,status,currency,amount,period_start,period_end,due_date,paid_at)
		VALUES ($1,'paid','EUR',0,$2,$3,$3,NOW())`, tenant, periodStart, effective)

	logger := logging.NewLogger()
	billingService := handlers.NewService(db, logger, nil, nil, nil, nil, nil)
	handlers.NewJobManager(db, logger, &recordingCommodoreCache{}, nil, nil, &downgradeRecoveryReconciler{}, billingService, nil)
	server := &PurserServer{db: db, logger: logger, billing: billingService}
	op := operatorAssignCtx(operatorID)
	recoveryEvents := func() []*internalv1.SubscriptionUpdated {
		t.Helper()
		rows, err := db.QueryContext(ctx, `SELECT payload, COALESCE(actor_user_id::text,'') FROM purser.domain_event_outbox
			WHERE tenant_id = $1::uuid AND event_type = 'billing.subscription_updated'`, tenant)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []*internalv1.SubscriptionUpdated
		for rows.Next() {
			var payload []byte
			var actor string
			if err = rows.Scan(&payload, &actor); err != nil {
				t.Fatal(err)
			}
			var ev internalv1.SubscriptionUpdated
			if err = proto.Unmarshal(payload, &ev); err != nil {
				t.Fatal(err)
			}
			if slices.Contains(ev.GetChangedFields(), "tier_downgrade_recovery_requested") {
				if actor != operatorID {
					t.Fatalf("recovery event actor = %q, want the operator %s", actor, operatorID)
				}
				out = append(out, &ev)
			}
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}

	first, err := server.AdminListCheckoutRecovery(op, &purserpb.AdminListCheckoutRecoveryRequest{TenantId: tenant, Limit: 10})
	if err != nil || len(first.GetHeldDowngrades()) != 1 || first.GetHeldDowngrades()[0].GetTargetTierId() != target || first.GetHeldDowngradesHasMore() {
		t.Fatalf("first page = %v, %v, want the one held downgrade", first, err)
	}
	later, err := server.AdminListCheckoutRecovery(op, &purserpb.AdminListCheckoutRecoveryRequest{TenantId: tenant, Limit: 10, Offset: 10})
	if err != nil || len(later.GetHeldDowngrades()) != 0 {
		t.Fatalf("later page = %v, %v, want no held downgrades", later, err)
	}

	preview, err := server.AdminRecoverTierDowngrade(op, &purserpb.AdminRecoverTierDowngradeRequest{TenantId: tenant, Reason: "applier missed the run"})
	if err != nil || preview.GetStatus() != handlers.DowngradeRecoveryDue || preview.GetDowngrade().GetTargetTierId() != target {
		t.Fatalf("preview = %v, %v, want due for target %s", preview, err, target)
	}
	_, err = server.AdminRecoverTierDowngrade(op, &purserpb.AdminRecoverTierDowngradeRequest{
		TenantId: tenant, Confirmed: true, Reason: "applier missed the run", TargetTierId: current,
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("confirmation for another target = %v, want FailedPrecondition", err)
	}
	if events := recoveryEvents(); len(events) != 0 {
		t.Fatalf("preview or refused confirmation recorded %d recovery events", len(events))
	}

	applied, err := server.AdminRecoverTierDowngrade(op, &purserpb.AdminRecoverTierDowngradeRequest{
		TenantId: tenant, Confirmed: true, Reason: "applier missed the run", TargetTierId: target,
	})
	if err != nil || applied.GetStatus() != handlers.DowngradeRecoveryApplied {
		t.Fatalf("confirmation = %v, %v, want applied", applied, err)
	}
	events := recoveryEvents()
	if len(events) != 1 || events[0].GetReason() != "applier missed the run" {
		t.Fatalf("recovery events = %v, want one carrying the reason", events)
	}
	var tier string
	var pending sql.NullString
	if err = db.QueryRowContext(ctx, `SELECT tier_id::text, pending_tier_id::text FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant).Scan(&tier, &pending); err != nil {
		t.Fatal(err)
	}
	if tier != target || pending.Valid {
		t.Fatalf("after recovery: tier=%s pending=%v, want the target with nothing pending", tier, pending)
	}
}
