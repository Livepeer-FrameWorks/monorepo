package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
)

// No subscription row → the cheapest safe default: postpaid, not suspended,
// zero balance. Foghorn/Commodore admission must never read "suspended" or a
// negative balance for a tenant Purser simply hasn't provisioned yet.
func TestGetTenantBillingStatusNoSubscriptionDefault(t *testing.T) {
	s, mock := newReadServer(t, true)
	mock.ExpectQuery(`LEFT JOIN purser\.prepaid_balances pb`).
		WillReturnError(sqlmockNoRows())

	resp, err := s.GetTenantBillingStatus(context.Background(), &purserpb.GetTenantBillingStatusRequest{TenantId: "tenant-1"})
	if err != nil {
		t.Fatalf("GetTenantBillingStatus: %v", err)
	}
	if resp.BillingModel != "postpaid" || resp.IsSuspended || resp.IsBalanceNegative || resp.BalanceCents != 0 {
		t.Fatalf("unexpected default: %+v", resp)
	}
}

// A prepaid tenant whose balance has gone non-positive must report
// IsBalanceNegative — the gate that drives suspension/throttle downstream.
// is_balance_negative is prepaid-only: a postpaid tenant with the same balance
// must NOT trip it.
func TestGetTenantBillingStatusPrepaidNegativeBalance(t *testing.T) {
	cols := []string{
		"billing_model", "status", "balance_cents", "retention", "dvr_entitlements",
		"tier_id", "billing_period_start", "billing_period_end", "storage_limit", "resource_limits",
	}

	t.Run("prepaid non-positive trips negative", func(t *testing.T) {
		s, mock := newReadServer(t, true)
		mock.ExpectQuery(`LEFT JOIN purser\.prepaid_balances pb`).
			WillReturnRows(sqlmock.NewRows(cols).
				AddRow("prepaid", "active", int64(-50), nil, nil, nil, nil, nil, nil, nil))

		resp, err := s.GetTenantBillingStatus(context.Background(), &purserpb.GetTenantBillingStatusRequest{TenantId: "tenant-1"})
		if err != nil {
			t.Fatalf("GetTenantBillingStatus: %v", err)
		}
		if !resp.IsBalanceNegative || resp.BalanceCents != -50 || resp.BillingModel != "prepaid" {
			t.Fatalf("expected negative prepaid balance: %+v", resp)
		}
	})

	t.Run("postpaid same balance stays non-negative", func(t *testing.T) {
		s, mock := newReadServer(t, true)
		mock.ExpectQuery(`LEFT JOIN purser\.prepaid_balances pb`).
			WillReturnRows(sqlmock.NewRows(cols).
				AddRow("postpaid", "suspended", int64(-50), nil, nil, nil, nil, nil, nil, nil))

		resp, err := s.GetTenantBillingStatus(context.Background(), &purserpb.GetTenantBillingStatusRequest{TenantId: "tenant-1"})
		if err != nil {
			t.Fatalf("GetTenantBillingStatus: %v", err)
		}
		if resp.IsBalanceNegative {
			t.Fatalf("postpaid balance must not trip is_balance_negative: %+v", resp)
		}
		if !resp.IsSuspended {
			t.Fatalf("status 'suspended' must map to IsSuspended: %+v", resp)
		}
	})
}

func TestGetTenantBillingStatusEmptyTenantGuard(t *testing.T) {
	s := newGuardServer(t)
	_, err := s.GetTenantBillingStatus(context.Background(), &purserpb.GetTenantBillingStatusRequest{})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("err = %v, want InvalidArgument", err)
	}
}

func TestGetPrepaidBalanceMapsAndComputesLowBalance(t *testing.T) {
	s, mock := newReadServer(t, true)
	now := time.Now()

	mock.ExpectQuery(`FROM purser\.prepaid_balances\s+WHERE tenant_id = \$1 AND currency = \$2`).
		WithArgs("tenant-1", "EUR").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "tenant_id", "balance_cents", "currency", "low_balance_threshold_cents", "created_at", "updated_at",
		}).AddRow("bal-1", "tenant-1", int64(100), "EUR", int64(500), now, now))
	// drain-rate aggregation over last hour
	mock.ExpectQuery(`SELECT COALESCE\(SUM\(ABS\(amount_cents\)\), 0\)`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"drain"}).AddRow(int64(250)))

	resp, err := s.GetPrepaidBalance(context.Background(), &purserpb.GetPrepaidBalanceRequest{TenantId: "tenant-1", Currency: "EUR"})
	if err != nil {
		t.Fatalf("GetPrepaidBalance: %v", err)
	}
	// 100 < 500 threshold → low balance.
	if !resp.IsLowBalance {
		t.Fatalf("IsLowBalance = false, want true (100 < 500)")
	}
	if resp.DrainRateCentsPerHour != 250 {
		t.Fatalf("DrainRateCentsPerHour = %d, want 250", resp.DrainRateCentsPerHour)
	}
}

func TestGetPrepaidBalanceNotFound(t *testing.T) {
	s, mock := newReadServer(t, true)
	mock.ExpectQuery(`FROM purser\.prepaid_balances`).
		WillReturnError(sqlmockNoRows())
	_, err := s.GetPrepaidBalance(context.Background(), &purserpb.GetPrepaidBalanceRequest{TenantId: "tenant-1"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("err = %v, want NotFound", err)
	}
}

func TestGetPrepaidBalanceEmptyTenantGuard(t *testing.T) {
	s := newGuardServer(t)
	_, err := s.GetPrepaidBalance(context.Background(), &purserpb.GetPrepaidBalanceRequest{})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("err = %v, want InvalidArgument", err)
	}
}

func TestGetPendingTopupByIDAndNotFound(t *testing.T) {
	topupCols := []string{
		"id", "tenant_id", "provider", "checkout_id", "amount_cents", "currency",
		"status", "expires_at", "completed_at", "balance_transaction_id", "created_at", "updated_at",
	}

	t.Run("by id maps row", func(t *testing.T) {
		s, mock := newReadServer(t, true)
		now := time.Now()
		mock.ExpectQuery(`FROM purser\.pending_topups WHERE id = \$1`).
			WithArgs("tu-1").
			WillReturnRows(sqlmock.NewRows(topupCols).
				AddRow("tu-1", "tenant-1", "stripe", "cs_1", int64(2000), "EUR", "pending", now, nil, nil, now, now))

		resp, err := s.GetPendingTopup(context.Background(), &purserpb.GetPendingTopupRequest{Lookup: &purserpb.GetPendingTopupRequest_TopupId{TopupId: "tu-1"}})
		if err != nil {
			t.Fatalf("GetPendingTopup: %v", err)
		}
		if resp.Id != "tu-1" || resp.AmountCents != 2000 {
			t.Fatalf("mapping wrong: %+v", resp)
		}
		if resp.CompletedAt != nil || resp.BalanceTransactionId != nil {
			t.Fatalf("NULL fields should stay unset: %+v", resp)
		}
	})

	t.Run("not found", func(t *testing.T) {
		s, mock := newReadServer(t, true)
		mock.ExpectQuery(`FROM purser\.pending_topups WHERE provider = \$1 AND checkout_id = \$2`).
			WithArgs("stripe", "cs_x").
			WillReturnError(sqlmockNoRows())
		_, err := s.GetPendingTopup(context.Background(), &purserpb.GetPendingTopupRequest{Provider: "stripe", Lookup: &purserpb.GetPendingTopupRequest_CheckoutId{CheckoutId: "cs_x"}})
		if status.Code(err) != codes.NotFound {
			t.Fatalf("err = %v, want NotFound", err)
		}
	})

	t.Run("missing selector guard", func(t *testing.T) {
		s := newGuardServer(t)
		_, err := s.GetPendingTopup(context.Background(), &purserpb.GetPendingTopupRequest{})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("err = %v, want InvalidArgument", err)
		}
	})
}

// GetCryptoTopup flips a still-pending row past its expiry to "expired" for the
// client even before the sweep updates the DB, and maps the asset symbol to the
// proto enum.
func TestGetCryptoTopupExpiryFlipAndAssetEnum(t *testing.T) {
	cryptoCols := []string{
		"id", "tenant_id", "wallet_address", "asset", "expected_amount_cents",
		"status", "tx_hash", "confirmations", "received_amount_base_units", "credited_amount_cents",
		"expires_at", "detected_at", "completed_at", "created_at",
		"credited_amount_currency", "quote_source", "network",
	}

	t.Run("pending past expiry reads expired", func(t *testing.T) {
		s, mock := newReadServer(t, true)
		past := time.Now().Add(-1 * time.Hour)
		mock.ExpectQuery(`FROM purser\.crypto_wallets WHERE id = \$1 AND purpose = 'prepaid'`).
			WithArgs("ct-1").
			WillReturnRows(sqlmock.NewRows(cryptoCols).
				AddRow("ct-1", "tenant-1", "0xabc", "ETH", int64(1000),
					"pending", nil, nil, nil, nil,
					past, nil, nil, time.Now(),
					nil, nil, nil))

		resp, err := s.GetCryptoTopup(context.Background(), &purserpb.GetCryptoTopupRequest{TopupId: "ct-1"})
		if err != nil {
			t.Fatalf("GetCryptoTopup: %v", err)
		}
		if resp.Status != "expired" {
			t.Fatalf("Status = %q, want expired (pending past expiry)", resp.Status)
		}
		if resp.Asset != purserpb.CryptoAsset_CRYPTO_ASSET_ETH {
			t.Fatalf("Asset = %v, want ETH", resp.Asset)
		}
	})

	t.Run("empty id guard", func(t *testing.T) {
		s := newGuardServer(t)
		_, err := s.GetCryptoTopup(context.Background(), &purserpb.GetCryptoTopupRequest{})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("err = %v, want InvalidArgument", err)
		}
	})

	t.Run("not found", func(t *testing.T) {
		s, mock := newReadServer(t, true)
		mock.ExpectQuery(`FROM purser\.crypto_wallets`).
			WillReturnError(sqlmockNoRows())
		_, err := s.GetCryptoTopup(context.Background(), &purserpb.GetCryptoTopupRequest{TopupId: "ct-x"})
		if status.Code(err) != codes.NotFound {
			t.Fatalf("err = %v, want NotFound", err)
		}
	})
}

// GetOperatorPayouts is a per-operator ledger read; a service call with an
// explicit tenant resolves directly, an empty tenant on a service call is
// rejected.
func TestGetOperatorPayoutsMapsRows(t *testing.T) {
	s, mock := newReadServer(t, true)
	now := time.Now()
	mock.ExpectQuery(`FROM purser\.operator_payouts`).
		WithArgs("op-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "currency", "total_cents", "status", "method", "external_reference", "created_at", "paid_at",
		}).
			AddRow("po-1", "EUR", int64(5000), "paid", "sepa", "ref-9", now, now).
			AddRow("po-2", "EUR", int64(1200), "pending", "", "", now, nil))

	resp, err := s.GetOperatorPayouts(context.Background(), &purserpb.GetOperatorPayoutsRequest{TenantId: "op-1"})
	if err != nil {
		t.Fatalf("GetOperatorPayouts: %v", err)
	}
	if len(resp.Payouts) != 2 {
		t.Fatalf("got %d payouts, want 2", len(resp.Payouts))
	}
	if resp.Payouts[0].TotalCents != 5000 || resp.Payouts[0].PaidAt == nil {
		t.Fatalf("paid payout mapping wrong: %+v", resp.Payouts[0])
	}
	if resp.Payouts[1].PaidAt != nil {
		t.Fatalf("pending payout PaidAt should be unset: %+v", resp.Payouts[1])
	}
}

func TestGetOperatorPayoutsEmptyTenantGuard(t *testing.T) {
	s := newGuardServer(t)
	// service call (context.Background) with no tenant_id → InvalidArgument
	_, err := s.GetOperatorPayouts(context.Background(), &purserpb.GetOperatorPayoutsRequest{})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("err = %v, want InvalidArgument", err)
	}
}

// CheckClusterAccess denies when the tenant's tier level is below the cluster's
// required level, surfacing both levels and a reason — the marketplace access
// gate.
func TestCheckClusterAccessTierDenied(t *testing.T) {
	s, mock := newReadServer(t, true)
	now := time.Now()

	mock.ExpectQuery(`SELECT COALESCE\(bt\.tier_level, 0\)`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"tier_level"}).AddRow(int32(1)))
	// GetClusterPricing read — required_tier_level 5 outranks the tenant's 1.
	mock.ExpectQuery(`FROM purser\.cluster_pricing\s+WHERE cluster_id = \$1`).
		WithArgs("cluster-a").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "cluster_id", "pricing_model", "stripe_product_id", "stripe_price_id_monthly",
			"stripe_meter_event_name", "base_price", "currency", "metered_rates",
			"required_tier_level", "allow_free_tier", "default_quotas", "created_at", "updated_at",
		}).AddRow("cp-1", "cluster-a", "monthly", nil, nil, nil, "49.00", "EUR", nil,
			int32(5), false, nil, now, now))

	resp, err := s.CheckClusterAccess(context.Background(), &purserpb.CheckClusterAccessRequest{TenantId: "tenant-1", ClusterId: "cluster-a"})
	if err != nil {
		t.Fatalf("CheckClusterAccess: %v", err)
	}
	if resp.Allowed {
		t.Fatalf("Allowed = true, want false (tier 1 < required 5)")
	}
	if resp.TenantTierLevel != 1 || resp.RequiredTierLevel != 5 {
		t.Fatalf("levels wrong: %+v", resp)
	}
}

func TestCheckClusterAccessEmptyInputGuard(t *testing.T) {
	s := newGuardServer(t)
	_, err := s.CheckClusterAccess(context.Background(), &purserpb.CheckClusterAccessRequest{TenantId: "tenant-1"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("err = %v, want InvalidArgument", err)
	}
}
