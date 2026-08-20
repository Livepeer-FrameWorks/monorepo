package handlers

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/sirupsen/logrus"
)

func TestReconcileFailedTimeoutsSkipsWithoutReversal(t *testing.T) {
	auth := testX402Authorization("25000000")
	server := newTestRPCServer(t, testX402Receipt(t, auth), "0x20")
	defer server.Close()
	t.Setenv("BASE_RPC_ENDPOINT", server.URL)

	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer mockDB.Close()

	reconciler := NewX402Reconciler(mockDB, logrus.New(), false)

	settledAt := time.Now().Add(-10 * time.Minute)
	mock.ExpectQuery("SELECT id, network, tx_hash, tenant_id, amount_cents, settled_at").
		WithArgs(reconciler.recoveryWindowHours).
		WillReturnRows(sqlmock.NewRows([]string{"id", "network", "tx_hash", "tenant_id", "amount_cents", "settled_at", "auth_payload"}).
			AddRow("nonce-1", "base", "0xlate", "tenant-1", int64(2500), settledAt, testX402PayloadJSON(t, auth)))
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs("tenant-1", "nonce-1").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	reconciler.reconcileFailedTimeouts(context.Background())

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestReconcileConfirmedSettlementsHandlesReorg(t *testing.T) {
	server := newTestRPCServer(t, nil, "0x64")
	defer server.Close()
	t.Setenv("BASE_RPC_ENDPOINT", server.URL)
	t.Setenv("X402_REORG_DEPTH_BLOCKS", "1")

	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer mockDB.Close()

	reconciler := NewX402Reconciler(mockDB, logrus.New(), false)

	settledAt := time.Now().Add(-30 * time.Minute)
	mock.ExpectQuery("SELECT id, network, tx_hash, tenant_id, amount_cents, settled_at, block_number").
		WillReturnRows(sqlmock.NewRows([]string{"id", "network", "tx_hash", "tenant_id", "amount_cents", "settled_at", "block_number", "client_ip"}).
			AddRow("nonce-2", "base", "0xreorg", "tenant-1", int64(2000), settledAt, int64(10), "127.0.0.1"))
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs("tenant-1", "nonce-2").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectExec("UPDATE purser.x402_nonces").
		WithArgs("nonce-2", "transaction reorged or missing").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs("tenant-1", "nonce-2").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs("tenant-1", "nonce-2").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery("SELECT balance_cents FROM purser.prepaid_balances").
		WithArgs("tenant-1", "EUR").
		WillReturnRows(sqlmock.NewRows([]string{"balance_cents"}).AddRow(int64(5000)))
	mock.ExpectExec("UPDATE purser.prepaid_balances").
		WithArgs(int64(3000), "tenant-1", "EUR").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO purser.balance_transactions").
		WithArgs(sqlmock.AnyArg(), "tenant-1", int64(-2000), int64(3000), sqlmock.AnyArg(), "nonce-2").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO purser.credit_notes").
		WithArgs("tenant-1", "nonce-2", "0xreorg").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT tenant_id, amount_cents, status, rollup_applied_at, rollup_reversed_at").
		WithArgs("nonce-2").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "amount_cents", "status", "rollup_applied_at", "rollup_reversed_at"}).
			AddRow("tenant-1", int64(2000), "failed", time.Now(), nil))
	mock.ExpectExec("UPDATE purser.tenant_balance_rollups").
		WithArgs("tenant-1", int64(2000)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE purser.x402_nonces").
		WithArgs("nonce-2").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	reconciler.reconcileConfirmedSettlements(context.Background())

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestReconcilePendingSettlementCreditsMissingLedgerBeforeConfirm(t *testing.T) {
	auth := testX402Authorization("25000000")
	server := newTestRPCServer(t, testX402Receipt(t, auth), "0x20")
	defer server.Close()
	t.Setenv("BASE_RPC_ENDPOINT", server.URL)

	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer mockDB.Close()

	reconciler := NewX402Reconciler(mockDB, logrus.New(), false)
	settlement := PendingSettlement{
		ID:          "nonce-3",
		Network:     "base",
		TxHash:      "0xcredit",
		TenantID:    "tenant-1",
		AmountCents: 2500,
		SettledAt:   time.Now().Add(-5 * time.Minute),
		AuthPayload: testX402PayloadJSON(t, auth),
	}

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT status, tenant_id, amount_cents, tx_hash").
		WithArgs("nonce-3").
		WillReturnRows(sqlmock.NewRows([]string{"status", "tenant_id", "amount_cents", "tx_hash"}).
			AddRow("pending", "tenant-1", int64(2500), "0xcredit"))
	mock.ExpectQuery("SELECT balance_after_cents FROM purser.balance_transactions").
		WithArgs("tenant-1", "nonce-3").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("INSERT INTO purser.prepaid_balances").
		WithArgs("tenant-1", "EUR").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("SELECT balance_cents FROM purser.prepaid_balances").
		WithArgs("tenant-1", "EUR").
		WillReturnRows(sqlmock.NewRows([]string{"balance_cents"}).AddRow(int64(1000)))
	mock.ExpectExec("UPDATE purser.prepaid_balances").
		WithArgs(int64(3500), "tenant-1", "EUR").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO purser.balance_transactions").
		WithArgs(sqlmock.AnyArg(), "tenant-1", int64(2500), int64(3500), sqlmock.AnyArg(), "nonce-3").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE purser.x402_nonces").
		WithArgs("nonce-3", int64(16), int64(21000)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE purser.x402_settlement_attempts").
		WithArgs("nonce-3", "0xcredit").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("UPDATE purser.x402_payment_quotes").
		WithArgs("nonce-3", "0xcredit").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	reconciler.reconcileSettlement(context.Background(), settlement)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func testX402Authorization(value string) *X402Authorization {
	return &X402Authorization{
		From: "0x1111111111111111111111111111111111111111", To: "0x2222222222222222222222222222222222222222",
		Value: value, ValidAfter: "0", ValidBefore: "9999999999",
		Nonce: "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
}

func testX402PayloadJSON(t *testing.T, auth *X402Authorization) string {
	t.Helper()
	payload, err := json.Marshal(X402PaymentPayload{Payload: &X402ExactPayload{Authorization: auth}})
	if err != nil {
		t.Fatal(err)
	}
	return string(payload)
}

func testX402Receipt(t *testing.T, auth *X402Authorization) *TransactionReceipt {
	t.Helper()
	from, err := padAddress(auth.From)
	if err != nil {
		t.Fatal(err)
	}
	to, err := padAddress(auth.To)
	if err != nil {
		t.Fatal(err)
	}
	amount, ok := new(big.Int).SetString(auth.Value, 10)
	if !ok {
		t.Fatal("invalid test amount")
	}
	amountWord := make([]byte, 32)
	amount.FillBytes(amountWord)
	return &TransactionReceipt{
		Status: "0x1", BlockNumber: "0x10", GasUsed: "0x5208",
		Logs: []TransactionLog{{
			Address: Networks["base"].USDCContract,
			Topics: []string{
				"0x" + hex.EncodeToString(keccak256([]byte("Transfer(address,address,uint256)"))),
				"0x" + hex.EncodeToString(from), "0x" + hex.EncodeToString(to),
			},
			Data: "0x" + hex.EncodeToString(amountWord),
		}},
	}
}

func TestRecoverReversedBalanceUsesDistinctIdempotencyReference(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer mockDB.Close()

	reconciler := NewX402Reconciler(mockDB, logrus.New(), false)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs("tenant-1", "nonce-4").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec("INSERT INTO purser.prepaid_balances").
		WithArgs("tenant-1", "EUR").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT balance_cents FROM purser.prepaid_balances").
		WithArgs("tenant-1", "EUR").
		WillReturnRows(sqlmock.NewRows([]string{"balance_cents"}).AddRow(int64(1000)))
	mock.ExpectExec("UPDATE purser.prepaid_balances").
		WithArgs(int64(3500), "tenant-1", "EUR").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO purser.balance_transactions").
		WithArgs(sqlmock.AnyArg(), "tenant-1", int64(2500), int64(3500), sqlmock.AnyArg(), "nonce-4").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := reconciler.recoverReversedBalance(context.Background(), "tenant-1", 2500, "nonce-4", "0xrecovered"); err != nil {
		t.Fatalf("recoverReversedBalance: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestRecoverReversedBalanceIsIdempotent(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer mockDB.Close()

	reconciler := NewX402Reconciler(mockDB, logrus.New(), false)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs("tenant-1", "nonce-4").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectRollback()

	if err := reconciler.recoverReversedBalance(context.Background(), "tenant-1", 2500, "nonce-4", "0xrecovered"); err != nil {
		t.Fatalf("recoverReversedBalance duplicate: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func newTestRPCServer(t *testing.T, receipt *TransactionReceipt, latestBlock string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read error", http.StatusBadRequest)
			return
		}
		var req struct {
			Method string `json:"method"`
			Params []any  `json:"params"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "eth_getTransactionReceipt":
			resp := map[string]any{
				"jsonrpc": "2.0",
				"id":      1,
				"result":  receipt,
			}
			if receipt == nil {
				resp["result"] = nil
			}
			_ = json.NewEncoder(w).Encode(resp)
		case "eth_blockNumber":
			resp := map[string]any{
				"jsonrpc": "2.0",
				"id":      1,
				"result":  latestBlock,
			}
			_ = json.NewEncoder(w).Encode(resp)
		case "eth_getBlockByNumber":
			blockNumber := latestBlock
			if len(req.Params) > 0 {
				if requested, ok := req.Params[0].(string); ok && requested != "finalized" && requested != "safe" {
					blockNumber = requested
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      1,
				"result": map[string]any{
					"number": blockNumber,
					"hash":   "0x" + strings.Repeat("1", 64),
				},
			})
		default:
			http.Error(w, "unknown method", http.StatusBadRequest)
		}
	}))
}
