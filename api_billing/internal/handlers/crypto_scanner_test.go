package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestHexQuantityToDecimal(t *testing.T) {
	got, err := hexQuantityToDecimal("0xde0b6b3a7640000")
	if err != nil || got != "1000000000000000000" {
		t.Fatalf("hexQuantityToDecimal() = %q, %v", got, err)
	}
	if _, err := hexQuantityToDecimal("not-hex"); err == nil {
		t.Fatal("malformed quantity was accepted")
	}
}

func TestCryptoScannerStartBlockRequiresProductionAnchor(t *testing.T) {
	t.Setenv("BUILD_ENV", "production")
	t.Setenv("CRYPTO_SCAN_START_BLOCK_BASE", "")
	if _, err := cryptoScannerStartBlock("base", 10_000); err == nil {
		t.Fatal("production scanner accepted an implicit start block")
	}
	t.Setenv("CRYPTO_SCAN_START_BLOCK_BASE", "1234")
	got, err := cryptoScannerStartBlock("base", 10_000)
	if err != nil || got != 1234 {
		t.Fatalf("start block = %d, %v", got, err)
	}
}

func TestCryptoScannerDevelopmentBootstrapIsBounded(t *testing.T) {
	t.Setenv("BUILD_ENV", "development")
	t.Setenv("CRYPTO_SCAN_START_BLOCK_BASE", "")
	got, err := cryptoScannerStartBlock("base", 10_000)
	if err != nil || got != 9_000 {
		t.Fatalf("start block = %d, %v", got, err)
	}
}

func scannerAddresses(count int) map[string]struct{} {
	addresses := make(map[string]struct{}, count)
	for index := 1; index <= count; index++ {
		addresses[fmt.Sprintf("0x%040x", index)] = struct{}{}
	}
	return addresses
}

func TestScanUSDCLogsShardsLargeDestinationSet(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		defer request.Body.Close()
		var payload struct {
			ID     int   `json:"id"`
			Params []any `json:"params"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		filter := payload.Params[0].(map[string]any)
		topics := filter["topics"].([]any)[2].([]any)
		if len(topics) > cryptoScanTopicBatchSize {
			t.Fatalf("topic shard size=%d", len(topics))
		}
		calls++
		_ = json.NewEncoder(writer).Encode(map[string]any{"jsonrpc": "2.0", "id": payload.ID, "result": []any{}})
	}))
	defer server.Close()
	t.Setenv("TEST_SCANNER_RPC_ENDPOINT", server.URL)
	monitor := &CryptoMonitor{rpc: NewRPCClient()}
	network := NetworkConfig{Name: "test", RPCEndpointEnv: "TEST_SCANNER_RPC_ENDPOINT", USDCContract: "0x1111111111111111111111111111111111111111"}
	if _, err := monitor.scanUSDCLogs(context.Background(), network, 1, 100, scannerAddresses(2500)); err != nil {
		t.Fatal(err)
	}
	if calls != 25 {
		t.Fatalf("eth_getLogs calls=%d want=25", calls)
	}
}

func TestScanUSDCLogsRecursivelySplitsRejectedShard(t *testing.T) {
	maxAccepted := 25
	accepted := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		defer request.Body.Close()
		var payload struct {
			ID     int   `json:"id"`
			Params []any `json:"params"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		filter := payload.Params[0].(map[string]any)
		topics := filter["topics"].([]any)[2].([]any)
		response := map[string]any{"jsonrpc": "2.0", "id": payload.ID}
		if len(topics) > maxAccepted {
			response["error"] = map[string]any{"code": -32005, "message": "too many topics"}
		} else {
			accepted += len(topics)
			response["result"] = []any{}
		}
		_ = json.NewEncoder(writer).Encode(response)
	}))
	defer server.Close()
	t.Setenv("TEST_SCANNER_RPC_ENDPOINT", server.URL)
	monitor := &CryptoMonitor{rpc: NewRPCClient()}
	network := NetworkConfig{Name: "test", RPCEndpointEnv: "TEST_SCANNER_RPC_ENDPOINT", USDCContract: "0x1111111111111111111111111111111111111111"}
	if _, err := monitor.scanUSDCLogs(context.Background(), network, 1, 100, scannerAddresses(80)); err != nil {
		t.Fatal(err)
	}
	if accepted != 80 {
		t.Fatalf("accepted destinations=%d want=80", accepted)
	}
}

func TestScanUSDCLogsFailedShardCanReplayWholeRange(t *testing.T) {
	fail := true
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		defer request.Body.Close()
		var payload struct {
			ID int `json:"id"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		calls++
		response := map[string]any{"jsonrpc": "2.0", "id": payload.ID}
		if fail {
			response["error"] = map[string]any{"code": -32000, "message": "temporary provider failure"}
		} else {
			response["result"] = []any{}
		}
		_ = json.NewEncoder(writer).Encode(response)
	}))
	defer server.Close()
	t.Setenv("TEST_SCANNER_RPC_ENDPOINT", server.URL)
	monitor := &CryptoMonitor{rpc: NewRPCClient()}
	network := NetworkConfig{Name: "test", RPCEndpointEnv: "TEST_SCANNER_RPC_ENDPOINT", USDCContract: "0x1111111111111111111111111111111111111111"}
	addresses := scannerAddresses(1)
	if _, err := monitor.scanUSDCLogs(context.Background(), network, 50, 60, addresses); err == nil {
		t.Fatal("failed shard unexpectedly succeeded")
	}
	fail = false
	if _, err := monitor.scanUSDCLogs(context.Background(), network, 50, 60, addresses); err != nil {
		t.Fatalf("whole-range replay failed: %v", err)
	}
	if calls != 2 {
		t.Fatalf("RPC calls=%d want=2", calls)
	}
}

func TestCommitScanBatchDeduplicatesLogsAndFencesCursor(t *testing.T) {
	event := observedDeposit{
		Asset: "USDC", TxHash: "0xtx", LogIndex: 1, BlockNumber: 100,
		BlockHash: "0xblock", From: "0xfrom", To: "0xto", Amount: "5000000",
	}

	t.Run("duplicate logs are idempotent", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		mock.ExpectBegin()
		for range 2 {
			mock.ExpectExec("INSERT INTO purser.crypto_deposit_events").
				WithArgs("base", event.Asset, event.TxHash, event.LogIndex, event.BlockNumber,
					event.BlockHash, event.From, event.To, event.Amount).
				WillReturnResult(sqlmock.NewResult(0, 1))
		}
		mock.ExpectExec("UPDATE purser.crypto_scan_cursors").
			WithArgs(int64(100), "0xblock", int64(110), "base", int64(90)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		monitor := &CryptoMonitor{db: db}
		if err := monitor.commitScanBatch(context.Background(), "base", 90, 100, 110, "0xblock", []observedDeposit{event, event}); err != nil {
			t.Fatal(err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("cursor compare-and-swap rejects stale worker", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		mock.ExpectBegin()
		mock.ExpectExec("UPDATE purser.crypto_scan_cursors").
			WithArgs(int64(100), "0xblock", int64(110), "base", int64(90)).
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectRollback()
		monitor := &CryptoMonitor{db: db}
		if err := monitor.commitScanBatch(context.Background(), "base", 90, 100, 110, "0xblock", nil); err == nil {
			t.Fatal("stale cursor worker unexpectedly committed")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}
