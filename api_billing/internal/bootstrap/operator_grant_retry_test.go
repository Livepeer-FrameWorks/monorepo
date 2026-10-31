package bootstrap

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/lib/pq"
)

func TestCustomerGrantDatabaseConflictRetriesWholeTransaction(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tenant, tier := "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"
	entry := CustomerBilling{Tenant: TenantRef{Ref: "quartermaster.tenants[acme]"}, Tier: "trial", Model: "postpaid", ClusterAccess: "none"}
	prepared := map[string]*PreparedCustomerGrant{"acme": {tenantID: tenant, checkGrant: true}}
	for attempt := 0; attempt < 2; attempt++ {
		mock.ExpectBegin()
		mock.ExpectQuery("LockTenantSubscriptionForProviderChange").WillReturnRows(sqlmock.NewRows([]string{"subscription_id", "tier_id", "status", "billing_model", "stripe_subscription_id", "mollie_subscription_id", "pending_reason", "free_fallback_provider_stopping", "free_fallback_provider_ended_at"}).AddRow("sub", tier, "active", "postpaid", nil, nil, nil, false, nil))
		mock.ExpectQuery("IsTenantBillingClosed").WillReturnRows(sqlmock.NewRows([]string{"closed"}).AddRow(false))
		if attempt == 0 {
			mock.ExpectQuery("GetSubscriptionOperatorGrant").WillReturnError(&pq.Error{Code: "40001", Message: "restart read required"})
			mock.ExpectRollback()
		} else {
			mock.ExpectQuery("GetSubscriptionOperatorGrant").WillReturnError(sql.ErrNoRows)
			mock.ExpectQuery("GetBootstrapBillingTier").WillReturnRows(sqlmock.NewRows([]string{"id", "tier_level", "currency"}).AddRow(tier, 4, "EUR"))
			mock.ExpectQuery("HasPendingTierUpgrade").WithArgs(tenant).WillReturnRows(sqlmock.NewRows([]string{"pending"}).AddRow(false))
			mock.ExpectQuery("GetSubscriptionOperatorGrant").WillReturnError(sql.ErrNoRows)
			mock.ExpectQuery("GetBootstrapTenantSubscription").WillReturnRows(sqlmock.NewRows([]string{"tier_id", "billing_model"}).AddRow(tier, "postpaid"))
			mock.ExpectCommit()
		}
	}
	attempts := 0
	err = database.WithRetryablePostgresTx(context.Background(), db, nil, func(tx *sql.Tx) error {
		attempts++
		result, _, reconcileErr := ReconcileCustomerBilling(context.Background(), tx, []CustomerBilling{entry}, &fakeQM{tenantUUIDs: map[string]string{"acme": tenant}}, prepared)
		if reconcileErr == nil && len(result.Deferred) > 0 {
			t.Errorf("database conflict became a business deferral: %+v", result.Deferred)
		}
		return reconcileErr
	})
	if err != nil || attempts != 2 {
		t.Fatalf("attempts=%d err=%v, want successful transaction replay", attempts, err)
	}
	if prepared["acme"].deferred != nil {
		t.Fatal("failed attempt poisoned prepared state")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
