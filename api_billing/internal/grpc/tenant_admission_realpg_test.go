//go:build schema_verify

package grpc

import (
	"context"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
)

func TestTenantAdmissionStatus_RealPG(t *testing.T) {
	db := startPurserTransitionRealPG(t)
	ctx := context.Background()
	tierID := uuid.NewString()
	tenantID := uuid.NewString()

	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.billing_tiers (id, tier_name, display_name)
		VALUES ($1, $2, 'Admission RPC real-engine')
	`, tierID, "admission-rpc-"+tierID); err != nil {
		t.Fatalf("insert tier: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.tenant_subscriptions (tenant_id, tier_id, status, billing_model)
		VALUES ($1, $2, 'active', 'prepaid')
	`, tenantID, tierID); err != nil {
		t.Fatalf("insert subscription: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.prepaid_balances (tenant_id, balance_cents, currency)
		VALUES ($1, 100, 'EUR')
	`, tenantID); err != nil {
		t.Fatalf("insert balance: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.usage_reservations
		    (tenant_id, source_id, cluster_id, sequence, report_id, period_start,
		     period_end, meters, reserved_amount_micro, currency, updated_at)
		VALUES ($1, 'rpc', 'cluster-rpc', 1, 'rpc-report', NOW() - INTERVAL '1 minute',
		        NOW(), '{}'::jsonb, 1250001, 'EUR', NOW())
	`, tenantID); err != nil {
		t.Fatalf("insert reservation: %v", err)
	}

	server := &PurserServer{db: db, logger: logging.NewLogger()}
	response, err := server.GetTenantAdmissionStatus(ctx, &purserpb.GetTenantAdmissionStatusRequest{TenantId: tenantID})
	if err != nil {
		t.Fatalf("GetTenantAdmissionStatus: %v", err)
	}
	if response.BillingModel != "prepaid" || response.BalanceCents != 100 || response.ReservedBalanceCents != 126 || response.AvailableBalanceCents != -26 || !response.IsBalanceNegative {
		t.Fatalf("unexpected admission response: %#v", response)
	}

	missing, err := server.GetTenantAdmissionStatus(ctx, &purserpb.GetTenantAdmissionStatusRequest{TenantId: uuid.NewString()})
	if err != nil {
		t.Fatalf("GetTenantAdmissionStatus missing subscription: %v", err)
	}
	if missing.BillingModel != "prepaid" || !missing.IsBalanceNegative || missing.AvailableBalanceCents != 0 {
		t.Fatalf("missing subscription did not fail closed: %#v", missing)
	}
}
