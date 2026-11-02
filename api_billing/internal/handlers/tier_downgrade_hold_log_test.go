package handlers

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

const heldDowngradeLogMessage = "Pending downgrade held by its subscription pricing state; resolve it, then run frameworks admin billing recovery downgrade"

// A downgrade the subscription's pricing state refuses is otherwise silent past
// the alert, so the applier names it at Warn with the recovery command.
func TestApplyPendingDowngradeLogsAPricingHoldAtWarn(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()
	logger, hook := logtest.NewNullLogger()
	reconciler := &stubTierReconciler{}
	jm := &JobManager{db: mockDB, logger: logger, tierReconciler: reconciler, billing: &Service{}}
	tenantID := "tenant-1"
	mock.ExpectQuery(`SELECT subscription\.tier_id::text AS tier_id,\s+COALESCE\(subscription\.pending_tier_id::text`).
		WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"tier_id", "pending_tier_id", "pending_effective_at", "tier_level", "tier_name"}).
			AddRow("tier-A", "tier-B", time.Now().Add(-time.Hour), int32(1), "developer"))
	mock.ExpectBegin()
	mock.ExpectQuery("LockTenantSubscriptionForProviderChange").WithArgs(tenantID).WillReturnRows(sqlmock.NewRows([]string{"subscription_id", "tier_id", "status", "billing_model", "stripe_subscription_id", "mollie_subscription_id", "pending_reason", "free_fallback_provider_stopping", "free_fallback_provider_ended_at"}).AddRow("sub", "tier-A", "active", "postpaid", nil, nil, "downgrade", false, nil))
	mock.ExpectQuery("IsTenantBillingClosed").WillReturnRows(sqlmock.NewRows([]string{"closed"}).AddRow(true))
	mock.ExpectQuery("GetSubscriptionOperatorGrant").WithArgs(tenantID).WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	jm.applyPendingDowngrade(context.Background(), tenantID)

	if got := logMessages(hook, logrus.WarnLevel, heldDowngradeLogMessage); got != 1 {
		t.Fatalf("Warn entries for the held downgrade = %d, want 1; entries: %v", got, hook.AllEntries())
	}
	if reconciler.calls != 0 {
		t.Fatalf("reconciler ran %d times for a refused downgrade", reconciler.calls)
	}
}
