package grpc

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
)

func TestGetTenantAdmissionStatusNoSubscriptionDefault(t *testing.T) {
	s, mock := newReadServer(t, true)
	mock.ExpectQuery(`FROM purser\.tenant_subscriptions ts`).
		WithArgs("EUR", "tenant-1").
		WillReturnError(sqlmockNoRows())

	resp, err := s.GetTenantAdmissionStatus(context.Background(), &purserpb.GetTenantAdmissionStatusRequest{TenantId: "tenant-1"})
	if err != nil {
		t.Fatalf("GetTenantAdmissionStatus: %v", err)
	}
	if resp.BillingModel != "prepaid" || !resp.IsBalanceNegative || resp.AvailableBalanceCents != 0 {
		t.Fatalf("unexpected default: %+v", resp)
	}
}

func TestGetTenantAdmissionStatusMapsBoundedDecision(t *testing.T) {
	s, mock := newReadServer(t, true)
	cols := []string{
		"billing_model", "subscription_status", "balance_cents", "reserved_balance_cents",
		"payment_method", "stripe_subscription_id", "mollie_subscription_id", "tier_name",
	}
	mock.ExpectQuery(`FROM purser\.tenant_subscriptions ts`).
		WithArgs("EUR", "tenant-1").
		WillReturnRows(sqlmock.NewRows(cols).AddRow("prepaid", "active", int64(100), int64(125), nil, nil, nil, "prepaid"))

	resp, err := s.GetTenantAdmissionStatus(context.Background(), &purserpb.GetTenantAdmissionStatusRequest{TenantId: "tenant-1"})
	if err != nil {
		t.Fatalf("GetTenantAdmissionStatus: %v", err)
	}
	if !resp.IsBalanceNegative || resp.BalanceCents != 100 || resp.ReservedBalanceCents != 125 || resp.AvailableBalanceCents != -25 {
		t.Fatalf("unexpected admission decision: %+v", resp)
	}

	postpaid := mapTenantAdmissionStatus(tenantAdmissionData{
		BillingModel: "postpaid", SubscriptionStatus: "suspended", BalanceCents: sql.NullInt64{Int64: -50, Valid: true},
		PaymentMethod: sql.NullString{String: "stripe", Valid: true}, StripeSubscriptionID: sql.NullString{String: "sub_1", Valid: true}, TierName: "pro",
	})
	if postpaid.IsBalanceNegative || !postpaid.IsSuspended || !postpaid.CollectionReady || postpaid.CollectionProvider != "stripe" || postpaid.TierName != "pro" {
		t.Fatalf("unexpected postpaid decision: %+v", postpaid)
	}
}

func TestGetTenantAdmissionStatusEmptyTenantGuard(t *testing.T) {
	s := newGuardServer(t)
	_, err := s.GetTenantAdmissionStatus(context.Background(), &purserpb.GetTenantAdmissionStatusRequest{})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("err = %v, want InvalidArgument", err)
	}
}

// No subscription row must fail closed for rated admission without blocking the
// Gateway's explicit non-rated onboarding/payment surfaces.
func TestGetTenantBillingStatusNoSubscriptionDefault(t *testing.T) {
	s, mock := newReadServer(t, true)
	mock.ExpectQuery(`LEFT JOIN purser\.prepaid_balances pb`).
		WillReturnError(sqlmockNoRows())

	resp, err := s.GetTenantBillingStatus(context.Background(), &purserpb.GetTenantBillingStatusRequest{TenantId: "tenant-1"})
	if err != nil {
		t.Fatalf("GetTenantBillingStatus: %v", err)
	}
	if resp.BillingModel != "prepaid" || resp.IsSuspended || !resp.IsBalanceNegative || resp.BalanceCents != 0 {
		t.Fatalf("unexpected default: %+v", resp)
	}
}

// A prepaid tenant whose balance has gone non-positive must report
// IsBalanceNegative — the gate that drives suspension/throttle downstream.
// is_balance_negative is prepaid-only: a postpaid tenant with the same balance
// must NOT trip it.
func TestGetTenantBillingStatusPrepaidNegativeBalance(t *testing.T) {
	cols := []string{
		"billing_model", "status", "balance_cents", "reserved_balance_cents", "retention", "dvr_entitlements",
		"tier_id", "billing_period_start", "billing_period_end", "storage_limit", "resource_limits",
		"payment_method", "stripe_subscription_id", "mollie_subscription_id", "tier_name",
	}

	t.Run("prepaid non-positive trips negative", func(t *testing.T) {
		s, mock := newReadServer(t, true)
		mock.ExpectQuery(`LEFT JOIN purser\.prepaid_balances pb`).
			WillReturnRows(sqlmock.NewRows(cols).
				AddRow("prepaid", "active", int64(-50), int64(0), "", "", "", nil, nil, "", "", nil, nil, nil, "prepaid"))

		resp, err := s.GetTenantBillingStatus(context.Background(), &purserpb.GetTenantBillingStatusRequest{TenantId: "tenant-1"})
		if err != nil {
			t.Fatalf("GetTenantBillingStatus: %v", err)
		}
		if !resp.IsBalanceNegative || resp.BalanceCents != -50 || resp.BillingModel != "prepaid" {
			t.Fatalf("expected negative prepaid balance: %+v", resp)
		}
	})

	t.Run("active reservation reduces available balance", func(t *testing.T) {
		s, mock := newReadServer(t, true)
		mock.ExpectQuery(`LEFT JOIN purser\.prepaid_balances pb`).
			WillReturnRows(sqlmock.NewRows(cols).
				AddRow("prepaid", "active", int64(100), int64(125), "", "", "", nil, nil, "", "", nil, nil, nil, "prepaid"))

		resp, err := s.GetTenantBillingStatus(context.Background(), &purserpb.GetTenantBillingStatusRequest{TenantId: "tenant-1"})
		if err != nil {
			t.Fatalf("GetTenantBillingStatus: %v", err)
		}
		if !resp.IsBalanceNegative || resp.BalanceCents != 100 || resp.ReservedBalanceCents != 125 || resp.AvailableBalanceCents != -25 {
			t.Fatalf("expected reservation-adjusted prepaid balance: %+v", resp)
		}
	})

	t.Run("postpaid same balance stays non-negative", func(t *testing.T) {
		s, mock := newReadServer(t, true)
		mock.ExpectQuery(`LEFT JOIN purser\.prepaid_balances pb`).
			WillReturnRows(sqlmock.NewRows(cols).
				AddRow("postpaid", "suspended", int64(-50), int64(0), "", "", "", nil, nil, "", "", "stripe", "sub_1", nil, "pro"))

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
		if !resp.CollectionReady || resp.CollectionProvider != "stripe" {
			t.Fatalf("provider-backed postpaid must be collection ready: %+v", resp)
		}
		if resp.TierName != "pro" {
			t.Fatalf("tier identity missing from access status: %+v", resp)
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
	tenantID := "8f9f7d99-9d4c-45f3-b7db-0fd323b08140"
	balanceID := "133668e7-70ce-4798-8378-d08f02a83ba2"

	mock.ExpectQuery(`FROM purser\.prepaid_balances pb.*LEFT JOIN purser\.usage_reservations r`).
		WithArgs(tenantID, "EUR").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "tenant_id", "balance_cents", "currency", "low_balance_threshold_cents", "created_at", "updated_at", "reserved_balance_cents",
		}).AddRow(balanceID, tenantID, int64(100), "EUR", int64(500), now, now, int64(25)))
	// drain-rate aggregation over last hour
	mock.ExpectQuery(`SELECT COALESCE\(SUM\(ABS\(amount_cents\)\), 0\)`).
		WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"drain"}).AddRow(int64(250)))

	resp, err := s.GetPrepaidBalance(context.Background(), &purserpb.GetPrepaidBalanceRequest{TenantId: tenantID, Currency: "EUR"})
	if err != nil {
		t.Fatalf("GetPrepaidBalance: %v", err)
	}
	// 100 < 500 threshold → low balance.
	if !resp.IsLowBalance {
		t.Fatalf("IsLowBalance = false, want true (75 < 500)")
	}
	if resp.AvailableBalanceCents != 75 || resp.ReservedBalanceCents != 25 {
		t.Fatalf("unexpected reservation-adjusted balance: %+v", resp)
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
		topupID := "15d7af64-2138-4c55-b41d-88f0cfbcbb2f"
		tenantID := "25217e2a-0cb3-48a5-a499-0a55dc02eb93"
		mock.ExpectQuery(`FROM purser\.pending_topups\s+WHERE id = \$1`).
			WithArgs(topupID).
			WillReturnRows(sqlmock.NewRows(topupCols).
				AddRow(topupID, tenantID, "stripe", "cs_1", int64(2000), "EUR", "pending", now, nil, nil, now, now))

		resp, err := s.GetPendingTopup(context.Background(), &purserpb.GetPendingTopupRequest{Lookup: &purserpb.GetPendingTopupRequest_TopupId{TopupId: topupID}})
		if err != nil {
			t.Fatalf("GetPendingTopup: %v", err)
		}
		if resp.Id != topupID || resp.AmountCents != 2000 {
			t.Fatalf("mapping wrong: %+v", resp)
		}
		if resp.CompletedAt != nil || resp.BalanceTransactionId != nil {
			t.Fatalf("NULL fields should stay unset: %+v", resp)
		}
	})

	t.Run("not found", func(t *testing.T) {
		s, mock := newReadServer(t, true)
		mock.ExpectQuery(`FROM purser\.pending_topups\s+WHERE provider = \$1 AND checkout_id = \$2`).
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
		mock.ExpectQuery(`FROM purser\.crypto_wallets\s+WHERE id = \$1::text::uuid`).
			WithArgs("ct-1", false, "").
			WillReturnRows(sqlmock.NewRows(cryptoCols).
				AddRow("ct-1", "tenant-1", "0xabc", "ETH", int64(1000),
					"pending", nil, int32(0), "", nil,
					past, nil, nil, time.Now(),
					nil, nil, ""))

		resp, err := s.GetCryptoTopup(serviceTestContext(), &purserpb.GetCryptoTopupRequest{TopupId: "ct-1"})
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
		_, err := s.GetCryptoTopup(serviceTestContext(), &purserpb.GetCryptoTopupRequest{})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("err = %v, want InvalidArgument", err)
		}
	})

	t.Run("not found", func(t *testing.T) {
		s, mock := newReadServer(t, true)
		mock.ExpectQuery(`FROM purser\.crypto_wallets`).
			WillReturnError(sqlmockNoRows())
		_, err := s.GetCryptoTopup(serviceTestContext(), &purserpb.GetCryptoTopupRequest{TopupId: "ct-x"})
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
	firstPayoutID := "be7333fc-35bd-4b93-8378-e0fcf5945f46"
	secondPayoutID := "0d4680f8-4029-42ff-95e0-d1797b2daf7e"
	mock.ExpectQuery(`FROM purser\.operator_payouts`).
		WithArgs("op-1", false, time.Time{}, false, time.Time{}).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "currency", "total_cents", "status", "method", "external_reference", "created_at", "paid_at",
		}).
			AddRow(firstPayoutID, "EUR", int64(5000), "paid", "sepa", "ref-9", now, now).
			AddRow(secondPayoutID, "EUR", int64(1200), "pending", "", "", now, nil))

	resp, err := s.GetOperatorPayouts(serviceTestContext(), &purserpb.GetOperatorPayoutsRequest{TenantId: "op-1"})
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
	// Explicit service call with no tenant_id → InvalidArgument.
	_, err := s.GetOperatorPayouts(serviceTestContext(), &purserpb.GetOperatorPayoutsRequest{})
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

	mock.ExpectQuery(`SELECT COALESCE\(tier\.tier_level, 0\)::int AS tier_level`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"tier_level"}).AddRow(int32(1)))
	// GetClusterPricing read — required_tier_level 5 outranks the tenant's 1.
	mock.ExpectQuery(`FROM purser\.cluster_pricing\s+WHERE cluster_id = \$1`).
		WithArgs("cluster-a").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "cluster_id", "pricing_model", "stripe_product_id", "stripe_price_id_monthly",
			"stripe_meter_event_name", "base_price", "currency", "metered_rates",
			"required_tier_level", "allow_free_tier", "default_quotas", "created_at", "updated_at",
		}).AddRow("cp-1", "cluster-a", "monthly", nil, nil, nil, "49.00", "EUR", []byte(`{}`),
			int32(5), false, []byte(`{}`), now, now))

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
