package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
)

const (
	currentTierID  = "11111111-1111-1111-1111-111111111111"
	freeTierID     = "22222222-2222-2222-2222-222222222222"
	paygTierID     = "33333333-3333-3333-3333-333333333333"
	prodTierID     = "44444444-4444-4444-4444-444444444444"
	inactiveTierID = "55555555-5555-5555-5555-555555555555"
	tenantID       = "tenant-aaa"
)

// expectLoadSubscription stubs the SELECT for the current subscription row.
func expectLoadSubscription(mock sqlmock.Sqlmock, tenantID, tierID string, tierLevel int32, billingModel string, periodStart, periodEnd time.Time) {
	cols := []string{"tier_id", "tier_level", "billing_model", "billing_period_start", "billing_period_end", "stripe_current_period_end"}
	rows := sqlmock.NewRows(cols).AddRow(tierID, tierLevel, billingModel, periodStart, periodEnd, nil)
	mock.ExpectQuery(`SELECT ts\.tier_id, bt\.tier_level, ts\.billing_model,\s+ts\.billing_period_start, ts\.billing_period_end, ts\.stripe_current_period_end`).
		WithArgs(tenantID).
		WillReturnRows(rows)
}

// expectLoadTargetTier stubs the SELECT against billing_tiers for the target.
func expectLoadTargetTier(mock sqlmock.Sqlmock, tierID string, tierLevel int32, isDefaultPrepaid, isActive bool) {
	cols := []string{"tier_level", "tier_name", "is_default_prepaid", "is_active"}
	rows := sqlmock.NewRows(cols).AddRow(tierLevel, "target-tier", isDefaultPrepaid, isActive)
	mock.ExpectQuery(`SELECT tier_level, tier_name, is_default_prepaid, is_active\s+FROM purser\.billing_tiers`).
		WithArgs(tierID).
		WillReturnRows(rows)
}

func TestChangeBillingTier_RejectsPrepaid(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	server := &PurserServer{db: db, logger: logging.NewLogger()}

	now := time.Now()
	expectLoadSubscription(mock, tenantID, currentTierID, 0, "prepaid", now, now.Add(time.Hour))

	resp, err := server.ChangeBillingTier(context.Background(), &purserpb.ChangeBillingTierRequest{
		TenantId: tenantID,
		TierId:   prodTierID,
	})
	if err == nil {
		t.Fatalf("expected error for prepaid tenant, got resp=%+v", resp)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

func TestChangeBillingTier_RejectsPrepaidDefaultTier(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	server := &PurserServer{db: db, logger: logging.NewLogger()}

	now := time.Now()
	expectLoadSubscription(mock, tenantID, currentTierID, 4, "postpaid", now, now.Add(time.Hour))
	expectLoadTargetTier(mock, paygTierID, 0, true, true) // is_default_prepaid=true

	_, err = server.ChangeBillingTier(context.Background(), &purserpb.ChangeBillingTierRequest{
		TenantId: tenantID,
		TierId:   paygTierID,
	})
	if err == nil {
		t.Fatalf("expected error for prepaid-default target, got nil")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

func TestChangeBillingTier_RejectsInactiveTarget(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	server := &PurserServer{db: db, logger: logging.NewLogger()}

	now := time.Now()
	expectLoadSubscription(mock, tenantID, currentTierID, 4, "postpaid", now, now.Add(time.Hour))
	expectLoadTargetTier(mock, inactiveTierID, 3, false, false)

	_, err = server.ChangeBillingTier(context.Background(), &purserpb.ChangeBillingTierRequest{
		TenantId: tenantID,
		TierId:   inactiveTierID,
	})
	if err == nil {
		t.Fatalf("expected error for inactive target, got nil")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

func TestChangeBillingTier_UpgradePathFlipsImmediately(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	// No tierReconciler / commodoreClient configured — both are nil-safe in
	// the upgrade path.
	server := &PurserServer{db: db, logger: logging.NewLogger()}

	// Current: free (level 1); Target: production (level 4) → upgrade.
	periodStart := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := periodStart.AddDate(0, 1, 0)
	expectLoadSubscription(mock, tenantID, freeTierID, 1, "postpaid", periodStart, periodEnd)
	expectLoadTargetTier(mock, prodTierID, 4, false, true)
	mock.ExpectExec(`UPDATE purser\.tenant_subscriptions\s+SET tier_id = \$1`).
		WithArgs(prodTierID, tenantID, periodStart, periodEnd).
		WillReturnResult(sqlmock.NewResult(0, 1))

	resp, err := server.ChangeBillingTier(context.Background(), &purserpb.ChangeBillingTierRequest{
		TenantId: tenantID,
		TierId:   prodTierID,
	})
	if err != nil {
		t.Fatalf("ChangeBillingTier: %v", err)
	}
	if resp.GetAppliedTierId() != prodTierID {
		t.Errorf("AppliedTierId = %q, want %q", resp.GetAppliedTierId(), prodTierID)
	}
	if resp.GetPendingTierId() != "" {
		t.Errorf("PendingTierId = %q, want empty", resp.GetPendingTierId())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

func TestChangeBillingTier_DowngradePathStagesPending(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	server := &PurserServer{db: db, logger: logging.NewLogger()}

	periodEnd := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	periodStart := periodEnd.AddDate(0, -1, 0)
	// Current: production (4); Target: free (1) → downgrade.
	expectLoadSubscription(mock, tenantID, prodTierID, 4, "postpaid", periodStart, periodEnd)
	expectLoadTargetTier(mock, freeTierID, 1, false, true)
	mock.ExpectExec(`UPDATE purser\.tenant_subscriptions\s+SET pending_tier_id = \$1`).
		WithArgs(freeTierID, periodEnd, tenantID, periodStart, periodEnd).
		WillReturnResult(sqlmock.NewResult(0, 1))

	resp, err := server.ChangeBillingTier(context.Background(), &purserpb.ChangeBillingTierRequest{
		TenantId: tenantID,
		TierId:   freeTierID,
	})
	if err != nil {
		t.Fatalf("ChangeBillingTier: %v", err)
	}
	if resp.GetPendingTierId() != freeTierID {
		t.Errorf("PendingTierId = %q, want %q", resp.GetPendingTierId(), freeTierID)
	}
	if resp.GetAppliedTierId() != "" {
		t.Errorf("AppliedTierId = %q, want empty", resp.GetAppliedTierId())
	}
	if !resp.GetEffectiveAt().AsTime().Equal(periodEnd) {
		t.Errorf("EffectiveAt = %v, want %v", resp.GetEffectiveAt().AsTime(), periodEnd)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

func TestChangeBillingTier_DowngradeWithoutSubscriptionPeriodUsesOpenDraftPeriod(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	server := &PurserServer{db: db, logger: logging.NewLogger()}

	periodStart := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := periodStart.AddDate(0, 1, 0)
	rows := sqlmock.NewRows([]string{"tier_id", "tier_level", "billing_model", "billing_period_start", "billing_period_end", "stripe_current_period_end"}).
		AddRow(prodTierID, int32(4), "postpaid", nil, nil, nil)
	mock.ExpectQuery(`SELECT ts\.tier_id, bt\.tier_level, ts\.billing_model,\s+ts\.billing_period_start, ts\.billing_period_end, ts\.stripe_current_period_end`).
		WithArgs(tenantID).
		WillReturnRows(rows)
	expectLoadTargetTier(mock, freeTierID, 1, false, true)
	mock.ExpectQuery(`SELECT period_start, period_end\s+FROM purser\.billing_invoices`).
		WithArgs(tenantID, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"period_start", "period_end"}).AddRow(periodStart, periodEnd))
	mock.ExpectExec(`UPDATE purser\.tenant_subscriptions\s+SET pending_tier_id = \$1`).
		WithArgs(freeTierID, periodEnd, tenantID, periodStart, periodEnd).
		WillReturnResult(sqlmock.NewResult(0, 1))

	resp, err := server.ChangeBillingTier(context.Background(), &purserpb.ChangeBillingTierRequest{
		TenantId: tenantID,
		TierId:   freeTierID,
	})
	if err != nil {
		t.Fatalf("ChangeBillingTier: %v", err)
	}
	if !resp.GetEffectiveAt().AsTime().Equal(periodEnd) {
		t.Errorf("EffectiveAt = %v, want %v", resp.GetEffectiveAt().AsTime(), periodEnd)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

func TestChangeBillingTier_SameTierNoOp(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	server := &PurserServer{db: db, logger: logging.NewLogger()}

	now := time.Now()
	expectLoadSubscription(mock, tenantID, prodTierID, 4, "postpaid", now, now.Add(time.Hour))
	expectLoadTargetTier(mock, prodTierID, 4, false, true)
	mock.ExpectExec(`UPDATE purser\.tenant_subscriptions\s+SET billing_period_start = COALESCE`).
		WithArgs(tenantID, now, now.Add(time.Hour)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	resp, err := server.ChangeBillingTier(context.Background(), &purserpb.ChangeBillingTierRequest{
		TenantId: tenantID,
		TierId:   prodTierID,
	})
	if err != nil {
		t.Fatalf("ChangeBillingTier: %v", err)
	}
	if resp.GetAppliedTierId() != prodTierID {
		t.Errorf("AppliedTierId = %q, want %q", resp.GetAppliedTierId(), prodTierID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}
