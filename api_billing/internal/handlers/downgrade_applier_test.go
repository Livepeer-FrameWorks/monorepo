package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

type stubTierReconciler struct {
	calls    int
	tenantID string
	level    int32
	err      error
}

func (s *stubTierReconciler) Reconcile(_ context.Context, tenantID string, level int32) ([]string, string, error) {
	s.calls++
	s.tenantID = tenantID
	s.level = level
	return nil, "", s.err
}

func TestApplyPendingDowngrade_NotDue_NoOp(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()

	reconciler := &stubTierReconciler{}
	jm := &JobManager{db: mockDB, logger: logging.NewLogger(), tierReconciler: reconciler}

	tenantID := "tenant-1"
	currentTier := "tier-A"
	pending := "tier-B"
	future := time.Now().Add(48 * time.Hour)
	rows := sqlmock.NewRows([]string{"tier_id", "pending_tier_id", "pending_effective_at", "tier_level"}).
		AddRow(currentTier, pending, future, int32(1))
	mock.ExpectQuery(`SELECT ts\.tier_id,\s+ts\.pending_tier_id`).
		WithArgs(tenantID).
		WillReturnRows(rows)

	jm.applyPendingDowngrade(context.Background(), tenantID)

	if reconciler.calls != 0 {
		t.Errorf("reconciler called %d times for not-due downgrade; want 0", reconciler.calls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

func TestApplyPendingDowngrade_NoPending_NoOp(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()

	reconciler := &stubTierReconciler{}
	jm := &JobManager{db: mockDB, logger: logging.NewLogger(), tierReconciler: reconciler}

	tenantID := "tenant-1"
	rows := sqlmock.NewRows([]string{"tier_id", "pending_tier_id", "pending_effective_at", "tier_level"}).
		AddRow("tier-A", nil, nil, nil)
	mock.ExpectQuery(`SELECT ts\.tier_id,\s+ts\.pending_tier_id`).
		WithArgs(tenantID).
		WillReturnRows(rows)

	jm.applyPendingDowngrade(context.Background(), tenantID)

	if reconciler.calls != 0 {
		t.Errorf("reconciler called %d times for tenant with no pending; want 0", reconciler.calls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

func TestApplyPendingDowngrade_Happy_FlipsThenReconcilesThenClears(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()

	reconciler := &stubTierReconciler{}
	jm := &JobManager{db: mockDB, logger: logging.NewLogger(), tierReconciler: reconciler}

	tenantID := "tenant-1"
	pending := "tier-B"
	past := time.Now().Add(-time.Hour)

	rows := sqlmock.NewRows([]string{"tier_id", "pending_tier_id", "pending_effective_at", "tier_level"}).
		AddRow("tier-A", pending, past, int32(1))
	mock.ExpectQuery(`SELECT ts\.tier_id,\s+ts\.pending_tier_id`).
		WithArgs(tenantID).
		WillReturnRows(rows)
	// Step 1: flip tier_id.
	mock.ExpectExec(`UPDATE purser\.tenant_subscriptions\s+SET tier_id = \$1`).
		WithArgs(pending, tenantID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	// Step 3: clear pending_* (Step 2 is the reconciler stub call).
	mock.ExpectExec(`UPDATE purser\.tenant_subscriptions\s+SET pending_tier_id = NULL`).
		WithArgs(tenantID, pending).
		WillReturnResult(sqlmock.NewResult(0, 1))

	jm.applyPendingDowngrade(context.Background(), tenantID)

	if reconciler.calls != 1 {
		t.Errorf("reconciler called %d times; want 1", reconciler.calls)
	}
	if reconciler.tenantID != tenantID || reconciler.level != 1 {
		t.Errorf("reconciler called with tenant=%q level=%d; want tenant=%q level=1", reconciler.tenantID, reconciler.level, tenantID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

func TestApplyPendingDowngrade_ReconcileFailLeavesPending(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()

	reconciler := &stubTierReconciler{err: errStubReconcile}
	jm := &JobManager{db: mockDB, logger: logging.NewLogger(), tierReconciler: reconciler}

	tenantID := "tenant-1"
	pending := "tier-B"
	past := time.Now().Add(-time.Hour)

	rows := sqlmock.NewRows([]string{"tier_id", "pending_tier_id", "pending_effective_at", "tier_level"}).
		AddRow("tier-A", pending, past, int32(1))
	mock.ExpectQuery(`SELECT ts\.tier_id,\s+ts\.pending_tier_id`).
		WithArgs(tenantID).
		WillReturnRows(rows)
	// Step 1 flip still happens — favors-user ordering.
	mock.ExpectExec(`UPDATE purser\.tenant_subscriptions\s+SET tier_id = \$1`).
		WithArgs(pending, tenantID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	// Step 3 must NOT run when reconcile fails — pending_* stays set so the
	// next cron tick retries.

	jm.applyPendingDowngrade(context.Background(), tenantID)

	if reconciler.calls != 1 {
		t.Errorf("reconciler called %d times; want 1", reconciler.calls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

var errStubReconcile = stubErr("stub reconcile failure")

type stubErr string

func (e stubErr) Error() string { return string(e) }
