package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/ethereum/go-ethereum/crypto"
)

const durableBroadcastTestPrivateKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func durableBroadcastTestPayload() *X402PaymentPayload {
	return &X402PaymentPayload{Payload: &X402ExactPayload{
		Signature: "0x" + strings.Repeat("00", 65),
		Authorization: &X402Authorization{
			From: "0x1111111111111111111111111111111111111111", To: "0x2222222222222222222222222222222222222222",
			Value: "5000000", ValidAfter: "0", ValidBefore: "9999999999",
			Nonce: "0x" + strings.Repeat("aa", 32),
		},
	}}
}

func TestEmbeddedFacilitatorRebroadcastsIdenticalPreparedTransactionAfterUnknownOutcome(t *testing.T) { //nolint:funlen // Explicitly proves every durable transition around the ambiguous RPC boundary.
	const privateKey = durableBroadcastTestPrivateKey
	gasAddress, err := deriveAddressFromPrivKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	payload := &X402PaymentPayload{Payload: &X402ExactPayload{
		Signature: "0x" + strings.Repeat("00", 65),
		Authorization: &X402Authorization{
			From: "0x1111111111111111111111111111111111111111", To: "0x2222222222222222222222222222222222222222",
			Value: "5000000", ValidAfter: "0", ValidBefore: "9999999999",
			Nonce: "0x" + strings.Repeat("aa", 32),
		},
	}}
	callData, err := transferWithAuthorizationCallData(payload)
	if err != nil {
		t.Fatal(err)
	}

	network := Networks["base"]
	expectedRawHandler := &X402Handler{gasWalletPrivKey: privateKey}
	expectedRaw, err := expectedRawHandler.signDynamicFeeTransaction(
		7, network.USDCContract, big.NewInt(0), 150000,
		big.NewInt(3_000_000_000), big.NewInt(1_000_000_000), callData, big.NewInt(network.ChainID),
	)
	if err != nil {
		t.Fatal(err)
	}
	expectedHash := crypto.Keccak256Hash(expectedRaw).Hex()

	var mu sync.Mutex
	var broadcastRaw []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var rpcRequest struct {
			Method string `json:"method"`
			Params []any  `json:"params"`
		}
		if decodeErr := json.NewDecoder(request.Body).Decode(&rpcRequest); decodeErr != nil {
			http.Error(w, decodeErr.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		result := any("0x")
		switch rpcRequest.Method {
		case "eth_getTransactionCount":
			result = "0x5"
		case "eth_gasPrice", "eth_maxPriorityFeePerGas":
			result = "0x3b9aca00"
		case "eth_sendRawTransaction":
			raw, _ := rpcRequest.Params[0].(string)
			mu.Lock()
			broadcastRaw = append(broadcastRaw, raw)
			attempt := len(broadcastRaw)
			mu.Unlock()
			if attempt == 1 {
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":`))
				return
			}
			result = expectedHash
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
	}))
	defer server.Close()
	t.Setenv(network.RPCEndpointEnv, server.URL)

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	handler := &X402Handler{db: db, rpc: NewRPCClient(), gasWalletPrivKey: privateKey, gasWalletAddress: gasAddress}

	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT transaction_hash FROM purser.x402_settlement_attempts").
		WithArgs("settlement-1").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT COALESCE\\(MAX\\(relayer_nonce\\) \\+ 1, 0\\)").
		WithArgs(network.Name, gasAddress).
		WillReturnRows(sqlmock.NewRows([]string{"next_nonce"}).AddRow(int64(7)))
	mock.ExpectExec("INSERT INTO purser.x402_settlement_attempts").
		WithArgs("settlement-1", network.Name, network.ChainID, gasAddress, int64(7), expectedRaw,
			strings.ToLower(expectedHash), int64(150000), "3000000000", "1000000000").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE purser.x402_nonces SET tx_hash").
		WithArgs(expectedHash, "settlement-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery("SELECT signed_raw_transaction, transaction_hash").
		WithArgs("settlement-1").
		WillReturnRows(sqlmock.NewRows([]string{"signed_raw_transaction", "transaction_hash"}).AddRow(expectedRaw, expectedHash))
	mock.ExpectExec("UPDATE purser.x402_settlement_attempts").
		WithArgs("broadcast_unknown", sqlmock.AnyArg(), "settlement-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	txHash, err := handler.submitDurableTransferWithAuthorization(context.Background(), "settlement-1", payload, network)
	if err == nil || txHash != expectedHash {
		t.Fatalf("first broadcast = (%q, %v), want durable unknown outcome for %s", txHash, err, expectedHash)
	}

	mock.ExpectQuery("SELECT signed_raw_transaction, transaction_hash").
		WithArgs("settlement-1").
		WillReturnRows(sqlmock.NewRows([]string{"signed_raw_transaction", "transaction_hash"}).AddRow(expectedRaw, expectedHash))
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE purser.x402_settlement_attempts").
		WithArgs("settlement-1", expectedHash).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE purser.x402_nonces").
		WithArgs(expectedHash, "settlement-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE purser.x402_payment_quotes").
		WithArgs("settlement-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	retriedHash, err := handler.rebroadcastPreparedSettlementAttempt(context.Background(), "settlement-1", network)
	if err != nil || retriedHash != expectedHash {
		t.Fatalf("retry broadcast = (%q, %v), want %s", retriedHash, err, expectedHash)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(broadcastRaw) != 2 || broadcastRaw[0] != broadcastRaw[1] {
		t.Fatalf("broadcast transactions differ: %#v", broadcastRaw)
	}
	if broadcastRaw[0] != "0x"+strings.ToLower(fmt.Sprintf("%x", expectedRaw)) {
		t.Fatalf("broadcast raw transaction = %q, want prepared bytes", broadcastRaw[0])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestEmbeddedFacilitatorDoesNotBroadcastBeforePreparedAttemptCommits(t *testing.T) {
	payload := durableBroadcastTestPayload()
	network := Networks["base"]
	gasAddress, err := deriveAddressFromPrivKey(durableBroadcastTestPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	sendCalls := 0
	rpcServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var rpcRequest struct {
			Method string `json:"method"`
		}
		if decodeErr := json.NewDecoder(request.Body).Decode(&rpcRequest); decodeErr != nil {
			http.Error(w, decodeErr.Error(), http.StatusBadRequest)
			return
		}
		result := any("0x")
		switch rpcRequest.Method {
		case "eth_getTransactionCount":
			result = "0x5"
		case "eth_gasPrice", "eth_maxPriorityFeePerGas":
			result = "0x3b9aca00"
		case "eth_sendRawTransaction":
			sendCalls++
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
	}))
	defer rpcServer.Close()
	t.Setenv(network.RPCEndpointEnv, rpcServer.URL)

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT transaction_hash FROM purser.x402_settlement_attempts").
		WithArgs("settlement-db-fail").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT COALESCE\\(MAX\\(relayer_nonce\\) \\+ 1, 0\\)").
		WithArgs(network.Name, gasAddress).
		WillReturnRows(sqlmock.NewRows([]string{"next_nonce"}).AddRow(int64(0)))
	mock.ExpectExec("INSERT INTO purser.x402_settlement_attempts").
		WillReturnError(errors.New("database unavailable before prepared commit"))
	mock.ExpectRollback()

	handler := &X402Handler{db: db, rpc: NewRPCClient(), gasWalletPrivKey: durableBroadcastTestPrivateKey, gasWalletAddress: gasAddress}
	if _, err := handler.submitDurableTransferWithAuthorization(context.Background(), "settlement-db-fail", payload, network); err == nil {
		t.Fatal("settlement unexpectedly succeeded")
	}
	if sendCalls != 0 {
		t.Fatalf("eth_sendRawTransaction calls=%d, want 0 before durable commit", sendCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestEmbeddedFacilitatorReplaysAfterRPCAcceptanceAndDatabaseFailure(t *testing.T) { //nolint:funlen // The expectations define the crash boundary and exact replay contract.
	payload := durableBroadcastTestPayload()
	network := Networks["base"]
	gasAddress, err := deriveAddressFromPrivKey(durableBroadcastTestPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	callData, err := transferWithAuthorizationCallData(payload)
	if err != nil {
		t.Fatal(err)
	}
	expectedRaw, err := (&X402Handler{gasWalletPrivKey: durableBroadcastTestPrivateKey}).signDynamicFeeTransaction(
		5, network.USDCContract, big.NewInt(0), 150000,
		big.NewInt(3_000_000_000), big.NewInt(1_000_000_000), callData, big.NewInt(network.ChainID),
	)
	if err != nil {
		t.Fatal(err)
	}
	expectedHash := crypto.Keccak256Hash(expectedRaw).Hex()
	var sent []string
	rpcServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var rpcRequest struct {
			Method string `json:"method"`
			Params []any  `json:"params"`
		}
		if decodeErr := json.NewDecoder(request.Body).Decode(&rpcRequest); decodeErr != nil {
			http.Error(w, decodeErr.Error(), http.StatusBadRequest)
			return
		}
		result := any("0x")
		switch rpcRequest.Method {
		case "eth_getTransactionCount":
			result = "0x5"
		case "eth_gasPrice", "eth_maxPriorityFeePerGas":
			result = "0x3b9aca00"
		case "eth_sendRawTransaction":
			sent = append(sent, rpcRequest.Params[0].(string))
			result = expectedHash
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
	}))
	defer rpcServer.Close()
	t.Setenv(network.RPCEndpointEnv, rpcServer.URL)

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT transaction_hash FROM purser.x402_settlement_attempts").WithArgs("settlement-rpc-db-fail").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT COALESCE\\(MAX\\(relayer_nonce\\) \\+ 1, 0\\)").WithArgs(network.Name, gasAddress).
		WillReturnRows(sqlmock.NewRows([]string{"next_nonce"}).AddRow(int64(0)))
	mock.ExpectExec("INSERT INTO purser.x402_settlement_attempts").
		WithArgs("settlement-rpc-db-fail", network.Name, network.ChainID, gasAddress, int64(5), expectedRaw,
			strings.ToLower(expectedHash), int64(150000), "3000000000", "1000000000").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE purser.x402_nonces SET tx_hash").WithArgs(expectedHash, "settlement-rpc-db-fail").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery("SELECT signed_raw_transaction, transaction_hash").WithArgs("settlement-rpc-db-fail").
		WillReturnRows(sqlmock.NewRows([]string{"signed_raw_transaction", "transaction_hash"}).AddRow(expectedRaw, expectedHash))
	mock.ExpectBegin().WillReturnError(errors.New("database unavailable after RPC acceptance"))

	handler := &X402Handler{db: db, rpc: NewRPCClient(), gasWalletPrivKey: durableBroadcastTestPrivateKey, gasWalletAddress: gasAddress}
	firstHash, err := handler.submitDurableTransferWithAuthorization(context.Background(), "settlement-rpc-db-fail", payload, network)
	if err == nil || firstHash != expectedHash {
		t.Fatalf("first attempt=(%q,%v), want durable hash and database error", firstHash, err)
	}

	mock.ExpectQuery("SELECT signed_raw_transaction, transaction_hash").WithArgs("settlement-rpc-db-fail").
		WillReturnRows(sqlmock.NewRows([]string{"signed_raw_transaction", "transaction_hash"}).AddRow(expectedRaw, expectedHash))
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE purser.x402_settlement_attempts").WithArgs("settlement-rpc-db-fail", expectedHash).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE purser.x402_nonces").WithArgs(expectedHash, "settlement-rpc-db-fail").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE purser.x402_payment_quotes").WithArgs("settlement-rpc-db-fail").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	retriedHash, err := handler.rebroadcastPreparedSettlementAttempt(context.Background(), "settlement-rpc-db-fail", network)
	if err != nil || retriedHash != expectedHash {
		t.Fatalf("retry=(%q,%v), want %s", retriedHash, err, expectedHash)
	}
	if len(sent) != 2 || sent[0] != sent[1] {
		t.Fatalf("RPC replay was not byte-identical: %#v", sent)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestEmbeddedFacilitatorRejectsMalformedRPCQuantities(t *testing.T) {
	responses := map[string]string{
		"eth_getTransactionCount":  "not-hex",
		"eth_gasPrice":             "0x",
		"eth_maxPriorityFeePerGas": "-1",
		"eth_call":                 "garbage",
	}
	rpcServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var rpcRequest struct {
			Method string `json:"method"`
		}
		if decodeErr := json.NewDecoder(request.Body).Decode(&rpcRequest); decodeErr != nil {
			http.Error(w, decodeErr.Error(), http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": 1, "result": responses[rpcRequest.Method],
		})
	}))
	defer rpcServer.Close()
	network := Networks["base"]
	t.Setenv(network.RPCEndpointEnv, rpcServer.URL)
	handler := &X402Handler{rpc: NewRPCClient()}

	if _, err := handler.getNonce(context.Background(), network, "0x1111111111111111111111111111111111111111"); err == nil {
		t.Fatal("malformed nonce unexpectedly accepted")
	}
	if _, err := handler.getGasPrice(context.Background(), network); err == nil {
		t.Fatal("malformed gas price unexpectedly accepted")
	}
	if _, err := handler.getPriorityFee(context.Background(), network); err == nil {
		t.Fatal("malformed priority fee unexpectedly accepted")
	}
	if _, err := handler.getUSDCBalance(context.Background(), network, "0x1111111111111111111111111111111111111111"); err == nil {
		t.Fatal("malformed USDC balance unexpectedly accepted")
	}
}
