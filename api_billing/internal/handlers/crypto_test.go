package handlers

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
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

func TestGetETHTransactions_InlineDecodeAndMapping(t *testing.T) {
	t.Setenv("TEST_EXPLORER_API_KEY", "key")
	network := NetworkConfig{
		Name:           "base",
		DisplayName:    "Base",
		ExplorerAPIURL: "",
		ExplorerAPIEnv: "TEST_EXPLORER_API_KEY",
	}
	cm := &CryptoMonitor{logger: logrus.New()}

	t.Run("malformed response", func(t *testing.T) {
		network.ExplorerAPIURL = "https://explorer.test/api"
		withDefaultHTTPClient(t, &http.Client{
			Transport: testRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
				return newJSONResponse(http.StatusOK, `{"result":`), nil
			}),
		})

		_, err := cm.getETHTransactions(context.Background(), network, "0xabc")
		if err == nil || !strings.Contains(err.Error(), "failed to parse response") {
			t.Fatalf("expected parse error, got %v", err)
		}
	})

	t.Run("maps incoming non-zero tx", func(t *testing.T) {
		network.ExplorerAPIURL = "https://explorer.test/api"
		withDefaultHTTPClient(t, &http.Client{
			Transport: testRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
				return newJSONResponse(http.StatusOK, `{
				"status":"1",
				"result":[
					{"hash":"0x1","to":"0xABC","value":"1000000000000000000","confirmations":"12","blockNumber":"100","timeStamp":"1700000000"},
					{"hash":"0x2","to":"0xdef","value":"42","confirmations":"5","blockNumber":"101","timeStamp":"1700000100"},
					{"hash":"0x3","to":"0xabc","value":"0","confirmations":"99","blockNumber":"102","timeStamp":"1700000200"}
				]
			}`), nil
			}),
		})

		txs, err := cm.getETHTransactions(context.Background(), network, "0xabc")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(txs) != 1 {
			t.Fatalf("expected one incoming tx, got %d", len(txs))
		}
		if txs[0].Hash != "0x1" || txs[0].To != "0xABC" || txs[0].Confirmations != 12 || txs[0].BlockNumber != 100 {
			t.Fatalf("unexpected mapped tx: %+v", txs[0])
		}
		if txs[0].BlockTime.Unix() != 1700000000 {
			t.Fatalf("unexpected block time: %v", txs[0].BlockTime)
		}
	})
}

func TestGetERC20TransactionsForNetwork_InlineDecodeAndMapping(t *testing.T) {
	t.Setenv("TEST_EXPLORER_API_KEY", "key")
	network := NetworkConfig{
		Name:           "arbitrum",
		DisplayName:    "Arbitrum One",
		ExplorerAPIURL: "",
		ExplorerAPIEnv: "TEST_EXPLORER_API_KEY",
	}
	cm := &CryptoMonitor{logger: logrus.New()}

	t.Run("malformed response", func(t *testing.T) {
		network.ExplorerAPIURL = "https://explorer.test/api"
		withDefaultHTTPClient(t, &http.Client{
			Transport: testRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
				return newJSONResponse(http.StatusOK, `{"message":`), nil
			}),
		})

		_, err := cm.getERC20TransactionsForNetwork(context.Background(), network, "0xabc", "0xcontract")
		if err == nil || !strings.Contains(err.Error(), "failed to parse response") {
			t.Fatalf("expected parse error, got %v", err)
		}
	})

	t.Run("maps incoming non-zero tx", func(t *testing.T) {
		network.ExplorerAPIURL = "https://explorer.test/api"
		withDefaultHTTPClient(t, &http.Client{
			Transport: testRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
				return newJSONResponse(http.StatusOK, `{
				"status":"1",
				"message":"OK",
				"result":[
					{"hash":"0xa","from":"0xfrom","to":"0xAbC","value":"1000000","confirmations":"7","blockNumber":"200","timeStamp":"1700000300"},
					{"hash":"0xb","from":"0xfrom2","to":"0xother","value":"1000000","confirmations":"7","blockNumber":"201","timeStamp":"1700000400"}
				]
			}`), nil
			}),
		})

		txs, err := cm.getERC20TransactionsForNetwork(context.Background(), network, "0xabc", "0xcontract")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(txs) != 1 {
			t.Fatalf("expected one incoming tx, got %d", len(txs))
		}
		if txs[0].Hash != "0xa" || txs[0].From != "0xfrom" || txs[0].To != "0xAbC" || txs[0].BlockNumber != 200 {
			t.Fatalf("unexpected mapped tx: %+v", txs[0])
		}
	})
}

func int64Ptr(val int64) *int64 {
	return &val
}
