package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"frameworks/api_billing/internal/appconfig/appconfigtest"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

func TestCryptoScannerErrorStage(t *testing.T) {
	cause := errors.New("provider timed out")
	err := newCryptoScannerError("finalized_head", cause)
	if got := cryptoScannerErrorStage(err); got != "finalized_head" {
		t.Fatalf("stage = %q", got)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("wrapped error does not retain cause: %v", err)
	}
	if got := cryptoScannerErrorStage(cause); got != "unknown" {
		t.Fatalf("untyped stage = %q", got)
	}
	if err := newCryptoScannerError("batch_commit", nil); err != nil {
		t.Fatalf("nil cause = %v", err)
	}
}

func TestCryptoScannerErrorReason(t *testing.T) {
	tests := map[string]struct {
		err  error
		want string
	}{
		"timeout":          {err: context.DeadlineExceeded, want: "timeout"},
		"rate limited":     {err: errors.New("RPC HTTP 429: quota exceeded"), want: "rate_limited"},
		"authentication":   {err: errors.New("RPC HTTP 403: denied"), want: "authentication"},
		"provider failure": {err: errors.New("RPC HTTP 503: unavailable"), want: "provider_http"},
		"RPC response":     {err: errors.New("RPC error: method disabled"), want: "rpc_response"},
		"invalid response": {err: errors.New("RPC returned no usable finalized head"), want: "invalid_response"},
		"configuration":    {err: errors.New("RPC endpoint is not configured"), want: "configuration"},
		"internal":         {err: errors.New("database write failed"), want: "internal"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if got := cryptoScannerErrorReason(test.err); got != test.want {
				t.Fatalf("reason = %q, want %q", got, test.want)
			}
		})
	}
}

func TestHexQuantityToDecimal(t *testing.T) {
	got, err := hexQuantityToDecimal("0xde0b6b3a7640000")
	if err != nil || got != "1000000000000000000" {
		t.Fatalf("hexQuantityToDecimal() = %q, %v", got, err)
	}
	if _, err := hexQuantityToDecimal("not-hex"); err == nil {
		t.Fatal("malformed quantity was accepted")
	}
}

func TestCryptoScannerStartBlock(t *testing.T) {
	tests := map[string]struct {
		buildEnv  string
		anchor    string
		safeHead  int64
		addresses int
		want      int64
		wantErr   string
	}{
		"explicit anchor wins":                     {buildEnv: "production", anchor: "1234", addresses: 3, want: 1234},
		"malformed anchor is refused":              {buildEnv: "production", anchor: "-5", wantErr: "must be a non-negative block number"},
		"production without addresses uses head":   {buildEnv: "production", want: 10_000},
		"production with addresses needs anchor":   {buildEnv: "production", addresses: 3, wantErr: "CRYPTO_SCAN_START_BLOCK_BASE is required in production: 3 deposit addresses exist on base"},
		"development without addresses uses head":  {buildEnv: "development", want: 10_000},
		"development with addresses looks back":    {buildEnv: "development", addresses: 3, want: 9_000},
		"development look-back is bounded at zero": {buildEnv: "development", safeHead: 500, addresses: 1, want: 0},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			appconfigtest.Set(t, "BUILD_ENV", test.buildEnv)
			appconfigtest.Set(t, "CRYPTO_SCAN_START_BLOCK_BASE", test.anchor)
			safeHead := test.safeHead
			if safeHead == 0 {
				safeHead = 10_000
			}
			got, err := cryptoScannerStartBlock("base", safeHead, test.addresses)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("err = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("start block = %d, %v; want %d", got, err, test.want)
			}
		})
	}
}

func TestValidateCryptoScannerStartOnlyRejectsMalformedAnchor(t *testing.T) {
	appconfigtest.Set(t, "BUILD_ENV", "production")
	appconfigtest.Set(t, "CRYPTO_SCAN_START_BLOCK_BASE_SEPOLIA", "")
	if err := ValidateCryptoScannerStart("base-sepolia"); err != nil {
		t.Fatalf("absent anchor refused: %v", err)
	}
	appconfigtest.Set(t, "CRYPTO_SCAN_START_BLOCK_BASE_SEPOLIA", "not-a-block")
	if err := ValidateCryptoScannerStart("base-sepolia"); err == nil {
		t.Fatal("malformed anchor accepted")
	}
}

// A production network that has never issued a deposit address gets its first
// cursor at the safe head without an explicit anchor.
func TestLoadOrCreateScanCursorDerivesStartWithoutIssuedAddresses(t *testing.T) {
	appconfigtest.Set(t, "BUILD_ENV", "production")
	appconfigtest.Set(t, "CRYPTO_SCAN_START_BLOCK_BASE_SEPOLIA", "")
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)
	cursorColumns := []string{"next_block", "last_scanned_block", "last_scanned_block_hash"}
	mock.ExpectQuery("FROM purser.crypto_scan_cursors").WithArgs("base-sepolia").
		WillReturnRows(sqlmock.NewRows(cursorColumns))
	mock.ExpectQuery("FROM purser.crypto_wallets").WithArgs("base-sepolia").
		WillReturnRows(sqlmock.NewRows([]string{"wallet_address"}))
	mock.ExpectExec("INSERT INTO purser.crypto_scan_cursors").
		WithArgs("base-sepolia", int64(7_000), int64(7_000)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("FROM purser.crypto_scan_cursors").WithArgs("base-sepolia").
		WillReturnRows(sqlmock.NewRows(cursorColumns).AddRow(int64(7_000), nil, nil))

	monitor := &CryptoMonitor{db: db}
	next, _, _, err := monitor.loadOrCreateScanCursor(context.Background(), Networks["base-sepolia"], 7_000)
	if err != nil {
		t.Fatalf("loadOrCreateScanCursor: %v", err)
	}
	if next != 7_000 {
		t.Fatalf("next block = %d, want the safe head 7000", next)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

type countingRPC struct {
	calls atomic.Int64
	fail  bool
}

func (c *countingRPC) serve(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		defer request.Body.Close()
		c.calls.Add(1)
		if c.fail {
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": "0x10"})
	}))
	t.Cleanup(server.Close)
	for _, network := range Networks {
		appconfigtest.Set(t, network.RPCEndpointEnv, server.URL)
	}
	return server
}

func logMessages(hook *logtest.Hook, level logrus.Level, message string) int {
	count := 0
	for _, entry := range hook.AllEntries() {
		if entry.Level == level && entry.Message == message {
			count++
		}
	}
	return count
}

// Without an HD wallet xpub no deposit address can exist: the scanner makes no
// RPC calls, records no scanner errors, and says so once.
func TestScanRPCDepositsIsInactiveWithoutHDWalletXpub(t *testing.T) {
	appconfigtest.Set(t, "BUILD_ENV", "production")
	rpc := &countingRPC{}
	rpc.serve(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for range 3 {
		mock.ExpectQuery("FROM purser.hd_wallet_state").WillReturnError(sql.ErrNoRows)
	}
	logger, hook := logtest.NewNullLogger()
	monitor := &CryptoMonitor{db: db, logger: logger, rpc: NewRPCClient(), includeTestnets: true}
	for range 3 {
		monitor.scanRPCDeposits(context.Background())
	}
	if got := rpc.calls.Load(); got != 0 {
		t.Fatalf("RPC calls = %d, want none while no deposit address can exist", got)
	}
	if got := logMessages(hook, logrus.WarnLevel, "Crypto RPC scan failed"); got != 0 {
		t.Fatalf("scan failure warnings = %d, want none", got)
	}
	if got := logMessages(hook, logrus.InfoLevel, "Crypto deposit scanner inactive"); got != 1 {
		t.Fatalf("inactive notices = %d, want exactly one", got)
	}
}

// A persistent per-network failure is logged once per network, not per tick,
// and recovery is logged once.
func TestScanRPCDepositsLogsPersistentFailureOnce(t *testing.T) {
	appconfigtest.Set(t, "BUILD_ENV", "production")
	rpc := &countingRPC{fail: true}
	rpc.serve(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for range 3 {
		mock.ExpectQuery("FROM purser.hd_wallet_state").
			WillReturnRows(sqlmock.NewRows([]string{"xpub"}).AddRow("xpub-test"))
	}
	logger, hook := logtest.NewNullLogger()
	monitor := &CryptoMonitor{db: db, logger: logger, rpc: NewRPCClient(), includeTestnets: true}
	for range 3 {
		monitor.scanRPCDeposits(context.Background())
	}
	if rpc.calls.Load() == 0 {
		t.Fatal("active scanner made no RPC calls")
	}
	networks := len(DepositNetworks(true))
	if got := logMessages(hook, logrus.WarnLevel, "Crypto RPC scan failed"); got != networks {
		t.Fatalf("scan failure warnings = %d over 3 ticks, want one per network (%d)", got, networks)
	}
	monitor.noteNetworkScanResult(context.Background(), "base", nil)
	monitor.noteNetworkScanResult(context.Background(), "base", nil)
	if got := logMessages(hook, logrus.InfoLevel, "Crypto RPC scan recovered"); got != 1 {
		t.Fatalf("recovery notices = %d, want one", got)
	}
}

func TestRPCBlockByNumberDecodesHashOnlyTransactions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		defer request.Body.Close()
		var payload struct {
			Params []any `json:"params"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if len(payload.Params) != 2 || payload.Params[1] != false {
			t.Fatalf("params = %#v, want header-only block request", payload.Params)
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"jsonrpc": "2.0", "id": 1,
			"result": map[string]any{
				"number":       "0x2a",
				"hash":         "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"transactions": []string{"0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
			},
		})
	}))
	defer server.Close()
	appconfigtest.Set(t, "BASE_RPC_ENDPOINT", server.URL)

	monitor := &CryptoMonitor{rpc: NewRPCClient()}
	block, err := monitor.rpcBlockByNumber(context.Background(), Networks["base"], 42, false)
	if err != nil {
		t.Fatalf("rpcBlockByNumber: %v", err)
	}
	if block.Number != "0x2a" || block.Hash == "" || len(block.Transactions) != 0 {
		t.Fatalf("header-only block = %+v", block)
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
	appconfigtest.Set(t, "BASE_SEPOLIA_RPC_ENDPOINT", server.URL)
	monitor := &CryptoMonitor{rpc: NewRPCClient()}
	network := NetworkConfig{Name: "test", RPCEndpointEnv: "BASE_SEPOLIA_RPC_ENDPOINT", USDCContract: "0x1111111111111111111111111111111111111111"}
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
	appconfigtest.Set(t, "BASE_SEPOLIA_RPC_ENDPOINT", server.URL)
	monitor := &CryptoMonitor{rpc: NewRPCClient()}
	network := NetworkConfig{Name: "test", RPCEndpointEnv: "BASE_SEPOLIA_RPC_ENDPOINT", USDCContract: "0x1111111111111111111111111111111111111111"}
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
	appconfigtest.Set(t, "BASE_SEPOLIA_RPC_ENDPOINT", server.URL)
	monitor := &CryptoMonitor{rpc: NewRPCClient()}
	network := NetworkConfig{Name: "test", RPCEndpointEnv: "BASE_SEPOLIA_RPC_ENDPOINT", USDCContract: "0x1111111111111111111111111111111111111111"}
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
