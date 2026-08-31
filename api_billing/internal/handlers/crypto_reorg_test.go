package handlers

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/sirupsen/logrus"
)

func TestReverseAllocatedPrepaidDepositIsAtomicAndAudited(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	monitor := &CryptoMonitor{db: db, logger: logrus.New()}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT event\.status AS event_status, event\.canonical, wallet\.network`).
		WithArgs("event-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"status", "canonical", "network", "tenant_id", "purpose", "invoice_id",
			"credited_amount_cents", "credited_amount_currency", "wallet_id", "tx_hash",
		}).AddRow("allocated", true, "base", "tenant-1", "prepaid", "", int64(2500), "EUR", "wallet-1", "0xtx"))
	mock.ExpectQuery(`SELECT id::text AS id, amount_cents[\s\S]*reference_type = \$2`).
		WithArgs("tenant-1", "crypto_payment", "wallet-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "amount_cents"}).AddRow("credit-1", int64(2500)))
	mock.ExpectQuery(`SELECT EXISTS\([\s\S]*reference_type = \$2`).
		WithArgs("tenant-1", "crypto_reorg", "event-1").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery(`UPDATE purser\.prepaid_balances`).
		WithArgs(int64(-2500), "tenant-1", "EUR").
		WillReturnRows(sqlmock.NewRows([]string{"balance_cents"}).AddRow(int64(-1500)))
	mock.ExpectExec(`INSERT INTO purser\.balance_transactions`).
		WithArgs(sqlmock.AnyArg(), "tenant-1", int64(-2500), int64(-1500),
			"Crypto deposit reversed after finalized-block canonicality failure", "event-1",
			"crypto_reorg", "allocated deposit block is no longer canonical",
			"old=0xold canonical=0xnew", "credit-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO purser\.credit_notes`).
		WithArgs("event-1", "0xtx", "tenant-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE purser\.crypto_deposit_events[\s\S]*status = 'reorged'`).
		WithArgs("event-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE purser\.crypto_wallets[\s\S]*status = 'review_required'`).
		WithArgs("wallet-1", "tenant-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectExec(`INSERT INTO purser\.billing_event_outbox`).
		WithArgs(sqlmock.AnyArg(), eventCryptoDepositReorg, "tenant-1", "", "crypto_deposit_event", "event-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := monitor.reverseAllocatedDeposit(context.Background(), "event-1", "0xold", "0xnew"); err != nil {
		t.Fatalf("reverseAllocatedDeposit: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
