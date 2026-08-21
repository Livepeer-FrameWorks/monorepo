//go:build schema_verify

package grpc

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestPrepaidBalanceRepository_RealPG(t *testing.T) { //nolint:funlen // One engine contract proves the transaction's coupled invariants.
	db := startPurserTransitionRealPG(t)
	ctx := context.Background()
	server := &PurserServer{db: db, logger: logging.NewLogger()}
	tenantID := uuid.NewString()
	tierID := uuid.NewString()
	subscriptionID := uuid.NewString()

	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.billing_tiers (id, tier_name, display_name, tier_level)
		VALUES ($1, $2, 'Prepaid balance contract', 50)
	`, tierID, "prepaid-contract-"+uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.tenant_subscriptions (
			id, tenant_id, tier_id, status, billing_model, billing_email
		) VALUES ($1, $2, $3, 'suspended', 'prepaid', 'balance-contract@example.com')
	`, subscriptionID, tenantID, tierID); err != nil {
		t.Fatal(err)
	}
	periodStart := time.Now().UTC().Add(-30 * time.Minute)
	periodEnd := periodStart.Add(5 * time.Minute)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.tier_entitlements (tier_id, key, value) VALUES
			($1, 'recording_retention_days', '30'),
			($1, 'dvr_max_window_seconds', '3600'),
			($1, 'storage_limit_gb', '20'),
			($1, 'max_concurrent_viewers', '10')
	`, tierID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.subscription_entitlement_overrides (subscription_id, key, value) VALUES
			($1, 'recording_retention_days', '45'),
			($1, 'dvr_max_window_seconds', '7200'),
			($1, 'max_concurrent_viewers', '25')
	`, subscriptionID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.tier_pricing_rules (
			id, tier_id, meter, model, currency, included_quantity, unit_price
		) VALUES
			($2, $1, 'delivered_minutes', 'tiered_graduated', 'EUR', 100, 0.01),
			($3, $1, 'storage_gb_seconds_cold', 'tiered_graduated', 'EUR', 50, 0.001)
	`, tierID, uuid.NewString(), uuid.NewString()); err != nil {
		t.Fatal(err)
	}

	referenceID := uuid.NewString()
	referenceType := "contract_topup"
	request := &purserpb.TopupBalanceRequest{
		TenantId: tenantID, AmountCents: 750, Currency: "EUR", Description: "replayed top-up",
		ReferenceId: &referenceID, ReferenceType: &referenceType,
	}
	start := make(chan struct{})
	results := make([]*purserpb.BalanceTransaction, 2)
	errs := make([]error, 2)
	var wait sync.WaitGroup
	for index := range results {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			results[index], errs[index] = server.TopupBalance(ctx, request)
		}(index)
	}
	close(start)
	wait.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("top-up replay %d: %v", index, err)
		}
	}
	if results[0].GetId() != results[1].GetId() || results[0].GetBalanceAfterCents() != 750 || results[1].GetBalanceAfterCents() != 750 {
		t.Fatalf("replayed results = %+v / %+v", results[0], results[1])
	}

	var balance int64
	var subscriptionStatus string
	var ledgerRows, outboxRows int
	if err := db.QueryRowContext(ctx, `
		SELECT balance_cents FROM purser.prepaid_balances
		WHERE tenant_id = $1 AND currency = 'EUR'
	`, tenantID).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT status FROM purser.tenant_subscriptions WHERE tenant_id = $1
	`, tenantID).Scan(&subscriptionStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM purser.balance_transactions
		WHERE tenant_id = $1 AND reference_type = $2 AND reference_id = $3
	`, tenantID, referenceType, referenceID).Scan(&ledgerRows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM purser.billing_event_outbox
		WHERE tenant_id = $1 AND event_type = 'topup_credited'
	`, tenantID).Scan(&outboxRows); err != nil {
		t.Fatal(err)
	}
	if balance != 750 || subscriptionStatus != "active" || ledgerRows != 1 || outboxRows != 1 {
		t.Fatalf("balance/status/ledger/outbox = %d/%s/%d/%d", balance, subscriptionStatus, ledgerRows, outboxRows)
	}

	adjusted, err := server.AdjustBalance(ctx, &purserpb.AdjustBalanceRequest{
		TenantId: tenantID, AmountCents: 25, Currency: "EUR", Description: "contract adjustment",
	})
	if err != nil {
		t.Fatalf("unreferenced adjustment: %v", err)
	}
	if adjusted.GetBalanceAfterCents() != 775 || adjusted.GetTransactionType() != "refund" {
		t.Fatalf("adjustment = %+v", adjusted)
	}
	if _, err := server.DeductBalance(ctx, &purserpb.DeductBalanceRequest{
		TenantId: tenantID, AmountCents: 30, Currency: "EUR", Description: "recent usage",
	}); err != nil {
		t.Fatalf("usage deduction: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.usage_records (
			id, tenant_id, cluster_id, usage_type, unit, dimension_key, source_id,
			report_id, usage_value, value_kind, period_start, period_end, granularity
		) VALUES ($1, $2, 'cluster-contract', 'delivered_minutes', 'minute', repeat('0', 64),
			'source-contract', 'report-contract', 12, 'delta', $3, $4, 'minute_5')
	`, uuid.NewString(), tenantID, periodStart, periodEnd); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.usage_adjustments (
			id, tenant_id, usage_type, unit, delta_value, period_start, period_end,
			source_system, source_id
		) VALUES ($1, $2, 'delivered_minutes', 'minute', -2, $3, $4, 'contract', $5)
	`, uuid.NewString(), tenantID, periodStart, periodEnd, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.usage_reservations (
			tenant_id, source_id, cluster_id, sequence, report_id, period_start,
			period_end, meters, reserved_amount_micro, currency
		) VALUES ($1, 'reservation-source', 'cluster-contract', 1, 'reservation-report',
			$2, $3, '{}', 12345, 'EUR')
	`, tenantID, periodStart, periodEnd); err != nil {
		t.Fatal(err)
	}

	statusResponse, err := server.GetTenantBillingStatus(ctx, &purserpb.GetTenantBillingStatusRequest{TenantId: tenantID})
	if err != nil {
		t.Fatalf("get tenant billing status: %v", err)
	}
	if statusResponse.GetBalanceCents() != 745 || statusResponse.GetReservedBalanceCents() != 2 || statusResponse.GetAvailableBalanceCents() != 743 {
		t.Fatalf("billing status balances = %+v", statusResponse)
	}
	if statusResponse.GetRecordingRetentionDays() != 45 || statusResponse.GetDvrPolicy().GetMaxWindowSeconds() != 7200 {
		t.Fatalf("override precedence = %+v", statusResponse)
	}
	if statusResponse.GetStorageLimitBytes() != 20*(1<<30) || statusResponse.GetTenantResourceLimits().GetMaxViewers() != 25 {
		t.Fatalf("resource entitlements = %+v", statusResponse)
	}
	if len(statusResponse.GetAllowances()) != 1 || statusResponse.GetAllowances()[0].GetUsed() != 10 || statusResponse.GetAllowances()[0].GetRemaining() != 90 {
		t.Fatalf("allowances = %+v", statusResponse.GetAllowances())
	}
	if statusResponse.GetStoragePricing().GetIncludedGbHours() != 50 || statusResponse.GetStoragePricing().GetUnitPricePerGbHour() != 0.001 {
		t.Fatalf("storage pricing = %+v", statusResponse.GetStoragePricing())
	}
	prepaidResponse, err := server.GetPrepaidBalance(ctx, &purserpb.GetPrepaidBalanceRequest{TenantId: tenantID, Currency: "EUR"})
	if err != nil {
		t.Fatalf("get prepaid balance: %v", err)
	}
	if prepaidResponse.GetReservedBalanceCents() != 2 || prepaidResponse.GetAvailableBalanceCents() != 743 || prepaidResponse.GetDrainRateCentsPerHour() != 30 {
		t.Fatalf("prepaid read model = %+v", prepaidResponse)
	}

	if _, err := db.ExecContext(ctx, `
		CREATE FUNCTION purser.reject_balance_contract_row() RETURNS trigger AS $$
		BEGIN
			IF NEW.description = 'force-ledger-failure' THEN
				RAISE EXCEPTION 'forced ledger failure';
			END IF;
			RETURN NEW;
		END;
		$$ LANGUAGE plpgsql;
		CREATE TRIGGER reject_balance_contract_row
		BEFORE INSERT ON purser.balance_transactions
		FOR EACH ROW EXECUTE FUNCTION purser.reject_balance_contract_row();
	`); err != nil {
		t.Fatal(err)
	}
	_, err = server.AdjustBalance(ctx, &purserpb.AdjustBalanceRequest{
		TenantId: tenantID, AmountCents: 5, Currency: "EUR", Description: "force-ledger-failure",
	})
	if status.Code(err) != codes.Internal {
		t.Fatalf("forced ledger failure = %v, want Internal", err)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT balance_cents FROM purser.prepaid_balances
		WHERE tenant_id = $1 AND currency = 'EUR'
	`, tenantID).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if balance != 745 {
		t.Fatalf("balance after rejected ledger row = %d, want 745", balance)
	}

	topupType := "topup"
	listed, err := server.ListBalanceTransactions(ctx, &purserpb.ListBalanceTransactionsRequest{
		TenantId: tenantID, TransactionType: &topupType,
		TimeRange: &commonpb.TimeRange{
			Start: timestamppb.New(time.Now().Add(-time.Hour)),
			End:   timestamppb.New(time.Now().Add(time.Hour)),
		},
	})
	if err != nil {
		t.Fatalf("list filtered transactions: %v", err)
	}
	if len(listed.GetTransactions()) != 1 || listed.GetTransactions()[0].GetId() != results[0].GetId() {
		t.Fatalf("filtered transactions = %+v", listed.GetTransactions())
	}

	empty, err := server.ListBalanceTransactions(ctx, &purserpb.ListBalanceTransactionsRequest{
		TenantId:  tenantID,
		TimeRange: &commonpb.TimeRange{Start: timestamppb.New(time.Now().Add(time.Hour))},
	})
	if err != nil {
		t.Fatalf("list future transactions: %v", err)
	}
	if len(empty.GetTransactions()) != 0 {
		t.Fatalf("future filter returned %+v", empty.GetTransactions())
	}

	pendingTopupID := uuid.NewString()
	completedTopupID := uuid.NewString()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.pending_topups (
			id, tenant_id, provider, checkout_id, amount_cents, currency, status,
			expires_at, completed_at, balance_transaction_id
		) VALUES
			($1, $3, 'stripe', NULL, 500, 'EUR', 'pending', NOW() + INTERVAL '1 hour', NULL, NULL),
			($2, $3, 'mollie', 'tr_contract', 750, 'EUR', 'completed', NOW() + INTERVAL '1 hour', NOW(), $4)
	`, pendingTopupID, completedTopupID, tenantID, results[0].GetId()); err != nil {
		t.Fatal(err)
	}
	pendingTopup, err := server.GetPendingTopup(ctx, &purserpb.GetPendingTopupRequest{
		Lookup: &purserpb.GetPendingTopupRequest_TopupId{TopupId: pendingTopupID},
	})
	if err != nil || pendingTopup.GetCheckoutId() != "" || pendingTopup.GetCompletedAt() != nil {
		t.Fatalf("pending top-up by id = %+v / %v", pendingTopup, err)
	}
	completedTopup, err := server.GetPendingTopup(ctx, &purserpb.GetPendingTopupRequest{
		Provider: "mollie", Lookup: &purserpb.GetPendingTopupRequest_CheckoutId{CheckoutId: "tr_contract"},
	})
	if err != nil || completedTopup.GetId() != completedTopupID || completedTopup.GetBalanceTransactionId() != results[0].GetId() {
		t.Fatalf("completed top-up by checkout = %+v / %v", completedTopup, err)
	}
	completedStatus := "completed"
	topups, err := server.ListPendingTopups(ctx, &purserpb.ListPendingTopupsRequest{
		TenantId: tenantID, Status: &completedStatus,
	})
	if err != nil || len(topups.GetTopups()) != 1 || topups.GetTopups()[0].GetId() != completedTopupID {
		t.Fatalf("completed top-up list = %+v / %v", topups, err)
	}
}
