package handlers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/sirupsen/logrus"
)

func TestReconcileFailedTimeoutsSkipsWithoutReversal(t *testing.T) {
	server := newTestRPCServer(t, &TransactionReceipt{
		Status:      "0x1",
		BlockNumber: "0x10",
		GasUsed:     "0x5208",
	}, "0x20")
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
		WillReturnRows(sqlmock.NewRows([]string{"id", "network", "tx_hash", "tenant_id", "amount_cents", "settled_at"}).
			AddRow("nonce-1", "base", "0xlate", "tenant-1", int64(2500), settledAt))
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs("tenant-1", "0xlate").
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
		WillReturnRows(sqlmock.NewRows([]string{"id", "network", "tx_hash", "tenant_id", "amount_cents", "settled_at", "block_number"}).
			AddRow("nonce-2", "base", "0xreorg", "tenant-1", int64(2000), settledAt, int64(10)))
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs("tenant-1", "0xreorg").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectExec("UPDATE purser.x402_nonces").
		WithArgs("nonce-2", "transaction reorged or missing").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT balance_cents FROM purser.prepaid_balances").
		WithArgs("tenant-1", "EUR").
		WillReturnRows(sqlmock.NewRows([]string{"balance_cents"}).AddRow(int64(5000)))
	mock.ExpectExec("INSERT INTO purser.prepaid_balances").
		WithArgs("tenant-1", int64(3000), "EUR").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO purser.balance_transactions").
		WithArgs(sqlmock.AnyArg(), "tenant-1", int64(-2000), int64(3000), sqlmock.AnyArg(), "0xreorg").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	reconciler.reconcileConfirmedSettlements(context.Background())

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
		}
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "eth_getTransactionReceipt":
			resp := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      1,
				"result":  receipt,
			}
			if receipt == nil {
				resp["result"] = nil
			}
			_ = json.NewEncoder(w).Encode(resp)
		case "eth_blockNumber":
			resp := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      1,
				"result":  latestBlock,
			}
			_ = json.NewEncoder(w).Encode(resp)
		default:
			http.Error(w, "unknown method", http.StatusBadRequest)
		}
	}))
}
