package handlers

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"frameworks/pkg/billing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/sirupsen/logrus"
)

func TestConfirmPrepaidTopupCreatesBalanceAndTransaction(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer mockDB.Close()

	mock.ExpectBegin()
	tx, err := mockDB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	wallet := PendingWallet{
		ID:                  "wallet-1",
		TenantID:            "tenant-1",
		Purpose:             "prepaid",
		ExpectedAmountCents: int64Ptr(2500),
		Asset:               "USDC",
	}

	currency := billing.DefaultCurrency()

	mock.ExpectQuery("SELECT balance_cents FROM purser.prepaid_balances").
		WithArgs("tenant-1", currency).
		WillReturnError(sql.ErrNoRows)

	mock.ExpectExec("INSERT INTO purser.prepaid_balances").
		WithArgs("tenant-1", int64(2500), currency).
		WillReturnResult(sqlmock.NewResult(1, 1))

	mock.ExpectExec("INSERT INTO purser.balance_transactions").
		WithArgs(sqlmock.AnyArg(), "tenant-1", int64(2500), int64(2500), sqlmock.AnyArg(), "wallet-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	mock.ExpectRollback()

	cm := &CryptoMonitor{db: mockDB, logger: logrus.New()}
	err = cm.confirmPrepaidTopup(context.Background(), tx, wallet, CryptoTransaction{
		Hash: "0xabc",
	}, 25.0, time.Now())
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	if err := tx.Rollback(); err != nil {
		t.Fatalf("failed to rollback: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestIsValidPaymentForNetworkHonorsConfirmations(t *testing.T) {
	cm := &CryptoMonitor{logger: logrus.New()}
	network := NetworkConfig{Confirmations: 2}
	tx := CryptoTransaction{
		Value:         "1000000",
		Confirmations: 1,
	}

	isValid, amount := cm.isValidPaymentForNetwork(tx, 1.0, "USDC", network, "prepaid")
	if isValid {
		t.Fatalf("expected invalid payment due to confirmations")
	}
	if amount != 1.0 {
		t.Fatalf("expected amount 1.0, got %f", amount)
	}
}

func int64Ptr(val int64) *int64 {
	return &val
}
