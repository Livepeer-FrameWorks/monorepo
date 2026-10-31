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
	cols := []string{"id", "tenant_id", "provider", "purpose", "provider_customer_id", "provider_session_id", "provider_payment_id", "provider_subscription_id", "status", "expires_at", "attempt_count", "checkout_completed_at", "recovery_started_at", "created_at", "amount_cents", "currency", "provider_deadline", "recovery_outcome", "provider_request", "submission_state", "compensation_current"}
	values = append(values, nil, nil, time.Now(), int64(0), "EUR", values[9], nil, nil, "unknown", false)
	return sqlmock.NewRows(cols).AddRow(values...)
}

func pendingProviderCheckoutForTest(ctx context.Context, db *sql.DB, tenantID string) (bool, error) {
	row, err := purserdb.New(db).GetPendingProviderCheckout(ctx, purserdb.GetPendingProviderCheckoutParams{TenantID: tenantID})
	return row.Pending, err
}
