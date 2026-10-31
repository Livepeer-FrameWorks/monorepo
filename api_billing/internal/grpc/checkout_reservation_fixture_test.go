package grpc

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"time"

	"frameworks/api_billing/internal/database/purserdb"

	"github.com/DATA-DOG/go-sqlmock"
)

func checkoutReservationTestRows(values ...driver.Value) *sqlmock.Rows {
	return checkoutReservationRows(false, values...)
}

func checkoutReservationJournalRows(values ...driver.Value) *sqlmock.Rows {
	return checkoutReservationRows(true, values...)
}

func checkoutReservationRows(journal bool, values ...driver.Value) *sqlmock.Rows {
	cols := []string{"id", "tenant_id", "provider", "purpose", "provider_customer_id", "provider_session_id", "provider_payment_id", "provider_subscription_id", "status", "expires_at", "attempt_count", "checkout_completed_at", "recovery_started_at", "created_at", "amount_cents", "currency", "provider_deadline", "recovery_outcome", "provider_request", "submission_state", "succeeded_at", "activated_at", "funding_intent_id", "provider_mandate_id", "compensation_current"}
	var payload driver.Value
	state := "unknown"
	if journal {
		payload = []byte(`{}`)
		state = "submitted"
	}
	values = append(values, nil, nil, time.Now(), int64(0), "EUR", values[9], nil, payload, state, nil, nil, "", nil, false)
	return sqlmock.NewRows(cols).AddRow(values...)
}

func pendingProviderCheckoutForTest(ctx context.Context, db *sql.DB, tenantID string) (bool, error) {
	row, err := purserdb.New(db).GetPendingProviderCheckout(ctx, purserdb.GetPendingProviderCheckoutParams{TenantID: tenantID})
	return row.Pending, err
}
