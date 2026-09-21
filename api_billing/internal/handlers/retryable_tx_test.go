package handlers

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/sirupsen/logrus"
)

func expectPrepaidTopupCreditBody(mock sqlmock.Sqlmock, balanceAfter int64) {
	mock.ExpectQuery(`(?s)SELECT status, tenant_id::text AS tenant_id.*FROM purser.pending_topups.*FOR UPDATE`).
		WithArgs("topup-retry").
		WillReturnRows(pendingTopupRows("pending", "tenant-a", "mollie", 1500, "EUR", "sess-retry", ""))
	mock.ExpectExec("UPDATE purser.pending_topups").
		WithArgs("pay-retry", "sess-retry", "topup-retry", "tenant-a", "mollie", int64(1500), "EUR").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO purser.prepaid_balances").
		WithArgs("tenant-a", "EUR").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("UPDATE purser.prepaid_balances").
		WithArgs(int64(1500), "tenant-a", "EUR").
		WillReturnRows(sqlmock.NewRows([]string{"balance_cents"}).AddRow(balanceAfter))
	mock.ExpectExec(`INSERT INTO purser.balance_transactions`).
		WithArgs(sqlmock.AnyArg(), "tenant-a", int64(1500), balanceAfter, "topup", "Card top-up via mollie", "topup-retry", "topup", "webhook", nil, "mollie checkout completed", "sess-retry", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE purser.pending_topups").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "topup-retry").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE purser.payment_provider_intents").
		WithArgs("pay-retry", "sess-retry", sqlmock.AnyArg(), "topup-retry").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE purser.tenant_subscriptions").
		WithArgs("tenant-a").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("INSERT INTO purser.billing_event_outbox").
		WithArgs(sqlmock.AnyArg(), eventTopupCredited, "tenant-a", "", "topup", "topup-retry", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

// TestPrepaidCheckoutCreditReplaysSerializationFailure proves the top-up
// credit transaction is replayed from the start after SQLSTATE 40001, whether
// the conflict surfaces on a statement or at commit, that the billing event
// is enqueued inside the attempt that commits, and that entitlement
// convergence runs once, after the successful commit.
func TestPrepaidCheckoutCreditReplaysSerializationFailure(t *testing.T) {
	serializationFailure := &pq.Error{Code: "40001", Message: "restart read required"}
	cases := []struct {
		name         string
		firstAttempt func(sqlmock.Sqlmock)
	}{
		{
			name: "statement",
			firstAttempt: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(`(?s)SELECT status, tenant_id::text AS tenant_id.*FROM purser.pending_topups.*FOR UPDATE`).
					WithArgs("topup-retry").
					WillReturnRows(pendingTopupRows("pending", "tenant-a", "mollie", 1500, "EUR", "sess-retry", ""))
				mock.ExpectExec("UPDATE purser.pending_topups").
					WithArgs("pay-retry", "sess-retry", "topup-retry", "tenant-a", "mollie", int64(1500), "EUR").
					WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec("INSERT INTO purser.prepaid_balances").
					WithArgs("tenant-a", "EUR").
					WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectQuery("UPDATE purser.prepaid_balances").
					WithArgs(int64(1500), "tenant-a", "EUR").
					WillReturnError(serializationFailure)
				mock.ExpectRollback()
			},
		},
		{
			name: "commit",
			firstAttempt: func(mock sqlmock.Sqlmock) {
				expectPrepaidTopupCreditBody(mock, 9999)
				mock.ExpectCommit().WillReturnError(serializationFailure)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mockDB, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			defer mockDB.Close()

			converged := 0
			s := &Service{
				db:     mockDB,
				logger: logrus.New(),
				convergeTenantEntitlements: func(_ context.Context, tenantID string) error {
					if tenantID != "tenant-a" {
						t.Fatalf("converged tenant = %q", tenantID)
					}
					if err := mock.ExpectationsWereMet(); err != nil {
						t.Fatalf("entitlements converged before the credit committed: %v", err)
					}
					converged++
					return nil
				},
			}

			mock.ExpectBegin()
			tc.firstAttempt(mock)
			mock.ExpectBegin()
			expectPrepaidTopupCreditBody(mock, 2000)
			mock.ExpectCommit()

			if err := s.handlePrepaidCheckoutCompleted(context.Background(), "sess-retry", "pay-retry", "tenant-a", "topup-retry", 1500, "EUR", ProviderMollie, true); err != nil {
				t.Fatalf("handlePrepaidCheckoutCompleted: %v", err)
			}
			if converged != 1 {
				t.Fatalf("entitlement convergence calls = %d, want 1", converged)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet expectations: %v", err)
			}
		})
	}
}

// TestDeductPrepaidBalanceForUsageReturnsReplayedAttempt proves values computed
// inside a replayed closure come from the attempt that committed, not from the
// aborted one.
func TestDeductPrepaidBalanceForUsageReturnsReplayedAttempt(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()

	jm := &JobManager{db: mockDB, logger: logrus.New()}
	expectDeduction := func(balance int64) {
		mock.ExpectBegin()
		mock.ExpectExec("INSERT INTO purser.prepaid_balances").
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery("FROM purser.prepaid_balances").
			WillReturnRows(sqlmock.NewRows([]string{"balance_cents"}).AddRow(balance))
		mock.ExpectExec("INSERT INTO purser.balance_transactions").
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec("UPDATE purser.prepaid_balances").
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
	expectDeduction(5000)
	mock.ExpectCommit().WillReturnError(&pq.Error{Code: "40001", Message: "serialization failure"})
	expectDeduction(4000)
	mock.ExpectCommit()

	previous, next, applied, err := jm.deductPrepaidBalanceForUsage(context.Background(), "tenant-a", 250, "usage", uuid.New())
	if err != nil {
		t.Fatalf("deductPrepaidBalanceForUsage: %v", err)
	}
	if previous != 4000 || next != 3750 || !applied {
		t.Fatalf("got previous=%d next=%d applied=%v, want 4000 3750 true", previous, next, applied)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
