package handlers

import (
	"context"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/shopspring/decimal"
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
		ID:                     "wallet-1",
		TenantID:               "tenant-1",
		Purpose:                "prepaid",
		ExpectedAmountCents:    int64Ptr(2500),
		Asset:                  "USDC",
		CreditedAmountCurrency: "USD",
	}

	currency := "USD"

	mock.ExpectExec("INSERT INTO purser.prepaid_balances").
		WithArgs("tenant-1", currency).
		WillReturnResult(sqlmock.NewResult(0, 0))

	mock.ExpectQuery("SELECT balance_cents FROM purser.prepaid_balances").
		WithArgs("tenant-1", currency).
		WillReturnRows(sqlmock.NewRows([]string{"balance_cents"}).AddRow(int64(0)))

	mock.ExpectExec("UPDATE purser.prepaid_balances").
		WithArgs(int64(2500), "tenant-1", currency).
		WillReturnResult(sqlmock.NewResult(0, 1))

	mock.ExpectExec("INSERT INTO purser.balance_transactions").
		WithArgs(sqlmock.AnyArg(), "tenant-1", int64(2500), int64(2500), sqlmock.AnyArg(), "wallet-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	mock.ExpectExec("UPDATE purser.crypto_wallets").
		WithArgs("wallet-1", int64(2500), "USD", "tenant-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	mock.ExpectRollback()

	cm := &CryptoMonitor{db: mockDB, logger: logrus.New()}
	// USDC at 6 decimals: 25.00 USDC = 25_000_000 base units, credits 2500 cents.
	txBaseUnits := new(big.Int).SetInt64(25_000_000)
	creditedCents, creditedCurrency, err := cm.confirmPrepaidTopup(context.Background(), tx, wallet, CryptoTransaction{
		Hash: "0xabc",
	}, txBaseUnits, time.Now())
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if creditedCents != 2500 {
		t.Fatalf("creditedCents = %d, want 2500", creditedCents)
	}
	if creditedCurrency != "USD" {
		t.Fatalf("creditedCurrency = %q, want USD", creditedCurrency)
	}

	if err := tx.Rollback(); err != nil {
		t.Fatalf("failed to rollback: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestConfirmInvoicePaymentUpdatesPendingIntent(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer mockDB.Close()

	mock.ExpectBegin()
	dbTx, err := mockDB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	invoiceID := "invoice-1"
	wallet := PendingWallet{
		ID:            "wallet-1",
		TenantID:      "tenant-1",
		Purpose:       "invoice",
		InvoiceID:     &invoiceID,
		Asset:         "USDC",
		Network:       "base",
		WalletAddress: "0xwallet",
	}
	chainTx := CryptoTransaction{Hash: "0xtx", BlockNumber: 123}

	mock.ExpectQuery("UPDATE purser.billing_payments bp").
		WithArgs("0xtx", sqlmock.AnyArg(), 42.5, "USDC", "base", int64(123), "invoice-1", "tenant-1", "crypto_usdc", "0xwallet").
		WillReturnRows(sqlmock.NewRows([]string{"id", "amount", "currency"}).AddRow("payment-1", 42.50, "EUR"))
	mock.ExpectExec("UPDATE purser.billing_invoices").
		WithArgs(sqlmock.AnyArg(), "invoice-1", "tenant-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectRollback()

	cm := &CryptoMonitor{db: mockDB, logger: logrus.New()}
	payment, err := cm.confirmInvoicePayment(context.Background(), dbTx, wallet, chainTx, 42.5, time.Now())
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if payment.PaymentID != "payment-1" || payment.Amount != 42.50 || payment.Currency != "EUR" {
		t.Fatalf("unexpected payment result: %+v", payment)
	}

	if err := dbTx.Rollback(); err != nil {
		t.Fatalf("failed to rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestEvaluatePayment_DetectsAndConfirms(t *testing.T) {
	cm := &CryptoMonitor{logger: logrus.New()}
	network := NetworkConfig{Confirmations: 2}
	wallet := PendingWallet{Asset: "USDC", Purpose: "prepaid"}

	// 1.0 USDC = 1_000_000 base units; expected 1.0 USDC equivalent.
	tx := CryptoTransaction{Value: "1000000", Confirmations: 1}
	match := cm.evaluatePayment(tx, wallet, 1.0, network)
	if !match.amountSeen {
		t.Fatalf("expected amount to match")
	}
	if match.confirmed {
		t.Fatalf("expected NOT confirmed at 1/2 confirmations")
	}

	tx.Confirmations = 2
	match = cm.evaluatePayment(tx, wallet, 1.0, network)
	if !match.amountSeen || !match.confirmed {
		t.Fatalf("expected amount seen + confirmed, got %+v", match)
	}
}

func TestEvaluatePayment_PrepaidETHComparesBaseUnits(t *testing.T) {
	cm := &CryptoMonitor{logger: logrus.New()}
	network := NetworkConfig{Confirmations: 1}
	// Quote: 0.015 ETH = 15_000_000_000_000_000 wei. Locked at $3300/ETH.
	expectedBaseUnits, _ := new(big.Int).SetString("15000000000000000", 10)
	wallet := PendingWallet{
		Asset:                   "ETH",
		Purpose:                 "prepaid",
		ExpectedAmountBaseUnits: expectedBaseUnits,
		QuotedPriceUSD:          decimal.NewFromInt(3300),
	}

	// Exactly the quoted amount → seen.
	tx := CryptoTransaction{Value: "15000000000000000", Confirmations: 1}
	match := cm.evaluatePayment(tx, wallet, 0, network)
	if !match.amountSeen {
		t.Fatalf("expected amountSeen for exact match")
	}

	// 1% under (below 0.5% tolerance) → not seen.
	tx.Value = "14850000000000000"
	match = cm.evaluatePayment(tx, wallet, 0, network)
	if match.amountSeen {
		t.Fatalf("expected amountSeen=false for 1%% underpay")
	}
}

func TestEvaluatePayment_InvoiceETHComparesQuotedBaseUnits(t *testing.T) {
	cm := &CryptoMonitor{logger: logrus.New()}
	network := NetworkConfig{Confirmations: 3}
	expectedBaseUnits, _ := new(big.Int).SetString("15000000000000000", 10)
	wallet := PendingWallet{
		Asset:                   "ETH",
		Purpose:                 "invoice",
		ExpectedAmountBaseUnits: expectedBaseUnits,
		QuotedPriceUSD:          decimal.NewFromInt(3300),
	}

	tx := CryptoTransaction{Value: "15000000000000000", Confirmations: 2}
	match := cm.evaluatePayment(tx, wallet, 0, network)
	if !match.amountSeen {
		t.Fatalf("expected amountSeen for exact invoice quote")
	}
	if match.confirmed {
		t.Fatalf("expected invoice quote to wait for required confirmations")
	}

	tx.Confirmations = 3
	match = cm.evaluatePayment(tx, wallet, 0, network)
	if !match.amountSeen || !match.confirmed {
		t.Fatalf("expected invoice quote seen + confirmed, got %+v", match)
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
