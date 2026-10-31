package handlers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"time"

	"frameworks/api_billing/internal/database/purserdb"

	"github.com/DATA-DOG/go-sqlmock"
)

func checkoutReservationTestRows(values ...driver.Value) *sqlmock.Rows {
	cols := []string{"id", "tenant_id", "provider", "purpose", "provider_customer_id", "provider_session_id", "provider_payment_id", "provider_subscription_id", "status", "expires_at", "attempt_count", "checkout_completed_at", "recovery_started_at", "created_at", "amount_cents", "currency", "provider_deadline", "recovery_outcome", "provider_request", "submission_state", "succeeded_at", "activated_at", "funding_intent_id", "provider_mandate_id", "compensation_current"}
	values = append(values, nil, nil, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), int64(0), "EUR", values[9], nil, nil, "unknown", nil, nil, "", nil, false)
	return sqlmock.NewRows(cols).AddRow(values...)
}

func pendingProviderCheckoutForTest(ctx context.Context, db *sql.DB, tenantID string) (bool, error) {
	row, err := purserdb.New(db).GetPendingProviderCheckout(ctx, purserdb.GetPendingProviderCheckoutParams{TenantID: tenantID})
	return row.Pending, err
}

func expectCheckoutRecoverySessionLock(mock sqlmock.Sqlmock) {
	mock.ExpectQuery("SELECT pg_try_advisory_lock").WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(true))
}
func expectCheckoutRecoverySessionUnlock(mock sqlmock.Sqlmock) {
	mock.ExpectQuery("SELECT pg_advisory_unlock").WillReturnRows(sqlmock.NewRows([]string{"unlocked"}).AddRow(true))
}
