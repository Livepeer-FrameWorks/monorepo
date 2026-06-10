package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
)

// SetClusterPricing upserts a cluster_pricing row (validating the model and
// metered rates at write time) and echoes the stored config via GetClusterPricing.
func TestSetClusterPricingUpsertsAndRereads(t *testing.T) {
	s, mock := newReadServer(t, true)
	now := time.Now()

	// existing-pricing probe → none yet
	mock.ExpectQuery(`SELECT pricing_model, metered_rates::text\s+FROM purser\.cluster_pricing`).
		WithArgs("cluster-a").
		WillReturnError(sqlmockNoRows())
	// upsert RETURNING id
	mock.ExpectQuery(`INSERT INTO purser\.cluster_pricing`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("cp-1"))
	// trailing GetClusterPricing read
	mock.ExpectQuery(`FROM purser\.cluster_pricing\s+WHERE cluster_id = \$1`).
		WithArgs("cluster-a").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "cluster_id", "pricing_model", "stripe_product_id", "stripe_price_id_monthly",
			"stripe_meter_event_name", "base_price", "currency", "metered_rates",
			"required_tier_level", "allow_free_tier", "default_quotas", "created_at", "updated_at",
		}).AddRow("cp-1", "cluster-a", "free_unmetered", nil, nil, nil, nil, "EUR", nil,
			int32(0), true, nil, now, now))

	resp, err := s.SetClusterPricing(context.Background(), &purserpb.SetClusterPricingRequest{
		ClusterId:    "cluster-a",
		PricingModel: "free_unmetered",
	})
	if err != nil {
		t.Fatalf("SetClusterPricing: %v", err)
	}
	if resp.PricingModel != "free_unmetered" || resp.ClusterId != "cluster-a" {
		t.Fatalf("echoed pricing wrong: %+v", resp)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}

func TestSetClusterPricingGuards(t *testing.T) {
	t.Run("empty cluster", func(t *testing.T) {
		s := newGuardServer(t)
		_, err := s.SetClusterPricing(context.Background(), &purserpb.SetClusterPricingRequest{})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("err = %v, want InvalidArgument", err)
		}
	})
	t.Run("invalid pricing model rejected before DB", func(t *testing.T) {
		s := newGuardServer(t)
		_, err := s.SetClusterPricing(context.Background(), &purserpb.SetClusterPricingRequest{
			ClusterId:    "cluster-a",
			PricingModel: "bogus_model",
		})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("err = %v, want InvalidArgument", err)
		}
	})
}

// AdjustBalance records a non-reference adjustment: ensure the balance row,
// mutate it, and write the ledger entry — all in one tx. A negative adjustment
// is type "adjustment" and skips the top-up reactivation path entirely.
func TestAdjustBalanceNegativeAdjustment(t *testing.T) {
	s, mock := newReadServer(t, true)

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO purser\.prepaid_balances`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`UPDATE purser\.prepaid_balances\s+SET balance_cents = balance_cents \+ \$1`).
		WillReturnRows(sqlmock.NewRows([]string{"balance_cents"}).AddRow(int64(900)))
	mock.ExpectExec(`INSERT INTO purser\.balance_transactions`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	resp, err := s.AdjustBalance(context.Background(), &purserpb.AdjustBalanceRequest{
		TenantId:    "tenant-1",
		AmountCents: -100,
		Description: "manual correction",
	})
	if err != nil {
		t.Fatalf("AdjustBalance: %v", err)
	}
	if resp.AmountCents != -100 || resp.BalanceAfterCents != 900 {
		t.Fatalf("amounts wrong: %+v", resp)
	}
	if resp.TransactionType != "adjustment" {
		t.Fatalf("TransactionType = %q, want adjustment", resp.TransactionType)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}

// A positive adjustment is a "refund" and, on success, checks for a suspended
// subscription to reactivate. With no suspended row (0 rows affected) the
// reactivation is a no-op and no cache-invalidation fan-out fires.
func TestAdjustBalancePositiveRefundChecksReactivation(t *testing.T) {
	s, mock := newReadServer(t, true)

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO purser\.prepaid_balances`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`UPDATE purser\.prepaid_balances`).
		WillReturnRows(sqlmock.NewRows([]string{"balance_cents"}).AddRow(int64(200)))
	mock.ExpectExec(`INSERT INTO purser\.balance_transactions`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	// post-commit reactivation probe — no suspended row
	mock.ExpectExec(`UPDATE purser\.tenant_subscriptions\s+SET status = 'active'`).
		WithArgs("tenant-1").
		WillReturnResult(sqlmock.NewResult(0, 0))

	resp, err := s.AdjustBalance(context.Background(), &purserpb.AdjustBalanceRequest{
		TenantId:    "tenant-1",
		AmountCents: 200,
		Description: "goodwill credit",
	})
	if err != nil {
		t.Fatalf("AdjustBalance: %v", err)
	}
	if resp.TransactionType != "refund" {
		t.Fatalf("TransactionType = %q, want refund", resp.TransactionType)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}

// No prepaid balance row to update → NotFound, and the tx rolls back.
func TestAdjustBalanceNoBalanceRow(t *testing.T) {
	s, mock := newReadServer(t, true)
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO purser\.prepaid_balances`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`UPDATE purser\.prepaid_balances`).
		WillReturnError(sqlmockNoRows())
	mock.ExpectRollback()

	_, err := s.AdjustBalance(context.Background(), &purserpb.AdjustBalanceRequest{
		TenantId:    "tenant-1",
		AmountCents: -100,
		Description: "x",
	})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("err = %v, want NotFound", err)
	}
}

func TestAdjustBalanceInputGuards(t *testing.T) {
	s := newGuardServer(t)
	if _, err := s.AdjustBalance(context.Background(), &purserpb.AdjustBalanceRequest{Description: "x"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("empty tenant: err = %v, want InvalidArgument", err)
	}
	if _, err := s.AdjustBalance(context.Background(), &purserpb.AdjustBalanceRequest{TenantId: "t1"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("empty description: err = %v, want InvalidArgument", err)
	}
}

// requireMarketplaceOwnerApproved gates third-party cluster access: only an
// 'approved' AND payout-eligible operator passes. Missing row or any other
// state is a FailedPrecondition.
func TestRequireMarketplaceOwnerApproved(t *testing.T) {
	owner := uuid.New()

	t.Run("approved and payout-eligible passes", func(t *testing.T) {
		s, mock := newReadServer(t, true)
		mock.ExpectQuery(`FROM purser\.cluster_owners\s+WHERE tenant_id = \$1`).
			WillReturnRows(sqlmock.NewRows([]string{"status", "payout_eligible"}).AddRow("approved", true))
		if err := s.requireMarketplaceOwnerApproved(context.Background(), owner); err != nil {
			t.Fatalf("expected approval, got %v", err)
		}
	})

	t.Run("unknown operator rejected", func(t *testing.T) {
		s, mock := newReadServer(t, true)
		mock.ExpectQuery(`FROM purser\.cluster_owners`).
			WillReturnError(sqlmockNoRows())
		if err := s.requireMarketplaceOwnerApproved(context.Background(), owner); status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("err = %v, want FailedPrecondition", err)
		}
	})

	t.Run("approved but not payout eligible rejected", func(t *testing.T) {
		s, mock := newReadServer(t, true)
		mock.ExpectQuery(`FROM purser\.cluster_owners`).
			WillReturnRows(sqlmock.NewRows([]string{"status", "payout_eligible"}).AddRow("approved", false))
		if err := s.requireMarketplaceOwnerApproved(context.Background(), owner); status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("err = %v, want FailedPrecondition", err)
		}
	})
}

// When the tenant clears the tier bar but the cluster needs commercial
// classification, an unconfigured Quartermaster client makes the cluster
// unclassifiable → access is denied (fail-closed), not granted.
func TestCheckClusterAccessClassifyUnavailableDenies(t *testing.T) {
	s, mock := newReadServer(t, true)
	now := time.Now()

	mock.ExpectQuery(`SELECT COALESCE\(bt\.tier_level, 0\)`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"tier_level"}).AddRow(int32(5)))
	mock.ExpectQuery(`FROM purser\.cluster_pricing\s+WHERE cluster_id = \$1`).
		WithArgs("cluster-a").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "cluster_id", "pricing_model", "stripe_product_id", "stripe_price_id_monthly",
			"stripe_meter_event_name", "base_price", "currency", "metered_rates",
			"required_tier_level", "allow_free_tier", "default_quotas", "created_at", "updated_at",
		}).AddRow("cp-1", "cluster-a", "metered", nil, nil, nil, nil, "EUR", nil,
			int32(1), true, nil, now, now))

	resp, err := s.CheckClusterAccess(context.Background(), &purserpb.CheckClusterAccessRequest{TenantId: "tenant-1", ClusterId: "cluster-a"})
	if err != nil {
		t.Fatalf("CheckClusterAccess: %v", err)
	}
	if resp.Allowed {
		t.Fatalf("Allowed = true, want false (cluster unclassifiable → fail-closed)")
	}
}
