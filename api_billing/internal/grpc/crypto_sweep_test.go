package grpc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"frameworks/api_billing/internal/handlers"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/cryptosweep"
)

func newSweepRPCTestServer(t *testing.T, balance, nonce string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		result := balance
		if request.Method == "eth_getTransactionCount" {
			result = nonce
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
	}))
}

func TestRecheckETHSweepRejectsChangedBalanceAndNonce(t *testing.T) {
	item := cryptosweep.ManifestItem{
		Asset: "ETH", SourceAddress: "0x1111111111111111111111111111111111111111",
		AmountBaseUnits: "50", SourceNonce: 1,
	}
	t.Run("balance", func(t *testing.T) {
		server := newSweepRPCTestServer(t, "0x20", "0x1")
		defer server.Close()
		t.Setenv("BASE_RPC_ENDPOINT", server.URL)
		purser := &PurserServer{rpcClient: handlers.NewRPCClient()}
		if err := purser.recheckSweepItemBeforeBroadcast(context.Background(), handlers.Networks["base"], item); err == nil || !strings.Contains(err.Error(), "balance") {
			t.Fatalf("expected changed balance rejection, got %v", err)
		}
	})
	t.Run("nonce", func(t *testing.T) {
		server := newSweepRPCTestServer(t, "0x100", "0x2")
		defer server.Close()
		t.Setenv("BASE_RPC_ENDPOINT", server.URL)
		purser := &PurserServer{rpcClient: handlers.NewRPCClient()}
		if err := purser.recheckSweepItemBeforeBroadcast(context.Background(), handlers.Networks["base"], item); err == nil || !strings.Contains(err.Error(), "nonce") {
			t.Fatalf("expected changed nonce rejection, got %v", err)
		}
	})
}

func TestParseSweepHexRejectsMalformedRPCQuantities(t *testing.T) {
	for _, value := range []string{"", "0x", "7", "-0x1", "0xnope"} {
		if _, err := parseSweepHex(value); err == nil {
			t.Fatalf("parseSweepHex(%q) unexpectedly succeeded", value)
		}
	}
	if value, err := parseSweepHex("0x2a"); err != nil || value.Int64() != 42 {
		t.Fatalf("valid quantity = %v, %v", value, err)
	}
}

func TestReserveRelayerTransactionReplaysPersistedIntent(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	t.Setenv("CRYPTO_SWEEP_RELAYER_PRIVATE_KEY_BASE", strings.Repeat("1", 64))

	server := &PurserServer{db: db}
	item := cryptosweep.ManifestItem{
		ItemID: "item-1", AssetContract: "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913",
		SourceAddress:      "0x1111111111111111111111111111111111111111",
		DestinationAddress: "0x2222222222222222222222222222222222222222",
		AmountBaseUnits:    "1000000", AuthorizationNonce: "0x" + strings.Repeat("3", 64),
		AuthorizationAfter: 1, AuthorizationBefore: 2, GasLimit: 150000,
	}
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT relay_transaction, tx_hash, status[\s\S]*FOR UPDATE`).
		WithArgs("item-1").
		WillReturnRows(sqlmock.NewRows([]string{"relay_transaction", "tx_hash", "status"}).
			AddRow("0xexistingraw", "0xexistinghash", "broadcast"))
	mock.ExpectCommit()

	raw, hash, err := server.reserveRelayerTransaction(
		context.Background(), handlers.Networks["base"], item, "0x"+strings.Repeat("0", 130),
	)
	if err != nil {
		t.Fatalf("reserveRelayerTransaction: %v", err)
	}
	if raw != "0xexistingraw" || hash != "0xexistinghash" {
		t.Fatalf("expected persisted relay intent, got raw=%q hash=%q", raw, hash)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
