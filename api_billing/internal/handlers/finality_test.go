package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGetFinalityHeadUsesFinalizedTag(t *testing.T) {
	var requestedTag string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Params []any `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		requestedTag, _ = request.Params[0].(string)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": 1,
			"result": map[string]any{
				"number": "0x2a", "hash": "0x" + strings.Repeat("a", 64),
				"transactions": []string{"0x" + strings.Repeat("b", 64)},
			},
		})
	}))
	defer server.Close()
	t.Setenv("BASE_RPC_ENDPOINT", server.URL)

	head, err := GetFinalityHead(context.Background(), NewRPCClient(), Networks["base"])
	if err != nil {
		t.Fatalf("GetFinalityHead: %v", err)
	}
	if requestedTag != "finalized" || head.Tag != "finalized" || head.Number != 42 {
		t.Fatalf("unexpected finalized head: request=%q head=%+v", requestedTag, head)
	}
}

func TestValidateCryptoCustodyNetworkAcceptsNonContractTreasury(t *testing.T) {
	var requestedMethod string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		requestedMethod = request.Method
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": 1,
			"result": map[string]any{"number": "0x2a", "hash": "0x" + strings.Repeat("a", 64)},
		})
	}))
	defer server.Close()
	t.Setenv("BASE_RPC_ENDPOINT", server.URL)
	t.Setenv("CRYPTO_TREASURY_BASE", "0x1111111111111111111111111111111111111111")

	if err := ValidateCryptoCustodyNetwork(context.Background(), NewRPCClient(), Networks["base"], "ETH"); err != nil {
		t.Fatalf("ValidateCryptoCustodyNetwork: %v", err)
	}
	if requestedMethod != "eth_getBlockByNumber" {
		t.Fatalf("unexpected treasury validation RPC method %q", requestedMethod)
	}
}

func TestGetFinalityHeadFailsClosedWhenUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": nil})
	}))
	defer server.Close()
	t.Setenv("BASE_RPC_ENDPOINT", server.URL)

	if _, err := GetFinalityHead(context.Background(), NewRPCClient(), Networks["base"]); err == nil {
		t.Fatal("expected unavailable finalized head to fail closed")
	}
}
